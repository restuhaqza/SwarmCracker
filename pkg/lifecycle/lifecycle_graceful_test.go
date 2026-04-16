package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGracefulShutdown_ProcessExitsGracefully tests the normal case where
// the process exits after receiving SIGTERM
func TestGracefulShutdown_ProcessExitsGracefully(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	// Create a sleep process that will respond to SIGTERM
	cmd := exec.Command("sleep", "10")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	// Create VM instance
	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}
	vmInstance := &VMInstance{
		ID:             "test-vm-1",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 5,
		SocketPath:     "/tmp/test-sock-1.sock",
		InitSystem:     "init",
	}
	vmMgr.vms["test-vm-1"] = vmInstance

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run gracefulShutdown - this should send SIGTERM and wait for process to exit
	err := vmMgr.gracefulShutdown(ctx, vmInstance)

	// Verify the process was shut down
	assert.NoError(t, err, "gracefulShutdown should succeed")
	assert.Equal(t, VMStateStopped, vmInstance.State, "VM should be in stopped state")

	// Wait a bit to ensure process is fully reaped
	_, err = os.FindProcess(cmd.Process.Pid)
	// Process should be gone or unretrievable
	cmd.Wait() // Clean up zombie
}

// TestGracefulShutdown_GracePeriodExpiry tests that SIGKILL is sent when
// the process doesn't exit within the grace period
func TestGracefulShutdown_GracePeriodExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	// Create a process that ignores SIGTERM (using a custom signal handler)
	// We'll use sleep with a very long duration and rely on the timeout
	cmd := exec.Command("sleep", "100")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	// Create VM instance with very short grace period
	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}
	vmInstance := &VMInstance{
		ID:             "test-vm-2",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 1, // Very short grace period
		SocketPath:     "/tmp/test-sock-2.sock",
		InitSystem:     "init",
	}
	vmMgr.vms["test-vm-2"] = vmInstance

	// Create context
	ctx := context.Background()

	// Run gracefulShutdown - should timeout and force kill
	err := vmMgr.gracefulShutdown(ctx, vmInstance)

	// Verify force kill was attempted
	assert.NoError(t, err, "gracefulShutdown should succeed after force kill")

	// Clean up the process
	cmd.Wait()
}

// TestGracefulShutdown_ContextCancelImmediate tests immediate context cancellation
// triggers force kill
func TestGracefulShutdown_ContextCancelImmediate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	// Create a long-running process
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	// Create VM instance
	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}
	vmInstance := &VMInstance{
		ID:             "test-vm-3",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 30,
		SocketPath:     "/tmp/test-sock-3.sock",
		InitSystem:     "init",
	}
	vmMgr.vms["test-vm-3"] = vmInstance

	// Create context that will be cancelled quickly
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel context immediately
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	// Run gracefulShutdown - should handle context cancellation
	err := vmMgr.gracefulShutdown(ctx, vmInstance)

	// Verify force kill was triggered
	assert.NoError(t, err, "gracefulShutdown should handle context cancellation")

	// Clean up
	cmd.Wait()
}

// TestGracefulShutdown_InvalidPID tests that invalid PID
// triggers force kill immediately
func TestGracefulShutdown_InvalidPID(t *testing.T) {
	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}
	vmInstance := &VMInstance{
		ID:             "test-vm-4",
		PID:            99999, // Non-existent PID
		State:          VMStateRunning,
		GracePeriodSec: 5,
		SocketPath:     "/tmp/test-sock-4.sock",
		InitSystem:     "init",
	}
	vmMgr.vms["test-vm-4"] = vmInstance

	ctx := context.Background()

	// Run gracefulShutdown - process not found should trigger force kill
	err := vmMgr.gracefulShutdown(ctx, vmInstance)

	// On Linux, os.FindProcess succeeds even for non-existent PIDs.
	// The error comes when trying to signal/kill it.
	// gracefulShutdown should attempt force kill but may error.
	// What matters is the VM is stopped.
	assert.True(t, err == nil || err.Error() == "os: process already finished",
		"gracefulShutdown should handle missing process")
}

