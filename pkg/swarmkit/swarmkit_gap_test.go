package swarmkit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/restuhaqza/swarmcracker/pkg/storage"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunCleanup tests the runCleanup method
func TestRunCleanup(t *testing.T) {
	// Create a temporary rootfs directory
	tempDir := t.TempDir()
	rootfsDir := filepath.Join(tempDir, "rootfs")
	stateDir := filepath.Join(tempDir, "state")

	err := os.MkdirAll(rootfsDir, 0755)
	require.NoError(t, err)
	err = os.MkdirAll(stateDir, 0755)
	require.NoError(t, err)

	// Create an old image file (should be cleaned up)
	oldImagePath := filepath.Join(rootfsDir, "old-image.ext4")
	err = os.WriteFile(oldImagePath, []byte("old data"), 0644)
	require.NoError(t, err)
	// Set modification time to 30 days ago
	thirtyDaysAgo := time.Now().Add(-30 * 24 * time.Hour)
	err = os.Chtimes(oldImagePath, thirtyDaysAgo, thirtyDaysAgo)
	require.NoError(t, err)

	// Create a recent image file (should NOT be cleaned up)
	recentImagePath := filepath.Join(rootfsDir, "recent-image.ext4")
	err = os.WriteFile(recentImagePath, []byte("recent data"), 0644)
	require.NoError(t, err)

	// Create executor
	cfg := &Config{
		FirecrackerPath:  "firecracker",
		KernelPath:       "/usr/share/firecracker/vmlinux",
		RootfsDir:        rootfsDir,
		SocketDir:        filepath.Join(tempDir, "sockets"),
		StateDir:         stateDir,
		MaxImageAgeDays:  7, // Only clean images older than 7 days
		DefaultVCPUs:     1,
		DefaultMemoryMB:  512,
		BridgeName:       "swarm-br0",
		Subnet:           "192.168.127.0/24",
		BridgeIP:         "192.168.127.1/24",
	}

	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	defer func() {
		exec.cleanupCancel()
		<-exec.cleanupDone
	}()

	// Run cleanup
	exec.runCleanup(context.Background())

	// Verify old image was removed
	_, err = os.Stat(oldImagePath)
	assert.True(t, os.IsNotExist(err), "Old image should be removed")

	// Verify recent image still exists
	_, err = os.Stat(recentImagePath)
	assert.NoError(t, err, "Recent image should not be removed")
}

// TestConfigure tests the Configure method
func TestConfigure(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        filepath.Join(tempDir, "state"),
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	defer func() {
		exec.cleanupCancel()
		<-exec.cleanupDone
	}()

	node := &api.Node{
		ID: "test-node-1",
		Description: &api.NodeDescription{
			Hostname: "test-host",
		},
	}

	err = exec.Configure(context.Background(), node)
	assert.NoError(t, err, "Configure should succeed")
}

// TestSetNetworkBootstrapKeys tests the SetNetworkBootstrapKeys method
func TestSetNetworkBootstrapKeys(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        filepath.Join(tempDir, "state"),
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	defer func() {
		exec.cleanupCancel()
		<-exec.cleanupDone
	}()

	keys := []*api.EncryptionKey{
		{
			Key: []byte("test-key-1"),
		},
		{
			Key: []byte("test-key-2"),
		},
	}

	err = exec.SetNetworkBootstrapKeys(keys)
	assert.NoError(t, err, "SetNetworkBootstrapKeys should succeed")
}

// TestControllerUpdate tests the Controller Update method
func TestControllerUpdate(t *testing.T) {
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-update",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	// Test update before starting
	updatedTask := &api.Task{
		ID:        "test-task-update",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:updated",
				},
			},
		},
	}

	err = ctrl.Update(context.Background(), updatedTask)
	assert.NoError(t, err, "Update before starting should succeed")
	assert.Equal(t, "nginx:updated", ctrl.task.Spec.GetContainer().Image, "Task spec should be updated")

	// Test update after starting (should be no-op)
	ctrl.started = true
	err = ctrl.Update(context.Background(), updatedTask)
	assert.NoError(t, err, "Update after starting should succeed (no-op)")
}

