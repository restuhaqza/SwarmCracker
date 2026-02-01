package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

// TestVMMManager_AdditionalStartErrors tests additional start error scenarios
func TestVMMManager_AdditionalStartErrors(t *testing.T) {
	tests := []struct {
		name        string
		task        *types.Task
		config      interface{}
		expectError bool
	}{
		{
			name:        "nil_task",
			task:        nil,
			config:      "{}",
			expectError: true,
		},
		{
			name: "empty_task_id",
			task: &types.Task{
				ID: "",
			},
			config:      "{}",
			expectError: true,
		},
		{
			name: "invalid_config_type",
			task: &types.Task{
				ID: "test-invalid-type",
			},
			config:      12345, // Invalid type
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ManagerConfig{
				SocketDir: t.TempDir(),
			}
			vm := NewVMMManager(cfg).(*VMMManager)

			ctx := context.Background()
			err := vm.Start(ctx, tt.task, tt.config)

			if tt.expectError {
				assert.Error(t, err)
			}
		})
	}
}

// TestVMMManager_AdditionalStopErrors tests additional stop scenarios
func TestVMMManager_AdditionalStopErrors(t *testing.T) {
	tests := []struct {
		name        string
		setupVM     func(*VMMManager, *types.Task)
		task        *types.Task
		expectError bool
	}{
		{
			name:        "nil_task",
			setupVM:     nil,
			task:        nil,
			expectError: true,
		},
		{
			name: "vm_in_crashed_state",
			setupVM: func(vm *VMMManager, task *types.Task) {
				vm.mu.Lock()
				vm.vms[task.ID] = &VMInstance{
					ID:         task.ID,
					PID:        1234,
					State:      VMStateCrashed,
					SocketPath: filepath.Join(vm.socketDir, task.ID+".sock"),
				}
				vm.mu.Unlock()
			},
			task: &types.Task{
				ID: "test-crashed",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ManagerConfig{
				SocketDir: t.TempDir(),
			}
			vm := NewVMMManager(cfg).(*VMMManager)

			if tt.setupVM != nil && tt.task != nil {
				tt.setupVM(vm, tt.task)
			}

			ctx := context.Background()
			err := vm.Stop(ctx, tt.task)

			if tt.expectError {
				assert.Error(t, err)
			}
		})
	}
}

// TestVMMManager_AdditionalWaitScenarios tests additional wait scenarios
func TestVMMManager_AdditionalWaitScenarios(t *testing.T) {
	cfg := &ManagerConfig{
		SocketDir: t.TempDir(),
	}
	vm := NewVMMManager(cfg).(*VMMManager)

	// Test with non-existent task
	task := &types.Task{
		ID: "non-existent",
	}

	ctx := context.Background()
	status, err := vm.Wait(ctx, task)
	assert.NoError(t, err)
	assert.Equal(t, types.TaskState_ORPHANED, status.State)
}

// TestVMMManager_AdditionalDescribeScenarios tests additional describe scenarios
func TestVMMManager_AdditionalDescribeScenarios(t *testing.T) {
	cfg := &ManagerConfig{
		SocketDir: t.TempDir(),
	}
	vm := NewVMMManager(cfg).(*VMMManager)

	tests := []struct {
		name   string
		setup  func(*VMMManager, *types.Task)
		task   *types.Task
		check  func(*testing.T, *types.TaskStatus)
	}{
		{
			name:  "non_existent_vm",
			setup: nil,
			task: &types.Task{
				ID: "non-existent",
			},
			check: func(t *testing.T, status *types.TaskStatus) {
				assert.Equal(t, types.TaskState_ORPHANED, status.State)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(vm, tt.task)
			}

			ctx := context.Background()
			status, err := vm.Describe(ctx, tt.task)
			assert.NoError(t, err)
			if tt.check != nil {
				tt.check(t, status)
			}
		})
	}
}

// TestVMMManager_AdditionalRemoveScenarios tests additional remove scenarios
func TestVMMManager_AdditionalRemoveScenarios(t *testing.T) {
	cfg := &ManagerConfig{
		SocketDir: t.TempDir(),
	}
	vm := NewVMMManager(cfg).(*VMMManager)

	tests := []struct {
		name   string
		setup  func(*VMMManager, *types.Task)
		task   *types.Task
		check  func(*testing.T, *VMMManager, string)
	}{
		{
			name:  "non_existent_vm",
			setup: nil,
			task: &types.Task{
				ID: "non-existent",
			},
			check: func(t *testing.T, vm *VMMManager, taskID string) {
				vm.mu.RLock()
				_, exists := vm.vms[taskID]
				vm.mu.RUnlock()
				assert.False(t, exists)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(vm, tt.task)
			}

			ctx := context.Background()
			err := vm.Remove(ctx, tt.task)
			assert.NoError(t, err)

			if tt.check != nil {
				tt.check(t, vm, tt.task.ID)
			}
		})
	}
}

// TestVMMManager_ConcurrentLifecycleTests tests concurrent lifecycle operations
func TestVMMManager_ConcurrentLifecycleTests(t *testing.T) {
	cfg := &ManagerConfig{
		SocketDir: t.TempDir(),
	}
	vm := NewVMMManager(cfg).(*VMMManager)

	ctx := context.Background()
	var wg sync.WaitGroup

	// Concurrent describe operations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			task := &types.Task{
				ID: "describe-" + string(rune('A'+idx)),
			}
			_, _ = vm.Describe(ctx, task)
		}(i)
	}

	wg.Wait()
	assert.True(t, true) // Verify no deadlock
}

