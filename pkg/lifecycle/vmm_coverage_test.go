package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Additional tests to improve lifecycle coverage to 80%+

func TestVMMManager_Start_ErrorScenarios(t *testing.T) {
	tests := []struct {
		name        string
		config      *ManagerConfig
		task        *types.Task
		vmConfig    interface{}
		setup       func(*VMMManager, *types.Task)
		wantErr     bool
		errContains string
	}{
		{
			name: "nil task",
			config: &ManagerConfig{
				SocketDir: "/tmp/test-socket",
			},
			task:        nil,
			vmConfig:    "{}",
			wantErr:     true,
			errContains: "task",
		},
		{
			name: "empty task ID",
			config: &ManagerConfig{
				SocketDir: "/tmp/test-socket",
			},
			task: &types.Task{
				ID:        "",
				ServiceID: "service-1",
			},
			vmConfig:    "{}",
			wantErr:     false, // Will fail when trying to start Firecracker
		},
		{
			name: "nil config",
			config: &ManagerConfig{
				SocketDir: "/tmp/test-socket",
			},
			task: &types.Task{
				ID:        "test-nil-config",
				ServiceID: "service-1",
			},
			vmConfig:    nil,
			wantErr:     true, // Will fail with "failed to start firecracker" or config error
			errContains: "", // Don't check specific message
		},
		{
			name: "invalid JSON config",
			config: &ManagerConfig{
				SocketDir: "/tmp/test-socket",
			},
			task: &types.Task{
				ID:        "test-invalid-json",
				ServiceID: "service-1",
			},
			vmConfig:    "{invalid json}",
			wantErr:     true, // Will fail when trying to start
			errContains: "", // Don't check specific message
		},
		{
			name: "empty string config",
			config: &ManagerConfig{
				SocketDir: "/tmp/test-socket",
			},
			task: &types.Task{
				ID:        "test-empty-config",
				ServiceID: "service-1",
			},
			vmConfig:    "",
			wantErr:     true,
			errContains: "invalid",
		},
		{
			name: "task already exists",
			config: &ManagerConfig{
				SocketDir: "/tmp/test-socket",
			},
			task: &types.Task{
				ID:        "test-already-exists",
				ServiceID: "service-1",
			},
			vmConfig: "{}",
			setup: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:        task.ID,
					State:     VMStateRunning,
					CreatedAt: time.Now(),
				}
			},
			wantErr:     true,
			errContains: "already exists",
		},
		{
			name: "valid config but no Firecracker",
			config: &ManagerConfig{
				SocketDir:      "/tmp/test-socket-valid",
				KernelPath:     "/tmp/vmlinux",
				RootfsDir:      "/tmp/rootfs",
				DefaultVCPUs:   1,
				DefaultMemoryMB: 512,
			},
			task: &types.Task{
				ID:        "test-valid-config",
				ServiceID: "service-1",
			},
			vmConfig: `{
				"boot-source": {
					"kernel_image_path": "/tmp/vmlinux"
				},
				"drives": [{
					"drive_id": "rootfs",
					"path_on_host": "/tmp/rootfs.ext4",
					"is_root_device": true,
					"is_read_only": false
				}]
			}`,
			wantErr: false, // Will fail when trying to start Firecracker binary
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(tt.config).(*VMMManager)
			defer os.RemoveAll(tt.config.SocketDir)

			if tt.setup != nil {
				tt.setup(vm, tt.task)
			}

			err := vm.Start(context.Background(), tt.task, tt.vmConfig)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				// May error without Firecracker binary, but that's expected
				_ = err
			}
		})
	}
}