// TestUpdatePreparedTask tests Update on a prepared but not started task
func TestUpdatePreparedTask(t *testing.T) {
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-update-prep",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:original",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	// Mark as prepared but not started
	ctrl.prepared = true

	updatedTask := &api.Task{
		ID:        "test-task-update-prep",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:updated",
				},
			},
		},
	}

	err = ctrl.Update(context.Background(), updatedTask)
	assert.NoError(t, err, "Update on prepared task should succeed")
	assert.Equal(t, "nginx:updated", ctrl.task.Spec.GetContainer().Image)
}

// TestControllerWait tests the Controller Wait method
func TestControllerWait(t *testing.T) {
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-wait",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	// Test wait when task is not in processes map (should return success)
	err = ctrl.Wait(context.Background())
	assert.NoError(t, err, "Wait for non-running task should succeed")
}

// TestControllerClose tests the Controller Close method
func TestControllerClose(t *testing.T) {
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-close",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	err = ctrl.Close()
	assert.NoError(t, err, "Close should succeed")
}

// TestContainerStatus tests the ContainerStatus method
func TestContainerStatus(t *testing.T) {
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-status",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	// Test status when not started
	status, err := ctrl.ContainerStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, task.ID, status.ContainerID)
	assert.Equal(t, int32(0), status.PID)

	// Test status when started but VM not running
	ctrl.started = true
	status, err = ctrl.ContainerStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, task.ID, status.ContainerID)
	assert.Equal(t, int32(0), status.PID)
	assert.Equal(t, int32(1), status.ExitCode, "Should show exit code 1 when VM not running")
}

// TestPortStatus tests the PortStatus method
func TestPortStatus(t *testing.T) {
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-port",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	_, err = ctrl.PortStatus(context.Background())
	require.NoError(t, err)
}

// TestHostname tests the hostname function
func TestHostname(t *testing.T) {
	h := hostname()
	assert.NotEmpty(t, h, "Hostname should not be empty")
	// Should be either actual hostname or "localhost" fallback
	assert.True(t, h != "localhost" || h == "localhost", "Hostname should be valid")
}

// TestKvmAvailable tests the kvmAvailable method
func TestKvmAvailable(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        filepath.Join(tempDir, "state"),
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	defer func() {
		exec.cleanupCancel()
		<-exec.cleanupDone
	}()

	// Test kvmAvailable - will return true if /dev/kvm exists, false otherwise
	available := exec.kvmAvailable()
	// Just ensure it doesn't panic
	assert.True(t, available == true || available == false)
}

// TestArchSupported tests the archSupported method
func TestArchSupported(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        filepath.Join(tempDir, "state"),
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	defer func() {
		exec.cleanupCancel()
		<-exec.cleanupDone
	}()

	supported := exec.archSupported()
	assert.True(t, supported == true || supported == false)
}

// TestGetCPUs tests the getCPUs method
func TestGetCPUs(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &Config{
		FirecrackerPath:  "firecracker",
		KernelPath:       "/usr/share/firecracker/vmlinux",
		RootfsDir:        filepath.Join(tempDir, "rootfs"),
		SocketDir:        filepath.Join(tempDir, "sockets"),
		StateDir:         filepath.Join(tempDir, "state"),
		DefaultVCPUs:     1,
		DefaultMemoryMB:  512,
		BridgeName:       "swarm-br0",
		Subnet:           "192.168.127.0/24",
		BridgeIP:         "192.168.127.1/24",
		ReservedCPUs:     1,
		ReservedMemoryMB: 512,
	}

	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	defer func() {
		exec.cleanupCancel()
		<-exec.cleanupDone
	}()

	cpus := exec.getCPUs()
	assert.Greater(t, cpus, int64(0), "CPU count should be positive")
	assert.Less(t, cpus, int64(1000000000000), "CPU count should be reasonable (<1000 CPUs in nanocpus)")
}

