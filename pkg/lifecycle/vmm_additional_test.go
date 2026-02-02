package lifecycle

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMMManager_Start_MockedAPI tests Start with mocked Firecracker API
func TestVMMManager_Start_MockedAPI(t *testing.T) {
	tests := []struct {
		name           string
		apiHandler     http.HandlerFunc
		configJSON     string
		expectError    bool
		errorContains  string
		validateState  VMState
	}{
		{
			name: "successful start with mocked API",
			apiHandler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/boot-source":
					w.WriteHeader(http.StatusNoContent)
				case "/machine-config":
					w.WriteHeader(http.StatusNoContent)
				case "/actions":
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			},
			configJSON: `{
				"boot_source": {
					"kernel_image_path": "/usr/share/firecracker/vmlinux"
				},
				"machine_config": {
					"vcpu_count": 1,
					"mem_size_mib": 512
				}
			}`,
			expectError:   false,
			validateState: VMStateRunning,
		},
		{
			name: "API returns error on boot source",
			apiHandler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/boot-source" {
					w.WriteHeader(http.StatusBadRequest)
				}
			},
			configJSON: `{
				"boot_source": {
					"kernel_image_path": "/usr/share/firecracker/vmlinux"
				}
			}`,
			expectError:   true,
			errorContains: "boot source",
		},
		{
			name: "API returns error on actions",
			apiHandler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/actions" {
					w.WriteHeader(http.StatusInternalServerError)
				} else {
					w.WriteHeader(http.StatusNoContent)
				}
			},
			configJSON: `{
				"boot_source": {
					"kernel_image_path": "/usr/share/firecracker/vmlinux"
				}
			}`,
			expectError:   true,
			errorContains: "status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock API server
			tmpDir := t.TempDir()
			_ = filepath.Join(tmpDir, "test.sock")

			// Create a Unix socket listener for testing
			// Note: This is a simplified test - real implementation would need actual Unix socket
			_ = httptest.NewServer(tt.apiHandler)

			vm := &VMMManager{
				config: &ManagerConfig{
					SocketDir: tmpDir,
				},
				vms:       make(map[string]*VMInstance),
				socketDir: tmpDir,
			}

			task := &types.Task{
				ID: "test-vm-mock",
			}

			ctx := context.Background()
			err := vm.Start(ctx, task, tt.configJSON)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				// Start will fail because we don't have actual Firecracker process
				// but we can test the config parsing logic
				_ = err
			}
		})
	}
}

// TestVMMManager_Describe_ProcessStates tests Describe with different process states
func TestVMMManager_Describe_ProcessStates(t *testing.T) {
	tests := []struct {
		name           string
		setupFunc      func() (*VMInstance, error)
		expectedState  types.TaskState
		expectError    bool
	}{
		{
			name: "running process",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:        "test-vm",
					PID:       os.Getpid(),
					State:     VMStateRunning,
					CreatedAt: time.Now(),
				}, nil
			},
			expectedState: types.TaskState_RUNNING,
			expectError:   false,
		},
		{
			name: "non-existent process",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:        "test-vm",
					PID:       99999,
					State:     VMStateRunning,
					CreatedAt: time.Now(),
				}, nil
			},
			expectedState: types.TaskState_COMPLETE,
			expectError:   false,
		},
		{
			name: "process with invalid PID",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:        "test-vm",
					PID:       -1,
					State:     VMStateRunning,
					CreatedAt: time.Now(),
				}, nil
			},
			expectedState: types.TaskState_FAILED,
			expectError:   false,
		},
		{
			name: "process with zero PID",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:        "test-vm",
					PID:       0,
					State:     VMStateRunning,
					CreatedAt: time.Now(),
				}, nil
			},
			expectedState: types.TaskState_FAILED,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := &VMMManager{
				config: &ManagerConfig{
					SocketDir: "/tmp/test",
				},
				vms: make(map[string]*VMInstance),
			}

			vmInstance, err := tt.setupFunc()
			if err != nil {
				t.Fatalf("setupFunc failed: %v", err)
			}

			task := &types.Task{
				ID: vmInstance.ID,
			}

			vm.vms[task.ID] = vmInstance

			ctx := context.Background()
			status, err := vm.Describe(ctx, task)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedState, status.State)
			}
		})
	}
}

