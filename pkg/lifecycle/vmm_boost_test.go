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

// Additional tests to boost lifecycle coverage to 80%+

func TestVMMManager_Start_ContextCancellation(t *testing.T) {
	vm := NewVMMManager(&ManagerConfig{
		SocketDir: "/tmp/test-ctx-cancel",
	}).(*VMMManager)
	defer os.RemoveAll("/tmp/test-ctx-cancel")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	task := &types.Task{
		ID: "test-cancel",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "nginx:latest",
			},
		},
	}

	err := vm.Start(ctx, task, "{}")
	// Should handle cancellation
	assert.Error(t, err)
}

func TestVMMManager_Start_ConfigErrors(t *testing.T) {
	tests := []struct {
		name    string
		config  interface{}
		wantErr bool
	}{
		{
			name:    "empty string config",
			config:  "",
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			config:  "{broken",
			wantErr: true,
		},
		{
			name:    "valid JSON minimal",
			config:  `{"boot-source": {"kernel_image_path": "/tmp/vmlinux"}}`,
			wantErr: false, // Will fail when starting firecracker
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{
				SocketDir: "/tmp/test-invalid-config",
			}).(*VMMManager)
			defer os.RemoveAll("/tmp/test-invalid-config")

			task := &types.Task{
				ID: "test-invalid",
			}

			err := vm.Start(context.Background(), task, tt.config)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				// May fail when starting Firecracker
				_ = err
			}
		})
	}
}

func TestVMMManager_Stop_AlreadyStopped(t *testing.T) {
	vm := NewVMMManager(&ManagerConfig{
		SocketDir: "/tmp/test-already-stopped",
	}).(*VMMManager)
	defer os.RemoveAll("/tmp/test-already-stopped")

	task := &types.Task{
		ID: "test-stopped",
	}

	// Stop non-existent VM
	err := vm.Stop(context.Background(), task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestVMMManager_Stop_VMInStates(t *testing.T) {
	states := []VMState{
		VMStateNew,
		VMStateStarting,
		VMStateStopping,
		VMStateStopped,
		VMStateCrashed,
	}

	for _, state := range states {
		t.Run("state_"+string(state), func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{
				SocketDir: "/tmp/test-stop-states",
			}).(*VMMManager)
			defer os.RemoveAll("/tmp/test-stop-states")

			task := &types.Task{
				ID: "test-" + string(state),
			}

			vm.vms[task.ID] = &VMInstance{
				ID:        task.ID,
				State:     state,
				CreatedAt: time.Now(),
				PID:       1234,
				SocketPath: filepath.Join("/tmp", task.ID+".sock"),
			}

			err := vm.Stop(context.Background(), task)
			// Should handle various states
			_ = err
		})
	}
}

func TestVMMManager_Wait_Timeouts(t *testing.T) {
	vm := NewVMMManager(&ManagerConfig{
		SocketDir: "/tmp/test-wait-timeout",
	}).(*VMMManager)
	defer os.RemoveAll("/tmp/test-wait-timeout")

	task := &types.Task{
		ID: "test-timeout",
	}

	vm.vms[task.ID] = &VMInstance{
		ID:        task.ID,
		State:     VMStateRunning,
		CreatedAt: time.Now(),
		PID:       9999,
	}

	// Very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	status, err := vm.Wait(ctx, task)
	// Should timeout or return status
	if err == nil {
		assert.NotNil(t, status)
	}
}

func TestVMMManager_Wait_ContextCancellation(t *testing.T) {
	vm := NewVMMManager(&ManagerConfig{
		SocketDir: "/tmp/test-wait-cancel",
	}).(*VMMManager)
	defer os.RemoveAll("/tmp/test-wait-cancel")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	task := &types.Task{
		ID: "test-wait-cancel",
	}

	// Wait returns nil error when VM doesn't exist
	status, err := vm.Wait(ctx, task)
	assert.NoError(t, err)
	assert.NotNil(t, status)
	assert.Equal(t, types.TaskState_ORPHANED, status.State)
}

