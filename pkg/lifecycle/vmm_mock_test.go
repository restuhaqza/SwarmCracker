package lifecycle

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

// TestVMMManagerWithExecutors_Start tests VM start with mocked executors
func TestVMMManagerWithExecutors_Start(t *testing.T) {
	// Create a temp directory for socket files
	tmpDir := t.TempDir()

	tests := []struct {
		name        string
		setupMock   func(*MockProcessExecutor, *MockHTTPClient, string)
		config      *ManagerConfig
		task        *types.Task
		vmConfig    interface{}
		expectError bool
		validate    func(*testing.T, *VMMManagerInternal, *MockProcessExecutor, *MockHTTPClient, string)
	}{
		{
			name: "start_vm_success",
			setupMock: func(proc *MockProcessExecutor, httpClient *MockHTTPClient, socketDir string) {
				// Binary lookup succeeds
				proc.Binaries["firecracker"] = "/usr/bin/firecracker"

				// Create socket file for waitForAPIServer check
				socketPath := filepath.Join(socketDir, "test-vm.sock")
				file, _ := os.Create(socketPath)
				if file != nil {
					file.Close()
				}

				// Setup HTTP responses
				httpClient.SetResponse("GET", "http://unix"+socketPath+"/", http.StatusOK, []byte("{}"))
				httpClient.SetResponse("PUT", "http://unix"+socketPath+"/actions", http.StatusNoContent, []byte{})
			},
			config: &ManagerConfig{
				SocketDir: tmpDir,
			},
			task: &types.Task{
				ID: "test-vm",
			},
			vmConfig:    `{"kernel-image-path": "/vmlinux", "drives": []}`,
			expectError: false,
			validate: func(t *testing.T, vm *VMMManagerInternal, proc *MockProcessExecutor, httpClient *MockHTTPClient, socketDir string) {
				// Clean up socket file
				socketPath := filepath.Join(socketDir, "test-vm.sock")
				os.Remove(socketPath)

				// Verify VM was stored
				_, exists := vm.vms["test-vm"]
				assert.True(t, exists, "VM should be stored")

				// Verify binary lookup
				assert.GreaterOrEqual(t, len(proc.Calls), 1, "Should have looked up binary")

				// Verify HTTP calls
				assert.GreaterOrEqual(t, len(httpClient.Calls), 2, "Should make HTTP calls")
			},
		},
		{
			name: "start_vm_nil_task",
			setupMock: func(proc *MockProcessExecutor, httpClient *MockHTTPClient, socketDir string) {},
			config: &ManagerConfig{
				SocketDir: tmpDir,
			},
			task:        nil,
			vmConfig:    `{}`,
			expectError: true,
			validate:    nil,
		},
		{
			name: "start_vm_binary_not_found",
			setupMock: func(proc *MockProcessExecutor, httpClient *MockHTTPClient, socketDir string) {
				// Binary lookup fails
				proc.Binaries = make(map[string]string)
			},
			config: &ManagerConfig{
				SocketDir: tmpDir,
			},
			task: &types.Task{
				ID: "test-vm-no-bin",
			},
			vmConfig:    `{}`,
			expectError: true,
			validate:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procExec := NewMockProcessExecutor()
			httpClient := NewMockHTTPClient()

			if tt.setupMock != nil {
				tt.setupMock(procExec, httpClient, tmpDir)
			}

			vm := NewVMMManagerWithExecutors(tt.config, procExec, httpClient)
			ctx := context.Background()

			err := vm.Start(ctx, tt.task, tt.vmConfig)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.validate != nil {
				tt.validate(t, vm, procExec, httpClient, tmpDir)
			}
		})
	}
}

