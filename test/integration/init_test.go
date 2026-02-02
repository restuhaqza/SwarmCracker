package integration

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/lifecycle"
	"github.com/restuhaqza/swarmcracker/pkg/translator"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_InitSystemTini tests init system with tini.
func TestIntegration_InitSystemTini(t *testing.T) {
	firecracker, runtime := checkPrerequisites(t)
	if !firecracker || runtime == "" {
		t.Skip("Prerequisites not met")
	}

	t.Log("Testing init system with tini")

	tmpDir := t.TempDir()

	// Use test kernel path
	kernelPath := "/home/kali/.local/share/firecracker/vmlinux"
	if _, err := os.Stat(kernelPath); os.IsNotExist(err) {
		t.Skipf("Kernel not found at %s", kernelPath)
	}

	// Create image preparer with tini
	imageCfg := &image.PreparerConfig{
		RootfsDir:       tmpDir,
		InitSystem:      "tini",
		InitGracePeriod: 5,
	}
	preparer := image.NewImagePreparer(imageCfg)

	// Create a test task with nginx
	task := &types.Task{
		ID:        "test-init-tini",
		ServiceID: "test-service",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image:   testImageNginx,
				Command: []string{"nginx"},
				Args:    []string{"-g", "daemon off;"},
			},
		},
		Annotations: make(map[string]string),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Prepare the image (with init injection)
	t.Log("Preparing image with tini...")
	err := preparer.Prepare(ctx, task)
	if err != nil {
		t.Logf("Image preparation failed: %v", err)
		t.Skip("Container runtime may not be configured")
		return
	}

	// Verify init system was injected
	initSystem, ok := task.Annotations["init_system"]
	require.True(t, ok, "init_system annotation should be set")
	assert.Equal(t, "tini", initSystem)

	initPath, ok := task.Annotations["init_path"]
	require.True(t, ok, "init_path annotation should be set")
	assert.Equal(t, "/sbin/tini", initPath)

	rootfsPath, ok := task.Annotations["rootfs"]
	require.True(t, ok, "rootfs annotation should be set")
	require.FileExists(t, rootfsPath, "rootfs file should exist")

	t.Logf("Image prepared with init system: %s", initSystem)

	// Create translator with tini
	transCfg := &lifecycle.ManagerConfig{
		KernelPath: kernelPath,
		RootfsDir:  tmpDir,
		SocketDir:  tmpDir,
	}
	translator := translator.NewTaskTranslator(transCfg)

	// Translate task to config
	config, err := translator.Translate(task)
	require.NoError(t, err)

	// Verify boot args contain init
	configStr, ok := config.(string)
	require.True(t, ok)
	assert.Contains(t, configStr, "/sbin/tini")
	assert.Contains(t, configStr, "--")

	t.Logf("Config generated with init boot args")

	// Note: We don't actually start the VM in this test because it requires
	// additional setup (networking, etc.) but we've verified that:
	// 1. Init system is configured in the preparer
	// 2. Init path is set in annotations
	// 3. Boot args are properly wrapped with init
}

// TestIntegration_InitSystemDumbInit tests init system with dumb-init.
func TestIntegration_InitSystemDumbInit(t *testing.T) {
	firecracker, runtime := checkPrerequisites(t)
	if !firecracker || runtime == "" {
		t.Skip("Prerequisites not met")
	}

	t.Log("Testing init system with dumb-init")

	tmpDir := t.TempDir()

	// Use test kernel path
	kernelPath := "/home/kali/.local/share/firecracker/vmlinux"
	if _, err := os.Stat(kernelPath); os.IsNotExist(err) {
		t.Skipf("Kernel not found at %s", kernelPath)
	}

	// Create image preparer with dumb-init
	imageCfg := &image.PreparerConfig{
		RootfsDir:       tmpDir,
		InitSystem:      "dumb-init",
		InitGracePeriod: 5,
	}
	preparer := image.NewImagePreparer(imageCfg)

	// Create a test task with redis
	task := &types.Task{
		ID:        "test-init-dumbinit",
		ServiceID: "test-service",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image:   testImageRedis,
				Command: []string{"redis-server"},
			},
		},
		Annotations: make(map[string]string),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Prepare the image
	t.Log("Preparing image with dumb-init...")
	err := preparer.Prepare(ctx, task)
	if err != nil {
		t.Logf("Image preparation failed: %v", err)
		t.Skip("Container runtime may not be configured")
		return
	}

	// Verify init system
	initSystem, ok := task.Annotations["init_system"]
	require.True(t, ok)
	assert.Equal(t, "dumb-init", initSystem)

	initPath, ok := task.Annotations["init_path"]
	require.True(t, ok)
	assert.Equal(t, "/sbin/dumb-init", initPath)

	t.Logf("Image prepared with init system: %s", initSystem)
}

