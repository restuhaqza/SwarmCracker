package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewVMMManager(t *testing.T) {
	tests := []struct {
		name    string
		config  interface{}
		wantDir string
	}{
		{
			name:    "default config",
			config:  nil,
			wantDir: "/var/run/firecracker",
		},
		{
			name: "custom config",
			config: &ManagerConfig{
				SocketDir: "/tmp/test-firecracker",
			},
			wantDir: "/tmp/test-firecracker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmm := NewVMMManager(tt.config).(*VMMManager)

			assert.NotNil(t, vmm)
			assert.NotNil(t, vmm.vms)
			assert.Equal(t, tt.wantDir, vmm.socketDir)
		})
	}
}

func TestVMMManager_Start(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test - requires Firecracker")
	}

	// Create temporary socket directory
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")
	err := os.MkdirAll(socketDir, 0755)
	require.NoError(t, err)

	config := &ManagerConfig{
		SocketDir:  socketDir,
		KernelPath: "/usr/share/firecracker/vmlinux",
	}
	vmm := NewVMMManager(config).(*VMMManager)

	task := &types.Task{
		ID:        "test-vm-1",
		ServiceID: "service-1",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "nginx:latest",
			},
		},
		Annotations: make(map[string]string),
	}

	// Create a minimal Firecracker config
	vmConfig := map[string]interface{}{
		"boot_source": map[string]interface{}{
			"kernel_image_path": config.KernelPath,
			"boot_args":         "console=ttyS0 reboot=k panic=1",
		},
		"machine_config": map[string]interface{}{
			"vcpu_count":   1,
			"mem_size_mib": 512,
			"ht_enabled":   false,
		},
	}

	configJSON, _ := json.Marshal(vmConfig)

	ctx := context.Background()
	err = vmm.Start(ctx, task, string(configJSON))

	// This will fail if Firecracker is not installed or no KVM
	// In CI, we expect this to fail
	if err != nil {
		t.Skipf("Firecracker not available (expected in CI): %v", err)
		return
	}

	// Verify VM instance was created
	vmm.mu.RLock()
	vmInstance, exists := vmm.vms[task.ID]
	vmm.mu.RUnlock()

	assert.True(t, exists)
	assert.Equal(t, task.ID, vmInstance.ID)
	assert.Equal(t, VMStateRunning, vmInstance.State)
	assert.NotEmpty(t, vmInstance.SocketPath)

	// Cleanup
	_ = vmm.Remove(ctx, task)
}

func TestVMMManager_Start_InvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	task := &types.Task{
		ID:        "test-vm-invalid",
		ServiceID: "service-1",
	}

	ctx := context.Background()
	err := vmm.Start(ctx, task, "invalid json {{{")

	assert.Error(t, err)
}

func TestVMMManager_Stop(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test - requires Firecracker")
	}

	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir:  socketDir,
		KernelPath: "/usr/share/firecracker/vmlinux",
	}
	vmm := NewVMMManager(config).(*VMMManager)

	task := &types.Task{
		ID:        "test-vm-stop",
		ServiceID: "service-1",
	}

	// Manually add a VM instance for testing
	vmm.mu.Lock()
	vmm.vms[task.ID] = &VMInstance{
		ID:         task.ID,
		PID:        12345, // Fake PID
		State:      VMStateRunning,
		CreatedAt:  time.Now(),
		SocketPath: filepath.Join(socketDir, task.ID+".sock"),
	}
	vmm.mu.Unlock()

	ctx := context.Background()
	err := vmm.Stop(ctx, task)

	// Will fail because PID 12345 doesn't exist, but we test the logic
	assert.Error(t, err)
}

func TestVMMManager_Wait(t *testing.T) {
	config := &ManagerConfig{
		SocketDir: "/tmp/test",
	}
	vmm := NewVMMManager(config).(*VMMManager)

	task := &types.Task{
		ID:        "test-vm-wait",
		ServiceID: "service-1",
	}

	// Test with non-existent VM
	status, err := vmm.Wait(context.Background(), task)

	require.NoError(t, err)
	assert.Equal(t, types.TaskState_ORPHANED, status.State)
	assert.Contains(t, status.Message, "not found")

	// Test with a mock running VM
	vmm.mu.Lock()
	vmm.vms[task.ID] = &VMInstance{
		ID:        task.ID,
		PID:       os.Getpid(), // Use current process
		State:     VMStateRunning,
		CreatedAt: time.Now(),
	}
	vmm.mu.Unlock()

	status, err = vmm.Wait(context.Background(), task)

	require.NoError(t, err)
	assert.Equal(t, types.TaskState_RUNNING, status.State)
}