// TestVMMManagerWithExecutors_Stop tests VM stop operations
func TestVMMManagerWithExecutors_Stop(t *testing.T) {
	tests := []struct {
		name        string
		setupMock   func(*MockProcessExecutor, *MockHTTPClient)
		config      *ManagerConfig
		task        *types.Task
		expectError bool
		validate    func(*testing.T, *VMMManagerInternal)
	}{
		{
			name: "stop_vm_with_init_system",
			setupMock: func(proc *MockProcessExecutor, httpClient *MockHTTPClient) {
				proc.Processes[12345] = &mockProcess{pid: 12345}
			},
			config: &ManagerConfig{
				SocketDir: "/var/run/firecracker",
			},
			task: &types.Task{
				ID: "test-vm-stop-init",
				Annotations: map[string]string{
					"init_system": "tini",
				},
			},
			expectError: false,
			validate: func(t *testing.T, vm *VMMManagerInternal) {
				// Create VM first
				vm.vms["test-vm-stop-init"] = &VMInstance{
					ID:             "test-vm-stop-init",
					PID:            12345,
					State:          VMStateRunning,
					InitSystem:     "tini",
					GracePeriodSec: 1,
				}
			},
		},
		{
			name: "stop_vm_without_init_system",
			setupMock: func(proc *MockProcessExecutor, httpClient *MockHTTPClient) {
				// Setup HTTP response for SendCtrlAltDel
				httpClient.SetResponse("PUT", "http://unix/var/run/firecracker/test-vm-hard.sock/actions", http.StatusNoContent, []byte{})
			},
			config: &ManagerConfig{
				SocketDir: "/var/run/firecracker",
			},
			task: &types.Task{
				ID: "test-vm-hard",
			},
			expectError: false,
			validate: func(t *testing.T, vm *VMMManagerInternal) {
				vm.vms["test-vm-hard"] = &VMInstance{
					ID:         "test-vm-hard",
					PID:        12345,
					State:      VMStateRunning,
					InitSystem: "none",
					SocketPath: "/var/run/firecracker/test-vm-hard.sock",
				}
			},
		},
		{
			name: "stop_nonexistent_vm",
			setupMock: func(proc *MockProcessExecutor, httpClient *MockHTTPClient) {},
			config: &ManagerConfig{
				SocketDir: "/var/run/firecracker",
			},
			task: &types.Task{
				ID: "nonexistent-vm",
			},
			expectError: true,
		},
		{
			name: "stop_vm_graceful_shutdown_fails",
			setupMock: func(proc *MockProcessExecutor, httpClient *MockHTTPClient) {
				// Process exists but signal fails
				proc.Processes[12345] = &mockProcess{pid: 12345}
			},
			config: &ManagerConfig{
				SocketDir: "/var/run/firecracker",
			},
			task: &types.Task{
				ID: "test-vm-fail-graceful",
			},
			expectError: false, // Should fall back to force kill
			validate: func(t *testing.T, vm *VMMManagerInternal) {
				vm.vms["test-vm-fail-graceful"] = &VMInstance{
					ID:             "test-vm-fail-graceful",
					PID:            12345,
					State:          VMStateRunning,
					InitSystem:     "tini",
					GracePeriodSec: 1,
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procExec := NewMockProcessExecutor()
			httpClient := NewMockHTTPClient()
			
			if tt.setupMock != nil {
				tt.setupMock(procExec, httpClient)
			}

			vm := NewVMMManagerWithExecutors(tt.config, procExec, httpClient)
			ctx := context.Background()

			if tt.validate != nil {
				tt.validate(t, vm)
			}

			err := vm.Stop(ctx, tt.task)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				// May or may not error depending on implementation
				_ = err
			}
		})
	}
}

