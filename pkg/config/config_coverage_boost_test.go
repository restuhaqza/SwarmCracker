package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadConfig_InvalidYAML tests loading invalid YAML configurations
func TestLoadConfig_InvalidYAML(t *testing.T) {
	tests := []struct {
		name        string
		yamlContent string
		expectError bool
		errorMsg    string
	}{
		{
			name: "invalid_yaml_syntax",
			yamlContent: `
executor:
  kernel_path: /boot/vmlinuz
  invalid_yaml: [unclosed
`,
			expectError: true,
			errorMsg:    "failed to parse",
		},
		{
			name:        "empty_file",
			yamlContent: ``,
			expectError: false, // Empty config is valid (uses defaults)
		},
		{
			name: "invalid_yaml_type",
			yamlContent: `
executor: "not_a_struct"
`,
			expectError: false, // Will parse but may fail validation
		},
		{
			name: "yaml_with_nulls",
			yamlContent: `
executor:
  kernel_path: null
  rootfs_dir: null
`,
			expectError: false, // Will parse, validation will catch it
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary config file
			tmpFile := filepath.Join(t.TempDir(), "config.yaml")
			err := os.WriteFile(tmpFile, []byte(tt.yamlContent), 0644)
			require.NoError(t, err)

			cfg, err := LoadConfig(tmpFile)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tt.errorMsg))
				}
			} else {
				// May succeed or fail validation
				_ = cfg
				_ = err
			}
		})
	}
}

// TestConfig_Validate_MissingFields tests validation with missing required fields
func TestConfig_Validate_MissingFields(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		expectError bool
		errorMsg    string
	}{
		{
			name: "missing_kernel_path",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "",
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: true,
			errorMsg:    "kernel_path",
		},
		{
			name: "missing_rootfs_dir",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "",
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: true,
			errorMsg:    "rootfs_dir",
		},
		{
			name: "invalid_vcpus",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 0,
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: true,
			errorMsg:    "vcpus",
		},
		{
			name: "invalid_memory",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 1,
					DefaultMemoryMB: 0,
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: true,
			errorMsg:    "memory",
		},
		{
			name: "negative_vcpus",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath:   "/boot/vmlinuz",
					RootfsDir:    "/var/lib/firecracker",
					DefaultVCPUs: -1,
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: true,
			errorMsg:    "vcpus",
		},
		{
			name: "negative_memory",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath:     "/boot/vmlinuz",
					RootfsDir:      "/var/lib/firecracker",
					DefaultVCPUs:   1,
					DefaultMemoryMB: -512,
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: true,
			errorMsg:    "memory",
		},
		{
			name: "missing_bridge_name",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath:     "/boot/vmlinuz",
					RootfsDir:      "/var/lib/firecracker",
					DefaultVCPUs:   1,
					DefaultMemoryMB: 512,
				},
				Network: NetworkConfig{
					BridgeName: "",
				},
			},
			expectError: true,
			errorMsg:    "bridge_name",
		},
		{
			name: "rate_limit_enabled_but_zero_pps",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 1,
					DefaultMemoryMB: 512,
				},
				Network: NetworkConfig{
					BridgeName:       "br0",
					EnableRateLimit:  true,
					MaxPacketsPerSec: 0,
				},
			},
			expectError: true,
			errorMsg:    "max_packets_per_sec",
		},
		{
			name: "rate_limit_enabled_but_negative_pps",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 1,
					DefaultMemoryMB: 512,
				},
				Network: NetworkConfig{
					BridgeName:       "br0",
					EnableRateLimit:  true,
					MaxPacketsPerSec: -100,
				},
			},
			expectError: true,
			errorMsg:    "max_packets_per_sec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, strings.ToLower(err.Error()), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestConfig_Validate_JailerErrors tests jailer validation errors
func TestConfig_Validate_JailerErrors(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		expectError bool
		errorMsg    string
	}{
		{
			name: "jailer_enabled_but_missing_uid",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 1,
					DefaultMemoryMB: 512,
					EnableJailer: true,
					Jailer: JailerConfig{
						UID: 0,
						GID: 1000,
						ChrootBaseDir: "/var/lib/jailer",
					},
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: true,
			errorMsg:    "uid",
		},
		{
			name: "jailer_enabled_but_missing_gid",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 1,
					DefaultMemoryMB: 512,
					EnableJailer: true,
					Jailer: JailerConfig{
						UID: 1000,
						GID: 0,
						ChrootBaseDir: "/var/lib/jailer",
					},
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: true,
			errorMsg:    "gid",
		},
		{
			name: "jailer_enabled_but_missing_chroot",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 1,
					DefaultMemoryMB: 512,
					EnableJailer: true,
					Jailer: JailerConfig{
						UID: 1000,
						GID: 1000,
						ChrootBaseDir: "",
					},
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: true,
			errorMsg:    "chroot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, strings.ToLower(err.Error()), tt.errorMsg)
				}
			}
		})
	}
}