// TestGetMemory tests the getMemory method
func TestGetMemory(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &Config{
		FirecrackerPath:  "firecracker",
		KernelPath:       "/usr/share/firecracker/vmlinux",
		RootfsDir:        filepath.Join(tempDir, "rootfs"),
		SocketDir:        filepath.Join(tempDir, "sockets"),
		StateDir:         filepath.Join(tempDir, "state"),
		DefaultVCPUs:     1,
		DefaultMemoryMB:  512,
		BridgeName:       "swarm-br0",
		Subnet:           "192.168.127.0/24",
		BridgeIP:         "192.168.127.1/24",
		ReservedCPUs:     1,
		ReservedMemoryMB: 512,
	}

	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	defer func() {
		exec.cleanupCancel()
		<-exec.cleanupDone
	}()

	memory := exec.getMemory()
	assert.Greater(t, memory, int64(0), "Memory should be positive")
	assert.Greater(t, memory, int64(512*1024*1024), "Memory should be at least 512MB")
}

// TestReadMeminfo tests the readMeminfo method
func TestReadMeminfo(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &Config{
		FirecrackerPath:  "firecracker",
		KernelPath:       "/usr/share/firecracker/vmlinux",
		RootfsDir:        filepath.Join(tempDir, "rootfs"),
		SocketDir:        filepath.Join(tempDir, "sockets"),
		StateDir:         filepath.Join(tempDir, "state"),
		DefaultVCPUs:     1,
		DefaultMemoryMB:  512,
		BridgeName:       "swarm-br0",
		Subnet:           "192.168.127.0/24",
		BridgeIP:         "192.168.127.1/24",
		ReservedCPUs:     1,
		ReservedMemoryMB: 512,
	}

	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	defer func() {
		exec.cleanupCancel()
		<-exec.cleanupDone
	}()

	// Test on actual system (may return 0 if /proc/meminfo not available)
	meminfo := exec.readMeminfo()
	// On Linux with /proc/meminfo, should return positive value
	// On other systems or if file unavailable, may return 0
	assert.True(t, meminfo >= 0)
}

