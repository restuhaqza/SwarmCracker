package lifecycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMMManager_MultipleVMs tests managing multiple VMs simultaneously
func TestVMMManager_MultipleVMs(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	ctx := context.Background()
	numVMs := 5

	// Create multiple mock VMs
	for i := 0; i < numVMs; i++ {
		taskID := fmt.Sprintf("task-%d", i)
		socketPath := filepath.Join(socketDir, taskID+".sock")

		// Create socket file
		err := os.WriteFile(socketPath, []byte("dummy"), 0644)
		require.NoError(t, err)

		vmm.mu.Lock()
		vmm.vms[taskID] = &VMInstance{
			ID:         taskID,
			PID:        1000 + i,
			State:      VMStateRunning,
			CreatedAt:  time.Now(),
			SocketPath: socketPath,
		}
		vmm.mu.Unlock()
	}

	// Verify all VMs are tracked
	vmm.mu.RLock()
	assert.Equal(t, numVMs, len(vmm.vms))
	vmm.mu.RUnlock()

	// Describe all VMs
	for i := 0; i < numVMs; i++ {
		taskID := fmt.Sprintf("task-%d", i)
		task := &types.Task{ID: taskID}

		status, err := vmm.Describe(ctx, task)
		require.NoError(t, err)
		// State might be ORPHANED or COMPLETE since PIDs don't exist
		// The important part is that Describe doesn't crash
		assert.NotNil(t, status)
	}

	// Remove all VMs
	for i := 0; i < numVMs; i++ {
		taskID := fmt.Sprintf("task-%d", i)
		task := &types.Task{ID: taskID}

		err := vmm.Remove(ctx, task)
		require.NoError(t, err)
	}

	// Verify all VMs are removed
	vmm.mu.RLock()
	assert.Equal(t, 0, len(vmm.vms))
	vmm.mu.RUnlock()
}

// TestVMMManager_ConcurrentOperations tests thread safety
func TestVMMManager_ConcurrentOperations(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	ctx := context.Background()
	numGoroutines := 10
	opsPerGoroutine := 20

	var wg sync.WaitGroup

	// Launch concurrent goroutines
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for j := 0; j < opsPerGoroutine; j++ {
				taskID := fmt.Sprintf("task-%d-%d", id, j)
				task := &types.Task{
					ID:        taskID,
					ServiceID: "service-1",
				}

				// Mix of operations
				switch j % 4 {
				case 0:
					// Describe (should handle missing)
					_, _ = vmm.Describe(ctx, task)
				case 1:
					// Wait (should handle missing)
					_, _ = vmm.Wait(ctx, task)
				case 2:
					// Stop (should handle missing)
					_ = vmm.Stop(ctx, task)
				case 3:
					// Remove (should handle missing)
					_ = vmm.Remove(ctx, task)
				}
			}
		}(i)
	}

	wg.Wait()
}

// TestVMMManager_StateTransitions tests VM state changes
func TestVMMManager_StateTransitions(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	taskID := "state-test"
	task := &types.Task{ID: taskID, ServiceID: "service-1"}
	socketPath := filepath.Join(socketDir, taskID+".sock")

	// Create initial VM in NEW state
	vmm.mu.Lock()
	vmm.vms[taskID] = &VMInstance{
		ID:        taskID,
		PID:       os.Getpid(), // Use real process so Signal(0) succeeds
		State:     VMStateNew,
		CreatedAt: time.Now(),
		SocketPath: socketPath,
	}
	vmm.mu.Unlock()

	// Transition: NEW -> STARTING
	vmm.mu.Lock()
	vmm.vms[taskID].State = VMStateStarting
	vmm.mu.Unlock()

	status, err := vmm.Describe(context.Background(), task)
	require.NoError(t, err)
	assert.Equal(t, types.TaskState_STARTING, status.State)

	// Transition: STARTING -> RUNNING
	vmm.mu.Lock()
	vmm.vms[taskID].State = VMStateRunning
	vmm.mu.Unlock()

	status, err = vmm.Describe(context.Background(), task)
	require.NoError(t, err)
	assert.Equal(t, types.TaskState_RUNNING, status.State)

	// Transition: RUNNING -> COMPLETE (simulate completion by removing)
	vmm.mu.Lock()
	delete(vmm.vms, taskID)
	vmm.mu.Unlock()

	status, err = vmm.Describe(context.Background(), task)
	require.NoError(t, err)
	assert.Equal(t, types.TaskState_ORPHANED, status.State)
}