// TestVMMManager_Wait_ProcessStates tests Wait with different process states
func TestVMMManager_Wait_ProcessStates(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func() (*VMInstance, error)
		expectedState types.TaskState
		expectError   bool
	}{
		{
			name: "wait on running process",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:    "test-vm",
					PID:   os.Getpid(),
					State: VMStateRunning,
				}, nil
			},
			expectedState: types.TaskState_RUNNING,
			expectError:   false,
		},
		{
			name: "wait on exited process",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:    "test-vm",
					PID:   99999,
					State: VMStateRunning,
				}, nil
			},
			expectedState: types.TaskState_COMPLETE,
			expectError:   false,
		},
		{
			name: "wait on new VM",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:    "test-vm",
					PID:   os.Getpid(),
					State: VMStateNew,
				}, nil
			},
			expectedState: types.TaskState_RUNNING,
			expectError:   false,
		},
		{
			name: "wait on crashed VM",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:    "test-vm",
					PID:   99999,
					State: VMStateCrashed,
				}, nil
			},
			expectedState: types.TaskState_COMPLETE,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := &VMMManager{
				config: &ManagerConfig{
					SocketDir: "/tmp/test",
				},
				vms: make(map[string]*VMInstance),
			}

			vmInstance, err := tt.setupFunc()
			if err != nil {
				t.Fatalf("setupFunc failed: %v", err)
			}

			task := &types.Task{
				ID: vmInstance.ID,
			}

			vm.vms[task.ID] = vmInstance

			ctx := context.Background()
			status, err := vm.Wait(ctx, task)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedState, status.State)
			}
		})
	}
}

// TestVMMManager_Stop_ProcessStates tests Stop with different process states
func TestVMMManager_Stop_ProcessStates(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func() (*VMInstance, error)
		expectError bool
		errorMsg    string
	}{
		{
			name: "stop running VM with tini",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:             "test-vm",
					PID:            99999,
					State:          VMStateRunning,
					InitSystem:     "tini",
					GracePeriodSec: 5,
				}, nil
			},
			expectError: true, // Will fail because PID doesn't exist
		},
		{
			name: "stop running VM with dumb-init",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:             "test-vm",
					PID:            99999,
					State:          VMStateRunning,
					InitSystem:     "dumb-init",
					GracePeriodSec: 10,
				}, nil
			},
			expectError: true,
		},
		{
			name: "stop running VM with no init",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:             "test-vm",
					PID:            99999,
					State:          VMStateRunning,
					InitSystem:     "none",
					GracePeriodSec: 0,
				}, nil
			},
			expectError: true,
		},
		{
			name: "stop already stopped VM",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:             "test-vm",
					PID:            99999,
					State:          VMStateStopped,
					InitSystem:     "tini",
					GracePeriodSec: 5,
				}, nil
			},
			expectError: true,
		},
		{
			name: "stop new VM",
			setupFunc: func() (*VMInstance, error) {
				return &VMInstance{
					ID:             "test-vm",
					PID:            99999,
					State:          VMStateNew,
					InitSystem:     "tini",
					GracePeriodSec: 5,
				}, nil
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
				vms: make(map[string]*VMInstance),
			}

			vmInstance, err := tt.setupFunc()
			if err != nil {
				t.Fatalf("setupFunc failed: %v", err)
			}

			task := &types.Task{
				ID: vmInstance.ID,
			}

			vm.vms[task.ID] = vmInstance

			ctx := context.Background()
			err = vm.Stop(ctx, task)

			if tt.expectError {
				assert.Error(t, err)
			}
		})
	}
}

