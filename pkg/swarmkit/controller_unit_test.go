package swarmkit

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestController_Prepare tests the Prepare method with mocks
func TestController_Prepare(t *testing.T) {
	t.Run("prepare successfully", func(t *testing.T) {
		mockImagePrep := &mockImagePrepSuccess{}
		mockNetworkMgr := &mockNetworkManagerFull{}
		mockVMMMgr := &mockVMMManagerSuccess{}

		ctrl := &Controller{
			task: &api.Task{
				ID: "task-prepare-1",
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Image: "nginx:latest",
						},
					},
				},
			},
			config:     &Config{RootfsDir: "/tmp"},
			imagePrep:  mockImagePrep,
			networkMgr: mockNetworkMgr,
			vmmMgr:     mockVMMMgr,
			mu:         sync.Mutex{},
			prepared:   false,
		}

		ctx := context.Background()
		err := ctrl.Prepare(ctx)
		assert.NoError(t, err)
		assert.True(t, ctrl.prepared)
	})

	t.Run("prepare already prepared", func(t *testing.T) {
		ctrl := &Controller{
			task:      &api.Task{ID: "task-prepare-2"},
			config:    &Config{},
			mu:        sync.Mutex{},
			prepared:  true,
			started:   false,
		}

		ctx := context.Background()
		err := ctrl.Prepare(ctx)
		assert.NoError(t, err) // Should return nil when already prepared
	})

	t.Run("prepare image fails", func(t *testing.T) {
		mockImagePrep := &mockImagePrepError{err: errors.New("image prep failed")}
		mockNetworkMgr := &mockNetworkManagerFull{}

		ctrl := &Controller{
			task: &api.Task{
				ID: "task-prepare-3",
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{Image: "nginx"},
					},
				},
			},
			config:     &Config{},
			imagePrep:  mockImagePrep,
			networkMgr: mockNetworkMgr,
			mu:         sync.Mutex{},
			prepared:   false,
		}

		ctx := context.Background()
		err := ctrl.Prepare(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "image preparation failed")
		assert.False(t, ctrl.prepared)
	})

	t.Run("prepare network fails", func(t *testing.T) {
		mockImagePrep := &mockImagePrepSuccess{}
		mockNetworkMgr := &mockNetworkManagerFull{prepareErr: errors.New("network prep failed")}

		ctrl := &Controller{
			task: &api.Task{
				ID: "task-prepare-4",
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{Image: "nginx"},
					},
				},
			},
			config:     &Config{},
			imagePrep:  mockImagePrep,
			networkMgr: mockNetworkMgr,
			mu:         sync.Mutex{},
			prepared:   false,
		}

		ctx := context.Background()
		err := ctrl.Prepare(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "network preparation failed")
		assert.False(t, ctrl.prepared)
	})
}

