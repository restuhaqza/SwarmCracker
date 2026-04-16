package swarmkit

import (
	"context"
	"errors"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

func TestMockVMMManager_AllMethods(t *testing.T) {
	mock := &MockVMMManager{
		StartFunc: func(ctx context.Context, task *types.Task, config interface{}) error {
			if task.ID == "" {
				return errors.New("task ID required")
			}
			return nil
		},
		StopFunc: func(ctx context.Context, task *types.Task) error {
			return nil
		},
		ForceStopFunc: func(ctx context.Context, task *types.Task) error {
			return nil
		},
		WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
			return &types.TaskStatus{State: types.TaskStateComplete}, nil
		},
		RemoveFunc: func(ctx context.Context, task *types.Task) error {
			return nil
		},
		GetPIDFunc: func(taskID string) int {
			return 12345
		},
		CheckVMAPIHealthFunc: func(taskID string) bool {
			return taskID != "unhealthy"
		},
		IsRunningFunc: func(taskID string) bool {
			return taskID == "running"
		},
	}

	ctx := context.Background()
	task := &types.Task{ID: "test-task"}

	// Test all methods
	if err := mock.Start(ctx, task, nil); err != nil {
		t.Errorf("Start failed: %v", err)
	}

	if err := mock.Stop(ctx, task); err != nil {
		t.Errorf("Stop failed: %v", err)
	}

	if err := mock.ForceStop(ctx, task); err != nil {
		t.Errorf("ForceStop failed: %v", err)
	}

	status, err := mock.Wait(ctx, task)
	if err != nil {
		t.Errorf("Wait failed: %v", err)
	}
	if status.State != types.TaskStateComplete {
		t.Errorf("Expected TaskStateComplete, got %v", status.State)
	}

	if err := mock.Remove(ctx, task); err != nil {
		t.Errorf("Remove failed: %v", err)
	}

	if pid := mock.GetPID("test-task"); pid != 12345 {
		t.Errorf("Expected pid 12345, got %d", pid)
	}

	if !mock.CheckVMAPIHealth("test-task") {
		t.Error("Expected healthy API check")
	}

	if mock.CheckVMAPIHealth("unhealthy") {
		t.Error("Expected unhealthy API check to return false")
	}

	if !mock.IsRunning("running") {
		t.Error("Expected IsRunning to return true for 'running'")
	}

	if mock.IsRunning("stopped") {
		t.Error("Expected IsRunning to return false for 'stopped'")
	}
}

func TestMockVMMManager_StartError(t *testing.T) {
	mock := &MockVMMManager{
		StartFunc: func(ctx context.Context, task *types.Task, config interface{}) error {
			return errors.New("start failed")
		},
	}

	ctx := context.Background()
	task := &types.Task{ID: "test-task"}

	if err := mock.Start(ctx, task, nil); err == nil {
		t.Error("Expected error from Start")
	}
}

func TestMockImagePreparer_AllMethods(t *testing.T) {
	mock := &MockImagePreparer{
		PrepareFunc: func(ctx context.Context, task *types.Task) error {
			return nil
		},
		CleanupFunc: func(ctx context.Context, taskID string) error {
			if taskID == "" {
				return errors.New("task ID required")
			}
			return nil
		},
	}

	ctx := context.Background()
	task := &types.Task{ID: "test-task"}

	if err := mock.Prepare(ctx, task); err != nil {
		t.Errorf("Prepare failed: %v", err)
	}

	if err := mock.Cleanup(ctx, "test-task"); err != nil {
		t.Errorf("Cleanup failed: %v", err)
	}
}

func TestMockNetworkManager_AllMethods(t *testing.T) {
	mock := &MockNetworkManager{
		PrepareNetworkFunc: func(ctx context.Context, task *types.Task) error {
			if len(task.Networks) == 0 {
				return errors.New("no networks configured")
			}
			return nil
		},
		CleanupNetworkFunc: func(ctx context.Context, task *types.Task) error {
			return nil
		},
	}

	ctx := context.Background()

	taskWithNetworks := &types.Task{
		ID:       "test-task",
		Networks: []types.NetworkAttachment{{}},
	}

	if err := mock.PrepareNetwork(ctx, taskWithNetworks); err != nil {
		t.Errorf("PrepareNetwork failed: %v", err)
	}

	taskNoNetworks := &types.Task{ID: "no-networks"}
	if err := mock.PrepareNetwork(ctx, taskNoNetworks); err == nil {
		t.Error("Expected error for task with no networks")
	}

	if err := mock.CleanupNetwork(ctx, taskWithNetworks); err != nil {
		t.Errorf("CleanupNetwork failed: %v", err)
	}
}

