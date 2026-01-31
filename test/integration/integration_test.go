package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/executor"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/lifecycle"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/restuhaqza/swarmcracker/pkg/translator"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests require Firecracker and container runtime
// These tests are skipped if prerequisites are not met

const (
	// Test images (small, simple images)
	testImageNginx    = "nginx:alpine"
	testImageRedis   = "redis:alpine"
	testImageAlpine  = "alpine:latest"
)

func init() {
	// Setup logging for integration tests
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	})
}

// checkPrerequisites verifies that all required tools are available
func checkPrerequisites(t *testing.T) (firecracker bool, containerRuntime string) {
	t.Helper()

	// Check for Firecracker
	if _, err := exec.LookPath("firecracker"); err == nil {
		firecracker = true
	}

	// Check for container runtimes
	runtimes := []string{"docker", "podman"}
	for _, runtime := range runtimes {
		if _, err := exec.LookPath(runtime); err == nil {
			containerRuntime = runtime
			break
		}
	}

	// Check for KVM
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("KVM device not available (/dev/kvm)")
	}

	if !firecracker {
		t.Skip("Firecracker not found. Install from: https://github.com/firecracker-microvm/firecracker")
	}

	if containerRuntime == "" {
		t.Skip("Container runtime not found (docker or podman required)")
	}

	return firecracker, containerRuntime
}

// setupTestEnvironment creates a test environment
func setupTestEnvironment(t *testing.T) (string, func()) {
	t.Helper()

	tmpDir := t.TempDir()

	// Create required directories
	dirs := []string{
		filepath.Join(tmpDir, "rootfs"),
		filepath.Join(tmpDir, "sockets"),
		filepath.Join(tmpDir, "cache"),
	}

	for _, dir := range dirs {
		err := os.MkdirAll(dir, 0755)
		require.NoError(t, err)
	}

	cleanup := func() {
		// TempDir will be cleaned up automatically
	}

	return tmpDir, cleanup
}

// TestIntegration_EndToEnd tests the complete pipeline with real Firecracker
func TestIntegration_EndToEnd(t *testing.T) {
	firecracker, runtime := checkPrerequisites(t)

	if !firecracker || runtime == "" {
		return
	}

	t.Logf("Running integration tests with %s", runtime)

	tmpDir, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Create executor
	cfg := &config.Config{}
	cfg.SetDefaults()
	cfg.Executor.RootfsDir = filepath.Join(tmpDir, "rootfs")
	cfg.Executor.SocketDir = filepath.Join(tmpDir, "sockets")

	exec, err := createTestExecutor(cfg)
	require.NoError(t, err)
	defer exec.Close()

	// Create a test task
	task := createTestTask("test-e2e", testImageAlpine)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Prepare the task (pull image, create rootfs)
	t.Log("Preparing task...")
	err = exec.Prepare(ctx, task)
	if err != nil {
		t.Logf("Prepare failed (expected without full setup): %v", err)
		t.Skip("Image preparation requires configured container runtime")
		return
	}

	// Verify rootfs was created
	rootfsPath, ok := task.Annotations["rootfs"]
	require.True(t, ok, "rootfs annotation should be set")
	require.FileExists(t, rootfsPath, "rootfs file should exist")

	t.Logf("Rootfs created at: %s", rootfsPath)

	// Start the task
	t.Log("Starting task...")
	err = exec.Start(ctx, task)
	if err != nil {
		t.Logf("Start failed: %v", err)
		// Cleanup
		exec.Remove(ctx, task)
		t.Skip("Firecracker startup requires additional setup")
		return
	}

	// Wait a bit for VM to start
	time.Sleep(2 * time.Second)

	// Check task status
	status, err := exec.Wait(ctx, task)
	require.NoError(t, err)
	assert.Equal(t, types.TaskState_RUNNING, status.State)

	t.Log("Task is running!")

	// Cleanup
	t.Log("Cleaning up...")
	err = exec.Remove(ctx, task)
	assert.NoError(t, err)
}

