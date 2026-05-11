//go:build !integration

// Package image tests for RealContainerRuntime, RealFilesystemOperator, and RealBinaryLocator
// These tests exercise the real implementations which call exec.Command
package image

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewRealContainerRuntime tests the constructor
func TestNewRealContainerRuntime(t *testing.T) {
	tests := []struct {
		name    string
		runtime string
	}{
		{
			name:    "docker_runtime",
			runtime: "docker",
		},
		{
			name:    "podman_runtime",
			runtime: "podman",
		},
		{
			name:    "empty_runtime",
			runtime: "",
		},
		{
			name:    "custom_runtime",
			runtime: "custom-container-runtime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := NewRealContainerRuntime(tt.runtime)
			assert.NotNil(t, runtime, "NewRealContainerRuntime should return non-nil")

			// Verify it implements the interface
			var _ ContainerRuntime = runtime
		})
	}
}

// TestRealContainerRuntime_CreateContainer tests CreateContainer method
func TestRealContainerRuntime_CreateContainer(t *testing.T) {
	tests := []struct {
		name     string
		runtime  string
		imageRef string
		destPath string
		wantErr  bool
	}{
		{
			name:     "nonexistent_runtime_create",
			runtime:  "nonexistent-runtime-xyz",
			imageRef: "alpine:latest",
			destPath: "/tmp/test",
			wantErr:  true,
		},
		{
			name:     "podman_create_without_podman",
			runtime:  "podman",
			imageRef: "alpine:latest",
			destPath: "/tmp/test",
			wantErr:  true, // podman not available
		},
		{
			name:     "docker_create_nonexistent_image",
			runtime:  "docker",
			imageRef: "nonexistent-image-xyz:invalid",
			destPath: t.TempDir(),
			wantErr:  true, // image doesn't exist
		},
		{
			name:     "empty_image_ref",
			runtime:  "docker",
			imageRef: "",
			destPath: "/tmp/test",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip if runtime is available and would create real containers
			if tt.runtime == "docker" || tt.runtime == "podman" {
				if _, err := exec.LookPath(tt.runtime); err != nil {
					// Runtime not available, test should work
				}
			}

			runtime := NewRealContainerRuntime(tt.runtime)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			containerID, err := runtime.CreateContainer(ctx, tt.imageRef, tt.destPath)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Empty(t, containerID)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, containerID)
			}
		})
	}
}