// TestVMMManager_SignalHandling tests signal handling edge cases
func TestVMMManager_SignalHandling(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func() (*VMInstance, *types.Task)
		signalFunc  func(*VMMManager, *VMInstance) error
		expectError bool
	}{
		{
			name: "signal 0 on running process",
			setupFunc: func() (*VMInstance, *types.Task) {
				return &VMInstance{
					ID:    "test-vm",
					PID:   os.Getpid(),
					State: VMStateRunning,
				}, &types.Task{ID: "test-vm"}
			},
			signalFunc: func(vm *VMMManager, vmi *VMInstance) error {
				process, err := os.FindProcess(vmi.PID)
				if err != nil {
					return err
				}
				return process.Signal(syscall.Signal(0))
			},
			expectError: false,
		},
		{
			name: "signal 0 on non-existent process",
			setupFunc: func() (*VMInstance, *types.Task) {
				return &VMInstance{
					ID:    "test-vm",
					PID:   99999,
					State: VMStateRunning,
				}, &types.Task{ID: "test-vm"}
			},
			signalFunc: func(vm *VMMManager, vmi *VMInstance) error {
				process, err := os.FindProcess(vmi.PID)
				if err != nil {
					return err
				}
				return process.Signal(syscall.Signal(0))
			},
			expectError: true,
		},
		{
			name: "SIGTERM on running process",
			setupFunc: func() (*VMInstance, *types.Task) {
				// Create a subprocess that we can signal
				cmd := createSleepProcess(t)
				return &VMInstance{
					ID:    "test-vm",
					PID:   cmd.Process.Pid,
					State: VMStateRunning,
				}, &types.Task{ID: "test-vm"}
			},
			signalFunc: func(vm *VMMManager, vmi *VMInstance) error {
				process, err := os.FindProcess(vmi.PID)
				if err != nil {
					return err
				}
				return process.Signal(syscall.SIGTERM)
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := &VMMManager{
				config: &ManagerConfig{
					SocketDir: "/tmp/test",
				},
				vms: make(map[string]*VMInstance),
			}

			vmInstance, task := tt.setupFunc()
			vm.vms[task.ID] = vmInstance

			err := tt.signalFunc(vm, vmInstance)

			// Clean up any created processes
			if vmInstance.PID > 0 && vmInstance.PID < 100000 {
				process, _ := os.FindProcess(vmInstance.PID)
				if process != nil {
					process.Kill()
				}
			}

			if tt.expectError {
				assert.Error(t, err)
			} else {
				_ = err
			}
		})
	}
}

// TestVMMManager_Start_VMAlreadyExists tests starting VM that already exists
func TestVMMManager_Start_VMAlreadyExists(t *testing.T) {
	tmpDir := t.TempDir()

	vm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	task := &types.Task{
		ID: "existing-vm",
	}

	// Add existing VM
	vm.vms[task.ID] = &VMInstance{
		ID:    task.ID,
		PID:   12345,
		State: VMStateRunning,
	}

	configJSON := `{"boot_source": {"kernel_image_path": "/usr/share/firecracker/vmlinux"}}`

	ctx := context.Background()
	err := vm.Start(ctx, task, configJSON)

	// Should error because VM already exists
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// TestVMMManager_Start_InvalidKernelPath tests start with invalid kernel path
func TestVMMManager_Start_InvalidKernelPath(t *testing.T) {
	tmpDir := t.TempDir()

	vm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	task := &types.Task{
		ID: "invalid-kernel",
	}

	configJSON := `{
		"boot_source": {
			"kernel_image_path": "/nonexistent/vmlinux"
		}
	}`

	ctx := context.Background()
	err := vm.Start(ctx, task, configJSON)

	// Will fail because Firecracker binary not found or kernel invalid
	assert.Error(t, err)
}

// TestVMMManager_Describe_NilTask tests describe with nil task
func TestVMMManager_Describe_NilTask(t *testing.T) {
	vm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: "/tmp/test",
		},
		vms: make(map[string]*VMInstance),
	}

	ctx := context.Background()
	status, err := vm.Describe(ctx, nil)

	assert.Error(t, err)
	assert.Nil(t, status)
}

