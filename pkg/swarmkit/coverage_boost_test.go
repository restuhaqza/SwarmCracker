package swarmkit

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

func TestVMMManagerConfig_JailerFields(t *testing.T) {
	cfg := &VMMManagerConfig{
		UseJailer:       true,
		JailerUID:       1000,
		JailerGID:       1000,
		JailerChrootDir: "/var/jailer",
		ParentCgroup:    "/sys/fs/cgroup/firecracker",
		CgroupVersion:   "v2",
		EnableCgroups:   true,
	}

	if !cfg.UseJailer {
		t.Error("UseJailer should be true")
	}
	if cfg.JailerUID != 1000 {
		t.Errorf("Expected JailerUID 1000, got %d", cfg.JailerUID)
	}
	if cfg.CgroupVersion != "v2" {
		t.Errorf("Expected CgroupVersion v2, got %s", cfg.CgroupVersion)
	}
}

func TestConfig_JailerConfig(t *testing.T) {
	cfg := &Config{
		EnableJailer:    true,
		JailerPath:      "/usr/bin/jailer",
		JailerUID:       1000,
		JailerGID:       1000,
		JailerChrootDir: "/var/jailer",
		ParentCgroup:    "/firecracker",
		CgroupVersion:   "v2",
		EnableCgroups:   true,
	}

	if !cfg.EnableJailer {
		t.Error("EnableJailer should be true")
	}
	if cfg.JailerPath != "/usr/bin/jailer" {
		t.Errorf("Expected JailerPath /usr/bin/jailer, got %s", cfg.JailerPath)
	}
}

func TestConfig_ResourceLimits(t *testing.T) {
	cfg := &Config{
		ReservedCPUs:     2,
		ReservedMemoryMB: 1024,
		MaxImageAgeDays:  7,
	}

	if cfg.ReservedCPUs != 2 {
		t.Errorf("Expected ReservedCPUs 2, got %d", cfg.ReservedCPUs)
	}
	if cfg.ReservedMemoryMB != 1024 {
		t.Errorf("Expected ReservedMemoryMB 1024, got %d", cfg.ReservedMemoryMB)
	}
	if cfg.MaxImageAgeDays != 7 {
		t.Errorf("Expected MaxImageAgeDays 7, got %d", cfg.MaxImageAgeDays)
	}
}

func TestController_PreparedState(t *testing.T) {
	ctrl := &Controller{
		prepared: false,
		started:  false,
	}

	// Test initial state
	if ctrl.prepared {
		t.Error("Controller should not be prepared initially")
	}
	if ctrl.started {
		t.Error("Controller should not be started initially")
	}
}

func TestExecutor_VMMManagerField(t *testing.T) {
	exec := &Executor{
		config: &Config{
			DefaultVCPUs:    2,
			DefaultMemoryMB: 512,
		},
		controllers: make(map[string]*Controller),
	}

	if exec.config.DefaultVCPUs != 2 {
		t.Errorf("Expected DefaultVCPUs 2, got %d", exec.config.DefaultVCPUs)
	}
	if exec.controllers == nil {
		t.Error("controllers map should be initialized")
	}
}

func TestMockVMMManager_WaitReturnsRunning(t *testing.T) {
	mock := &MockVMMManager{}

	// Without WaitFunc configured, should return running status
	ctx := t.Context()
	task := createTestTask("test")

	status, err := mock.Wait(ctx, task)
	if err != nil {
		t.Errorf("Wait should succeed: %v", err)
	}
	if status == nil {
		t.Error("Wait should return non-nil status")
	}
}

func TestMockImagePreparer_PrepareReturnsNil(t *testing.T) {
	mock := &MockImagePreparer{}

	ctx := t.Context()
	task := createTestTask("test")

	// Without PrepareFunc configured, should return nil
	if err := mock.Prepare(ctx, task); err != nil {
		t.Errorf("Prepare should return nil: %v", err)
	}

	// Without CleanupFunc configured, should return nil
	if err := mock.Cleanup(ctx, "test"); err != nil {
		t.Errorf("Cleanup should return nil: %v", err)
	}
}

func TestMockNetworkManager_DefaultReturns(t *testing.T) {
	mock := &MockNetworkManager{}

	ctx := t.Context()
	task := createTestTask("test")

	// Test default returns
	if err := mock.PrepareNetwork(ctx, task); err != nil {
		t.Errorf("PrepareNetwork should return nil: %v", err)
	}

	if err := mock.CleanupNetwork(ctx, task); err != nil {
		t.Errorf("CleanupNetwork should return nil: %v", err)
	}
}

func TestMockVolumeManager_DefaultReturns(t *testing.T) {
	mock := &MockVolumeManager{}

	ctx := t.Context()
	task := createTestTask("test")

	// Test default returns
	if err := mock.PrepareVolumes(ctx, task); err != nil {
		t.Errorf("PrepareVolumes should return nil: %v", err)
	}

	if err := mock.CleanupVolumes(ctx, task); err != nil {
		t.Errorf("CleanupVolumes should return nil: %v", err)
	}
}

func TestMockSecretManager_DefaultReturns(t *testing.T) {
	mock := &MockSecretManager{}

	ctx := t.Context()

	// Test default returns
	if err := mock.InjectSecrets(ctx, "test", nil, "/tmp/rootfs"); err != nil {
		t.Errorf("InjectSecrets should return nil: %v", err)
	}

	if err := mock.InjectConfigs(ctx, "test", nil, "/tmp/rootfs"); err != nil {
		t.Errorf("InjectConfigs should return nil: %v", err)
	}
}

// Helper function for tests
func createTestTask(id string) *types.Task {
	return &types.Task{
		ID:        id,
		ServiceID: "svc-" + id,
		NodeID:    "node-" + id,
	}
}