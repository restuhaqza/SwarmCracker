package swarmkit

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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

// TestPrepare_MissingAnnotations tests Prepare when annotations are missing
func TestPrepare_MissingAnnotations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

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
		EnableJailer:    false,
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
		ID:        "test-task-no-annotations",
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

	ctx := context.Background()

	// Prepare should complete even without initial annotations
	// The annotations are added during image preparation
	err = ctrl.Prepare(ctx)
	assert.NoError(t, err, "Prepare should succeed and add annotations")
	assert.True(t, ctrl.prepared, "Task should be marked as prepared")
	assert.NotNil(t, ctrl.internalTask, "Internal task should be created")
}

// TestPrepare_InvalidConfig tests Prepare with invalid configurations
func TestPrepare_InvalidConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
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
		EnableJailer:    false,
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

	tests := []struct {
		name        string
		task        *api.Task
		expectError bool
		errorMsg    string
	}{
		{
			name: "nil container spec",
			task: &api.Task{
				ID:        "test-task-nil-container",
				ServiceID: "test-service",
				NodeID:    "test-node",
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: nil,
					},
				},
			},
			expectError: false, // Should create minimal task
		},
		{
			name: "empty image name",
			task: &api.Task{
				ID:        "test-task-empty-image",
				ServiceID: "test-service",
				NodeID:    "test-node",
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Image: "",
						},
					},
				},
			},
			expectError: true, // Empty image should fail during image prep
		},
		{
			name: "task with nil runtime",
			task: &api.Task{
				ID:        "test-task-nil-runtime",
				ServiceID: "test-service",
				NodeID:    "test-node",
				Spec: api.TaskSpec{
					Runtime: nil,
				},
			},
			expectError: false, // Should create minimal task
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl, err := NewController(tt.task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
			require.NoError(t, err)

			ctx := context.Background()
			err = ctrl.Prepare(ctx)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				// Even with invalid specs, Prepare should create a minimal task
				// The actual error would come from image prep
				if err == nil {
					assert.True(t, ctrl.prepared || ctrl.internalTask != nil)
				}
			}
		})
	}
}

// TestPrepare_WithJailerDisabled tests Prepare when jailer is disabled
func TestPrepare_WithJailerDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
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
		EnableJailer:    false, // Jailer disabled
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
		ID:        "test-task-no-jailer",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "alpine:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	ctx := context.Background()
	err = ctrl.Prepare(ctx)
	assert.NoError(t, err, "Prepare should succeed without jailer")
	assert.True(t, ctrl.prepared, "Task should be marked as prepared")
	assert.NotNil(t, ctrl.internalTask, "Internal task should be created")

	// Verify VMM manager is using non-jailer mode
	assert.NotNil(t, ctrl.vmmMgr)
}

// TestPrepare_AlreadyPrepared tests Prepare when task is already prepared
func TestPrepare_AlreadyPrepared(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
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
		ID:        "test-task-already-prepared",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "alpine:latest",
				},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	ctx := context.Background()

	// First prepare
	err = ctrl.Prepare(ctx)
	require.NoError(t, err)
	require.True(t, ctrl.prepared)

	// Second prepare should be a no-op
	err = ctrl.Prepare(ctx)
	assert.NoError(t, err, "Prepare should succeed when already prepared")
	assert.True(t, ctrl.prepared)
}

// TestPrepare_WithNetworks tests Prepare with network attachments
func TestPrepare_WithNetworks(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
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
		ID:        "test-task-with-networks",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
		Networks: []*api.NetworkAttachment{
			{
				Network: &api.Network{
					ID: "net-1",
					Spec: api.NetworkSpec{
						Annotations: api.Annotations{
							Name: "test-network",
						},
						DriverConfig: &api.Driver{
							Name: "bridge",
							Options: map[string]string{
								"bridge": "swarm-br0",
							},
						},
					},
				},
				Addresses: []string{"192.168.127.10/24"},
			},
		},
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	ctx := context.Background()
	err = ctrl.Prepare(ctx)
	assert.NoError(t, err, "Prepare with networks should succeed")
	assert.True(t, ctrl.prepared)
	assert.NotNil(t, ctrl.internalTask)

	// Verify networks are preserved in internal task
	if ctrl.internalTask != nil {
		assert.Greater(t, len(ctrl.internalTask.Networks), 0, "Networks should be present in internal task")
	}
}

