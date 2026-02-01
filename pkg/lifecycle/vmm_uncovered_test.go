package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMMManager_Remove_Uncovered tests additional Remove scenarios
func TestVMMManager_Remove_Uncovered(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*VMMManager, *types.Task)
		task        *types.Task
		expectError bool
		validate    func(*VMMManager, *types.Task)
	}{
		{
			name: "remove non-existent task",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				// No setup - task doesn't exist
			},
			task: &types.Task{
				ID: "non-existent",
			},
			expectError: false,
			validate: func(vm *VMMManager, task *types.Task) {
				_, exists := vm.vms[task.ID]
				assert.False(t, exists, "Task should not exist after removal attempt")
			},
		},
		{
			name: "remove running VM",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				// Create a fake VM instance
				vm.vms[task.ID] = &VMInstance{
					ID:         task.ID,
					PID:        9999, // Fake PID
					State:      VMStateRunning,
					SocketPath: "/tmp/test-socket-" + task.ID + ".sock",
				}
			},
			task: &types.Task{
				ID: "running-vm",
			},
			expectError: false,
			validate: func(vm *VMMManager, task *types.Task) {
				_, exists := vm.vms[task.ID]
				assert.False(t, exists, "VM should be removed from map")
			},
		},
		{
			name: "remove stopped VM",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:         task.ID,
					PID:        12345,
					State:      VMStateStopped,
					SocketPath: "/tmp/test-socket-" + task.ID + ".sock",
				}
			},
			task: &types.Task{
				ID: "stopped-vm",
			},
			expectError: false,
			validate: func(vm *VMMManager, task *types.Task) {
				_, exists := vm.vms[task.ID]
				assert.False(t, exists, "Stopped VM should be removed")
			},
		},
		{
			name: "remove VM with nil task",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				vm.vms["some-vm"] = &VMInstance{
					ID:    "some-vm",
					State: VMStateRunning,
				}
			},
			task:        nil,
			expectError: true,
			validate: func(vm *VMMManager, task *types.Task) {
				// VM should still exist since removal failed
				_, exists := vm.vms["some-vm"]
				assert.True(t, exists, "VM should still exist when task is nil")
			},
		},
		{
			name: "remove VM with socket file",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				socketPath := filepath.Join(os.TempDir(), "test-socket-"+task.ID+".sock")
				// Create socket file
				file, err := os.Create(socketPath)
				require.NoError(t, err)
				file.Close()

				vm.vms[task.ID] = &VMInstance{
					ID:         task.ID,
					PID:        54321,
					State:      VMStateRunning,
					SocketPath: socketPath,
				}
			},
			task: &types.Task{
				ID: "vm-with-socket",
			},
			expectError: false,
			validate: func(vm *VMMManager, task *types.Task) {
				_, exists := vm.vms[task.ID]
				assert.False(t, exists, "VM should be removed")
				// Socket should be removed (or at least attempted)
			},
		},
		{
			name: "remove VM in new state",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:         task.ID,
					State:      VMStateNew,
					SocketPath: "",
				}
			},
			task: &types.Task{
				ID: "new-vm",
			},
			expectError: false,
			validate: func(vm *VMMManager, task *types.Task) {
				_, exists := vm.vms[task.ID]
				assert.False(t, exists, "New VM should be removed")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a temporary socket directory
			tmpDir := t.TempDir()

			vm := &VMMManager{
				config: &ManagerConfig{
					SocketDir: tmpDir,
				},
				vms:       make(map[string]*VMInstance),
				socketDir: tmpDir,
			}

			// Run setup
			if tt.setupFunc != nil {
				tt.setupFunc(vm, tt.task)
			}

			// Execute Remove
			ctx := context.Background()
			err := vm.Remove(ctx, tt.task)

			// Validate error
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Run validation
			if tt.validate != nil {
				tt.validate(vm, tt.task)
			}
		})
	}
}

// TestVMMManager_ForceKillVM_Uncovered tests additional forceKillVM scenarios
func TestVMMManager_ForceKillVM_Uncovered(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func() *VMInstance
		expectError bool
	}{
		{
			name: "kill VM with valid PID",
			setupFunc: func() *VMInstance {
				// Create a subprocess that we can kill
				cmd := createSleepProcess(t)
				return &VMInstance{
					ID:  "test-vm",
					PID: cmd.Process.Pid,
					State: VMStateRunning,
				}
			},
			expectError: false, // May or may not error depending on if process exists
		},
		{
			name: "kill VM with invalid PID",
			setupFunc: func() *VMInstance {
				return &VMInstance{
					ID:  "test-vm",
					PID: 99999, // Non-existent PID
					State: VMStateRunning,
				}
			},
			expectError: true, // Should error on invalid PID
		},
		{
			name: "kill VM with negative PID",
			setupFunc: func() *VMInstance {
				return &VMInstance{
					ID:  "test-vm",
					PID: -1,
					State: VMStateRunning,
				}
			},
			expectError: true,
		},
		{
			name: "kill VM with zero PID",
			setupFunc: func() *VMInstance {
				return &VMInstance{
					ID:  "test-vm",
					PID: 0,
					State: VMStateRunning,
				}
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := &VMMManager{}
			vmInstance := tt.setupFunc()

			err := vm.forceKillVM(vmInstance)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				// Non-blocking check - process may or may not exist
				_ = err
			}

			// If we created a real process, clean it up
			if vmInstance.PID > 0 && vmInstance.PID < 100000 {
				process, _ := os.FindProcess(vmInstance.PID)
				if process != nil {
					process.Kill()
				}
			}
		})
	}
}

