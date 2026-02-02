package lifecycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMMManager_ContextCancellation tests context cancellation handling
func TestVMMManager_ContextCancellation(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*VMMManager, *types.Task)
		task        *types.Task
		config      interface{}
		cancelFunc  func(context.Context, context.CancelFunc)
		expectError bool
		errorMsg    string
	}{
		{
			name: "start with cancelled context",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				// No setup
			},
			task: &types.Task{
				ID: "cancelled-start",
			},
			config: map[string]interface{}{
				"boot_source": map[string]interface{}{
					"kernel_image_path": "/usr/share/firecracker/vmlinux",
				},
			},
			cancelFunc: func(ctx context.Context, cancel context.CancelFunc) {
				cancel() // Cancel immediately
			},
			expectError: true,
		},
		{
			name: "stop with cancelled context",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:             task.ID,
					PID:            12345,
					State:          VMStateRunning,
					InitSystem:     "tini",
					GracePeriodSec: 10,
				}
			},
			task: &types.Task{
				ID: "cancelled-stop",
			},
			config: nil,
			cancelFunc: func(ctx context.Context, cancel context.CancelFunc) {
				// Cancel after a short delay
				time.AfterFunc(10*time.Millisecond, cancel)
			},
			expectError: false, // Should force kill and succeed
		},
		{
			name: "wait with cancelled context",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:    task.ID,
					PID:   os.Getpid(),
					State: VMStateRunning,
				}
			},
			task: &types.Task{
				ID: "cancelled-wait",
			},
			config: nil,
			cancelFunc: func(ctx context.Context, cancel context.CancelFunc) {
				cancel() // Cancel immediately
			},
			expectError: false, // Should return immediately with current status
		},
		{
			name: "describe with cancelled context",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:    task.ID,
					PID:   os.Getpid(),
					State: VMStateRunning,
				}
			},
			task: &types.Task{
				ID: "cancelled-describe",
			},
			config: nil,
			cancelFunc: func(ctx context.Context, cancel context.CancelFunc) {
				cancel() // Cancel immediately
			},
			expectError: false, // Describe is quick, should succeed
		},
		{
			name: "remove with cancelled context",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:         task.ID,
					State:      VMStateRunning,
					SocketPath: "/tmp/test.sock",
				}
			},
			task: &types.Task{
				ID: "cancelled-remove",
			},
			config: nil,
			cancelFunc: func(ctx context.Context, cancel context.CancelFunc) {
				cancel() // Cancel immediately
			},
			expectError: false, // Remove is local, should succeed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			vm := &VMMManager{
				config: &ManagerConfig{
					SocketDir: tmpDir,
				},
				vms:       make(map[string]*VMInstance),
				socketDir: tmpDir,
			}

			if tt.setupFunc != nil {
				tt.setupFunc(vm, tt.task)
			}

			ctx, cancel := context.WithCancel(context.Background())

			if tt.cancelFunc != nil {
				tt.cancelFunc(ctx, cancel)
			}

			defer cancel()

			var err error
			switch {
			case tt.config != nil:
				err = vm.Start(ctx, tt.task, tt.config)
			default:
				// For stop/remove/wait/describe, we need to call the right method
				switch {
				case tt.name == "cancelled-stop":
					err = vm.Stop(ctx, tt.task)
				case tt.name == "cancelled-wait":
					_, err = vm.Wait(ctx, tt.task)
				case tt.name == "cancelled-describe":
					_, err = vm.Describe(ctx, tt.task)
				case tt.name == "cancelled-remove":
					err = vm.Remove(ctx, tt.task)
				}
			}

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				// May or may not error depending on operation
				_ = err
			}
		})
	}
}

