package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// TestStopProcessTerminatesWhatIgnoresSIGTERM is the teardown's last resort: a VMM that
// catches SIGTERM is still stopped, and the wait after SIGKILL is what makes "stopped"
// true rather than "signalled".
func TestStopProcessTerminatesWhatIgnoresSIGTERM(t *testing.T) {
	proc := exec.Command("sh", "-c", `trap "" TERM; sleep 60`)
	require.NoError(t, proc.Start())
	t.Cleanup(func() { _ = proc.Process.Kill() })

	// The command must be running before it can be asked to stop, and it must have
	// installed its trap.
	time.Sleep(100 * time.Millisecond)

	require.NoError(t, stopProcess(proc.Process.Pid, 50*time.Millisecond))
	assert.False(t, vminstance.ProcessAlive(proc.Process.Pid))

	// Stopping a process that is already gone is success: a teardown is retried.
	require.NoError(t, stopProcess(proc.Process.Pid, 50*time.Millisecond))
	require.NoError(t, stopProcess(0, 50*time.Millisecond))
}

// TestRemoveCgroupReportsWhatIsStillInUse pins the one cgroup failure a teardown must
// report rather than swallow: a directory the kernel still holds is not removed, and
// saying it was would be the leftover this file exists to prevent.
func TestRemoveCgroupReportsWhatIsStillInUse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cgroup")
	require.NoError(t, os.Mkdir(dir, 0o755))

	removed, err := removeCgroup(dir)
	require.NoError(t, err)
	assert.True(t, removed, "an empty directory is a cgroup the teardown removed")

	removed, err = removeCgroup(dir)
	require.NoError(t, err)
	assert.False(t, removed, "a cgroup that is already gone is not reported as removed")

	// A directory with a child is what a cgroup with a live process looks like to
	// rmdir. It is an error rather than a quiet success — but only after the removal
	// has waited out its grace, because a process that has just been signalled is
	// "gone" by /proc before the kernel lets go of its cgroup. This is the shape of the
	// failure a host run found six times in 100 concurrent removals.
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("1\n"), 0o600))

	start := time.Now()
	removed, err = removeCgroup(dir)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.False(t, removed)
	assert.ErrorIs(t, err, syscall.ENOTEMPTY,
		"the kernel's own reason is still the cause after the wait")
	assert.ErrorIs(t, err, ErrInUse,
		"and the failure is typed, so the API reports resource-in-use rather than unreachable")
	assert.GreaterOrEqual(t, elapsed, cgroupReleaseGrace,
		"the removal waited for the kernel to release the directory before giving up")
}

// TestRemoveKeepsTheRecordWhenACredentialCannotBeRevoked pins why token revocation is
// not best effort: a sandbox that is gone while a credential naming it still works is a
// failure to report, and the record stays so the next rm retries it.
func TestRemoveKeepsTheRecordWhenACredentialCannotBeRevoked(t *testing.T) {
	f := newFixture(t)
	f.manager.cfg.RevokeTokens = func(context.Context, string) (int, error) {
		return 1, errors.New("token store is read-only")
	}

	require.NoError(t, f.store.Put(testRecord("sbx-1")))

	_, err := f.manager.Remove(context.Background(), "sbx-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revoke the API tokens")

	_, found, err := f.store.Get("sbx-1")
	require.NoError(t, err)
	assert.True(t, found, "the record is the handle the next rm uses")
}

// TestRemoveStopsAtACredentialItCannotDelete pins the ordering inside the stop step: the
// host's copy of the guest credential goes before the jail, and one that cannot be
// removed fails the teardown rather than being reported as gone.
func TestRemoveStopsAtACredentialItCannotDelete(t *testing.T) {
	f := newFixture(t)

	disk := filepath.Join(t.TempDir(), "sbx-1.ext4")
	require.NoError(t, os.WriteFile(disk, []byte("disk"), 0o600))

	// A non-empty directory where the token file belongs: it cannot be removed as a
	// file, whatever the process's privileges.
	tokenFile := filepath.Join(t.TempDir(), "sbx-1.agent-token")
	require.NoError(t, os.MkdirAll(filepath.Join(tokenFile, "held"), 0o700))

	rec := testRecord("sbx-1")
	rec.Disk, rec.AgentTokenFile = disk, tokenFile
	require.NoError(t, f.store.Put(rec))

	_, err := f.manager.Remove(context.Background(), "sbx-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent token")

	_, statErr := os.Stat(disk)
	assert.NoError(t, statErr, "a teardown that stopped early leaves the disk for the next attempt")
	_, found, getErr := f.store.Get("sbx-1")
	require.NoError(t, getErr)
	assert.True(t, found)
}

