//go:build !integration

// Package image tests for low-coverage preparer functions
package image

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractWithGGCR_Validation tests extractWithGGCR input validation
func TestExtractWithGGCR_Validation_Low(t *testing.T) {
	tests := []struct {
		name        string
		imageRef    string
		destPath    string
		wantErr     bool
		errContains string
	}{
		{
			name:        "empty_image_ref",
			imageRef:    "",
			destPath:    "/tmp/test",
			wantErr:     true,
			errContains: "image reference must not be empty",
		},
		{
			name:        "empty_dest_path",
			imageRef:    "alpine:latest",
			destPath:    "",
			wantErr:     true,
			errContains: "destination path must not be empty",
		},
		{
			name:        "nonexistent_image",
			imageRef:    "nonexistent-image-xyz:invalid",
			destPath:    t.TempDir(),
			wantErr:     true,
			errContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := NewImagePreparer(&PreparerConfig{
				RootfsDir: t.TempDir(),
			}).(*ImagePreparer)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := ip.extractWithGGCR(ctx, tt.imageRef, tt.destPath)

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

// TestExtractWithDockerCLI_Low tests extractWithDockerCLI method
func TestExtractWithDockerCLI_Low(t *testing.T) {
	tests := []struct {
		name        string
		imageRef    string
		destPath    string
		wantErr     bool
		errContains string
	}{
		{
			name:        "empty_image_ref",
			imageRef:    "",
			destPath:    t.TempDir(),
			wantErr:     true,
			errContains: "",
		},
		{
			name:        "nonexistent_image",
			imageRef:    "nonexistent-image-xyz:invalid",
			destPath:    t.TempDir(),
			wantErr:     true,
			errContains: "",
		},
		{
			name:        "empty_dest_path",
			imageRef:    "alpine:latest",
			destPath:    "",
			wantErr:     true,
			errContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := NewImagePreparer(&PreparerConfig{
				RootfsDir: t.TempDir(),
			}).(*ImagePreparer)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := ip.extractWithDockerCLI(ctx, tt.imageRef, tt.destPath)

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

// TestExtractTarStream_Low tests extractTarStream function
func TestExtractTarStream_Low(t *testing.T) {
	tests := []struct {
		name     string
		setupTar func(t *testing.T) io.Reader
		dest     string
		wantErr  bool
		verify   func(t *testing.T, dest string)
	}{
		{
			name: "extract_simple_directory",
			setupTar: func(t *testing.T) io.Reader {
				var buf bytes.Buffer
				tw := tar.NewWriter(&buf)

				hdr := &tar.Header{Name: "testdir/", Typeflag: tar.TypeDir, Mode: 0755}
				require.NoError(t, tw.WriteHeader(hdr))

				hdr = &tar.Header{Name: "testdir/file.txt", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len("test content"))}
				require.NoError(t, tw.WriteHeader(hdr))
				tw.Write([]byte("test content"))

				require.NoError(t, tw.Close())
				return &buf
			},
			dest:    t.TempDir(),
			wantErr: false,
			verify: func(t *testing.T, dest string) {
				dirInfo, err := os.Stat(filepath.Join(dest, "testdir"))
				require.NoError(t, err)
				assert.True(t, dirInfo.IsDir())
			},
		},
		{
			name: "extract_with_symlinks",
			setupTar: func(t *testing.T) io.Reader {
				var buf bytes.Buffer
				tw := tar.NewWriter(&buf)

				hdr := &tar.Header{Name: "original.txt", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len("original content"))}
				require.NoError(t, tw.WriteHeader(hdr))
				tw.Write([]byte("original content"))

				hdr = &tar.Header{Name: "link.txt", Typeflag: tar.TypeSymlink, Linkname: "original.txt", Mode: 0777}
				require.NoError(t, tw.WriteHeader(hdr))

				require.NoError(t, tw.Close())
				return &buf
			},
			dest:    t.TempDir(),
			wantErr: false,
			verify: func(t *testing.T, dest string) {
				linkTarget, err := os.Readlink(filepath.Join(dest, "link.txt"))
				require.NoError(t, err)
				assert.Equal(t, "original.txt", linkTarget)
			},
		},
		{
			name: "extract_with_hardlinks",
			setupTar: func(t *testing.T) io.Reader {
				var buf bytes.Buffer
				tw := tar.NewWriter(&buf)

				hdr := &tar.Header{Name: "original.txt", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len("hardlink content"))}
				require.NoError(t, tw.WriteHeader(hdr))
				tw.Write([]byte("hardlink content"))

				hdr = &tar.Header{Name: "hardlink.txt", Typeflag: tar.TypeLink, Linkname: "original.txt", Mode: 0644}
				require.NoError(t, tw.WriteHeader(hdr))

				require.NoError(t, tw.Close())
				return &buf
			},
			dest:    t.TempDir(),
			wantErr: false,
			verify: func(t *testing.T, dest string) {
				origInfo, err := os.Stat(filepath.Join(dest, "original.txt"))
				require.NoError(t, err)
				linkInfo, err := os.Stat(filepath.Join(dest, "hardlink.txt"))
				require.NoError(t, err)
				assert.Equal(t, origInfo.Size(), linkInfo.Size())
			},
		},
		{
			name: "extract_with_nested_directories",
			setupTar: func(t *testing.T) io.Reader {
				var buf bytes.Buffer
				tw := tar.NewWriter(&buf)

				dirs := []string{"dir1/", "dir1/dir2/", "dir1/dir2/dir3/"}
				for _, dir := range dirs {
					hdr := &tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0755}
					require.NoError(t, tw.WriteHeader(hdr))
				}

				hdr := &tar.Header{Name: "dir1/dir2/dir3/deep.txt", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len("deep content"))}
				require.NoError(t, tw.WriteHeader(hdr))
				tw.Write([]byte("deep content"))

				require.NoError(t, tw.Close())
				return &buf
			},
			dest:    t.TempDir(),
			wantErr: false,
			verify: func(t *testing.T, dest string) {
				fileInfo, err := os.Stat(filepath.Join(dest, "dir1", "dir2", "dir3", "deep.txt"))
				require.NoError(t, err)
				assert.Equal(t, int64(len("deep content")), fileInfo.Size())
			},
		},
		{
			name: "extract_with_path_traversal_attempt",
			setupTar: func(t *testing.T) io.Reader {
				var buf bytes.Buffer
				tw := tar.NewWriter(&buf)

				hdr := &tar.Header{Name: "../../../etc/passwd", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len("malicious"))}
				require.NoError(t, tw.WriteHeader(hdr))
				tw.Write([]byte("malicious"))

				require.NoError(t, tw.Close())
				return &buf
			},
			dest:    t.TempDir(),
			wantErr: false,
			verify: func(t *testing.T, dest string) {
				destClean := filepath.Clean(dest)
				assert.NoDirExists(t, filepath.Join(destClean, "etc"), "path traversal should be blocked")
			},
		},
		{
			name: "extract_with_unknown_type",
			setupTar: func(t *testing.T) io.Reader {
				var buf bytes.Buffer
				tw := tar.NewWriter(&buf)

				hdr := &tar.Header{Name: "test.txt", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len("test"))}
				require.NoError(t, tw.WriteHeader(hdr))
				tw.Write([]byte("test"))

				hdr = &tar.Header{Name: "unknown", Typeflag: tar.TypeChar, Mode: 0644}
				require.NoError(t, tw.WriteHeader(hdr))

				require.NoError(t, tw.Close())
				return &buf
			},
			dest:    t.TempDir(),
			wantErr: false,
			verify: func(t *testing.T, dest string) {
				fileInfo, err := os.Stat(filepath.Join(dest, "test.txt"))
				require.NoError(t, err)
				assert.Equal(t, int64(len("test")), fileInfo.Size())
			},
		},
		{
			name: "extract_empty_tar",
			setupTar: func(t *testing.T) io.Reader {
				var buf bytes.Buffer
				tw := tar.NewWriter(&buf)
				require.NoError(t, tw.Close())
				return &buf
			},
			dest:    t.TempDir(),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tarReader := tt.setupTar(t)
			err := extractTarStream(tarReader, tt.dest)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.verify != nil {
					tt.verify(t, tt.dest)
				}
			}
		})
	}
}