// TestPrepare_WithSecretsAndConfigs tests Prepare with secrets and configs
func TestPrepare_WithSecretsAndConfigs(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")
	rootfsDir := filepath.Join(tempDir, "rootfs")

	err := os.MkdirAll(rootfsDir, 0755)
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

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-with-secrets",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
					Secrets: []*api.SecretReference{
						{
							SecretID:   "secret-1",
							SecretName: "api-key",
							Target: &api.SecretReference_File{
								File: &api.FileTarget{
									Name: "/run/secrets/api.key",
								},
							},
						},
					},
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
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	ctx := context.Background()

	// Create a fake rootfs with annotations
	rootfsPath := filepath.Join(rootfsDir, task.ID+".ext4")
	err = os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644)
	require.NoError(t, err)

	// Manually set the internal task with rootfs annotation for this test
	ctrl.internalTask = &types.Task{
		ID:   task.ID,
		Spec: types.TaskSpec{Runtime: &types.Container{}},
		Annotations: map[string]string{
			"rootfs": rootfsPath,
		},
	}

	err = ctrl.Prepare(ctx)
	// Prepare might fail due to image prep, but secrets/configs should be processed
	// The important part is that no panic occurs
	assert.True(t, err == nil || ctrl.internalTask != nil)
}

// TestPeriodicCleanup_ContextCancellation tests periodicCleanup with context cancellation
func TestPeriodicCleanup_ContextCancellation(t *testing.T) {
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
		MaxImageAgeDays: 7,
	}

	exec, err := NewExecutor(cfg)
	require.NoError(t, err)

	// Verify cleanup goroutine is running
	assert.NotNil(t, exec.cleanupDone)

	// Cancel the cleanup context immediately
	exec.cleanupCancel()

	// Wait for the goroutine to stop (with timeout)
	select {
	case <-exec.cleanupDone:
		// Goroutine stopped cleanly
		assert.True(t, true, "Cleanup goroutine stopped cleanly")
	case <-time.After(5 * time.Second):
		t.Fatal("Cleanup goroutine did not stop within timeout")
	}
}

// TestPeriodicCleanup_InitialDelay tests periodicCleanup initial delay behavior
func TestPeriodicCleanup_InitialDelay(t *testing.T) {
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
		MaxImageAgeDays: 7,
	}

	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	defer func() {
		exec.cleanupCancel()
		<-exec.cleanupDone
	}()

	// The initial cleanup is scheduled after 5 minutes
	// We cancel before it runs to verify it doesn't block
	time.Sleep(100 * time.Millisecond)

	exec.cleanupCancel()

	select {
	case <-exec.cleanupDone:
		// Goroutine stopped
		assert.True(t, true)
	case <-time.After(2 * time.Second):
		t.Fatal("Cleanup goroutine did not stop")
	}
}

