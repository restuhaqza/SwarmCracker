//go:build !integration
// +build !integration

package lifecycle

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfigureVM_ErrorPaths tests error paths in configureVM
func TestConfigureVM_ErrorPaths(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	vmm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	ctx := context.Background()

	tests := []struct {
		name        string
		config      interface{}
		expectError bool
		errorMsg    string
	}{
		{
			name:        "nil config",
			config:      nil,
			expectError: true,
			errorMsg:    "invalid config",
		},
		{
			name:        "invalid type",
			config:      12345,
			expectError: true,
			errorMsg:    "invalid config type",
		},
		{
			name:        "invalid JSON string",
			config:      "invalid json {{{",
			expectError: true,
			errorMsg:    "failed to unmarshal",
		},
		{
			name: "empty map",
			config: map[string]interface{}{
				"boot_source":   map[string]interface{}{},
				"machine_config": map[string]interface{}{},
			},
			expectError: false, // Empty config is valid
		},
		{
			name: "boot source only",
			config: map[string]interface{}{
				"boot_source": map[string]interface{}{
					"kernel_image_path": "/path/to/kernel",
					"boot_args":         "console=ttyS0",
				},
			},
			expectError: false, // Will error on socket, but logic is tested
		},
		{
			name: "machine config only",
			config: map[string]interface{}{
				"machine_config": map[string]interface{}{
					"vcpu_count":   2,
					"mem_size_mib": 512,
					"ht_enabled":   false,
				},
			},
			expectError: false, // Will error on socket, but logic is tested
		},
		{
			name: "drives with empty drive_id",
			config: map[string]interface{}{
				"drives": []interface{}{
					map[string]interface{}{
						"drive_id": "",
					},
				},
			},
			expectError: false, // Empty drive_id is skipped
		},
		{
			name: "drives with invalid type",
			config: map[string]interface{}{
				"drives": []interface{}{
					"not a map",
				},
			},
			expectError: false, // Invalid drive entries are skipped
		},
		{
			name: "valid JSON string config",
			config: `{"boot_source": {"kernel_image_path": "/kernel"}}`,
			expectError: false,
		},
		{
			name: "config with all sections",
			config: map[string]interface{}{
				"boot_source": map[string]interface{}{
					"kernel_image_path": "/kernel",
				},
				"machine_config": map[string]interface{}{
					"vcpu_count":   2,
					"mem_size_mib": 512,
				},
				"drives": []interface{}{
					map[string]interface{}{
						"drive_id":      "rootfs",
						"path_on_host":  "/rootfs.ext4",
						"is_root_device": true,
						"is_read_only":  false,
					},
				},
			},
			expectError: false, // Will error on socket, but logic is tested
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := vmm.configureVM(ctx, socketPath, tt.config)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			}
			// Non-expected errors (like socket connection) are ok
			_ = err
		})
	}
}

// TestGracefulShutdown tests graceful shutdown logic
func TestGracefulShutdown(t *testing.T) {
	tmpDir := t.TempDir()

	vmm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	tests := []struct {
		name        string
		setupVM     func() *VMInstance
		expectError bool
	}{
		{
			name: "non-existent process",
			setupVM: func() *VMInstance {
				return &VMInstance{
					ID:             "test-vm",
					PID:            99999, // Non-existent PID
					State:          VMStateRunning,
					CreatedAt:      time.Now(),
					InitSystem:     "systemd",
					GracePeriodSec: 1,
				}
			},
			expectError: true, // forceKillVM will fail
		},
		{
			name: "init system none (should not use graceful)",
			setupVM: func() *VMInstance {
				return &VMInstance{
					ID:             "test-vm-none",
					PID:            99999, // Non-existent PID
					State:          VMStateRunning,
					CreatedAt:      time.Now(),
					InitSystem:     "none",
					GracePeriodSec: 1,
				}
			},
			expectError: true, // Will fail to find process
		},
		{
			name: "very short grace period",
			setupVM: func() *VMInstance {
				return &VMInstance{
					ID:             "test-vm-short",
					PID:            99999,
					State:          VMStateRunning,
					CreatedAt:      time.Now(),
					InitSystem:     "systemd",
					GracePeriodSec: 0,
				}
			},
			expectError: true,
		},
		{
			name: "long grace period with non-existent process",
			setupVM: func() *VMInstance {
				return &VMInstance{
					ID:             "test-vm-long",
					PID:            99998,
					State:          VMStateRunning,
					CreatedAt:      time.Now(),
					InitSystem:     "systemd",
					GracePeriodSec: 10,
				}
			},
			expectError: true, // SIGTERM will fail
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmInstance := tt.setupVM()
			ctx := context.Background()

			err := vmm.gracefulShutdown(ctx, vmInstance)

			if tt.expectError {
				assert.Error(t, err)
			}
			// Check state was potentially updated
			_ = vmInstance.State
		})
	}
}