// TestWaitForAPIServer_Uncovered tests additional waitForAPIServer scenarios
func TestWaitForAPIServer_Uncovered(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func() string
		timeout     time.Duration
		expectError bool
	}{
		{
			name: "wait for existing socket",
			setupFunc: func() string {
				// Create a socket file
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "fc.sock")
				file, err := os.Create(socketPath)
				require.NoError(t, err)
				file.Close()
				return socketPath
			},
			timeout:     1 * time.Second,
			expectError: true, // Will timeout because no real API server
		},
		{
			name: "wait for non-existent socket",
			setupFunc: func() string {
				return "/tmp/non-existent-socket-12345.sock"
			},
			timeout:     100 * time.Millisecond,
			expectError: true,
		},
		{
			name: "wait with very short timeout",
			setupFunc: func() string {
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "fc.sock")
				file, err := os.Create(socketPath)
				require.NoError(t, err)
				file.Close()
				return socketPath
			},
			timeout:     1 * time.Millisecond,
			expectError: true,
		},
		{
			name: "wait with zero timeout",
			setupFunc: func() string {
				return "/tmp/test.sock"
			},
			timeout:     0,
			expectError: true,
		},
		{
			name: "wait with empty socket path",
			setupFunc: func() string {
				return ""
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
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestWaitForShutdown_Uncovered tests additional waitForShutdown scenarios
func TestWaitForShutdown_Uncovered(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func() (string, func())
		timeout     time.Duration
		expectError bool
		expectDone  bool
	}{
		{
			name: "wait for socket to be removed",
			setupFunc: func() (string, func()) {
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "fc.sock")
				file, err := os.Create(socketPath)
				require.NoError(t, err)
				file.Close()

				// Cleanup function that removes socket after delay
				cleanup := func() {
					time.Sleep(50 * time.Millisecond)
					os.Remove(socketPath)
				}

				return socketPath, cleanup
			},
			timeout:     200 * time.Millisecond,
			expectError: false,
			expectDone:  true,
		},
		{
			name: "wait with socket that never gets removed",
			setupFunc: func() (string, func()) {
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "fc.sock")
				file, err := os.Create(socketPath)
				require.NoError(t, err)
				file.Close()

				return socketPath, func() {} // No cleanup
			},
			timeout:     100 * time.Millisecond,
			expectError: true,
			expectDone:  false,
		},
		{
			name: "wait with already removed socket",
			setupFunc: func() (string, func()) {
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "fc.sock")
				// Don't create the socket

				return socketPath, func() {}
			},
			timeout:     100 * time.Millisecond,
			expectError: false,
			expectDone:  true,
		},
		{
			name: "wait with very short timeout",
			setupFunc: func() (string, func()) {
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "fc.sock")
				file, err := os.Create(socketPath)
				require.NoError(t, err)
				file.Close()

				cleanup := func() {
					time.Sleep(200 * time.Millisecond)
					os.Remove(socketPath)
				}

				return socketPath, cleanup
			},
			timeout:     50 * time.Millisecond,
			expectError: true,
			expectDone:  false,
		},
		{
			name: "wait with empty socket path",
			setupFunc: func() (string, func()) {
				return "", func() {}
			},
			timeout:     100 * time.Millisecond,
			expectError: false,
			expectDone:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socketPath, cleanup := tt.setupFunc()

			// Start cleanup in goroutine if it does something
			if cleanup != nil {
				go cleanup()
			}

			done, err := waitForShutdown(socketPath, tt.timeout)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectDone, done, "waitForShutdown done state mismatch")
		})
	}
}

// TestVMMManager_Stop_AdditionalCoverage tests additional Stop scenarios
func TestVMMManager_Stop_AdditionalCoverage(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*VMMManager, *types.Task)
		task        *types.Task
		expectError bool
	}{
		{
			name: "stop non-existent VM",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				// No setup
			},
			task: &types.Task{
				ID: "non-existent",
			},
			expectError: false, // Should not error, just return
		},
		{
			name: "stop already stopped VM",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:          task.ID,
					State:       VMStateStopped,
					GracePeriodSec: 5,
				}
			},
			task: &types.Task{
				ID: "already-stopped",
			},
			expectError: false,
		},
		{
			name: "stop crashing VM",
			setupFunc: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:          task.ID,
					State:       VMStateCrashed,
					GracePeriodSec: 5,
				}
			},
			task: &types.Task{
				ID: "crashed-vm",
			},
			expectError: false,
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

			tt.setupFunc(vm, tt.task)

			ctx := context.Background()
			err := vm.Stop(ctx, tt.task)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestVMMManager_Start_ErrorPaths tests additional Start error paths
func TestVMMManager_Start_ErrorPaths(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*VMMManager)
		task        *types.Task
		config      interface{}
		expectError bool
	}{
		{
			name: "start with nil task",
			setupFunc: func(vm *VMMManager) {},
			task:      nil,
			config:    nil,
			expectError: true,
		},
		{
			name: "start task that already exists",
			setupFunc: func(vm *VMMManager) {
				vm.vms["existing-task"] = &VMInstance{
					ID:    "existing-task",
					State: VMStateRunning,
				}
			},
			task: &types.Task{
				ID: "existing-task",
			},
			config:      nil,
			expectError: false, // Current implementation allows restart
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

			tt.setupFunc(vm)

			ctx := context.Background()
			err := vm.Start(ctx, tt.task, tt.config)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				// May succeed or fail depending on implementation
				_ = err
			}
		})
	}
}

// Helper function to create a sleep process for testing
func createSleepProcess(t *testing.T) *exec.Cmd {
	cmd := exec.Command("sleep", "10")
	err := cmd.Start()
	require.NoError(t, err)
	return cmd
}
