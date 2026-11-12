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
	// rmdir, and it is an error rather than a quiet success.
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("1\n"), 0o600))
	removed, err = removeCgroup(dir)
	require.Error(t, err)
	assert.False(t, removed)
	assert.ErrorIs(t, err, syscall.ENOTEMPTY)
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
