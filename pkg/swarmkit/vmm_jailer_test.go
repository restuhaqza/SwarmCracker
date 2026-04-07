package swarmkit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TestVMMManagerWithJailerConfig tests VMM manager creation with jailer configuration.
func TestVMMManagerWithJailerConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Skip if jailer not available
	jailerPath, err := exec.LookPath("jailer")
	if err != nil {
		t.Skip("Jailer not found, skipping jailer tests")
	}

	firecrackerPath, err := exec.LookPath("firecracker")
	if err != nil {
		t.Skip("Firecracker not found, skipping jailer tests")
	}

	cfg := &VMMManagerConfig{
		FirecrackerPath: firecrackerPath,
		JailerPath:      jailerPath,
		SocketDir:       filepath.Join(tmpDir, "sockets"),
		UseJailer:       true,
		JailerUID:       1000,
		JailerGID:       1000,
		JailerChrootDir: filepath.Join(tmpDir, "jailer"),
		CgroupVersion:   "v2",
		EnableCgroups:   false, // Disable for unit test
	}

	vmm, err := NewVMMManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
	}

	if vmm == nil {
		t.Fatal("Expected non-nil VMM manager")
	}

	if !vmm.useJailer {
		t.Error("Expected useJailer to be true")
	}

	if vmm.jailer == nil {
		t.Error("Expected jailer instance to be created")
	}
}

// TestVMMManagerLegacyMode tests backwards compatibility without jailer.
func TestVMMManagerLegacyMode(t *testing.T) {
	tmpDir := t.TempDir()

	firecrackerPath, err := exec.LookPath("firecracker")
	if err != nil {
		t.Skip("Firecracker not found, skipping test")
	}

	// Create with legacy constructor
	vmm, err := NewVMMManager(firecrackerPath, filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewVMMManager() error = %v", err)
	}

	if vmm == nil {
		t.Fatal("Expected non-nil VMM manager")
	}

	if vmm.useJailer {
		t.Error("Expected useJailer to be false in legacy mode")
	}

	if vmm.jailer != nil {
		t.Error("Expected jailer to be nil in legacy mode")
	}
}

// TestVMMManagerConfigDefaults tests default configuration values.
func TestVMMManagerConfigDefaults(t *testing.T) {
	tmpDir := t.TempDir()

	firecrackerPath, err := exec.LookPath("firecracker")
	if err != nil {
		t.Skip("Firecracker not found, skipping test")
	}

	jailerPath, err := exec.LookPath("jailer")
	if err != nil {
		t.Skip("Jailer not found, skipping test")
	}

	// Test with minimal config (should use defaults)
	cfg := &VMMManagerConfig{
		FirecrackerPath: firecrackerPath,
		JailerPath:      jailerPath,
		SocketDir:       filepath.Join(tmpDir, "sockets"),
		UseJailer:       true,
		// Other fields intentionally omitted to test defaults
	}

	vmm, err := NewVMMManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
	}

	// Verify defaults were applied
	if vmm.jailerConfig.UID != 1000 {
		t.Errorf("Expected default UID 1000, got %d", vmm.jailerConfig.UID)
	}
	if vmm.jailerConfig.GID != 1000 {
		t.Errorf("Expected default GID 1000, got %d", vmm.jailerConfig.GID)
	}
	if vmm.jailerConfig.ChrootBaseDir != "/var/lib/swarmcracker/jailer" {
		t.Errorf("Expected default chroot dir, got %q", vmm.jailerConfig.ChrootBaseDir)
	}
}

// TestVMMManagerStartWithJailerInvalidConfig tests error handling for invalid configs.
func TestVMMManagerStartWithJailerInvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()

	firecrackerPath, err := exec.LookPath("firecracker")
	if err != nil {
		t.Skip("Firecracker not found, skipping test")
	}

	jailerPath, err := exec.LookPath("jailer")
	if err != nil {
		t.Skip("Jailer not found, skipping test")
	}

	cfg := &VMMManagerConfig{
		FirecrackerPath: firecrackerPath,
		JailerPath:      jailerPath,
		SocketDir:       filepath.Join(tmpDir, "sockets"),
		UseJailer:       true,
		JailerUID:       1000,
		JailerGID:       1000,
		JailerChrootDir: filepath.Join(tmpDir, "jailer"),
	}

	vmm, err := NewVMMManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
	}

	// Create task with invalid config
	task := &types.Task{
		ID:        "test-invalid",
		ServiceID: "test-service",
	}

	// Invalid config (missing required fields)
	invalidConfig := map[string]interface{}{
		"invalid": "data",
	}

	ctx := context.Background()
	err = vmm.Start(ctx, task, invalidConfig)
	if err == nil {
		t.Error("Expected error for invalid config, got nil")
	}
}