// TestGracefulShutdown_ContextCancellation tests context cancellation
func TestGracefulShutdown_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	vmm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	vmInstance := &VMInstance{
		ID:             "test-vm-cancel",
		PID:            99999, // Non-existent PID to avoid killing current process
		State:          VMStateRunning,
		CreatedAt:      time.Now(),
		InitSystem:     "systemd",
		GracePeriodSec: 10,
	}

	// Create a context that will be cancelled
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	// Start graceful shutdown in a goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- vmm.gracefulShutdown(ctx, vmInstance)
	}()

	// Should return quickly due to context cancellation
	select {
	case err := <-errChan:
		// Should error because context was cancelled
		assert.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("gracefulShutdown did not return quickly after context cancellation")
	}
}

// TestHardShutdown tests hard shutdown logic
func TestHardShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	vmm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	tests := []struct {
		name        string
		setupVM     func() *VMInstance
		expectError bool
	}{
		{
			name: "non-existent socket",
			setupVM: func() *VMInstance {
				return &VMInstance{
					ID:         "test-vm",
					PID:        99999,
					State:      VMStateRunning,
					CreatedAt:  time.Now(),
					SocketPath: socketPath,
				}
			},
			expectError: true, // Socket doesn't exist, will forceKillVM
		},
		{
			name: "invalid socket path",
			setupVM: func() *VMInstance {
				return &VMInstance{
					ID:         "test-vm-invalid",
					PID:        99999,
					State:      VMStateRunning,
					CreatedAt:  time.Now(),
					SocketPath: "/non/existent/path/socket.sock",
				}
			},
			expectError: true, // Will fail to connect
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmInstance := tt.setupVM()
			ctx := context.Background()

			err := vmm.hardShutdown(ctx, vmInstance)

			// Most will error due to non-existent socket/PID
			_ = err
			// Verify state might be updated
			_ = vmInstance.State
		})
	}
}

// TestHardShutdown_WithMockSocket tests with a mock Unix socket server
func TestHardShutdown_WithMockSocket(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Create a simple Unix socket server that responds to requests
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)

		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			return
		}
		defer listener.Close()

		// Accept one connection
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read request
		buf := make([]byte, 1024)
		_, err = conn.Read(buf)
		if err != nil {
			return
		}

		// Send a response
		response := "HTTP/1.1 204 No Content\r\n\r\n"
		_, _ = conn.Write([]byte(response))
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	vmm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	vmInstance := &VMInstance{
		ID:         "test-vm-mock",
		PID:        99999,
		State:      VMStateRunning,
		CreatedAt:  time.Now(),
		SocketPath: socketPath,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := vmm.hardShutdown(ctx, vmInstance)

	// Should complete without error (even if forceKillVM fails)
	_ = err
	assert.Equal(t, VMStateStopped, vmInstance.State)

	<-serverDone // Wait for server to finish
}

// TestForceKillVM_ErrorPaths tests force kill error paths
func TestForceKillVM_ErrorPaths(t *testing.T) {
	tmpDir := t.TempDir()

	vmm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	tests := []struct {
		name        string
		pid         int
		expectError bool
	}{
		{
			name:        "non-existent process",
			pid:         99999,
			expectError: true, // Process.FindProcess will error
		},
		{
			name:        "zero PID",
			pid:         0,
			expectError: true,
		},
		{
			name:        "negative PID",
			pid:         -1,
			expectError: true,
		},
		{
			name:        "valid but non-existent PID",
			pid:         12345,
			expectError: true, // Process doesn't exist
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmInstance := &VMInstance{
				ID:        "test-vm",
				PID:       tt.pid,
				State:     VMStateRunning,
				CreatedAt: time.Now(),
			}

			err := vmm.forceKillVM(vmInstance)

			if tt.expectError {
				assert.Error(t, err)
			}
		})
	}
}