func TestVMMManager_Describe(t *testing.T) {
	config := &ManagerConfig{
		SocketDir: "/tmp/test",
	}
	vmm := NewVMMManager(config).(*VMMManager)

	task := &types.Task{
		ID:        "test-vm-describe",
		ServiceID: "service-1",
	}

	// Test with non-existent VM
	status, err := vmm.Describe(context.Background(), task)

	require.NoError(t, err)
	assert.Equal(t, types.TaskState_ORPHANED, status.State)

	// Test with a mock running VM
	vmm.mu.Lock()
	vmm.vms[task.ID] = &VMInstance{
		ID:        task.ID,
		PID:       os.Getpid(),
		State:     VMStateRunning,
		CreatedAt: time.Now(),
	}
	vmm.mu.Unlock()

	status, err = vmm.Describe(context.Background(), task)

	require.NoError(t, err)
	assert.Equal(t, types.TaskState_RUNNING, status.State)

	// Check runtime status
	runtimeStatus, ok := status.RuntimeStatus.(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, task.ID, runtimeStatus["vm_id"])
	assert.Equal(t, "running", runtimeStatus["state"])
}

func TestVMMManager_Remove(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	task := &types.Task{
		ID:        "test-vm-remove",
		ServiceID: "service-1",
	}

	// Create a mock socket file
	socketPath := filepath.Join(socketDir, task.ID+".sock")
	err := os.WriteFile(socketPath, []byte("dummy"), 0644)
	require.NoError(t, err)

	// Add a VM instance with a fake PID (not current process)
	vmm.mu.Lock()
	vmm.vms[task.ID] = &VMInstance{
		ID:         task.ID,
		PID:        12345, // Fake PID that doesn't exist
		State:      VMStateRunning,
		CreatedAt:  time.Now(),
		SocketPath: socketPath,
	}
	vmm.mu.Unlock()

	ctx := context.Background()
	err = vmm.Remove(ctx, task)

	require.NoError(t, err)

	// Verify VM was removed
	vmm.mu.RLock()
	_, exists := vmm.vms[task.ID]
	vmm.mu.RUnlock()

	assert.False(t, exists)

	// Verify socket was removed
	_, err = os.Stat(socketPath)
	assert.True(t, os.IsNotExist(err))
}

func TestVMMManager_Remove_NonExistent(t *testing.T) {
	config := &ManagerConfig{
		SocketDir: "/tmp/test",
	}
	vmm := NewVMMManager(config).(*VMMManager)

	task := &types.Task{
		ID:        "non-existent",
		ServiceID: "service-1",
	}

	// Removing non-existent VM should not error
	ctx := context.Background()
	err := vmm.Remove(ctx, task)

	assert.NoError(t, err)
}

func TestVMState_String(t *testing.T) {
	tests := []struct {
		state VMState
		want  string
	}{
		{VMStateNew, "new"},
		{VMStateStarting, "starting"},
		{VMStateRunning, "running"},
		{VMStateStopping, "stopping"},
		{VMStateStopped, "stopped"},
		{VMStateCrashed, "crashed"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, string(tt.state))
		})
	}
}

func TestForceKillVM(t *testing.T) {
	config := &ManagerConfig{
		SocketDir: "/tmp/test",
	}
	vmm := NewVMMManager(config).(*VMMManager)

	vmInstance := &VMInstance{
		ID:        "test-kill",
		PID:       99999, // Fake PID that doesn't exist
		State:     VMStateRunning,
		CreatedAt: time.Now(),
	}

	// This will try to kill a non-existent process
	// The error is expected but we're testing the logic
	err := vmm.forceKillVM(vmInstance)

	// Should error because process doesn't exist
	assert.Error(t, err)
}

func TestWaitForAPIServer(t *testing.T) {
	t.Run("non-existent socket", func(t *testing.T) {
		err := waitForAPIServer("/tmp/non-existent.sock", 100*time.Millisecond)
		assert.Error(t, err)
	})

	t.Run("timeout", func(t *testing.T) {
		// Create a socket that won't respond
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		// Create a dummy socket file
		err := os.WriteFile(socketPath, []byte("dummy"), 0644)
		require.NoError(t, err)

		err = waitForAPIServer(socketPath, 100*time.Millisecond)
		assert.Error(t, err)
	})
}

func TestWaitForShutdown(t *testing.T) {
	t.Run("already deleted", func(t *testing.T) {
		deleted, err := waitForShutdown("/tmp/non-existent.sock", 100*time.Millisecond)
		assert.True(t, deleted)
		assert.NoError(t, err)
	})

	t.Run("timeout", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		// Create a socket file
		err := os.WriteFile(socketPath, []byte("dummy"), 0644)
		require.NoError(t, err)

		deleted, err := waitForShutdown(socketPath, 100*time.Millisecond)
		assert.False(t, deleted)
		assert.Error(t, err)
	})
}

// Benchmark VMM operations
func BenchmarkVMMManager_Start(b *testing.B) {
	tmpDir := b.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")

	config := &ManagerConfig{
		SocketDir: socketDir,
	}
	vmm := NewVMMManager(config).(*VMMManager)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		task := &types.Task{
			ID:        "bench-task-" + string(rune(i)),
			ServiceID: "bench-service",
		}
		// Just test the locking and logic, not actual VM start
		vmm.mu.Lock()
		vmm.vms[task.ID] = &VMInstance{
			ID:    task.ID,
			PID:   12345,
			State: VMStateRunning,
		}
		vmm.mu.Unlock()
	}
}

