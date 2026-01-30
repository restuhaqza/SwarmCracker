// Package mocks provides mock implementations for testing.
package mocks

import (
	"context"
	"fmt"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// MockVMMManager is a mock VMM manager.
type MockVMMManager struct {
	StartCalled    bool
	StopCalled     bool
	WaitCalled     bool
	DescribeCalled bool
	RemoveCalled   bool
	StartedTasks   map[string]bool
	StoppedTasks   map[string]bool
	ShouldFail     bool
	WaitStatus     *types.TaskStatus
}

// NewMockVMMManager creates a new mock VMM manager.
func NewMockVMMManager() *MockVMMManager {
	return &MockVMMManager{
		StartedTasks: make(map[string]bool),
		StoppedTasks: make(map[string]bool),
		ShouldFail:   false,
		WaitStatus: &types.TaskStatus{
			State:   types.TaskState_RUNNING,
			Message: "Mock VM running",
		},
	}
}

// Start starts a mock VM.
func (m *MockVMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error {
	m.StartCalled = true
	if m.ShouldFail {
		return fmt.Errorf("mock: start failed")
	}
	m.StartedTasks[task.ID] = true
	return nil
}

// Stop stops a mock VM.
func (m *MockVMMManager) Stop(ctx context.Context, task *types.Task) error {
	m.StopCalled = true
	if m.ShouldFail {
		return fmt.Errorf("mock: stop failed")
	}
	m.StoppedTasks[task.ID] = true
	return nil
}

// Wait waits for a mock VM.
func (m *MockVMMManager) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	m.WaitCalled = true
	if m.ShouldFail {
		return nil, fmt.Errorf("mock: wait failed")
	}
	return m.WaitStatus, nil
}

// Describe describes a mock VM.
func (m *MockVMMManager) Describe(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	m.DescribeCalled = true
	if m.ShouldFail {
		return nil, fmt.Errorf("mock: describe failed")
	}
	return &types.TaskStatus{
		State:   types.TaskState_RUNNING,
		Message: "Mock VM described",
	}, nil
}

// Remove removes a mock VM.
func (m *MockVMMManager) Remove(ctx context.Context, task *types.Task) error {
	m.RemoveCalled = true
	if m.ShouldFail {
		return fmt.Errorf("mock: remove failed")
	}
	delete(m.StartedTasks, task.ID)
	return nil
}

// IsTaskStarted checks if a task was started.
func (m *MockVMMManager) IsTaskStarted(taskID string) bool {
	return m.StartedTasks[taskID]
}

// IsTaskStopped checks if a task was stopped.
func (m *MockVMMManager) IsTaskStopped(taskID string) bool {
	return m.StoppedTasks[taskID]
}

// SetWaitStatus sets the status to return from Wait.
func (m *MockVMMManager) SetWaitStatus(status *types.TaskStatus) {
	m.WaitStatus = status
}

// MockTaskTranslator is a mock task translator.
type MockTaskTranslator struct {
	TranslateCalled bool
	TranslatedTasks map[string]bool
	ShouldFail      bool
	ResultConfig    interface{}
}

// NewMockTaskTranslator creates a new mock task translator.
func NewMockTaskTranslator() *MockTaskTranslator {
	return &MockTaskTranslator{
		TranslatedTasks: make(map[string]bool),
		ShouldFail:      false,
		ResultConfig:    `{"boot_source":{"kernel_image_path":"/kernel"}}`,
	}
}

// Translate translates a mock task.
func (m *MockTaskTranslator) Translate(task *types.Task) (interface{}, error) {
	m.TranslateCalled = true
	if m.ShouldFail {
		return nil, fmt.Errorf("mock: translate failed")
	}
	m.TranslatedTasks[task.ID] = true
	return m.ResultConfig, nil
}

// IsTaskTranslated checks if a task was translated.
func (m *MockTaskTranslator) IsTaskTranslated(taskID string) bool {
	return m.TranslatedTasks[taskID]
}

// SetResultConfig sets the config to return from Translate.
func (m *MockTaskTranslator) SetResultConfig(config interface{}) {
	m.ResultConfig = config
}

// MockImagePreparer is a mock image preparer.
type MockImagePreparer struct {
	PrepareCalled   bool
	PreparedTasks   map[string]bool
	ShouldFail      bool
	RootfsMap       map[string]string
}

// NewMockImagePreparer creates a new mock image preparer.
func NewMockImagePreparer() *MockImagePreparer {
	return &MockImagePreparer{
		PreparedTasks: make(map[string]bool),
		ShouldFail:    false,
		RootfsMap:     make(map[string]string),
	}
}

// Prepare prepares a mock image.
func (m *MockImagePreparer) Prepare(ctx context.Context, task *types.Task) error {
	m.PrepareCalled = true
	if m.ShouldFail {
		return fmt.Errorf("mock: prepare failed")
	}

	container, ok := task.Spec.Runtime.(*types.Container)
	if !ok {
		return fmt.Errorf("not a container")
	}

	rootfsPath := fmt.Sprintf("/mock/rootfs/%s.ext4", container.Image)
	m.PreparedTasks[task.ID] = true
	m.RootfsMap[task.ID] = rootfsPath
	task.Annotations["rootfs"] = rootfsPath
	return nil
}

// IsTaskPrepared checks if a task was prepared.
func (m *MockImagePreparer) IsTaskPrepared(taskID string) bool {
	return m.PreparedTasks[taskID]
}

// GetRootfsPath gets the rootfs path for a task.
func (m *MockImagePreparer) GetRootfsPath(taskID string) string {
	return m.RootfsMap[taskID]
}

// MockNetworkManager is a mock network manager.
type MockNetworkManager struct {
	PrepareCalled   bool
	CleanupCalled   bool
	PreparedTasks   map[string]bool
	CleanedTasks    map[string]bool
	ShouldFail      bool
}

// NewMockNetworkManager creates a new mock network manager.
func NewMockNetworkManager() *MockNetworkManager {
	return &MockNetworkManager{
		PreparedTasks: make(map[string]bool),
		CleanedTasks:  make(map[string]bool),
		ShouldFail:    false,
	}
}

// PrepareNetwork prepares mock networking.
func (m *MockNetworkManager) PrepareNetwork(ctx context.Context, task *types.Task) error {
	m.PrepareCalled = true
	if m.ShouldFail {
		return fmt.Errorf("mock: prepare network failed")
	}
	m.PreparedTasks[task.ID] = true
	return nil
}

// CleanupNetwork cleans up mock networking.
func (m *MockNetworkManager) CleanupNetwork(ctx context.Context, task *types.Task) error {
	m.CleanupCalled = true
	if m.ShouldFail {
		return fmt.Errorf("mock: cleanup network failed")
	}
	m.CleanedTasks[task.ID] = true
	return nil
}

// IsTaskPrepared checks if network was prepared for a task.
func (m *MockNetworkManager) IsTaskPrepared(taskID string) bool {
	return m.PreparedTasks[taskID]
}

// IsTaskCleaned checks if network was cleaned for a task.
func (m *MockNetworkManager) IsTaskCleaned(taskID string) bool {
	return m.CleanedTasks[taskID]
}

// Helper function to create a test task
func NewTestTask(id, image string) *types.Task {
	return &types.Task{
		ID:        id,
		ServiceID: "service-" + id,
		NodeID:    "node-1",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image:   image,
				Command: []string{"/bin/sh"},
				Args:    []string{"-c", "echo hello"},
			},
			Resources: types.ResourceRequirements{
				Limits: &types.Resources{
					NanoCPUs:    1e9,
					MemoryBytes: 512 * 1024 * 1024,
				},
			},
		},
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "swarm-br0",
							},
						},
					},
				},
				Addresses: []string{"10.0.0.2/24"},
			},
		},
		Annotations: make(map[string]string),
	}
}