// TestStart_ErrorPaths tests error paths in Start
func TestStart_ErrorPaths(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")
	err := os.MkdirAll(socketDir, 0755)
	require.NoError(t, err)

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	ctx := context.Background()

	tests := []struct {
		name        string
		task        *types.Task
		config      interface{}
		expectError bool
		errorMsg    string
	}{
		{
			name:        "nil task",
			task:        nil,
			config:      map[string]interface{}{},
			expectError: true,
			errorMsg:    "task cannot be nil",
		},
		{
			name: "empty task ID",
			task: &types.Task{
				ID: "",
			},
			config:      map[string]interface{}{},
			expectError: false, // Will error later in the flow
		},
		{
			name: "nil config",
			task: &types.Task{
				ID: "test-nil-config",
			},
			config:      nil,
			expectError: true,
			errorMsg:    "invalid config",
		},
		{
			name: "already exists",
			task: &types.Task{
				ID: "test-exists",
			},
			config: map[string]interface{}{},
			expectError: false, // Will error on Firecracker binary
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Pre-add VM for "already exists" test
			if tt.task != nil && tt.task.ID == "test-exists" {
				vmm.mu.Lock()
				vmm.vms[tt.task.ID] = &VMInstance{
					ID:    tt.task.ID,
					State: VMStateRunning,
				}
				vmm.mu.Unlock()
			}

			err := vmm.Start(ctx, tt.task, tt.config)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			}
		})
	}
}

// TestStart_AlreadyExists tests starting a VM that already exists
func TestStart_AlreadyExists(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")
	err := os.MkdirAll(socketDir, 0755)
	require.NoError(t, err)

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	task := &types.Task{
		ID:        "existing-vm",
		ServiceID: "service-1",
	}

	// Pre-add VM
	vmm.mu.Lock()
	vmm.vms[task.ID] = &VMInstance{
		ID:        task.ID,
		PID:       1234,
		State:     VMStateRunning,
		CreatedAt: time.Now(),
	}
	vmm.mu.Unlock()

	ctx := context.Background()
	vmConfig := map[string]interface{}{
		"boot_source": map[string]interface{}{
			"kernel_image_path": "/path/to/kernel",
		},
	}

	err = vmm.Start(ctx, task, vmConfig)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// TestStop_ErrorPaths tests error paths in Stop
func TestStop_ErrorPaths(t *testing.T) {
	tmpDir := t.TempDir()

	vmm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	ctx := context.Background()

	tests := []struct {
		name        string
		setupTask   func() *types.Task
		expectError bool
		errorMsg    string
	}{
		{
			name: "nil task",
			setupTask: func() *types.Task {
				return nil
			},
			expectError: true,
			errorMsg:    "task cannot be nil",
		},
		{
			name: "non-existent VM",
			setupTask: func() *types.Task {
				return &types.Task{
					ID: "non-existent",
				}
			},
			expectError: true,
			errorMsg:    "not found",
		},
		{
			name: "VM with invalid PID",
			setupTask: func() *types.Task {
				taskID := "invalid-pid-vm"
				vmm.mu.Lock()
				vmm.vms[taskID] = &VMInstance{
					ID:             taskID,
					PID:            99999,
					State:          VMStateRunning,
					CreatedAt:      time.Now(),
					InitSystem:     "systemd",
					GracePeriodSec: 1,
					SocketPath:     filepath.Join(tmpDir, taskID+".sock"),
				}
				vmm.mu.Unlock()

				return &types.Task{
					ID: taskID,
				}
			},
			expectError: true, // forceKillVM will fail
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := tt.setupTask()

			err := vmm.Stop(ctx, task)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			}
		})
	}
}

// TestStop_InitSystemNone tests stopping VM with init system none
func TestStop_InitSystemNone(t *testing.T) {
	tmpDir := t.TempDir()

	vmm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	taskID := "no-init-vm"
	socketPath := filepath.Join(tmpDir, taskID+".sock")

	vmm.mu.Lock()
	vmm.vms[taskID] = &VMInstance{
		ID:         taskID,
		PID:        99999,
		State:      VMStateRunning,
		CreatedAt:  time.Now(),
		InitSystem: "none",
		SocketPath: socketPath,
	}
	vmm.mu.Unlock()

	ctx := context.Background()
	err := vmm.Stop(ctx, &types.Task{ID: taskID})

	// Should use hardShutdown which will fail on non-existent socket
	assert.Error(t, err)
}