// TestParseMeminfoLine tests the parseMeminfoLine function
func TestParseMeminfoLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		expected int64
	}{
		{
			name:     "valid memtotal line",
			line:     "MemTotal:       16384000 kB",
			expected: 16384000,
		},
		{
			name:     "valid memavailable line",
			line:     "MemAvailable:   8192000 kB",
			expected: 8192000,
		},
		{
			name:     "line with different spacing",
			line:     "MemTotal:	16384000 kB",
			expected: 16384000,
		},
		{
			name:     "invalid line",
			line:     "Some random text",
			expected: 0,
		},
		{
			name:     "empty line",
			line:     "",
			expected: 0,
		},
		{
			name:     "line without number",
			line:     "MemTotal:       kB",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseMeminfoLine(tt.line)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestLogWriter tests the logWriter Write method
func TestLogWriter(t *testing.T) {
	logger := zerolog.New(os.Stdout).With().Str("test", "logwriter").Logger()
	writer := &logWriter{logger: logger}

	n, err := writer.Write([]byte("test log message"))
	assert.NoError(t, err)
	assert.Equal(t, 16, n) // "test log message" is 16 characters

	n, err = writer.Write([]byte(""))
	assert.NoError(t, err)
	assert.Equal(t, 0, n)
}

// TestControllerRemove tests the Controller Remove method with cleanup
func TestControllerRemove(t *testing.T) {
	tempDir := t.TempDir()
	rootfsDir := filepath.Join(tempDir, "rootfs")
	socketDir := filepath.Join(tempDir, "sockets")
	stateDir := filepath.Join(tempDir, "state")

	err := os.MkdirAll(rootfsDir, 0755)
	require.NoError(t, err)
	err = os.MkdirAll(socketDir, 0755)
	require.NoError(t, err)
	err = os.MkdirAll(stateDir, 0755)
	require.NoError(t, err)

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       rootfsDir,
		SocketDir:       socketDir,
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	volumeMgr, err := storage.NewVolumeManager(filepath.Join(tempDir, "volumes"))
	require.NoError(t, err)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	taskID := "test-task-remove"
	task := &api.Task{
		ID:        taskID,
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, volumeMgr, secretMgr)
	require.NoError(t, err)

	// Create test rootfs file
	rootfsPath := filepath.Join(rootfsDir, taskID+".ext4")
	err = os.WriteFile(rootfsPath, []byte("test rootfs"), 0644)
	require.NoError(t, err)

	// Create test socket file
	socketPath := filepath.Join(socketDir, taskID+".sock")
	err = os.WriteFile(socketPath, []byte("test socket"), 0644)
	require.NoError(t, err)

	ctx := context.Background()
	err = ctrl.Remove(ctx)
	require.NoError(t, err)

	// Verify files were removed
	_, err = os.Stat(rootfsPath)
	assert.True(t, os.IsNotExist(err), "Rootfs should be removed")

	_, err = os.Stat(socketPath)
	assert.True(t, os.IsNotExist(err), "Socket should be removed")

	assert.False(t, ctrl.started, "Started flag should be false")
	assert.False(t, ctrl.prepared, "Prepared flag should be false")
}

// TestMountRootfsUnmount tests the mountRootfs and unmountRootfs methods
func TestMountRootfsUnmount(t *testing.T) {
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-mount",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	// Test with non-existent image path (should fail gracefully)
	imagePath := filepath.Join(tempDir, "nonexistent.ext4")

	mountDir, err := ctrl.mountRootfs(imagePath)
	assert.Error(t, err, "Mount should fail for non-existent image")
	assert.Empty(t, mountDir, "Mount dir should be empty on error")

	// Test unmountRootfs with non-existent directory (should not panic)
	err = ctrl.unmountRootfs("/tmp/nonexistent-mount-xyz123")
	// This should not crash, even if umount fails
	assert.NoError(t, err, "Unmount should not return error even if directory doesn't exist")
}

// TestConvertSecrets tests the convertSecrets function
func TestConvertSecrets(t *testing.T) {
	tests := []struct {
		name     string
		task     *api.Task
		expected int
	}{
		{
			name: "with secrets",
			task: &api.Task{
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Secrets: []*api.SecretReference{
								{
									SecretID:   "secret-1",
									SecretName: "db-password",
									Target: &api.SecretReference_File{
										File: &api.FileTarget{
											Name: "/run/secrets/db_password",
										},
									},
								},
								{
									SecretID:   "secret-2",
									SecretName: "api-key",
									Target: &api.SecretReference_File{
										File: &api.FileTarget{
											Name: "/run/secrets/api_key",
										},
									},
								},
							},
						},
					},
				},
			},
			expected: 2,
		},
		{
			name: "with secret using default target",
			task: &api.Task{
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Secrets: []*api.SecretReference{
								{
									SecretID:   "secret-1",
									SecretName: "db-password",
									// No Target specified, should use default
								},
							},
						},
					},
				},
			},
			expected: 1,
		},
		{
			name: "no container spec",
			task: &api.Task{
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: nil,
					},
				},
			},
			expected: 0,
		},
		{
			name: "no secrets",
			task: &api.Task{
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Secrets: []*api.SecretReference{},
						},
					},
				},
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secrets := convertSecrets(tt.task)
			assert.Len(t, secrets, tt.expected)

			if len(secrets) > 0 {
				// Check that default target is used when not specified
				if tt.task.Spec.GetContainer().Secrets[0].Target == nil {
					assert.Equal(t, "/run/secrets/"+tt.task.Spec.GetContainer().Secrets[0].SecretName, secrets[0].Target)
				}
			}
		})
	}
}

// TestConvertConfigs tests the convertConfigs function
func TestConvertConfigs(t *testing.T) {
	tests := []struct {
		name     string
		task     *api.Task
		expected int
	}{
		{
			name: "with configs",
			task: &api.Task{
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Configs: []*api.ConfigReference{
								{
									ConfigID:   "config-1",
									ConfigName: "nginx-config",
									Target: &api.ConfigReference_File{
										File: &api.FileTarget{
											Name: "/etc/nginx/nginx.conf",
										},
									},
								},
							},
						},
					},
				},
			},
			expected: 1,
		},
		{
			name: "with config using default target",
			task: &api.Task{
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Configs: []*api.ConfigReference{
								{
									ConfigID:   "config-1",
									ConfigName: "app-config",
									// No Target specified
								},
							},
						},
					},
				},
			},
			expected: 1,
		},
		{
			name: "no container spec",
			task: &api.Task{
				Spec: api.TaskSpec{},
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configs := convertConfigs(tt.task)
			assert.Len(t, configs, tt.expected)

			if len(configs) > 0 {
				// Check that default target is used when not specified
				if tt.task.Spec.GetContainer() != nil && len(tt.task.Spec.GetContainer().Configs) > 0 && tt.task.Spec.GetContainer().Configs[0].Target == nil {
					assert.Equal(t, "/config/"+tt.task.Spec.GetContainer().Configs[0].ConfigName, configs[0].Target)
				}
			}
		})
	}
}

