package vminstance

// The teardown half of the registry (SBX-015): a record is deleted only after the
// resources it names are gone, and a record whose VM is still running is refused.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The store satisfies the registry a VMM manager is given.
var _ Registry = (*Store)(nil)

// teardownStore is a registry whose liveness answer the test controls: a record
// put while alive describes a running VM, and flipping alive makes every record
// stale without killing anything.
type teardownStore struct {
	*Store
	alive bool
}

func newTeardownStore(t *testing.T) *teardownStore {
	t.Helper()

	store := &teardownStore{alive: true}
	real, err := NewStore(Config{
		Dir: filepath.Join(t.TempDir(), DirName),
		Alive: func(int) bool {
			return store.alive
		},
	})
	require.NoError(t, err)
	store.Store = real
	return store
}

// putDiskRecord writes a record for a VM that owns a TAP device, a namespace and a
// disk, all inside the test's temporary directory, so a release can be asserted
// against real files.
func putDiskRecord(t *testing.T, store *teardownStore, id string) Instance {
	t.Helper()

	dir := t.TempDir()
	disk := filepath.Join(dir, id+".ext4")
	require.NoError(t, os.WriteFile(disk, []byte("rootfs"), 0o600))

	inst := Instance{
		ID:        id,
		Kind:      KindSandbox,
		PID:       os.Getpid(),
		APISocket: filepath.Join(dir, id+".sock"),
		TAP:       "tap-" + id,
		NetNS:     filepath.Join(dir, "ns-"+id),
		Disk:      disk,
		Booted:    time.Now().UTC(),
	}
	require.NoError(t, store.Put(inst))
	return inst
}

// recordingLeftovers records which resources a release was asked to free, and
// answers from the test's scripted failures.
type recordingLeftovers struct {
	taps   []string
	netns  []string
	disks  []string
	failOn string
}

func (r *recordingLeftovers) leftovers() Leftovers {
	return Leftovers{
		DeleteTap: func(name string) error {
			r.taps = append(r.taps, name)
			if r.failOn == "tap" {
				return errors.New("device is busy")
			}
			return nil
		},
		DeleteNetNS: func(name string) error {
			r.netns = append(r.netns, name)
			if r.failOn == "netns" {
				return errors.New("namespace is busy")
			}
			return nil
		},
		DeleteDisk: func(path string) error {
			r.disks = append(r.disks, path)
			if r.failOn == "disk" {
				return errors.New("disk is busy")
			}
			return os.Remove(path)
		},
	}
}

// TestTeardownFreesTheRecordAndDeletesIt pins the whole path for the case the
// record exists for: a VM that is gone, and the record as the only handle to what
// it left behind.
func TestTeardownFreesTheRecordAndDeletesIt(t *testing.T) {
	store := newTeardownStore(t)
	inst := putDiskRecord(t, store, "sbx-gone")
	store.alive = false

	recorder := &recordingLeftovers{}
	found, err := Teardown(store, inst.ID, recorder.leftovers())
	require.NoError(t, err)
	require.True(t, found)

	assert.Equal(t, []string{"tap-" + inst.ID}, recorder.taps)
	assert.Equal(t, []string{inst.NetNS}, recorder.netns)
	assert.Equal(t, []string{inst.Disk}, recorder.disks)
	_, statErr := os.Stat(inst.Disk)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "the disk the record named is freed")

	_, stillThere, err := store.Peek(inst.ID)
	require.NoError(t, err)
	assert.False(t, stillThere, "the record is deleted once its resources are free")
}

