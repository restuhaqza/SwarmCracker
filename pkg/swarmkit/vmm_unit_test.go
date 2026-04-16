package swarmkit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// newTestVMMManager creates a VMMManager for testing with a temp socket dir
func newTestVMMManager(t *testing.T, useJailer bool) *VMMManager {
	t.Helper()

	tempDir := t.TempDir()

	cfg := &VMMManagerConfig{
		FirecrackerPath: "", // Will try to resolve, may fail
		JailerPath:      "", // Will try to resolve if jailer enabled
		SocketDir:       tempDir,
		UseJailer:       useJailer,
		JailerUID:       1000,
		JailerGID:       1000,
		JailerChrootDir: filepath.Join(tempDir, "jailer"),
		EnableCgroups:   false,
	}

	vmm, err := NewVMMManagerWithConfig(cfg)
	if err != nil {
		// If firecracker/jailer binary not found, create a partially initialized VMM for testing
		// This allows testing methods that don't require the actual binary
		if useJailer && err != nil && err.Error() != "" {
			// Try without jailer for tests that don't need it
			cfg.UseJailer = false
			vmm, err = NewVMMManagerWithConfig(cfg)
			if err != nil {
				t.Skipf("Skipping test: firecracker binary not found: %v", err)
				return nil
			}
		} else {
			t.Skipf("Skipping test: firecracker binary not found: %v", err)
			return nil
		}
	}

	return vmm
}

// hasFirecrackerBinary checks if firecracker binary is available
func hasFirecrackerBinary() bool {
	_, err := exec.LookPath("firecracker")
	return err == nil
}

// hasJailerBinary checks if jailer binary is available
func hasJailerBinary() bool {
	_, err := exec.LookPath("jailer")
	return err == nil
}

// TestToInt tests the toInt conversion function
func TestToInt(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected int
	}{
		{
			name:     "int value",
			input:    42,
			expected: 42,
		},
		{
			name:     "negative int",
			input:    -10,
			expected: -10,
		},
		{
			name:     "float64 value",
			input:    float64(3.14),
			expected: 3,
		},
		{
			name:     "float64 whole number",
			input:    float64(100.0),
			expected: 100,
		},
		{
			name:     "int64 value",
			input:    int64(1234567890),
			expected: 1234567890,
		},
		{
			name:     "negative int64",
			input:    int64(-999),
			expected: -999,
		},
		{
			name:     "string - unknown type",
			input:    "not a number",
			expected: 0,
		},
		{
			name:     "nil",
			input:    nil,
			expected: 0,
		},
		{
			name:     "bool - unknown type",
			input:    true,
			expected: 0,
		},
		{
			name:     "float32 - unknown type",
			input:    float32(3.14),
			expected: 0,
		},
		{
			name:     "uint - unknown type",
			input:    uint(42),
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toInt(tt.input)
			if result != tt.expected {
				t.Errorf("toInt(%v) = %d; want %d", tt.input, result, tt.expected)
			}
		})
	}
}

// TestNewVMMManager tests NewVMMManager (without jailer)
func TestNewVMMManager(t *testing.T) {
	if !hasFirecrackerBinary() {
		t.Skip("firecracker binary not found")
	}

	tempDir := t.TempDir()

	vmm, err := NewVMMManager("", tempDir)
	if err != nil {
		t.Fatalf("NewVMMManager() error = %v", err)
	}

	if vmm == nil {
		t.Fatal("NewVMMManager() returned nil VMMManager")
	}

	if vmm.useJailer {
		t.Error("NewVMMManager() should have useJailer=false")
	}

	if vmm.socketDir != tempDir {
		t.Errorf("socketDir = %s; want %s", vmm.socketDir, tempDir)
	}
}