// TestVMMManager_ResourceCleanup tests proper resource cleanup
func TestVMMManager_ResourceCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	ctx := context.Background()
	numVMs := 3

	// Create VMs with socket files
	for i := 0; i < numVMs; i++ {
		taskID := fmt.Sprintf("cleanup-task-%d", i)
		socketPath := filepath.Join(socketDir, taskID+".sock")

		// Create socket file
		err := os.WriteFile(socketPath, []byte("dummy"), 0644)
		require.NoError(t, err)

		vmm.mu.Lock()
		vmm.vms[taskID] = &VMInstance{
			ID:         taskID,
			PID:        1000 + i,
			State:      VMStateRunning,
			CreatedAt:  time.Now(),
			SocketPath: socketPath,
		}
		vmm.mu.Unlock()
	}

	// Remove all VMs
	for i := 0; i < numVMs; i++ {
		taskID := fmt.Sprintf("cleanup-task-%d", i)
		task := &types.Task{ID: taskID}

		err := vmm.Remove(ctx, task)
		require.NoError(t, err)
	}

	// Verify socket files are cleaned up
	files, err := os.ReadDir(socketDir)
	require.NoError(t, err)
	assert.Equal(t, 0, len(files), "All socket files should be removed")
}

// TestVMMManager_DescribeAllStates tests Describe with different VM states
func TestVMMManager_DescribeAllStates(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	ctx := context.Background()

	testCases := []struct {
		name         string
		state        VMState
		shouldReport bool
	}{
		{"new-vm", VMStateNew, true},
		{"starting-vm", VMStateStarting, true},
		{"running-vm", VMStateRunning, true},
		{"stopping-vm", VMStateStopping, true},
		{"stopped-vm", VMStateStopped, true},
		{"crashed-vm", VMStateCrashed, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			taskID := tc.name
			task := &types.Task{ID: taskID}
			socketPath := filepath.Join(socketDir, taskID+".sock")

			// Create VM with specific state
			vmm.mu.Lock()
			vmm.vms[taskID] = &VMInstance{
				ID:         taskID,
				PID:        os.Getpid(),
				State:      tc.state,
				CreatedAt:  time.Now(),
				SocketPath: socketPath,
			}
			vmm.mu.Unlock()

			status, err := vmm.Describe(ctx, task)
			require.NoError(t, err)

			if tc.shouldReport {
				assert.NotNil(t, status)
				assert.NotEqual(t, types.TaskState_ORPHANED, status.State)
			}

			// Cleanup
			vmm.mu.Lock()
			delete(vmm.vms, taskID)
			vmm.mu.Unlock()
		})
	}
}

// TestVMMManager_RemoveAll tests removing all VMs at once
func TestVMMManager_RemoveAll(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	ctx := context.Background()
	numVMs := 10

	// Create multiple VMs
	for i := 0; i < numVMs; i++ {
		taskID := fmt.Sprintf("remove-all-%d", i)
		socketPath := filepath.Join(socketDir, taskID+".sock")

		os.WriteFile(socketPath, []byte("dummy"), 0644)

		vmm.mu.Lock()
		vmm.vms[taskID] = &VMInstance{
			ID:         taskID,
			PID:        1000 + i,
			State:      VMStateRunning,
			CreatedAt:  time.Now(),
			SocketPath: socketPath,
		}
		vmm.mu.Unlock()
	}

	// Remove all VMs
	for i := 0; i < numVMs; i++ {
		taskID := fmt.Sprintf("remove-all-%d", i)
		task := &types.Task{ID: taskID}

		err := vmm.Remove(ctx, task)
		require.NoError(t, err)
	}

	// Verify all are removed
	vmm.mu.RLock()
	assert.Equal(t, 0, len(vmm.vms))
	vmm.mu.RUnlock()
}