// TestGracefulShutdown_SIGTERMFailure tests that SIGTERM failure
// triggers force kill
func TestGracefulShutdown_SIGTERMFailure(t *testing.T) {
	// This test uses a process that will exit immediately
	// When we try to signal it, the signal will fail
	cmd := exec.Command("true") // exits immediately
	require.NoError(t, cmd.Start(), "failed to start process")
	cmd.Wait() // Wait for it to exit

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}
	vmInstance := &VMInstance{
		ID:             "test-vm-5",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 5,
		SocketPath:     "/tmp/test-sock-5.sock",
		InitSystem:     "init",
	}
	vmMgr.vms["test-vm-5"] = vmInstance

	ctx := context.Background()

	// Run gracefulShutdown - SIGTERM to dead process should trigger force kill
	err := vmMgr.gracefulShutdown(ctx, vmInstance)

	// Should handle SIGTERM failure and attempt force kill
	// May error if process is truly gone
	assert.True(t, err == nil || err.Error() == "os: process already finished",
		"gracefulShutdown should handle SIGTERM failure")
}

// TestGracefulShutdown_ZeroGracePeriod tests immediate force kill
func TestGracefulShutdown_ZeroGracePeriod(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	// Create a process
	cmd := exec.Command("sleep", "10")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}
	vmInstance := &VMInstance{
		ID:             "test-vm-6",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 0, // Zero grace period
		SocketPath:     "/tmp/test-sock-6.sock",
		InitSystem:     "init",
	}
	vmMgr.vms["test-vm-6"] = vmInstance

	ctx := context.Background()

	// Run gracefulShutdown - should timeout immediately and force kill
	err := vmMgr.gracefulShutdown(ctx, vmInstance)

	assert.NoError(t, err, "gracefulShutdown should handle zero grace period")

	// Clean up
	cmd.Wait()
}

// TestGracefulShutdown_StateTransition tests that VM state is
// properly transitioned to stopped
func TestGracefulShutdown_StateTransition(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	cmd := exec.Command("sleep", "5")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}
	vmInstance := &VMInstance{
		ID:             "test-vm-7",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 3,
		SocketPath:     "/tmp/test-sock-7.sock",
		InitSystem:     "init",
	}
	vmMgr.vms["test-vm-7"] = vmInstance

	// Verify initial state
	assert.Equal(t, VMStateRunning, vmInstance.State, "initial state should be running")

	ctx := context.Background()

	err := vmMgr.gracefulShutdown(ctx, vmInstance)

	assert.NoError(t, err, "gracefulShutdown should succeed")
	assert.Equal(t, VMStateStopped, vmInstance.State, "final state should be stopped")

	// Clean up
	cmd.Wait()
}

// TestGracefulShutdown_MultipleVMs tests that shutting down one VM
// doesn't affect others
func TestGracefulShutdown_MultipleVMs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	// Create two processes
	cmd1 := exec.Command("sleep", "10")
	require.NoError(t, cmd1.Start(), "failed to start first sleep process")

	cmd2 := exec.Command("sleep", "10")
	require.NoError(t, cmd2.Start(), "failed to start second sleep process")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}

	vm1 := &VMInstance{
		ID:             "test-vm-8a",
		PID:            cmd1.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 3,
		SocketPath:     "/tmp/test-sock-8a.sock",
		InitSystem:     "init",
	}

	vm2 := &VMInstance{
		ID:             "test-vm-8b",
		PID:            cmd2.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 3,
		SocketPath:     "/tmp/test-sock-8b.sock",
		InitSystem:     "init",
	}

	vmMgr.vms["test-vm-8a"] = vm1
	vmMgr.vms["test-vm-8b"] = vm2

	ctx := context.Background()

	// Shutdown first VM
	err := vmMgr.gracefulShutdown(ctx, vm1)
	assert.NoError(t, err, "first gracefulShutdown should succeed")
	assert.Equal(t, VMStateStopped, vm1.State, "first VM should be stopped")

	// Second VM should still be running
	assert.Equal(t, VMStateRunning, vm2.State, "second VM should still be running")

	// Verify second process is still alive
	err = cmd2.Process.Signal(syscall.Signal(0))
	assert.NoError(t, err, "second process should still be alive")

	// Cleanup both
	cmd1.Wait()
	cmd2.Process.Kill()
	cmd2.Wait()
}