// TestRemoveNamesWhatItCannotRemove pins the teardown's report of its own step: a socket or
// a disk that cannot be removed fails the removal rather than being counted as freed, so
// nothing is claimed that is still there.
func TestRemoveNamesWhatItCannotRemove(t *testing.T) {
	t.Run("an API socket that will not go", func(t *testing.T) {
		f := newFixture(t)

		// A non-empty directory where the socket belongs: removeFile cannot remove it.
		socket := filepath.Join(t.TempDir(), "sbx-1.sock")
		require.NoError(t, os.MkdirAll(filepath.Join(socket, "held"), 0o700))

		rec := testRecord("sbx-1")
		require.NoError(t, f.store.Put(rec))
		require.NoError(t, f.instances.Put(vminstance.Instance{
			ID: "sbx-1", Kind: vminstance.KindSandbox, APISocket: socket,
		}))

		_, err := f.manager.Remove(context.Background(), "sbx-1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "remove its API socket")

		_, found, getErr := f.store.Get("sbx-1")
		require.NoError(t, getErr)
		assert.True(t, found, "a teardown that could not finish leaves its record")
	})

	t.Run("a disk that will not go", func(t *testing.T) {
		f := newFixture(t)

		disk := filepath.Join(t.TempDir(), "sbx-1.ext4")
		require.NoError(t, os.MkdirAll(filepath.Join(disk, "held"), 0o700))

		rec := testRecord("sbx-1")
		rec.Disk = disk
		require.NoError(t, f.store.Put(rec))

		freed, err := f.manager.Remove(context.Background(), "sbx-1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "remove its disk")
		assert.Empty(t, freed.Disk, "a disk that is still there is not reported as freed")
	})

	t.Run("a registry that cannot be read", func(t *testing.T) {
		f := newFixture(t)
		require.NoError(t, f.store.Put(testRecord("sbx-1")))
		f.instances.readErr = errors.New("the registry is unreadable")

		_, err := f.manager.Remove(context.Background(), "sbx-1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the instance record")
	})
}

// TestRemoveCgroupWaitsForAProcessThatIsLeaving is the race the host run exposed: a VMM
// that has been signalled is gone from /proc before the kernel lets go of its cgroup, so
// a removal that gives up on the first EBUSY fails for a sandbox that is already dead.
// Here the child appears held for a moment and is released while the removal is waiting.
func TestRemoveCgroupWaitsForAProcessThatIsLeaving(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cgroup")
	require.NoError(t, os.Mkdir(dir, 0o755))

	// A real child directory stands in for the process the kernel has not released yet,
	// which is what makes rmdir answer ENOTEMPTY.
	held := filepath.Join(dir, "held")
	require.NoError(t, os.MkdirAll(held, 0o755))

	released := make(chan struct{})
	go func() {
		time.Sleep(cgroupReleaseGrace / 4)
		_ = os.Remove(held)
		close(released)
	}()

	removed, err := removeCgroup(dir)
	<-released
	require.NoError(t, err, "a process that finishes leaving within the grace must not fail the removal")
	assert.True(t, removed, "and the cgroup is reported as removed, because it was")
}

// TestAFailedSandboxWithResidueIsReaped is the leak this fix closes. A teardown that
// failed leaves the record naming a netns, a disk, a jail and a vsock — resources no
// caller will ask about again, because the sandbox is already failed. Before the reaper
// could take it, only idle and max TTLs made a record due, so a failed sandbox with no
// TTL was never retried and held its resources for the life of the node.
func TestAFailedSandboxWithResidueIsReaped(t *testing.T) {
	f := newFixture(t)

	// The shape a failed teardown leaves: failed, no TTL, and still naming the netns,
	// disk, jail and vsock that the teardown did not get to.
	rec := testRecord("sbx-failed")
	rec.State = StateFailed
	rec.Reason = "cgroup is still in use"
	rec.NetNS = "/var/run/netns/sbx-failed"
	rec.VsockPath = "/run/firecracker/sbx-failed.vsock"
	rec.Disk, rec.JailRoot = "", ""
	rec.IdleTTLSeconds, rec.MaxTTLSeconds = 0, 0
	rec.Updated = time.Now().Add(-time.Minute)
	require.NoError(t, f.manager.cfg.Store.Put(rec))

	// Nothing is due while the record is still inside its retry window: a caller reading
	// it right after the failure should see the reason, not a record that vanished.
	justFailed := rec
	justFailed.Updated = time.Now().Add(-time.Second)
	if limit, due := justFailed.ReapDue(time.Now()); due {
		t.Fatalf("a failed sandbox must not be reaped before its retry window passes, got limit %q", limit)
	}

	reaped, err := f.manager.Reap(context.Background(), time.Now())
	require.NoError(t, err)
	require.Len(t, reaped, 1, "the failed sandbox is the only thing due")
	assert.Equal(t, ReapLimitFailed, reaped[0].Limit)
	assert.NoError(t, reaped[0].Err)

	_, err = f.manager.Get(rec.ID)
	assert.ErrorIs(t, err, ErrNotFound, "and its record is gone, so the resources it named are free")
}

// TestAFailedSandboxWithNothingLeftKeepsItsRecord is the other side: a create that failed
// and freed everything is the answer to "what happened to the sandbox I asked for", so
// it is not reaped early just for being failed.
func TestAFailedSandboxWithNothingLeftKeepsItsRecord(t *testing.T) {
	rec := Record{ID: "sbx-clean-fail", State: StateFailed, Reason: "the image could not be pulled"}
	rec.Updated = time.Now().Add(-time.Hour)

	assert.False(t, rec.HoldsResidue(), "nothing is named, so nothing is held")
	if limit, due := rec.ReapDue(time.Now()); due {
		t.Fatalf("a failed record with no residue must not be reaped early, got limit %q", limit)
	}
}
