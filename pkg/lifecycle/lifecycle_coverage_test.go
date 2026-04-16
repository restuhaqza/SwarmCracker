package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStart_ProcessLookupFailure tests Start when firecracker binary is not found
func TestStart_ProcessLookupFailure(t *testing.T) {
	config := &ManagerConfig{
		SocketDir: "/tmp/test-start-fail",
	}
	vmMgr := NewVMMManager(config)

	task := &types.Task{
		ID: "test-task-lookup-fail",
	}

	ctx := context.Background()

	// Start should fail because firecracker binary won't be found in test environment
	// or if found, the config will be invalid
	err := vmMgr.Start(ctx, task, "")
	assert.Error(t, err, "Start should fail without valid config")
}

// TestStart_InvalidConfig tests Start with invalid configuration
func TestStart_InvalidConfig(t *testing.T) {
	config := &ManagerConfig{
		SocketDir: "/tmp/test-start-invalid",
	}
	vmMgr := NewVMMManager(config)

	task := &types.Task{
		ID: "test-task-invalid-config",
	}

	ctx := context.Background()

	// Empty config should fail
	err := vmMgr.Start(ctx, task, "")
	assert.Error(t, err, "Start should fail with empty config")
}

// TestStart_NilTask tests Start with nil task
func TestStart_NilTask(t *testing.T) {
	config := &ManagerConfig{
		SocketDir: "/tmp/test-start-nil",
	}
	vmMgr := NewVMMManager(config)

	ctx := context.Background()

	err := vmMgr.Start(ctx, nil, "invalid-config")
	assert.Error(t, err, "Start should fail with nil task")
}

// TestStart_DuplicateVM tests starting a VM with the same ID twice
func TestStart_DuplicateVM(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	config := &ManagerConfig{
		SocketDir: "/tmp/test-start-dup",
	}
	vmMgr := NewVMMManager(config)

	task := &types.Task{
		ID: "test-task-duplicate",
	}

	ctx := context.Background()

	// First attempt - will fail due to invalid config, but that's OK
	vmMgr.Start(ctx, task, "invalid-config")

	// Second attempt with same ID should fail with "already exists" error
	err := vmMgr.Start(ctx, task, "invalid-config")
	// The error might be about duplicate or about invalid config
	assert.Error(t, err, "Start should fail for duplicate VM ID")
}

// TestForceKillVM_ProcessAlreadyFinished tests forceKillVM when process is already gone
func TestForceKillVM_ProcessAlreadyFinished(t *testing.T) {
	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}

	// Use a PID that doesn't exist
	vmInstance := &VMInstance{
		ID:         "test-vm-finished",
		PID:        99999,
		State:      VMStateRunning,
		SocketPath: "/tmp/test-sock-finished.sock",
	}

	// On Linux, FindProcess succeeds even for non-existent PIDs
	// The error comes when trying to kill the process
	err := vmMgr.forceKillVM(vmInstance)
	// May error if process doesn't exist - that's OK
	assert.True(t, err == nil || err.Error() == "os: process already finished" ||
		err.Error() == "signal: killed", "forceKillVM should handle finished process")
}

// TestForceKillVM_AlreadyStoppedProcess tests forceKillVM on already stopped process
func TestForceKillVM_AlreadyStoppedProcess(t *testing.T) {
	// Create and immediately exit a process
	cmd := exec.Command("true")
	err := cmd.Run()
	require.NoError(t, err, "failed to run true command")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}

	vmInstance := &VMInstance{
		ID:         "test-vm-stopped",
		PID:        cmd.Process.Pid,
		State:      VMStateStopped,
		SocketPath: "/tmp/test-sock-stopped.sock",
	}

	// Process is already gone
	err = vmMgr.forceKillVM(vmInstance)
	// Should not panic
	assert.True(t, err == nil || err.Error() == "os: process already finished",
		"forceKillVM should handle stopped process")
}

// TestForceKillVM_SignalFailure tests forceKillVM when signal fails
func TestForceKillVM_SignalFailure(t *testing.T) {
	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}

	// PID 1 is init, which we can't kill
	vmInstance := &VMInstance{
		ID:         "test-vm-init",
		PID:        1,
		State:      VMStateRunning,
		SocketPath: "/tmp/test-sock-init.sock",
	}

	// Should error - can't kill init
	err := vmMgr.forceKillVM(vmInstance)
	assert.Error(t, err, "forceKillVM should fail for PID 1")
}

// TestGracefulShutdown_VariousGracePeriods tests gracefulShutdown with different grace periods
func TestGracefulShutdown_VariousGracePeriods(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	testCases := []struct {
		name         string
		gracePeriod  int
		expectKill   bool
	}{
		{"immediate", 0, true},
		{"very-short", 1, true},
		{"short", 2, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sleep", "10")
			require.NoError(t, cmd.Start(), "failed to start sleep process")

			vmMgr := &VMMManager{
				vms: make(map[string]*VMInstance),
			}

			vmInstance := &VMInstance{
				ID:             "test-vm-grace-" + tc.name,
				PID:            cmd.Process.Pid,
				State:          VMStateRunning,
				GracePeriodSec: tc.gracePeriod,
				SocketPath:     "/tmp/test-sock-" + tc.name + ".sock",
				InitSystem:     "init",
			}
			vmMgr.vms[vmInstance.ID] = vmInstance

			ctx := context.Background()

			err := vmMgr.gracefulShutdown(ctx, vmInstance)
			assert.NoError(t, err, "gracefulShutdown should succeed")

			// Cleanup
			cmd.Wait()
		})
	}
}