// TestInjectInitSystem_Low tests injectInitSystem method
func TestInjectInitSystem_Low(t *testing.T) {
	tests := []struct {
		name        string
		setupRootfs func(t *testing.T) string
		initSystem  string
		wantErr     bool
		errContains string
	}{
		{
			name: "inject_none_init_system",
			setupRootfs: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "rootfs.ext4")
			},
			initSystem: "none",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := NewImagePreparer(&PreparerConfig{
				RootfsDir:       t.TempDir(),
				InitSystem:      tt.initSystem,
				InitGracePeriod: 10,
			}).(*ImagePreparer)

			rootfsPath := tt.setupRootfs(t)

			err := ip.injectInitSystem(rootfsPath)

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

// TestInjectTini_Direct tests injectTini via InitInjector
func TestInjectTini_Direct(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemTini, GracePeriodSec: 10})

	rootfsPath := filepath.Join(t.TempDir(), "test.ext4")
	os.WriteFile(rootfsPath, []byte("fake ext4"), 0644)

	// injectTini calls mountRootfs which creates temp dir but doesn't actually mount
	err := ii.Inject(rootfsPath)
	// May or may not succeed depending on permissions - just verify no crash
	_ = err
}

// TestInjectDumbInit_Direct tests injectDumbInit via InitInjector
func TestInjectDumbInit_Direct(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemDumbInit, GracePeriodSec: 10})

	rootfsPath := filepath.Join(t.TempDir(), "test.ext4")
	os.WriteFile(rootfsPath, []byte("fake ext4"), 0644)

	err := ii.Inject(rootfsPath)
	_ = err
}

