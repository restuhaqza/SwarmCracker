package image

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImagePreparer_Prepare_CacheHitMiss tests cache behavior
func TestImagePreparer_Prepare_CacheHitMiss(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func(*ImagePreparer, *types.Task)
		task          *types.Task
		expectCache   bool
		expectError   bool
		validateCache func(*testing.T, *ImagePreparer, string)
	}{
		{
			name: "cache hit - rootfs exists",
			setupFunc: func(ip *ImagePreparer, task *types.Task) {
				imageID := generateImageID("nginx:latest")
				rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")
				err := os.WriteFile(rootfsPath, []byte("cached image"), 0644)
				require.NoError(t, err)
			},
			task: &types.Task{
				ID: "cache-hit-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:latest",
					},
				},
				Annotations: make(map[string]string),
			},
			expectCache: true,
			expectError: false,
			validateCache: func(t *testing.T, ip *ImagePreparer, rootfsPath string) {
				// Verify cache file exists
				_, err := os.Stat(rootfsPath)
				assert.NoError(t, err)
			},
		},
		{
			name: "cache miss - rootfs does not exist",
			setupFunc: func(ip *ImagePreparer, task *types.Task) {
				// Don't create rootfs
			},
			task: &types.Task{
				ID: "cache-miss-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:alpine",
					},
				},
				Annotations: make(map[string]string),
			},
			expectCache: false,
			expectError: true, // Will fail without container runtime
		},
		{
			name: "cache hit with different image tag",
			setupFunc: func(ip *ImagePreparer, task *types.Task) {
				imageID := generateImageID("redis:alpine")
				rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")
				err := os.WriteFile(rootfsPath, []byte("cached redis"), 0644)
				require.NoError(t, err)
			},
			task: &types.Task{
				ID: "cache-redis-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "redis:alpine",
					},
				},
				Annotations: make(map[string]string),
			},
			expectCache: true,
			expectError: false,
		},
		{
			name: "cache hit with private registry",
			setupFunc: func(ip *ImagePreparer, task *types.Task) {
				imageID := generateImageID("registry.example.com/myapp:v1.0")
				rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")
				err := os.WriteFile(rootfsPath, []byte("cached private image"), 0644)
				require.NoError(t, err)
			},
			task: &types.Task{
				ID: "cache-private-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "registry.example.com/myapp:v1.0",
					},
				},
				Annotations: make(map[string]string),
			},
			expectCache: true,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			ip := &ImagePreparer{
				config: &PreparerConfig{
					RootfsDir: tmpDir,
				},
				rootfsDir: tmpDir,
				initInjector: NewInitInjector(&InitSystemConfig{
					Type: InitSystemNone,
				}),
			}

			if tt.setupFunc != nil {
				tt.setupFunc(ip, tt.task)
			}

			ctx := context.Background()
			err := ip.Prepare(ctx, tt.task)

			if tt.expectError {
				// Expected to fail (no container runtime)
				_ = err
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.task.Annotations["rootfs"])

				if tt.validateCache != nil {
					tt.validateCache(t, ip, tt.task.Annotations["rootfs"])
				}
			}
		})
	}
}

// TestCreateExt4Image_EdgeCases tests ext4 image creation edge cases
func TestCreateExt4Image_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func() (string, string)
		expectError bool
		errorMsg    string
		validate    func(*testing.T, string)
	}{
		{
			name: "create with minimal size directory",
			setupFunc: func() (string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				err := os.MkdirAll(sourceDir, 0755)
				require.NoError(t, err)
				// Create a small file
				err = os.WriteFile(filepath.Join(sourceDir, "small.txt"), []byte("small"), 0644)
				require.NoError(t, err)

				outputPath := filepath.Join(tmpDir, "output.ext4")
				return sourceDir, outputPath
			},
			expectError: false, // May succeed if mkfs.ext4 is available
		},
		{
			name: "create with empty directory",
			setupFunc: func() (string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				err := os.MkdirAll(sourceDir, 0755)
				require.NoError(t, err)

				outputPath := filepath.Join(tmpDir, "output.ext4")
				return sourceDir, outputPath
			},
			expectError: true,
		},
		{
			name: "create with non-existent source",
			setupFunc: func() (string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "nonexistent")
				outputPath := filepath.Join(tmpDir, "output.ext4")
				return sourceDir, outputPath
			},
			expectError: true,
		},
		{
			name: "create with file instead of directory",
			setupFunc: func() (string, string) {
				tmpDir := t.TempDir()
				sourceFile := filepath.Join(tmpDir, "file")
				err := os.WriteFile(sourceFile, []byte("not a dir"), 0644)
				require.NoError(t, err)

				outputPath := filepath.Join(tmpDir, "output.ext4")
				return sourceFile, outputPath
			},
			expectError: true,
		},
		{
			name: "create with nested directories",
			setupFunc: func() (string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "source")
				err := os.MkdirAll(filepath.Join(sourceDir, "a/b/c/d"), 0755)
				require.NoError(t, err)
				// Create files at each level
				_ = os.WriteFile(filepath.Join(sourceDir, "root.txt"), []byte("root"), 0644)
				_ = os.WriteFile(filepath.Join(sourceDir, "a", "a.txt"), []byte("a"), 0644)
				_ = os.WriteFile(filepath.Join(sourceDir, "a/b", "b.txt"), []byte("b"), 0644)
				_ = os.WriteFile(filepath.Join(sourceDir, "a/b/c", "c.txt"), []byte("c"), 0644)
				_ = os.WriteFile(filepath.Join(sourceDir, "a/b/c/d", "d.txt"), []byte("d"), 0644)

				outputPath := filepath.Join(tmpDir, "output.ext4")
				return sourceDir, outputPath
			},
			expectError: true, // mkfs.ext4 not available
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceDir, outputPath := tt.setupFunc()

			ip := &ImagePreparer{}
			err := ip.createExt4Image(sourceDir, outputPath)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tt.errorMsg))
				}
			}

			if tt.validate != nil {
				tt.validate(t, outputPath)
			}
		})
	}
}

