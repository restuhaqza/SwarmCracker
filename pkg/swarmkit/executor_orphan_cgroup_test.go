package swarmkit

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCleanupOrphanedVMs_RemovesCgroupAfterProcessExits proves the orphan sweep
// leaves no cgroup behind: an orphan has no controller left to run the normal
// teardown, so without this every daemon restart would leak its cgroup. The
// fake cgroup manager snapshots process liveness at RemoveCgroup time, which
// pins the slice-5 rule — never remove a cgroup while the VMM process is alive.
func TestCleanupOrphanedVMs_RemovesCgroupAfterProcessExits(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	fake := &fakeCgroupManager{online: "0-7"}
	// A non-nil ProcessState means Wait reaped the process (the sweep SIGTERMs
	// it, so it is Signaled rather than Exited).
	fake.removeProcessAlive = func() bool {
		return cmd.ProcessState == nil
	}
	vmm := cgroupTestVMM(t, fake)
	vmm.processes["orphan-task"] = cmd

	e := &Executor{
		config:      &Config{SocketDir: vmm.socketDir, RootfsDir: t.TempDir()},
		controllers: map[string]*Controller{},
		vmmMgr:      vmm,
	}
	e.cleanupOrphanedVMs(context.Background())

	require.NotNil(t, cmd.ProcessState, "the orphan process must be killed and reaped")

	removed := fake.removed
	require.Equal(t, []string{"orphan-task"}, removed,
		"the orphan's cgroup must be removed so it does not outlive its VM")
	assert.Equal(t, []bool{false}, fake.removeLiveness(),
		"the cgroup must only be removed after the process has exited")
	assert.Empty(t, vmm.GetRunningProcesses(), "the orphan must leave the process map")
}

// TestCleanupOrphanedVMs_LeavesActiveTaskCgroup keeps the sweep honest: only an
// orphan's cgroup is removed, never an active task's.
func TestCleanupOrphanedVMs_LeavesActiveTaskCgroup(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	fake := &fakeCgroupManager{online: "0-7"}
	vmm := cgroupTestVMM(t, fake)
	vmm.processes["active-task"] = cmd

	e := &Executor{
		config:      &Config{SocketDir: vmm.socketDir, RootfsDir: t.TempDir()},
		controllers: map[string]*Controller{"active-task": {}},
		vmmMgr:      vmm,
	}
	e.cleanupOrphanedVMs(context.Background())

	assert.Empty(t, fake.removed, "an active task's cgroup must be left for its own teardown")
	assert.NotNil(t, vmm.GetRunningProcesses()["active-task"])
}

// TestCleanupOrphanedVMs_CgroupRemovalFailureIsBestEffort proves a failed
// removal does not abort the sweep: the shared policy warns and teardown
// continues to the next orphan.
func TestCleanupOrphanedVMs_CgroupRemovalFailureIsBestEffort(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	fake := &fakeCgroupManager{online: "0-7", removeErr: errors.New("busy")}
	vmm := cgroupTestVMM(t, fake)
	vmm.processes["orphan-task"] = cmd

	e := &Executor{
		config:      &Config{SocketDir: vmm.socketDir, RootfsDir: t.TempDir()},
		controllers: map[string]*Controller{},
		vmmMgr:      vmm,
	}
	e.cleanupOrphanedVMs(context.Background())

	assert.Equal(t, []string{"orphan-task"}, fake.removed, "removal was attempted despite failing")
	assert.Empty(t, vmm.GetRunningProcesses(), "a failed cgroup removal must not abort the orphan sweep")
}
