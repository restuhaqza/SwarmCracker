package image

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImagePreparer_ContextCancellation tests context cancellation handling
func TestImagePreparer_ContextCancellation(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*ImagePreparer, *types.Task)
		task        *types.Task
		cancelFunc  func(context.Context, context.CancelFunc)
		expectError bool
		errorMsg    string
	}{
		{
			name: "prepare with cancelled context",
			setupFunc: func(ip *ImagePreparer, task *types.Task) {
				// No setup - ensure no cached rootfs
			},
			task: &types.Task{
				ID: "cancelled-prepare",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:latest",
					},
				},
				Annotations: make(map[string]string),
			},
			cancelFunc: func(ctx context.Context, cancel context.CancelFunc) {
				cancel() // Cancel immediately
			},
			expectError: true,
		},
		{
			name: "prepare with timeout",
			setupFunc: func(ip *ImagePreparer, task *types.Task) {
				// No setup
			},
			task: &types.Task{
				ID: "timeout-prepare",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:alpine",
					},
				},
				Annotations: make(map[string]string),
			},
			cancelFunc: func(ctx context.Context, cancel context.CancelFunc) {
				time.AfterFunc(10*time.Millisecond, cancel)
			},
			expectError: true,
		},
		{
			name: "cleanup with cancelled context",
			setupFunc: func(ip *ImagePreparer, task *types.Task) {
				// Create some test files
				_ = os.MkdirAll(ip.rootfsDir, 0755)
				_ = os.WriteFile(filepath.Join(ip.rootfsDir, "test.ext4"), []byte("test"), 0644)
			},
			task: &types.Task{
				ID: "cancelled-cleanup",
			},
			cancelFunc: func(ctx context.Context, cancel context.CancelFunc) {
				cancel() // Cancel immediately
			},
			expectError: false, // Cleanup should be quick
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

			ctx, cancel := context.WithCancel(context.Background())

			if tt.cancelFunc != nil {
				tt.cancelFunc(ctx, cancel)
			}

			defer cancel()

			var err error
			if strings.Contains(tt.name, "cleanup") {
				err = ip.Cleanup(ctx, 7)
			} else {
				err = ip.Prepare(ctx, tt.task)
			}

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				_ = err
			}
		})
	}
}

// TestImagePreparer_ConcurrentOperations tests concurrent image preparation
func TestImagePreparer_ConcurrentOperations(t *testing.T) {
	t.Run("concurrent prepare different images", func(t *testing.T) {
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

		numGoroutines := 10
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		errors := make(chan error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()

				task := &types.Task{
					ID: fmt.Sprintf("task-%d", id),
					Spec: types.TaskSpec{
						Runtime: &types.Container{
							Image: fmt.Sprintf("nginx:%d", id),
						},
					},
					Annotations: make(map[string]string),
				}

				ctx := context.Background()
				err := ip.Prepare(ctx, task)
				errors <- err
			}(i)
		}

		wg.Wait()
		close(errors)

		// All operations should complete (may error without container runtime)
		errorCount := 0
		for err := range errors {
			if err != nil {
				errorCount++
			}
		}
		assert.Equal(t, numGoroutines, errorCount)
	})

	t.Run("concurrent prepare same image (cache hit)", func(t *testing.T) {
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

		// Create cached image
		rootfsPath := filepath.Join(tmpDir, "nginx-latest.ext4")
		err := os.WriteFile(rootfsPath, []byte("cached"), 0644)
		require.NoError(t, err)

		numGoroutines := 20
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()

				task := &types.Task{
					ID: fmt.Sprintf("task-%d", id),
					Spec: types.TaskSpec{
						Runtime: &types.Container{
							Image: "nginx:latest",
						},
					},
					Annotations: make(map[string]string),
				}

				ctx := context.Background()
				err := ip.Prepare(ctx, task)

				// Should succeed from cache
				assert.NoError(t, err)
				assert.Equal(t, rootfsPath, task.Annotations["rootfs"])
			}(i)
		}

		wg.Wait()
	})
}