// TestNewVMMManagerWithValidConfig tests NewVMMManagerWithConfig with valid config
func TestNewVMMManagerWithValidConfig(t *testing.T) {
	if !hasFirecrackerBinary() {
		t.Skip("firecracker binary not found")
	}

	tempDir := t.TempDir()

	tests := []struct {
		name      string
		useJailer bool
	}{
		{
			name:      "without jailer",
			useJailer: false,
		},
		{
			name:      "with jailer",
			useJailer: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.useJailer && !hasJailerBinary() {
				t.Skip("jailer binary not found")
			}

			cfg := &VMMManagerConfig{
				FirecrackerPath: "",
				JailerPath:      "",
				SocketDir:       tempDir,
				UseJailer:       tt.useJailer,
				JailerUID:       1000,
				JailerGID:       1000,
				JailerChrootDir: filepath.Join(tempDir, "jailer"),
				EnableCgroups:   false,
			}

			vmm, err := NewVMMManagerWithConfig(cfg)
			if err != nil {
				t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
			}

			if vmm == nil {
				t.Fatal("NewVMMManagerWithConfig() returned nil VMMManager")
			}

			if vmm.useJailer != tt.useJailer {
				t.Errorf("useJailer = %v; want %v", vmm.useJailer, tt.useJailer)
			}

			if vmm.socketDir != tempDir {
				t.Errorf("socketDir = %s; want %s", vmm.socketDir, tempDir)
			}
		})
	}
}

// TestNewVMMManagerWithEmptyFirecrackerPath tests with empty firecracker path (should try LookPath)
func TestNewVMMManagerWithEmptyFirecrackerPath(t *testing.T) {
	if !hasFirecrackerBinary() {
		t.Skip("firecracker binary not found")
	}

	tempDir := t.TempDir()

	cfg := &VMMManagerConfig{
		FirecrackerPath: "", // Empty - should resolve via LookPath
		SocketDir:       tempDir,
		UseJailer:       false,
	}

	vmm, err := NewVMMManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
	}

	if vmm.firecrackerPath == "" {
		t.Error("firecrackerPath should not be empty after resolution")
	}
}

// TestNewVMMManagerWithMissingFirecracker tests error when firecracker not found
func TestNewVMMManagerWithMissingFirecracker(t *testing.T) {
	if hasFirecrackerBinary() {
		t.Skip("firecracker binary is available, cannot test missing case")
	}

	tempDir := t.TempDir()

	cfg := &VMMManagerConfig{
		FirecrackerPath: "",
		SocketDir:       tempDir,
		UseJailer:       false,
	}

	_, err := NewVMMManagerWithConfig(cfg)
	if err == nil {
		t.Error("Expected error when firecracker binary not found, got nil")
	}
}

// TestNewVMMManagerWithJailer tests jailer initialization
func TestNewVMMManagerWithJailer(t *testing.T) {
	if !hasFirecrackerBinary() {
		t.Skip("firecracker binary not found")
	}
	if !hasJailerBinary() {
		t.Skip("jailer binary not found")
	}

	tempDir := t.TempDir()

	tests := []struct {
		name         string
		enableCgroups bool
	}{
		{
			name:         "jailer without cgroups",
			enableCgroups: false,
		},
		{
			name:         "jailer with cgroups",
			enableCgroups: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &VMMManagerConfig{
				FirecrackerPath: "",
				JailerPath:      "",
				SocketDir:       tempDir,
				UseJailer:       true,
				JailerUID:       1000,
				JailerGID:       1000,
				JailerChrootDir: filepath.Join(tempDir, "jailer"),
				EnableCgroups:   tt.enableCgroups,
			}

			vmm, err := NewVMMManagerWithConfig(cfg)
			if err != nil {
				t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
			}

			if !vmm.useJailer {
				t.Error("useJailer should be true")
			}

			if vmm.jailer == nil {
				t.Error("jailer should be initialized")
			}

			// Note: cgroupMgr may be nil if cgroups can't be created (requires root)
			// This is expected behavior in non-root environments
			if tt.enableCgroups {
				if vmm.cgroupMgr != nil {
					t.Log("cgroupMgr initialized (has root privileges)")
				} else {
					t.Log("cgroupMgr not initialized (expected without root privileges)")
				}
			} else {
				if vmm.cgroupMgr != nil {
					t.Error("cgroupMgr should be nil when EnableCgroups=false")
				}
			}
		})
	}
}

