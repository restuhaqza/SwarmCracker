package agent

// The `quiesce` operation, from both sides: what the guest does to the filesystems,
// what the wire carries, and what a caller holds after a refusal.
//
// The real operation blocks this process's filesystem, so every test replaces the
// three seams it uses — the mount table, the sync and the two ioctls — with fakes
// that record the order they were called in. The order is the point: a freeze that
// skipped the sync would leave the checkpoint's disk image missing pages that the
// memory image still holds, which is the disagreement the operation exists to avoid.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// withQuiesceSeams points the operation at a fixture mount table and replaces its
// kernel-facing seams. It returns the operations the guest carried out, in order, as
// "sync", "freeze <mount>" and "thaw <mount>".
func withQuiesceSeams(t *testing.T, table string, refuse map[string]error) *[]string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "mounts")
	require.NoError(t, os.WriteFile(path, []byte(table), 0o644))

	var calls []string
	originalMounts, originalSync := guestMounts, syncFilesystems
	originalFreeze, originalThaw := freezeFilesystem, thawFilesystem
	guestMounts = path
	syncFilesystems = func() { calls = append(calls, "sync") }
	freezeFilesystem = func(mount string) (bool, error) {
		if err := refuse[mount]; err != nil {
			return false, err
		}
		calls = append(calls, "freeze "+mount)
		return false, nil
	}
	thawFilesystem = func(mount string) (bool, error) {
		calls = append(calls, "thaw "+mount)
		return false, nil
	}
	t.Cleanup(func() {
		guestMounts, syncFilesystems = originalMounts, originalSync
		freezeFilesystem, thawFilesystem = originalFreeze, originalThaw
	})
	return &calls
}

// ext4Mounts is a guest with one writable disk-backed filesystem, which is the shape
// a sandbox's guest has: one root on /dev/vda.
const ext4Mounts = `/dev/vda / ext4 rw,relatime 0 0
proc /proc proc rw,nosuid,nodev,noexec,relatime 0 0
sysfs /sys sysfs rw,nosuid,nodev,noexec,relatime 0 0
tmpfs /dev/shm tmpfs rw,nosuid,nodev,relatime 0 0
/dev/vdb /var/lib/extra ext4 ro,relatime 0 0
`

// TestQuiesceFreezesAfterSyncing pins the order the guest promises: flush first, then
// block the writers. It is the whole consistency argument for the checkpoint that
// follows, and a test that only checked "frozen" would pass with the two swapped.
func TestQuiesceFreezesAfterSyncing(t *testing.T) {
	calls := withQuiesceSeams(t, ext4Mounts, nil)

	report, err := quiesceGuest(context.Background(), QuiesceFreeze)
	require.NoError(t, err)

	assert.Equal(t, []string{"sync", "freeze /"}, *calls)
	assert.Equal(t, QuiesceFreeze, report.Action)
	assert.Equal(t, QuiesceModeFreeze, report.Mode)
	assert.Equal(t, []string{"/"}, report.Mounts, "only the writable device-backed mount is frozen")
	assert.False(t, report.Already)
}

// TestQuiesceIsIdempotent pins the RFC's exit criterion: reaching the same state twice
// is a success that says so, because a checkpoint retried after a failure must not fail
// on its own first attempt.
func TestQuiesceIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mounts")
	require.NoError(t, os.WriteFile(path, []byte(ext4Mounts), 0o644))

	originalMounts, originalSync := guestMounts, syncFilesystems
	originalFreeze, originalThaw := freezeFilesystem, thawFilesystem
	guestMounts = path
	syncFilesystems = func() {}
	freezeFilesystem = func(string) (bool, error) { return true, nil }
	thawFilesystem = func(string) (bool, error) { return true, nil }
	t.Cleanup(func() {
		guestMounts, syncFilesystems = originalMounts, originalSync
		freezeFilesystem, thawFilesystem = originalFreeze, originalThaw
	})

	frozen, err := quiesceGuest(context.Background(), QuiesceFreeze)
	require.NoError(t, err)
	assert.True(t, frozen.Already, "a guest that was already frozen reports it rather than failing")

	thawed, err := quiesceGuest(context.Background(), QuiesceThaw)
	require.NoError(t, err)
	assert.True(t, thawed.Already)
}

// TestQuiesceThawDoesNotSync pins that the release is only a release: syncing on the way
// out would flush writes the caller may not want made (a fork that was abandoned), and
// it would make the thaw's cost depend on the workload rather than on one ioctl.
func TestQuiesceThawReleasesWithoutSyncing(t *testing.T) {
	calls := withQuiesceSeams(t, ext4Mounts, nil)

	report, err := quiesceGuest(context.Background(), QuiesceThaw)
	require.NoError(t, err)

	assert.Equal(t, []string{"thaw /"}, *calls)
	assert.Equal(t, QuiesceThaw, report.Action)
}