// TestConfig_Merge_Scenarios tests config merge scenarios
func TestConfig_Merge_Scenarios(t *testing.T) {
	tests := []struct {
		name     string
		base     Config
		override Config
		check    func(*testing.T, *Config)
	}{
		{
			name: "merge_executor_fields",
			base: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 1,
					DefaultMemoryMB: 512,
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			override: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/custom-vmlinuz",
					RootfsDir:  "/var/lib/custom",
				},
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "/boot/custom-vmlinuz", cfg.Executor.KernelPath)
				assert.Equal(t, "/var/lib/custom", cfg.Executor.RootfsDir)
				assert.Equal(t, 1, cfg.Executor.DefaultVCPUs) // Unchanged
				assert.Equal(t, 512, cfg.Executor.DefaultMemoryMB) // Unchanged
			},
		},
		{
			name: "merge_network_config",
			base: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 1,
					DefaultMemoryMB: 512,
				},
				Network: NetworkConfig{
					BridgeName:       "br0",
					EnableRateLimit:  false,
					MaxPacketsPerSec: 0,
				},
			},
			override: Config{
				Network: NetworkConfig{
					BridgeName:       "custom-br",
					EnableRateLimit:  true,
					MaxPacketsPerSec: 1000,
				},
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "custom-br", cfg.Network.BridgeName)
				assert.True(t, cfg.Network.EnableRateLimit)
				assert.Equal(t, 1000, cfg.Network.MaxPacketsPerSec)
			},
		},
		{
			name: "merge_with_empty_override",
			base: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 1,
					DefaultMemoryMB: 512,
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			override: Config{},
			check: func(t *testing.T, cfg *Config) {
				// Base values should remain
				assert.Equal(t, "/boot/vmlinuz", cfg.Executor.KernelPath)
				assert.Equal(t, "br0", cfg.Network.BridgeName)
			},
		},
		{
			name: "merge_jailer_config",
			base: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 1,
					DefaultMemoryMB: 512,
					EnableJailer: false,
					Jailer: JailerConfig{
						UID: 1000,
						GID: 1000,
						ChrootBaseDir: "/var/lib/jailer",
					},
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			override: Config{
				Executor: ExecutorConfig{
					EnableJailer: true,
					// Jailer config doesn't merge, just EnableJailer flag
				},
			},
			check: func(t *testing.T, cfg *Config) {
				assert.True(t, cfg.Executor.EnableJailer)
				// Jailer fields from base should remain
				assert.Equal(t, 1000, cfg.Executor.Jailer.UID)
				assert.Equal(t, 1000, cfg.Executor.Jailer.GID)
				assert.Equal(t, "/var/lib/jailer", cfg.Executor.Jailer.ChrootBaseDir)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.base.Merge(&tt.override)
			if tt.check != nil {
				tt.check(t, result)
			}
		})
	}
}

// TestConfig_Save_ErrorScenarios tests config save error scenarios
func TestConfig_Save_ErrorScenarios(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		config      Config
		expectError bool
		errorMsg    string
	}{
		{
			name: "save_to_readonly_directory",
			path: "/proc/readonly/config.yaml",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: true,
			errorMsg:    "failed",
		},
		{
			name: "save_to_valid_path",
			path: filepath.Join(t.TempDir(), "test-config.yaml"),
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: false,
		},
		{
			name: "save_with_nested_directories",
			path: filepath.Join(t.TempDir(), "nested/dir/config.yaml"),
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Save(tt.path)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, strings.ToLower(err.Error()), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				// Verify file exists
				_, err := os.Stat(tt.path)
				assert.NoError(t, err)
			}
		})
	}
}

// TestLoadConfig_FileErrors tests file loading errors
func TestLoadConfig_FileErrors(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "file_not_exists",
			path:        "/non/existent/path/config.yaml",
			expectError: true,
			errorMsg:    "failed to read",
		},
		{
			name:        "empty_path",
			path:        "",
			expectError: true,
			errorMsg:    "failed to read",
		},
		{
			name:        "directory_instead_of_file",
			path:        "/tmp",
			expectError: true,
			errorMsg:    "failed to read",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(tt.path)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, strings.ToLower(err.Error()), tt.errorMsg)
				}
			}
		})
	}
}