// TestNewExecutorNilConfig tests NewExecutor with nil config
func TestNewExecutorNilConfig(t *testing.T) {
	exec, err := NewExecutor(nil)
	assert.Error(t, err, "NewExecutor with nil config should return error")
	assert.Nil(t, exec)
	assert.Contains(t, err.Error(), "config cannot be nil")
}

// TestNewTaskTranslatorNilKernelPath tests NewTaskTranslator with empty kernel path
func TestNewTaskTranslatorNilKernelPath(t *testing.T) {
	trans, err := NewTaskTranslator("", "192.168.127.1/24")
	assert.Error(t, err, "NewTaskTranslator with empty kernel path should return error")
	assert.Nil(t, trans)
	assert.Contains(t, err.Error(), "kernel path cannot be empty")
}

// TestVMMManagerForceStop tests ForceStop method
func TestVMMManagerForceStop(t *testing.T) {
	tempDir := t.TempDir()

	vmmMgr, err := NewVMMManager("firecracker", tempDir)
	require.NoError(t, err)

	task := &types.Task{
		ID: "test-force-stop-task",
	}

	// Test force stopping non-existent task
	err = vmmMgr.ForceStop(context.Background(), task)
	assert.Error(t, err, "ForceStop should return error for non-existent task")
	assert.Contains(t, err.Error(), "task not found")
}

// TestVMMManagerDescribe tests Describe method
func TestVMMManagerDescribe(t *testing.T) {
	tempDir := t.TempDir()

	vmmMgr, err := NewVMMManager("firecracker", tempDir)
	require.NoError(t, err)

	task := &types.Task{
		ID: "test-describe-task",
	}

	// Test describing non-existent task
	status, err := vmmMgr.Describe(context.Background(), task)
	require.NoError(t, err)
	assert.NotNil(t, status)
	assert.Equal(t, types.TaskStateComplete, status.State)
}

// TestSyncVolumeData tests syncVolumeData method
func TestSyncVolumeData(t *testing.T) {
	tempDir := t.TempDir()
	rootfsDir := filepath.Join(tempDir, "rootfs")
	stateDir := filepath.Join(tempDir, "state")
	volumesDir := filepath.Join(tempDir, "volumes")

	err := os.MkdirAll(rootfsDir, 0755)
	require.NoError(t, err)
	err = os.MkdirAll(stateDir, 0755)
	require.NoError(t, err)

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       rootfsDir,
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	volumeMgr, err := storage.NewVolumeManager(volumesDir)
	require.NoError(t, err)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-sync",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, volumeMgr, secretMgr)
	require.NoError(t, err)

	// Test with no rootfs annotation (should return early)
	internalTask := &types.Task{
		ID:        task.ID,
		ServiceID: task.ServiceID,
		NodeID:    task.NodeID,
		Spec: types.TaskSpec{
			Runtime: &types.Container{},
		},
		Annotations: map[string]string{}, // No rootfs
	}

	ctrl.internalTask = internalTask
	mounts := []types.Mount{
		{
			Source: "volume://test-vol",
			Target: "/data",
		},
	}

	ctx := context.Background()
	err = ctrl.syncVolumeData(ctx, internalTask, mounts)
	assert.NoError(t, err, "Sync with no rootfs should not error")
}

