package vminstance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The PIDs the fixtures run under. They are the store's liveness seam's input,
// not real processes: a test names which of them is running, which is how a live
// VM and a crashed VM are both described without spawning or killing anything.
const (
	livePID      = 4101
	otherLivePID = 4102
	deadPID      = 4199
)

// pidSet is a liveliness oracle a test controls: a PID is running exactly when
// the test says so.
type pidSet map[int]bool

func (p pidSet) alive(pid int) bool { return p[pid] }

// newTestStore opens a registry over a fresh directory with both fixture PIDs
// running, and returns the set so a test can kill one.
func newTestStore(t *testing.T) (*Store, pidSet, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), DirName)
	pids := pidSet{livePID: true, otherLivePID: true}
	store, err := NewStore(Config{Dir: dir, Alive: pids.alive})
	require.NoError(t, err)
	return store, pids, dir
}

// recordNames returns the entry names in the registry directory, so a test can
// assert that a write left no temporary behind.
func recordNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// writeFile writes a file verbatim into the registry directory, for the records
// a test has to produce without going through Put.
func writeFile(t *testing.T, path, body string) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

func TestDir(t *testing.T) {
	cases := []struct {
		name     string
		stateDir string
		want     string
	}{
		{name: "the daemon's state directory", stateDir: "/var/lib/swarmkit", want: "/var/lib/swarmkit/" + DirName},
		{name: "a trailing separator is cleaned", stateDir: "/var/lib/swarmkit/", want: "/var/lib/swarmkit/" + DirName},
		{name: "a relative state directory keeps its shape", stateDir: "state", want: "state/" + DirName},
		{name: "no state directory", stateDir: "", want: ""},
		{name: "blank state directory", stateDir: "   ", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Dir(tc.stateDir))
		})
	}
}

func TestNewStore(t *testing.T) {
	t.Run("creates the directory 0700", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", DirName)
		store, err := NewStore(Config{Dir: dir})
		require.NoError(t, err)
		require.NotNil(t, store)

		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
			"the records name host paths and are readable only by the daemon")
		assert.Equal(t, dir, store.Dir())
	})

	t.Run("an existing directory is reused", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), DirName)
		first, err := NewStore(Config{Dir: dir})
		require.NoError(t, err)

		again, err := NewStore(Config{Dir: dir})
		require.NoError(t, err, "a daemon restart must open the same directory again")
		assert.Equal(t, first.Dir(), again.Dir())
	})

	t.Run("an empty directory is refused", func(t *testing.T) {
		store, err := NewStore(Config{Dir: " "})
		require.Error(t, err)
		assert.Nil(t, store)
		assert.Contains(t, err.Error(), "directory is required")
	})

	t.Run("a file in the way is reported", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), DirName)
		writeFile(t, path, "not a directory")

		store, err := NewStore(Config{Dir: path})
		require.Error(t, err)
		assert.Nil(t, store)
		assert.Contains(t, err.Error(), path)
	})

	t.Run("the probe leaves no file behind", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), DirName)
		_, err := NewStore(Config{Dir: dir})
		require.NoError(t, err)

		assert.Empty(t, recordNames(t, dir), "opening the registry leaves no residue a human must explain")
	})

	// Opening a directory is not the same as being able to write a record into
	// it: MkdirAll succeeds on an existing directory whatever its mode, and Put
	// runs while a VM is starting. A node that cannot write the record must
	// refuse to start, or it runs VMs that nothing can resolve after a restart.
	t.Run("a read-only directory is refused", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), DirName)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		store, err := NewStore(Config{Dir: dir})
		if err == nil {
			if os.Geteuid() == 0 {
				t.Skip("running as root: a 0500 directory is still writable, so the probe cannot fail")
			}
			t.Fatalf("opening %s with no write permission reported no error", dir)
		}
		assert.Nil(t, store)
		assert.Contains(t, err.Error(), dir, "the error must name the directory that cannot hold records")
		assert.Contains(t, err.Error(), "not writable")
	})
}

func TestPutGetRoundTrip(t *testing.T) {
	store, _, dir := newTestStore(t)
	want := instanceFixture("sbx-1", livePID)

	require.NoError(t, store.Put(want))

	got, found, err := store.Get(want.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, want, got, "every host path must come back as it was written, not re-derived")

	// One record per instance, named after the instance, so a human can find it.
	assert.Equal(t, []string{"sbx-1" + recordSuffix}, recordNames(t, dir),
		"a successful Put leaves exactly one file, no temporary remainder")

	info, err := os.Stat(recordPath(dir, want.ID))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"a record names host paths and is readable only by the daemon")
}