// TestController_Start tests the Start method with mocks
func TestController_Start(t *testing.T) {
	t.Run("start not prepared fails", func(t *testing.T) {
		ctrl := &Controller{
			task:     &api.Task{ID: "task-start-1"},
			config:   &Config{},
			mu:       sync.Mutex{},
			prepared: false,
			started:  false,
		}

		ctx := context.Background()
		err := ctrl.Start(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not prepared")
	})

	t.Run("start already started returns nil", func(t *testing.T) {
		ctrl := &Controller{
			task:     &api.Task{ID: "task-start-2"},
			config:   &Config{},
			mu:       sync.Mutex{},
			prepared: true,
			started:  true,
		}

		ctx := context.Background()
		err := ctrl.Start(ctx)
		assert.NoError(t, err)
	})

	t.Run("start without internal task fails", func(t *testing.T) {
		ctrl := &Controller{
			task:        &api.Task{ID: "task-start-3"},
			config:      &Config{},
			mu:          sync.Mutex{},
			prepared:    true,
			started:     false,
			internalTask: nil,
		}

		ctx := context.Background()
		err := ctrl.Start(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "internal task not prepared")
	})
}

// TestController_Shutdown tests the Shutdown method
func TestController_Shutdown(t *testing.T) {
	t.Run("shutdown not started returns nil", func(t *testing.T) {
		ctrl := &Controller{
			task:     &api.Task{ID: "task-shutdown-1"},
			config:   &Config{},
			mu:       sync.Mutex{},
			started:  false,
		}

		ctx := context.Background()
		err := ctrl.Shutdown(ctx)
		assert.NoError(t, err)
	})

	t.Run("shutdown with mock vmm success", func(t *testing.T) {
		mockVMMMgr := &mockVMMManagerSuccess{}
		mockNetworkMgr := &mockNetworkManagerFull{}

		ctrl := &Controller{
			task:       &api.Task{ID: "task-shutdown-2"},
			config:     &Config{},
			vmmMgr:     mockVMMMgr,
			networkMgr: mockNetworkMgr,
			mu:         sync.Mutex{},
			started:    true,
		}

		ctx := context.Background()
		err := ctrl.Shutdown(ctx)
		assert.NoError(t, err)
		assert.False(t, ctrl.started)
	})

	t.Run("shutdown vmm fails", func(t *testing.T) {
		mockVMMMgr := &mockVMMManagerError{stopErr: errors.New("vmm stop failed")}
		mockNetworkMgr := &mockNetworkManagerFull{}

		ctrl := &Controller{
			task:       &api.Task{ID: "task-shutdown-3"},
			config:     &Config{},
			vmmMgr:     mockVMMMgr,
			networkMgr: mockNetworkMgr,
			mu:         sync.Mutex{},
			started:    true,
		}

		ctx := context.Background()
		err := ctrl.Shutdown(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to shutdown VM")
	})
}

// TestController_Terminate tests the Terminate method
func TestController_Terminate(t *testing.T) {
	t.Run("terminate not started returns nil", func(t *testing.T) {
		ctrl := &Controller{
			task:     &api.Task{ID: "task-term-1"},
			config:   &Config{},
			mu:       sync.Mutex{},
			started:  false,
		}

		ctx := context.Background()
		err := ctrl.Terminate(ctx)
		assert.NoError(t, err)
	})

	t.Run("terminate with mock vmm success", func(t *testing.T) {
		mockVMMMgr := &mockVMMManagerSuccess{}

		ctrl := &Controller{
			task:    &api.Task{ID: "task-term-2"},
			config:  &Config{},
			vmmMgr:  mockVMMMgr,
			mu:      sync.Mutex{},
			started: true,
		}

		ctx := context.Background()
		err := ctrl.Terminate(ctx)
		assert.NoError(t, err)
		assert.False(t, ctrl.started)
	})

	t.Run("terminate vmm fails", func(t *testing.T) {
		mockVMMMgr := &mockVMMManagerError{stopErr: errors.New("force stop failed")}

		ctrl := &Controller{
			task:    &api.Task{ID: "task-term-3"},
			config:  &Config{},
			vmmMgr:  mockVMMMgr,
			mu:      sync.Mutex{},
			started: true,
		}

		ctx := context.Background()
		err := ctrl.Terminate(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to force terminate VM")
	})
}

// TestController_Wait tests the Wait method
func TestController_Wait(t *testing.T) {
	t.Run("wait with success", func(t *testing.T) {
		mockVMMMgr := &mockVMMManagerSuccess{}

		ctrl := &Controller{
			task:    &api.Task{ID: "task-wait-1"},
			config:  &Config{},
			vmmMgr:  mockVMMMgr,
			mu:      sync.Mutex{},
		}

		ctx := context.Background()
		err := ctrl.Wait(ctx)
		assert.NoError(t, err)
	})

	t.Run("wait with error", func(t *testing.T) {
		mockVMMMgr := &mockVMMManagerError{
			waitStatus: &types.TaskStatus{
				State: types.TaskStateFailed,
				Err:   errors.New("vm exited with error"),
			},
		}

		ctrl := &Controller{
			task:    &api.Task{ID: "task-wait-2"},
			config:  &Config{},
			vmmMgr:  mockVMMMgr,
			mu:      sync.Mutex{},
		}

		ctx := context.Background()
		err := ctrl.Wait(ctx)
		assert.Error(t, err)
	})
}

// TestExecutor_Controller_New tests creating new controllers
func TestExecutor_Controller_New(t *testing.T) {
	t.Run("controller creates new", func(t *testing.T) {
		e := &Executor{
			config:      &Config{KernelPath: "/tmp/vmlinux", BridgeIP: "192.168.1.1"},
			controllers: make(map[string]*Controller),
			executorMu:  sync.RWMutex{},
			imagePrep:   &mockImagePrepSuccess{},
			networkMgr:  &mockNetworkManagerFull{},
			vmmMgr:      &mockVMMManagerSuccess{},
		}

		task := &api.Task{
			ID: "task-new",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "nginx"},
				},
			},
		}

		ctrl, err := e.Controller(task)
		require.NoError(t, err)
		assert.NotNil(t, ctrl)

		// Verify it was stored
		e.executorMu.RLock()
		storedCtrl, exists := e.controllers["task-new"]
		e.executorMu.RUnlock()
		assert.True(t, exists)
		assert.Equal(t, ctrl, storedCtrl)
	})

	t.Run("controller OnRemove callback", func(t *testing.T) {
		e := &Executor{
			config:      &Config{KernelPath: "/tmp/vmlinux", BridgeIP: "192.168.1.1"},
			controllers: make(map[string]*Controller),
			executorMu:  sync.RWMutex{},
			imagePrep:   &mockImagePrepSuccess{},
			networkMgr:  &mockNetworkManagerFull{},
			vmmMgr:      &mockVMMManagerSuccess{},
		}

		task := &api.Task{ID: "task-callback"}

		ctrl, err := e.Controller(task)
		require.NoError(t, err)
		_ = ctrl // We verify via the stored controller instead

		// Get the concrete Controller from executor's map to verify OnRemove
		e.executorMu.RLock()
		storedCtrl := e.controllers["task-callback"]
		e.executorMu.RUnlock()
		assert.NotNil(t, storedCtrl.OnRemove)

		// Call OnRemove and verify controller is removed from executor
		storedCtrl.OnRemove()

		e.executorMu.RLock()
		_, exists := e.controllers["task-callback"]
		e.executorMu.RUnlock()
		assert.False(t, exists)
	})
}

