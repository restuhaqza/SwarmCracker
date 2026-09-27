// Package swarmkit provides mock implementations for testing.

package swarmkit

import (
	"context"
	"os/exec"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// MockVMMManager is a mock implementation for testing.
type MockVMMManager struct {
	StartFunc               func(ctx context.Context, task *types.Task, config interface{}) error
	StopFunc                func(ctx context.Context, task *types.Task) error
	ForceStopFunc           func(ctx context.Context, task *types.Task) error
	WaitFunc                func(ctx context.Context, task *types.Task) (*types.TaskStatus, error)
	RemoveFunc              func(ctx context.Context, task *types.Task) error
	GetPIDFunc              func(taskID string) int
	CheckVMAPIHealthFunc    func(ctx context.Context, taskID string) bool
	IsRunningFunc           func(taskID string) bool
	DescribeFunc            func(ctx context.Context, task *types.Task) (*types.TaskStatus, error)
	GetRunningProcessesFunc func() map[string]*exec.Cmd
	RemoveProcessFunc       func(taskID string)
	processes               map[string]*exec.Cmd
}

func (m *MockVMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error {
	if m.StartFunc != nil {
		return m.StartFunc(ctx, task, config)
	}
	return nil
}

func (m *MockVMMManager) Stop(ctx context.Context, task *types.Task) error {
	if m.StopFunc != nil {
		return m.StopFunc(ctx, task)
	}
	return nil
}

func (m *MockVMMManager) ForceStop(ctx context.Context, task *types.Task) error {
	if m.ForceStopFunc != nil {
		return m.ForceStopFunc(ctx, task)
	}
	return nil
}

func (m *MockVMMManager) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	if m.WaitFunc != nil {
		return m.WaitFunc(ctx, task)
	}
	return &types.TaskStatus{State: types.TaskStateRunning}, nil
}

func (m *MockVMMManager) Remove(ctx context.Context, task *types.Task) error {
	if m.RemoveFunc != nil {
		return m.RemoveFunc(ctx, task)
	}
	return nil
}

func (m *MockVMMManager) GetPID(taskID string) int {
	if m.GetPIDFunc != nil {
		return m.GetPIDFunc(taskID)
	}
	return 0
}

func (m *MockVMMManager) CheckVMAPIHealth(ctx context.Context, taskID string) bool {
	if m.CheckVMAPIHealthFunc != nil {
		return m.CheckVMAPIHealthFunc(ctx, taskID)
	}
	return true
}

func (m *MockVMMManager) IsRunning(taskID string) bool {
	if m.IsRunningFunc != nil {
		return m.IsRunningFunc(taskID)
	}
	return false
}

func (m *MockVMMManager) Describe(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	if m.DescribeFunc != nil {
		return m.DescribeFunc(ctx, task)
	}
	return &types.TaskStatus{State: types.TaskStateRunning}, nil
}

func (m *MockVMMManager) GetRunningProcesses() map[string]*exec.Cmd {
	if m.GetRunningProcessesFunc != nil {
		return m.GetRunningProcessesFunc()
	}
	if m.processes != nil {
		return m.processes
	}
	return make(map[string]*exec.Cmd)
}

func (m *MockVMMManager) RemoveProcess(taskID string) {
	if m.RemoveProcessFunc != nil {
		m.RemoveProcessFunc(taskID)
		return
	}
	if m.processes != nil {
		delete(m.processes, taskID)
	}
}

// MockImagePreparer is a mock implementation for testing.
type MockImagePreparer struct {
	PrepareFunc func(ctx context.Context, task *types.Task) error
	CleanupFunc func(ctx context.Context, keepDays int) (filesRemoved int, bytesFreed int64, err error)
}

func (m *MockImagePreparer) Prepare(ctx context.Context, task *types.Task) error {
	if m.PrepareFunc != nil {
		return m.PrepareFunc(ctx, task)
	}
	return nil
}

func (m *MockImagePreparer) Cleanup(ctx context.Context, keepDays int) (filesRemoved int, bytesFreed int64, err error) {
	if m.CleanupFunc != nil {
		return m.CleanupFunc(ctx, keepDays)
	}
	return 0, 0, nil
}

// MockNetworkManager is a mock implementation for testing.
type MockNetworkManager struct {
	PrepareNetworkFunc   func(ctx context.Context, task *types.Task) error
	CleanupNetworkFunc   func(ctx context.Context, task *types.Task) error
	GetTapIPFunc         func(taskID string) (string, error)
	InitFunc             func(ctx context.Context) error
	SetNodeDiscoveryFunc func(discovery types.NodeDiscovery)
	UpdateVXLANPeersFunc func(peers []string) error
}

func (m *MockNetworkManager) PrepareNetwork(ctx context.Context, task *types.Task) error {
	if m.PrepareNetworkFunc != nil {
		return m.PrepareNetworkFunc(ctx, task)
	}
	return nil
}

func (m *MockNetworkManager) CleanupNetwork(ctx context.Context, task *types.Task) error {
	if m.CleanupNetworkFunc != nil {
		return m.CleanupNetworkFunc(ctx, task)
	}
	return nil
}

func (m *MockNetworkManager) GetTapIP(taskID string) (string, error) {
	if m.GetTapIPFunc != nil {
		return m.GetTapIPFunc(taskID)
	}
	return "192.168.127.2", nil // Default mock IP
}

func (m *MockNetworkManager) Init(ctx context.Context) error {
	if m.InitFunc != nil {
		return m.InitFunc(ctx)
	}
	return nil
}

func (m *MockNetworkManager) SetNodeDiscovery(discovery types.NodeDiscovery) {
	if m.SetNodeDiscoveryFunc != nil {
		m.SetNodeDiscoveryFunc(discovery)
	}
}

func (m *MockNetworkManager) UpdateVXLANPeers(peers []string) error {
	if m.UpdateVXLANPeersFunc != nil {
		return m.UpdateVXLANPeersFunc(peers)
	}
	return nil
}

// MockVolumeManager is a mock implementation for testing.
type MockVolumeManager struct {
	PrepareVolumesFunc func(ctx context.Context, task *types.Task) error
	CleanupVolumesFunc func(ctx context.Context, task *types.Task) error
}

func (m *MockVolumeManager) PrepareVolumes(ctx context.Context, task *types.Task) error {
	if m.PrepareVolumesFunc != nil {
		return m.PrepareVolumesFunc(ctx, task)
	}
	return nil
}

func (m *MockVolumeManager) CleanupVolumes(ctx context.Context, task *types.Task) error {
	if m.CleanupVolumesFunc != nil {
		return m.CleanupVolumesFunc(ctx, task)
	}
	return nil
}

// MockSecretManager is a mock implementation for testing.
type MockSecretManager struct {
	InjectSecretsFunc func(ctx context.Context, taskID string, secrets []types.SecretRef, rootfsPath string) error
	InjectConfigsFunc func(ctx context.Context, taskID string, configs []types.ConfigRef, rootfsPath string) error
}

func (m *MockSecretManager) InjectSecrets(ctx context.Context, taskID string, secrets []types.SecretRef, rootfsPath string) error {
	if m.InjectSecretsFunc != nil {
		return m.InjectSecretsFunc(ctx, taskID, secrets, rootfsPath)
	}
	return nil
}

func (m *MockSecretManager) InjectConfigs(ctx context.Context, taskID string, configs []types.ConfigRef, rootfsPath string) error {
	if m.InjectConfigsFunc != nil {
		return m.InjectConfigsFunc(ctx, taskID, configs, rootfsPath)
	}
	return nil
}

// MockTaskTranslator is a mock implementation for testing.
type MockTaskTranslator struct {
	TranslateFunc func(task *types.Task) (interface{}, error)
}

func (m *MockTaskTranslator) Translate(task *types.Task) (interface{}, error) {
	if m.TranslateFunc != nil {
		return m.TranslateFunc(task)
	}
	return map[string]interface{}{}, nil
}