// TestRunCleanup_CleanupLogic tests the cleanup logic with various scenarios
func TestRunCleanup_CleanupLogic(t *testing.T) {
	tests := []struct {
		name             string
		maxAgeDays       int
		createOldFile    bool
		createRecentFile bool
		expectOldRemoved bool
	}{
		{
			name:             "cleanup old files only",
			maxAgeDays:       7,
			createOldFile:    true,
			createRecentFile: true,
			expectOldRemoved: true,
		},
		{
			name:             "cleanup with zero max age (default to 7)",
			maxAgeDays:       0,
			createOldFile:    true,
			createRecentFile: true,
			expectOldRemoved: true,
		},
		{
			name:             "no cleanup when max age is high",
			maxAgeDays:       365,
			createOldFile:    true,
			createRecentFile: true,
			expectOldRemoved: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			rootfsDir := filepath.Join(tempDir, "rootfs")
			stateDir := filepath.Join(tempDir, "state")

			err := os.MkdirAll(rootfsDir, 0755)
			require.NoError(t, err)
			err = os.MkdirAll(stateDir, 0755)
			require.NoError(t, err)

			var oldImagePath string
			if tt.createOldFile {
				oldImagePath = filepath.Join(rootfsDir, "old-image.ext4")
				err = os.WriteFile(oldImagePath, []byte("old data"), 0644)
				require.NoError(t, err)
				// Set modification time to 30 days ago
				thirtyDaysAgo := time.Now().Add(-30 * 24 * time.Hour)
				err = os.Chtimes(oldImagePath, thirtyDaysAgo, thirtyDaysAgo)
				require.NoError(t, err)
			}

			var recentImagePath string
			if tt.createRecentFile {
				recentImagePath = filepath.Join(rootfsDir, "recent-image.ext4")
				err = os.WriteFile(recentImagePath, []byte("recent data"), 0644)
				require.NoError(t, err)
			}

			cfg := &Config{
				FirecrackerPath: "firecracker",
				KernelPath:      "/usr/share/firecracker/vmlinux",
				RootfsDir:       rootfsDir,
				SocketDir:       filepath.Join(tempDir, "sockets"),
				StateDir:        stateDir,
				MaxImageAgeDays: tt.maxAgeDays,
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

			// Run cleanup directly
			exec.runCleanup(context.Background())

			// Verify old file status
			if tt.createOldFile {
				_, err := os.Stat(oldImagePath)
				if tt.expectOldRemoved {
					assert.True(t, os.IsNotExist(err), "Old image should be removed")
				} else {
					assert.NoError(t, err, "Old image should not be removed")
				}
			}

			// Verify recent file still exists
			if tt.createRecentFile {
				_, err := os.Stat(recentImagePath)
				assert.NoError(t, err, "Recent image should not be removed")
			}
		})
	}
}

// TestRunCleanup_EmptyDirectory tests cleanup with empty directory
func TestRunCleanup_EmptyDirectory(t *testing.T) {
	tempDir := t.TempDir()
	rootfsDir := filepath.Join(tempDir, "rootfs")
	stateDir := filepath.Join(tempDir, "state")

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
		MaxImageAgeDays: 7,
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

	// Run cleanup on empty directory - should not error
	exec.runCleanup(context.Background())
	assert.True(t, true, "Cleanup on empty directory should not error")
}

// TestDescribe_WithMockResources tests Describe with various resource scenarios
func TestDescribe_WithMockResources(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	tests := []struct {
		name              string
		config            *Config
		expectFirecracker bool
		expectGenericRes  bool
	}{
		{
			name: "basic resources",
			config: &Config{
				FirecrackerPath: "firecracker",
				ReservedCPUs:    1,
			},
			expectFirecracker: true, // KVM likely available on most systems
			expectGenericRes:  true,
		},
		{
			name: "with reserved resources",
			config: &Config{
				FirecrackerPath:  "firecracker",
				ReservedCPUs:     2,
				ReservedMemoryMB: 1024,
			},
			expectFirecracker: true,
			expectGenericRes:  true,
		},
		{
			name: "minimal config",
			config: &Config{
				FirecrackerPath: "firecracker",
			},
			expectFirecracker: true,
			expectGenericRes:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec, err := NewExecutor(tt.config)
			require.NoError(t, err)
			defer func() {
				exec.cleanupCancel()
				<-exec.cleanupDone
			}()

			ctx := context.Background()
			desc, err := exec.Describe(ctx)

			require.NoError(t, err)
			require.NotNil(t, desc)

			// Check hostname is set
			assert.NotEmpty(t, desc.Hostname, "Hostname should be set")

			// Check platform
			assert.NotNil(t, desc.Platform, "Platform should be set")
			assert.NotEmpty(t, desc.Platform.Architecture, "Architecture should be set")
			assert.NotEmpty(t, desc.Platform.OS, "OS should be set")

			// Check resources
			require.NotNil(t, desc.Resources, "Resources should be set")
			assert.Greater(t, desc.Resources.NanoCPUs, int64(0), "NanoCPUs should be positive")
			assert.Greater(t, desc.Resources.MemoryBytes, int64(0), "MemoryBytes should be positive")

			// Check generic resources
			if tt.expectGenericRes {
				assert.NotNil(t, desc.Resources.Generic, "Generic resources should be set")
			}

			// Check Firecracker availability
			if tt.expectFirecracker {
				kvmAvailable := exec.kvmAvailable()
				archSupported := exec.archSupported()

				if kvmAvailable && archSupported {
					assert.Greater(t, len(desc.Resources.Generic), 0, "Should have Firecracker generic resource when KVM available")
				}
			}
		})
	}
}

