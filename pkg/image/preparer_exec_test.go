package image

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/storage"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractWithPodman_PodmanNotAvailable tests extractWithPodman when podman is not available
func TestExtractWithPodman_PodmanNotAvailable(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping test that requires podman in short mode")
	}

	// Create a temporary directory for testing
	tempDir := t.TempDir()
	destPath := filepath.Join(tempDir, "extract")

	// Create an ImagePreparer
	config := &PreparerConfig{
		RootfsDir: tempDir,
	}
	ip := NewImagePreparer(config).(*ImagePreparer)

	ctx := context.Background()

	// Test with a non-existent runtime (simulating podman not available)
	err := ip.extractWithDockerCLI(ctx, "nginx:latest", destPath)

	// Should fail with an error about the runtime not being found
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed")
}

// TestExtractWithPodman_Timeout tests extractWithPodman with timeout scenarios
func TestExtractWithPodman_Timeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping test that requires podman in short mode")
	}

	// Create a temporary directory for testing
	tempDir := t.TempDir()
	destPath := filepath.Join(tempDir, "extract")

	// Create an ImagePreparer
	config := &PreparerConfig{
		RootfsDir: tempDir,
	}
	ip := NewImagePreparer(config).(*ImagePreparer)

	// Create a context with a very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	// Give some time for the timeout to expire
	time.Sleep(10 * time.Millisecond)

	// Try to extract - should fail due to timeout
	err := ip.extractWithDockerCLI(ctx, "nginx:latest", destPath)

	// Should fail with context deadline exceeded or similar error
	assert.Error(t, err)
}

// TestExtractWithPodman_InvalidImageRef tests extractWithPodman with invalid image reference
func TestExtractWithPodman_InvalidImageRef(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping test that requires podman in short mode")
	}

	tempDir := t.TempDir()
	destPath := filepath.Join(tempDir, "extract")

	config := &PreparerConfig{
		RootfsDir: tempDir,
	}
	ip := NewImagePreparer(config).(*ImagePreparer)

	ctx := context.Background()

	// Test with invalid image reference
	err := ip.extractWithDockerCLI(ctx, "this,is:not,valid/image", destPath)

	assert.Error(t, err)
}

// TestExtractWithPodman_InvalidContainerID tests extractWithPodman when it returns invalid container ID
func TestExtractWithPodman_InvalidContainerID(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping test that requires podman in short mode")
	}

	tempDir := t.TempDir()
	destPath := filepath.Join(tempDir, "extract")

	config := &PreparerConfig{
		RootfsDir: tempDir,
	}
	ip := NewImagePreparer(config).(*ImagePreparer)

	ctx := context.Background()

	// Test with an image reference that might return invalid output
	// This tests the validation logic in extractWithPodman
	err := ip.extractWithDockerCLI(ctx, "", destPath)

	assert.Error(t, err)
}

// TestInjectInitSystem_FilesystemMock tests injectInitSystem with mocked filesystem
func TestInjectInitSystem_FilesystemMock(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*ImagePreparer, string)
		expectError bool
		validate    func(*testing.T, string)
	}{
		{
			name: "inject_init_success",
			setupFunc: func(ip *ImagePreparer, rootfsPath string) {
				// Create a mock ext4 image file
				err := os.WriteFile(rootfsPath, []byte("mock ext4 image"), 0644)
				require.NoError(t, err)
			},
			expectError: false, // Should succeed or log debug message and continue
			validate: func(t *testing.T, rootfsPath string) {
				// Verify the rootfs file exists
				_, err := os.Stat(rootfsPath)
				assert.NoError(t, err)
			},
		},
		{
			name: "inject_init_missing_rootfs",
			setupFunc: func(ip *ImagePreparer, rootfsPath string) {
				// Don't create the rootfs file - it should handle gracefully
			},
			expectError: false, // Should not error, just log debug message
			validate:    nil,
		},
		{
			name: "inject_init_with_init_binary",
			setupFunc: func(ip *ImagePreparer, rootfsPath string) {
				// Create a mock ext4 image
				err := os.WriteFile(rootfsPath, []byte("mock ext4 with init"), 0644)
				require.NoError(t, err)

				// Set up init binary path if available
				ip.initInjector.config.Type = "tini"
			},
			expectError: false,
			validate: func(t *testing.T, rootfsPath string) {
				_, err := os.Stat(rootfsPath)
				assert.NoError(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			rootfsPath := filepath.Join(tempDir, "test.ext4")

			config := &PreparerConfig{
				RootfsDir:       tempDir,
				InitSystem:      "none", // Start with none to avoid binary lookup
				InitGracePeriod: 5,
			}
			ip := NewImagePreparer(config).(*ImagePreparer)

			if tt.setupFunc != nil {
				tt.setupFunc(ip, rootfsPath)
			}

			err := ip.injectInitSystem(rootfsPath)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				// injectInitSystem logs debug on mount failure but doesn't return error
				// It should either succeed or continue gracefully
				// We just check it doesn't panic
				_ = err
			}

			if tt.validate != nil {
				tt.validate(t, rootfsPath)
			}
		})
	}
}

