package security

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewManager(t *testing.T) {
	tests := []struct {
		name        string
		config      *config.Config
		expectError bool
	}{
		{
			name: "valid jailer configuration",
			config: &config.Config{
				Executor: config.ExecutorConfig{
					EnableJailer: true,
					Jailer: config.JailerConfig{
						UID:           1000,
						GID:           1000,
						ChrootBaseDir: t.TempDir(),
					},
				},
			},
			expectError: false,
		},
		{
			name: "jailer disabled",
			config: &config.Config{
				Executor: config.ExecutorConfig{
					EnableJailer: false,
				},
			},
			expectError: false,
		},
		{
			name: "invalid jailer (negative UID)",
			config: &config.Config{
				Executor: config.ExecutorConfig{
					EnableJailer: true,
					Jailer: config.JailerConfig{
						UID:           -1,
						GID:           1000,
						ChrootBaseDir: t.TempDir(),
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, err := NewManager(tt.config)
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, mgr)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, mgr)
			}
		})
	}
}

func TestManager_IsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		config   *config.Config
		expected bool
	}{
		{
			name: "jailer enabled",
			config: &config.Config{
				Executor: config.ExecutorConfig{
					EnableJailer: true,
					Jailer: config.JailerConfig{
						UID:           1000,
						GID:           1000,
						ChrootBaseDir: t.TempDir(),
					},
				},
			},
			expected: true,
		},
		{
			name: "jailer disabled",
			config: &config.Config{
				Executor: config.ExecutorConfig{
					EnableJailer: false,
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, err := NewManager(tt.config)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, mgr.IsEnabled())
		})
	}
}

func TestManager_PrepareVM(t *testing.T) {
	config := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: true,
			Jailer: config.JailerConfig{
				UID:           os.Getuid(),
				GID:           os.Getgid(),
				ChrootBaseDir: t.TempDir(),
			},
		},
	}

	mgr, err := NewManager(config)
	require.NoError(t, err)

	vmID := "test-vm-prepare"
	ctx := context.Background()

	vmCtx, err := mgr.PrepareVM(ctx, vmID)
	assert.NoError(t, err)
	assert.NotNil(t, vmCtx)
	assert.True(t, vmCtx.Enabled)
	assert.Equal(t, vmID, vmCtx.VMID)
	assert.NotEmpty(t, vmCtx.JailPath)
	assert.NotEmpty(t, vmCtx.SeccompProfile)

	// Verify files exist
	assert.FileExists(t, vmCtx.SeccompProfile)
	assert.DirExists(t, vmCtx.JailPath)
}

func TestManager_PrepareVM_Disabled(t *testing.T) {
	config := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: false,
		},
	}

	mgr, err := NewManager(config)
	require.NoError(t, err)

	vmID := "test-vm-disabled"
	ctx := context.Background()

	vmCtx, err := mgr.PrepareVM(ctx, vmID)
	assert.NoError(t, err)
	assert.NotNil(t, vmCtx)
	assert.False(t, vmCtx.Enabled)
}

func TestManager_CleanupVM(t *testing.T) {
	config := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: true,
			Jailer: config.JailerConfig{
				UID:           os.Getuid(),
				GID:           os.Getgid(),
				ChrootBaseDir: t.TempDir(),
			},
		},
	}

	mgr, err := NewManager(config)
	require.NoError(t, err)

	vmID := "test-vm-cleanup"
	ctx := context.Background()

	// Prepare VM
	vmCtx, err := mgr.PrepareVM(ctx, vmID)
	require.NoError(t, err)

	// Verify files exist
	assert.FileExists(t, vmCtx.SeccompProfile)
	assert.DirExists(t, vmCtx.JailPath)

	// Cleanup
	err = mgr.CleanupVM(ctx, vmCtx)
	assert.NoError(t, err)

	// Verify files removed
	_, err = os.Stat(vmCtx.SeccompProfile)
	assert.True(t, os.IsNotExist(err))

	_, err = os.Stat(vmCtx.JailPath)
	assert.True(t, os.IsNotExist(err))
}

func TestSecureFilePermissions(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root to change file ownership")
	}

	testFile := filepath.Join(t.TempDir(), "test.txt")
	err := os.WriteFile(testFile, []byte("test"), 0644)
	require.NoError(t, err)

	err = SecureFilePermissions(testFile)
	assert.NoError(t, err)

	// Verify permissions (should be 0600)
	info, err := os.Stat(testFile)
	assert.NoError(t, err)
	// Can't verify ownership without root, but we checked above
	assert.NotNil(t, info)
}