// TestController_ConvertTask tests the convertTask method
func TestController_ConvertTask(t *testing.T) {
	t.Run("convert with container spec", func(t *testing.T) {
		ctrl := &Controller{
			task: &api.Task{
				ID:        "task-convert-1",
				ServiceID: "service-1",
				NodeID:    "node-1",
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Image:   "nginx:latest",
							Command: []string{"nginx"},
							Args:    []string{"-g", "daemon off;"},
							Env:     []string{"PATH=/usr/bin"},
						},
					},
				},
				Networks: []*api.NetworkAttachment{
					{
						Network: &api.Network{
							ID: "net-1",
							Spec: api.NetworkSpec{
								Annotations: api.Annotations{Name: "test-net"},
								DriverConfig: &api.Driver{
									Name: "bridge",
								},
							},
						},
						Addresses: []string{"192.168.1.10/24"},
					},
				},
			},
			config: &Config{},
			mu:     sync.Mutex{},
		}

		task := ctrl.convertTask()
		assert.Equal(t, "task-convert-1", task.ID)
		assert.Equal(t, "service-1", task.ServiceID)
		assert.Equal(t, "node-1", task.NodeID)

		container, ok := task.Spec.Runtime.(*types.Container)
		assert.True(t, ok)
		assert.Equal(t, "nginx:latest", container.Image)
		assert.Equal(t, []string{"nginx"}, container.Command)
		assert.Equal(t, []string{"-g", "daemon off;"}, container.Args)
		assert.Len(t, task.Networks, 1)
	})

	t.Run("convert without container spec", func(t *testing.T) {
		ctrl := &Controller{
			task: &api.Task{
				ID:        "task-convert-2",
				ServiceID: "service-2",
				NodeID:    "node-2",
				Spec:      api.TaskSpec{}, // No container spec
			},
			config: &Config{},
			mu:     sync.Mutex{},
		}

		task := ctrl.convertTask()
		assert.Equal(t, "task-convert-2", task.ID)
		container, ok := task.Spec.Runtime.(*types.Container)
		assert.True(t, ok)
		assert.Empty(t, container.Image)
	})

	t.Run("convert with nil networks", func(t *testing.T) {
		ctrl := &Controller{
			task: &api.Task{
				ID:        "task-convert-3",
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{Image: "nginx"},
					},
				},
				Networks: []*api.NetworkAttachment{nil},
			},
			config: &Config{},
			mu:     sync.Mutex{},
		}

		task := ctrl.convertTask()
		assert.Empty(t, task.Networks) // Should skip nil network
	})
}

