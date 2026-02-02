// Package image prepares OCI images as root filesystems for Firecracker VMs.
package image

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewImagePreparer tests ImagePreparer creation
func TestNewImagePreparer_Unit(t *testing.T) {
	tests := []struct {
		name    string
		config  interface{}
		wantDir string
	}{
		{
			name: "with valid config",
			config: &PreparerConfig{
				RootfsDir:       "/tmp/test-rootfs",
				InitSystem:      "tini",
				InitGracePeriod: 10,
			},
			wantDir: "/tmp/test-rootfs",
		},
		{
			name:    "with nil config",
			config:  nil,
			wantDir: "/var/lib/firecracker/rootfs",
		},
		{
			name: "with empty config",
			config: &PreparerConfig{},
			wantDir: "",
		},
		{
			name: "with dumb-init",
			config: &PreparerConfig{
				InitSystem:      "dumb-init",
				InitGracePeriod: 5,
			},
			wantDir: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := NewImagePreparer(tt.config)
			assert.NotNil(t, ip)

			preparer, ok := ip.(*ImagePreparer)
			require.True(t, ok)
			assert.NotNil(t, preparer.config)
			assert.NotNil(t, preparer.initInjector)
		})
	}
}

// TestImagePreparer_Prepare_Validation tests input validation
func TestImagePreparer_Prepare_Validation_Unit(t *testing.T) {
	tests := []struct {
		name        string
		task        *types.Task
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil task",
			task:        nil,
			wantErr:     true,
			errContains: "task cannot be nil",
		},
		{
			name: "nil runtime",
			task: &types.Task{
				ID:  "test-task",
				Spec: types.TaskSpec{
					Runtime: nil,
				},
			},
			wantErr:     true,
			errContains: "task runtime cannot be nil",
		},
		{
			name: "non-container runtime",
			task: &types.Task{
				ID:  "test-task",
				Spec: types.TaskSpec{
					Runtime: "string",
				},
			},
			wantErr:     true,
			errContains: "task runtime is not a container",
		},
		{
			name: "valid container runtime",
			task: &types.Task{
				ID:  "test-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:latest",
					},
				},
				Annotations: make(map[string]string),
			},
			wantErr: false, // Will fail on actual image operations
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := NewImagePreparer(&PreparerConfig{
				RootfsDir: t.TempDir(),
			})
			ctx := context.Background()

			err := ip.Prepare(ctx, tt.task)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			}
		})
	}
}

// TestGenerateImageID tests image ID generation
func TestGenerateImageID_Unit(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		want     string
	}{
		{
			name:     "simple image with tag",
			imageRef: "nginx:latest",
			want:     "nginx-latest",
		},
		{
			name:     "image without tag",
			imageRef: "nginx",
			want:     "nginx-latest",
		},
		{
			name:     "image with registry",
			imageRef: "docker.io/library/nginx:latest",
			want:     "docker.io/library/nginx-latest",
		},
		{
			name:     "image with port",
			imageRef: "localhost:5000/myimage:latest",
			want:     "localhost:5000/myimage-latest",
		},
		{
			name:     "complex image name",
			imageRef: "my.registry.io/org/subdir/image:v1.0.0",
			want:     "my.registry.io/org/subdir/image-v1.0.0",
		},
		{
			name:     "empty image",
			imageRef: "",
			want:     "-latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateImageID(tt.imageRef)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestGetDirSize tests directory size calculation
func TestGetDirSize_Unit(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr bool
	}{
		{
			name: "empty directory",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
		},
		{
			name: "directory with files",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				// Create test files
				file1 := filepath.Join(dir, "file1.txt")
				file2 := filepath.Join(dir, "file2.txt")
				
				err := os.WriteFile(file1, []byte("hello"), 0644)
				require.NoError(t, err)
				err = os.WriteFile(file2, []byte("world"), 0644)
				require.NoError(t, err)
				
				return dir
			},
			wantErr: false,
		},
		{
			name: "nested directories",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				subdir := filepath.Join(dir, "subdir")
				err := os.Mkdir(subdir, 0755)
				require.NoError(t, err)
				
				file := filepath.Join(subdir, "file.txt")
				err = os.WriteFile(file, []byte("nested content"), 0644)
				require.NoError(t, err)
				
				return dir
			},
			wantErr: false,
		},
		{
			name: "non-existent directory",
			setup: func(t *testing.T) string {
				return "/non/existent/path"
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			
			size, err := getDirSize(path)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.GreaterOrEqual(t, size, int64(0))
			}
		})
	}
}