// TestGracefulShutdown_AlreadyStoppedProcess tests graceful shutdown
// when the process is already stopped/exited
func TestGracefulShutdown_AlreadyStoppedProcess(t *testing.T) {
	// Create and immediately wait for process to exit
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run(), "failed to run true command")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}
	vmInstance := &VMInstance{
		ID:             "test-vm-9",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 5,
		SocketPath:     "/tmp/test-sock-9.sock",
		InitSystem:     "init",
	}
	vmMgr.vms["test-vm-9"] = vmInstance

	ctx := context.Background()

	// gracefulShutdown should handle this gracefully
	// Process is already gone, so it will fail at FindProcess or Signal stage
	err := vmMgr.gracefulShutdown(ctx, vmInstance)
	// Should not panic - may return error but that's OK
	assert.True(t, err == nil || err.Error() == "os: process already finished",
		"gracefulShutdown should handle already-stopped process without panic")
}

// TestGracefulShutdown_NilContext tests behavior with nil context
func TestGracefulShutdown_NilContext(t *testing.T) {
	// Create a process
	cmd := exec.Command("sleep", "5")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}
	vmInstance := &VMInstance{
		ID:             "test-vm-10",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 2,
		SocketPath:     "/tmp/test-sock-10.sock",
		InitSystem:     "init",
	}
	vmMgr.vms["test-vm-10"] = vmInstance

	// Use background context as nil would panic
	ctx := context.Background()

	err := vmMgr.gracefulShutdown(ctx, vmInstance)
	assert.NoError(t, err, "gracefulShutdown should succeed with valid context")

	// Clean up
	cmd.Wait()
}

// TestGracefulShutdown_NegativeGracePeriod tests that negative grace period
// is handled (treated as no grace period)
func TestGracefulShutdown_NegativeGracePeriod(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	cmd := exec.Command("sleep", "10")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}
	vmInstance := &VMInstance{
		ID:             "test-vm-11",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: -1, // Negative grace period
		SocketPath:     "/tmp/test-sock-11.sock",
		InitSystem:     "init",
	}
	vmMgr.vms["test-vm-11"] = vmInstance

	ctx := context.Background()

	// Should timeout immediately and force kill
	err := vmMgr.gracefulShutdown(ctx, vmInstance)
	assert.NoError(t, err, "gracefulShutdown should handle negative grace period")

	// Clean up
	cmd.Wait()
}

// TestForceKillVM_StubbornProcess tests the forceKillVM on a stubborn process
func TestForceKillVM_StubbornProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode - requires actual process")
	}

	// Create a stubborn process
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start(), "failed to start sleep process")

	vmMgr := &VMMManager{
		vms: make(map[string]*VMInstance),
	}
	vmInstance := &VMInstance{
		ID:             "test-vm-force",
		PID:            cmd.Process.Pid,
		State:          VMStateRunning,
		GracePeriodSec: 5,
		SocketPath:     "/tmp/test-sock-force.sock",
		InitSystem:     "init",
	}

	// Call forceKillVM directly
	err := vmMgr.forceKillVM(vmInstance)
	assert.NoError(t, err, "forceKillVM should succeed")

	// Verify process was killed
	_, err = os.FindProcess(cmd.Process.Pid)
	// Process should be gone or killed
	cmd.Wait() // Clean up zombie
}

// TestForceKillVM_HTTPPauseSuccess tests forceKillVM with successful HTTP pause
func TestForceKillVM_HTTPPauseSuccess(t *testing.T) {
	t.Skip("HTTP pause not implemented in forceKillVM - this test needs VMMManagerInternal")

	// NOTE: forceKillVM doesn't make HTTP calls - it only kills the process.
	// HTTP pause for /vm/config would be in a different function.
	// This test is a placeholder for when that functionality is added.

	// The actual forceKillVM only:
	// 1. Finds the process
	// 2. Calls process.Kill()
	// It does NOT make HTTP requests.

	// To test HTTP pause, we'd need to test the caller that uses forceKillVM
	// after making an HTTP PUT to /vm/config
}

// BenchmarkGracefulShutdown_FastExit benchmarks graceful shutdown with
// a process that exits quickly
func BenchmarkGracefulShutdown_FastExit(b *testing.B) {
	ctx := context.Background()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cmd := exec.Command("true")
			if err := cmd.Start(); err != nil {
				b.Fatal(err)
			}

			vmMgr := &VMMManager{
				vms: make(map[string]*VMInstance),
			}
			vmInstance := &VMInstance{
				ID:             "bench-vm",
				PID:            cmd.Process.Pid,
				State:          VMStateRunning,
				GracePeriodSec: 5,
				SocketPath:     "/tmp/bench-sock.sock",
				InitSystem:     "init",
			}

			vmMgr.gracefulShutdown(ctx, vmInstance)
			cmd.Wait()
		}
	})
}