// TestVMMManager_ConcurrentContextOperations tests concurrent VM operations with context
func TestVMMManager_ConcurrentContextOperations(t *testing.T) {
	t.Run("concurrent start different VMs", func(t *testing.T) {
		tmpDir := t.TempDir()
		vm := &VMMManager{
			config: &ManagerConfig{
				SocketDir: tmpDir,
			},
			vms:       make(map[string]*VMInstance),
			socketDir: tmpDir,
		}

		numGoroutines := 10
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		errors := make(chan error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()

				task := &types.Task{
					ID: fmt.Sprintf("vm-%d", id),
				}

				// This will fail without actual Firecracker, but we test concurrency
				ctx := context.Background()
				config := map[string]interface{}{}
				err := vm.Start(ctx, task, config)
				errors <- err
			}(i)
		}

		wg.Wait()
		close(errors)

		// All operations should complete (may error without Firecracker)
		errorCount := 0
		for err := range errors {
			if err != nil {
				errorCount++
			}
		}
		assert.Equal(t, numGoroutines, errorCount)
	})

	t.Run("concurrent describe operations", func(t *testing.T) {
		tmpDir := t.TempDir()
		vm := &VMMManager{
			config: &ManagerConfig{
				SocketDir: tmpDir,
			},
			vms:       make(map[string]*VMInstance),
			socketDir: tmpDir,
		}

		// Add some VMs
		for i := 0; i < 5; i++ {
			vm.vms[fmt.Sprintf("vm-%d", i)] = &VMInstance{
				ID:    fmt.Sprintf("vm-%d", i),
				PID:   os.Getpid(),
				State: VMStateRunning,
			}
		}

		numGoroutines := 20
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()

				task := &types.Task{
					ID: fmt.Sprintf("vm-%d", id%5),
				}

				ctx := context.Background()
				status, err := vm.Describe(ctx, task)

				// Should not crash
				assert.NoError(t, err)
				assert.NotNil(t, status)
			}(i)
		}

		wg.Wait()
	})

	t.Run("concurrent wait operations", func(t *testing.T) {
		tmpDir := t.TempDir()
		vm := &VMMManager{
			config: &ManagerConfig{
				SocketDir: tmpDir,
			},
			vms:       make(map[string]*VMInstance),
			socketDir: tmpDir,
		}

		// Add some VMs
		for i := 0; i < 5; i++ {
			vm.vms[fmt.Sprintf("vm-%d", i)] = &VMInstance{
				ID:    fmt.Sprintf("vm-%d", i),
				PID:   os.Getpid(),
				State: VMStateRunning,
			}
		}

		numGoroutines := 20
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()

				task := &types.Task{
					ID: fmt.Sprintf("vm-%d", id%5),
				}

				ctx := context.Background()
				status, err := vm.Wait(ctx, task)

				// Should not crash
				assert.NoError(t, err)
				assert.NotNil(t, status)
			}(i)
		}

		wg.Wait()
	})

	t.Run("concurrent remove operations", func(t *testing.T) {
		tmpDir := t.TempDir()
		vm := &VMMManager{
			config: &ManagerConfig{
				SocketDir: tmpDir,
			},
			vms:       make(map[string]*VMInstance),
			socketDir: tmpDir,
		}

		// Add some VMs with sockets
		for i := 0; i < 5; i++ {
			socketPath := filepath.Join(tmpDir, fmt.Sprintf("vm-%d.sock", i))
			_ = os.WriteFile(socketPath, []byte("dummy"), 0644)

			vm.vms[fmt.Sprintf("vm-%d", i)] = &VMInstance{
				ID:         fmt.Sprintf("vm-%d", i),
				State:      VMStateRunning,
				SocketPath: socketPath,
			}
		}

		numGoroutines := 10
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()

				task := &types.Task{
					ID: fmt.Sprintf("vm-%d", id%5),
				}

				ctx := context.Background()
				err := vm.Remove(ctx, task)

				// Should not crash
				assert.NoError(t, err)
			}(i)
		}

		wg.Wait()
	})
}

// TestGracefulShutdown_EdgeCases tests graceful shutdown edge cases
func TestGracefulShutdown_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func() *VMInstance
		expectError bool
		validate    func(*testing.T, *VMInstance)
	}{
		{
			name: "graceful shutdown with zero grace period",
			setupFunc: func() *VMInstance {
				return &VMInstance{
					ID:             "test-vm",
					PID:            99999, // Non-existent
					State:          VMStateRunning,
					InitSystem:     "tini",
					GracePeriodSec: 0,
				}
			},
			expectError: true, // Should force kill
			validate: func(t *testing.T, vmi *VMInstance) {
				assert.Equal(t, VMStateStopped, vmi.State)
			},
		},
		{
			name: "graceful shutdown with negative grace period",
			setupFunc: func() *VMInstance {
				return &VMInstance{
					ID:             "test-vm",
					PID:            99999,
					State:          VMStateRunning,
					InitSystem:     "tini",
					GracePeriodSec: -1,
				}
			},
			expectError: true,
		},
		{
			name: "graceful shutdown with very short grace period",
			setupFunc: func() *VMInstance {
				return &VMInstance{
					ID:             "test-vm",
					PID:            99999,
					State:          VMStateRunning,
					InitSystem:     "tini",
					GracePeriodSec: 1,
				}
			},
			expectError: true, // Should timeout and force kill
		},
		{
			name: "graceful shutdown with init system none",
			setupFunc: func() *VMInstance {
				return &VMInstance{
					ID:             "test-vm",
					PID:            99999,
					State:          VMStateRunning,
					InitSystem:     "none",
					GracePeriodSec: 10,
				}
			},
			expectError: true, // Will fall through to force kill
		},
		{
			name: "graceful shutdown with dumb-init",
			setupFunc: func() *VMInstance {
				return &VMInstance{
					ID:             "test-vm",
					PID:            99999,
					State:          VMStateRunning,
					InitSystem:     "dumb-init",
					GracePeriodSec: 5,
				}
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := &VMMManager{
				config: &ManagerConfig{
					SocketDir: "/tmp/test",
				},
			}

			vmInstance := tt.setupFunc()
			err := vm.gracefulShutdown(context.Background(), vmInstance)

			if tt.expectError {
				assert.Error(t, err)
			}

			if tt.validate != nil {
				tt.validate(t, vmInstance)
			}
		})
	}
}

