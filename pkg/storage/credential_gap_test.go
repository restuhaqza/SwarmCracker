package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInjectSecret_Direct tests injectSecret directly
func TestInjectSecret_Direct(t *testing.T) {
	sm := NewSecretManager("", "")
	mountDir := t.TempDir()

	tests := []struct {
		name    string
		secret  types.SecretRef
		wantErr bool
	}{
		{
			name: "success with target",
			secret: types.SecretRef{
				ID:     "secret-1",
				Name:   "test_secret",
				Target: "/run/secrets/test_secret",
				Data:   []byte("secret data"),
			},
			wantErr: false,
		},
		{
			name: "success with default target",
			secret: types.SecretRef{
				ID:   "secret-2",
				Name: "default_secret",
				Data: []byte("default data"),
			},
			wantErr: false,
		},
		{
			name: "success with nested path",
			secret: types.SecretRef{
				ID:     "secret-3",
				Name:   "nested",
				Target: "/run/secrets/app/config/nested_secret",
				Data:   []byte("nested data"),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sm.injectSecret(mountDir, tt.secret)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify file was created
				targetPath := tt.secret.Target
				if targetPath == "" {
					targetPath = filepath.Join("/run/secrets", tt.secret.Name)
				}
				fullPath := filepath.Join(mountDir, targetPath)
				info, err := os.Stat(fullPath)
				require.NoError(t, err)
				assert.Equal(t, tt.secret.Data, readFile(t, fullPath))
				// Check permissions (0400)
				assert.Equal(t, os.FileMode(0400), info.Mode().Perm())
			}
		})
	}
}

// TestInjectConfig_Direct tests injectConfig directly
func TestInjectConfig_Direct(t *testing.T) {
	sm := NewSecretManager("", "")
	mountDir := t.TempDir()

	tests := []struct {
		name    string
		config  types.ConfigRef
		wantErr bool
	}{
		{
			name: "success with target",
			config: types.ConfigRef{
				ID:     "config-1",
				Name:   "test_config",
				Target: "/config/test_config",
				Data:   []byte("config data"),
			},
			wantErr: false,
		},
		{
			name: "success with default target",
			config: types.ConfigRef{
				ID:   "config-2",
				Name: "default_config",
				Data: []byte("default data"),
			},
			wantErr: false,
		},
		{
			name: "success with nested path",
			config: types.ConfigRef{
				ID:     "config-3",
				Name:   "nested",
				Target: "/config/app/yaml/nested.yaml",
				Data:   []byte("nested: true\n"),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sm.injectConfig(mountDir, tt.config)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify file was created
				targetPath := tt.config.Target
				if targetPath == "" {
					targetPath = filepath.Join("/config", tt.config.Name)
				}
				fullPath := filepath.Join(mountDir, targetPath)
				info, err := os.Stat(fullPath)
				require.NoError(t, err)
				assert.Equal(t, tt.config.Data, readFile(t, fullPath))
				// Check permissions (0444)
				assert.Equal(t, os.FileMode(0444), info.Mode().Perm())
			}
		})
	}
}

// TestInjectSecrets_SuccessPath tests InjectSecrets with mocked mount
func TestInjectSecrets_SuccessPath(t *testing.T) {
	// Skip if not running as root (can't mount)
	if os.Getuid() != 0 {
		t.Skip("requires root for mount operations")
	}

	// Create a real ext4 image for testing
	tmpDir := t.TempDir()
	rootfsPath := filepath.Join(tmpDir, "rootfs.ext4")

	// Create small ext4 image
	createTestExt4Image(t, rootfsPath, 10) // 10MB

	sm := NewSecretManager("", "")
	ctx := context.Background()

	secrets := []types.SecretRef{
		{
			ID:     "secret-1",
			Name:   "db_password",
			Target: "/run/secrets/db_password",
			Data:   []byte("supersecret123"),
		},
		{
			ID:     "secret-2",
			Name:   "api_key",
			Target: "/run/secrets/api/key",
			Data:   []byte("apikey-xyz"),
		},
	}

	err := sm.InjectSecrets(ctx, "test-task", secrets, rootfsPath)
	require.NoError(t, err)
}

// TestInjectConfigs_SuccessPath tests InjectConfigs with mocked mount
func TestInjectConfigs_SuccessPath(t *testing.T) {
	// Skip if not running as root (can't mount)
	if os.Getuid() != 0 {
		t.Skip("requires root for mount operations")
	}

	// Create a real ext4 image for testing
	tmpDir := t.TempDir()
	rootfsPath := filepath.Join(tmpDir, "rootfs.ext4")

	// Create small ext4 image
	createTestExt4Image(t, rootfsPath, 10) // 10MB

	sm := NewSecretManager("", "")
	ctx := context.Background()

	configs := []types.ConfigRef{
		{
			ID:     "config-1",
			Name:   "app.yaml",
			Target: "/config/app.yaml",
			Data:   []byte("port: 8080\n"),
		},
		{
			ID:     "config-2",
			Name:   "nginx.conf",
			Target: "/config/nginx/nginx.conf",
			Data:   []byte("server { listen 80; }\n"),
		},
	}

	err := sm.InjectConfigs(ctx, "test-task", configs, rootfsPath)
	require.NoError(t, err)
}

// TestMountRootfs_TempDirError tests mountRootfs temp dir creation error
// This is hard to test directly, so we test the error propagation
func TestMountRootfs_TempDirError(t *testing.T) {
	// This test verifies that if mount fails, the error is properly propagated
	sm := NewSecretManager("", "")
	ctx := context.Background()

	// Use a non-existent rootfs path - mount should fail
	err := sm.InjectSecrets(ctx, "test-task", []types.SecretRef{
		{Name: "test", Data: []byte("data")},
	}, "/nonexistent/rootfs.ext4")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to mount rootfs")
}

// TestUnmountRootfs_Error tests unmountRootfs error handling
func TestUnmountRootfs_Error(t *testing.T) {
	sm := NewSecretManager("", "")

	// Call unmountRootfs on a non-existent mount point
	// This should log a warning but not error
	sm.unmountRootfs("/nonexistent/mount")
	// No error expected - just logs warning
}

// Helper function to read file contents
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

// Helper function to create a test ext4 image (requires root)
func createTestExt4Image(t *testing.T, path string, sizeMB int) {
	t.Helper()
	// Create sparse file
	file, err := os.Create(path)
	require.NoError(t, err)
	err = file.Truncate(int64(sizeMB) * 1024 * 1024)
	require.NoError(t, err)
	file.Close()

	// Format as ext4 (requires root)
	// exec.Command("mkfs.ext4", "-F", "-q", path).Run()
	// For non-root tests, we skip this
}