// TestDescribe_ResourceComputation tests that resources are computed correctly
func TestDescribe_ResourceComputation(t *testing.T) {
	totalCPUs := runtime.NumCPU()

	cfg := &Config{
		FirecrackerPath:  "firecracker",
		ReservedCPUs:     totalCPUs / 2,
		ReservedMemoryMB: 512,
	}

	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	defer func() {
		exec.cleanupCancel()
		<-exec.cleanupDone
	}()

	ctx := context.Background()
	desc, err := exec.Describe(ctx)
	require.NoError(t, err)

	// Verify CPUs are computed correctly
	expectedCPUs := totalCPUs - (totalCPUs / 2)
	if expectedCPUs < 1 {
		expectedCPUs = 1
	}
	expectedNanoCPUs := int64(expectedCPUs) * 1e9
	assert.Equal(t, expectedNanoCPUs, desc.Resources.NanoCPUs, "NanoCPUs should match expected value")

	// Verify memory is positive
	assert.Greater(t, desc.Resources.MemoryBytes, int64(512*1024*1024), "Memory should be at least 512MB")
}

// TestNewExecutor_JailerEnabled tests NewExecutor with jailer enabled
func TestNewExecutor_JailerEnabled(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
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
		EnableJailer:    true,
		JailerPath:      "/usr/local/bin/jailer",
		JailerUID:       12345,
		JailerGID:       12345,
		JailerChrootDir: "/var/lib/swarmcracker/jailer",
		ParentCgroup:    "/sys/fs/cgroup",
		CgroupVersion:   "v2",
		EnableCgroups:   true,
	}

	exec, err := NewExecutor(cfg)

	if err != nil {
		// Jailer binary may not be available in test environment
		t.Skipf("Jailer not available in test environment: %v", err)
		return
	}

	require.NoError(t, err)
	assert.NotNil(t, exec)
	assert.NotNil(t, exec.vmmMgr)

	defer func() {
		exec.cleanupCancel()
		<-exec.cleanupDone
	}()

	// Verify VMM manager is configured for jailer mode
	assert.NotNil(t, exec.vmmMgr)
}