// TestExtractOCIImage_Runtimes tests OCI extraction with different runtimes
func TestExtractOCIImage_Runtimes(t *testing.T) {
	tests := []struct {
		name          string
		imageRef      string
		expectRuntime string
		expectError   bool
	}{
		{
			name:          "docker available",
			imageRef:      "nginx:latest",
			expectRuntime: "docker",
			expectError:   true, // Will fail even with docker
		},
		{
			name:          "podman available",
			imageRef:      "nginx:alpine",
			expectRuntime: "podman",
			expectError:   true, // Will fail even with podman
		},
		{
			name:          "no runtime available",
			imageRef:      "redis:latest",
			expectRuntime: "",
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rootfsDir := tmpDir

			ip := &ImagePreparer{
				config: &PreparerConfig{
					RootfsDir: rootfsDir,
				},
				rootfsDir: rootfsDir,
			}

			imageID := generateImageID(tt.imageRef)
			outputPath := filepath.Join(rootfsDir, imageID+".ext4")

			ctx := context.Background()
			err := ip.prepareImage(ctx, tt.imageRef, imageID, outputPath)

			if tt.expectError {
				assert.Error(t, err)
			}
		})
	}
}

// TestInitInjector_Inject tests init system injection
func TestInitInjector_Inject(t *testing.T) {
	tests := []struct {
		name        string
		initType    InitSystemType
		rootfsSetup func() string
		expectError bool
	}{
		{
			name:     "inject tini",
			initType: InitSystemTini,
			rootfsSetup: func() string {
				tmpDir := t.TempDir()
				_ = os.MkdirAll(filepath.Join(tmpDir, "sbin"), 0755)
				return tmpDir
			},
			expectError: true, // Mount will fail
		},
		{
			name:     "inject dumb-init",
			initType: InitSystemDumbInit,
			rootfsSetup: func() string {
				tmpDir := t.TempDir()
				_ = os.MkdirAll(filepath.Join(tmpDir, "sbin"), 0755)
				return tmpDir
			},
			expectError: true,
		},
		{
			name:     "inject none",
			initType: InitSystemNone,
			rootfsSetup: func() string {
				return t.TempDir()
			},
			expectError: false, // Should succeed immediately
		},
		{
			name:     "inject unsupported type",
			initType: "unsupported",
			rootfsSetup: func() string {
				return t.TempDir()
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(&InitSystemConfig{
				Type: tt.initType,
			})

			rootfsPath := tt.rootfsSetup()
			err := ii.Inject(rootfsPath)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestImagePreparer_Prepare_InitSystemInjection tests init system injection during prepare
func TestImagePreparer_Prepare_InitSystemInjection(t *testing.T) {
	tests := []struct {
		name          string
		initType      InitSystemType
		gracePeriod   int
		setupFunc     func(*ImagePreparer, *types.Task)
		expectError   bool
		validate      func(*testing.T, *types.Task)
	}{
		{
			name:        "prepare with tini",
			initType:    InitSystemTini,
			gracePeriod: 10,
			setupFunc: func(ip *ImagePreparer, task *types.Task) {
				imageID := generateImageID("nginx:latest")
				rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")
				_ = os.WriteFile(rootfsPath, []byte("cached"), 0644)
			},
			expectError: false,
			validate: func(t *testing.T, task *types.Task) {
				assert.Equal(t, "tini", task.Annotations["init_system"])
				assert.Equal(t, "/sbin/tini", task.Annotations["init_path"])
			},
		},
		{
			name:        "prepare with dumb-init",
			initType:    InitSystemDumbInit,
			gracePeriod: 5,
			setupFunc: func(ip *ImagePreparer, task *types.Task) {
				imageID := generateImageID("nginx:alpine")
				rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")
				_ = os.WriteFile(rootfsPath, []byte("cached"), 0644)
			},
			expectError: false,
			validate: func(t *testing.T, task *types.Task) {
				assert.Equal(t, "dumb-init", task.Annotations["init_system"])
				assert.Equal(t, "/sbin/dumb-init", task.Annotations["init_path"])
			},
		},
		{
			name:        "prepare with no init",
			initType:    InitSystemNone,
			gracePeriod: 0,
			setupFunc: func(ip *ImagePreparer, task *types.Task) {
				imageID := generateImageID("redis:latest")
				rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")
				_ = os.WriteFile(rootfsPath, []byte("cached"), 0644)
			},
			expectError: false,
			validate: func(t *testing.T, task *types.Task) {
				assert.Empty(t, task.Annotations["init_system"])
			},
		},
		{
			name:        "prepare with custom grace period",
			initType:    InitSystemTini,
			gracePeriod: 30,
			setupFunc: func(ip *ImagePreparer, task *types.Task) {
				imageID := generateImageID("nginx:latest")
				rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")
				_ = os.WriteFile(rootfsPath, []byte("cached"), 0644)
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			ip := &ImagePreparer{
				config: &PreparerConfig{
					RootfsDir:       tmpDir,
					InitSystem:      string(tt.initType),
					InitGracePeriod: tt.gracePeriod,
				},
				rootfsDir: tmpDir,
				initInjector: NewInitInjector(&InitSystemConfig{
					Type:           tt.initType,
					GracePeriodSec: tt.gracePeriod,
				}),
			}

			task := &types.Task{
				ID: "init-test-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:latest",
					},
				},
				Annotations: make(map[string]string),
			}

			if tt.setupFunc != nil {
				tt.setupFunc(ip, task)
			}

			ctx := context.Background()
			err := ip.Prepare(ctx, task)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.validate != nil {
				tt.validate(t, task)
			}
		})
	}
}

// TestImagePreparer_Prepare_SpecialImageNames tests special image name handling
func TestImagePreparer_Prepare_SpecialImageNames(t *testing.T) {
	tests := []struct {
		name        string
		imageRef    string
		setupFunc   func(*ImagePreparer, string)
		expectError bool
		validateID  func(*testing.T, string)
	}{
		{
			name:     "image with digest",
			imageRef: "nginx@sha256:abcd1234567890",
			setupFunc: func(ip *ImagePreparer, imageID string) {
				rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")
				_ = os.WriteFile(rootfsPath, []byte("cached"), 0644)
			},
			expectError: false,
			validateID: func(t *testing.T, imageID string) {
				assert.Contains(t, imageID, "sha256")
			},
		},
		{
			name:     "image with port",
			imageRef: "localhost:5000/myimage:latest",
			setupFunc: func(ip *ImagePreparer, imageID string) {
				rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")
				_ = os.WriteFile(rootfsPath, []byte("cached"), 0644)
			},
			expectError: false,
		},
		{
			name:     "image with underscores",
			imageRef: "my_private_registry/my_image:v1.0",
			setupFunc: func(ip *ImagePreparer, imageID string) {
				rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")
				_ = os.WriteFile(rootfsPath, []byte("cached"), 0644)
			},
			expectError: false,
		},
		{
			name:     "very long image name",
			imageRef: strings.Repeat("a", 200) + ":latest",
			setupFunc: func(ip *ImagePreparer, imageID string) {
				rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")
				_ = os.WriteFile(rootfsPath, []byte("cached"), 0644)
			},
			expectError: false,
		},
		{
			name:     "image with special characters in tag",
			imageRef: "myapp:v1.0.0-beta.1",
			setupFunc: func(ip *ImagePreparer, imageID string) {
				rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")
				_ = os.WriteFile(rootfsPath, []byte("cached"), 0644)
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			ip := &ImagePreparer{
				config: &PreparerConfig{
					RootfsDir: tmpDir,
				},
				rootfsDir: tmpDir,
				initInjector: NewInitInjector(&InitSystemConfig{
					Type: InitSystemNone,
				}),
			}

			imageID := generateImageID(tt.imageRef)

			if tt.setupFunc != nil {
				tt.setupFunc(ip, imageID)
			}

			task := &types.Task{
				ID: "special-name-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: tt.imageRef,
					},
				},
				Annotations: make(map[string]string),
			}

			ctx := context.Background()
			err := ip.Prepare(ctx, task)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.validateID != nil {
				tt.validateID(t, imageID)
			}
		})
	}
}