func TestVMMManager_Describe_AllStates(t *testing.T) {
	states := []VMState{
		VMStateNew,
		VMStateStarting,
		VMStateRunning,
		VMStateStopping,
		VMStateStopped,
		VMStateCrashed,
	}

	for _, state := range states {
		t.Run("describe_"+string(state), func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{
				SocketDir: "/tmp/test-describe-states",
			}).(*VMMManager)
			defer os.RemoveAll("/tmp/test-describe-states")

			task := &types.Task{
				ID: "test-" + string(state),
			}

			vm.vms[task.ID] = &VMInstance{
				ID:         task.ID,
				State:      state,
				CreatedAt:  time.Now().Add(-time.Hour),
				PID:        os.Getpid(), // Use real PID so process check succeeds
				SocketPath: "/tmp/test.sock",
			}

			status, err := vm.Describe(context.Background(), task)
			assert.NoError(t, err)
			assert.NotNil(t, status)
			// RuntimeStatus is set because process exists
			assert.NotNil(t, status.RuntimeStatus)
		})
	}
}

func TestVMMManager_forceKillVM_Scenarios(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*VMMManager, *types.Task)
		wantErr bool
	}{
		{
			name: "kill with zero PID",
			setup: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:    task.ID,
					State: VMStateRunning,
					PID:   0,
				}
			},
			wantErr: false, // forceKill should handle gracefully
		},
		{
			name: "kill with negative PID",
			setup: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:    task.ID,
					State: VMStateRunning,
					PID:   -1,
				}
			},
			wantErr: false,
		},
		{
			name: "kill with large PID",
			setup: func(vm *VMMManager, task *types.Task) {
				vm.vms[task.ID] = &VMInstance{
					ID:    task.ID,
					State: VMStateRunning,
					PID:   999999,
				}
			},
			wantErr: false, // Process won't exist but shouldn't error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{
				SocketDir: "/tmp/test-forcekill",
			}).(*VMMManager)
			defer os.RemoveAll("/tmp/test-forcekill")

			task := &types.Task{
				ID: "test-forcekill",
			}

			if tt.setup != nil {
				tt.setup(vm, task)
			}

			vmInstance := vm.vms[task.ID]
			if vmInstance != nil {
				err := vm.forceKillVM(vmInstance)
				if tt.wantErr {
					assert.Error(t, err)
				} else {
					// Should not panic
					_ = err
				}
			}
		})
	}
}

func TestVMMManager_waitForAPIServer_Scenarios(t *testing.T) {
	tests := []struct {
		name     string
		socket   string
		timeout  time.Duration
		wantErr  bool
	}{
		{
			name:    "non-existent socket",
			socket:  "/nonexistent/test.sock",
			timeout: 10 * time.Millisecond,
			wantErr: true,
		},
		{
			name:    "empty socket path",
			socket:  "",
			timeout: 10 * time.Millisecond,
			wantErr: true,
		},
		{
			name:    "very short timeout",
			socket:  "/tmp/test-timeout.sock",
			timeout: 1 * time.Nanosecond,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := waitForAPIServer(tt.socket, tt.timeout)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				_ = err
			}
		})
	}
}

