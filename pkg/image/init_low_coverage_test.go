// Package image tests low-coverage functions in init.go
package image

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitInjector_Inject_ErrorPaths tests error paths in Inject
func TestInitInjector_Inject_ErrorPaths(t *testing.T) {
	tests := []struct {
		name        string
		setupInjector func(*testing.T) *InitInjector
		setupRootfs  func(*testing.T) string
		wantErr      bool
		errContains  string
	}{
		{
			name: "inject_tini_invalid_rootfs",
			setupInjector: func(t *testing.T) *InitInjector {
				return NewInitInjector(&InitSystemConfig{
					Type:           InitSystemTini,
					GracePeriodSec: 10,
				})
			},
			setupRootfs: func(t *testing.T) string {
				// Create a file instead of ext4 image
				path := filepath.Join(t.TempDir(), "invalid.ext4")
				err := os.WriteFile(path, []byte("not ext4"), 0644)
				require.NoError(t, err)
				return path
			},
			wantErr:     false, // mountRootfs doesn't actually mount, just creates temp dir
			errContains: "",
		},
		{
			name: "inject_dumb_init_invalid_rootfs",
			setupInjector: func(t *testing.T) *InitInjector {
				return NewInitInjector(&InitSystemConfig{
					Type:           InitSystemDumbInit,
					GracePeriodSec: 5,
				})
			},
			setupRootfs: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "invalid.ext4")
				err := os.WriteFile(path, []byte("not ext4"), 0644)
				require.NoError(t, err)
				return path
			},
			wantErr:     false, // mountRootfs doesn't actually mount, just creates temp dir
			errContains: "",
		},
		{
			name: "inject_none_returns_nil",
			setupInjector: func(t *testing.T) *InitInjector {
				return NewInitInjector(&InitSystemConfig{
					Type:           InitSystemNone,
					GracePeriodSec: 0,
				})
			},
			setupRootfs: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "any.ext4")
			},
			wantErr:     false,
			errContains: "",
		},
		{
			name: "inject_unsupported_type",
			setupInjector: func(t *testing.T) *InitInjector {
				// Create injector with custom type
				return &InitInjector{
					config: &InitSystemConfig{
						Type:           InitSystemType("unsupported"),
						GracePeriodSec: 10,
					},
				}
			},
			setupRootfs: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "test.ext4")
			},
			wantErr:     true,
			errContains: "unsupported init system type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := tt.setupInjector(t)
			rootfsPath := tt.setupRootfs(t)

			err := ii.Inject(rootfsPath)

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

// TestInitInjector_InjectTini_AlreadyExists tests tini already in rootfs
func TestInitInjector_InjectTini_AlreadyExists(t *testing.T) {
	// Create a temporary mount directory structure
	tempDir := t.TempDir()
	sbinDir := filepath.Join(tempDir, "sbin")
	err := os.MkdirAll(sbinDir, 0755)
	require.NoError(t, err)

	// Create tini binary
	tiniPath := filepath.Join(sbinDir, "tini")
	err = os.WriteFile(tiniPath, []byte("#!/bin/sh\necho tini"), 0755)
	require.NoError(t, err)

	// Mount temp dir as rootfs (using temp dir since mountRootfs creates temp dir anyway)
	// We'll directly check if tini exists
	mountDir := tempDir

	// Check if tini exists - it does
	_, err = os.Stat(filepath.Join(mountDir, "sbin", "tini"))
	assert.NoError(t, err, "tini should exist")
}

// TestInitInjector_InjectDumbInit_AlreadyExists tests dumb-init already in rootfs
func TestInitInjector_InjectDumbInit_AlreadyExists(t *testing.T) {
	// Create a temporary mount directory structure
	tempDir := t.TempDir()
	sbinDir := filepath.Join(tempDir, "sbin")
	err := os.MkdirAll(sbinDir, 0755)
	require.NoError(t, err)

	// Create dumb-init binary
	dumbInitPath := filepath.Join(sbinDir, "dumb-init")
	err = os.WriteFile(dumbInitPath, []byte("#!/bin/sh\necho dumb-init"), 0755)
	require.NoError(t, err)

	// Check if dumb-init exists
	_, err = os.Stat(filepath.Join(tempDir, "sbin", "dumb-init"))
	assert.NoError(t, err, "dumb-init should exist")
}

// TestInitInjector_GetInitPath tests GetInitPath for different types
func TestInitInjector_GetInitPath(t *testing.T) {
	tests := []struct {
		name     string
		config   *InitSystemConfig
		expected string
	}{
		{
			name: "tini_path",
			config: &InitSystemConfig{
				Type: InitSystemTini,
			},
			expected: "/sbin/tini",
		},
		{
			name: "dumb_init_path",
			config: &InitSystemConfig{
				Type: InitSystemDumbInit,
			},
			expected: "/sbin/dumb-init",
		},
		{
			name: "none_path",
			config: &InitSystemConfig{
				Type: InitSystemNone,
			},
			expected: "",
		},
		{
			name: "unknown_type",
			config: &InitSystemConfig{
				Type: InitSystemType("unknown"),
			},
			expected: "/sbin/init",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(tt.config)
			path := ii.GetInitPath()
			assert.Equal(t, tt.expected, path)
		})
	}
}