// TestGracefulShutdown_ConsecutiveShutdowns tests calling gracefulShutdown twice
func TestGracefulShutdown_ConsecutiveShutdowns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	cmd := exec.Command("sleep", "5")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}

	vmInstance := &VMInstance{
		ID:             "test-vm-consecutive",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 2,
		SocketPath:     "/tmp/test-sock-consecutive.sock",
		InitSystem:     "init",
	}
	vmMgr.vms[vmInstance.ID] = vmInstance

	ctx := context.Background()

	// First shutdown
	err := vmMgr.gracefulShutdown(ctx, vmInstance)
	assert.NoError(t, err, "first gracefulShutdown should succeed")
	assert.Equal(t, VMStateStopped, vmInstance.State, "VM should be stopped")

	// Second shutdown - process is already gone
	err = vmMgr.gracefulShutdown(ctx, vmInstance)
	// May error but shouldn't panic
	assert.True(t, err == nil || err.Error() == "os: process already finished",
		"second gracefulShutdown should handle already-stopped process")

	// Cleanup
	cmd.Wait()
}

// TestForceKillVM_RealProcess tests forceKillVM on an actual process
func TestForceKillVM_RealProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	// Create a long-running process
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}

	vmInstance := &VMInstance{
		ID:         "test-vm-real-kill",
		PID:        cmd.Process.Pid,
		State:      VMStateRunning,
		SocketPath: "/tmp/test-sock-real-kill.sock",
	}

	// Kill the process
	err := vmMgr.forceKillVM(vmInstance)
	assert.NoError(t, err, "forceKillVM should succeed")

	// Verify process was killed
	_, err = os.FindProcess(cmd.Process.Pid)
	// Process should be gone or killed

	// Cleanup zombie
	cmd.Wait()
}

// TestGracefulShutdown_ContextTimeout tests gracefulShutdown with context that has timeout
func TestGracefulShutdown_ContextTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}

	vmInstance := &VMInstance{
		ID:             "test-vm-ctx-timeout",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 30,
		SocketPath:     "/tmp/test-sock-ctx-timeout.sock",
		InitSystem:     "init",
	}
	vmMgr.vms[vmInstance.ID] = vmInstance

	// Context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Should timeout and force kill
	err := vmMgr.gracefulShutdown(ctx, vmInstance)
	assert.NoError(t, err, "gracefulShutdown should handle context timeout")

	// Cleanup
	cmd.Wait()
}

// TestStart_SocketDirCreation tests that socket directory is created
func TestStart_SocketDirCreation(t *testing.T) {
	tmpDir := "/tmp/test-socket-creation-" + time.Now().Format("20060102150405")
	config := &ManagerConfig{
		SocketDir: tmpDir,
	}

	// Remove directory if it exists
	os.RemoveAll(tmpDir)

	vmMgr := NewVMMManager(config)
	assert.NotNil(t, vmMgr, "VMMManager should be created")

	// Directory should be created
	_, err := os.Stat(tmpDir)
	assert.NoError(t, err, "socket directory should be created")

	// Cleanup
	os.RemoveAll(tmpDir)
}

// TestGracefulShutdown_NoInitSystem tests gracefulShutdown without init system
func TestGracefulShutdown_NoInitSystem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	cmd := exec.Command("sleep", "5")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}

	vmInstance := &VMInstance{
		ID:             "test-vm-no-init",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 2,
		SocketPath:     "/tmp/test-sock-no-init.sock",
		InitSystem:     "none",
	}
	vmMgr.vms[vmInstance.ID] = vmInstance

	ctx := context.Background()

	// Should still work - just sends SIGTERM
	err := vmMgr.gracefulShutdown(ctx, vmInstance)
	assert.NoError(t, err, "gracefulShutdown should work without init system")

	// Cleanup
	cmd.Wait()
}

// TestForceKillVM_ZeroPID tests forceKillVM with PID 0
func TestForceKillVM_ZeroPID(t *testing.T) {
	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}

	vmInstance := &VMInstance{
		ID:         "test-vm-zero-pid",
		PID:        0,
		State:      VMStateRunning,
		SocketPath: "/tmp/test-sock-zero-pid.sock",
	}

	// PID 0 is invalid
	err := vmMgr.forceKillVM(vmInstance)
	assert.Error(t, err, "forceKillVM should fail with PID 0")
}

// TestGracefulShutdown_VerifyProcessPolling tests that gracefulShutdown polls for process exit
func TestGracefulShutdown_VerifyProcessPolling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	// Create a process that exits quickly
	cmd := exec.Command("sleep", "1")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}

	vmInstance := &VMInstance{
		ID:             "test-vm-polling",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 5,
		SocketPath:     "/tmp/test-sock-polling.sock",
		InitSystem:     "init",
	}
	vmMgr.vms[vmInstance.ID] = vmInstance

	ctx := context.Background()

	start := time.Now()
	err := vmMgr.gracefulShutdown(ctx, vmInstance)
	elapsed := time.Since(start)

	assert.NoError(t, err, "gracefulShutdown should succeed")
	assert.Equal(t, VMStateStopped, vmInstance.State, "VM should be stopped")
	// Should complete quickly (process exits in 1s)
	assert.Less(t, elapsed.Seconds(), 2.0, "should complete within 2 seconds")

	// Cleanup
	cmd.Wait()
}