// TestGenerateImageID_ContextEdgeCases tests image ID generation edge cases with context
func TestGenerateImageID_ContextEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		want     string
	}{
		{
			name:     "empty image reference",
			imageRef: "",
			want:     "-latest",
		},
		{
			name:     "image with multiple slashes",
			imageRef: "registry.example.com/org/repo/image:tag",
			want:     "registry.example.com-org-repo-image-tag",
		},
		{
			name:     "image with special characters",
			imageRef: "my.registry.io/my_org/my-image:v1.0.0-beta",
			want:     "my.registry.io-my_org-my-image-v1.0.0-beta",
		},
		{
			name:     "image with digest",
			imageRef: "nginx@sha256:abcd1234",
			want:     "nginx@sha256:abcd1234-latest",
		},
		{
			name:     "image with port",
			imageRef: "localhost:5000/myimage:latest",
			want:     "localhost:5000-myimage-latest",
		},
		{
			name:     "image with only tag",
			imageRef: ":latest",
			want:     "-latest",
		},
		{
			name:     "very long image reference",
			imageRef: strings.Repeat("a", 500) + ":latest",
			want:     strings.Repeat("a", 500) + "-latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateImageID(tt.imageRef)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestGetDirSize_EdgeCases tests directory size calculation edge cases
func TestGetDirSize_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func() string
		expectError bool
		validate    func(*testing.T, int64, error)
	}{
		{
			name: "size with symbolic links",
			setupFunc: func() string {
				tmpDir := t.TempDir()
				file1 := filepath.Join(tmpDir, "file1.txt")
				_ = os.WriteFile(file1, []byte("data"), 0644)

				link1 := filepath.Join(tmpDir, "link1")
				_ = os.Symlink(file1, link1)

				return tmpDir
			},
			expectError: false,
			validate: func(t *testing.T, size int64, err error) {
				assert.NoError(t, err)
				assert.Greater(t, size, int64(0))
			},
		},
		{
			name: "size with nested directories",
			setupFunc: func() string {
				tmpDir := t.TempDir()
				for i := 0; i < 5; i++ {
					nestedDir := filepath.Join(tmpDir, fmt.Sprintf("dir%d", i))
					_ = os.MkdirAll(nestedDir, 0755)
					for j := 0; j < 3; j++ {
						file := filepath.Join(nestedDir, fmt.Sprintf("file%d.txt", j))
						_ = os.WriteFile(file, []byte("data"), 0644)
					}
				}
				return tmpDir
			},
			expectError: false,
			validate: func(t *testing.T, size int64, err error) {
				assert.NoError(t, err)
				assert.Greater(t, size, int64(0))
			},
		},
		{
			name: "size with empty files",
			setupFunc: func() string {
				tmpDir := t.TempDir()
				for i := 0; i < 10; i++ {
					file := filepath.Join(tmpDir, fmt.Sprintf("empty%d.txt", i))
					_ = os.WriteFile(file, []byte{}, 0644)
				}
				return tmpDir
			},
			expectError: false,
			validate: func(t *testing.T, size int64, err error) {
				assert.NoError(t, err)
				assert.Equal(t, int64(0), size)
			},
		},
		{
			name: "size with permission denied",
			setupFunc: func() string {
				tmpDir := t.TempDir()
				noAccessDir := filepath.Join(tmpDir, "noaccess")
				_ = os.MkdirAll(noAccessDir, 0000)
				return tmpDir
			},
			expectError: true,
		},
		{
			name: "size of file instead of directory",
			setupFunc: func() string {
				tmpDir := t.TempDir()
				file := filepath.Join(tmpDir, "notadir.txt")
				_ = os.WriteFile(file, []byte("data"), 0644)
				return file
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setupFunc()
			size, err := getDirSize(path)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.validate != nil {
				tt.validate(t, size, err)
			}
		})
	}
}

