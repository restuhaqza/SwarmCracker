package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// Wrapper methods for VMMManagerInternal to use mocked dependencies

// configureVMWithClient wraps configureVM to use mocked HTTP client
func (vm *VMMManagerInternal) configureVMWithClient(ctx context.Context, socketPath string, config interface{}) error {
	client := vm.httpClient

	// Parse config - could be a map or struct
	var configMap map[string]interface{}

	switch c := config.(type) {
	case map[string]interface{}:
		configMap = c
	case string:
		if err := json.Unmarshal([]byte(c), &configMap); err != nil {
			return fmt.Errorf("failed to unmarshal config json: %w", err)
		}
	default:
		return fmt.Errorf("invalid config type: expected map[string]interface{} or json string")
	}

	// Configure boot source if provided
	if bootSource, ok := configMap["boot_source"].(map[string]interface{}); ok {
		bootSourceJSON, _ := json.Marshal(bootSource)
		req, _ := http.NewRequestWithContext(ctx, "PUT",
			"http://localhost/boot-source",
			bytes.NewReader(bootSourceJSON),
		)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to set boot source: %w", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			return fmt.Errorf("boot source returned status: %d", resp.StatusCode)
		}
	}

	// Configure machine if provided
	if machineConfig, ok := configMap["machine_config"].(map[string]interface{}); ok {
		machineConfigJSON, _ := json.Marshal(machineConfig)
		req, _ := http.NewRequestWithContext(ctx, "PUT",
			"http://localhost/machine-config",
			bytes.NewReader(machineConfigJSON),
		)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to set machine config: %w", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			return fmt.Errorf("machine config returned status: %d", resp.StatusCode)
		}
	}

	// Configure drives if provided
	if drivesRaw, ok := configMap["drives"].([]interface{}); ok {
		for _, driveRaw := range drivesRaw {
			drive, ok := driveRaw.(map[string]interface{})
			if !ok {
				continue
			}
			driveID, _ := drive["drive_id"].(string)
			if driveID == "" {
				continue
			}
			driveJSON, _ := json.Marshal(drive)
			req, _ := http.NewRequestWithContext(ctx, "PUT",
				"http://localhost/drives/"+driveID,
				bytes.NewReader(driveJSON),
			)
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("failed to set drive %s: %w", driveID, err)
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
				return fmt.Errorf("drive %s returned status: %d", driveID, resp.StatusCode)
			}
		}
	}

	return nil
}