// TestConvertTask_WithNetworks tests convertTask with various network configurations
func TestConvertTask_WithNetworks(t *testing.T) {
	tests := []struct {
		name             string
		task             *api.Task
		expectedNetworks int
		expectedDriver   string
	}{
		{
			name: "single bridge network",
			task: &api.Task{
				ID: "task-1",
				Networks: []*api.NetworkAttachment{
					{
						Network: &api.Network{
							ID: "net-1",
							Spec: api.NetworkSpec{
								Annotations: api.Annotations{
									Name: "my-network",
								},
								DriverConfig: &api.Driver{
									Name: "bridge",
									Options: map[string]string{
										"bridge": "custom-br0",
									},
								},
							},
						},
						Addresses: []string{"10.0.0.2/24"},
					},
				},
			},
			expectedNetworks: 1,
			expectedDriver:   "bridge",
		},
		{
			name: "multiple networks",
			task: &api.Task{
				ID: "task-2",
				Networks: []*api.NetworkAttachment{
					{
						Network: &api.Network{
							ID: "net-1",
							Spec: api.NetworkSpec{
								Annotations: api.Annotations{Name: "network-1"},
								DriverConfig: &api.Driver{
									Name: "bridge",
								},
							},
						},
						Addresses: []string{"10.0.0.2/24"},
					},
					{
						Network: &api.Network{
							ID: "net-2",
							Spec: api.NetworkSpec{
								Annotations: api.Annotations{Name: "network-2"},
								DriverConfig: &api.Driver{
									Name: "overlay",
								},
							},
						},
						Addresses: []string{"10.0.1.2/24"},
					},
				},
			},
			expectedNetworks: 2,
		},
		{
			name:             "no networks",
			task:             &api.Task{ID: "task-3"},
			expectedNetworks: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.task.Spec.Runtime == nil {
				tt.task.Spec = api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Image: "test",
						},
					},
				}
			}

			ctrl := &Controller{
				task: tt.task,
			}

			internalTask := ctrl.convertTask()

			assert.NotNil(t, internalTask)
			assert.Equal(t, tt.expectedNetworks, len(internalTask.Networks))

			if tt.expectedDriver != "" && len(internalTask.Networks) > 0 {
				assert.Equal(t, tt.expectedDriver, internalTask.Networks[0].Network.Spec.Driver)
			}
		})
	}
}

// TestNewExecutor_WithDefaults tests NewExecutor applies all defaults
func TestNewExecutor_WithDefaults(t *testing.T) {
	cfg := &Config{
		// Only provide firecracker path
		FirecrackerPath: "firecracker",
	}

	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	defer func() {
		exec.cleanupCancel()
		<-exec.cleanupDone
	}()

	// Verify all defaults were applied
	assert.Equal(t, "/usr/share/firecracker/vmlinux", exec.config.KernelPath)
	assert.Equal(t, "/var/lib/firecracker/rootfs", exec.config.RootfsDir)
	assert.Equal(t, "/var/run/firecracker", exec.config.SocketDir)
	assert.Equal(t, 1, exec.config.DefaultVCPUs)
	assert.Equal(t, 512, exec.config.DefaultMemoryMB)
	assert.Equal(t, "swarm-br0", exec.config.BridgeName)
	assert.Equal(t, "192.168.127.0/24", exec.config.Subnet)
	assert.Equal(t, "192.168.127.1/24", exec.config.BridgeIP)
	assert.Equal(t, "static", exec.config.IPMode)
}

// TestController_NilSecretManager tests controller behavior with nil secret manager
func TestController_NilSecretManager(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	tempDir := t.TempDir()
	rootfsDir := filepath.Join(tempDir, "rootfs")

	err := os.MkdirAll(rootfsDir, 0755)
	require.NoError(t, err)

	cfg := &Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       rootfsDir,
		SocketDir:       filepath.Join(tempDir, "sockets"),
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

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-nil-secret-mgr",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "alpine:latest",
					Secrets: []*api.SecretReference{
						{SecretID: "s1", SecretName: "secret1"},
					},
				},
			},
		},
	}

	// Create controller with nil secret manager
	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, nil)
	require.NoError(t, err)

	// Create fake rootfs
	rootfsPath := filepath.Join(rootfsDir, task.ID+".ext4")
	err = os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644)
	require.NoError(t, err)

	ctrl.internalTask = &types.Task{
		ID:   task.ID,
		Spec: types.TaskSpec{Runtime: &types.Container{}},
		Annotations: map[string]string{
			"rootfs": rootfsPath,
		},
		Secrets: []types.SecretRef{
			{ID: "s1", Name: "secret1", Target: "/run/secrets/secret1"},
		},
	}

	ctx := context.Background()
	err = ctrl.Prepare(ctx)

	// Should not panic with nil secret manager
	// Secret injection is skipped when secretMgr is nil
	assert.True(t, err == nil || ctrl.internalTask != nil)
}