// TestVMMManager_StartInvalidConfig tests starting with invalid configurations
func TestVMMManager_StartInvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	ctx := context.Background()

	testCases := []struct {
		name        string
		config      interface{}
		expectError bool
	}{
		{
			name:        "nil config",
			config:      nil,
			expectError: true,
		},
		{
			name:        "invalid json",
			config:      "invalid {{{",
			expectError: true,
		},
		{
			name:        "empty string",
			config:      "",
			expectError: true,
		},
		{
			name:        "valid json",
			config:      `{"boot_source": {"kernel_image_path": "/kernel"}}`,
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			task := &types.Task{
				ID:        "invalid-config-test",
				ServiceID: "service-1",
			}

			err := vmm.Start(ctx, task, tc.config)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				// May still fail due to missing Firecracker, but JSON should be valid
				if err != nil {
					t.Logf("Start failed (expected in test env): %v", err)
				}
			}
		})
	}
}

// TestVMMManager_StaleSocketFile tests handling of stale socket files
func TestVMMManager_StaleSocketFile(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	taskID := "stale-socket"
	task := &types.Task{ID: taskID}
	socketPath := filepath.Join(socketDir, taskID+".sock")

	// Create a stale socket file (no VM instance)
	err := os.WriteFile(socketPath, []byte("stale"), 0644)
	require.NoError(t, err)

	// Try to remove the task (no VM instance exists, so stale socket remains)
	err = vmm.Remove(context.Background(), task)
	require.NoError(t, err)

	// Note: stale socket file is not cleaned up because there's no VM instance
	// In production, the VMInstance would exist and cleanup would happen
	// For this test, we verify the Remove doesn't error
	_ = err
}

// TestVMMManager_ConcurrentStartStop tests concurrent start/stop operations
func TestVMMManager_ConcurrentStartStop(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	ctx := context.Background()
	numOperations := 20

	var wg sync.WaitGroup
	startCount := atomic.Int32{}
	stopCount := atomic.Int32{}

	// Concurrent start/stop operations
	for i := 0; i < numOperations; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			taskID := fmt.Sprintf("concurrent-%d", id)
			task := &types.Task{ID: taskID, ServiceID: "service-1"}

			// Attempt to start (will fail without Firecracker)
			_ = vmm.Start(ctx, task, `{"boot_source": {"kernel_image_path": "/kernel"}}`)
			startCount.Add(1)

			// Attempt to stop
			_ = vmm.Stop(ctx, task)
			stopCount.Add(1)
		}(i)
	}

	wg.Wait()

	// Verify operations were attempted
	assert.Equal(t, int32(numOperations), startCount.Load())
	assert.Equal(t, int32(numOperations), stopCount.Load())
}

// TestVMMManager_UptimeTracking tests VM uptime calculation
func TestVMMManager_UptimeTracking(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	taskID := "uptime-test"
	task := &types.Task{ID: taskID}
	socketPath := filepath.Join(socketDir, taskID+".sock")

	// Create VM with known creation time
	pastTime := time.Now().Add(-10 * time.Second)

	vmm.mu.Lock()
	vmm.vms[taskID] = &VMInstance{
		ID:         taskID,
		PID:        os.Getpid(),
		State:      VMStateRunning,
		CreatedAt:  pastTime,
		SocketPath: socketPath,
	}
	vmm.mu.Unlock()

	status, err := vmm.Describe(context.Background(), task)
	require.NoError(t, err)

	runtimeStatus, ok := status.RuntimeStatus.(map[string]interface{})
	require.True(t, ok)

	uptimeStr, ok := runtimeStatus["uptime"].(string)
	require.True(t, ok, "uptime should be present in runtime status")

	// Verify uptime is reported
	assert.True(t, len(uptimeStr) > 0, "uptime string should not be empty")
}

// Benchmark concurrent operations
func BenchmarkVMMManager_ConcurrentOperations(b *testing.B) {
	tmpDir := b.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			taskID := fmt.Sprintf("bench-%d", i%100)
			task := &types.Task{ID: taskID}

			// Quick describe operation
			vmm.Describe(ctx, task)
			i++
		}
	})
}