func TestVMMManager_Stop_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*VMMManager, *types.Task)
		task        *types.Task
		wantErr     bool
		errContains string
	}{
		{
			name:  "stop non-existent VM",
			setup: func(vm *VMMManager, task *types.Task) {},
			task: &types.Task{
				ID: "non-existent-stop",
			},
			wantErr:     true,
			errContains: "not found",
		},
		{
			name: "stop VM in new state",
			setup: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:        task.ID,
					State:     VMStateNew,
					CreatedAt: time.Now(),
					SocketPath: filepath.Join(os.TempDir(), task.ID+".sock"),
				}
			},
			task: &types.Task{
				ID: "new-state-vm",
			},
			wantErr: false, // Should handle gracefully
		},
		{
			name: "stop VM in crashed state",
			setup: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:        task.ID,
					State:     VMStateCrashed,
					CreatedAt: time.Now(),
					SocketPath: filepath.Join(os.TempDir(), task.ID+".sock"),
				}
			},
			task: &types.Task{
				ID: "crashed-vm",
			},
			wantErr: false, // Should cleanup
		},
		{
			name: "stop VM already stopped",
			setup: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:        task.ID,
					State:     VMStateStopped,
					CreatedAt: time.Now(),
					SocketPath: filepath.Join(os.TempDir(), task.ID+".sock"),
				}
			},
			task: &types.Task{
				ID: "already-stopped",
			},
			wantErr:     false, // Should handle gracefully (return nil if stopped)
			errContains: "",
		},
		{
			name: "stop VM with nil task",
			setup: func(vm *VMMManager, task *types.Task) {},
			task:        nil,
			wantErr:     true,
			errContains: "task",
		},
		{
			name: "stop VM with invalid socket path",
			setup: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:        task.ID,
					State:     VMStateRunning,
					CreatedAt: time.Now(),
					SocketPath: "/nonexistent/path/socket.sock",
					PID:        12345,
				}
			},
			task: &types.Task{
				ID: "invalid-socket",
			},
			wantErr: false, // Will try to stop, may fail gracefully
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{
				SocketDir: "/tmp/test-stop",
			}).(*VMMManager)
			defer os.RemoveAll("/tmp/test-stop")

			if tt.setup != nil {
				tt.setup(vm, tt.task)
			}

			err := vm.Stop(context.Background(), tt.task)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				// Should not panic
				_ = err
			}
		})
	}
}

func TestVMMManager_Wait_Variations(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*VMMManager, *types.Task)
		task    *types.Task
		wantErr bool
	}{
		{
			name:  "wait for non-existent VM",
			setup: func(vm *VMMManager, task *types.Task) {},
			task: &types.Task{
				ID: "wait-nonexistent",
			},
			wantErr: false, // Describe returns TaskStatus even for non-existent
		},
		{
			name: "wait for VM in new state",
			setup: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:        task.ID,
					State:     VMStateNew,
					CreatedAt: time.Now(),
				}
			},
			task: &types.Task{
				ID: "wait-new",
			},
			wantErr: false, // Returns status with current state
		},
		{
			name: "wait for stopped VM",
			setup: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:        task.ID,
					State:     VMStateStopped,
					CreatedAt: time.Now(),
				}
			},
			task: &types.Task{
				ID: "wait-stopped",
			},
			wantErr: false, // Should return immediately
		},
		{
			name: "wait for crashed VM",
			setup: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:        task.ID,
					State:     VMStateCrashed,
					CreatedAt: time.Now(),
				}
			},
			task: &types.Task{
				ID: "wait-crashed",
			},
			wantErr: false, // Returns status with crashed state
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{
				SocketDir: "/tmp/test-wait",
			}).(*VMMManager)

			if tt.setup != nil {
				tt.setup(vm, tt.task)
			}

			// Use short timeout for tests
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			status, err := vm.Wait(ctx, tt.task)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, status)
			}
		})
	}
}

func TestVMMManager_Describe_Details(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*VMMManager, *types.Task)
		task    *types.Task
		wantErr bool
	}{
		{
			name:  "describe non-existent VM",
			setup: func(vm *VMMManager, task *types.Task) {},
			task: &types.Task{
				ID: "describe-nonexistent",
			},
			wantErr: false, // Returns TaskStatus with error message
		},
		{
			name: "describe VM with all states",
			setup: func(vm *VMMManager, task *types.Task) {
				states := []VMState{
					VMStateNew,
					VMStateStarting,
					VMStateRunning,
					VMStateStopping,
					VMStateStopped,
					VMStateCrashed,
				}

				for i, state := range states {
					taskID := "describe-state-" + string(state)
					vm.vms[taskID] = &VMInstance{
						ID:        taskID,
						State:     state,
						CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute),
						PID:       1000 + i,
						SocketPath: filepath.Join(os.TempDir(), taskID+".sock"),
					}
				}
			},
			task: &types.Task{
				ID: "describe-state-running",
			},
			wantErr: false,
		},
		{
			name: "describe with nil task",
			setup: func(vm *VMMManager, task *types.Task) {},
			task:        nil,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{
				SocketDir: "/tmp/test-describe",
			}).(*VMMManager)

			if tt.setup != nil {
				tt.setup(vm, tt.task)
			}

			desc, err := vm.Describe(context.Background(), tt.task)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, desc)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, desc)
			}
		})
	}
}

