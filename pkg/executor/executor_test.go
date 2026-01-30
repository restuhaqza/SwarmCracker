package executor

import (
	"context"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/restuhaqza/swarmcracker/test/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFirecrackerExecutor(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid config with defaults",
			config: &Config{
				KernelPath: "/usr/share/firecracker/vmlinux",
			},
			wantErr: false,
		},
		{
			name: "nil config",
			config: nil,
			wantErr: true,
		},
		{
			name: "config with all fields",
			config: &Config{
				KernelPath:     "/usr/share/firecracker/vmlinux",
				InitrdPath:     "/initrd.img",
				RootfsDir:      "/var/lib/firecracker/rootfs",
				SocketDir:      "/var/run/firecracker",
				DefaultVCPUs:   2,
				DefaultMemoryMB: 2048,
				EnableJailer:   true,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmmManager := mocks.NewMockVMMManager()
			translator := mocks.NewMockTaskTranslator()
			imagePrep := mocks.NewMockImagePreparer()
			networkMgr := mocks.NewMockNetworkManager()

			exec, err := NewFirecrackerExecutor(
				tt.config,
				vmmManager,
				translator,
				imagePrep,
				networkMgr,
			)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, exec)
			} else {
				require.NoError(t, err)
				require.NotNil(t, exec)

				// For configs with all fields, verify those values are used
				// For configs with defaults, verify defaults are set
				if tt.name == "valid config with defaults" {
					assert.Equal(t, 1, exec.config.DefaultVCPUs)
					assert.Equal(t, 512, exec.config.DefaultMemoryMB)
					assert.Equal(t, "/var/run/firecracker", exec.config.SocketDir)
					assert.Equal(t, "/var/lib/firecracker/rootfs", exec.config.RootfsDir)
				} else if tt.name == "config with all fields" {
					assert.Equal(t, 2, exec.config.DefaultVCPUs)
					assert.Equal(t, 2048, exec.config.DefaultMemoryMB)
					assert.Equal(t, "/var/run/firecracker", exec.config.SocketDir)
					assert.Equal(t, "/var/lib/firecracker/rootfs", exec.config.RootfsDir)
				}
			}
		})
	}
}

func TestFirecrackerExecutor_Prepare(t *testing.T) {
	tests := []struct {
		name      string
		task      *types.Task
		setupMock func(*mocks.MockImagePreparer, *mocks.MockNetworkManager)
		wantErr   bool
	}{
		{
			name: "successful prepare",
			task: mocks.NewTestTask("task-1", "nginx:latest"),
			setupMock: func(imgPrep *mocks.MockImagePreparer, netMgr *mocks.MockNetworkManager) {
				// No special setup needed - defaults to success
			},
			wantErr: false,
		},
		{
			name: "image preparation fails",
			task: mocks.NewTestTask("task-2", "redis:alpine"),
			setupMock: func(imgPrep *mocks.MockImagePreparer, netMgr *mocks.MockNetworkManager) {
				imgPrep.ShouldFail = true
			},
			wantErr: true,
		},
		{
			name: "network preparation fails",
			task: mocks.NewTestTask("task-3", "postgres:14"),
			setupMock: func(imgPrep *mocks.MockImagePreparer, netMgr *mocks.MockNetworkManager) {
				netMgr.ShouldFail = true
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmmManager := mocks.NewMockVMMManager()
			translator := mocks.NewMockTaskTranslator()
			imagePrep := mocks.NewMockImagePreparer()
			networkMgr := mocks.NewMockNetworkManager()

			if tt.setupMock != nil {
				tt.setupMock(imagePrep, networkMgr)
			}

			config := &Config{KernelPath: "/kernel"}
			exec, err := NewFirecrackerExecutor(config, vmmManager, translator, imagePrep, networkMgr)
			require.NoError(t, err)

			ctx := context.Background()
			err = exec.Prepare(ctx, tt.task)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify image was prepared
				assert.True(t, imagePrep.IsTaskPrepared(tt.task.ID))
				// Verify network was prepared
				assert.True(t, networkMgr.IsTaskPrepared(tt.task.ID))
				// Verify rootfs annotation was set
				assert.NotEmpty(t, tt.task.Annotations["rootfs"])
			}
		})
	}
}