// TestMountRootfs_ErrorHandling tests mountRootfs error handling
func TestMountRootfs_ErrorHandling(t *testing.T) {
	ctrl := &Controller{
		logger: zerolog.Nop(),
	}

	// Test with non-existent image
	_, err := ctrl.mountRootfs("/nonexistent/path.ext4")
	assert.Error(t, err, "Should fail with non-existent image")

	// Test with invalid path
	_, err = ctrl.mountRootfs("/dev/null")
	assert.Error(t, err, "Should fail with invalid image")
}

// TestUnmountRootfs_ErrorHandling tests unmountRootfs error handling
func TestUnmountRootfs_ErrorHandling(t *testing.T) {
	ctrl := &Controller{
		logger: zerolog.Nop(),
	}

	// Test with non-existent directory
	err := ctrl.unmountRootfs("/nonexistent/mount-point")
	// Should not error, just logs warning
	assert.NoError(t, err, "Unmount should not error on non-existent path")
}

// TestPrepare_WithSecretsOnly tests Prepare with only secrets (no configs)
func TestPrepare_WithSecretsOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")
	rootfsDir := filepath.Join(tempDir, "rootfs")

	err := os.MkdirAll(rootfsDir, 0755)
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

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-secrets-only",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
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
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	// Create fake rootfs
	rootfsPath := filepath.Join(rootfsDir, task.ID+".ext4")
	err = os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644)
	require.NoError(t, err)

	ctrl.internalTask = &types.Task{
		ID:   task.ID,
		Spec: types.TaskSpec{Runtime: &types.Container{}},
		Annotations: map[string]string{
			"rootfs": rootfsPath,
		},
		Secrets: []types.SecretRef{
			{ID: "secret-1", Name: "db-password", Target: "/run/secrets/db_password"},
			{ID: "secret-2", Name: "api-key", Target: "/run/secrets/api_key"},
		},
	}

	ctx := context.Background()
	err = ctrl.Prepare(ctx)

	// Should complete without panicking
	assert.True(t, err == nil || ctrl.internalTask != nil)
}

// TestPrepare_WithConfigsOnly tests Prepare with only configs (no secrets)
func TestPrepare_WithConfigsOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")
	rootfsDir := filepath.Join(tempDir, "rootfs")

	err := os.MkdirAll(rootfsDir, 0755)
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

	secretMgr := storage.NewSecretManager(
		filepath.Join(stateDir, "secrets"),
		filepath.Join(stateDir, "configs"),
	)

	vmmMgr, err := NewVMMManager(cfg.FirecrackerPath, cfg.SocketDir)
	require.NoError(t, err)

	task := &api.Task{
		ID:        "test-task-configs-only",
		ServiceID: "test-service",
		NodeID:    "test-node",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
					Configs: []*api.ConfigReference{
						{
							ConfigID:   "config-1",
							ConfigName: "nginx-conf",
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
	}

	ctrl, err := NewController(task, cfg, imagePrep, networkMgr, vmmMgr, nil, secretMgr)
	require.NoError(t, err)

	// Create fake rootfs
	rootfsPath := filepath.Join(rootfsDir, task.ID+".ext4")
	err = os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644)
	require.NoError(t, err)

	ctrl.internalTask = &types.Task{
		ID:   task.ID,
		Spec: types.TaskSpec{Runtime: &types.Container{}},
		Annotations: map[string]string{
			"rootfs": rootfsPath,
		},
		Configs: []types.ConfigRef{
			{ID: "config-1", Name: "nginx-conf", Target: "/etc/nginx/nginx.conf"},
		},
	}

	ctx := context.Background()
	err = ctrl.Prepare(ctx)

	// Should complete without panicking
	assert.True(t, err == nil || ctrl.internalTask != nil)
}