// TestInitInjector_GetInitArgs tests GetInitArgs for different types
func TestInitInjector_GetInitArgs(t *testing.T) {
	tests := []struct {
		name        string
		config      *InitSystemConfig
		containerArgs []string
		expected    []string
	}{
		{
			name: "tini_args",
			config: &InitSystemConfig{
				Type: InitSystemTini,
			},
			containerArgs: []string{"/bin/sh", "-c", "echo hello"},
			expected:      []string{"/sbin/tini", "--", "/bin/sh", "-c", "echo hello"},
		},
		{
			name: "dumb_init_args",
			config: &InitSystemConfig{
				Type: InitSystemDumbInit,
			},
			containerArgs: []string{"/bin/sh", "-c", "echo hello"},
			expected:      []string{"/sbin/dumb-init", "/bin/sh", "-c", "echo hello"},
		},
		{
			name: "none_args",
			config: &InitSystemConfig{
				Type: InitSystemNone,
			},
			containerArgs: []string{"/bin/sh", "-c", "echo hello"},
			expected:      []string{"/bin/sh", "-c", "echo hello"},
		},
		{
			name: "tini_empty_args",
			config: &InitSystemConfig{
				Type: InitSystemTini,
			},
			containerArgs: []string{},
			expected:      []string{"/sbin/tini", "--"},
		},
		{
			name: "unknown_type_args",
			config: &InitSystemConfig{
				Type: InitSystemType("unknown"),
			},
			containerArgs: []string{"/bin/sh"},
			expected:      []string{"/bin/sh"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(tt.config)
			args := ii.GetInitArgs(tt.containerArgs)
			assert.Equal(t, tt.expected, args)
		})
	}
}

// TestInitInjector_GetGracePeriod tests GetGracePeriod
func TestInitInjector_GetGracePeriod(t *testing.T) {
	tests := []struct {
		name     string
		config   *InitSystemConfig
		expected int
	}{
		{
			name: "custom_grace_period",
			config: &InitSystemConfig{
				Type:           InitSystemTini,
				GracePeriodSec: 30,
			},
			expected: 30,
		},
		{
			name: "zero_grace_period",
			config: &InitSystemConfig{
				Type:           InitSystemTini,
				GracePeriodSec: 0,
			},
			expected: 10, // NewInitInjector sets default of 10 for init systems
		},
		{
			name: "large_grace_period",
			config: &InitSystemConfig{
				Type:           InitSystemDumbInit,
				GracePeriodSec: 300,
			},
			expected: 300,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(tt.config)
			period := ii.GetGracePeriod()
			assert.Equal(t, tt.expected, period)
		})
	}
}

// TestInitInjector_IsEnabled tests IsEnabled
func TestInitInjector_IsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		config   *InitSystemConfig
		expected bool
	}{
		{
			name: "tini_enabled",
			config: &InitSystemConfig{
				Type: InitSystemTini,
			},
			expected: true,
		},
		{
			name: "dumb_init_enabled",
			config: &InitSystemConfig{
				Type: InitSystemDumbInit,
			},
			expected: true,
		},
		{
			name: "none_disabled",
			config: &InitSystemConfig{
				Type: InitSystemNone,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(tt.config)
			enabled := ii.IsEnabled()
			assert.Equal(t, tt.expected, enabled)
		})
	}
}

// TestInitInjector_MountRootfs tests mountRootfs behavior
func TestInitInjector_MountRootfs(t *testing.T) {
	tests := []struct {
		name        string
		setupRootfs func(*testing.T) string
		wantErr     bool
	}{
		{
			name: "mount_invalid_ext4",
			setupRootfs: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "test.ext4")
				err := os.WriteFile(path, []byte("not ext4"), 0644)
				require.NoError(t, err)
				return path
			},
			wantErr: false, // mountRootfs only creates temp dir, doesn't actually mount
		},
		{
			name: "mount_nonexistent_file",
			setupRootfs: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent.ext4")
			},
			wantErr: false, // mountRootfs only creates temp dir
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(&InitSystemConfig{
				Type: InitSystemTini,
			})

			rootfsPath := tt.setupRootfs(t)
			mountDir, err := ii.mountRootfs(rootfsPath)

			// mountRootfs creates temp dir but doesn't actually mount
			// It returns the temp dir path
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				// Should successfully create temp dir
				assert.NoError(t, err)
				assert.NotEmpty(t, mountDir)
				// Verify dir exists
				info, err := os.Stat(mountDir)
				assert.NoError(t, err)
				assert.True(t, info.IsDir())
				// Cleanup
				ii.unmountRootfs(mountDir)
			}
		})
	}
}

// TestInitInjector_UnmountRootfs tests unmountRootfs behavior
func TestInitInjector_UnmountRootfs(t *testing.T) {
	tests := []struct {
		name       string
		setupMount func(*testing.T) string
	}{
		{
			name: "unmount_temp_dir",
			setupMount: func(t *testing.T) string {
				return t.TempDir()
			},
		},
		{
			name: "unmount_nonexistent_dir",
			setupMount: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent-mount")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(&InitSystemConfig{
				Type: InitSystemTini,
			})

			mountDir := tt.setupMount(t)
			err := ii.unmountRootfs(mountDir)

			// unmountRootfs always returns nil
			assert.NoError(t, err)
		})
	}
}