// TestRealContainerRuntime_ExportContainer tests ExportContainer method
func TestRealContainerRuntime_ExportContainer(t *testing.T) {
	tests := []struct {
		name        string
		runtime     string
		containerID string
		tarPath     string
		wantErr     bool
	}{
		{
			name:        "nonexistent_runtime_export",
			runtime:     "nonexistent-runtime-xyz",
			containerID: "nonexistent-container",
			tarPath:     filepath.Join(t.TempDir(), "export.tar"),
			wantErr:     true,
		},
		{
			name:        "podman_export_without_podman",
			runtime:     "podman",
			containerID: "nonexistent-container",
			tarPath:     filepath.Join(t.TempDir(), "export.tar"),
			wantErr:     true,
		},
		{
			name:        "docker_export_nonexistent_container",
			runtime:     "docker",
			containerID: "nonexistent-container-xyz-123",
			tarPath:     filepath.Join(t.TempDir(), "export.tar"),
			wantErr:     true,
		},
		{
			name:        "empty_container_id",
			runtime:     "docker",
			containerID: "",
			tarPath:     filepath.Join(t.TempDir(), "export.tar"),
			wantErr:     true,
		},
		{
			name:        "empty_tar_path",
			runtime:     "docker",
			containerID: "some-container",
			tarPath:     "",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := NewRealContainerRuntime(tt.runtime)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := runtime.ExportContainer(ctx, tt.containerID, tt.tarPath)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestRealContainerRuntime_RemoveContainer tests RemoveContainer method
func TestRealContainerRuntime_RemoveContainer(t *testing.T) {
	tests := []struct {
		name        string
		runtime     string
		containerID string
		wantErr     bool
	}{
		{
			name:        "nonexistent_runtime_remove",
			runtime:     "nonexistent-runtime-xyz",
			containerID: "nonexistent-container",
			wantErr:     true,
		},
		{
			name:        "podman_remove_without_podman",
			runtime:     "podman",
			containerID: "nonexistent-container",
			wantErr:     true,
		},
		// Note: docker rm -f <nonexistent> may succeed silently due to -f flag
		// So we don't test this case with docker
		{
			name:        "empty_container_id",
			runtime:     "docker",
			containerID: "",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := NewRealContainerRuntime(tt.runtime)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := runtime.RemoveContainer(ctx, tt.containerID)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestRealContainerRuntime_PullImage tests PullImage method
func TestRealContainerRuntime_PullImage(t *testing.T) {
	tests := []struct {
		name     string
		runtime  string
		imageRef string
		wantErr  bool
	}{
		{
			name:     "nonexistent_runtime_pull",
			runtime:  "nonexistent-runtime-xyz",
			imageRef: "alpine:latest",
			wantErr:  true,
		},
		{
			name:     "podman_pull_without_podman",
			runtime:  "podman",
			imageRef: "alpine:latest",
			wantErr:  true,
		},
		{
			name:     "docker_pull_nonexistent_image",
			runtime:  "docker",
			imageRef: "nonexistent-image-xyz:invalid-tag",
			wantErr:  true,
		},
		{
			name:     "empty_image_ref",
			runtime:  "docker",
			imageRef: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := NewRealContainerRuntime(tt.runtime)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := runtime.PullImage(ctx, tt.imageRef)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestRealContainerRuntime_ImageExists tests ImageExists method
func TestRealContainerRuntime_ImageExists(t *testing.T) {
	tests := []struct {
		name      string
		runtime   string
		imageRef  string
		wantExist bool
	}{
		{
			name:      "nonexistent_runtime_image_exists",
			runtime:   "nonexistent-runtime-xyz",
			imageRef:  "alpine:latest",
			wantExist: false,
		},
		{
			name:      "podman_image_exists_without_podman",
			runtime:   "podman",
			imageRef:  "alpine:latest",
			wantExist: false,
		},
		{
			name:      "docker_image_exists_nonexistent_image",
			runtime:   "docker",
			imageRef:  "nonexistent-image-xyz:invalid",
			wantExist: false,
		},
		{
			name:      "empty_image_ref",
			runtime:   "docker",
			imageRef:  "",
			wantExist: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := NewRealContainerRuntime(tt.runtime)
			ctx := context.Background()

			exists := runtime.ImageExists(ctx, tt.imageRef)

			assert.Equal(t, tt.wantExist, exists)
		})
	}
}

// TestNewRealFilesystemOperator tests the constructor
func TestNewRealFilesystemOperator(t *testing.T) {
	fs := NewRealFilesystemOperator()
	assert.NotNil(t, fs, "NewRealFilesystemOperator should return non-nil")

	// Verify it implements the interface
	var _ FilesystemOperator = fs
}

// TestRealFilesystemOperator_MkfsExt4 tests MkfsExt4 method
func TestRealFilesystemOperator_MkfsExt4(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) (string, string)
		wantErr bool
	}{
		{
			name: "mkfs_nonexistent_source",
			setup: func(t *testing.T) (string, string) {
				tmpDir := t.TempDir()
				sourceDir := filepath.Join(tmpDir, "nonexistent")
				outputPath := filepath.Join(tmpDir, "output.ext4")
				return sourceDir, outputPath
			},
			wantErr: true,
		},
		{
			name: "mkfs_empty_source_directory",
			setup: func(t *testing.T) (string, string) {
				tmpDir := t.TempDir()
				// Empty source directory
				sourceDir := filepath.Join(tmpDir, "empty")
				err := os.MkdirAll(sourceDir, 0755)
				require.NoError(t, err)
				outputPath := filepath.Join(tmpDir, "output.ext4")
				return sourceDir, outputPath
			},
			wantErr: true, // mkfs.ext4 may fail without proper setup
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := NewRealFilesystemOperator()
			sourceDir, outputPath := tt.setup(t)

			err := fs.MkfsExt4(sourceDir, outputPath)

			if tt.wantErr {
				assert.Error(t, err)
			}
			// If no error, just verify the call was made
		})
	}
}

// TestRealFilesystemOperator_Truncate tests Truncate method
func TestRealFilesystemOperator_Truncate(t *testing.T) {
	tests := []struct {
		name    string
		sizeMB  int
		setup   func(t *testing.T) string
		wantErr bool
		verify  func(t *testing.T, path string, sizeMB int)
	}{
		{
			name:   "truncate_1mb",
			sizeMB: 1,
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "test-1mb.ext4")
			},
			wantErr: false,
			verify: func(t *testing.T, path string, sizeMB int) {
				info, err := os.Stat(path)
				require.NoError(t, err)
				expectedSize := int64(sizeMB) * 1024 * 1024
				assert.Equal(t, expectedSize, info.Size())
			},
		},
		{
			name:   "truncate_10mb",
			sizeMB: 10,
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "test-10mb.ext4")
			},
			wantErr: false,
			verify: func(t *testing.T, path string, sizeMB int) {
				info, err := os.Stat(path)
				require.NoError(t, err)
				expectedSize := int64(sizeMB) * 1024 * 1024
				assert.Equal(t, expectedSize, info.Size())
			},
		},
		{
			name:   "truncate_0mb",
			sizeMB: 0,
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "test-0mb.ext4")
			},
			wantErr: false, // truncate -s 0M creates a 0-byte file
			verify: func(t *testing.T, path string, sizeMB int) {
				info, err := os.Stat(path)
				require.NoError(t, err)
				assert.Equal(t, int64(0), info.Size())
			},
		},
		{
			name:   "truncate_100mb",
			sizeMB: 100,
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "test-100mb.ext4")
			},
			wantErr: false,
			verify: func(t *testing.T, path string, sizeMB int) {
				info, err := os.Stat(path)
				require.NoError(t, err)
				expectedSize := int64(sizeMB) * 1024 * 1024
				assert.Equal(t, expectedSize, info.Size())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := NewRealFilesystemOperator()
			path := tt.setup(t)

			err := fs.Truncate(path, tt.sizeMB)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.verify != nil {
					tt.verify(t, path, tt.sizeMB)
				}
			}
		})
	}
}