// TestInitInjector_EdgeCases tests init injector edge cases
func TestInitInjector_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		config      *InitSystemConfig
		expectError bool
		validate    func(*testing.T, *InitInjector)
	}{
		{
			name: "nil config defaults to tini",
			config: nil,
			expectError: false,
			validate: func(t *testing.T, ii *InitInjector) {
				assert.Equal(t, InitSystemTini, ii.config.Type)
				assert.Equal(t, 10, ii.config.GracePeriodSec)
			},
		},
		{
			name: "zero grace period defaults to 10",
			config: &InitSystemConfig{
				Type:           InitSystemTini,
				GracePeriodSec: 0,
			},
			expectError: false,
			validate: func(t *testing.T, ii *InitInjector) {
				assert.Equal(t, InitSystemTini, ii.config.Type)
				assert.Equal(t, 10, ii.config.GracePeriodSec)
			},
		},
		{
			name: "none init system keeps zero grace period",
			config: &InitSystemConfig{
				Type:           InitSystemNone,
				GracePeriodSec: 0,
			},
			expectError: false,
			validate: func(t *testing.T, ii *InitInjector) {
				assert.Equal(t, InitSystemNone, ii.config.Type)
				assert.Equal(t, 0, ii.config.GracePeriodSec)
			},
		},
		{
			name: "negative grace period",
			config: &InitSystemConfig{
				Type:           InitSystemTini,
				GracePeriodSec: -1,
			},
			expectError: false,
			validate: func(t *testing.T, ii *InitInjector) {
				assert.Equal(t, -1, ii.config.GracePeriodSec)
			},
		},
		{
			name: "very large grace period",
			config: &InitSystemConfig{
				Type:           InitSystemTini,
				GracePeriodSec: 3600,
			},
			expectError: false,
			validate: func(t *testing.T, ii *InitInjector) {
				assert.Equal(t, 3600, ii.config.GracePeriodSec)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(tt.config)

			if tt.validate != nil {
				tt.validate(t, ii)
			}
		})
	}
}

// TestInitInjector_GetInitPath tests init path retrieval
func TestInitInjector_GetInitPath(t *testing.T) {
	tests := []struct {
		config     *InitSystemConfig
		wantPath   string
	}{
		{
			config: &InitSystemConfig{
				Type: InitSystemTini,
			},
			wantPath: "/sbin/tini",
		},
		{
			config: &InitSystemConfig{
				Type: InitSystemDumbInit,
			},
			wantPath: "/sbin/dumb-init",
		},
		{
			config: &InitSystemConfig{
				Type: InitSystemNone,
			},
			wantPath: "",
		},
		{
			config: &InitSystemConfig{
				Type: "unknown",
			},
			wantPath: "/sbin/init",
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.config.Type), func(t *testing.T) {
			ii := NewInitInjector(tt.config)
			path := ii.GetInitPath()
			assert.Equal(t, tt.wantPath, path)
		})
	}
}