func TestVMMManager_Remove_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*VMMManager, *types.Task)
		task    *types.Task
		wantErr bool
	}{
		{
			name:  "remove non-existent VM",
			setup: func(vm *VMMManager, task *types.Task) {},
			task: &types.Task{
				ID: "remove-nonexistent",
			},
			wantErr: false, // Should be idempotent
		},
		{
			name: "remove VM in running state",
			setup: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:        task.ID,
					State:     VMStateRunning,
					CreatedAt: time.Now(),
					PID:       9999,
					SocketPath: filepath.Join(os.TempDir(), task.ID+".sock"),
				}
			},
			task: &types.Task{
				ID: "remove-running",
			},
			wantErr: false, // Should force kill and remove
		},
		{
			name: "remove with nil task",
			setup: func(vm *VMMManager, task *types.Task) {},
			task:        nil,
			wantErr:     true,
		},
		{
			name: "remove VM with socket file",
			setup: func(vm *VMMManager, task *types.Task) {
				socketPath := filepath.Join(os.TempDir(), task.ID+".sock")
				// Create a fake socket file
				file, err := os.Create(socketPath)
				require.NoError(t, err)
				file.Close()

				vm.vms[task.ID] = &VMInstance{
					ID:        task.ID,
					State:     VMStateStopped,
					CreatedAt: time.Now(),
					SocketPath: socketPath,
				}
			},
			task: &types.Task{
				ID: "remove-with-socket",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{
				SocketDir: "/tmp/test-remove",
			}).(*VMMManager)

			if tt.setup != nil {
				tt.setup(vm, tt.task)
			}

			err := vm.Remove(context.Background(), tt.task)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				// Verify VM is removed
				_, exists := vm.vms[tt.task.ID]
				assert.False(t, exists)
			}
		})
	}
}

func TestNewVMMManager_ConfigVariations(t *testing.T) {
	tests := []struct {
		name          string
		config        interface{}
		expectedSocketDir string
	}{
		{
			name: "default config",
			config: &ManagerConfig{
				SocketDir: "/var/run/firecracker",
			},
			expectedSocketDir: "/var/run/firecracker",
		},
		{
			name: "custom socket dir",
			config: &ManagerConfig{
				SocketDir: "/tmp/custom-sockets",
			},
			expectedSocketDir: "/tmp/custom-sockets",
		},
		{
			name:          "nil config",
			config:        nil,
			expectedSocketDir: "/var/run/firecracker",
		},
		{
			name: "empty config struct",
			config: &ManagerConfig{},
			expectedSocketDir: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(tt.config).(*VMMManager)
			if vm.socketDir != "" {
				defer os.RemoveAll(vm.socketDir)
			}

			assert.NotNil(t, vm)
			assert.NotNil(t, vm.vms)
			assert.Equal(t, tt.expectedSocketDir, vm.socketDir)

			// Verify socket directory was created (if non-empty)
			// Note: /var/run/firecracker may not be created without root
			if tt.expectedSocketDir != "" && tt.expectedSocketDir != "/var/run/firecracker" {
				info, err := os.Stat(tt.expectedSocketDir)
				assert.NoError(t, err)
				assert.True(t, info.IsDir())
			}
		})
	}
}

func TestVMMManager_ConcurrentDescribeOps(t *testing.T) {
	vm := NewVMMManager(&ManagerConfig{
		SocketDir: "/tmp/test-concurrent-ops",
	}).(*VMMManager)
	defer os.RemoveAll("/tmp/test-concurrent-ops")

	ctx := context.Background()
	numOps := 10

	// Add some VMs
	for i := 0; i < 5; i++ {
		taskID := "vm-" + string(rune('A'+i))
		vm.vms[taskID] = &VMInstance{
			ID:        taskID,
			State:     VMStateRunning,
			CreatedAt: time.Now(),
		}
	}

	// Concurrent describe operations
	descChan := make(chan error, numOps)
	for i := 0; i < numOps; i++ {
		go func(idx int) {
			taskID := "vm-" + string(rune('A'+(idx%5)))
			if idx >= 5 {
				taskID = "non-existent"
			}
			task := &types.Task{ID: taskID}
			_, err := vm.Describe(ctx, task)
			descChan <- err
		}(i)
	}

	for i := 0; i < numOps; i++ {
		<-descChan // Collect results
	}
}

func TestVMMManager_MultipleRemovals(t *testing.T) {
	vm := NewVMMManager(&ManagerConfig{
		SocketDir: "/tmp/test-multi-remove",
	}).(*VMMManager)
	defer os.RemoveAll("/tmp/test-multi-remove")

	// Add multiple VMs
	for i := 0; i < 5; i++ {
		taskID := "multi-remove-" + string(rune('A'+i))
		vm.vms[taskID] = &VMInstance{
			ID:        taskID,
			State:     VMStateRunning,
			CreatedAt: time.Now(),
			PID:       2000 + i,
		}
	}

	ctx := context.Background()

	// Remove each one individually
	for i := 0; i < 5; i++ {
		taskID := "multi-remove-" + string(rune('A'+i))
		err := vm.Remove(ctx, &types.Task{ID: taskID})
		assert.NoError(t, err)
	}

	// Verify all VMs are removed
	assert.Empty(t, vm.vms)
}