// TestRealFilesystemOperator_Mount tests Mount method
func TestRealFilesystemOperator_Mount(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) (string, string)
		wantErr bool
	}{
		{
			name: "mount_without_permissions",
			setup: func(t *testing.T) (string, string) {
				tmpDir := t.TempDir()
				// Create a fake image file
				imagePath := filepath.Join(tmpDir, "image.ext4")
				err := os.WriteFile(imagePath, []byte("fake ext4"), 0644)
				require.NoError(t, err)
				mountDir := filepath.Join(tmpDir, "mount")
				err = os.MkdirAll(mountDir, 0755)
				require.NoError(t, err)
				return imagePath, mountDir
			},
			wantErr: true, // mount requires root privileges
		},
		{
			name: "mount_nonexistent_image",
			setup: func(t *testing.T) (string, string) {
				tmpDir := t.TempDir()
				imagePath := filepath.Join(tmpDir, "nonexistent.ext4")
				mountDir := filepath.Join(tmpDir, "mount")
				return imagePath, mountDir
			},
			wantErr: true,
		},
		{
			name: "mount_nonexistent_mount_dir",
			setup: func(t *testing.T) (string, string) {
				tmpDir := t.TempDir()
				imagePath := filepath.Join(tmpDir, "image.ext4")
				err := os.WriteFile(imagePath, []byte("fake ext4"), 0644)
				require.NoError(t, err)
				mountDir := filepath.Join(tmpDir, "nonexistent-mount")
				return imagePath, mountDir
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := NewRealFilesystemOperator()
			imagePath, mountDir := tt.setup(t)

			err := fs.Mount(imagePath, mountDir)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestRealFilesystemOperator_Unmount tests Unmount method
func TestRealFilesystemOperator_Unmount(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr bool
	}{
		{
			name: "unmount_nonexistent_dir",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent-mount")
			},
			wantErr: true, // umount requires root and valid mount point
		},
		{
			name: "unmount_empty_path",
			setup: func(t *testing.T) string {
				return ""
			},
			wantErr: true,
		},
		{
			name: "unmount_regular_dir",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				mountDir := filepath.Join(tmpDir, "mount")
				err := os.MkdirAll(mountDir, 0755)
				require.NoError(t, err)
				return mountDir
			},
			wantErr: true, // Can't unmount a regular directory (not a mount point)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := NewRealFilesystemOperator()
			mountDir := tt.setup(t)

			err := fs.Unmount(mountDir)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestRealFilesystemOperator_CreateFile tests CreateFile method comprehensively
func TestRealFilesystemOperator_CreateFile(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr bool
		verify  func(t *testing.T, path string)
	}{
		{
			name: "create_file_in_temp_dir",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "newfile.txt")
			},
			wantErr: false,
			verify: func(t *testing.T, path string) {
				info, err := os.Stat(path)
				require.NoError(t, err)
				assert.Equal(t, int64(0), info.Size())
			},
		},
		{
			name: "create_file_in_subdirectory",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				subDir := filepath.Join(tmpDir, "subdir")
				err := os.MkdirAll(subDir, 0755)
				require.NoError(t, err)
				return filepath.Join(subDir, "nestedfile.txt")
			},
			wantErr: false,
			verify: func(t *testing.T, path string) {
				info, err := os.Stat(path)
				require.NoError(t, err)
				assert.Equal(t, int64(0), info.Size())
			},
		},
		{
			name: "create_file_in_nonexistent_dir",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent", "file.txt")
			},
			wantErr: true,
		},
		{
			name: "create_file_overwrite_existing",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "existing.txt")
				err := os.WriteFile(path, []byte("existing content"), 0644)
				require.NoError(t, err)
				return path
			},
			wantErr: false,
			verify: func(t *testing.T, path string) {
				// File should be overwritten (truncated)
				info, err := os.Stat(path)
				require.NoError(t, err)
				assert.Equal(t, int64(0), info.Size())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := NewRealFilesystemOperator()
			path := tt.setup(t)

			err := fs.CreateFile(path)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.verify != nil {
					tt.verify(t, path)
				}
			}
		})
	}
}