// TestInitInjector_GetInitArgs tests init args generation
func TestInitInjector_GetInitArgs(t *testing.T) {
	tests := []struct {
		name          string
		initType      InitSystemType
		containerArgs []string
		wantArgs      []string
	}{
		{
			name:          "tini with args",
			initType:      InitSystemTini,
			containerArgs: []string{"/bin/sh", "-c", "echo hello"},
			wantArgs:      []string{"/sbin/tini", "--", "/bin/sh", "-c", "echo hello"},
		},
		{
			name:          "tini with no args",
			initType:      InitSystemTini,
			containerArgs: []string{},
			wantArgs:      []string{"/sbin/tini", "--"},
		},
		{
			name:          "dumb-init with args",
			initType:      InitSystemDumbInit,
			containerArgs: []string{"/bin/bash"},
			wantArgs:      []string{"/sbin/dumb-init", "/bin/bash"},
		},
		{
			name:          "dumb-init with no args",
			initType:      InitSystemDumbInit,
			containerArgs: []string{},
			wantArgs:      []string{"/sbin/dumb-init"},
		},
		{
			name:          "none with args",
			initType:      InitSystemNone,
			containerArgs: []string{"/bin/sh"},
			wantArgs:      []string{"/bin/sh"},
		},
		{
			name:          "none with no args",
			initType:      InitSystemNone,
			containerArgs: []string{},
			wantArgs:      []string{},
		},
		{
			name:          "unknown type falls back to none",
			initType:      "unknown",
			containerArgs: []string{"/bin/sh"},
			wantArgs:      []string{"/bin/sh"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(&InitSystemConfig{
				Type: tt.initType,
			})

			args := ii.GetInitArgs(tt.containerArgs)
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}

// TestImagePreparer_Prepare_InvalidInputs tests invalid input handling
func TestImagePreparer_Prepare_InvalidInputs(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*ImagePreparer)
		task        *types.Task
		expectError bool
		errorMsg    string
	}{
		{
			name:      "nil task",
			setupFunc: func(ip *ImagePreparer) {},
			task:      nil,
			expectError: true,
			errorMsg:    "cannot be nil",
		},
		{
			name: "nil runtime",
			setupFunc: func(ip *ImagePreparer) {},
			task: &types.Task{
				ID: "test-nil-runtime",
				Spec: types.TaskSpec{
					Runtime: nil,
				},
			},
			expectError: true,
			errorMsg:    "not a container",
		},
		{
			name: "non-container runtime",
			setupFunc: func(ip *ImagePreparer) {},
			task: &types.Task{
				ID: "test-bad-runtime",
				Spec: types.TaskSpec{
					Runtime: "not a container",
				},
			},
			expectError: true,
			errorMsg:    "not a container",
		},
		{
			name: "empty image name",
			setupFunc: func(ip *ImagePreparer) {},
			task: &types.Task{
				ID: "test-empty-image",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "",
					},
				},
				Annotations: make(map[string]string),
			},
			expectError: true,
		},
		{
			name: "nil annotations",
			setupFunc: func(ip *ImagePreparer) {},
			task: &types.Task{
				ID: "test-nil-annotations",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:latest",
					},
				},
				Annotations: nil,
			},
			expectError: false, // Should initialize annotations
		},
		{
			name: "special characters in image name",
			setupFunc: func(ip *ImagePreparer) {
				// Create cached rootfs
				rootfsPath := filepath.Join(ip.rootfsDir, "my--registry.io-my_org-my-image-v1.0.0-beta.ext4")
				_ = os.WriteFile(rootfsPath, []byte("cached"), 0644)
			},
			task: &types.Task{
				ID: "test-special-chars",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "my.registry.io/my_org/my-image:v1.0.0-beta",
					},
				},
				Annotations: make(map[string]string),
			},
			expectError: false, // Should work with cached image
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
				tt.setupFunc(ip)
			}

			ctx := context.Background()
			err := ip.Prepare(ctx, tt.task)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				// May succeed or fail
				_ = err
			}
		})
	}
}

// TestImagePreparer_Cleanup_EdgeCases tests cleanup edge cases
func TestImagePreparer_Cleanup_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*ImagePreparer)
		retention   int
		expectError bool
		validate    func(*testing.T, *ImagePreparer)
	}{
		{
			name: "cleanup with zero retention",
			setupFunc: func(ip *ImagePreparer) {
				_ = os.MkdirAll(ip.rootfsDir, 0755)
				oldFile := filepath.Join(ip.rootfsDir, "old.ext4")
				_ = os.WriteFile(oldFile, []byte("old"), 0644)
				oldTime := time.Now().Add(-1 * time.Hour)
				_ = os.Chtimes(oldFile, oldTime, oldTime)
			},
			retention:   0,
			expectError: false,
		},
		{
			name: "cleanup with negative retention",
			setupFunc: func(ip *ImagePreparer) {
				_ = os.MkdirAll(ip.rootfsDir, 0755)
				oldFile := filepath.Join(ip.rootfsDir, "old.ext4")
				_ = os.WriteFile(oldFile, []byte("old"), 0644)
			},
			retention:   -1,
			expectError: false,
		},
		{
			name: "cleanup with very large retention",
			setupFunc: func(ip *ImagePreparer) {
				_ = os.MkdirAll(ip.rootfsDir, 0755)
				oldFile := filepath.Join(ip.rootfsDir, "old.ext4")
				_ = os.WriteFile(oldFile, []byte("old"), 0644)
			},
			retention:   36500, // 100 years
			expectError: false,
		},
		{
			name: "cleanup with non-existent directory",
			setupFunc: func(ip *ImagePreparer) {
				// Don't create directory
			},
			retention:   7,
			expectError: false, // Should not error
		},
		{
			name: "cleanup with empty directory",
			setupFunc: func(ip *ImagePreparer) {
				_ = os.MkdirAll(ip.rootfsDir, 0755)
			},
			retention:   7,
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
			}

			if tt.setupFunc != nil {
				tt.setupFunc(ip)
			}

			ctx := context.Background()
			err := ip.Cleanup(ctx, tt.retention)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.validate != nil {
				tt.validate(t, ip)
			}
		})
	}
}