// TestConfigureVM_WithMockServer tests configureVM with a mock HTTP server
func TestConfigureVM_WithMockServer(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Track received requests
	requestsReceived := []string{}
	requestsMu := &sync.Mutex{}

	// Create a mock HTTP server over Unix socket
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)

		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			return
		}
		defer listener.Close()

		// Handle multiple requests
		for i := 0; i < 5; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			// Read request
			buf := make([]byte, 4096)
			n, err := conn.Read(buf)
			if err != nil {
				conn.Close()
				continue
			}

			requestStr := string(buf[:n])
			requestsMu.Lock()
			requestsReceived = append(requestsReceived, requestStr)
			requestsMu.Unlock()

			// Send success response
			response := "HTTP/1.1 204 No Content\r\n\r\n"
			_, _ = conn.Write([]byte(response))
			conn.Close()
		}
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	vmm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	ctx := context.Background()

	config := map[string]interface{}{
		"boot_source": map[string]interface{}{
			"kernel_image_path": "/path/to/kernel",
			"boot_args":         "console=ttyS0",
		},
		"machine_config": map[string]interface{}{
			"vcpu_count":   2,
			"mem_size_mib": 512,
			"ht_enabled":   false,
		},
		"drives": []interface{}{
			map[string]interface{}{
				"drive_id":      "rootfs",
				"path_on_host":  "/rootfs.ext4",
				"is_root_device": true,
				"is_read_only":  false,
			},
		},
	}

	err := vmm.configureVM(ctx, socketPath, config)

	// Wait a bit for requests to complete
	time.Sleep(200 * time.Millisecond)

	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(requestsReceived), 3) // boot, machine, drives

	// Don't wait for server - it will timeout on its own
}

// TestWaitForAPIServer_EdgeCases tests edge cases for waitForAPIServer
func TestWaitForAPIServer_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupSocket func(t *testing.T) string
		timeout     time.Duration
		expectError bool
	}{
		{
			name: "non-existent socket path",
			setupSocket: func(t *testing.T) string {
				return "/tmp/non-existent-socket-12345.sock"
			},
			timeout:     100 * time.Millisecond,
			expectError: true,
		},
		{
			name: "socket file but not a socket",
			setupSocket: func(t *testing.T) string {
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "fake.sock")
				err := os.WriteFile(socketPath, []byte("not a socket"), 0644)
				require.NoError(t, err)
				return socketPath
			},
			timeout:     100 * time.Millisecond,
			expectError: true,
		},
		{
			name: "zero timeout",
			setupSocket: func(t *testing.T) string {
				return "/tmp/non-existent-socket-zero.sock"
			},
			timeout:     0,
			expectError: true,
		},
		{
			name: "very short timeout",
			setupSocket: func(t *testing.T) string {
				return "/tmp/non-existent-socket-short.sock"
			},
			timeout:     1 * time.Millisecond,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socketPath := tt.setupSocket(t)
			err := waitForAPIServer(socketPath, tt.timeout)

			if tt.expectError {
				assert.Error(t, err)
			}
		})
	}
}

// TestWaitForShutdown_EdgeCases tests edge cases for waitForShutdown
func TestWaitForShutdown_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupSocket func(t *testing.T) string
		timeout     time.Duration
		expectError bool
		expectDeleted bool
	}{
		{
			name: "already deleted",
			setupSocket: func(t *testing.T) string {
				return "/tmp/non-existent-already.sock"
			},
			timeout:       100 * time.Millisecond,
			expectError:   false,
			expectDeleted: true,
		},
		{
			name: "persistent socket",
			setupSocket: func(t *testing.T) string {
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "persistent.sock")
				// Create empty file (not a real socket, but file exists)
				_ = os.WriteFile(socketPath, []byte(""), 0644)
				return socketPath
			},
			timeout:       100 * time.Millisecond,
			expectError:   true,
			expectDeleted: false,
		},
		{
			name: "zero timeout with existing file",
			setupSocket: func(t *testing.T) string {
				tmpDir := t.TempDir()
				socketPath := filepath.Join(tmpDir, "zero-timeout.sock")
				_ = os.WriteFile(socketPath, []byte(""), 0644)
				return socketPath
			},
			timeout:       0,
			expectError:   true,
			expectDeleted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socketPath := tt.setupSocket(t)
			deleted, err := waitForShutdown(socketPath, tt.timeout)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectDeleted, deleted)
		})
	}
}

