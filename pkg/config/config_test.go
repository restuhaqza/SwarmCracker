package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantErr  bool
		validate func(*testing.T, *Config)
	}{
		{
			name: "valid minimal config",
			content: `
kernel_path: "/usr/share/firecracker/vmlinux"
rootfs_dir: "/var/lib/firecracker/rootfs"
`,
			wantErr: false,
			validate: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "/usr/share/firecracker/vmlinux", cfg.KernelPath)
				assert.Equal(t, "/var/lib/firecracker/rootfs", cfg.RootfsDir)
			},
		},
		{
			name: "valid full config",
			content: `
kernel_path: "/usr/share/firecracker/vmlinux"
initrd_path: "/usr/share/firecracker/initrd.img"
rootfs_dir: "/var/lib/firecracker/rootfs"
socket_dir: "/var/run/firecracker"
default_vcpus: 2
default_memory_mb: 1024
enable_jailer: true
jailer:
  uid: 1000
  gid: 1000
  chroot_base_dir: "/srv/jailer"
  netns: "/var/run/netns/firecracker"
network:
  bridge_name: "swarm-br0"
  enable_rate_limit: true
  max_packets_per_sec: 10000
`,
			wantErr: false,
			validate: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "/usr/share/firecracker/vmlinux", cfg.KernelPath)
				assert.Equal(t, "/usr/share/firecracker/initrd.img", cfg.InitrdPath)
				assert.Equal(t, 2, cfg.DefaultVCPUs)
				assert.Equal(t, 1024, cfg.DefaultMemoryMB)
				assert.True(t, cfg.EnableJailer)
				assert.Equal(t, 1000, cfg.Jailer.UID)
				assert.Equal(t, "swarm-br0", cfg.Network.BridgeName)
			},
		},
		{
			name: "invalid YAML",
			content: `
kernel_path: "/usr/share/firecracker/vmlinux"
invalid yaml content {{{
`,
			wantErr: true,
		},
		{
			name:    "empty config",
			content: ``,
			wantErr: false, // Empty YAML is valid, just produces empty struct
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp config file
			tmpFile, err := os.CreateTemp("", "config-*.yaml")
			require.NoError(t, err)
			defer os.Remove(tmpFile.Name())

			// Write content
			_, err = tmpFile.WriteString(tt.content)
			require.NoError(t, err)
			tmpFile.Close()

			// Load config
			cfg, err := LoadConfig(tmpFile.Name())

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, cfg)
			} else {
				require.NoError(t, err)
				require.NotNil(t, cfg)
				if tt.validate != nil {
					tt.validate(t, cfg)
				}
			}
		})
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	cfg, err := LoadConfig("/non/existent/path/config.yaml")

	assert.Error(t, err)
	assert.Nil(t, cfg)
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: &Config{
				Executor: ExecutorConfig{
					KernelPath:      "/usr/share/firecracker/vmlinux",
					RootfsDir:       "/var/lib/firecracker/rootfs",
					DefaultVCPUs:    1,
					DefaultMemoryMB: 512,
				},
				Network: NetworkConfig{
					BridgeName: "swarm-br0",
				},
			},
			wantErr: false,
		},
		{
			name: "missing kernel path",
			config: &Config{
				Executor: ExecutorConfig{
					RootfsDir:       "/var/lib/firecracker/rootfs",
					DefaultVCPUs:    1,
					DefaultMemoryMB: 512,
				},
				Network: NetworkConfig{
					BridgeName: "swarm-br0",
				},
			},
			wantErr: true,
		},
		{
			name: "missing rootfs dir",
			config: &Config{
				Executor: ExecutorConfig{
					KernelPath:      "/usr/share/firecracker/vmlinux",
					DefaultVCPUs:    1,
					DefaultMemoryMB: 512,
				},
				Network: NetworkConfig{
					BridgeName: "swarm-br0",
				},
			},
			wantErr: true,
		},
		{
			name: "invalid VCPUs",
			config: &Config{
				Executor: ExecutorConfig{
					KernelPath:      "/usr/share/firecracker/vmlinux",
					RootfsDir:       "/var/lib/firecracker/rootfs",
					DefaultVCPUs:    0,
					DefaultMemoryMB: 512,
				},
				Network: NetworkConfig{
					BridgeName: "swarm-br0",
				},
			},
			wantErr: true,
		},
		{
			name: "invalid memory",
			config: &Config{
				Executor: ExecutorConfig{
					KernelPath:      "/usr/share/firecracker/vmlinux",
					RootfsDir:       "/var/lib/firecracker/rootfs",
					DefaultVCPUs:    1,
					DefaultMemoryMB: 0,
				},
				Network: NetworkConfig{
					BridgeName: "swarm-br0",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfig_SetDefaults(t *testing.T) {
	config := &Config{
		Executor: ExecutorConfig{
			KernelPath: "/usr/share/firecracker/vmlinux",
		},
	}

	config.SetDefaults()

	assert.Equal(t, 1, config.Executor.DefaultVCPUs)
	assert.Equal(t, 512, config.Executor.DefaultMemoryMB)
	assert.Equal(t, "/var/run/firecracker", config.Executor.SocketDir)
	assert.Equal(t, "/usr/share/firecracker/vmlinux", config.Executor.KernelPath)
	assert.Equal(t, "swarm-br0", config.Network.BridgeName)
	assert.False(t, config.Network.EnableRateLimit)
}

func TestGetDefaultConfigPath(t *testing.T) {
	tests := []struct {
		name     string
		setupEnv func()
		wantPath string
	}{
		{
			name: "default path",
			setupEnv: func() {
				os.Unsetenv("SWARMCRACKER_CONFIG")
			},
			wantPath: "/etc/swarmcracker/config.yaml",
		},
		{
			name: "env override",
			setupEnv: func() {
				os.Setenv("SWARMCRACKER_CONFIG", "/custom/config.yaml")
			},
			wantPath: "/custom/config.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupEnv != nil {
				tt.setupEnv()
			}

			path := GetDefaultConfigPath()
			assert.Equal(t, tt.wantPath, path)
		})
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	// Test loading from environment variable
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
kernel_path: "/usr/share/firecracker/vmlinux"
rootfs_dir: "/var/lib/firecracker/rootfs"
`

	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	os.Setenv("SWARMCRACKER_CONFIG", configPath)
	defer os.Unsetenv("SWARMCRACKER_CONFIG")

	cfg, err := LoadConfigFromEnv()

	require.NoError(t, err)
	assert.Equal(t, "/usr/share/firecracker/vmlinux", cfg.KernelPath)
}

func TestLoadConfigFromEnv_NotSet(t *testing.T) {
	os.Unsetenv("SWARMCRACKER_CONFIG")

	cfg, err := LoadConfigFromEnv()

	// Should try default path and fail
	assert.Error(t, err)
	assert.Nil(t, cfg)
}

func TestConfig_Merge(t *testing.T) {
	base := &Config{
		Executor: ExecutorConfig{
			KernelPath:      "/kernel",
			RootfsDir:       "/rootfs",
			DefaultVCPUs:    1,
			DefaultMemoryMB: 512,
			EnableJailer:    false,
		},
		Network: NetworkConfig{
			BridgeName: "br0",
		},
	}

	override := &Config{
		Executor: ExecutorConfig{
			DefaultVCPUs:    2,
			DefaultMemoryMB: 1024,
			EnableJailer:    true,
		},
		Network: NetworkConfig{
			BridgeName: "br1",
		},
	}

	result := base.Merge(override)

	assert.Equal(t, "/kernel", result.Executor.KernelPath) // from base
	assert.Equal(t, "/rootfs", result.Executor.RootfsDir)  // from base
	assert.Equal(t, 2, result.Executor.DefaultVCPUs)       // from override
	assert.Equal(t, 1024, result.Executor.DefaultMemoryMB) // from override
	assert.True(t, result.Executor.EnableJailer)           // from override
	assert.Equal(t, "br1", result.Network.BridgeName)      // from override
}

func TestNetworkConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  NetworkConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: NetworkConfig{
				BridgeName: "swarm-br0",
			},
			wantErr: false,
		},
		{
			name:    "missing bridge name",
			config:  NetworkConfig{},
			wantErr: true,
		},
		{
			name: "invalid rate limit",
			config: NetworkConfig{
				BridgeName:       "swarm-br0",
				EnableRateLimit:  true,
				MaxPacketsPerSec: 0,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestJailerConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  JailerConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: JailerConfig{
				UID:           1000,
				GID:           1000,
				ChrootBaseDir: "/srv/jailer",
			},
			wantErr: false,
		},
		{
			name: "missing UID",
			config: JailerConfig{
				GID:           1000,
				ChrootBaseDir: "/srv/jailer",
			},
			wantErr: true,
		},
		{
			name: "missing GID",
			config: JailerConfig{
				UID:           1000,
				ChrootBaseDir: "/srv/jailer",
			},
			wantErr: true,
		},
		{
			name: "missing chroot dir",
			config: JailerConfig{
				UID: 1000,
				GID: 1000,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfig_Save(t *testing.T) {
	config := &Config{
		Executor: ExecutorConfig{
			KernelPath:      "/usr/share/firecracker/vmlinux",
			RootfsDir:       "/var/lib/firecracker/rootfs",
			DefaultVCPUs:    2,
			DefaultMemoryMB: 1024,
		},
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	err := config.Save(configPath)

	require.NoError(t, err)

	// Verify file was created
	_, err = os.Stat(configPath)
	assert.NoError(t, err)

	// Load and verify content
	loaded, err := LoadConfig(configPath)
	require.NoError(t, err)

	assert.Equal(t, config.Executor.KernelPath, loaded.Executor.KernelPath)
	assert.Equal(t, config.Executor.RootfsDir, loaded.Executor.RootfsDir)
	assert.Equal(t, config.Executor.DefaultVCPUs, loaded.Executor.DefaultVCPUs)
	assert.Equal(t, config.Executor.DefaultMemoryMB, loaded.Executor.DefaultMemoryMB)
}

func TestConfig_String(t *testing.T) {
	config := &Config{
		Executor: ExecutorConfig{
			KernelPath:      "/kernel",
			RootfsDir:       "/rootfs",
			DefaultVCPUs:    2,
			DefaultMemoryMB: 1024,
		},
	}

	str := config.String()

	assert.Contains(t, str, "KernelPath: /kernel")
	assert.Contains(t, str, "RootfsDir: /rootfs")
	assert.Contains(t, str, "DefaultVCPUs: 2")
}

// Benchmark config loading
func BenchmarkLoadConfig(b *testing.B) {
	tmpDir := b.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
kernel_path: "/usr/share/firecracker/vmlinux"
rootfs_dir: "/var/lib/firecracker/rootfs"
socket_dir: "/var/run/firecracker"
default_vcpus: 2
default_memory_mb: 1024
network:
  bridge_name: "swarm-br0"
  enable_rate_limit: true
`

	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(b, err)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = LoadConfig(configPath)
	}
}