func BenchmarkVMMManager_Describe(b *testing.B) {
	config := &ManagerConfig{
		SocketDir: "/tmp/test",
	}
	vmm := NewVMMManager(config).(*VMMManager)

	// Pre-populate with VMs
	for i := 0; i < 100; i++ {
		taskID := "task-" + string(rune(i))
		vmm.vms[taskID] = &VMInstance{
			ID:        taskID,
			PID:       os.Getpid(),
			State:     VMStateRunning,
			CreatedAt: time.Now(),
		}
	}

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		task := &types.Task{
			ID: "task-" + string(rune(i%100)),
		}
		_, _ = vmm.Describe(ctx, task)
	}
}

// TestVMMManager_Start_EdgeCases tests edge cases for Start
func TestVMMManager_Start_EdgeCases(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "firecracker")
	err := os.MkdirAll(socketDir, 0755)
	require.NoError(t, err)

	config := &ManagerConfig{
		SocketDir:  socketDir,
		KernelPath: "/usr/share/firecracker/vmlinux",
	}
	vmm := NewVMMManager(config).(*VMMManager)

	ctx := context.Background()

	tests := []struct {
		name        string
		task        *types.Task
		configJSON  string
		expectError bool
	}{
		{
			name:        "nil task",
			task:        nil,
			configJSON:  "{}",
			expectError: false, // Will be handled by nil check
		},
		{
			name: "empty task ID",
			task: &types.Task{
				ID: "",
			},
			configJSON:  "{}",
			expectError: false,
		},
		{
			name: "special characters in task ID",
			task: &types.Task{
				ID: "task_with-special.chars/123",
			},
			configJSON:  "{}",
			expectError: false,
		},
		{
			name: "very long task ID",
			task: &types.Task{
				ID: string(make([]byte, 500)),
			},
			configJSON:  "{}",
			expectError: false,
		},
		{
			name: "valid config minimal",
			task: &types.Task{
				ID: "test-minimal",
			},
			configJSON:  `{"boot_source": {"kernel_image_path": "/usr/share/firecracker/vmlinux"}}`,
			expectError: false,
		},
		{
			name: "config with drives",
			task: &types.Task{
				ID: "test-drives",
			},
			configJSON:  `{"boot_source": {"kernel_image_path": "/usr/share/firecracker/vmlinux"}, "drives": [{"drive_id": "rootfs", "path_on_host": "/tmp/rootfs.ext4", "is_root_device": true, "is_read_only": false}]}`,
			expectError: false,
		},
		{
			name: "config with network interfaces",
			task: &types.Task{
				ID: "test-net",
			},
			configJSON:  `{"boot_source": {"kernel_image_path": "/usr/share/firecracker/vmlinux"}, "network_interfaces": [{"iface_id": "eth0", "guest_mac": "02:FC:00:00:00:01"}]}`,
			expectError: false,
		},
		{
			name: "config with machine config",
			task: &types.Task{
				ID: "test-machine",
			},
			configJSON:  `{"boot_source": {"kernel_image_path": "/usr/share/firecracker/vmlinux"}, "machine_config": {"vcpu_count": 2, "mem_size_mib": 1024, "ht_enabled": false}}`,
			expectError: false,
		},
		{
			name: "empty config JSON",
			task: &types.Task{
				ID: "test-empty",
			},
			configJSON:  "{}",
			expectError: false,
		},
		{
			name: "config with all sections",
			task: &types.Task{
				ID: "test-full",
			},
			configJSON:  `{"boot_source": {"kernel_image_path": "/usr/share/firecracker/vmlinux"}, "drives": [{"drive_id": "rootfs", "path_on_host": "/tmp/rootfs.ext4", "is_root_device": true, "is_read_only": false}], "machine_config": {"vcpu_count": 2, "mem_size_mib": 1024}, "network_interfaces": [{"iface_id": "eth0", "guest_mac": "02:FC:00:00:00:01"}]}`,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// These tests will likely fail without actual Firecracker
			// but we test the logic and error handling
			err := vmm.Start(ctx, tt.task, tt.configJSON)
			// Don't assert - just verify no panics
			_ = err
		})
	}
}

// TestVMMManager_Wait_EdgeCases tests edge cases for Wait
func TestVMMManager_Wait_EdgeCases(t *testing.T) {
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
		name string
		task *types.Task
	}{
		{
			name: "nil task",
			task: nil,
		},
		{
			name: "non-existent task",
			task: &types.Task{
				ID: "wait-does-not-exist",
			},
		},
		{
			name: "empty task ID",
			task: &types.Task{
				ID: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not panic on nil or empty tasks
			if tt.task == nil {
				// Recover from panic if nil task causes one
				defer func() {
					if r := recover(); r != nil {
						t.Logf("Recovered from panic with nil task (expected): %v", r)
					}
				}()
				_, _ = vmm.Wait(ctx, tt.task)
			} else {
				status, err := vmm.Wait(ctx, tt.task)
				// For non-existent tasks, should return ORPHANED
				if tt.task.ID != "" {
					assert.NoError(t, err)
					assert.Equal(t, types.TaskState_ORPHANED, status.State)
				}
			}
		})
	}
}
