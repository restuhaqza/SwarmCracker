package snapshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// needSleepProcess returns a started `sleep 30` command. A real process is what
// makes the stop test meaningful: the signals under test are the ones the CLI and
// the broker apply to a restored Firecracker.
func needSleepProcess(t *testing.T) *exec.Cmd {
	t.Helper()

	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		// A failed test must not leave the sleeper behind.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	return cmd
}

// needRestoredManager returns a manager over a fresh snapshot directory. The
// socket the tracked VM owns lives beside it.
func needRestoredManager(t *testing.T, socketPath string) (*Manager, *exec.Cmd) {
	t.Helper()

	mgr, err := NewManager(SnapshotConfig{SnapshotDir: t.TempDir()})
	require.NoError(t, err)

	cmd := needSleepProcess(t)
	mgr.TrackRestoredProcess("task-1", cmd.Process, socketPath)
	return mgr, cmd
}

// TestStopRestoredStopsARealProcess pins the whole stop path: SIGTERM reaches the
// process, the manager waits for it to exit, and the entry is gone so a second
// call cannot signal a reused PID.
func TestStopRestoredStopsARealProcess(t *testing.T) {
	socketDir := t.TempDir()
	socketPath := filepath.Join(socketDir, "task-1-restored.sock")
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))

	mgr, cmd := needRestoredManager(t, socketPath)

	start := time.Now()
	require.NoError(t, mgr.StopRestored("task-1"))
	elapsed := time.Since(start)

	require.Less(t, elapsed, restoredStopTimeout,
		"SIGTERM must end a cooperative process well before the SIGKILL fallback")

	// Reap the exited child. StopRestored never calls Wait: it does not own the
	// command, only the *os.Process.
	_, _ = cmd.Process.Wait()

	// The store owns the socket: the stop that ended the process unlinked it.
	_, statErr := os.Stat(socketPath)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "the stopped VM's socket must be removed by the store")

	require.ErrorIs(t, mgr.StopRestored("task-1"), ErrNoRestoredVM,
		"a stopped VM must be idempotent, not signalled twice")
}

// TestStopRestoredUnknownTask pins the sentinel: this manager tracks no restored
// VM for the task, which the broker turns into 404 no_restored_vm.
func TestStopRestoredUnknownTask(t *testing.T) {
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: t.TempDir()})
	require.NoError(t, err)

	err = mgr.StopRestored("task-does-not-exist")
	require.ErrorIs(t, err, ErrNoRestoredVM)
}

// TestStopRestoredAlreadyExitedProcess pins the race the CLI can hit: the VM
// exited between the lookup and the signal. The caller asked for the end state,
// so the answer is success and the entry is still removed. A reaped process (the
// daemon's Wait goroutine ran) answers os.ErrProcessDone, which is also success.
func TestStopRestoredAlreadyExitedProcess(t *testing.T) {
	cmd := needSleepProcess(t)
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: t.TempDir()})
	require.NoError(t, err)

	require.NoError(t, cmd.Process.Kill())
	_, _ = cmd.Process.Wait()

	mgr.TrackRestoredProcess("task-1", cmd.Process, "")

	assert.NoError(t, mgr.StopRestored("task-1"),
		"a process that already exited satisfies a stop")
	assert.ErrorIs(t, mgr.StopRestored("task-1"), ErrNoRestoredVM)
}

// TestStopRestoredKeepsTheEntryWhenTheSignalFails pins the retry contract: a
// stop that cannot signal the process must keep the entry and return the error,
// because the VM may still be alive. Dropping it would strand a running process
// behind a 404 forever.
func TestStopRestoredKeepsTheEntryWhenTheSignalFails(t *testing.T) {
	mgr, _ := needRestoredManager(t, "")

	previous := signalProcess
	signalProcess = func(*os.Process, syscall.Signal) error { return syscall.EPERM }
	t.Cleanup(func() { signalProcess = previous })

	err := mgr.StopRestored("task-1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoRestoredVM, "a failed signal is not a missing VM")

	// The same VM is still tracked, so a later stop with a working signal path
	// ends it instead of answering ErrNoRestoredVM.
	signalProcess = previous
	require.NoError(t, mgr.StopRestored("task-1"))
}

// TestStopRestoredWaitsForAConcurrentRestore pins the serialization that protects
// the socket: a restore holds the task's lock while it unlinks the old socket and
// starts its process, so a stop that arrives meanwhile waits instead of deleting
// the socket the restore just created.
func TestStopRestoredWaitsForAConcurrentRestore(t *testing.T) {
	socketDir := t.TempDir()
	socketPath := filepath.Join(socketDir, "task-1-restored.sock")
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))

	mgr, _ := needRestoredManager(t, socketPath)

	// Stand in for a restore in flight: it holds the same per-task lock.
	restoreLock := mgr.restoredLock("task-1")
	restoreLock.Lock()

	done := make(chan error, 1)
	go func() { done <- mgr.StopRestored("task-1") }()

	select {
	case err := <-done:
		t.Fatalf("stop ran while a restore held the lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	// The socket still exists: nothing has unlinked it yet.
	assert.FileExists(t, socketPath, "a waiting stop must not touch the socket")

	restoreLock.Unlock()
	require.NoError(t, <-done)

	_, statErr := os.Stat(socketPath)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "the stop removes the socket once it owns the lock")
}