// Mock implementations for controller tests

type mockImagePrepSuccess struct{}

func (m *mockImagePrepSuccess) Prepare(ctx context.Context, task *types.Task) error {
	task.Annotations = map[string]string{"rootfs": "/tmp/test.ext4"}
	return nil
}

func (m *mockImagePrepSuccess) Cleanup(ctx context.Context, maxAgeDays int) (int, int64, error) {
	return 0, 0, nil
}

type mockImagePrepError struct {
	err error
}

func (m *mockImagePrepError) Prepare(ctx context.Context, task *types.Task) error {
	return m.err
}

func (m *mockImagePrepError) Cleanup(ctx context.Context, maxAgeDays int) (int, int64, error) {
	return 0, 0, nil
}

type mockVMMManagerSuccess struct{}

func (m *mockVMMManagerSuccess) Start(ctx context.Context, task *types.Task, config interface{}) error {
	return nil
}

func (m *mockVMMManagerSuccess) Stop(ctx context.Context, task *types.Task) error {
	return nil
}

func (m *mockVMMManagerSuccess) ForceStop(ctx context.Context, task *types.Task) error {
	return nil
}

func (m *mockVMMManagerSuccess) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	return &types.TaskStatus{State: types.TaskStateComplete}, nil
}

func (m *mockVMMManagerSuccess) Remove(ctx context.Context, task *types.Task) error {
	return nil
}

func (m *mockVMMManagerSuccess) GetPID(taskID string) int {
	return 12345
}

func (m *mockVMMManagerSuccess) CheckVMAPIHealth(taskID string) bool {
	return true
}

func (m *mockVMMManagerSuccess) IsRunning(taskID string) bool {
	return true
}

func (m *mockVMMManagerSuccess) Describe(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	return &types.TaskStatus{State: types.TaskStateRunning}, nil
}

func (m *mockVMMManagerSuccess) GetRunningProcesses() map[string]*exec.Cmd {
	return nil
}

func (m *mockVMMManagerSuccess) RemoveProcess(taskID string) {}

type mockVMMManagerError struct {
	startErr   error
	stopErr    error
	waitStatus *types.TaskStatus
	waitErr    error
}

func (m *mockVMMManagerError) Start(ctx context.Context, task *types.Task, config interface{}) error {
	return m.startErr
}

func (m *mockVMMManagerError) Stop(ctx context.Context, task *types.Task) error {
	return m.stopErr
}

func (m *mockVMMManagerError) ForceStop(ctx context.Context, task *types.Task) error {
	return m.stopErr
}

func (m *mockVMMManagerError) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	if m.waitStatus != nil {
		return m.waitStatus, m.waitErr
	}
	return &types.TaskStatus{State: types.TaskStateFailed}, m.waitErr
}

func (m *mockVMMManagerError) Remove(ctx context.Context, task *types.Task) error {
	return nil
}

func (m *mockVMMManagerError) GetPID(taskID string) int {
	return 0
}

func (m *mockVMMManagerError) CheckVMAPIHealth(taskID string) bool {
	return false
}

func (m *mockVMMManagerError) IsRunning(taskID string) bool {
	return false
}

func (m *mockVMMManagerError) Describe(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	return &types.TaskStatus{State: types.TaskStateFailed}, nil
}

func (m *mockVMMManagerError) GetRunningProcesses() map[string]*exec.Cmd {
	return nil
}

func (m *mockVMMManagerError) RemoveProcess(taskID string) {}