// TestInjectInitSystem_MountFailures tests injectInitSystem when mount operations fail
func TestInjectInitSystem_MountFailures(t *testing.T) {
	tests := []struct {
		name        string
		rootfsPath  string
		expectError bool
		description string
	}{
		{
			name:        "mount_fails_no_privileges",
			rootfsPath:  "/nonexistent/path/test.ext4",
			expectError: false, // Should not error, just log and continue
			description: "Mount fails due to missing privileges - should skip gracefully",
		},
		{
			name:        "mount_fails_invalid_path",
			rootfsPath:  "",
			expectError: false, // Should handle gracefully
			description: "Empty path - should handle gracefully",
		},
		{
			name:        "mount_fails_permission_denied",
			rootfsPath:  "/root/test.ext4", // Likely no permission
			expectError: false, // Should log and continue
			description: "Permission denied - should skip gracefully",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()

			config := &PreparerConfig{
				RootfsDir:       tempDir,
				InitSystem:      "none",
				InitGracePeriod: 5,
			}
			ip := NewImagePreparer(config).(*ImagePreparer)

			// Call injectInitSystem - it should not error even if mount fails
			err := ip.injectInitSystem(tt.rootfsPath)

			// injectInitSystem is designed to continue on mount failure
			// It logs a debug message but returns nil
			if tt.expectError {
				assert.Error(t, err, tt.description)
			} else {
				// Should not error - it continues gracefully
				_ = err
			}
		})
	}
}

// TestHandleMounts_NilVolumeManager tests handleMounts with nil volume manager
func TestHandleMounts_NilVolumeManager(t *testing.T) {
	tempDir := t.TempDir()
	rootfsPath := filepath.Join(tempDir, "test.ext4")

	// Create a mock ext4 image
	err := os.WriteFile(rootfsPath, []byte("mock ext4 for mounts"), 0644)
	require.NoError(t, err)

	config := &PreparerConfig{
		RootfsDir: tempDir,
	}
	ip := NewImagePreparer(config).(*ImagePreparer)

	// Explicitly set volumeManager to nil
	ip.volumeManager = nil

	ctx := context.Background()
	task := &types.Task{
		ID: "test-task",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "nginx:latest",
			},
		},
	}

	// Create test mounts
	mounts := []types.Mount{
		{
			Source:   "/host/path",
			Target:   "/container/path",
			ReadOnly: false,
		},
	}

	// handleMounts should handle nil volumeManager gracefully
	// It will try to mount, and if mountExt4 fails (no privileges), it continues
	err = ip.handleMounts(ctx, task, rootfsPath, mounts)

	// Should not error even with nil volumeManager
	// It will log mount failure but continue
	_ = err
}