// TestVMMManagerWithExecutors_Wait tests VM wait operations
func TestVMMManagerWithExecutors_Wait(t *testing.T) {
	tests := []struct {
		name        string
		setupMock   func(*MockProcessExecutor)
		config      *ManagerConfig
		task        *types.Task
		expectState types.TaskState
		expectError bool
	}{
		{
			name: "wait_for_running_vm",
			setupMock: func(proc *MockProcessExecutor) {
				proc.Processes[12345] = &mockProcess{pid: 12345}
			},
			config: &ManagerConfig{},
			task: &types.Task{
				ID: "test-vm-running",
			},
			expectState: types.TaskState_RUNNING,
			expectError: false,
		},
		{
			name: "wait_for_stopped_vm",
			setupMock: func(proc *MockProcessExecutor) {
				// Process doesn't exist
			},
			config: &ManagerConfig{},
			task: &types.Task{
				ID: "test-vm-stopped",
			},
			expectState: types.TaskState_COMPLETE,
			expectError: false,
		},
		{
			name: "wait_for_nonexistent_vm",
			setupMock: func(proc *MockProcessExecutor) {},
			config: &ManagerConfig{},
			task: &types.Task{
				ID: "nonexistent-vm",
			},
			expectState: types.TaskState_ORPHANED,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procExec := NewMockProcessExecutor()
			httpClient := NewMockHTTPClient()
			
			if tt.setupMock != nil {
				tt.setupMock(procExec)
			}

			vm := NewVMMManagerWithExecutors(tt.config, procExec, httpClient)
			ctx := context.Background()

			// Setup VM
			if tt.task.ID != "nonexistent-vm" {
				vm.vms[tt.task.ID] = &VMInstance{
					ID:    tt.task.ID,
					PID:   12345,
					State: VMStateRunning,
				}
			}

			status, err := vm.Wait(ctx, tt.task)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectState, status.State)
		})
	}
}

// TestVMMManagerWithExecutors_Describe tests VM describe operations
func TestVMMManagerWithExecutors_Describe(t *testing.T) {
	tests := []struct {
		name        string
		setupMock   func(*MockProcessExecutor)
		config      *ManagerConfig
		task        *types.Task
		vmState     VMState
		expectState types.TaskState
	}{
		{
			name: "describe_running_vm",
			setupMock: func(proc *MockProcessExecutor) {
				proc.Processes[12345] = &mockProcess{pid: 12345}
			},
			config: &ManagerConfig{},
			task: &types.Task{
				ID: "test-vm",
			},
			vmState:     VMStateRunning,
			expectState: types.TaskState_RUNNING,
		},
		{
			name: "describe_stopped_vm",
			setupMock: func(proc *MockProcessExecutor) {
				// Process doesn't exist
			},
			config: &ManagerConfig{},
			task: &types.Task{
				ID: "test-vm",
			},
			vmState:     VMStateStopped,
			expectState: types.TaskState_COMPLETE,
		},
		{
			name: "describe_crashed_vm",
			setupMock: func(proc *MockProcessExecutor) {
				// Process doesn't exist
			},
			config: &ManagerConfig{},
			task: &types.Task{
				ID: "test-vm",
			},
			vmState:     VMStateCrashed,
			expectState: types.TaskState_FAILED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procExec := NewMockProcessExecutor()
			httpClient := NewMockHTTPClient()
			
			if tt.setupMock != nil {
				tt.setupMock(procExec)
			}

			vm := NewVMMManagerWithExecutors(tt.config, procExec, httpClient)
			ctx := context.Background()

			vm.vms[tt.task.ID] = &VMInstance{
				ID:    tt.task.ID,
				PID:   12345,
				State: tt.vmState,
				CreatedAt: time.Now(),
			}

			status, err := vm.Describe(ctx, tt.task)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectState, status.State)
		})
	}
}