func TestPutReplacesTheRecord(t *testing.T) {
	store, _, dir := newTestStore(t)
	first := instanceFixture("sbx-1", livePID)
	require.NoError(t, store.Put(first))

	// The second recording is what a boot completion looks like: the same VM,
	// now ready, with the address it was assigned after the record existed.
	second := first
	second.Ready = false
	second.GuestIP = "10.62.0.9"
	second.VsockPath = "/var/run/firecracker/sbx-1-restored.vsock"
	require.NoError(t, store.Put(second))

	got, found, err := store.Get(first.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, second, got, "Put is a replace: the last recording wins")
	assert.Len(t, recordNames(t, dir), 1, "a replace must not leave the previous record or its temporary")
}

func TestPutRefusesAnUnusableRecord(t *testing.T) {
	store, _, dir := newTestStore(t)
	inst := instanceFixture("sbx-1", livePID)
	inst.APISocket = "relative.sock"

	err := store.Put(inst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apiSocket")
	assert.Empty(t, recordNames(t, dir), "a refused record must leave nothing on disk")
}

// TestPutRefusesADeadProcess pins the registry's central promise: a record is
// only ever written for a VM whose process exists, so no reader can be handed a
// record that resolves to a dead PID.
func TestPutRefusesADeadProcess(t *testing.T) {
	store, _, dir := newTestStore(t)

	err := store.Put(instanceFixture("sbx-1", deadPID))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not running")
	assert.Empty(t, recordNames(t, dir), "a record with a dead process must not reach the disk")
}

// TestPutLeavesNoTemporaryWhenItCannotPublish pins the crash-safety half of the
// atomic write: a rename that fails must not leave the half-written record in
// the directory for a later reader to trip over.
func TestPutLeavesNoTemporaryWhenItCannotPublish(t *testing.T) {
	store, _, dir := newTestStore(t)
	// Something other than a file is in the record's place, so the rename into
	// it cannot succeed.
	require.NoError(t, os.Mkdir(recordPath(dir, "sbx-1"), 0o700))

	err := store.Put(instanceFixture("sbx-1", livePID))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rename")

	assert.Equal(t, []string{"sbx-1" + recordSuffix}, recordNames(t, dir),
		"the temporary is removed, so the failure leaves nothing that looks like a record")
}

func TestGetMissingInstance(t *testing.T) {
	store, _, _ := newTestStore(t)

	got, found, err := store.Get("sbx-none")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, Instance{}, got)
}

func TestGetRefusesAnIDThatCannotNameAFile(t *testing.T) {
	store, _, _ := newTestStore(t)

	for _, id := range []string{"", "../escape", "a.b", "a b"} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			_, found, err := store.Get(id)
			require.Error(t, err)
			assert.False(t, found)
		})
	}
}

func TestGetRefusesAnUnusableRecord(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "not JSON", body: "{not a record", want: "is corrupt"},
		{
			name: "another instance's record",
			body: `{"id":"sbx-other","kind":"sandbox","pid":4101,"apiSocket":"/run/sbx-other.sock","booted":"2026-11-02T09:30:00Z"}`,
			want: `belongs to "sbx-other"`,
		},
		{
			name: "a record that describes no VM",
			body: `{"id":"sbx-1","kind":"sandbox","pid":0,"apiSocket":"/run/sbx-1.sock","booted":"2026-11-02T09:30:00Z"}`,
			want: "is not valid",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _, dir := newTestStore(t)
			path := recordPath(dir, "sbx-1")
			writeFile(t, path, tc.body)

			got, found, err := store.Get("sbx-1")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.False(t, found)
			assert.Equal(t, Instance{}, got)

			// The file is the only evidence of what the node believes, so a
			// reader leaves it for a human instead of deleting it.
			_, statErr := os.Stat(path)
			assert.NoError(t, statErr, "an unusable record is left in place for inspection")
		})
	}
}