// TestRealFilesystemOperator_CopyFile tests CopyFile method comprehensively
func TestRealFilesystemOperator_CopyFile(t *testing.T) {
	tests := []struct {
		name    string
		mode    os.FileMode
		setup   func(t *testing.T) (string, string)
		wantErr bool
		verify  func(t *testing.T, src, dst string)
	}{
		{
			name: "copy_regular_file",
			mode: 0644,
			setup: func(t *testing.T) (string, string) {
				tmpDir := t.TempDir()
				src := filepath.Join(tmpDir, "source.txt")
				dst := filepath.Join(tmpDir, "dest.txt")
				err := os.WriteFile(src, []byte("test content for copying"), 0644)
				require.NoError(t, err)
				return src, dst
			},
			wantErr: false,
			verify: func(t *testing.T, src, dst string) {
				srcContent, err := os.ReadFile(src)
				require.NoError(t, err)
				dstContent, err := os.ReadFile(dst)
				require.NoError(t, err)
				assert.Equal(t, srcContent, dstContent)
			},
		},
		{
			name: "copy_with_executable_mode",
			mode: 0755,
			setup: func(t *testing.T) (string, string) {
				tmpDir := t.TempDir()
				src := filepath.Join(tmpDir, "script.sh")
				dst := filepath.Join(tmpDir, "copied-script.sh")
				err := os.WriteFile(src, []byte("#!/bin/sh\necho hello"), 0644)
				require.NoError(t, err)
				return src, dst
			},
			wantErr: false,
			verify: func(t *testing.T, src, dst string) {
				info, err := os.Stat(dst)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0755), info.Mode())
			},
		},
		{
			name: "copy_large_file",
			mode: 0644,
			setup: func(t *testing.T) (string, string) {
				tmpDir := t.TempDir()
				src := filepath.Join(tmpDir, "large.bin")
				dst := filepath.Join(tmpDir, "large-copy.bin")
				// Create a 1KB file
				data := make([]byte, 1024)
				for i := range data {
					data[i] = byte(i % 256)
				}
				err := os.WriteFile(src, data, 0644)
				require.NoError(t, err)
				return src, dst
			},
			wantErr: false,
			verify: func(t *testing.T, src, dst string) {
				srcInfo, err := os.Stat(src)
				require.NoError(t, err)
				dstInfo, err := os.Stat(dst)
				require.NoError(t, err)
				assert.Equal(t, srcInfo.Size(), dstInfo.Size())
			},
		},
		{
			name: "copy_nonexistent_source",
			mode: 0644,
			setup: func(t *testing.T) (string, string) {
				tmpDir := t.TempDir()
				src := filepath.Join(tmpDir, "nonexistent.txt")
				dst := filepath.Join(tmpDir, "dest.txt")
				return src, dst
			},
			wantErr: true,
		},
		{
			name: "copy_to_nonexistent_directory",
			mode: 0644,
			setup: func(t *testing.T) (string, string) {
				tmpDir := t.TempDir()
				src := filepath.Join(tmpDir, "source.txt")
				err := os.WriteFile(src, []byte("content"), 0644)
				require.NoError(t, err)
				dst := filepath.Join(tmpDir, "nonexistent", "dest.txt")
				return src, dst
			},
			wantErr: true,
		},
		{
			name: "copy_empty_file",
			mode: 0644,
			setup: func(t *testing.T) (string, string) {
				tmpDir := t.TempDir()
				src := filepath.Join(tmpDir, "empty.txt")
				dst := filepath.Join(tmpDir, "empty-copy.txt")
				err := os.WriteFile(src, []byte{}, 0644)
				require.NoError(t, err)
				return src, dst
			},
			wantErr: false,
			verify: func(t *testing.T, src, dst string) {
				info, err := os.Stat(dst)
				require.NoError(t, err)
				assert.Equal(t, int64(0), info.Size())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := NewRealFilesystemOperator()
			src, dst := tt.setup(t)

			err := fs.CopyFile(src, dst, tt.mode)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.verify != nil {
					tt.verify(t, src, dst)
				}
			}
		})
	}
}