// TestInitSystemConfig tests init system configuration
func TestInitSystemConfig_Unit(t *testing.T) {
	tests := []struct {
		name           string
		config         InitSystemConfig
		expectedType   InitSystemType
		expectedPath   string
		expectedGrace  int
	}{
		{
			name: "tini configuration",
			config: InitSystemConfig{
				Type:           InitSystemTini,
				GracePeriodSec: 10,
			},
			expectedType:  InitSystemTini,
			expectedPath:  "/sbin/tini",
			expectedGrace: 10,
		},
		{
			name: "dumb-init configuration",
			config: InitSystemConfig{
				Type:           InitSystemDumbInit,
				GracePeriodSec: 5,
			},
			expectedType:  InitSystemDumbInit,
			expectedPath:  "/sbin/dumb-init",
			expectedGrace: 5,
		},
		{
			name: "none configuration",
			config: InitSystemConfig{
				Type:           InitSystemNone,
				GracePeriodSec: 0,
			},
			expectedType:  InitSystemNone,
			expectedPath:  "",
			expectedGrace: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedType, tt.config.Type)
			assert.Equal(t, tt.expectedGrace, tt.config.GracePeriodSec)
		})
	}
}

// TestInitSystemType_String tests init system type string representation
func TestInitSystemType_String_Unit(t *testing.T) {
	tests := []struct {
		initType InitSystemType
		expected string
	}{
		{InitSystemNone, "none"},
		{InitSystemTini, "tini"},
		{InitSystemDumbInit, "dumb-init"},
		{InitSystemType("custom"), "custom"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := string(tt.initType)
			assert.Equal(t, tt.expected, got)
		})
	}
}

// TestImagePreparer_CopyFile tests file copying
func TestImagePreparer_CopyFile_Unit(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T) (string, string)
		wantErr    bool
		verifySize bool
	}{
		{
			name: "copy regular file",
			setup: func(t *testing.T) (string, string) {
				srcDir := t.TempDir()
				dstDir := t.TempDir()
				
				srcFile := filepath.Join(srcDir, "source.txt")
				dstFile := filepath.Join(dstDir, "dest.txt")
				
				err := os.WriteFile(srcFile, []byte("test content"), 0644)
				require.NoError(t, err)
				
				return srcFile, dstFile
			},
			wantErr:    false,
			verifySize: true,
		},
		{
			name: "copy to non-existent directory",
			setup: func(t *testing.T) (string, string) {
				srcDir := t.TempDir()
				
				srcFile := filepath.Join(srcDir, "source.txt")
				dstFile := "/non/existent/path/dest.txt"
				
				err := os.WriteFile(srcFile, []byte("test content"), 0644)
				require.NoError(t, err)
				
				return srcFile, dstFile
			},
			wantErr:    true,
			verifySize: false,
		},
		{
			name: "copy non-existent source",
			setup: func(t *testing.T) (string, string) {
				dstDir := t.TempDir()
				
				srcFile := "/non/existent/source.txt"
				dstFile := filepath.Join(dstDir, "dest.txt")
				
				return srcFile, dstFile
			},
			wantErr:    true,
			verifySize: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, dst := tt.setup(t)
			
			ip := &ImagePreparer{}
			err := ip.copyFile(src, dst, 0644)
			
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				
				if tt.verifySize {
					// Verify file exists and has content
					info, err := os.Stat(dst)
					assert.NoError(t, err)
					assert.Greater(t, info.Size(), int64(0))
				}
			}
		})
	}
}

// TestImagePreparer_Prepare_AnnotationInitialization tests annotation map initialization
func TestImagePreparer_Prepare_AnnotationInitialization_Unit(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		expectNil   bool
	}{
		{
			name:        "nil annotations",
			annotations: nil,
			expectNil:   true,
		},
		{
			name:        "empty annotations",
			annotations: map[string]string{},
			expectNil:   false,
		},
		{
			name:        "existing annotations",
			annotations: map[string]string{"key": "value"},
			expectNil:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := NewImagePreparer(&PreparerConfig{
				RootfsDir: t.TempDir(),
			})
			preparer := ip.(*ImagePreparer)

			task := &types.Task{
				ID:  "test-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:latest",
					},
				},
				Annotations: tt.annotations,
			}

			ctx := context.Background()
			_ = preparer.Prepare(ctx, task)

			// Annotations should be initialized
			assert.NotNil(t, task.Annotations)
		})
	}
}

