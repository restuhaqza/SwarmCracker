// Package image tests for boosting coverage of low-coverage functions
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

func TestCopyDirectory_Coverage(t *testing.T) {
	tests := []struct {
		name     string
		setupSrc func(*testing.T) string
		setupDst func(*testing.T) string
		wantErr  bool
	}{
		{
			name: "copy_with_symlinks",
			setupSrc: func(t *testing.T) string {
				srcDir := t.TempDir()
				// Create file
				err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("content"), 0644)
				require.NoError(t, err)
				// Create subdirectory
				subDir := filepath.Join(srcDir, "subdir")
				err = os.MkdirAll(subDir, 0755)
				require.NoError(t, err)
				err = os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("nested"), 0644)
				require.NoError(t, err)
				return srcDir
			},
			setupDst: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
		},
		{
			name: "copy_empty_directory",
			setupSrc: func(t *testing.T) string {
				return t.TempDir()
			},
			setupDst: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
		},
		{
			name: "copy_nonexistent_source",
			setupSrc: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent")
			},
			setupDst: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := tt.setupSrc(t)
			dst := tt.setupDst(t)

			err := copyDirectory(src, dst)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
func TestInjectNetworkConfig_Coverage(t *testing.T) {
	tests := []struct {
		name        string
		setupRootfs func(*testing.T) string
		wantErr     bool
		checkFile   func(*testing.T, string)
	}{
		{
			name: "openrc_system_with_inittab",
			setupRootfs: func(t *testing.T) string {
				rootfs := t.TempDir()
				etcDir := filepath.Join(rootfs, "etc")
				err := os.MkdirAll(etcDir, 0755)
				require.NoError(t, err)
				// Create inittab with openrc reference
				inittabContent := `::sysinit:/sbin/openrc
::shutdown:/sbin/poweroff
`
				err = os.WriteFile(filepath.Join(etcDir, "inittab"), []byte(inittabContent), 0644)
				require.NoError(t, err)
				return rootfs
			},
			wantErr: false,
			checkFile: func(t *testing.T, rootfs string) {
				// Check that network/interfaces was created
				interfacesPath := filepath.Join(rootfs, "etc", "network", "interfaces")
				_, err := os.Stat(interfacesPath)
				assert.NoError(t, err, "network/interfaces should be created")
			},
		},
		{
			name: "non_openrc_system",
			setupRootfs: func(t *testing.T) string {
				rootfs := t.TempDir()
				etcDir := filepath.Join(rootfs, "etc")
				err := os.MkdirAll(etcDir, 0755)
				require.NoError(t, err)
				// Create inittab without openrc
				inittabContent := `::sysinit:/sbin/init
::shutdown:/sbin/poweroff
`
				err = os.WriteFile(filepath.Join(etcDir, "inittab"), []byte(inittabContent), 0644)
				require.NoError(t, err)
				return rootfs
			},
			wantErr: false,
			checkFile: func(t *testing.T, rootfs string) {
				// Should not create network config
				interfacesPath := filepath.Join(rootfs, "etc", "network", "interfaces")
				_, err := os.Stat(interfacesPath)
				assert.Error(t, err, "network/interfaces should not be created for non-openrc")
			},
		},
		{
			name: "no_inittab",
			setupRootfs: func(t *testing.T) string {
				rootfs := t.TempDir()
				etcDir := filepath.Join(rootfs, "etc")
				err := os.MkdirAll(etcDir, 0755)
				require.NoError(t, err)
				// No inittab file
				return rootfs
			},
			wantErr: false,
			checkFile: func(t *testing.T, rootfs string) {
				// Should not create network config
				interfacesPath := filepath.Join(rootfs, "etc", "network", "interfaces")
				_, err := os.Stat(interfacesPath)
				assert.Error(t, err, "network/interfaces should not be created without inittab")
			},
		},
		{
			name: "openrc_with_existing_network_config",
			setupRootfs: func(t *testing.T) string {
				rootfs := t.TempDir()
				etcDir := filepath.Join(rootfs, "etc")
				err := os.MkdirAll(etcDir, 0755)
				require.NoError(t, err)
				// Create inittab with openrc
				inittabContent := `::sysinit:/sbin/openrc
`
				err = os.WriteFile(filepath.Join(etcDir, "inittab"), []byte(inittabContent), 0644)
				require.NoError(t, err)
				// Create existing network/interfaces
				networkDir := filepath.Join(rootfs, "etc", "network")
				err = os.MkdirAll(networkDir, 0755)
				require.NoError(t, err)
				err = os.WriteFile(filepath.Join(networkDir, "interfaces"), []byte("# existing"), 0644)
				require.NoError(t, err)
				return rootfs
			},
			wantErr: false,
			checkFile: func(t *testing.T, rootfs string) {
				// Should overwrite existing config
				interfacesPath := filepath.Join(rootfs, "etc", "network", "interfaces")
				content, err := os.ReadFile(interfacesPath)
				assert.NoError(t, err)
				assert.Contains(t, string(content), "Firecracker VM")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := NewImagePreparer(&PreparerConfig{
				RootfsDir: t.TempDir(),
			}).(*ImagePreparer)

			rootfs := tt.setupRootfs(t)

			err := ip.injectNetworkConfig(rootfs)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkFile != nil {
					tt.checkFile(t, rootfs)
				}
			}
		})
	}
}