func TestFirecrackerExecutor_Start(t *testing.T) {
	tests := []struct {
		name      string
		task      *types.Task
		setupTask func(*types.Task)
		setupMock func(*mocks.MockTaskTranslator, *mocks.MockVMMManager)
		wantErr   bool
	}{
		{
			name: "successful start",
			task: mocks.NewTestTask("task-1", "nginx:latest"),
			setupTask: func(task *types.Task) {
				task.Annotations["rootfs"] = "/mock/rootfs/nginx.ext4"
			},
			setupMock: func(trans *mocks.MockTaskTranslator, vmm *mocks.MockVMMManager) {
				// Defaults to success
			},
			wantErr: false,
		},
		{
			name: "translation fails",
			task: mocks.NewTestTask("task-2", "redis:alpine"),
			setupTask: func(task *types.Task) {
				task.Annotations["rootfs"] = "/mock/rootfs/redis.ext4"
			},
			setupMock: func(trans *mocks.MockTaskTranslator, vmm *mocks.MockVMMManager) {
				trans.ShouldFail = true
			},
			wantErr: true,
		},
		{
			name: "VMM start fails",
			task: mocks.NewTestTask("task-3", "postgres:14"),
			setupTask: func(task *types.Task) {
				task.Annotations["rootfs"] = "/mock/rootfs/postgres.ext4"
			},
			setupMock: func(trans *mocks.MockTaskTranslator, vmm *mocks.MockVMMManager) {
				vmm.ShouldFail = true
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmmManager := mocks.NewMockVMMManager()
			translator := mocks.NewMockTaskTranslator()
			imagePrep := mocks.NewMockImagePreparer()
			networkMgr := mocks.NewMockNetworkManager()

			if tt.setupTask != nil {
				tt.setupTask(tt.task)
			}
			if tt.setupMock != nil {
				tt.setupMock(translator, vmmManager)
			}

			config := &Config{KernelPath: "/kernel"}
			exec, err := NewFirecrackerExecutor(config, vmmManager, translator, imagePrep, networkMgr)
			require.NoError(t, err)

			ctx := context.Background()
			err = exec.Start(ctx, tt.task)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify task was translated
				assert.True(t, translator.IsTaskTranslated(tt.task.ID))
				// Verify VM was started
				assert.True(t, vmmManager.IsTaskStarted(tt.task.ID))
			}
		})
	}
}

func TestFirecrackerExecutor_Wait(t *testing.T) {
	tests := []struct {
		name       string
		task       *types.Task
		setupWait  func(*mocks.MockVMMManager)
		wantState  types.TaskState
		wantErrMsg string
	}{
		{
			name: "running task",
			task: mocks.NewTestTask("task-1", "nginx:latest"),
			setupWait: func(vmm *mocks.MockVMMManager) {
				vmm.SetWaitStatus(&types.TaskStatus{
					State:   types.TaskState_RUNNING,
					Message: "VM is running",
				})
			},
			wantState: types.TaskState_RUNNING,
		},
		{
			name: "completed task",
			task: mocks.NewTestTask("task-2", "redis:alpine"),
			setupWait: func(vmm *mocks.MockVMMManager) {
				vmm.SetWaitStatus(&types.TaskStatus{
					State:   types.TaskState_COMPLETE,
					Message: "VM exited cleanly",
				})
			},
			wantState: types.TaskState_COMPLETE,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmmManager := mocks.NewMockVMMManager()
			if tt.setupWait != nil {
				tt.setupWait(vmmManager)
			}

			translator := mocks.NewMockTaskTranslator()
			imagePrep := mocks.NewMockImagePreparer()
			networkMgr := mocks.NewMockNetworkManager()

			config := &Config{KernelPath: "/kernel"}
			exec, err := NewFirecrackerExecutor(config, vmmManager, translator, imagePrep, networkMgr)
			require.NoError(t, err)

			ctx := context.Background()
			status, err := exec.Wait(ctx, tt.task)

			require.NoError(t, err)
			assert.Equal(t, tt.wantState, status.State)
			assert.True(t, vmmManager.WaitCalled)
		})
	}
}

func TestFirecrackerExecutor_Stop(t *testing.T) {
	vmmManager := mocks.NewMockVMMManager()
	translator := mocks.NewMockTaskTranslator()
	imagePrep := mocks.NewMockImagePreparer()
	networkMgr := mocks.NewMockNetworkManager()

	config := &Config{KernelPath: "/kernel"}
	exec, err := NewFirecrackerExecutor(config, vmmManager, translator, imagePrep, networkMgr)
	require.NoError(t, err)

	task := mocks.NewTestTask("task-1", "nginx:latest")
	ctx := context.Background()

	err = exec.Stop(ctx, task)

	require.NoError(t, err)
	assert.True(t, vmmManager.IsTaskStopped(task.ID))
	assert.True(t, vmmManager.StopCalled)
}

func TestFirecrackerExecutor_Describe(t *testing.T) {
	vmmManager := mocks.NewMockVMMManager()
	translator := mocks.NewMockTaskTranslator()
	imagePrep := mocks.NewMockImagePreparer()
	networkMgr := mocks.NewMockNetworkManager()

	config := &Config{KernelPath: "/kernel"}
	exec, err := NewFirecrackerExecutor(config, vmmManager, translator, imagePrep, networkMgr)
	require.NoError(t, err)

	task := mocks.NewTestTask("task-1", "nginx:latest")
	ctx := context.Background()

	status, err := exec.Describe(ctx, task)

	require.NoError(t, err)
	assert.Equal(t, types.TaskState_RUNNING, status.State)
	assert.True(t, vmmManager.DescribeCalled)
}