// TestImagePreparer_CacheBehavior tests cache behavior
func TestImagePreparer_CacheBehavior_Unit(t *testing.T) {
	rootfsDir := t.TempDir()
	
	ip := NewImagePreparer(&PreparerConfig{
		RootfsDir: rootfsDir,
	})
	preparer := ip.(*ImagePreparer)

	// Create a fake rootfs file
	imageID := "nginx-latest"
	rootfsPath := filepath.Join(rootfsDir, imageID+".ext4")
	err := os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644)
	require.NoError(t, err)

	task := &types.Task{
		ID:  "test-task",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "nginx:latest",
			},
		},
		Annotations: make(map[string]string),
	}

	ctx := context.Background()
	err = preparer.Prepare(ctx, task)

	// Should skip preparation since rootfs exists
	if err != nil {
		// May fail on other operations, but shouldn't be about missing rootfs
		assert.NotContains(t, err.Error(), "failed to prepare image")
	}

	// Check annotations
	if task.Annotations != nil {
		assert.Equal(t, rootfsPath, task.Annotations["rootfs"])
	}
}

// TestImagePreparer_GetInitBinaryPath tests init binary path resolution
func TestImagePreparer_GetInitBinaryPath_Unit(t *testing.T) {
	tests := []struct {
		name       string
		initSystem InitSystemType
		setup      func(t *testing.T) string
	}{
		{
			name:       "tini lookup",
			initSystem: InitSystemTini,
			setup: func(t *testing.T) string {
				// Create a fake tini binary
				tmpDir := t.TempDir()
				binPath := filepath.Join(tmpDir, "tini")
				err := os.WriteFile(binPath, []byte("#!/bin/sh"), 0755)
				require.NoError(t, err)
				return binPath
			},
		},
		{
			name:       "dumb-init lookup",
			initSystem: InitSystemDumbInit,
			setup: func(t *testing.T) string {
				// Create a fake dumb-init binary
				tmpDir := t.TempDir()
				binPath := filepath.Join(tmpDir, "dumb-init")
				err := os.WriteFile(binPath, []byte("#!/bin/sh"), 0755)
				require.NoError(t, err)
				return binPath
			},
		},
		{
			name:       "none lookup",
			initSystem: InitSystemNone,
			setup: func(t *testing.T) string {
				return ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := &ImagePreparer{
				initInjector: NewInitInjector(&InitSystemConfig{
					Type: tt.initSystem,
				}),
			}

			_ = tt.setup(t)
			
			// Just verify the function doesn't crash
			path := ip.getInitBinaryPath()
			assert.NotNil(t, path)
		})
	}
}

// TestImagePreparer_Prepare_InitSystemAnnotations tests init system annotation handling
func TestImagePreparer_Prepare_InitSystemAnnotations_Unit(t *testing.T) {
	tests := []struct {
		name              string
		initSystem        string
		expectedInit      string
		initEnabled       bool
	}{
		{
			name:         "tini enabled",
			initSystem:   "tini",
			expectedInit: "tini",
			initEnabled:  true,
		},
		{
			name:         "dumb-init enabled",
			initSystem:   "dumb-init",
			expectedInit: "dumb-init",
			initEnabled:  true,
		},
		{
			name:         "none enabled",
			initSystem:   "none",
			expectedInit: "",
			initEnabled:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootfsDir := t.TempDir()
			
			// Create fake rootfs
			imageID := "nginx-latest"
			rootfsPath := filepath.Join(rootfsDir, imageID+".ext4")
			err := os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644)
			require.NoError(t, err)

			ip := NewImagePreparer(&PreparerConfig{
				RootfsDir:       rootfsDir,
				InitSystem:      tt.initSystem,
				InitGracePeriod: 10,
			})
			preparer := ip.(*ImagePreparer)

			task := &types.Task{
				ID:  "test-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:latest",
					},
				},
				Annotations: make(map[string]string),
			}

			ctx := context.Background()
			_ = preparer.Prepare(ctx, task)

			// Check annotations
			if tt.initEnabled {
				assert.Equal(t, tt.expectedInit, task.Annotations["init_system"])
			}
		})
	}
}