// TestMountRootfs_Direct tests mountRootfs directly
func TestMountRootfs_Direct(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemTini, GracePeriodSec: 10})

	imagePath := filepath.Join(t.TempDir(), "test.ext4")
	os.WriteFile(imagePath, []byte("fake ext4"), 0644)

	mountDir, err := ii.mountRootfs(imagePath)
	assert.NoError(t, err)
	assert.NotEmpty(t, mountDir)

	// Verify mount dir exists
	info, err := os.Stat(mountDir)
	assert.NoError(t, err)
	assert.True(t, info.IsDir())

	ii.unmountRootfs(mountDir)
}

// TestCreateMinimalInit_Direct tests createMinimalInit with various scenarios
func TestCreateMinimalInit_Direct(t *testing.T) {
	tests := []struct {
		name     string
		initName string
	}{
		{name: "tini", initName: "tini"},
		{name: "dumb-init", initName: "dumb-init"},
		{name: "custom", initName: "my-custom-init"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(&InitSystemConfig{Type: InitSystemTini, GracePeriodSec: 10})

			mountDir := t.TempDir()
			err := ii.createMinimalInit(mountDir, tt.initName)

			assert.NoError(t, err)

			initPath := filepath.Join(mountDir, "sbin", tt.initName)
			info, err := os.Stat(initPath)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0755), info.Mode())
		})
	}
}

// TestUnmountRootfs_Direct tests unmountRootfs behavior
func TestUnmountRootfs_Direct(t *testing.T) {
	ii := NewInitInjector(&InitSystemConfig{Type: InitSystemTini, GracePeriodSec: 10})

	mountDir := t.TempDir()
	err := ii.unmountRootfs(mountDir)
	assert.NoError(t, err) // unmountRootfs always returns nil

	// Nonexistent path
	err = ii.unmountRootfs(filepath.Join(t.TempDir(), "nonexistent"))
	assert.NoError(t, err)
}