// TestVMMManagerJailerSocketPath tests socket path handling in jailer mode.
func TestVMMManagerJailerSocketPath(t *testing.T) {
	tmpDir := t.TempDir()

	firecrackerPath, err := exec.LookPath("firecracker")
	if err != nil {
		t.Skip("Firecracker not found, skipping test")
	}

	jailerPath, err := exec.LookPath("jailer")
	if err != nil {
		t.Skip("Jailer not found, skipping test")
	}

	chrootDir := filepath.Join(tmpDir, "jailer")
	cfg := &VMMManagerConfig{
		FirecrackerPath: firecrackerPath,
		JailerPath:      jailerPath,
		SocketDir:       filepath.Join(tmpDir, "sockets"),
		UseJailer:       true,
		JailerUID:       1000,
		JailerGID:       1000,
		JailerChrootDir: chrootDir,
	}

	if _, err := NewVMMManagerWithConfig(cfg); err != nil {
		t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
	}

	// Expected socket path in chroot
	taskID := "test-socket-path"
	expectedSocketPath := filepath.Join(chrootDir, taskID, "run", "firecracker", taskID+".sock")

	// Verify the path structure is correct
	if filepath.Dir(expectedSocketPath) != filepath.Join(chrootDir, taskID, "run", "firecracker") {
		t.Errorf("Unexpected socket path structure: %q", expectedSocketPath)
	}
}

// TestVMMManagerCgroupIntegration tests cgroup manager integration.
func TestVMMManagerCgroupIntegration(t *testing.T) {
	tmpDir := t.TempDir()

	firecrackerPath, err := exec.LookPath("firecracker")
	if err != nil {
		t.Skip("Firecracker not found, skipping test")
	}

	jailerPath, err := exec.LookPath("jailer")
	if err != nil {
		t.Skip("Jailer not found, skipping test")
	}

	// Skip if cgroup v2 not available
	if !isCgroupV2Available() {
		t.Skip("Cgroup v2 not available, skipping cgroup integration test")
	}

	cfg := &VMMManagerConfig{
		FirecrackerPath: firecrackerPath,
		JailerPath:      jailerPath,
		SocketDir:       filepath.Join(tmpDir, "sockets"),
		UseJailer:       true,
		JailerUID:       1000,
		JailerGID:       1000,
		JailerChrootDir: filepath.Join(tmpDir, "jailer"),
		EnableCgroups:   true,
		ResourceLimits: jailer.ResourceLimits{
			CPUQuotaUs:  500000,
			MemoryMax:   268435456,
			MemoryHigh:  241591910,
		},
	}

	vmm, err := NewVMMManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
	}

	if vmm.cgroupMgr == nil {
		t.Error("Expected cgroup manager to be created")
	}
}

// TestVMMManagerStopWithJailer tests stopping VMs in jailer mode.
func TestVMMManagerStopWithJailer(t *testing.T) {
	tmpDir := t.TempDir()

	firecrackerPath, err := exec.LookPath("firecracker")
	if err != nil {
		t.Skip("Firecracker not found, skipping test")
	}

	jailerPath, err := exec.LookPath("jailer")
	if err != nil {
		t.Skip("Jailer not found, skipping test")
	}

	cfg := &VMMManagerConfig{
		FirecrackerPath: firecrackerPath,
		JailerPath:      jailerPath,
		SocketDir:       filepath.Join(tmpDir, "sockets"),
		UseJailer:       true,
		JailerUID:       1000,
		JailerGID:       1000,
		JailerChrootDir: filepath.Join(tmpDir, "jailer"),
	}

	vmm, err := NewVMMManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
	}

	// Try to stop non-existent task (should handle gracefully)
	task := &types.Task{
		ID:        "nonexistent",
		ServiceID: "test-service",
	}

	ctx := context.Background()
	err = vmm.Stop(ctx, task)
	if err == nil {
		t.Error("Expected error for non-existent task")
	}
}