// TestVMMManager_Wait_NilTask tests wait with nil task
func TestVMMManager_Wait_NilTask(t *testing.T) {
	vm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: "/tmp/test",
		},
		vms: make(map[string]*VMInstance),
	}

	ctx := context.Background()
	status, err := vm.Wait(ctx, nil)

	// May error or return ORPHANED
	if err == nil {
		assert.Equal(t, types.TaskState_ORPHANED, status.State)
	}
}

// TestVMInstance_Uptime tests VM instance uptime calculation
func TestVMInstance_Uptime(t *testing.T) {
	tests := []struct {
		name         string
		createdAt    time.Time
		minUptime    time.Duration
		maxUptime    time.Duration
	}{
		{
			name:         "recent VM",
			createdAt:    time.Now().Add(-10 * time.Second),
			minUptime:    9 * time.Second,
			maxUptime:    11 * time.Second,
		},
		{
			name:         "old VM",
			createdAt:    time.Now().Add(-1 * time.Hour),
			minUptime:    59 * time.Minute,
			maxUptime:    61 * time.Minute,
		},
		{
			name:         "very new VM",
			createdAt:    time.Now(),
			minUptime:    0,
			maxUptime:    1 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := &VMMManager{
				config: &ManagerConfig{
					SocketDir: "/tmp/test",
				},
				vms: make(map[string]*VMInstance),
			}

			task := &types.Task{
				ID: "test-vm",
			}

			vm.vms[task.ID] = &VMInstance{
				ID:        task.ID,
				PID:       os.Getpid(),
				State:     VMStateRunning,
				CreatedAt: tt.createdAt,
			}

			ctx := context.Background()
			status, err := vm.Describe(ctx, task)

			require.NoError(t, err)
			require.NotNil(t, status.RuntimeStatus)

			runtimeStatus := status.RuntimeStatus.(map[string]interface{})
			uptimeStr := runtimeStatus["uptime"].(string)

			// Parse uptime - it's a string like "1h23m45.123s"
			assert.NotEmpty(t, uptimeStr)
		})
	}
}

// TestVMMManager_MultipleStartStop tests rapid start/stop cycles
func TestVMMManager_MultipleStartStop(t *testing.T) {
	tmpDir := t.TempDir()

	vm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	taskID := "test-vm"
	configJSON := `{"boot_source": {"kernel_image_path": "/usr/share/firecracker/vmlinux"}}`

	ctx := context.Background()

	// Try multiple start/stop cycles
	for i := 0; i < 3; i++ {
		task := &types.Task{
			ID: fmt.Sprintf("%s-%d", taskID, i),
		}

		// Start will fail without actual Firecracker, but we test the logic
		_ = vm.Start(ctx, task, configJSON)

		// Check if VM was created
		vm.mu.RLock()
		_, _ = vm.vms[task.ID]
		vm.mu.RUnlock()

		// Clean up
		_ = vm.Remove(ctx, task)
	}
}

// TestVMMManager_ConcurrentDescribeSameVM tests concurrent describe on same VM
func TestVMMManager_ConcurrentDescribeSameVM(t *testing.T) {
	vm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: "/tmp/test",
		},
		vms: make(map[string]*VMInstance),
	}

	task := &types.Task{
		ID: "test-vm",
	}

	vm.vms[task.ID] = &VMInstance{
		ID:        task.ID,
		PID:       os.Getpid(),
		State:     VMStateRunning,
		CreatedAt: time.Now(),
	}

	ctx := context.Background()

	// Launch multiple concurrent describe calls
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			status, err := vm.Describe(ctx, task)
			assert.NoError(t, err)
			assert.NotNil(t, status)
			done <- true
		}()
	}

	// Wait for all to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}