// TestNewVMMManagerWithMissingJailer tests error when jailer not found
func TestNewVMMManagerWithMissingJailer(t *testing.T) {
	if !hasFirecrackerBinary() {
		t.Skip("firecracker binary not found")
	}
	if hasJailerBinary() {
		t.Skip("jailer binary is available, cannot test missing case")
	}

	tempDir := t.TempDir()

	cfg := &VMMManagerConfig{
		FirecrackerPath: "",
		JailerPath:      "", // Will try to find jailer, should fail
		SocketDir:       tempDir,
		UseJailer:       true,
	}

	_, err := NewVMMManagerWithConfig(cfg)
	if err == nil {
		t.Error("Expected error when jailer binary not found, got nil")
	}
}

// TestDescribe tests the Describe method
func TestDescribe(t *testing.T) {
	vmm := newTestVMMManager(t, false)
	if vmm == nil {
		return
	}

	ctx := context.Background()

	tests := []struct {
		name        string
		setupFunc   func(*VMMManager) string
		wantState   types.TaskState
		wantMessage string
	}{
		{
			name: "task not found",
			setupFunc: func(v *VMMManager) string {
				return "nonexistent-task"
			},
			wantState:   types.TaskStateComplete,
			wantMessage: "Task not running",
		},
		{
			name: "task running",
			setupFunc: func(v *VMMManager) string {
				if !hasFirecrackerBinary() {
					return ""
				}
				// Create a fake process entry
				taskID := "test-task-running"
				cmd := exec.Command("sleep", "1")
				if err := cmd.Start(); err != nil {
					return ""
				}
				v.processes[taskID] = cmd
				t.Cleanup(func() {
					if cmd.Process != nil {
						cmd.Process.Kill()
					}
				})
				return taskID
			},
			wantState:   types.TaskStateRunning,
			wantMessage: "Task is running",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskID := tt.setupFunc(vmm)
			if taskID == "" && tt.name == "task running" {
				t.Skip("cannot create test process")
			}

			task := &types.Task{ID: taskID}
			status, err := vmm.Describe(ctx, task)
			if err != nil {
				t.Fatalf("Describe() error = %v", err)
			}

			if status.State != tt.wantState {
				t.Errorf("Describe() State = %v; want %v", status.State, tt.wantState)
			}

			if status.Message != tt.wantMessage {
				t.Errorf("Describe() Message = %s; want %s", status.Message, tt.wantMessage)
			}
		})
	}
}

// TestIsRunning tests the IsRunning method
func TestIsRunning(t *testing.T) {
	vmm := newTestVMMManager(t, false)
	if vmm == nil {
		return
	}

	tests := []struct {
		name     string
		setup    func(*VMMManager) string
		expected bool
	}{
		{
			name: "no process",
			setup: func(v *VMMManager) string {
				return "nonexistent-task"
			},
			expected: false,
		},
		{
			name: "registered process",
			setup: func(v *VMMManager) string {
				// Create a real process
				cmd := exec.Command("sleep", "1")
				if err := cmd.Start(); err != nil {
					return ""
				}
				taskID := "test-task-isrunning"
				v.processes[taskID] = cmd
				t.Cleanup(func() {
					if cmd.Process != nil {
						cmd.Process.Kill()
					}
				})
				return taskID
			},
			expected: true,
		},
		{
			name: "exited process",
			setup: func(v *VMMManager) string {
				// Create a process that exits immediately
				cmd := exec.Command("true")
				if err := cmd.Start(); err != nil {
					return ""
				}
				cmd.Wait() // Wait for it to exit
				taskID := "test-task-exited"
				v.processes[taskID] = cmd
				return taskID
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskID := tt.setup(vmm)
			if taskID == "" && tt.name != "no process" {
				t.Skip("cannot create test process")
			}

			result := vmm.IsRunning(taskID)
			if result != tt.expected {
				t.Errorf("IsRunning() = %v; want %v", result, tt.expected)
			}
		})
	}
}