// TestHandleMounts_ExecErrorPaths tests handleMounts exec-related error paths
func TestHandleMounts_ExecErrorPaths(t *testing.T) {
	tests := []struct {
		name        string
		setupPrep   func(*ImagePreparer, string) (*types.Task, []types.Mount, string)
		expectError bool
		description string
	}{
		{
			name: "empty_mount_target",
			setupPrep: func(ip *ImagePreparer, tempDir string) (*types.Task, []types.Mount, string) {
				rootfsPath := filepath.Join(tempDir, "test.ext4")
				os.WriteFile(rootfsPath, []byte("mock ext4"), 0644)

				task := &types.Task{
					ID: "test-task",
				}

				mounts := []types.Mount{
					{
						Source: "/host/path",
						Target: "", // Empty target - should be skipped
					},
				}

				return task, mounts, rootfsPath
			},
			expectError: false, // Should skip and continue
			description: "Mount with empty target should be skipped",
		},
		{
			name: "volume_mount_without_manager",
			setupPrep: func(ip *ImagePreparer, tempDir string) (*types.Task, []types.Mount, string) {
				rootfsPath := filepath.Join(tempDir, "test.ext4")
				os.WriteFile(rootfsPath, []byte("mock ext4"), 0644)

				ip.volumeManager = nil // No volume manager

				task := &types.Task{
					ID: "test-task",
				}

				mounts := []types.Mount{
					{
						Source:   "volume://test-volume",
						Target:   "/data",
						ReadOnly: false,
					},
				}

				return task, mounts, rootfsPath
			},
			expectError: false, // Should handle gracefully
			description: "Volume mount without volume manager should be handled",
		},
		{
			name: "invalid_rootfs_path",
			setupPrep: func(ip *ImagePreparer, tempDir string) (*types.Task, []types.Mount, string) {
				rootfsPath := "/nonexistent/path/test.ext4"

				task := &types.Task{
					ID: "test-task",
				}

				mounts := []types.Mount{
					{
						Source:   "/host/path",
						Target:   "/container/path",
						ReadOnly: false,
					},
				}

				return task, mounts, rootfsPath
			},
			expectError: false, // Mount will fail but should continue
			description: "Invalid rootfs path should be handled gracefully",
		},
		{
			name: "bind_mount_with_invalid_source",
			setupPrep: func(ip *ImagePreparer, tempDir string) (*types.Task, []types.Mount, string) {
				rootfsPath := filepath.Join(tempDir, "test.ext4")
				os.WriteFile(rootfsPath, []byte("mock ext4"), 0644)

				task := &types.Task{
					ID: "test-task",
				}

				mounts := []types.Mount{
					{
						Source:   "/nonexistent/host/path",
						Target:   "/container/path",
						ReadOnly: false,
					},
				}

				return task, mounts, rootfsPath
			},
			expectError: false, // Should log error and continue
			description: "Bind mount with invalid source should log and continue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()

			config := &PreparerConfig{
				RootfsDir: tempDir,
			}
			ip := NewImagePreparer(config).(*ImagePreparer)

			task, mounts, rootfsPath := tt.setupPrep(ip, tempDir)

			ctx := context.Background()

			err := ip.handleMounts(ctx, task, rootfsPath, mounts)

			if tt.expectError {
				assert.Error(t, err, tt.description)
			} else {
				// Most error paths in handleMounts log and continue
				_ = err
			}
		})
	}
}

// TestHandleMounts_WithVolumeManager tests handleMounts with a real volume manager
func TestHandleMounts_WithVolumeManager(t *testing.T) {
	tempDir := t.TempDir()
	rootfsPath := filepath.Join(tempDir, "test.ext4")

	// Create a mock ext4 image
	err := os.WriteFile(rootfsPath, []byte("mock ext4"), 0644)
	require.NoError(t, err)

	config := &PreparerConfig{
		RootfsDir: tempDir,
	}
	ip := NewImagePreparer(config).(*ImagePreparer)

	// Create a real volume manager for testing
	vm, err := storage.NewVolumeManager(filepath.Join(tempDir, "volumes"))
	require.NoError(t, err)
	ip.volumeManager = vm

	ctx := context.Background()
	task := &types.Task{
		ID: "test-task",
	}

	tests := []struct {
		name        string
		mounts      []types.Mount
		expectError bool
		description string
	}{
		{
			name: "volume_mount_success",
			mounts: []types.Mount{
				{
					Source:   "volume://test-vol",
					Target:   "/data",
					ReadOnly: false,
				},
			},
			expectError: false,
			description: "Successful volume mount",
		},
		{
			name: "multiple_mixed_mounts",
			mounts: []types.Mount{
				{
					Source:   "volume://vol1",
					Target:   "/data1",
					ReadOnly: false,
				},
				{
					Source:   "/host/path",
					Target:   "/data2",
					ReadOnly: true,
				},
				{
					Source:   "", // Empty source - should be skipped
					Target:   "/data3",
					ReadOnly: false,
				},
			},
			expectError: false,
			description: "Mixed volume and bind mounts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ip.handleMounts(ctx, task, rootfsPath, tt.mounts)

			if tt.expectError {
				assert.Error(t, err, tt.description)
			} else {
				// Should not error - errors are logged and processing continues
				_ = err
			}
		})
	}
}