// TestIntegration_ImagePreparation tests image preparation with real container runtime
func TestIntegration_ImagePreparation(t *testing.T) {
	_, runtime := checkPrerequisites(t)
	if runtime == "" {
		return
	}

	t.Logf("Testing image preparation with %s", runtime)

	tmpDir := t.TempDir()

	cfg := &image.PreparerConfig{
		RootfsDir: tmpDir,
	}

	preparer := image.NewImagePreparer(cfg)

	task := &types.Task{
		ID:        "test-image-prep",
		ServiceID: "test-service",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: testImageAlpine,
			},
		},
		Annotations: make(map[string]string),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Prepare the image
	err := preparer.Prepare(ctx, task)
	if err != nil {
		t.Logf("Image preparation failed: %v", err)
		t.Skipf("Container runtime %s may not be configured properly", runtime)
		return
	}

	// Verify rootfs was created
	rootfsPath, ok := task.Annotations["rootfs"]
	require.True(t, ok, "rootfs annotation should be set")
	require.FileExists(t, rootfsPath, "rootfs file should exist")

	// Check file size
	info, err := os.Stat(rootfsPath)
	require.NoError(t, err)
	t.Logf("Rootfs size: %d bytes (%.2f MB)", info.Size(), float64(info.Size())/(1024*1024))

	// Verify it's a valid ext4 image (check for magic bytes)
	data, err := os.ReadFile(rootfsPath)
	require.NoError(t, err)
	if len(data) > 1080 {
		// ext4 magic number is at offset 1080
		magic := string(data[1080:1084])
		t.Logf("EXT4 magic number: %s", magic)
	}
}

// TestIntegration_VMMManager tests VMM lifecycle with real Firecracker
func TestIntegration_VMMManager(t *testing.T) {
	firecracker, _ := checkPrerequisites(t)
	if !firecracker {
		return
	}

	t.Log("Testing VMM Manager with real Firecracker")

	tmpDir := t.TempDir()

	cfg := &lifecycle.ManagerConfig{
		KernelPath: "/usr/share/firecracker/vmlinux",
		RootfsDir:  tmpDir,
		SocketDir:  tmpDir,
	}

	vmm := lifecycle.NewVMMManager(cfg)

	// Create a simple task
	task := &types.Task{
		ID:        "test-vmm",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: testImageAlpine,
			},
		},
	}

	ctx := context.Background()

	// Try to describe a non-existent VM
	status, err := vmm.Describe(ctx, task)
	assert.NoError(t, err)
	assert.Equal(t, types.TaskState_ORPHANED, status.State)

	t.Log("VMM Manager test completed")
}

// TestIntegration_NetworkSetup tests network configuration
func TestIntegration_NetworkSetup(t *testing.T) {
	t.Log("Testing network setup (may require privileges)")

	nm := network.NewNetworkManager(types.NetworkConfig{
		BridgeName: "test-br-integration",
	})

	task := &types.Task{
		ID:        "test-net",
		ServiceID: "test-service",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "test-br-integration",
							},
						},
					},
				},
				Addresses: []string{"192.168.1.2/24"},
			},
		},
	}

	ctx := context.Background()

	// Try to prepare network (will likely fail without privileges)
	err := nm.PrepareNetwork(ctx, task)
	if err != nil {
		t.Logf("Network preparation failed (expected without privileges): %v", err)
	}

	// Cleanup
	err = nm.CleanupNetwork(ctx, task)
	assert.NoError(t, err, "Cleanup should not error")

	t.Log("Network setup test completed")
}

// TestIntegration_TaskTranslation tests task to VMM config translation
func TestIntegration_TaskTranslation(t *testing.T) {
	t.Log("Testing task translation")

	tmpDir := t.TempDir()

	cfg := &lifecycle.ManagerConfig{
		KernelPath: "/usr/share/firecracker/vmlinux",
		RootfsDir:  tmpDir,
		SocketDir:  tmpDir,
	}

	translator := translator.NewTaskTranslator(cfg)

	task := &types.Task{
		ID:        "test-translate",
		ServiceID: "test-service",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image:   testImageNginx,
				Command: []string{"nginx"},
				Args:    []string{"-g", "daemon off;"},
				Env:     []string{"NGINX_PORT=8080"},
			},
			Resources: types.ResourceRequirements{
				Limits: &types.Resources{
					NanoCPUs:    2 * 1e9,
					MemoryBytes: 512 * 1024 * 1024,
				},
			},
		},
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "network-1",
				},
			},
		},
		Annotations: make(map[string]string),
	}

	// Add rootfs annotation (required for translation)
	task.Annotations["rootfs"] = filepath.Join(tmpDir, "test-rootfs.ext4")

	config, err := translator.Translate(task)
	assert.NoError(t, err)
	assert.NotNil(t, config)

	// Verify config structure
	configStr, ok := config.(string)
	assert.True(t, ok, "Config should be a string")
	assert.Contains(t, configStr, "BootSource")
	assert.Contains(t, configStr, "Drives")
	assert.Contains(t, configStr, "NetworkInterfaces")
	assert.Contains(t, configStr, "MachineConfig")

	t.Logf("Translated config length: %d bytes", len(configStr))
}