// TestGetPID tests the GetPID method
func TestGetPID(t *testing.T) {
	vmm := newTestVMMManager(t, false)
	if vmm == nil {
		return
	}

	tests := []struct {
		name        string
		setup       func(*VMMManager) string
		expectedPID int
	}{
		{
			name: "no process returns 0",
			setup: func(v *VMMManager) string {
				return "nonexistent-task"
			},
			expectedPID: 0,
		},
		{
			name: "registered process returns PID",
			setup: func(v *VMMManager) string {
				cmd := exec.Command("sleep", "1")
				if err := cmd.Start(); err != nil {
					return ""
				}
				taskID := "test-task-pid"
				v.processes[taskID] = cmd
				t.Cleanup(func() {
					if cmd.Process != nil {
						cmd.Process.Kill()
					}
				})
				return taskID
			},
			expectedPID: -1, // Will check non-zero instead
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskID := tt.setup(vmm)
			if taskID == "" && tt.name != "no process returns 0" {
				t.Skip("cannot create test process")
			}

			pid := vmm.GetPID(taskID)
			if tt.expectedPID == 0 {
				if pid != 0 {
					t.Errorf("GetPID() = %d; want 0", pid)
				}
			} else {
				if pid == 0 {
					t.Errorf("GetPID() = 0; want non-zero PID")
				}
			}
		})
	}
}

// TestForceStop tests the ForceStop method
func TestForceStop(t *testing.T) {
	vmm := newTestVMMManager(t, false)
	if vmm == nil {
		return
	}

	ctx := context.Background()

	tests := []struct {
		name        string
		setup       func(*VMMManager) string
		expectError bool
	}{
		{
			name: "no process returns error",
			setup: func(v *VMMManager) string {
				return "nonexistent-task"
			},
			expectError: true,
		},
		{
			name: "force stop registered process",
			setup: func(v *VMMManager) string {
				cmd := exec.Command("sleep", "10")
				if err := cmd.Start(); err != nil {
					return ""
				}
				taskID := "test-task-forcestop"
				v.processes[taskID] = cmd
				return taskID
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskID := tt.setup(vmm)
			if taskID == "" && tt.name != "no process returns error" {
				t.Skip("cannot create test process")
			}

			task := &types.Task{ID: taskID}
			err := vmm.ForceStop(ctx, task)

			if tt.expectError {
				if err == nil {
					t.Error("ForceStop() expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("ForceStop() unexpected error = %v", err)
				}
				// Verify process was removed
				if _, ok := vmm.processes[taskID]; ok {
					t.Error("ForceStop() should remove process from map")
				}
			}
		})
	}
}

// TestRemove tests the Remove method
func TestRemove(t *testing.T) {
	vmm := newTestVMMManager(t, false)
	if vmm == nil {
		return
	}

	ctx := context.Background()

	tests := []struct {
		name        string
		setup       func(*VMMManager) string
		expectError bool
	}{
		{
			name: "remove nonexistent task - just cleans socket",
			setup: func(v *VMMManager) string {
				return "nonexistent-task"
			},
			expectError: false,
		},
		{
			name: "remove task with process",
			setup: func(v *VMMManager) string {
				cmd := exec.Command("sleep", "10")
				if err := cmd.Start(); err != nil {
					return ""
				}
				taskID := "test-task-remove"
				v.processes[taskID] = cmd
				return taskID
			},
			expectError: false,
		},
		{
			name: "remove with socket file",
			setup: func(v *VMMManager) string {
				// Create a socket file
				taskID := "test-task-socket"
				socketPath := filepath.Join(vmm.socketDir, taskID+".sock")
				file, err := os.Create(socketPath)
				if err != nil {
					return ""
				}
				file.Close()
				return taskID
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskID := tt.setup(vmm)
			if taskID == "" && tt.name != "remove nonexistent task - just cleans socket" {
				t.Skip("cannot setup test")
			}

			task := &types.Task{ID: taskID}
			err := vmm.Remove(ctx, task)

			if tt.expectError && err != nil {
				t.Errorf("Remove() unexpected error = %v", err)
			}

			// Verify process was removed
			if _, ok := vmm.processes[taskID]; ok {
				t.Error("Remove() should remove process from map")
			}

			// Verify socket was removed
			socketPath := filepath.Join(vmm.socketDir, taskID+".sock")
			if _, err := os.Stat(socketPath); err == nil {
				t.Error("Remove() should remove socket file")
			}
		})
	}
}

// TestWait tests the Wait method
func TestWait(t *testing.T) {
	vmm := newTestVMMManager(t, false)
	if vmm == nil {
		return
	}

	ctx := context.Background()

	tests := []struct {
		name        string
		setup       func(*VMMManager) string
		wantState   types.TaskState
	}{
		{
			name: "no process returns Complete",
			setup: func(v *VMMManager) string {
				return "nonexistent-task"
			},
			wantState: types.TaskStateComplete,
		},
		{
			name: "wait for completed process",
			setup: func(v *VMMManager) string {
				cmd := exec.Command("true")
				if err := cmd.Start(); err != nil {
					return ""
				}
				taskID := "test-task-wait-complete"
				v.processes[taskID] = cmd
				return taskID
			},
			wantState: types.TaskStateComplete,
		},
		{
			name: "wait for failed process",
			setup: func(v *VMMManager) string {
				cmd := exec.Command("false")
				if err := cmd.Start(); err != nil {
					return ""
				}
				taskID := "test-task-wait-failed"
				v.processes[taskID] = cmd
				return taskID
			},
			wantState: types.TaskStateFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskID := tt.setup(vmm)
			if taskID == "" && tt.name != "no process returns Complete" {
				t.Skip("cannot create test process")
			}

			task := &types.Task{ID: taskID}
			status, err := vmm.Wait(ctx, task)
			if err != nil {
				t.Fatalf("Wait() error = %v", err)
			}

			if status.State != tt.wantState {
				t.Errorf("Wait() State = %v; want %v", status.State, tt.wantState)
			}
		})
	}
}