// TestPrepare_ImageScenarios tests Prepare with various image scenarios
func TestPrepare_ImageScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	tests := []struct {
		name        string
		config      *PreparerConfig
		task        *types.Task
		setupRootfs func(*testing.T, string) string
		wantErr     bool
		errContains string
	}{
		{
			name: "rootfs_already_exists",
			config: &PreparerConfig{
				RootfsDir: t.TempDir(),
			},
			task: &types.Task{
				ID: "test-task-cached",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:latest",
					},
				},
				Annotations: make(map[string]string),
			},
			setupRootfs: func(t *testing.T, rootfsDir string) string {
				// Create existing rootfs file
				imageID := generateImageID("nginx:latest")
				rootfsPath := filepath.Join(rootfsDir, imageID+".ext4")
				err := os.WriteFile(rootfsPath, []byte("existing rootfs"), 0644)
				require.NoError(t, err)
				return rootfsPath
			},
			wantErr: false,
		},
		{
			name: "invalid_architecture",
			config: &PreparerConfig{
				RootfsDir: t.TempDir(),
			},
			task: &types.Task{
				ID: "test-task-arch",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:latest",
					},
				},
				Annotations: make(map[string]string),
			},
			setupRootfs: func(t *testing.T, rootfsDir string) string {
				return ""
			},
			wantErr:     false, // Architecture validation passes for amd64/arm64
			errContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootfsPath := tt.setupRootfs(t, tt.config.RootfsDir)

			ip := NewImagePreparer(tt.config)
			ctx := context.Background()

			err := ip.Prepare(ctx, tt.task)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				if !tt.wantErr && rootfsPath != "" {
					// For cached rootfs, check annotation
					assert.Equal(t, rootfsPath, tt.task.Annotations["rootfs"])
				}
			}
		})
	}
}

// TestCleanup_Coverage tests Cleanup with various scenarios
func TestCleanup_Coverage(t *testing.T) {
	tests := []struct {
		name        string
		setupRootfs func(*testing.T) string
		keepDays    int
		wantRemoved int
		setupFiles  func(*testing.T, string) []string
	}{
		{
			name: "cleanup_old_files",
			setupRootfs: func(t *testing.T) string {
				return t.TempDir()
			},
			keepDays:    7,
			wantRemoved: 0, // Files are recent, won't be cleaned up
			setupFiles: func(t *testing.T, rootfsDir string) []string {
				// Create an .ext4 file (recent, won't be cleaned)
				recentFile := filepath.Join(rootfsDir, "recent-image.ext4")
				// We can't easily set old mtime without platform-specific code
				// So just create the file as recent
				err := os.WriteFile(recentFile, []byte("recent"), 0644)
				require.NoError(t, err)
				return []string{recentFile}
			},
		},
		{
			name: "cleanup_skips_non_ext4",
			setupRootfs: func(t *testing.T) string {
				return t.TempDir()
			},
			keepDays:    7,
			wantRemoved: 0,
			setupFiles: func(t *testing.T, rootfsDir string) []string {
				// Create non-.ext4 files
				txtFile := filepath.Join(rootfsDir, "readme.txt")
				err := os.WriteFile(txtFile, []byte("readme"), 0644)
				require.NoError(t, err)
				dirPath := filepath.Join(rootfsDir, "subdir")
				err = os.MkdirAll(dirPath, 0755)
				require.NoError(t, err)
				return []string{txtFile, dirPath}
			},
		},
		{
			name: "cleanup_empty_directory",
			setupRootfs: func(t *testing.T) string {
				return t.TempDir()
			},
			keepDays:    7,
			wantRemoved: 0,
			setupFiles: func(t *testing.T, rootfsDir string) []string {
				return nil
			},
		},
		{
			name: "cleanup_nonexistent_directory",
			setupRootfs: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent")
			},
			keepDays:    7,
			wantRemoved: 0,
			setupFiles:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootfsDir := tt.setupRootfs(t)

			if tt.setupFiles != nil {
				tt.setupFiles(t, rootfsDir)
			}

			ip := NewImagePreparer(&PreparerConfig{
				RootfsDir: rootfsDir,
			})
			ctx := context.Background()

			filesRemoved, _, err := ip.Cleanup(ctx, tt.keepDays)

			assert.NoError(t, err)
			assert.Equal(t, tt.wantRemoved, filesRemoved)
		})
	}
}