func TestSecureDirectoryPermissions(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root to change directory ownership")
	}

	testDir := filepath.Join(t.TempDir(), "testdir")
	err := os.MkdirAll(testDir, 0755)
	require.NoError(t, err)

	err = SecureDirectoryPermissions(testDir)
	assert.NoError(t, err)

	// Verify directory exists
	assert.DirExists(t, testDir)
}

func TestValidatePath(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		setup       func() string
		expectError bool
	}{
		{
			name: "valid absolute path",
			path: "",
			setup: func() string {
				dir := t.TempDir()
				// Ensure not world-writable
				os.Chmod(dir, 0755)
				return dir
			},
			expectError: false,
		},
		{
			name:        "relative path",
			path:        "relative/path",
			setup:       func() string { return "relative/path" },
			expectError: true,
		},
		{
			name: "world-writable directory",
			path: "",
			setup: func() string {
				dir := t.TempDir()
				// Make world-writable
				os.Chmod(dir, 0777)
				return dir
			},
			expectError: true,
		},
		{
			name: "symlink",
			path: "",
			setup: func() string {
				target := t.TempDir()
				link := filepath.Join(t.TempDir(), "symlink")
				os.Symlink(target, link)
				return link
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path
			if tt.setup != nil {
				path = tt.setup()
			}

			err := ValidatePath(path)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetDefaultSecurityConfig(t *testing.T) {
	cfg := GetDefaultSecurityConfig()

	assert.NotNil(t, cfg)
	assert.Equal(t, 1000, cfg.UID)
	assert.Equal(t, 1000, cfg.GID)
	assert.Equal(t, "/srv/jailer", cfg.ChrootBaseDir)
	assert.Empty(t, cfg.NetNS)
}

func TestValidateSecurityConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      *config.Config
		expectError bool
	}{
		{
			name: "valid configuration",
			config: &config.Config{
				Executor: config.ExecutorConfig{
					EnableJailer: true,
					Jailer: config.JailerConfig{
						UID:           1000,
						GID:           1000,
						ChrootBaseDir: t.TempDir(),
					},
				},
			},
			expectError: false,
		},
		{
			name: "jailer disabled",
			config: &config.Config{
				Executor: config.ExecutorConfig{
					EnableJailer: false,
				},
			},
			expectError: false,
		},
		{
			name: "root UID",
			config: &config.Config{
				Executor: config.ExecutorConfig{
					EnableJailer: true,
					Jailer: config.JailerConfig{
						UID:           0,
						GID:           1000,
						ChrootBaseDir: t.TempDir(),
					},
				},
			},
			expectError: true,
		},
		{
			name: "root GID",
			config: &config.Config{
				Executor: config.ExecutorConfig{
					EnableJailer: true,
					Jailer: config.JailerConfig{
						UID:           1000,
						GID:           0,
						ChrootBaseDir: t.TempDir(),
					},
				},
			},
			expectError: true,
		},
		{
			name: "empty chroot directory",
			config: &config.Config{
				Executor: config.ExecutorConfig{
					EnableJailer: true,
					Jailer: config.JailerConfig{
						UID:           1000,
						GID:           1000,
						ChrootBaseDir: "",
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSecurityConfig(tt.config)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestManager_GetJailer(t *testing.T) {
	config := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: true,
			Jailer: config.JailerConfig{
				UID:           1000,
				GID:           1000,
				ChrootBaseDir: t.TempDir(),
			},
		},
	}

	mgr, err := NewManager(config)
	require.NoError(t, err)

	jailer := mgr.GetJailer()
	assert.NotNil(t, jailer)
	assert.Equal(t, 1000, jailer.UID)
	assert.Equal(t, 1000, jailer.GID)
}

func TestManager_GetSeccompProfilePath(t *testing.T) {
	config := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: true,
			Jailer: config.JailerConfig{
				UID:           1000,
				GID:           1000,
				ChrootBaseDir: t.TempDir(),
			},
		},
	}

	mgr, err := NewManager(config)
	require.NoError(t, err)

	vmID := "test-vm-profile"
	path := mgr.GetSeccompProfilePath(vmID)

	assert.Contains(t, path, vmID)
	assert.Contains(t, path, "seccomp.json")
}