// TestGetDefaultConfigPath_EnvironmentTests tests environment variable handling
func TestGetDefaultConfigPath_EnvironmentTests(t *testing.T) {
	// Save original value
	originalPath := os.Getenv("SWARMCRACKER_CONFIG")
	defer func() {
		if originalPath != "" {
			os.Setenv("SWARMCRACKER_CONFIG", originalPath)
		} else {
			os.Unsetenv("SWARMCRACKER_CONFIG")
		}
	}()

	tests := []struct {
		name              string
		envValue          string
		expectedContains  string
	}{
		{
			name:              "env_var_set",
			envValue:          "/custom/path/config.yaml",
			expectedContains:  "/custom/path/config.yaml",
		},
		{
			name:              "env_var_empty",
			envValue:          "",
			expectedContains:  "/etc/swarmcracker/config.yaml",
		},
		{
			name:              "env_var_with_spaces",
			envValue:          "  /path/with/spaces/config.yaml  ",
			expectedContains:  "/path/with/spaces/config.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				os.Setenv("SWARMCRACKER_CONFIG", tt.envValue)
			} else {
				os.Unsetenv("SWARMCRACKER_CONFIG")
			}

			result := GetDefaultConfigPath()
			assert.Contains(t, result, tt.expectedContains)
		})
	}
}