// TestConfigureVM tests the configureVM method
func TestConfigureVM(t *testing.T) {
	vmm := newTestVMMManager(t, false)
	if vmm == nil {
		return
	}

	ctx := context.Background()

	tests := []struct {
		name        string
		config      interface{}
		expectError bool
		errContains string
	}{
		{
			name:        "invalid config type - not map",
			config:      "not a map",
			expectError: true,
			errContains: "invalid config type",
		},
		{
			name:        "invalid config type - nil",
			config:      nil,
			expectError: true,
			errContains: "invalid config type",
		},
		{
			name: "invalid config type - array",
			config: []string{"a", "b"},
			expectError: true,
			errContains: "invalid config type",
		},
		{
			name:        "missing machine-config",
			config:      map[string]interface{}{},
			expectError: true,
			errContains: "missing machine-config",
		},
		{
			name: "missing drives - fails at machine-config PUT (no socket)",
			config: map[string]interface{}{
				"machine-config": map[string]interface{}{
					"vcpu_count":     2,
					"mem_size_mib":   512,
				},
				"boot-source": map[string]interface{}{
					"kernel_image_path": "/kernel",
					"boot_args":         "console=ttyS0",
				},
			},
			expectError: true,
			errContains: "failed to set machine config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &types.Task{ID: "test-vm-config"}
			socketPath := filepath.Join(vmm.socketDir, task.ID+".sock")

			err := vmm.configureVM(ctx, task, socketPath, tt.config)

			if tt.expectError {
				if err == nil {
					t.Errorf("configureVM() expected error, got nil")
				} else if tt.errContains != "" && err.Error() != tt.errContains && !contains(err.Error(), tt.errContains) {
					t.Errorf("configureVM() error = %v; want to contain %s", err, tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("configureVM() unexpected error = %v", err)
				}
			}
		})
	}
}