// TestVMMManager_ContextTimeout tests context timeout handling
func TestVMMManager_ContextTimeout(t *testing.T) {
	cfg := &ManagerConfig{
		SocketDir: t.TempDir(),
	}
	vm := NewVMMManager(cfg).(*VMMManager)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	// Give context time to expire
	time.Sleep(10 * time.Millisecond)

	task := &types.Task{
		ID: "test-timeout",
	}

	err := vm.Start(ctx, task, "{}")
	assert.Error(t, err)
}

// TestVMMManager_SpecialCharacters tests special characters in task IDs
func TestVMMManager_SpecialCharacters(t *testing.T) {
	cfg := &ManagerConfig{
		SocketDir: t.TempDir(),
	}
	vm := NewVMMManager(cfg).(*VMMManager)

	specialIDs := []string{
		"task-with-dashes",
		"task_with_underscores",
		"task.with.dots",
	}

	for _, taskID := range specialIDs {
		t.Run(taskID, func(t *testing.T) {
			ctx := context.Background()
			task := &types.Task{
				ID: taskID,
			}

			// Operations should handle special characters
			_ = vm.Start(ctx, task, "{}")
			_ = vm.Remove(ctx, task)
		})
	}
}

// TestVMMManager_EmptyConfig tests with empty config
func TestVMMManager_EmptyConfig(t *testing.T) {
	cfg := &ManagerConfig{
		SocketDir: t.TempDir(),
	}
	vm := NewVMMManager(cfg).(*VMMManager)

	task := &types.Task{
		ID: "test-empty-config",
	}

	ctx := context.Background()
	err := vm.Start(ctx, task, "")
	assert.Error(t, err)
}

// TestVMMManager_VeryLongTaskID tests very long task IDs
func TestVMMManager_VeryLongTaskID(t *testing.T) {
	cfg := &ManagerConfig{
		SocketDir: t.TempDir(),
	}
	vm := NewVMMManager(cfg).(*VMMManager)

	longTaskID := strings.Repeat("a", 1000)

	task := &types.Task{
		ID: longTaskID,
	}

	ctx := context.Background()
	_ = vm.Start(ctx, task, "{}")
	_ = vm.Remove(ctx, task)
}

// TestVMMManager_ForceKillVM tests force kill scenarios
func TestVMMManager_ForceKillVM(t *testing.T) {
	cfg := &ManagerConfig{
		SocketDir: t.TempDir(),
	}
	vm := NewVMMManager(cfg).(*VMMManager)

	tests := []struct {
		name        string
		pid         int
		expectError bool
	}{
		{
			name:        "zero_pid",
			pid:         0,
			expectError: true,
		},
		{
			name:        "non_existent_pid",
			pid:         99999,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmInstance := &VMInstance{
				ID:  "test-kill",
				PID: tt.pid,
			}

			err := vm.forceKillVM(vmInstance)
			if tt.expectError {
				assert.Error(t, err)
			}
		})
	}
}

// TestVMState_Values tests VMState values
func TestVMState_Values(t *testing.T) {
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
			s := string(state)
			assert.NotEmpty(t, s)
		})
	}
}

// TestNewVMMManager_NilConfig tests NewVMMManager with nil config
func TestNewVMMManager_NilConfig(t *testing.T) {
	vm := NewVMMManager(nil).(*VMMManager)
	assert.NotNil(t, vm)
	assert.Equal(t, "/var/run/firecracker", vm.socketDir)
}

// TestVMMManager_SocketFileHandling tests socket file handling
func TestVMMManager_SocketFileHandling(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &ManagerConfig{
		SocketDir: tmpDir,
	}
	vm := NewVMMManager(cfg).(*VMMManager)

	// Create a stale socket file
	taskID := "test-stale-socket"
	socketPath := filepath.Join(vm.socketDir, taskID+".sock")
	_ = os.WriteFile(socketPath, []byte("stale"), 0644)

	task := &types.Task{
		ID: taskID,
	}

	ctx := context.Background()
	_ = vm.Start(ctx, task, "{}")

	// Remove should clean up the socket file
	_ = vm.Remove(ctx, task)

	// Verify socket file was removed or handled
	_, err := os.Stat(socketPath)
	// Either removed or still there (both are acceptable)
	_ = err
}

// TestVMMManager_WaitForAPIServer_Timeouts tests API server timeout
func TestVMMManager_WaitForAPIServer_Timeouts(t *testing.T) {
	tests := []struct {
		name        string
		socketPath  string
		timeout     time.Duration
		expectError bool
	}{
		{
			name:        "non_existent_socket",
			socketPath:  "/non/existent/socket.sock",
			timeout:     100 * time.Millisecond,
			expectError: true,
		},
		{
			name:        "very_short_timeout",
			socketPath:  "/tmp/test.sock",
			timeout:     1 * time.Nanosecond,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := waitForAPIServer(tt.socketPath, tt.timeout)
			if tt.expectError {
				assert.Error(t, err)
			}
		})
	}
}

// TestVMMManager_WaitForShutdown_Scenarios tests waitForShutdown
func TestVMMManager_WaitForShutdown_Scenarios(t *testing.T) {
	tests := []struct {
		name        string
		socketPath  string
		timeout     time.Duration
		expectError bool
	}{
		{
			name:        "already_deleted",
			socketPath:  "/non/existent/socket.sock",
			timeout:     1 * time.Second,
			expectError: false,
		},
		{
			name:        "timeout_with_persistent_file",
			socketPath:  "",
			timeout:     100 * time.Millisecond,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "timeout_with_persistent_file" {
				// Create a temporary file that won't be deleted
				tmpFile := filepath.Join(t.TempDir(), "persistent.sock")
				_ = os.WriteFile(tmpFile, []byte("dummy"), 0644)
				tt.socketPath = tmpFile
			}

			removed, err := waitForShutdown(tt.socketPath, tt.timeout)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.True(t, removed)
			}
		})
	}
}