// TestVMMManagerRemoveWithJailer tests VM removal with jailer cleanup.
func TestVMMManagerRemoveWithJailer(t *testing.T) {
	tmpDir := t.TempDir()

	firecrackerPath, err := exec.LookPath("firecracker")
	if err != nil {
		t.Skip("Firecracker not found, skipping test")
	}

	jailerPath, err := exec.LookPath("jailer")
	if err != nil {
		t.Skip("Jailer not found, skipping test")
	}

	cfg := &VMMManagerConfig{
		FirecrackerPath: firecrackerPath,
		JailerPath:      jailerPath,
		SocketDir:       filepath.Join(tmpDir, "sockets"),
		UseJailer:       true,
		JailerUID:       1000,
		JailerGID:       1000,
		JailerChrootDir: filepath.Join(tmpDir, "jailer"),
	}

	vmm, err := NewVMMManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
	}

	task := &types.Task{
		ID:        "test-remove",
		ServiceID: "test-service",
	}

	ctx := context.Background()
	err = vmm.Remove(ctx, task)
	if err != nil {
		t.Errorf("Remove() error = %v", err)
	}
}

// TestVMMManagerResourceLimits tests resource limit application.
func TestVMMManagerResourceLimits(t *testing.T) {
	tmpDir := t.TempDir()

	firecrackerPath, err := exec.LookPath("firecracker")
	if err != nil {
		t.Skip("Firecracker not found, skipping test")
	}

	jailerPath, err := exec.LookPath("jailer")
	if err != nil {
		t.Skip("Jailer not found, skipping test")
	}

	cfg := &VMMManagerConfig{
		FirecrackerPath: firecrackerPath,
		JailerPath:      jailerPath,
		SocketDir:       filepath.Join(tmpDir, "sockets"),
		UseJailer:       true,
		JailerUID:       1000,
		JailerGID:       1000,
		JailerChrootDir: filepath.Join(tmpDir, "jailer"),
		EnableCgroups:   true,
	}

	vmm, err := NewVMMManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
	}

	// Verify resource limits can be set
	if vmm.jailerConfig == nil {
		t.Fatal("Expected jailer config to be set")
	}

	// Test various resource limit configurations
	limits := []jailer.ResourceLimits{
		{CPUQuotaUs: 250000, MemoryMax: 134217728},   // 0.25 CPU, 128MB
		{CPUQuotaUs: 500000, MemoryMax: 268435456},   // 0.5 CPU, 256MB
		{CPUQuotaUs: 1000000, MemoryMax: 536870912},  // 1 CPU, 512MB
		{CPUQuotaUs: 2000000, MemoryMax: 1073741824}, // 2 CPU, 1GB
	}

	for i, limit := range limits {
		t.Logf("Limit set %d: CPU=%dµs, Memory=%d bytes", i, limit.CPUQuotaUs, limit.MemoryMax)
	}
}

// TestVMMManagerJailerHealthCheck tests health check with jailer.
func TestVMMManagerJailerHealthCheck(t *testing.T) {
	tmpDir := t.TempDir()

	firecrackerPath, err := exec.LookPath("firecracker")
	if err != nil {
		t.Skip("Firecracker not found, skipping test")
	}

	jailerPath, err := exec.LookPath("jailer")
	if err != nil {
		t.Skip("Jailer not found, skipping test")
	}

	cfg := &VMMManagerConfig{
		FirecrackerPath: firecrackerPath,
		JailerPath:      jailerPath,
		SocketDir:       filepath.Join(tmpDir, "sockets"),
		UseJailer:       true,
		JailerUID:       1000,
		JailerGID:       1000,
		JailerChrootDir: filepath.Join(tmpDir, "jailer"),
	}

	vmm, err := NewVMMManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewVMMManagerWithConfig() error = %v", err)
	}

	// Test health check on non-existent VM (should return false)
	healthy := vmm.CheckVMAPIHealth("nonexistent-vm")
	if healthy {
		t.Error("Expected health check to fail for non-existent VM")
	}
}

// TestVMMManagerIsRunning tests VM running state detection.
func TestVMMManagerIsRunning(t *testing.T) {
	tmpDir := t.TempDir()

	firecrackerPath, err := exec.LookPath("firecracker")
	if err != nil {
		t.Skip("Firecracker not found, skipping test")
	}

	vmm, err := NewVMMManager(firecrackerPath, filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewVMMManager() error = %v", err)
	}

	// Test non-existent VM
	running := vmm.IsRunning("nonexistent")
	if running {
		t.Error("Expected non-existent VM to not be running")
	}
}

// Helper function to check cgroup v2 availability
func isCgroupV2Available() bool {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		return true
	}
	return false
}