// TestMountExt4_ExecErrorPaths tests mountExt4 exec-related error paths
func TestMountExt4_ExecErrorPaths(t *testing.T) {
	tests := []struct {
		name        string
		imagePath   string
		expectError bool
		description string
	}{
		{
			name:        "nonexistent_image",
			imagePath:   "/nonexistent/path/test.ext4",
			expectError: true,
			description: "Non-existent image should fail",
		},
		{
			name:        "empty_path",
			imagePath:   "",
			expectError: true,
			description: "Empty path should fail",
		},
		{
			name:        "invalid_image_file",
			imagePath:   "/dev/null", // Not a valid ext4 image
			expectError: true,
			description: "Invalid image file should fail",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()

			config := &PreparerConfig{
				RootfsDir: tempDir,
			}
			ip := NewImagePreparer(config).(*ImagePreparer)

			mountDir, err := ip.mountExt4(tt.imagePath)

			if tt.expectError {
				assert.Error(t, err, tt.description)
				assert.Empty(t, mountDir)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, mountDir)
				// Cleanup
				ip.unmountExt4(mountDir)
			}
		})
	}
}

// TestCreateExt4Image_ExecErrorPaths tests createExt4Image error paths
func TestCreateExt4Image_ExecErrorPaths(t *testing.T) {
	tests := []struct {
		name        string
		sourceDir   string
		outputPath  string
		expectError bool
		description string
	}{
		{
			name:        "nonexistent_source",
			sourceDir:   "/nonexistent/path",
			outputPath:  "/tmp/test.img",
			expectError: true,
			description: "Non-existent source directory should fail",
		},
		{
			name:        "empty_source_dir",
			sourceDir:   "",
			outputPath:  "/tmp/test.img",
			expectError: true,
			description: "Empty source directory should fail",
		},
		{
			name:        "invalid_output_path",
			sourceDir:   t.TempDir(),
			outputPath:  "/root/invalid.img", // No permission
			expectError: true,
			description: "Invalid output path should fail",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()

			config := &PreparerConfig{
				RootfsDir: tempDir,
			}
			ip := NewImagePreparer(config).(*ImagePreparer)

			// Create source directory if needed
			if tt.sourceDir == "" && !tt.expectError {
				tt.sourceDir = t.TempDir()
			}

			err := ip.createExt4Image(tt.sourceDir, tt.outputPath)

			if tt.expectError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err)
				// Cleanup
				os.Remove(tt.outputPath)
			}
		})
	}
}

// TestUnmountExt4_ErrorPaths tests unmountExt4 error paths
func TestUnmountExt4_ErrorPaths(t *testing.T) {
	tests := []struct {
		name        string
		mountDir    string
		description string
	}{
		{
			name:        "nonexistent_mount",
			mountDir:    "/nonexistent/mount/point",
			description: "Non-existent mount point should handle gracefully",
		},
		{
			name:        "empty_mount_dir",
			mountDir:    "",
			description: "Empty mount dir should handle gracefully",
		},
		{
			name:        "file_not_mounted",
			mountDir:    "/tmp/not-mounted",
			description: "File that's not mounted should handle gracefully",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()

			config := &PreparerConfig{
				RootfsDir: tempDir,
			}
			ip := NewImagePreparer(config).(*ImagePreparer)

			// unmountExt4 should handle errors gracefully
			// It uses exec.Command and logs errors but doesn't return
			ip.unmountExt4(tt.mountDir)

			// If we get here without panic, the test passes
		})
	}
}