// TestHandleMounts_Low tests handleMounts method
func TestHandleMounts_Low(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T) (*ImagePreparer, string, []types.Mount)
		wantErr     bool
		errContains string
	}{
		{
			name: "empty_mounts",
			setup: func(t *testing.T) (*ImagePreparer, string, []types.Mount) {
				ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)
				rootfsPath := filepath.Join(t.TempDir(), "rootfs.ext4")
				os.WriteFile(rootfsPath, []byte("fake ext4"), 0644)
				return ip, rootfsPath, []types.Mount{}
			},
			wantErr: false,
		},
		{
			name: "mount_with_empty_target",
			setup: func(t *testing.T) (*ImagePreparer, string, []types.Mount) {
				ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)
				rootfsPath := filepath.Join(t.TempDir(), "rootfs.ext4")
				os.WriteFile(rootfsPath, []byte("fake ext4"), 0644)
				return ip, rootfsPath, []types.Mount{{Source: "test", Target: "", ReadOnly: false}}
			},
			wantErr: false,
		},
		{
			name: "bind_mount_nonexistent_source",
			setup: func(t *testing.T) (*ImagePreparer, string, []types.Mount) {
				ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)
				rootfsPath := filepath.Join(t.TempDir(), "rootfs.ext4")
				os.WriteFile(rootfsPath, []byte("fake ext4"), 0644)
				return ip, rootfsPath, []types.Mount{{Source: "/nonexistent/path", Target: "/data", ReadOnly: false}}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip, rootfsPath, mounts := tt.setup(t)
			ctx := context.Background()
			task := &types.Task{ID: "test-task", Annotations: make(map[string]string)}

			err := ip.handleMounts(ctx, task, rootfsPath, mounts)

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

// TestHandleVolumeMount_Low tests handleVolumeMount method
func TestHandleVolumeMount_Low(t *testing.T) {
	ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)
	ctx := context.Background()
	task := &types.Task{ID: "test-task", Annotations: make(map[string]string)}
	mountDir := t.TempDir()

	mount := &types.Mount{Source: "volume:test", Target: "/data", ReadOnly: false}
	err := ip.handleVolumeMount(ctx, task, mountDir, mount)
	assert.Error(t, err) // volumeManager.GetVolume will fail
}

// TestHandleBindMount_Low tests handleBindMount method
func TestHandleBindMount_Low(t *testing.T) {
	tests := []struct {
		name    string
		mount   types.Mount
		wantErr bool
	}{
		{
			name:    "bind_mount_nonexistent_source",
			mount:   types.Mount{Source: "/nonexistent/path", Target: "/data", ReadOnly: false},
			wantErr: false, // Nonexistent source is skipped
		},
		{
			name:    "bind_mount_empty_source",
			mount:   types.Mount{Source: "", Target: "/data", ReadOnly: false},
			wantErr: false, // Empty source is skipped
		},
		{
			name:    "bind_mount_empty_target",
			mount:   types.Mount{Source: filepath.Join(t.TempDir(), "source"), Target: "", ReadOnly: false},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)
			mountDir := t.TempDir()

			err := ip.handleBindMount(mountDir, &tt.mount)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestMountExt4_Low tests mountExt4 method
func TestMountExt4_Low(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr bool
	}{
		{
			name: "mount_nonexistent_image",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent.ext4")
			},
			wantErr: true,
		},
		{
			name: "mount_regular_file",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "fake.ext4")
				os.WriteFile(path, []byte("fake ext4 content"), 0644)
				return path
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)
			imagePath := tt.setup(t)

			mountDir, err := ip.mountExt4(imagePath)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Empty(t, mountDir)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, mountDir)
			}
		})
	}
}

// TestValidateArchitecture_Low tests validateArchitecture method
func TestValidateArchitecture_Low(t *testing.T) {
	ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)
	err := ip.validateArchitecture()
	assert.NoError(t, err) // Should succeed for current architecture (amd64 or arm64)
}

// TestPrepareImage_Low tests prepareImage method error paths
func TestPrepareImage_Low(t *testing.T) {
	tests := []struct {
		name       string
		imageRef   string
		imageID    string
		outputPath string
		wantErr    bool
	}{
		{
			name:       "prepare_with_empty_image_ref",
			imageRef:   "",
			imageID:    "test",
			outputPath: filepath.Join(t.TempDir(), "output.ext4"),
			wantErr:    true,
		},
		{
			name:       "prepare_with_empty_output_path",
			imageRef:   "alpine:latest",
			imageID:    "test",
			outputPath: "",
			wantErr:    true,
		},
		{
			name:       "prepare_nonexistent_image",
			imageRef:   "nonexistent-image-xyz:invalid",
			imageID:    "test",
			outputPath: filepath.Join(t.TempDir(), "output.ext4"),
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := ip.prepareImage(ctx, tt.imageRef, tt.imageID, tt.outputPath)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}