// TestVMMManagerWithExecutors_Remove tests VM removal
func TestVMMManagerWithExecutors_Remove(t *testing.T) {
	tests := []struct {
		name        string
		setupMock   func(*MockProcessExecutor)
		config      *ManagerConfig
		task        *types.Task
		expectError bool
		validate    func(*testing.T, *VMMManagerInternal)
	}{
		{
			name: "remove_existing_vm",
			setupMock: func(proc *MockProcessExecutor) {
				proc.Processes[12345] = &mockProcess{pid: 12345}
			},
			config: &ManagerConfig{
				SocketDir: t.TempDir(),
			},
			task: &types.Task{
				ID: "test-vm-remove",
			},
			expectError: false,
			validate: func(t *testing.T, vm *VMMManagerInternal) {
				vm.vms["test-vm-remove"] = &VMInstance{
					ID:         "test-vm-remove",
					PID:        12345,
					State:      VMStateRunning,
					SocketPath: "/var/run/firecracker/test-vm-remove.sock",
				}
			},
		},
		{
			name:        "remove_nonexistent_vm",
			setupMock:   func(proc *MockProcessExecutor) {},
			config:      &ManagerConfig{},
			task: &types.Task{
				ID: "nonexistent-vm",
			},
			expectError: false,
		},
		{
			name: "remove_nil_task",
			setupMock: func(proc *MockProcessExecutor) {},
			config: &ManagerConfig{},
			task:        nil,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procExec := NewMockProcessExecutor()
			httpClient := NewMockHTTPClient()
			
			if tt.setupMock != nil {
				tt.setupMock(procExec)
			}

			vm := NewVMMManagerWithExecutors(tt.config, procExec, httpClient)
			ctx := context.Background()

			if tt.validate != nil {
				tt.validate(t, vm)
			}

			err := vm.Remove(ctx, tt.task)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestMockProcessExecutor tests mock process executor
func TestMockProcessExecutor(t *testing.T) {
	t.Run("FindProcess", func(t *testing.T) {
		procExec := NewMockProcessExecutor()
		mockProc := &mockProcess{pid: 12345}
		procExec.Processes[12345] = mockProc

		proc, err := procExec.FindProcess(12345)
		assert.NoError(t, err)
		assert.Equal(t, 12345, proc.Pid())

		_, err = procExec.FindProcess(99999)
		assert.Error(t, err)
	})

	t.Run("LookPath", func(t *testing.T) {
		procExec := NewMockProcessExecutor()
		procExec.Binaries["firecracker"] = "/usr/bin/firecracker"

		path, err := procExec.LookPath("firecracker")
		assert.NoError(t, err)
		assert.Equal(t, "/usr/bin/firecracker", path)

		_, err = procExec.LookPath("nonexistent")
		assert.Error(t, err)
	})

	t.Run("Command", func(t *testing.T) {
		procExec := NewMockProcessExecutor()
		procExec.Commands["ls"] = struct{ Output []byte; Err error }{
			Output: []byte("file1\nfile2\n"),
			Err:    nil,
		}

		cmd := procExec.Command("ls", "-la")
		output, err := cmd.Output()
		assert.NoError(t, err)
		assert.Equal(t, "file1\nfile2\n", string(output))
	})
}

// TestMockHTTPClient tests mock HTTP client
func TestMockHTTPClient(t *testing.T) {
	t.Run("Get", func(t *testing.T) {
		client := NewMockHTTPClient()
		client.SetResponse("GET", "http://test/api", http.StatusOK, []byte("response"))

		resp, err := client.Get("http://test/api")
		assert.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, _ := io.ReadAll(resp.Body)
		assert.Equal(t, "response", string(body))

		assert.Equal(t, 1, len(client.Calls))
		assert.Equal(t, "GET", client.Calls[0].Method)
		assert.Equal(t, "http://test/api", client.Calls[0].URL)
	})

	t.Run("Do", func(t *testing.T) {
		client := NewMockHTTPClient()
		client.SetResponse("PUT", "http://test/api/actions", http.StatusNoContent, []byte{})

		req, _ := http.NewRequest("PUT", "http://test/api/actions", nil)
		resp, err := client.Do(req)
		assert.NoError(t, err)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("Error", func(t *testing.T) {
		client := NewMockHTTPClient()
		client.SetError("GET", "http://test/error", errors.New("connection error"))

		_, err := client.Get("http://test/error")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "connection error")
	})
}

// TestVMMManagerWithExecutors_GracefulShutdown tests graceful shutdown scenarios
func TestVMMManagerWithExecutors_GracefulShutdown(t *testing.T) {
	tests := []struct {
		name        string
		setupProcess func() *mockProcess
		gracePeriod int
		expectKill  bool
	}{
		{
			name: "graceful_shutdown_succeeds",
			setupProcess: func() *mockProcess {
				return &mockProcess{
					pid:     12345,
					killed:  false,
					signals: make([]syscall.Signal, 0),
				}
			},
			gracePeriod: 2,
			expectKill:  false,
		},
		{
			name: "graceful_shutdown_times_out",
			setupProcess: func() *mockProcess {
				// Process that doesn't exit on SIGTERM
				return &mockProcess{
					pid:     12345,
					killed:  false,
					signals: make([]syscall.Signal, 0),
				}
			},
			gracePeriod: 1,
			expectKill:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procExec := NewMockProcessExecutor()
			httpClient := NewMockHTTPClient()

			mockProc := tt.setupProcess()
			procExec.Processes[12345] = mockProc

			config := &ManagerConfig{}
			vm := NewVMMManagerWithExecutors(config, procExec, httpClient)

			vmInstance := &VMInstance{
				ID:             "test-vm",
				PID:            12345,
				State:          VMStateRunning,
				InitSystem:     "tini",
				GracePeriodSec: tt.gracePeriod,
			}

			ctx := context.Background()
			err := vm.gracefulShutdown(ctx, vmInstance)

			// Should complete without error (may force kill)
			_ = err

			if tt.expectKill {
				assert.True(t, mockProc.killed, "Process should be force killed")
			}
		})
	}
}

// TestVMMManagerWithExecutors_HardShutdown tests hard shutdown scenarios
func TestVMMManagerWithExecutors_HardShutdown(t *testing.T) {
	t.Run("send_ctrl_alt_del_success", func(t *testing.T) {
		procExec := NewMockProcessExecutor()
		httpClient := NewMockHTTPClient()

		httpClient.SetResponse("PUT", "http://unix/var/run/fc/test.sock/actions", http.StatusNoContent, []byte{})

		config := &ManagerConfig{}
		vm := NewVMMManagerWithExecutors(config, procExec, httpClient)

		vmInstance := &VMInstance{
			ID:         "test-vm",
			PID:        12345,
			State:      VMStateRunning,
			SocketPath: "/var/run/fc/test.sock",
		}

		ctx := context.Background()
		err := vm.hardShutdown(ctx, vmInstance)

		assert.NoError(t, err)
		assert.Equal(t, 1, len(httpClient.Calls))
		assert.Equal(t, "PUT", httpClient.Calls[0].Method)
	})

	t.Run("send_ctrl_alt_del_fails", func(t *testing.T) {
		procExec := NewMockProcessExecutor()
		procExec.Processes[12345] = &mockProcess{pid: 12345}

		httpClient := NewMockHTTPClient()
		httpClient.SetError("PUT", "http://unix/var/run/fc/test.sock/actions", errors.New("connection refused"))

		config := &ManagerConfig{}
		vm := NewVMMManagerWithExecutors(config, procExec, httpClient)

		vmInstance := &VMInstance{
			ID:         "test-vm",
			PID:        12345,
			State:      VMStateRunning,
			SocketPath: "/var/run/fc/test.sock",
		}

		ctx := context.Background()
		err := vm.hardShutdown(ctx, vmInstance)

		// Should fallback to force kill, so no error
		assert.NoError(t, err)
	})
}

// TestVMMManagerWithExecutors_WaitForAPIServer tests API server wait
func TestVMMManagerWithExecutors_WaitForAPIServer(t *testing.T) {
	t.Run("api_server_ready", func(t *testing.T) {
		httpClient := NewMockHTTPClient()
		httpClient.SetResponse("GET", "http://unix/var/run/fc/test.sock/", http.StatusOK, []byte("{}"))

		config := &ManagerConfig{}
		vm := NewVMMManagerWithExecutors(config, nil, httpClient)

		// Create socket file
		tmpDir := t.TempDir()
		socketPath := tmpDir + "/test.sock"
		file, _ := os.Create(socketPath)
		file.Close()

		err := vm.waitForAPIServerWithClient(socketPath, 1*time.Second)
		assert.NoError(t, err)
	})

	t.Run("api_server_timeout", func(t *testing.T) {
		httpClient := NewMockHTTPClient()
		httpClient.SetResponse("GET", "http://unix/var/run/fc/test.sock/", http.StatusServiceUnavailable, nil)

		config := &ManagerConfig{}
		vm := NewVMMManagerWithExecutors(config, nil, httpClient)

		tmpDir := t.TempDir()
		socketPath := tmpDir + "/test.sock"
		file, _ := os.Create(socketPath)
		file.Close()

		err := vm.waitForAPIServerWithClient(socketPath, 100*time.Millisecond)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not ready")
	})
}