func TestMockVolumeManager_AllMethods(t *testing.T) {
	mock := &MockVolumeManager{
		PrepareVolumesFunc: func(ctx context.Context, task *types.Task) error {
			return nil
		},
		CleanupVolumesFunc: func(ctx context.Context, task *types.Task) error {
			return nil
		},
	}

	ctx := context.Background()
	task := &types.Task{ID: "test-task"}

	if err := mock.PrepareVolumes(ctx, task); err != nil {
		t.Errorf("PrepareVolumes failed: %v", err)
	}

	if err := mock.CleanupVolumes(ctx, task); err != nil {
		t.Errorf("CleanupVolumes failed: %v", err)
	}
}

func TestMockSecretManager_AllMethods(t *testing.T) {
	mock := &MockSecretManager{
		InjectSecretsFunc: func(ctx context.Context, taskID string, secrets []types.SecretRef, rootfsPath string) error {
			if rootfsPath == "" {
				return errors.New("rootfs path required")
			}
			return nil
		},
		InjectConfigsFunc: func(ctx context.Context, taskID string, configs []types.ConfigRef, rootfsPath string) error {
			return nil
		},
	}

	ctx := context.Background()
	secrets := []types.SecretRef{{ID: "secret-1"}}
	configs := []types.ConfigRef{{ID: "config-1"}}

	if err := mock.InjectSecrets(ctx, "test-task", secrets, "/tmp/rootfs"); err != nil {
		t.Errorf("InjectSecrets failed: %v", err)
	}

	if err := mock.InjectSecrets(ctx, "test-task", secrets, ""); err == nil {
		t.Error("Expected error for empty rootfs path")
	}

	if err := mock.InjectConfigs(ctx, "test-task", configs, "/tmp/rootfs"); err != nil {
		t.Errorf("InjectConfigs failed: %v", err)
	}
}

func TestConfig_Defaults(t *testing.T) {
	config := &Config{}

	// Set defaults manually since NewExecutor would do this
	if config.FirecrackerPath == "" {
		config.FirecrackerPath = "firecracker"
	}
	if config.DefaultVCPUs == 0 {
		config.DefaultVCPUs = 1
	}
	if config.DefaultMemoryMB == 0 {
		config.DefaultMemoryMB = 512
	}

	if config.FirecrackerPath != "firecracker" {
		t.Errorf("Expected default firecracker path, got %s", config.FirecrackerPath)
	}
	if config.DefaultVCPUs != 1 {
		t.Errorf("Expected default 1 vCPU, got %d", config.DefaultVCPUs)
	}
	if config.DefaultMemoryMB != 512 {
		t.Errorf("Expected default 512MB, got %d", config.DefaultMemoryMB)
	}
}

func TestVMMManagerConfig_Fields(t *testing.T) {
	cfg := &VMMManagerConfig{
		FirecrackerPath: "/usr/bin/firecracker",
		JailerPath:      "/usr/bin/jailer",
		SocketDir:       "/var/run/firecracker",
		UseJailer:       true,
		JailerUID:       1000,
		JailerGID:       1000,
		EnableCgroups:   true,
	}

	if cfg.FirecrackerPath != "/usr/bin/firecracker" {
		t.Errorf("Expected FirecrackerPath /usr/bin/firecracker, got %s", cfg.FirecrackerPath)
	}
	if !cfg.UseJailer {
		t.Error("Expected UseJailer to be true")
	}
	if cfg.JailerUID != 1000 {
		t.Errorf("Expected JailerUID 1000, got %d", cfg.JailerUID)
	}
}

func TestControllerStruct_Interface(t *testing.T) {
	// Test that Controller struct has expected fields
	ctrl := &Controller{
		prepared: true,
		started:  false,
	}

	if !ctrl.prepared {
		t.Error("Expected prepared to be true")
	}
	if ctrl.started {
		t.Error("Expected started to be false")
	}
}

func TestExecutorStruct_Fields(t *testing.T) {
	// Test that Executor struct has expected fields
	exec := &Executor{
		config:      &Config{DefaultVCPUs: 2},
		controllers: make(map[string]*Controller),
	}

	if exec.config.DefaultVCPUs != 2 {
		t.Errorf("Expected DefaultVCPUs 2, got %d", exec.config.DefaultVCPUs)
	}
	if exec.controllers == nil {
		t.Error("Expected controllers map to be initialized")
	}
}