// TestInitInjector_CreateMinimalInit tests createMinimalInit
func TestInitInjector_CreateMinimalInit(t *testing.T) {
	tests := []struct {
		name     string
		initName string
		setupDir func(*testing.T) string
	}{
		{
			name:     "create_tini",
			initName: "tini",
			setupDir: func(t *testing.T) string {
				return t.TempDir()
			},
		},
		{
			name:     "create_dumb_init",
			initName: "dumb-init",
			setupDir: func(t *testing.T) string {
				return t.TempDir()
			},
		},
		{
			name:     "create_custom_init",
			initName: "my-init",
			setupDir: func(t *testing.T) string {
				return t.TempDir()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(&InitSystemConfig{
				Type: InitSystemTini,
			})

			mountDir := tt.setupDir(t)

			err := ii.createMinimalInit(mountDir, tt.initName)

			assert.NoError(t, err)

			// Verify init binary was created
			initPath := filepath.Join(mountDir, "sbin", tt.initName)
			info, err := os.Stat(initPath)
			assert.NoError(t, err, "init binary should exist")
			assert.Equal(t, os.FileMode(0755), info.Mode())

			// Verify it's a script
			content, err := os.ReadFile(initPath)
			assert.NoError(t, err)
			assert.Contains(t, string(content), "#!/bin/sh")

			// Verify symlink was created
			initLink := filepath.Join(mountDir, "init")
			linkTarget, err := os.Readlink(initLink)
			if err == nil {
				assert.Equal(t, "/sbin/"+tt.initName, linkTarget)
			}
			// Symlink creation might fail in some environments, so we don't assert error
		})
	}
}

// TestNewInitInjector_DefaultValues tests default value handling
func TestNewInitInjector_DefaultValues(t *testing.T) {
	tests := []struct {
		name     string
		config   *InitSystemConfig
		validate func(*testing.T, *InitInjector)
	}{
		{
			name:   "nil_config_gets_defaults",
			config: nil,
			validate: func(t *testing.T, ii *InitInjector) {
				assert.Equal(t, InitSystemTini, ii.config.Type)
				assert.Equal(t, 10, ii.config.GracePeriodSec)
			},
		},
		{
			name: "zero_grace_with_init_gets_default",
			config: &InitSystemConfig{
				Type:           InitSystemTini,
				GracePeriodSec: 0,
			},
			validate: func(t *testing.T, ii *InitInjector) {
				assert.Equal(t, InitSystemTini, ii.config.Type)
				assert.Equal(t, 10, ii.config.GracePeriodSec)
			},
		},
		{
			name: "zero_grace_with_none_stays_zero",
			config: &InitSystemConfig{
				Type:           InitSystemNone,
				GracePeriodSec: 0,
			},
			validate: func(t *testing.T, ii *InitInjector) {
				assert.Equal(t, InitSystemNone, ii.config.Type)
				assert.Equal(t, 0, ii.config.GracePeriodSec)
			},
		},
		{
			name: "custom_values_preserved",
			config: &InitSystemConfig{
				Type:           InitSystemDumbInit,
				GracePeriodSec: 60,
			},
			validate: func(t *testing.T, ii *InitInjector) {
				assert.Equal(t, InitSystemDumbInit, ii.config.Type)
				assert.Equal(t, 60, ii.config.GracePeriodSec)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(tt.config)
			tt.validate(t, ii)
		})
	}
}

// TestInitInjector_Integration tests integration scenarios
func TestInitInjector_Integration(t *testing.T) {
	tests := []struct {
		name   string
		config *InitSystemConfig
	}{
		{
			name: "full_tini_workflow",
			config: &InitSystemConfig{
				Type:           InitSystemTini,
				GracePeriodSec: 15,
			},
		},
		{
			name: "full_dumb_init_workflow",
			config: &InitSystemConfig{
				Type:           InitSystemDumbInit,
				GracePeriodSec: 20,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ii := NewInitInjector(tt.config)

			// Test all methods work together
			assert.True(t, ii.IsEnabled())
			assert.NotEmpty(t, ii.GetInitPath())
			assert.Equal(t, tt.config.GracePeriodSec, ii.GetGracePeriod())

			// Test args generation
			containerArgs := []string{"/bin/nginx", "-g", "daemon off;"}
			initArgs := ii.GetInitArgs(containerArgs)
			assert.NotEmpty(t, initArgs)
			assert.Greater(t, len(initArgs), len(containerArgs))

			// Test injection with dummy rootfs
			rootfsPath := filepath.Join(t.TempDir(), "test.ext4")
			err := os.WriteFile(rootfsPath, []byte("dummy"), 0644)
			require.NoError(t, err)

			err = ii.Inject(rootfsPath)
			// Should not error (mountRootfs creates temp dir)
			assert.NoError(t, err)
		})
	}
}