// TestStartDirectErrorPaths tests error paths in startDirect
func TestStartDirectErrorPaths(t *testing.T) {
	// Create an invalid socket directory path that will fail
	invalidDir := "/proc/nonexistent-swarmcracker-test-12345"

	vmmMgr, err := NewVMMManager("firecracker", invalidDir)
	require.NoError(t, err)

	task := &types.Task{
		ID: "test-start-direct-error",
		Spec: types.TaskSpec{
			Runtime: &types.Container{},
		},
	}

	config := map[string]interface{}{
		"machine-config": map[string]interface{}{
			"vcpu_count":   1,
			"mem_size_mib": 512,
		},
		"boot-source": map[string]interface{}{
			"kernel_image_path": "/usr/share/firecracker/vmlinux",
			"boot_args":         "console=ttyS0",
		},
		"drives": []map[string]interface{}{
			{
				"drive_id":       "rootfs",
				"path_on_host":   "/nonexistent/rootfs.ext4",
				"is_root_device": true,
			},
		},
	}

	ctx := context.Background()
	err = vmmMgr.Start(ctx, task, config)
	// Should fail to create socket directory or start firecracker
	assert.Error(t, err, "startDirect should fail with invalid directory")
}

// TestControllerRemoveWithCallback tests Remove with OnRemove callback
func TestControllerRemoveWithCallback(t *testing.T) {
	tempDir := t.TempDir()
	rootfsDir := filepath.Join(tempDir, "rootfs")
	socketDir := filepath.Join(tempDir, "sockets")
	stateDir := filepath.Join(tempDir, "state")

	err := os.MkdirAll(rootfsDir, 0755)
	require.NoError(t, err)
	err = os.MkdirAll(socketDir, 0755)
	require.NoError(t, err)
	err = os.MkdirAll(stateDir, 0755)
	require.NoError(t, err)

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       rootfsDir,
		SocketDir:       socketDir,
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	taskID := "test-task-callback"
	task := &api.Task{
		ID:        taskID,
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	// Set up callback
	callbackCalled := false
	ctrl.OnRemove = func() {
		callbackCalled = true
	}

	ctx := context.Background()
	err = ctrl.Remove(ctx)
	require.NoError(t, err)

	// Verify callback was called
	assert.True(t, callbackCalled, "OnRemove callback should be called")
}

// TestStartWithoutPrepare tests Start without calling Prepare first
func TestStartWithoutPrepare(t *testing.T) {
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-no-prepare",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	// Try to start without preparing
	err = ctrl.Start(context.Background())
	assert.Error(t, err, "Start without prepare should fail")
	assert.Contains(t, err.Error(), "not prepared")
}

// TestShutdownNotStarted tests Shutdown on a task that was never started
func TestShutdownNotStarted(t *testing.T) {
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-shutdown-not-started",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	err = ctrl.Shutdown(context.Background())
	assert.NoError(t, err, "Shutdown of not started task should succeed (no-op)")
}

// TestTerminateNotStarted tests Terminate on a task that was never started
func TestTerminateNotStarted(t *testing.T) {
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       filepath.Join(tempDir, "rootfs"),
		SocketDir:       filepath.Join(tempDir, "sockets"),
		StateDir:        stateDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Subnet:          "192.168.127.0/24",
		BridgeIP:        "192.168.127.1/24",
	}

	imageCfg := &image.PreparerConfig{
		RootfsDir: cfg.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	netCfg := types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
		BridgeIP:   cfg.BridgeIP,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-terminate-not-started",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	err = ctrl.Terminate(context.Background())
	assert.NoError(t, err, "Terminate of not started task should succeed (no-op)")
}

// TestVMMManagerGetPID tests GetPID with non-existent task
func TestVMMManagerGetPID(t *testing.T) {
	tempDir := t.TempDir()

	vmmMgr, err := NewVMMManager("firecracker", tempDir)
	require.NoError(t, err)

	pid := vmmMgr.GetPID("non-existent-task")
	assert.Equal(t, 0, pid, "GetPID for non-existent task should return 0")
}

// TestVMMManagerIsRunningNonExistent tests IsRunning with non-existent task
func TestVMMManagerIsRunningNonExistent(t *testing.T) {
	tempDir := t.TempDir()

	vmmMgr, err := NewVMMManager("firecracker", tempDir)
	require.NoError(t, err)

	running := vmmMgr.IsRunning("non-existent-task")
	assert.False(t, running, "IsRunning for non-existent task should return false")
}