// TestNewRealBinaryLocator tests the constructor
func TestNewRealBinaryLocator(t *testing.T) {
	locator := NewRealBinaryLocator()
	assert.NotNil(t, locator, "NewRealBinaryLocator should return non-nil")

	// Verify it implements the interface
	var _ BinaryLocator = locator
}

// TestRealBinaryLocator_LookPath tests LookPath method
func TestRealBinaryLocator_LookPath(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		wantErr  bool
		wantPath string
	}{
		{
			name:    "lookpath_existing_binary",
			file:    "ls",
			wantErr: false,
		},
		{
			name:    "lookpath_another_existing_binary",
			file:    "cat",
			wantErr: false,
		},
		{
			name:    "lookpath_nonexistent_binary",
			file:    "nonexistent-binary-xyz",
			wantErr: true,
		},
		{
			name:    "lookpath_empty_string",
			file:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			locator := NewRealBinaryLocator()

			path, err := locator.LookPath(tt.file)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Empty(t, path)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, path)
			}
		})
	}
}

// TestRealBinaryLocator_Which tests Which method comprehensively
func TestRealBinaryLocator_Which(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantErr bool
	}{
		{
			name:    "which_existing_binary",
			file:    "ls",
			wantErr: false,
		},
		{
			name:    "which_another_existing_binary",
			file:    "cat",
			wantErr: false,
		},
		{
			name:    "which_sh",
			file:    "sh",
			wantErr: false,
		},
		{
			name:    "which_bash",
			file:    "bash",
			wantErr: false,
		},
		{
			name:    "which_nonexistent_binary",
			file:    "nonexistent-binary-xyz",
			wantErr: true,
		},
		{
			name:    "which_empty_string",
			file:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			locator := NewRealBinaryLocator()

			path, err := locator.Which(tt.file)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Empty(t, path)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, path)
				// Verify the path ends with the binary name (with newline stripped)
				assert.Contains(t, path, tt.file)
			}
		})
	}
}