func TestFirecrackerExecutor_Remove(t *testing.T) {
	vmmManager := mocks.NewMockVMMManager()
	translator := mocks.NewMockTaskTranslator()
	imagePrep := mocks.NewMockImagePreparer()
	networkMgr := mocks.NewMockNetworkManager()

	config := &Config{KernelPath: "/kernel"}
	exec, err := NewFirecrackerExecutor(config, vmmManager, translator, imagePrep, networkMgr)
	require.NoError(t, err)

	task := mocks.NewTestTask("task-1", "nginx:latest")
	ctx := context.Background()

	err = exec.Remove(ctx, task)

	require.NoError(t, err)
	assert.True(t, vmmManager.RemoveCalled)
	assert.True(t, networkMgr.IsTaskCleaned(task.ID))
}

func TestFirecrackerExecutor_Events(t *testing.T) {
	vmmManager := mocks.NewMockVMMManager()
	translator := mocks.NewMockTaskTranslator()
	imagePrep := mocks.NewMockImagePreparer()
	networkMgr := mocks.NewMockNetworkManager()

	config := &Config{KernelPath: "/kernel"}
	exec, err := NewFirecrackerExecutor(config, vmmManager, translator, imagePrep, networkMgr)
	require.NoError(t, err)

	ctx := context.Background()
	events, err := exec.Events(ctx)

	require.NoError(t, err)
	assert.NotNil(t, events)

	// Test event channel
	select {
	case <-events:
		t.Fatal("Channel should be empty initially")
	default:
		// Expected - channel is empty
	}

	// Send an event
	go func() {
		exec.events <- Event{
			Task:    mocks.NewTestTask("task-1", "nginx"),
			Message: "Test event",
		}
	}()

	select {
	case event := <-events:
		assert.Equal(t, "Test event", event.Message)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Should have received an event")
	}
}

func TestFirecrackerExecutor_Close(t *testing.T) {
	vmmManager := mocks.NewMockVMMManager()
	translator := mocks.NewMockTaskTranslator()
	imagePrep := mocks.NewMockImagePreparer()
	networkMgr := mocks.NewMockNetworkManager()

	config := &Config{KernelPath: "/kernel"}
	exec, err := NewFirecrackerExecutor(config, vmmManager, translator, imagePrep, networkMgr)
	require.NoError(t, err)

	err = exec.Close()

	assert.NoError(t, err)
}

// Integration-style test for full lifecycle
func TestFirecrackerExecutor_FullLifecycle(t *testing.T) {
	vmmManager := mocks.NewMockVMMManager()
	translator := mocks.NewMockTaskTranslator()
	imagePrep := mocks.NewMockImagePreparer()
	networkMgr := mocks.NewMockNetworkManager()

	config := &Config{KernelPath: "/kernel"}
	exec, err := NewFirecrackerExecutor(config, vmmManager, translator, imagePrep, networkMgr)
	require.NoError(t, err)

	ctx := context.Background()
	task := mocks.NewTestTask("task-1", "nginx:latest")

	// Prepare
	err = exec.Prepare(ctx, task)
	require.NoError(t, err)
	assert.True(t, imagePrep.IsTaskPrepared(task.ID))
	assert.NotEmpty(t, task.Annotations["rootfs"])

	// Start
	err = exec.Start(ctx, task)
	require.NoError(t, err)
	assert.True(t, translator.IsTaskTranslated(task.ID))
	assert.True(t, vmmManager.IsTaskStarted(task.ID))

	// Describe
	status, err := exec.Describe(ctx, task)
	require.NoError(t, err)
	assert.Equal(t, types.TaskState_RUNNING, status.State)

	// Wait
	waitStatus, err := exec.Wait(ctx, task)
	require.NoError(t, err)
	assert.NotNil(t, waitStatus)

	// Stop
	err = exec.Stop(ctx, task)
	require.NoError(t, err)
	assert.True(t, vmmManager.IsTaskStopped(task.ID))

	// Remove
	err = exec.Remove(ctx, task)
	require.NoError(t, err)
	assert.True(t, networkMgr.IsTaskCleaned(task.ID))
	assert.True(t, vmmManager.RemoveCalled)
}

// Benchmark executor operations
func BenchmarkFirecrackerExecutor_Start(b *testing.B) {
	vmmManager := mocks.NewMockVMMManager()
	translator := mocks.NewMockTaskTranslator()
	imagePrep := mocks.NewMockImagePreparer()
	networkMgr := mocks.NewMockNetworkManager()

	config := &Config{KernelPath: "/kernel"}
	exec, _ := NewFirecrackerExecutor(config, vmmManager, translator, imagePrep, networkMgr)

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		task := mocks.NewTestTask("bench-task", "nginx:latest")
		task.Annotations["rootfs"] = "/mock/rootfs/nginx.ext4"
		_ = exec.Start(ctx, task)
	}
}