// TestGetReportsAnUnreadableRecord pins that a record the node cannot read is an
// error and not "no such instance": telling a caller the VM is not there when
// the file merely cannot be opened would send it to the legacy derivation while
// the VM is recorded and running.
func TestGetReportsAnUnreadableRecord(t *testing.T) {
	store, _, dir := newTestStore(t)
	inst := instanceFixture("sbx-1", livePID)
	require.NoError(t, store.Put(inst))

	path := recordPath(dir, inst.ID)
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, found, err := store.Get(inst.ID)
	if err == nil {
		if os.Geteuid() == 0 {
			t.Skip("running as root: a 0000 file is still readable, so the read cannot fail")
		}
		t.Fatalf("reading %s with no read permission reported no error", path)
	}
	assert.False(t, found)
	assert.Contains(t, err.Error(), path)
}

// TestGetRefusesAStaleRecord pins the other half of the promise: a VM that died
// after its record was written stops resolving immediately, without waiting for
// a daemon restart or a recovery pass.
func TestGetRefusesAStaleRecord(t *testing.T) {
	store, pids, dir := newTestStore(t)
	inst := instanceFixture("sbx-1", livePID)
	require.NoError(t, store.Put(inst))

	delete(pids, livePID)

	got, found, err := store.Get(inst.ID)
	require.NoError(t, err, "a stale record is not an error: it is an instance that is no longer there")
	assert.False(t, found)
	assert.Equal(t, Instance{}, got)

	instances, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, instances, "a listing must not offer a VM that is gone")
	assert.Empty(t, skipped, "a stale record is usable, so it is not a skipped path")
	_, statErr := os.Stat(recordPath(dir, inst.ID))
	assert.NoError(t, statErr, "a stale record is the only handle to what the dead VM left behind")
}

// TestGetIgnoresAHalfWrittenRecord pins what makes the atomic write visible to a
// reader: a temporary that a crash left behind is not a record, even when its
// contents would be one.
func TestGetIgnoresAHalfWrittenRecord(t *testing.T) {
	store, _, dir := newTestStore(t)
	inst := instanceFixture("sbx-1", livePID)

	half := tempPrefix + inst.ID + "-1234" + tempSuffix
	data, err := json.Marshal(inst)
	require.NoError(t, err)
	writeFile(t, filepath.Join(dir, half), string(data))

	_, found, err := store.Get(inst.ID)
	require.NoError(t, err)
	assert.False(t, found, "a record mid-write must not resolve")

	instances, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, instances)
	assert.Empty(t, skipped, "a temporary is not a record, so it is not reported as an unusable one")
}

func TestList(t *testing.T) {
	store, pids, dir := newTestStore(t)

	for _, id := range []string{"sbx-b", "sbx-a", "sbx-c"} {
		require.NoError(t, store.Put(instanceFixture(id, livePID)))
	}
	// sbx-c's VM dies; sbx-b's record becomes unusable.
	delete(pids, livePID)
	require.NoError(t, store.Put(instanceFixture("sbx-d", otherLivePID)))
	writeFile(t, recordPath(dir, "sbx-b"), "{corrupt")

	instances, skipped, err := store.List()
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "sbx-d", instances[0].ID, "only live, usable records are listed")
	assert.Equal(t, []string{recordPath(dir, "sbx-b")}, skipped)
}

func TestListOnAnEmptyRegistry(t *testing.T) {
	store, _, _ := newTestStore(t)

	instances, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, instances)
	assert.Empty(t, skipped)
}

func TestDelete(t *testing.T) {
	store, _, dir := newTestStore(t)
	require.NoError(t, store.Put(instanceFixture("sbx-1", livePID)))
	require.NoError(t, store.Put(instanceFixture("sbx-2", otherLivePID)))

	require.NoError(t, store.Delete("sbx-1"))

	_, found, err := store.Get("sbx-1")
	require.NoError(t, err)
	assert.False(t, found)
	_, found, err = store.Get("sbx-2")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []string{"sbx-2" + recordSuffix}, recordNames(t, dir))
}

func TestDeleteIsIdempotent(t *testing.T) {
	store, _, _ := newTestStore(t)

	assert.NoError(t, store.Delete("sbx-never-recorded"),
		"a stop, a retried stop and a recovery all delete, and none may fail on a missing record")
}

func TestDeleteRemovesACorruptRecord(t *testing.T) {
	store, _, dir := newTestStore(t)
	writeFile(t, recordPath(dir, "sbx-1"), "{corrupt")

	require.NoError(t, store.Delete("sbx-1"), "a bad file must not wedge a stop")
	assert.Empty(t, recordNames(t, dir))
}