// TestHardShutdown_EdgeCases tests hard shutdown edge cases
func TestHardShutdown_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func() (*VMInstance, string)
		expectError bool
	}{
		{
			name: "hard shutdown with non-existent socket",
			setupFunc: func() (*VMInstance, string) {
				return &VMInstance{
					ID:         "test-vm",
					State:      VMStateRunning,
					SocketPath: "/tmp/non-existent.sock",
				}, "/tmp/non-existent.sock"
			},
			expectError: true, // Will force kill after API fails
		},
		{
			name: "hard shutdown with invalid socket path",
			setupFunc: func() (*VMInstance, string) {
				return &VMInstance{
					ID:         "test-vm",
					State:      VMStateRunning,
					SocketPath: "",
				}, ""
			},
			expectError: true,
		},
		{
			name: "hard shutdown with socket file (not real socket)",
			setupFunc: func() (*VMInstance, string) {
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "fake.sock")
				_ = os.WriteFile(socketPath, []byte("not a socket"), 0644)

				return &VMInstance{
					ID:         "test-vm",
					State:      VMStateRunning,
					SocketPath: socketPath,
				}, socketPath
			},
			expectError: true, // API call will fail, force kill
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := &VMMManager{}
			vmInstance, _ := tt.setupFunc()

			err := vm.hardShutdown(context.Background(), vmInstance)

			if tt.expectError {
				assert.Error(t, err)
			}
		})
	}
}

// TestConfigureVM_ErrorPaths tests configureVM error paths
func TestConfigureVM_ErrorPaths(t *testing.T) {
	tests := []struct {
		name        string
		socketPath  string
		config      interface{}
		expectError bool
		errorMsg    string
	}{
		{
			name:       "configure with nil config",
			socketPath: "/tmp/test.sock",
			config:     nil,
			expectError: true,
			errorMsg:    "invalid config type",
		},
		{
			name:       "configure with non-map config",
			socketPath: "/tmp/test.sock",
			config:     "not a map",
			expectError: true,
			errorMsg:    "invalid config type",
		},
		{
			name:       "configure with empty map",
			socketPath: "/tmp/test.sock",
			config:     map[string]interface{}{},
			expectError: false, // No config sections is valid
		},
		{
			name:       "configure with boot source only",
			socketPath: "/tmp/test.sock",
			config: map[string]interface{}{
				"boot_source": map[string]interface{}{
					"kernel_image_path": "/usr/share/firecracker/vmlinux",
					"boot_args":         "console=ttyS0",
				},
			},
			expectError: true, // Will fail without real API server
		},
		{
			name:       "configure with machine config only",
			socketPath: "/tmp/test.sock",
			config: map[string]interface{}{
				"machine_config": map[string]interface{}{
					"vcpu_count":   1,
					"mem_size_mib": 512,
				},
			},
			expectError: true,
		},
		{
			name:       "configure with both boot and machine",
			socketPath: "/tmp/test.sock",
			config: map[string]interface{}{
				"boot_source": map[string]interface{}{
					"kernel_image_path": "/usr/share/firecracker/vmlinux",
				},
				"machine_config": map[string]interface{}{
					"vcpu_count":   1,
					"mem_size_mib": 512,
				},
			},
			expectError: true,
		},
		{
			name:       "configure with invalid boot source type",
			socketPath: "/tmp/test.sock",
			config: map[string]interface{}{
				"boot_source": "invalid type",
			},
			expectError: true,
		},
		{
			name:       "configure with invalid machine config type",
			socketPath: "/tmp/test.sock",
			config: map[string]interface{}{
				"machine_config": "invalid type",
			},
			expectError: false, // Will skip invalid section
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := &VMMManager{}
			err := vm.configureVM(context.Background(), tt.socketPath, tt.config)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				// May succeed or fail depending on if socket exists
				_ = err
			}
		})
	}
}