// TestQuiesceRefusesWhatItCannotDo pins the three refusals a caller acts on: a direction
// the op does not have, a guest with nothing to freeze, and a filesystem that will not
// take the ioctl. Each is an error because a checkpoint taken anyway would claim a
// consistency it does not have.
func TestQuiesceRefusesWhatItCannotDo(t *testing.T) {
	t.Run("an action this build does not have", func(t *testing.T) {
		calls := withQuiesceSeams(t, ext4Mounts, nil)

		_, err := quiesceGuest(context.Background(), "suspend")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "neither")
		assert.Empty(t, *calls, "nothing is touched for an action that is not understood")
	})

	t.Run("a guest with nothing to freeze", func(t *testing.T) {
		withQuiesceSeams(t, "proc /proc proc rw 0 0\ntmpfs /tmp tmpfs rw 0 0\n", nil)

		_, err := quiesceGuest(context.Background(), QuiesceFreeze)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no writable filesystem backed by a block device")
	})

	t.Run("a filesystem that will not freeze", func(t *testing.T) {
		refused := map[string]error{"/var": unix.EOPNOTSUPP}
		calls := withQuiesceSeams(t, ext4Mounts+"/dev/vdc /var ext4 rw 0 0\n", refused)

		_, err := quiesceGuest(context.Background(), QuiesceFreeze)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "/var")
		// The mount before it was frozen; the caller learns which one stopped it, and
		// the report it gets back is not published as a success.
		assert.Equal(t, []string{"sync", "freeze /"}, *calls)
	})

	t.Run("a deadline already spent", func(t *testing.T) {
		calls := withQuiesceSeams(t, ext4Mounts, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := quiesceGuest(ctx, QuiesceFreeze)
		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, *calls)
	})
}

// TestWritableDeviceMountsSelectsWhatACheckpointCanCatch pins the selection rule: the
// filesystems a checkpoint can catch half-written are the writable ones on a block
// device. Everything else is either rebuilt on boot (proc, sysfs, devtmpfs), cannot
// disagree with the disk image (a read-only mount), or is not part of the image at all.
func TestWritableDeviceMountsSelectsWhatACheckpointCanCatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mounts")
	table := `/dev/vda / ext4 rw,relatime 0 0
/dev/vdb /var/lib/extra ext4 ro,relatime 0 0
tmpfs /dev/shm tmpfs rw,nosuid 0 0
proc /proc proc rw 0 0
/dev/vdc /mnt/with\040space ext4 rw,errors=remount-ro 0 0
overlay /overlay overlay rw 0 0
/dev/vdd
`
	require.NoError(t, os.WriteFile(path, []byte(table), 0o644))

	mounts, err := writableDeviceMounts(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"/", "/mnt/with\\040space"}, mounts,
		"a mount a checkpoint cannot catch is not frozen, and a truncated line is not a mount")

	_, err = writableDeviceMounts(filepath.Join(t.TempDir(), "absent"))
	assert.Error(t, err, "a guest whose mount table cannot be read cannot be quiesced")
}

// TestClientQuiesceOverTheWire is the operation as the node sees it: the report arrives
// as frames, and a refusal arrives with its own code rather than as a message a caller
// has to parse.
func TestClientQuiesceOverTheWire(t *testing.T) {
	calls := withQuiesceSeams(t, ext4Mounts, nil)
	conn, _ := newPipePair(t)
	c := testClient(conn)

	report, err := c.Quiesce(context.Background(), QuiesceFreeze)
	require.NoError(t, err)
	assert.Equal(t, QuiesceFreeze, report.Action)
	assert.Equal(t, []string{"/"}, report.Mounts)
	assert.Equal(t, []string{"sync", "freeze /"}, *calls)

	released, err := c.Quiesce(context.Background(), QuiesceThaw)
	require.NoError(t, err)
	assert.Equal(t, QuiesceThaw, released.Action)

	// A guest that cannot freeze answers with the code for it, and the node refuses
	// the checkpoint rather than storing an image of unknown consistency.
	_, err = c.Quiesce(context.Background(), "run")
	require.Error(t, err)
	assert.Equal(t, CodeQuiesceFailed, CodeOf(err))
}

// TestQuiesceIsAdvertised pins the capability handshake: a host must not issue an
// operation the guest did not advertise, so a build that serves quiesce has to say so.
func TestQuiesceIsAdvertised(t *testing.T) {
	assert.Contains(t, Capabilities(), CapQuiesce)

	// And a guest that does not advertise it is refused before its handler runs.
	conn, _ := newCappedPipePair(t, []string{CapHello, CapExec})
	c := testClient(conn)
	_, err := c.Quiesce(context.Background(), QuiesceFreeze)
	require.Error(t, err)
	assert.Equal(t, CodeUnsupportedOp, CodeOf(err))
	assert.Contains(t, err.Error(), "is not advertised by this agent")
}