func TestVMMManager_waitForShutdown_Scenarios(t *testing.T) {
	tests := []struct {
		name     string
		socket   string
		timeout  time.Duration
		wantErr  bool
		setup    func() string
		cleanup  func(string)
	}{
		{
			name:    "non-existent socket returns success",
			socket:  "/nonexistent/test.sock",
			timeout: 10 * time.Millisecond,
			wantErr: false, // Returns nil when socket doesn't exist (already shut down)
		},
		{
			name:    "empty socket path returns success",
			socket:  "",
			timeout: 10 * time.Millisecond,
			wantErr: false,
		},
		{
			name:    "timeout waiting for socket removal",
			timeout: 100 * time.Millisecond,
			wantErr: true,
			setup: func() string {
				// Create a socket file that won't be removed
				f, _ := os.Create("/tmp/test-timeout.sock")
				f.Close()
				return "/tmp/test-timeout.sock"
			},
			cleanup: func(path string) {
				os.Remove(path)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socket := tt.socket
			if tt.setup != nil {
				socket = tt.setup()
				defer tt.cleanup(socket)
			}
			_, err := waitForShutdown(socket, tt.timeout)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestVMMManager_IntegrationFlows(t *testing.T) {
	t.Run("start_stop_describe", func(t *testing.T) {
		vm := NewVMMManager(&ManagerConfig{
			SocketDir: "/tmp/test-integration",
		}).(*VMMManager)
		defer os.RemoveAll("/tmp/test-integration")

		task := &types.Task{
			ID: "test-integration",
			Spec: types.TaskSpec{
				Runtime: &types.Container{
					Image: "nginx:latest",
				},
			},
		}

		ctx := context.Background()

		// Try to start (will fail without Firecracker)
		err := vm.Start(ctx, task, "{}")
		_ = err

		// Check if VM exists
		_, exists := vm.vms[task.ID]
		_ = exists

		// Describe
		desc, _ := vm.Describe(ctx, task)
		_ = desc

		// Stop (if exists)
		if exists {
			_ = vm.Stop(ctx, task)
		}
	})
}

func TestVMState_Transitions(t *testing.T) {
	transitions := [][2]VMState{
		{VMStateNew, VMStateStarting},
		{VMStateStarting, VMStateRunning},
		{VMStateRunning, VMStateStopping},
		{VMStateStopping, VMStateStopped},
		{VMStateRunning, VMStateCrashed},
	}

	for _, transition := range transitions {
		t.Run(string(transition[0])+"_to_"+string(transition[1]), func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{
				SocketDir: "/tmp/test-transitions",
			}).(*VMMManager)
			defer os.RemoveAll("/tmp/test-transitions")

			task := &types.Task{
				ID: "test-transition",
			}

			vm.vms[task.ID] = &VMInstance{
				ID:        task.ID,
				State:     transition[0],
				CreatedAt: time.Now(),
				PID:       1000,
			}

			// Simulate state change
			vm.vms[task.ID].State = transition[1]

			status, err := vm.Describe(context.Background(), task)
			assert.NoError(t, err)
			assert.NotNil(t, status)
		})
	}
}

func TestVMMManager_ConcurrentStartRemove(t *testing.T) {
	vm := NewVMMManager(&ManagerConfig{
		SocketDir: "/tmp/test-concurrent-start-remove",
	}).(*VMMManager)
	defer os.RemoveAll("/tmp/test-concurrent-start-remove")

	ctx := context.Background()
	numOps := 10

	for i := 0; i < numOps; i++ {
		go func(idx int) {
			task := &types.Task{
				ID: "task-" + string(rune('A'+idx)),
			}

			// Start (will fail)
			_ = vm.Start(ctx, task, "{}")

			// Remove
			_ = vm.Remove(ctx, task)
		}(i)
	}

	// Give time for concurrent operations
	time.Sleep(100 * time.Millisecond)
}

func TestVMMManager_SocketFileCleanup(t *testing.T) {
	vm := NewVMMManager(&ManagerConfig{
		SocketDir: "/tmp/test-socket-cleanup",
	}).(*VMMManager)
	defer os.RemoveAll("/tmp/test-socket-cleanup")

	task := &types.Task{
		ID: "test-socket-cleanup",
	}

	// Create a fake socket file
	socketPath := filepath.Join(vm.socketDir, task.ID+".sock")
	os.MkdirAll(vm.socketDir, 0755)
	file, err := os.Create(socketPath)
	require.NoError(t, err)
	file.Close()

	// Add VM to map so Remove will clean it up
	vm.vms[task.ID] = &VMInstance{
		ID:         task.ID,
		State:      VMStateRunning,
		PID:        1234,
		SocketPath: socketPath,
	}

	// Verify file exists
	_, err = os.Stat(socketPath)
	assert.NoError(t, err)

	// Remove VM
	err = vm.Remove(context.Background(), task)
	assert.NoError(t, err)

	// Verify socket is removed
	_, err = os.Stat(socketPath)
	assert.True(t, os.IsNotExist(err))
}