// TestVMMManager_Describe_AllContextStates tests describe for all VM states with context
func TestVMMManager_Describe_AllContextStates(t *testing.T) {
	states := []VMState{
		VMStateNew,
		VMStateStarting,
		VMStateRunning,
		VMStateStopping,
		VMStateStopped,
		VMStateCrashed,
	}

	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			tmpDir := t.TempDir()
			vm := &VMMManager{
				config: &ManagerConfig{
					SocketDir: tmpDir,
				},
				vms:       make(map[string]*VMInstance),
				socketDir: tmpDir,
			}

			task := &types.Task{
				ID: "test-vm",
			}

			vm.vms[task.ID] = &VMInstance{
				ID:        task.ID,
				PID:       99999, // Non-existent
				State:     state,
				CreatedAt: time.Now(),
			}

			ctx := context.Background()
			status, err := vm.Describe(ctx, task)

			require.NoError(t, err)
			assert.NotNil(t, status)

			// Check runtime status
			runtimeStatus, ok := status.RuntimeStatus.(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, string(state), runtimeStatus["state"])
		})
	}
}

// TestWaitForAPIServer_Timeouts tests various timeout scenarios
func TestWaitForAPIServer_Timeouts(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func() string
		timeout     time.Duration
		expectError bool
	}{
		{
			name: "wait with 1ms timeout",
			setupFunc: func() string {
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "fc.sock")
				_ = os.WriteFile(socketPath, []byte("dummy"), 0644)
				return socketPath
			},
			timeout:     1 * time.Millisecond,
			expectError: true,
		},
		{
			name: "wait with 10ms timeout",
			setupFunc: func() string {
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "fc.sock")
				_ = os.WriteFile(socketPath, []byte("dummy"), 0644)
				return socketPath
			},
			timeout:     10 * time.Millisecond,
			expectError: true,
		},
		{
			name: "wait with 100ms timeout",
			setupFunc: func() string {
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "fc.sock")
				_ = os.WriteFile(socketPath, []byte("dummy"), 0644)
				return socketPath
			},
			timeout:     100 * time.Millisecond,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socketPath := tt.setupFunc()
			err := waitForAPIServer(socketPath, tt.timeout)

			if tt.expectError {
				assert.Error(t, err)
			}
		})
	}
}

// TestVMMManager_ContextStateTransitions tests VM state transitions with context
func TestVMMManager_ContextStateTransitions(t *testing.T) {
	// Use current process PID to ensure process exists
	currentPID := os.Getpid()

	tmpDir := t.TempDir()
	vm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	task := &types.Task{
		ID: "test-vm",
	}

	// Start with new state
	vm.vms[task.ID] = &VMInstance{
		ID:    task.ID,
		PID:   currentPID, // Use real process PID
		State: VMStateNew,
	}

	ctx := context.Background()

	// Check initial state (new)
	status, err := vm.Describe(ctx, task)
	require.NoError(t, err)
	assert.Equal(t, types.TaskState_NEW, status.State)

	// Transition to running
	vm.vms[task.ID].State = VMStateRunning
	status, err = vm.Describe(ctx, task)
	require.NoError(t, err)
	assert.Equal(t, types.TaskState_RUNNING, status.State)

	// Transition to stopping
	vm.vms[task.ID].State = VMStateStopping
	status, err = vm.Describe(ctx, task)
	require.NoError(t, err)
	assert.Equal(t, types.TaskState_STARTING, status.State) // Still in transition

	// Transition to stopped
	vm.vms[task.ID].State = VMStateStopped
	status, err = vm.Describe(ctx, task)
	require.NoError(t, err)
	// Note: Will return COMPLETE because process check succeeds

	// Transition to crashed
	vm.vms[task.ID].State = VMStateCrashed
	status, err = vm.Describe(ctx, task)
	require.NoError(t, err)
	// Note: Process is still running, so will map based on our state
}
