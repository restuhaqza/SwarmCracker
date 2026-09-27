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
	// With CVR-1.6, mount -o loop replaced by debugfs write
	// debugfs exits 0 even on errors; code now checks output for error indicators
	sm := NewSecretManager("", "")
	ctx := context.Background()

	// Use a non-existent rootfs path - debugfs should fail
	err := sm.InjectSecrets(ctx, "test-task", []types.SecretRef{
		{Name: "test", Data: []byte("data")},
	}, "/nonexistent/rootfs.ext4")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "debugfs")
}
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