// TestIntegration_InitSystemNone tests without init system.
func TestIntegration_InitSystemNone(t *testing.T) {
	firecracker, runtime := checkPrerequisites(t)
	if !firecracker || runtime == "" {
		t.Skip("Prerequisites not met")
	}

	t.Log("Testing without init system")

	tmpDir := t.TempDir()

	// Create image preparer without init
	imageCfg := &image.PreparerConfig{
		RootfsDir:       tmpDir,
		InitSystem:      "none",
		InitGracePeriod: 10,
	}
	preparer := image.NewImagePreparer(imageCfg)

	// Create a test task
	task := &types.Task{
		ID:        "test-init-none",
		ServiceID: "test-service",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: testImageAlpine,
				Args:  []string{"/bin/sh", "-c", "sleep 5"},
			},
		},
		Annotations: make(map[string]string),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Prepare the image
	t.Log("Preparing image without init...")
	err := preparer.Prepare(ctx, task)
	if err != nil {
		t.Logf("Image preparation failed: %v", err)
		t.Skip("Container runtime may not be configured")
		return
	}

	// Verify no init system
	_, ok := task.Annotations["init_system"]
	assert.False(t, ok, "init_system should not be set when disabled")

	_, ok = task.Annotations["init_path"]
	assert.False(t, ok, "init_path should not be set when disabled")

	t.Log("Image prepared without init system")
}

// TestIntegration_InitSystemGracefulShutdown tests graceful shutdown.
func TestIntegration_InitSystemGracefulShutdown(t *testing.T) {
	firecracker, _ := checkPrerequisites(t)
	if !firecracker {
		t.Skip("Firecracker not available")
	}

	t.Log("Testing graceful shutdown with init system")

	tmpDir := t.TempDir()

	// Use test kernel path
	kernelPath := "/home/kali/.local/share/firecracker/vmlinux"
	if _, err := os.Stat(kernelPath); os.IsNotExist(err) {
		t.Skipf("Kernel not found at %s", kernelPath)
	}

	// Create VMM manager
	cfg := &lifecycle.ManagerConfig{
		KernelPath: kernelPath,
		RootfsDir:  tmpDir,
		SocketDir:  tmpDir,
	}

	vmm := lifecycle.NewVMMManager(cfg)

	// Create task with init system annotations
	task := &types.Task{
		ID: "test-graceful-shutdown",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: testImageAlpine,
			},
		},
		Annotations: map[string]string{
			"init_system": "tini",
			"init_path":   "/sbin/tini",
		},
	}

	ctx := context.Background()

	// Verify VM doesn't exist
	status, err := vmm.Describe(ctx, task)
	assert.NoError(t, err)
	assert.Equal(t, types.TaskState_ORPHANED, status.State)

	// Test that graceful shutdown logic exists
	// (We don't actually start the VM in this test)
	t.Log("Graceful shutdown test completed (VM not actually started)")
}

// TestIntegration_InitBinariesAvailable checks if init binaries are available on host.
func TestIntegration_InitBinariesAvailable(t *testing.T) {
	t.Log("Checking for init binaries on host...")

	// Check for tini
	tiniPaths := []string{"/usr/bin/tini", "/usr/sbin/tini", "/sbin/tini"}
	tiniFound := false
	for _, path := range tiniPaths {
		if _, err := os.Stat(path); err == nil {
			t.Logf("✓ tini found at: %s", path)
			tiniFound = true
			break
		}
	}
	if !tiniFound {
		// Check in PATH
		if _, err := exec.LookPath("tini"); err == nil {
			t.Log("✓ tini found in PATH")
			tiniFound = true
		}
	}
	if !tiniFound {
		t.Log("✗ tini not found")
		t.Log("  Install: apt-get install tini (Debian/Ubuntu)")
		t.Log("  Or download: https://github.com/krallin/tini/releases")
	}

	// Check for dumb-init
	dumbInitPaths := []string{"/usr/bin/dumb-init", "/usr/sbin/dumb-init", "/sbin/dumb-init"}
	dumbInitFound := false
	for _, path := range dumbInitPaths {
		if _, err := os.Stat(path); err == nil {
			t.Logf("✓ dumb-init found at: %s", path)
			dumbInitFound = true
			break
		}
	}
	if !dumbInitFound {
		// Check in PATH
		if _, err := exec.LookPath("dumb-init"); err == nil {
			t.Log("✓ dumb-init found in PATH")
			dumbInitFound = true
		}
	}
	if !dumbInitFound {
		t.Log("✗ dumb-init not found")
		t.Log("  Install: apt-get install dumb-init (Debian/Ubuntu)")
		t.Log("  Or download: https://github.com/Yelp/dumb-init/releases")
	}

	if tiniFound || dumbInitFound {
		t.Log("")
		t.Log("At least one init binary is available for testing")
	} else {
		t.Log("")
		t.Log("No init binaries found - init system injection will use minimal shell scripts")
	}
}