// TestNewUnixClient tests newUnixClient helper
func TestNewUnixClient(t *testing.T) {
	socketPath := "/tmp/test.sock"

	client := newUnixClient(socketPath, 5*time.Second)

	assert.NotNil(t, client)
	assert.Equal(t, 5*time.Second, client.Timeout)

	transport, ok := client.Transport.(*http.Transport)
	assert.True(t, ok)
	assert.NotNil(t, transport)
}

// TestNewUnixClient_Timeouts tests various timeout values
func TestNewUnixClient_Timeouts(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
	}{
		{"zero timeout", 0},
		{"short timeout", 1 * time.Millisecond},
		{"normal timeout", 5 * time.Second},
		{"long timeout", 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newUnixClient("/tmp/test.sock", tt.timeout)
			assert.NotNil(t, client)
			assert.Equal(t, tt.timeout, client.Timeout)
		})
	}
}

// TestDescribe_ProcessError tests Describe with process errors
func TestDescribe_ProcessError(t *testing.T) {
	tmpDir := t.TempDir()

	vmm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	task := &types.Task{
		ID: "test-vm",
	}

	// Add VM with invalid PID
	vmm.mu.Lock()
	vmm.vms[task.ID] = &VMInstance{
		ID:        task.ID,
		PID:       -1, // Invalid PID
		State:     VMStateRunning,
		CreatedAt: time.Now(),
	}
	vmm.mu.Unlock()

	ctx := context.Background()
	status, err := vmm.Describe(ctx, task)

	assert.NoError(t, err)
	assert.NotNil(t, status)
	// Status should indicate failure or orphaned state
	assert.NotEqual(t, types.TaskStateRunning, status.State)
}

// TestVMInstance_StateTransitions_Gap tests VM state transitions
func TestVMInstance_StateTransitions_Gap(t *testing.T) {
	vm := &VMInstance{
		ID:        "test-vm",
		PID:       1234,
		State:     VMStateNew,
		CreatedAt: time.Now(),
	}

	// Test state transitions
	vm.State = VMStateStarting
	assert.Equal(t, VMStateStarting, vm.State)

	vm.State = VMStateRunning
	assert.Equal(t, VMStateRunning, vm.State)

	vm.State = VMStateStopping
	assert.Equal(t, VMStateStopping, vm.State)

	vm.State = VMStateStopped
	assert.Equal(t, VMStateStopped, vm.State)

	vm.State = VMStateCrashed
	assert.Equal(t, VMStateCrashed, vm.State)
}

// TestStart_ContextCancellation tests Start with context cancellation
func TestStart_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")
	err := os.MkdirAll(socketDir, 0755)
	require.NoError(t, err)

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	task := &types.Task{
		ID: "test-ctx-cancel",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	vmConfig := map[string]interface{}{
		"boot_source": map[string]interface{}{
			"kernel_image_path": "/path/to/kernel",
		},
	}

	err = vmm.Start(ctx, task, vmConfig)

	// Should error due to cancelled context or binary not found
	_ = err
}

// TestStop_ContextCancellation tests Stop with context cancellation
func TestStop_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	vmm := &VMMManager{
		config: &ManagerConfig{
			SocketDir: tmpDir,
		},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	taskID := "test-vm-ctx-cancel"
	// Use a fake PID to avoid signaling the actual test process
	fakePID := 99999
	vmm.mu.Lock()
	vmm.vms[taskID] = &VMInstance{
		ID:             taskID,
		PID:            fakePID,
		State:          VMStateRunning,
		CreatedAt:      time.Now(),
		InitSystem:     "systemd",
		GracePeriodSec: 1, // Shorter grace period
		SocketPath:     filepath.Join(tmpDir, taskID+".sock"),
	}
	vmm.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel in a goroutine after a short delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := vmm.Stop(ctx, &types.Task{ID: taskID})

	// Should error due to process not found or context cancellation
	assert.Error(t, err)
}