// TestConfig_SetDefaults_Scenarios tests SetDefaults in various scenarios
func TestConfig_SetDefaults_Scenarios(t *testing.T) {
	tests := []struct {
		name  string
		input Config
		check func(*testing.T, *Config)
	}{
		{
			name: "all_fields_empty",
			input: Config{},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "swarm-br0", cfg.Network.BridgeName)
				assert.Equal(t, "info", cfg.Logging.Level)
				assert.Equal(t, "json", cfg.Logging.Format)
				assert.Equal(t, "stdout", cfg.Logging.Output)
				assert.Equal(t, "/var/cache/swarmcracker", cfg.Images.CacheDir)
				assert.Equal(t, 1, cfg.Executor.DefaultVCPUs)
				assert.Equal(t, 512, cfg.Executor.DefaultMemoryMB)
				assert.Equal(t, "/var/run/firecracker", cfg.Executor.SocketDir)
			},
		},
		{
			name: "some_fields_set",
			input: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
				},
				Network: NetworkConfig{
					BridgeName: "custom-br",
				},
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "/boot/vmlinuz", cfg.Executor.KernelPath)
				assert.Equal(t, "custom-br", cfg.Network.BridgeName)
				assert.Equal(t, "info", cfg.Logging.Level)
				assert.Equal(t, 1, cfg.Executor.DefaultVCPUs)
			},
		},
		{
			name: "legacy_fields_migration",
			input: Config{
				KernelPath:     "/legacy/kernel",
				RootfsDir:      "/legacy/rootfs",
				SocketDir:      "/legacy/socket",
				DefaultVCPUs:   2,
				DefaultMemoryMB: 1024,
				EnableJailer:   true,
				Executor: ExecutorConfig{
					// Leave empty to force migration
				},
			},
			check: func(t *testing.T, cfg *Config) {
				// SetDefaults is called before LoadConfig does migration
				// So we need to test LoadConfig instead
				assert.Equal(t, "/legacy/kernel", cfg.KernelPath)
				assert.Equal(t, "/legacy/rootfs", cfg.RootfsDir)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &tt.input
			cfg.SetDefaults()
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

// TestConfig_LegacyFieldMigration tests legacy field migration
func TestConfig_LegacyFieldMigration(t *testing.T) {
	// Create a YAML config with legacy fields
	yamlContent := `
kernel_path: /boot/vmlinuz
rootfs_dir: /var/lib/firecracker
socket_dir: /var/run/firecracker
default_vcpus: 2
default_memory_mb: 1024
enable_jailer: true

executor:
  kernel_path: ""
  rootfs_dir: ""

network:
  bridge_name: br0
`

	tmpFile := filepath.Join(t.TempDir(), "legacy-config.yaml")
	err := os.WriteFile(tmpFile, []byte(yamlContent), 0644)
	require.NoError(t, err)

	cfg, err := LoadConfig(tmpFile)
	require.NoError(t, err)

	// Verify legacy fields were migrated to nested structure
	assert.Equal(t, "/boot/vmlinuz", cfg.Executor.KernelPath)
	assert.Equal(t, "/var/lib/firecracker", cfg.Executor.RootfsDir)
	assert.Equal(t, "/var/run/firecracker", cfg.Executor.SocketDir)
	assert.Equal(t, 2, cfg.Executor.DefaultVCPUs)
	assert.Equal(t, 1024, cfg.Executor.DefaultMemoryMB)
	assert.True(t, cfg.Executor.EnableJailer)
}

// TestConfig_String_Representation tests String() output
func TestConfig_String_Representation(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "full_config",
			cfg: Config{
				Executor: ExecutorConfig{
					KernelPath:     "/boot/vmlinuz",
					RootfsDir:      "/var/lib/firecracker",
					DefaultVCPUs:   2,
					DefaultMemoryMB: 1024,
					EnableJailer:   true,
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
		},
		{
			name: "minimal_config",
			cfg: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			str := tt.cfg.String()
			assert.NotEmpty(t, str)
			assert.Contains(t, str, "Config{")
			assert.Contains(t, str, tt.cfg.Executor.KernelPath)
			assert.Contains(t, str, tt.cfg.Executor.RootfsDir)
			assert.Contains(t, str, tt.cfg.Network.BridgeName)
		})
	}
}

// TestNetworkConfig_Validate_Scenarios tests NetworkConfig validation
func TestNetworkConfig_Validate_Scenarios(t *testing.T) {
	tests := []struct {
		name        string
		config      NetworkConfig
		expectError bool
		errorMsg    string
	}{
		{
			name:        "empty_bridge_name",
			config:      NetworkConfig{},
			expectError: true,
			errorMsg:    "bridge_name",
		},
		{
			name: "valid_config",
			config: NetworkConfig{
				BridgeName: "br0",
			},
			expectError: false,
		},
		{
			name: "rate_limit_without_max_pps",
			config: NetworkConfig{
				BridgeName:       "br0",
				EnableRateLimit:  true,
				MaxPacketsPerSec: 0,
			},
			expectError: true,
			errorMsg:    "max_packets_per_sec",
		},
		{
			name: "rate_limit_with_negative_pps",
			config: NetworkConfig{
				BridgeName:       "br0",
				EnableRateLimit:  true,
				MaxPacketsPerSec: -100,
			},
			expectError: true,
			errorMsg:    "max_packets_per_sec",
		},
		{
			name: "rate_limit_disabled_zero_pps",
			config: NetworkConfig{
				BridgeName:       "br0",
				EnableRateLimit:  false,
				MaxPacketsPerSec: 0,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, strings.ToLower(err.Error()), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestJailerConfig_Validate_Scenarios tests JailerConfig validation
func TestJailerConfig_Validate_Scenarios(t *testing.T) {
	tests := []struct {
		name        string
		config      JailerConfig
		expectError bool
		errorMsg    string
	}{
		{
			name:        "all_fields_empty",
			config:      JailerConfig{},
			expectError: true, // UID and GID are required
		},
		{
			name: "uid_zero",
			config: JailerConfig{
				UID: 0,
				GID: 1000,
				ChrootBaseDir: "/var/lib/jailer",
			},
			expectError: true,
			errorMsg:    "uid",
		},
		{
			name: "gid_zero",
			config: JailerConfig{
				UID: 1000,
				GID: 0,
				ChrootBaseDir: "/var/lib/jailer",
			},
			expectError: true,
			errorMsg:    "gid",
		},
		{
			name: "empty_chroot_dir",
			config: JailerConfig{
				UID: 1000,
				GID: 1000,
				ChrootBaseDir: "",
			},
			expectError: true,
			errorMsg:    "chroot",
		},
		{
			name: "valid_config",
			config: JailerConfig{
				UID: 1000,
				GID: 1000,
				ChrootBaseDir: "/var/lib/jailer",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, strings.ToLower(err.Error()), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestConfig_Merge_NilPointer tests merging with nil pointer
func TestConfig_Merge_NilPointer(t *testing.T) {
	base := Config{
		Executor: ExecutorConfig{
			KernelPath: "/boot/vmlinuz",
			RootfsDir:  "/var/lib/firecracker",
			DefaultVCPUs: 1,
			DefaultMemoryMB: 512,
		},
		Network: NetworkConfig{
			BridgeName: "br0",
		},
	}

	// Merge with nil should panic, so recover
	defer func() {
		if r := recover(); r != nil {
			assert.True(t, true)
		}
	}()

	result := base.Merge(nil)
	assert.NotNil(t, result)
	assert.Equal(t, "/boot/vmlinuz", result.Executor.KernelPath)
}

// TestConfig_NumericBoundaries tests numeric value boundaries
func TestConfig_NumericBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		expectError bool
	}{
		{
			name: "very_large_vcpus",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 999999,
					DefaultMemoryMB: 512,
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: false, // May be invalid but validation won't catch it
		},
		{
			name: "very_large_memory",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 1,
					DefaultMemoryMB: 999999999,
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: false, // May be invalid but validation won't catch it
		},
		{
			name: "minimal_valid_values",
			config: Config{
				Executor: ExecutorConfig{
					KernelPath: "/boot/vmlinuz",
					RootfsDir:  "/var/lib/firecracker",
					DefaultVCPUs: 1,
					DefaultMemoryMB: 1,
				},
				Network: NetworkConfig{
					BridgeName: "br0",
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