func TestDeleteRefusesAnIDThatCannotNameAFile(t *testing.T) {
	store, _, _ := newTestStore(t)

	assert.Error(t, store.Delete("../escape"))
}

func TestRecover(t *testing.T) {
	store, pids, dir := newTestStore(t)
	require.NoError(t, store.Put(instanceFixture("sbx-live", livePID)))
	require.NoError(t, store.Put(instanceFixture("sbx-dead", otherLivePID)))
	writeFile(t, recordPath(dir, "sbx-bad"), "{corrupt")

	// The dead VM's record only becomes stale once its process is gone.
	delete(pids, otherLivePID)
	// A crash mid-Put leaves a temporary with no identity behind it.
	writeFile(t, filepath.Join(dir, tempPrefix+"sbx-gone-1"+tempSuffix), `{"id":"sbx-gone"}`)

	report, err := store.Recover()
	require.NoError(t, err)

	require.Len(t, report.Stale, 1)
	assert.Equal(t, instanceFixture("sbx-dead", otherLivePID), report.Stale[0],
		"the report carries the whole record: its netns, TAP device and disk are the only handle to what the dead VM left behind, and a caller must not have to re-derive them")
	assert.Equal(t, []string{recordPath(dir, "sbx-bad")}, report.Skipped)
	assert.Equal(t, 1, report.Temporaries)

	assert.Equal(t, []string{"sbx-bad" + recordSuffix, "sbx-dead" + recordSuffix, "sbx-live" + recordSuffix},
		recordNames(t, dir),
		"recovery removes only the half-written record: the stale one is the only handle to its resources, and the unusable one is evidence")

	// The live VM is untouched and still resolves.
	_, found, err := store.Get("sbx-live")
	require.NoError(t, err)
	assert.True(t, found)

	// Recovery is idempotent: a second pass has nothing to remove and still
	// reports the VM that is gone until its resources are torn down.
	again, err := store.Recover()
	require.NoError(t, err)
	assert.Equal(t, report.Stale, again.Stale)
	assert.Equal(t, report.Skipped, again.Skipped)
	assert.Zero(t, again.Temporaries)
}

func TestRecoverOnAnEmptyRegistry(t *testing.T) {
	store, _, _ := newTestStore(t)

	report, err := store.Recover()
	require.NoError(t, err)
	assert.Empty(t, report.Stale)
	assert.Empty(t, report.Skipped)
	assert.Zero(t, report.Temporaries)
}

// TestZeroStore pins that a Store built as a literal, or a nil one, refuses
// every operation instead of addressing the working directory.
func TestZeroStore(t *testing.T) {
	var zero Store

	assert.Error(t, zero.Put(instanceFixture("sbx-1", livePID)))
	_, _, err := zero.Get("sbx-1")
	assert.Error(t, err)
	_, _, err = zero.List()
	assert.Error(t, err)
	assert.Error(t, zero.Delete("sbx-1"))
	_, err = zero.Recover()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "use NewStore")
}

// TestConcurrentPutsNeverExposeAPartialRecord exercises the atomic write the way
// a busy node does: writers keep replacing one record while a reader keeps
// asking for it. A write in place, or temporaries shared between writers, would
// let the reader decode a fragment or find nothing at all.
func TestConcurrentPutsNeverExposeAPartialRecord(t *testing.T) {
	store, _, _ := newTestStore(t)
	const id = "sbx-race"
	seed := instanceFixture(id, livePID)
	require.NoError(t, store.Put(seed))

	const writers, rounds = 4, 40
	failures := make(chan error, writers+1)

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				inst := seed
				inst.GuestIP = fmt.Sprintf("10.62.1.%d", w*rounds+r+1)
				inst.Ready = r%2 == 0
				if err := store.Put(inst); err != nil {
					failures <- fmt.Errorf("writer %d: %w", w, err)
					return
				}
			}
		}(w)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for r := 0; r < writers*rounds; r++ {
			inst, found, err := store.Get(id)
			if err != nil {
				failures <- fmt.Errorf("read %d: %w", r, err)
				return
			}
			if !found {
				failures <- errors.New("the record was missing while it was being replaced")
				return
			}
			if inst.ID != seed.ID || inst.PID != seed.PID || inst.APISocket != seed.APISocket {
				failures <- fmt.Errorf("read %d returned a record from another instance: %+v", r, inst)
				return
			}
		}
	}()

	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	instances, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, instances, 1)
	assert.Equal(t, id, instances[0].ID)
}