// TestStartWithJailer tests the startWithJailer method (validation only)
func TestStartWithJailer(t *testing.T) {
	if !hasFirecrackerBinary() || !hasJailerBinary() {
		t.Skip("firecracker or jailer binary not found")
	}

	vmm := newTestVMMManager(t, true)
	if vmm == nil {
		return
	}

	ctx := context.Background()

	tests := []struct {
		name        string
		config      interface{}
		expectError bool
		errContains string
	}{
		{
			name:        "invalid config type",
			config:      "not a map",
			expectError: true,
			errContains: "invalid config type",
		},
		{
			name:        "missing machine-config",
			config:      map[string]interface{}{},
			expectError: true,
			errContains: "missing machine-config",
		},
		{
			name: "missing boot-source",
			config: map[string]interface{}{
				"machine-config": map[string]interface{}{
					"vcpu_count":   2,
					"mem_size_mib": 512,
				},
			},
			expectError: true,
			errContains: "missing boot-source",
		},
		{
			name: "missing drives",
			config: map[string]interface{}{
				"machine-config": map[string]interface{}{
					"vcpu_count":   2,
					"mem_size_mib": 512,
				},
				"boot-source": map[string]interface{}{
					"kernel_image_path": "/kernel",
					"boot_args":         "console=ttyS0",
				},
			},
			expectError: true,
			errContains: "missing drives",
		},
		{
			name: "missing rootfs path",
			config: map[string]interface{}{
				"machine-config": map[string]interface{}{
					"vcpu_count":   2,
					"mem_size_mib": 512,
				},
				"boot-source": map[string]interface{}{
					"kernel_image_path": "/kernel",
					"boot_args":         "console=ttyS0",
				},
				"drives": []interface{}{
					map[string]interface{}{
						"drive_id":       "rootfs",
						"path_on_host":   "/rootfs.ext4",
						"is_root_device": false,
					},
				},
			},
			expectError: true,
			errContains: "rootfs path not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &types.Task{ID: "test-jailer-vm"}

			err := vmm.startWithJailer(ctx, task, tt.config)

			if tt.expectError {
				if err == nil {
					t.Errorf("startWithJailer() expected error, got nil")
				} else if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("startWithJailer() error = %v; want to contain %s", err, tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("startWithJailer() unexpected error = %v", err)
				}
			}
		})
	}
}

// TestCheckVMAPIHealth tests the CheckVMAPIHealth method
func TestCheckVMAPIHealth(t *testing.T) {
	vmm := newTestVMMManager(t, false)
	if vmm == nil {
		return
	}

	tests := []struct {
		name        string
		setup       func(*VMMManager) string
		expected    bool
	}{
		{
			name: "no socket file",
			setup: func(v *VMMManager) string {
				return "no-socket-task"
			},
			expected: false,
		},
		{
			name: "socket file exists but not a real API",
			setup: func(v *VMMManager) string {
				taskID := "fake-socket-task"
				socketPath := filepath.Join(vmm.socketDir, taskID+".sock")
				// Create a regular file, not a real socket
				file, _ := os.Create(socketPath)
				file.Close()
				return taskID
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskID := tt.setup(vmm)
			if taskID == "" {
				t.Skip("cannot setup test")
			}

			result := vmm.CheckVMAPIHealth(taskID)
			if result != tt.expected {
				t.Errorf("CheckVMAPIHealth() = %v; want %v", result, tt.expected)
			}
		})
	}
}

// TestStop tests the Stop method
func TestStop(t *testing.T) {
	vmm := newTestVMMManager(t, false)
	if vmm == nil {
		return
	}

	ctx := context.Background()

	t.Run("stop nonexistent task", func(t *testing.T) {
		task := &types.Task{ID: "nonexistent-task"}
		err := vmm.Stop(ctx, task)
		if err == nil {
			t.Error("Stop() expected error for nonexistent task, got nil")
		}
	})

	t.Run("stop running process gracefully", func(t *testing.T) {
		// Create a sleep process that handles SIGTERM
		cmd := exec.Command("sleep", "10")
		if err := cmd.Start(); err != nil {
			t.Skip("cannot create test process")
		}

		taskID := "test-task-stop"
		vmm.processes[taskID] = cmd

		task := &types.Task{ID: taskID}

		// Stop should send SIGTERM
		done := make(chan error, 1)
		go func() {
			done <- vmm.Stop(ctx, task)
		}()

		// Wait for either completion or timeout
		select {
		case err := <-done:
			if err != nil {
				t.Logf("Stop() completed (process may have been killed): %v", err)
			}
		case <-time.After(12 * time.Second):
			t.Error("Stop() took too long, possible hang")
			cmd.Process.Kill()
		}

		// Verify process was removed from map
		if _, ok := vmm.processes[taskID]; ok {
			t.Error("Stop() should remove process from map")
		}
	})
}