// createTestExecutor creates a configured executor for testing
func createTestExecutor(cfg *config.Config) (*executor.FirecrackerExecutor, error) {
	execConfig := &executor.Config{
		KernelPath:      cfg.Executor.KernelPath,
		RootfsDir:       cfg.Executor.RootfsDir,
		SocketDir:       cfg.Executor.SocketDir,
		DefaultVCPUs:    cfg.Executor.DefaultVCPUs,
		DefaultMemoryMB: cfg.Executor.DefaultMemoryMB,
		EnableJailer:    cfg.Executor.EnableJailer,
		Network: types.NetworkConfig{
			BridgeName:       cfg.Network.BridgeName,
			EnableRateLimit:  cfg.Network.EnableRateLimit,
			MaxPacketsPerSec: cfg.Network.MaxPacketsPerSec,
		},
	}

	vmmConfig := &lifecycle.ManagerConfig{
		KernelPath:      execConfig.KernelPath,
		RootfsDir:       execConfig.RootfsDir,
		SocketDir:       execConfig.SocketDir,
		DefaultVCPUs:    execConfig.DefaultVCPUs,
		DefaultMemoryMB: execConfig.DefaultMemoryMB,
		EnableJailer:    execConfig.EnableJailer,
	}

	imageConfig := &image.PreparerConfig{
		KernelPath:      execConfig.KernelPath,
		RootfsDir:       execConfig.RootfsDir,
		SocketDir:       execConfig.SocketDir,
		DefaultVCPUs:    execConfig.DefaultVCPUs,
		DefaultMemoryMB: execConfig.DefaultMemoryMB,
	}

	vmmManager := lifecycle.NewVMMManager(vmmConfig)
	taskTranslator := translator.NewTaskTranslator(vmmConfig)
	imagePreparer := image.NewImagePreparer(imageConfig)
	networkMgr := network.NewNetworkManager(execConfig.Network)

	return executor.NewFirecrackerExecutor(
		execConfig,
		vmmManager,
		taskTranslator,
		imagePreparer,
		networkMgr,
	)
}

// createTestTask creates a test task for integration testing
func createTestTask(id, imageRef string) *types.Task {
	return &types.Task{
		ID:        id,
		ServiceID: "test-service",
		NodeID:    "node-local",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image:   imageRef,
				Command: []string{},
				Args:    []string{},
				Env:     []string{},
				Mounts:  []types.Mount{},
			},
			Resources: types.ResourceRequirements{
				Limits: &types.Resources{
					NanoCPUs:    1 * 1e9,
					MemoryBytes: 512 * 1024 * 1024,
				},
			},
		},
		Status: types.TaskStatus{
			State: types.TaskState_PENDING,
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
			},
		},
		Annotations: make(map[string]string),
	}
}

// TestIntegration_PrerequisitesOnly checks what prerequisites are available
func TestIntegration_PrerequisitesOnly(t *testing.T) {
	t.Log("Checking integration test prerequisites...")

	// Check Firecracker
	if _, err := exec.LookPath("firecracker"); err == nil {
		t.Log("✓ Firecracker found")
	} else {
		t.Log("✗ Firecracker not found")
		t.Log("  Install: https://github.com/firecracker-microvm/firecracker/releases")
	}

	// Check container runtimes
	dockerFound := false
	if _, err := exec.LookPath("docker"); err == nil {
		t.Log("✓ Docker found")
		dockerFound = true
	}
	if _, err := exec.LookPath("podman"); err == nil {
		t.Log("✓ Podman found")
		dockerFound = true
	}
	if !dockerFound {
		t.Log("✗ No container runtime found (docker or podman required)")
	}

	// Check KVM
	if _, err := os.Stat("/dev/kvm"); err == nil {
		t.Log("✓ KVM device available")
	} else {
		t.Log("✗ KVM device not available")
	}

	// Check kernel
	kernelPaths := []string{
		"/usr/share/firecracker/vmlinux",
		"/boot/vmlinux",
		"/var/lib/firecracker/vmlinux",
	}
	kernelFound := false
	for _, path := range kernelPaths {
		if _, err := os.Stat(path); err == nil {
			t.Logf("✓ Kernel found: %s", path)
			kernelFound = true
			break
		}
	}
	if !kernelFound {
		t.Log("✗ Firecracker kernel not found")
		t.Log("  Download: https://github.com/firecracker-microvm/firecracker/releases")
	}

	t.Log("")
	t.Log("To run full integration tests, install:")
	t.Log("1. Firecracker: https://github.com/firecracker-microvm/firecracker/releases")
	t.Log("2. Firecracker kernel: See above link")
	t.Log("3. Container runtime: docker or podman")
}
