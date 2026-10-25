package snapshot

import (
	"os/exec"
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

// TestStopRestoredStopsARealProcess pins the whole stop path: SIGTERM reaches the
// process, the manager waits for it to exit, and the entry is gone so a second
// call cannot signal a reused PID.
func TestStopRestoredStopsARealProcess(t *testing.T) {
	cmd := needSleepProcess(t)
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: t.TempDir()})
	require.NoError(t, err)

	mgr.TrackRestoredProcess("task-1", cmd.Process)

	start := time.Now()
	require.NoError(t, mgr.StopRestored("task-1"))
	elapsed := time.Since(start)

	require.Less(t, elapsed, restoredStopTimeout,
		"SIGTERM must end a cooperative process well before the SIGKILL fallback")

	// Reap the exited child. StopRestored never calls Wait: it does not own the
	// command, only the *os.Process.
	_, _ = cmd.Process.Wait()

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
// so the answer is success and the entry is still removed.
func TestStopRestoredAlreadyExitedProcess(t *testing.T) {
	cmd := needSleepProcess(t)
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: t.TempDir()})
	require.NoError(t, err)

	require.NoError(t, cmd.Process.Kill())
	_, _ = cmd.Process.Wait()

	mgr.TrackRestoredProcess("task-1", cmd.Process)

	assert.NoError(t, mgr.StopRestored("task-1"),
		"a process that already exited satisfies a stop")
	assert.ErrorIs(t, mgr.StopRestored("task-1"), ErrNoRestoredVM)
}