// TestWaitForSocket tests the waitForSocket method
func TestWaitForSocket(t *testing.T) {
	vmm := newTestVMMManager(t, false)
	if vmm == nil {
		return
	}

	t.Run("socket already exists", func(t *testing.T) {
		socketPath := filepath.Join(vmm.socketDir, "test-socket.sock")
		file, err := os.Create(socketPath)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(socketPath)
		file.Close()

		err = vmm.waitForSocket(socketPath, 5*time.Second)
		if err != nil {
			t.Errorf("waitForSocket() unexpected error = %v", err)
		}
	})

	t.Run("socket created after delay", func(t *testing.T) {
		socketPath := filepath.Join(vmm.socketDir, "delayed-socket.sock")

		// Create socket after a delay
		go func() {
			time.Sleep(200 * time.Millisecond)
			file, _ := os.Create(socketPath)
			file.Close()
		}()
		defer os.Remove(socketPath)

		err := vmm.waitForSocket(socketPath, 1*time.Second)
		if err != nil {
			t.Errorf("waitForSocket() unexpected error = %v", err)
		}
	})

	t.Run("socket timeout", func(t *testing.T) {
		socketPath := filepath.Join(vmm.socketDir, "timeout-socket.sock")

		err := vmm.waitForSocket(socketPath, 100*time.Millisecond)
		if err == nil {
			t.Error("waitForSocket() expected timeout error, got nil")
		}
	})
}

// TestProcessMutex tests concurrent access to the processes map
func TestProcessMutex(t *testing.T) {
	vmm := newTestVMMManager(t, false)
	if vmm == nil {
		return
	}

	taskID := "concurrent-test-task"
	numGoroutines := 50
	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cmd := exec.Command("sleep", "1")
			if idx % 2 == 0 {
				if err := cmd.Start(); err == nil {
					vmm.processes[taskID] = cmd
				}
			} else {
				_ = vmm.GetPID(taskID)
				_ = vmm.IsRunning(taskID)
			}
		}(i)
	}

	wg.Wait()
	// Test passes if no race condition detected (go test -race)
}

// TestVMMManagerConfigDefaultsUnit tests default value handling in VMMManagerConfig
func TestVMMManagerConfigDefaultsUnit(t *testing.T) {
	if !hasFirecrackerBinary() {
		t.Skip("firecracker binary not found")
	}

	tempDir := t.TempDir()

	t.Run("default jailer UID/GID", func(t *testing.T) {
		cfg := &VMMManagerConfig{
			FirecrackerPath: "",
			SocketDir:       tempDir,
			UseJailer:       true,
			JailerUID:       0, // Should default to 1000
			JailerGID:       0, // Should default to 1000
		}

		vmm, err := NewVMMManagerWithConfig(cfg)
		if err != nil && !hasJailerBinary() {
			t.Skip("jailer binary not found")
		}
		if err != nil {
			t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
		}

		if vmm.jailerConfig == nil {
			t.Fatal("jailerConfig should not be nil")
		}

		if vmm.jailerConfig.UID != 1000 {
			t.Errorf("jailerConfig UID = %d; want 1000 (default)", vmm.jailerConfig.UID)
		}

		if vmm.jailerConfig.GID != 1000 {
			t.Errorf("jailerConfig GID = %d; want 1000 (default)", vmm.jailerConfig.GID)
		}
	})
}

// TestNewVMMManagerPaths tests with explicit paths
func TestNewVMMManagerPaths(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("with explicit firecracker path", func(t *testing.T) {
		// Use /bin/true as a stand-in for the binary (won't work for real but tests path resolution)
		cfg := &VMMManagerConfig{
			FirecrackerPath: "/bin/true",
			SocketDir:       tempDir,
			UseJailer:       false,
		}

		vmm, err := NewVMMManagerWithConfig(cfg)
		if err != nil {
			t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
		}

		if vmm.firecrackerPath != "/bin/true" {
			t.Errorf("firecrackerPath = %s; want /bin/true", vmm.firecrackerPath)
		}
	})
}