// TestRealBinaryLocator_FileExists tests FileExists method
func TestRealBinaryLocator_FileExists(t *testing.T) {
	tests := []struct {
		name      string
		setupPath func(t *testing.T) string
		wantExist bool
	}{
		{
			name: "existing_file",
			setupPath: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "testfile")
				err := os.WriteFile(path, []byte("test"), 0644)
				require.NoError(t, err)
				return path
			},
			wantExist: true,
		},
		{
			name: "existing_directory",
			setupPath: func(t *testing.T) string {
				path := t.TempDir()
				return path
			},
			wantExist: true,
		},
		{
			name: "nonexistent_file",
			setupPath: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent")
			},
			wantExist: false,
		},
		{
			name: "system_binary_ls",
			setupPath: func(t *testing.T) string {
				// Most systems have /bin/ls or /usr/bin/ls
				return "/bin/ls"
			},
			wantExist: true, // /bin/ls usually exists on Linux
		},
		{
			name: "empty_path",
			setupPath: func(t *testing.T) string {
				return ""
			},
			wantExist: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			locator := NewRealBinaryLocator()
			path := tt.setupPath(t)

			exists := locator.FileExists(path)

			assert.Equal(t, tt.wantExist, exists)
		})
	}
}

// TestPrepareImageWithMocks tests the prepareImageWithMocks function
func TestPrepareImageWithMocks(t *testing.T) {
	tests := []struct {
		name        string
		setupMocks  func(t *testing.T) (ContainerRuntime, FilesystemOperator, BinaryLocator)
		imageRef    string
		imageID     string
		outputPath  string
		wantErr     bool
		errContains string
	}{
		{
			name: "successful_prepare_with_mocks",
			setupMocks: func(t *testing.T) (ContainerRuntime, FilesystemOperator, BinaryLocator) {
				mockRuntime := NewMockContainerRuntime()
				mockRuntime.Images["alpine:latest"] = true

				mockFS := NewMockFilesystemOperator()
				mockLocator := NewMockBinaryLocator()

				return mockRuntime, mockFS, mockLocator
			},
			imageRef:   "alpine:latest",
			imageID:    "alpine-latest",
			outputPath: filepath.Join(t.TempDir(), "output.ext4"),
			wantErr:    false,
		},
		{
			name: "create_container_error",
			setupMocks: func(t *testing.T) (ContainerRuntime, FilesystemOperator, BinaryLocator) {
				mockRuntime := NewMockContainerRuntime()
				mockRuntime.CreateErr = assert.AnError

				mockFS := NewMockFilesystemOperator()
				mockLocator := NewMockBinaryLocator()

				return mockRuntime, mockFS, mockLocator
			},
			imageRef:    "alpine:latest",
			imageID:     "alpine-latest",
			outputPath:  filepath.Join(t.TempDir(), "output.ext4"),
			wantErr:     true,
			errContains: "",
		},
		{
			name: "export_container_error",
			setupMocks: func(t *testing.T) (ContainerRuntime, FilesystemOperator, BinaryLocator) {
				mockRuntime := NewMockContainerRuntime()
				mockRuntime.ExportErr = assert.AnError

				mockFS := NewMockFilesystemOperator()
				mockLocator := NewMockBinaryLocator()

				return mockRuntime, mockFS, mockLocator
			},
			imageRef:    "alpine:latest",
			imageID:     "alpine-latest",
			outputPath:  filepath.Join(t.TempDir(), "output.ext4"),
			wantErr:     true,
			errContains: "",
		},
		{
			name: "mkfs_error",
			setupMocks: func(t *testing.T) (ContainerRuntime, FilesystemOperator, BinaryLocator) {
				mockRuntime := NewMockContainerRuntime()
				mockRuntime.Images["test:latest"] = true

				mockFS := NewMockFilesystemOperator()
				mockFS.MkfsErr = assert.AnError

				mockLocator := NewMockBinaryLocator()

				return mockRuntime, mockFS, mockLocator
			},
			imageRef:    "test:latest",
			imageID:     "test-latest",
			outputPath:  filepath.Join(t.TempDir(), "output.ext4"),
			wantErr:     true,
			errContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime, fsOps, binLoc := tt.setupMocks(t)

			ip := NewImagePreparerWithMocks(
				&PreparerConfig{
					RootfsDir:  t.TempDir(),
					InitSystem: "tini",
				},
				runtime,
				fsOps,
				binLoc,
			)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := ip.prepareImageWithMocks(ctx, tt.imageRef, tt.imageID, tt.outputPath)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestInjectInitSystemWithMocks tests injectInitSystemWithMocks
func TestInjectInitSystemWithMocks(t *testing.T) {
	tests := []struct {
		name        string
		setupMocks  func(t *testing.T) (ContainerRuntime, FilesystemOperator, BinaryLocator)
		setupRootfs func(t *testing.T) string
		initSystem  string
		wantErr     bool
	}{
		{
			name: "inject_with_mount_error",
			setupMocks: func(t *testing.T) (ContainerRuntime, FilesystemOperator, BinaryLocator) {
				mockRuntime := NewMockContainerRuntime()
				mockFS := NewMockFilesystemOperator()
				mockFS.MountErr = assert.AnError
				mockLocator := NewMockBinaryLocator()
				return mockRuntime, mockFS, mockLocator
			},
			setupRootfs: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "rootfs.ext4")
			},
			initSystem: "tini",
			wantErr:    true,
		},
		{
			name: "inject_none_init_system",
			setupMocks: func(t *testing.T) (ContainerRuntime, FilesystemOperator, BinaryLocator) {
				mockRuntime := NewMockContainerRuntime()
				mockFS := NewMockFilesystemOperator()
				mockLocator := NewMockBinaryLocator()
				return mockRuntime, mockFS, mockLocator
			},
			setupRootfs: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "rootfs.ext4")
			},
			initSystem: "none",
			wantErr:    false, // none init system skips injection
		},
		{
			name: "inject_with_copy_file_error",
			setupMocks: func(t *testing.T) (ContainerRuntime, FilesystemOperator, BinaryLocator) {
				mockRuntime := NewMockContainerRuntime()
				mockFS := NewMockFilesystemOperator()
				mockFS.CopyErr = assert.AnError
				// Need to set up mock for mount to succeed and for binary lookup
				mockFS.Mounts["test"] = "/mnt"
				mockLocator := NewMockBinaryLocator()
				mockLocator.Binaries["/usr/bin/tini"] = "/usr/bin/tini"
				mockLocator.Binaries["tini"] = "/usr/bin/tini"
				return mockRuntime, mockFS, mockLocator
			},
			setupRootfs: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "rootfs.ext4")
			},
			initSystem: "tini",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime, fsOps, binLoc := tt.setupMocks(t)
			rootfsPath := tt.setupRootfs(t)

			ip := NewImagePreparerWithMocks(
				&PreparerConfig{
					RootfsDir:  t.TempDir(),
					InitSystem: tt.initSystem,
				},
				runtime,
				fsOps,
				binLoc,
			)

			err := ip.injectInitSystemWithMocks(rootfsPath)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestGetInitBinaryPathWithLocator tests getInitBinaryPathWithLocator