// TestTeardownKeepsTheRecordWhenAReleaseFails pins the order: the record is the
// only thing that knows the names, so a release that fails keeps it and says what
// is still held. A retry then has the same handle.
func TestTeardownKeepsTheRecordWhenAReleaseFails(t *testing.T) {
	store := newTeardownStore(t)
	inst := putDiskRecord(t, store, "sbx-stuck")
	store.alive = false

	failing := &recordingLeftovers{failOn: "disk"}
	found, err := Teardown(store, inst.ID, failing.leftovers())
	require.True(t, found)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk", "the error names the resource that is still held")

	_, kept, peekErr := store.Peek(inst.ID)
	require.NoError(t, peekErr)
	assert.True(t, kept, "a record whose resources are not all free must be kept")

	// The retry frees the same record and deletes it.
	retry := &recordingLeftovers{}
	found, err = Teardown(store, inst.ID, retry.leftovers())
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, []string{inst.Disk}, retry.disks)

	_, gone, peekErr := store.Peek(inst.ID)
	require.NoError(t, peekErr)
	assert.False(t, gone)
}

// TestTeardownRefusesALiveVM pins the guard: a record that still names a running
// process must not have its devices freed, and the record must stay.
func TestTeardownRefusesALiveVM(t *testing.T) {
	store := newTeardownStore(t)
	inst := putDiskRecord(t, store, "sbx-live")

	recorder := &recordingLeftovers{}
	found, err := Teardown(store, inst.ID, recorder.leftovers())
	require.False(t, found)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still running")
	assert.Empty(t, recorder.taps)
	assert.Empty(t, recorder.disks)

	_, kept, peekErr := store.Peek(inst.ID)
	require.NoError(t, peekErr)
	assert.True(t, kept)
}

// TestTeardownWithoutARecordIsSuccess pins the retry shape: a VM that never got as
// far as a record, and a teardown that already ran, both find nothing to do.
func TestTeardownWithoutARecordIsSuccess(t *testing.T) {
	store := newTeardownStore(t)

	found, err := Teardown(store, "sbx-never-recorded", Leftovers{})
	require.NoError(t, err)
	assert.False(t, found)

	// No registry at all is the same answer: a caller that keeps none has nothing
	// to tear down.
	found, err = Teardown(nil, "sbx-never-recorded", Leftovers{})
	require.NoError(t, err)
	assert.False(t, found)
}

// TestReleaseSkipsWhatTheRecordDoesNotName pins that a VM on the shared bridge
// with a shared disk frees nothing: the paths are optional, and a release must not
// invent them.
func TestReleaseSkipsWhatTheRecordDoesNotName(t *testing.T) {
	recorder := &recordingLeftovers{}

	require.NoError(t, recorder.leftovers().Release(Instance{ID: "sbx-1"}))
	assert.Empty(t, recorder.taps)
	assert.Empty(t, recorder.netns)
	assert.Empty(t, recorder.disks)
}

// TestPeekReadsAStaleRecord pins the read a teardown needs and the one every other
// reader must not have: Get refuses a record whose process is gone, Peek returns
// it because it is the handle to the resources still to be freed.
func TestPeekReadsAStaleRecord(t *testing.T) {
	store := newTeardownStore(t)
	inst := putDiskRecord(t, store, "sbx-stale")
	store.alive = false

	_, live, err := store.Get(inst.ID)
	require.NoError(t, err)
	assert.False(t, live, "a dead VM must not resolve")

	peeked, found, err := store.Peek(inst.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, inst.Disk, peeked.Disk)
}

// TestReleaseTreatsAMissingDiskAsSuccess pins the disk remover's idempotency: a
// teardown that runs twice finds the file gone and must not report a failure for
// it, or the record would never be deleted.
func TestReleaseTreatsAMissingDiskAsSuccess(t *testing.T) {
	store := newTeardownStore(t)
	inst := putDiskRecord(t, store, "sbx-twice")
	store.alive = false

	// The default removers: only the disk path exists, so the TAP and netns legs
	// are not exercised here (they are covered with injected removers above).
	require.NoError(t, Leftovers{}.Release(Instance{ID: inst.ID, Disk: inst.Disk}))
	require.NoError(t, Leftovers{}.Release(Instance{ID: inst.ID, Disk: inst.Disk}))
}