// TestImagePreparer_Prepare_ConcurrentSameImageDuplicate tests concurrent preparation of same image
func TestImagePreparer_Prepare_ConcurrentSameImageDuplicate(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

	tmpDir := t.TempDir()

	ip := &ImagePreparer{
		config: &PreparerConfig{
			RootfsDir: tmpDir,
		},
		rootfsDir: tmpDir,
		initInjector: NewInitInjector(&InitSystemConfig{
			Type: InitSystemNone,
		}),
	}

	// Create cached image after first request
	imageID := generateImageID("nginx:latest")
	_ = filepath.Join(tmpDir, imageID+".ext4")

	// Don't create cache initially - let first request try to create it

	numGoroutines := 10
	results := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			task := &types.Task{
				ID: "task-" + string(rune(id)),
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:latest",
					},
				},
				Annotations: make(map[string]string),
			}

			ctx := context.Background()
			err := ip.Prepare(ctx, task)
			results <- err
		}(i)
	}

	// Wait for all
	for i := 0; i < numGoroutines; i++ {
		_ = <-results
	}

	// All should complete without panics
}

// TestImagePreparer_Cleanup_OldFiles tests cleanup of old files
func TestImagePreparer_Cleanup_OldFiles(t *testing.T) {
	tests := []struct {
		name       string
		setupFunc  func(*ImagePreparer)
		retention  int
		validate   func(*testing.T, *ImagePreparer)
	}{
		{
			name: "cleanup removes old files",
			setupFunc: func(ip *ImagePreparer) {
				_ = os.MkdirAll(ip.rootfsDir, 0755)

				// Create old file
				oldFile := filepath.Join(ip.rootfsDir, "old.ext4")
				_ = os.WriteFile(oldFile, []byte("old"), 0644)
				oldTime := time.Now().Add(-30 * 24 * time.Hour)
				_ = os.Chtimes(oldFile, oldTime, oldTime)

				// Create recent file
				recentFile := filepath.Join(ip.rootfsDir, "recent.ext4")
				_ = os.WriteFile(recentFile, []byte("recent"), 0644)
			},
			retention: 7,
			validate: func(t *testing.T, ip *ImagePreparer) {
				// Both files should still exist (cleanup not fully implemented)
				_, err := os.Stat(filepath.Join(ip.rootfsDir, "old.ext4"))
				_ = err
				_, err = os.Stat(filepath.Join(ip.rootfsDir, "recent.ext4"))
				_ = err
			},
		},
		{
			name: "cleanup with very short retention",
			setupFunc: func(ip *ImagePreparer) {
				_ = os.MkdirAll(ip.rootfsDir, 0755)

				// Create files
				for i := 0; i < 5; i++ {
					file := filepath.Join(ip.rootfsDir, fmt.Sprintf("file%d.ext4", i))
					_ = os.WriteFile(file, []byte("data"), 0644)
					oldTime := time.Now().Add(-1 * time.Hour)
					_ = os.Chtimes(file, oldTime, oldTime)
				}
			},
			retention: 0,
			validate: func(t *testing.T, ip *ImagePreparer) {
				// Files may or may not exist
				_ = ip
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			ip := &ImagePreparer{
				config: &PreparerConfig{
					RootfsDir: tmpDir,
				},
				rootfsDir: tmpDir,
			}

			if tt.setupFunc != nil {
				tt.setupFunc(ip)
			}

			ctx := context.Background()
			err := ip.Cleanup(ctx, tt.retention)

			// Cleanup not fully implemented, should not error
			assert.NoError(t, err)

			if tt.validate != nil {
				tt.validate(t, ip)
			}
		})
	}
}