func TestGetInitBinaryPathWithLocator(t *testing.T) {
	tests := []struct {
		name         string
		setupLocator func(t *testing.T) BinaryLocator
		initSystem   string
	}{
		{
			name: "tini_via_lookpath",
			setupLocator: func(t *testing.T) BinaryLocator {
				mock := NewMockBinaryLocator()
				mock.Binaries["tini"] = "/usr/bin/tini"
				return mock
			},
			initSystem: "tini",
		},
		{
			name: "dumb_init_not_found",
			setupLocator: func(t *testing.T) BinaryLocator {
				mock := NewMockBinaryLocator()
				// No binaries registered
				return mock
			},
			initSystem: "dumb-init",
		},
		{
			name: "none_init_system",
			setupLocator: func(t *testing.T) BinaryLocator {
				mock := NewMockBinaryLocator()
				return mock
			},
			initSystem: "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binLoc := tt.setupLocator(t)

			ip := NewImagePreparerWithMocks(
				&PreparerConfig{
					RootfsDir:  t.TempDir(),
					InitSystem: tt.initSystem,
				},
				NewMockContainerRuntime(),
				NewMockFilesystemOperator(),
				binLoc,
			)

			path := ip.getInitBinaryPathWithLocator()

			// Just verify it doesn't crash and returns something
			// The actual result depends on mock setup and the code logic
			_ = path
		})
	}
}