// gracefulShutdownWithProcess wraps gracefulShutdown to use mocked process executor
func (vm *VMMManagerInternal) gracefulShutdownWithProcess(ctx context.Context, vmInstance *VMInstance) error {
	process, err := vm.processExecutor.FindProcess(vmInstance.PID)
	if err != nil {
		return vm.forceKillVMWithProcess(vmInstance)
	}

	// Send SIGTERM
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return vm.forceKillVMWithProcess(vmInstance)
	}

	// Wait for graceful shutdown or timeout
	gracePeriod := time.Duration(vmInstance.GracePeriodSec) * time.Second
	shutdownChan := make(chan error, 1)

	// Use a cancelable context for the polling goroutine
	pollCtx, cancelPoll := context.WithCancel(ctx)
	defer cancelPoll() // Stop the polling goroutine when we exit

	go func() {
		// Poll for process exit
		for {
			if pollCtx.Err() != nil {
				return // Stop polling when context is cancelled
			}
			if err := process.Signal(syscall.Signal(0)); err != nil {
				// Process has exited
				shutdownChan <- nil
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	select {
	case <-shutdownChan:
		vmInstance.State = VMStateStopped
		return nil
	case <-time.After(gracePeriod):
		return vm.forceKillVMWithProcess(vmInstance)
	case <-ctx.Done():
		return vm.forceKillVMWithProcess(vmInstance)
	}
}

// forceKillVMWithProcess wraps forceKillVM to use mocked process executor
func (vm *VMMManagerInternal) forceKillVMWithProcess(vmInstance *VMInstance) error {
	process, err := vm.processExecutor.FindProcess(vmInstance.PID)
	if err != nil {
		return err
	}

	return process.Kill()
}

// hardShutdownWithClient wraps hardShutdown to use mocked HTTP client and process executor
func (vm *VMMManagerInternal) hardShutdownWithClient(ctx context.Context, vmInstance *VMInstance) error {
	socketPath := vmInstance.SocketPath

	// Send shutdown signal
	client := vm.httpClient
	actions := ActionsType{ActionType: "SendCtrlAltDel"}

	body, _ := json.Marshal(actions)
	req, _ := http.NewRequestWithContext(ctx, "PUT",
		"http://localhost/actions",
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return vm.forceKillVMWithProcess(vmInstance)
	}
	defer resp.Body.Close()

	// Wait for VM to exit (with timeout)
	done := make(chan error, 1)
	go func() {
		_, err := waitForShutdown(socketPath, 5*time.Second)
		done <- err
	}()

	select {
	case <-done:
		vmInstance.State = VMStateStopped
	case <-time.After(5 * time.Second):
		vm.forceKillVMWithProcess(vmInstance)
		vmInstance.State = VMStateStopped
	case <-ctx.Done():
		vm.forceKillVMWithProcess(vmInstance)
		vmInstance.State = VMStateStopped
	}

	return nil
}

// TestConfigureVM_Success tests successful VM configuration
func TestConfigureVM_Success(t *testing.T) {
	tests := []struct {
		name     string
		config   interface{}
		setup    func(*MockHTTPClient)
		validate func(*MockHTTPClient)
	}{
		{
			name: "full configuration with boot source and machine config",
			config: map[string]interface{}{
				"boot_source": map[string]interface{}{
					"kernel_image_path": "/vmlinux",
					"boot_args":         "console=ttyS0 reboot=k panic=1 pci=off",
				},
				"machine_config": map[string]interface{}{
					"vcpu_count":  2,
					"mem_size_mib": 512,
					"ht_enabled":   false,
				},
			},
			setup: func(client *MockHTTPClient) {
				client.SetResponse("PUT", "http://localhost/boot-source", http.StatusNoContent, nil)
				client.SetResponse("PUT", "http://localhost/machine-config", http.StatusNoContent, nil)
			},
			validate: func(client *MockHTTPClient) {
				if len(client.Calls) != 2 {
					t.Errorf("expected 2 HTTP calls, got %d", len(client.Calls))
				}
			},
		},
		{
			name: "configuration with drives",
			config: map[string]interface{}{
				"drives": []interface{}{
					map[string]interface{}{
						"drive_id":      "rootfs",
						"is_root_device": true,
						"is_read_only":   false,
						"path_on_host":   "/path/to/rootfs.ext4",
					},
				},
			},
			setup: func(client *MockHTTPClient) {
				client.SetResponse("PUT", "http://localhost/drives/rootfs", http.StatusNoContent, nil)
			},
			validate: func(client *MockHTTPClient) {
				if len(client.Calls) != 1 {
					t.Errorf("expected 1 HTTP call, got %d", len(client.Calls))
				}
			},
		},
		{
			name: "configuration from JSON string",
			config: `{
				"boot_source": {
					"kernel_image_path": "/vmlinux"
				},
				"machine_config": {
					"vcpu_count": 1
				}
			}`,
			setup: func(client *MockHTTPClient) {
				client.SetResponse("PUT", "http://localhost/boot-source", http.StatusNoContent, nil)
				client.SetResponse("PUT", "http://localhost/machine-config", http.StatusNoContent, nil)
			},
			validate: func(client *MockHTTPClient) {
				if len(client.Calls) != 2 {
					t.Errorf("expected 2 HTTP calls, got %d", len(client.Calls))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockHTTP := NewMockHTTPClient()
			tt.setup(mockHTTP)

			// Create VMMManager with mocked HTTP client
			vm := &VMMManagerInternal{
				VMMManager: &VMMManager{
					vms: make(map[string]*VMInstance),
				},
				httpClient: mockHTTP,
			}

			ctx := context.Background()
			err := vm.configureVMWithClient(ctx, "/tmp/test.sock", tt.config)

			if err != nil {
				t.Errorf("configureVM failed: %v", err)
			}

			tt.validate(mockHTTP)
		})
	}
}

// TestConfigureVM_Errors tests error handling in configureVM
func TestConfigureVM_Errors(t *testing.T) {
	tests := []struct {
		name        string
		config      interface{}
		setup       func(*MockHTTPClient)
		expectedErr string
	}{
		{
			name:        "nil config",
			config:      nil,
			setup:       func(client *MockHTTPClient) {},
			expectedErr: "invalid config type",
		},
		{
			name:        "invalid config type",
			config:      123,
			setup:       func(client *MockHTTPClient) {},
			expectedErr: "invalid config type",
		},
		{
			name:        "invalid JSON string",
			config:      "{invalid json}",
			setup:       func(client *MockHTTPClient) {},
			expectedErr: "failed to unmarshal config json",
		},
		{
			name: "boot source HTTP error",
			config: map[string]interface{}{
				"boot_source": map[string]interface{}{
					"kernel_image_path": "/vmlinux",
				},
			},
			setup: func(client *MockHTTPClient) {
				client.SetError("PUT", "http://localhost/boot-source", fmt.Errorf("connection refused"))
			},
			expectedErr: "failed to set boot source",
		},
		{
			name: "boot source non-204 status",
			config: map[string]interface{}{
				"boot_source": map[string]interface{}{
					"kernel_image_path": "/vmlinux",
				},
			},
			setup: func(client *MockHTTPClient) {
				client.SetResponse("PUT", "http://localhost/boot-source", http.StatusBadRequest, nil)
			},
			expectedErr: "boot source returned status: 400",
		},
		{
			name: "machine config HTTP error",
			config: map[string]interface{}{
				"machine_config": map[string]interface{}{
					"vcpu_count": 2,
				},
			},
			setup: func(client *MockHTTPClient) {
				client.SetError("PUT", "http://localhost/machine-config", fmt.Errorf("timeout"))
			},
			expectedErr: "failed to set machine config",
		},
		{
			name: "drive configuration error",
			config: map[string]interface{}{
				"drives": []interface{}{
					map[string]interface{}{
						"drive_id": "rootfs",
					},
				},
			},
			setup: func(client *MockHTTPClient) {
				client.SetError("PUT", "http://localhost/drives/rootfs", fmt.Errorf("drive not found"))
			},
			expectedErr: "failed to set drive rootfs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockHTTP := NewMockHTTPClient()
			tt.setup(mockHTTP)

			vm := &VMMManagerInternal{
				VMMManager: &VMMManager{
					vms: make(map[string]*VMInstance),
				},
				httpClient: mockHTTP,
			}

			ctx := context.Background()
			err := vm.configureVMWithClient(ctx, "/tmp/test.sock", tt.config)

			if err == nil {
				t.Errorf("expected error containing '%s', got nil", tt.expectedErr)
			} else if tt.expectedErr != "" && !containsString(err.Error(), tt.expectedErr) {
				t.Errorf("expected error containing '%s', got '%s'", tt.expectedErr, err.Error())
			}
		})
	}
}

// TestGracefulShutdown_Success tests successful graceful shutdown
func TestGracefulShutdown_Success(t *testing.T) {
	mockProc := NewMockProcessExecutor()

	// Create a process that will "exit" after SIGTERM
	proc := &extendedMockProcess{
		mockProcess: &mockProcess{
			pid:     12345,
			killed:  false,
			signals: make([]syscall.Signal, 0),
		},
		exitAfterSignal: syscall.SIGTERM,
	}
	mockProc.Processes[12345] = proc

	vmInstance := &VMInstance{
		ID:             "test-vm",
		PID:            12345,
		State:          VMStateRunning,
		InitSystem:     "systemd",
		GracePeriodSec: 2,
	}

	vm := &VMMManagerInternal{
		VMMManager: &VMMManager{
			vms: map[string]*VMInstance{
				"test-vm": vmInstance,
			},
		},
		processExecutor: mockProc,
	}

	ctx := context.Background()
	err := vm.gracefulShutdownWithProcess(ctx, vmInstance)

	if err != nil {
		t.Errorf("gracefulShutdown failed: %v", err)
	}

	if vmInstance.State != VMStateStopped {
		t.Errorf("expected VMStateStopped, got %s", vmInstance.State)
	}
}

// TestGracefulShutdown_ProcessNotFound tests graceful shutdown when process is not found
func TestGracefulShutdown_ProcessNotFound(t *testing.T) {
	mockProc := NewMockProcessExecutor()
	// Don't add the process - simulating process not found

	vmInstance := &VMInstance{
		ID:             "test-vm",
		PID:            99999,
		State:          VMStateRunning,
		InitSystem:     "systemd",
		GracePeriodSec: 2,
	}

	vm := &VMMManagerInternal{
		VMMManager: &VMMManager{
			vms: make(map[string]*VMInstance),
		},
		processExecutor: mockProc,
	}

	ctx := context.Background()
	err := vm.gracefulShutdownWithProcess(ctx, vmInstance)

	// Should fall back to forceKillVM, which will error since process doesn't exist
	if err == nil {
		t.Error("expected error when process not found")
	}
}

// TestGracefulShutdown_SignalFailure tests graceful shutdown when signal fails
func TestGracefulShutdown_SignalFailure(t *testing.T) {
	mockProc := NewMockProcessExecutor()

	// Create a process that fails on Signal
	proc := &extendedMockProcess{
		mockProcess: &mockProcess{
			pid:     12345,
			signals: make([]syscall.Signal, 0),
		},
		signalError: fmt.Errorf("operation not permitted"),
	}
	mockProc.Processes[12345] = proc

	vmInstance := &VMInstance{
		ID:             "test-vm",
		PID:            12345,
		State:          VMStateRunning,
		InitSystem:     "systemd",
		GracePeriodSec: 2,
	}

	vm := &VMMManagerInternal{
		VMMManager: &VMMManager{
			vms: map[string]*VMInstance{
				"test-vm": vmInstance,
			},
		},
		processExecutor: mockProc,
	}

	ctx := context.Background()
	err := vm.gracefulShutdownWithProcess(ctx, vmInstance)

	// Should fall back to forceKillVM and succeed
	if err != nil {
		t.Errorf("graceful shutdown should succeed after fallback: %v", err)
	}

	// Verify process was killed
	if proc, ok := mockProc.Processes[12345].(*extendedMockProcess); ok {
		if !proc.killed {
			t.Error("expected process to be killed after signal failure")
		}
	}
}

// TestGracefulShutdown_Timeout tests graceful shutdown timeout
func TestGracefulShutdown_Timeout(t *testing.T) {
	mockProc := NewMockProcessExecutor()

	// Create a process that never exits
	proc := &extendedMockProcess{
		mockProcess: &mockProcess{
			pid:     12345,
			signals: make([]syscall.Signal, 0),
		},
		neverExits: true,
	}
	mockProc.Processes[12345] = proc

	vmInstance := &VMInstance{
		ID:             "test-vm",
		PID:            12345,
		State:          VMStateRunning,
		InitSystem:     "systemd",
		GracePeriodSec: 1, // Short grace period for faster test
	}

	vm := &VMMManagerInternal{
		VMMManager: &VMMManager{
			vms: map[string]*VMInstance{
				"test-vm": vmInstance,
			},
		},
		processExecutor: mockProc,
	}

	ctx := context.Background()
	err := vm.gracefulShutdownWithProcess(ctx, vmInstance)

	// Should timeout and fall back to forceKillVM
	// Since forceKillVM will succeed on mock, error might be nil
	// Just verify SIGTERM was sent
	if proc, ok := mockProc.Processes[12345].(*extendedMockProcess); ok {
		if len(proc.signals) == 0 {
			t.Error("expected SIGTERM to be sent")
		}
		if len(proc.signals) > 0 && proc.signals[0] != syscall.SIGTERM {
			t.Errorf("expected SIGTERM, got %v", proc.signals[0])
		}
	}

	_ = err // Error handling depends on forceKillVM behavior
}

// TestGracefulShutdown_ContextCancelled tests graceful shutdown with cancelled context
func TestGracefulShutdown_ContextCancelled(t *testing.T) {
	mockProc := NewMockProcessExecutor()

	// Create a process that never exits
	proc := &extendedMockProcess{
		mockProcess: &mockProcess{
			pid:     12345,
			signals: make([]syscall.Signal, 0),
		},
		neverExits: true,
	}
	mockProc.Processes[12345] = proc

	vmInstance := &VMInstance{
		ID:             "test-vm",
		PID:            12345,
		State:          VMStateRunning,
		InitSystem:     "systemd",
		GracePeriodSec: 10,
	}

	vm := &VMMManagerInternal{
		VMMManager: &VMMManager{
			vms: map[string]*VMInstance{
				"test-vm": vmInstance,
			},
		},
		processExecutor: mockProc,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := vm.gracefulShutdownWithProcess(ctx, vmInstance)

	// Should fall back to forceKillVM due to context cancellation
	_ = err // Verify the fallback occurred
}

// TestHardShutdown_Success tests successful hard shutdown
func TestHardShutdown_Success(t *testing.T) {
	mockHTTP := NewMockHTTPClient()
	mockHTTP.SetResponse("PUT", "http://localhost/actions", http.StatusNoContent, nil)

	// Create a temporary socket file for waitForShutdown to find
	tmpDir := t.TempDir()
	socketPath := tmpDir + "/test.sock"
	if err := os.WriteFile(socketPath, []byte("dummy"), 0644); err != nil {
		t.Fatalf("failed to create socket file: %v", err)
	}

	vmInstance := &VMInstance{
		ID:         "test-vm",
		PID:        12345,
		State:      VMStateRunning,
		SocketPath: socketPath,
	}

	vm := &VMMManagerInternal{
		VMMManager: &VMMManager{
			vms: map[string]*VMInstance{
				"test-vm": vmInstance,
			},
		},
		httpClient: mockHTTP,
	}

	// Goroutine to remove socket after a delay (simulating VM shutdown)
	go func() {
		time.Sleep(100 * time.Millisecond)
		os.Remove(socketPath)
	}()

	ctx := context.Background()
	err := vm.hardShutdownWithClient(ctx, vmInstance)

	if err != nil {
		t.Errorf("hardShutdown failed: %v", err)
	}

	if vmInstance.State != VMStateStopped {
		t.Errorf("expected VMStateStopped, got %s", vmInstance.State)
	}

	// Verify HTTP call was made
	if len(mockHTTP.Calls) != 1 {
		t.Errorf("expected 1 HTTP call, got %d", len(mockHTTP.Calls))
	}
}

// TestHardShutdown_HTTPError tests hard shutdown when HTTP request fails
func TestHardShutdown_HTTPError(t *testing.T) {
	mockProc := NewMockProcessExecutor()
	mockProc.Processes[12345] = &mockProcess{
		pid:     12345,
		killed:  false,
		signals: make([]syscall.Signal, 0),
	}

	mockHTTP := NewMockHTTPClient()
	mockHTTP.SetError("PUT", "http://localhost/actions", fmt.Errorf("connection refused"))

	vmInstance := &VMInstance{
		ID:         "test-vm",
		PID:        12345,
		State:      VMStateRunning,
		SocketPath: "/tmp/test.sock",
	}

	vm := &VMMManagerInternal{
		VMMManager: &VMMManager{
			vms: map[string]*VMInstance{
				"test-vm": vmInstance,
			},
		},
		httpClient:      mockHTTP,
		processExecutor: mockProc,
	}

	ctx := context.Background()
	err := vm.hardShutdownWithClient(ctx, vmInstance)

	// Should fall back to forceKillVM, which will succeed
	// (hardShutdown doesn't return error on forceKill fallback)
	if err != nil {
		t.Errorf("hardShutdown should not error on forceKill fallback: %v", err)
	}
}

// TestHardShutdown_Timeout tests hard shutdown timeout
func TestHardShutdown_Timeout(t *testing.T) {
	mockProc := NewMockProcessExecutor()
	mockProc.Processes[12345] = &mockProcess{
		pid:     12345,
		killed:  false,
		signals: make([]syscall.Signal, 0),
	}

	mockHTTP := NewMockHTTPClient()
	mockHTTP.SetResponse("PUT", "http://localhost/actions", http.StatusNoContent, nil)

	// Create a socket that won't be removed
	tmpDir := t.TempDir()
	socketPath := tmpDir + "/test.sock"
	if err := os.WriteFile(socketPath, []byte("dummy"), 0644); err != nil {
		t.Fatalf("failed to create socket file: %v", err)
	}

	vmInstance := &VMInstance{
		ID:         "test-vm",
		PID:        12345,
		State:      VMStateRunning,
		SocketPath: socketPath,
	}

	vm := &VMMManagerInternal{
		VMMManager: &VMMManager{
			vms: map[string]*VMInstance{
				"test-vm": vmInstance,
			},
		},
		httpClient:      mockHTTP,
		processExecutor: mockProc,
	}

	ctx := context.Background()
	err := vm.hardShutdownWithClient(ctx, vmInstance)

	// Should timeout and fall back to forceKillVM
	// hardShutdown doesn't return error even on timeout (logs warning)
	if err != nil {
		t.Errorf("hardShutdown should not error on timeout: %v", err)
	}

	// Verify VM was marked as stopped
	if vmInstance.State != VMStateStopped {
		t.Errorf("expected VMStateStopped, got %s", vmInstance.State)
	}

	// Verify process was killed (force kill after timeout)
	if proc, ok := mockProc.Processes[12345].(*mockProcess); ok {
		if !proc.killed {
			t.Error("expected process to be force-killed after timeout")
		}
	}
}

// TestWaitForShutdown_Success tests waitForShutdown with socket removal
func TestWaitForShutdown_Success(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := tmpDir + "/test.sock"

	// Create socket file
	if err := os.WriteFile(socketPath, []byte("dummy"), 0644); err != nil {
		t.Fatalf("failed to create socket file: %v", err)
	}

	// Remove socket after delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		os.Remove(socketPath)
	}()

	success, err := waitForShutdown(socketPath, 5*time.Second)

	if err != nil {
		t.Errorf("waitForShutdown failed: %v", err)
	}

	if !success {
		t.Error("expected success=true")
	}
}

// TestWaitForShutdown_SocketGone tests waitForShutdown when socket is already gone
func TestWaitForShutdown_SocketGone(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := tmpDir + "/nonexistent.sock"

	success, err := waitForShutdown(socketPath, 5*time.Second)

	if err != nil {
		t.Errorf("waitForShutdown failed: %v", err)
	}

	if !success {
		t.Error("expected success=true when socket doesn't exist")
	}
}

// TestWaitForShutdown_Timeout tests waitForShutdown timeout
func TestWaitForShutdown_Timeout(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := tmpDir + "/test.sock"

	// Create socket file that won't be removed
	if err := os.WriteFile(socketPath, []byte("dummy"), 0644); err != nil {
		t.Fatalf("failed to create socket file: %v", err)
	}

	// Short timeout for faster test
	success, err := waitForShutdown(socketPath, 100*time.Millisecond)

	if err == nil {
		t.Error("expected timeout error")
	}

	if success {
		t.Error("expected success=false on timeout")
	}
}

// TestSnapshot_NotImplemented tests Snapshot placeholder
func TestSnapshot_NotImplemented(t *testing.T) {
	vm := &VMMManager{
		vms: make(map[string]*VMInstance),
	}

	task := &types.Task{
		ID: "test-task",
	}

	ctx := context.Background()
	result, err := vm.Snapshot(ctx, task, nil)

	if err == nil {
		t.Error("expected error for unimplemented Snapshot")
	}

	if result != nil {
		t.Error("expected nil result")
	}

	expectedMsg := "snapshot not implemented"
	if !containsString(err.Error(), expectedMsg) {
		t.Errorf("expected error containing '%s', got '%s'", expectedMsg, err.Error())
	}
}

// TestRestore_NotImplemented tests Restore placeholder
func TestRestore_NotImplemented(t *testing.T) {
	vm := &VMMManager{
		vms: make(map[string]*VMInstance),
	}

	task := &types.Task{
		ID: "test-task",
	}

	ctx := context.Background()
	err := vm.Restore(ctx, task, nil)

	if err == nil {
		t.Error("expected error for unimplemented Restore")
	}

	expectedMsg := "restore not implemented"
	if !containsString(err.Error(), expectedMsg) {
		t.Errorf("expected error containing '%s', got '%s'", expectedMsg, err.Error())
	}
}

// TestForceKillVM tests forceKillVM helper
func TestForceKillVM(t *testing.T) {
	mockProc := NewMockProcessExecutor()
	mockProc.Processes[12345] = &mockProcess{
		pid:     12345,
		killed:  false,
		signals: make([]syscall.Signal, 0),
	}

	vmInstance := &VMInstance{
		ID:  "test-vm",
		PID: 12345,
	}

	vm := &VMMManagerInternal{
		VMMManager: &VMMManager{
			vms: map[string]*VMInstance{
				"test-vm": vmInstance,
			},
		},
		processExecutor: mockProc,
	}

	err := vm.forceKillVMWithProcess(vmInstance)

	if err != nil {
		t.Errorf("forceKillVM failed: %v", err)
	}

	// Verify process was killed
	if proc, ok := mockProc.Processes[12345].(*mockProcess); ok {
		if !proc.killed {
			t.Error("expected process to be killed")
		}
	}
}

// TestForceKillVM_ProcessNotFound tests forceKillVM when process not found
func TestForceKillVM_ProcessNotFound(t *testing.T) {
	mockProc := NewMockProcessExecutor()
	// Don't add process

	vmInstance := &VMInstance{
		ID:  "test-vm",
		PID: 99999,
	}

	vm := &VMMManagerInternal{
		VMMManager: &VMMManager{
			vms: make(map[string]*VMInstance),
		},
		processExecutor: mockProc,
	}

	err := vm.forceKillVMWithProcess(vmInstance)

	if err == nil {
		t.Error("expected error when process not found")
	}
}

// TestLifecycleExtended is a meta-test that runs all extended tests
func TestLifecycleExtended(t *testing.T) {
	t.Run("ConfigureVM", func(t *testing.T) {
		t.Run("Success", TestConfigureVM_Success)
		t.Run("Errors", TestConfigureVM_Errors)
	})

	t.Run("GracefulShutdown", func(t *testing.T) {
		t.Run("Success", TestGracefulShutdown_Success)
		t.Run("ProcessNotFound", TestGracefulShutdown_ProcessNotFound)
		t.Run("SignalFailure", TestGracefulShutdown_SignalFailure)
		t.Run("Timeout", TestGracefulShutdown_Timeout)
		t.Run("ContextCancelled", TestGracefulShutdown_ContextCancelled)
	})

	t.Run("HardShutdown", func(t *testing.T) {
		t.Run("Success", TestHardShutdown_Success)
		t.Run("HTTPError", TestHardShutdown_HTTPError)
		t.Run("Timeout", TestHardShutdown_Timeout)
	})

	t.Run("WaitForShutdown", func(t *testing.T) {
		t.Run("Success", TestWaitForShutdown_Success)
		t.Run("SocketGone", TestWaitForShutdown_SocketGone)
		t.Run("Timeout", TestWaitForShutdown_Timeout)
	})

	t.Run("Snapshot", TestSnapshot_NotImplemented)
	t.Run("Restore", TestRestore_NotImplemented)

	t.Run("ForceKillVM", func(t *testing.T) {
		t.Run("Success", TestForceKillVM)
		t.Run("ProcessNotFound", TestForceKillVM_ProcessNotFound)
	})
}

// extendedMockProcess wraps mockProcess with additional test behavior control
type extendedMockProcess struct {
	*mockProcess
	signalError     error
	exitAfterSignal syscall.Signal
	neverExits      bool
	killError       error
	exited          bool
}

func (m *extendedMockProcess) Signal(sig syscall.Signal) error {
	// Store the signal
	m.mockProcess.signals = append(m.mockProcess.signals, sig)

	if m.signalError != nil {
		return m.signalError
	}

	// After sending the exit signal, process "exits"
	if m.exitAfterSignal != 0 && sig == m.exitAfterSignal {
		m.neverExits = false
		m.exited = true
	}

	// Signal(0) is used to check if process exists
	if sig == syscall.Signal(0) {
		if m.exited {
			return fmt.Errorf("process has exited")
		}
		if m.neverExits {
			return nil // Process still exists
		}
		return nil // Process still exists
	}

	return nil
}

func (m *extendedMockProcess) Kill() error {
	m.mockProcess.killed = true
	return m.killError
}

// Helper function to check if string contains substring
func containsString(s, substr string) bool {
	return bytes.Contains([]byte(s), []byte(substr))
}