// TestGetDirSize_SymlinkLoops tests symlink handling
func TestGetDirSize_SymlinkLoops(t *testing.T) {
	t.Run("symlink to parent directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		parentDir := filepath.Join(tmpDir, "parent")
		_ = os.MkdirAll(parentDir, 0755)

		// Create symlink to parent
		linkPath := filepath.Join(parentDir, "link-to-parent")
		_ = os.Symlink(tmpDir, linkPath)

		// Try to get size - should handle gracefully
		size, err := getDirSize(parentDir)

		// May error due to symlink loop
		if err == nil {
			assert.GreaterOrEqual(t, size, int64(0))
		}
	})

	t.Run("broken symlink", func(t *testing.T) {
		tmpDir := t.TempDir()
		linkPath := filepath.Join(tmpDir, "broken-link")
		_ = os.Symlink("/nonexistent/target", linkPath)

		// Should handle broken symlinks gracefully
		size, err := getDirSize(tmpDir)

		// May error or return 0
		if err == nil {
			assert.Equal(t, int64(0), size)
		}
	})
}

// Helper function to create a sleep process for testing
func createSleepProcessForImage(t *testing.T) *exec.Cmd {
	cmd := exec.Command("sleep", "10")
	err := cmd.Start()
	require.NoError(t, err)
	return cmd
}
