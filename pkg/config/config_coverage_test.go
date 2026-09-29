package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDuration_ToDuration(t *testing.T) {
	tests := []struct {
		name string
		in   Duration
		want time.Duration
	}{
		{name: "30 seconds", in: Duration(30 * time.Second), want: 30 * time.Second},
		{name: "5 minutes", in: Duration(5 * time.Minute), want: 5 * time.Minute},
		{name: "zero", in: Duration(0), want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.in.ToDuration())
		})
	}
}

func TestDuration_String(t *testing.T) {
	tests := []struct {
		name string
		in   Duration
		want string
	}{
		{name: "90 seconds", in: Duration(90 * time.Second), want: "1m30s"},
		{name: "one hour", in: Duration(time.Hour), want: "1h0m0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.in.String())
		})
	}
}

func TestEnsureDefaultConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("SWARMCRACKER_CONFIG", path)

	// First call should create the file.
	created, err := EnsureDefaultConfig()
	require.NoError(t, err)
	assert.True(t, created, "first call should report a new file was created")

	info, err := os.Stat(path)
	require.NoError(t, err, "default config file should exist")
	assert.False(t, info.IsDir())

	// The written file must parse back through the package loader.
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.Version)
	assert.NotEmpty(t, cfg.Network.BridgeName)

	// Second call should be a no-op because the file already exists.
	created, err = EnsureDefaultConfig()
	require.NoError(t, err)
	assert.False(t, created, "second call should report the file already exists")
}

func TestEnsureDefaultConfig_SaveError(t *testing.T) {
	// Put the config path underneath a regular file so Save's MkdirAll fails.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0600))
	t.Setenv("SWARMCRACKER_CONFIG", filepath.Join(blocker, "config.yaml"))

	created, err := EnsureDefaultConfig()
	assert.False(t, created)
	assert.Error(t, err)
}

// TestDuration_UnmarshalYAML covers the string, day-suffix, and error branches
// of the Duration YAML converter.
func TestDuration_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    time.Duration
		wantErr bool
	}{
		{name: "plain duration", yaml: "d: 30s", want: 30 * time.Second},
		{name: "days suffix", yaml: "d: 1d", want: 24 * time.Hour},
		{name: "invalid plain duration", yaml: "d: abc", wantErr: true},
		{name: "invalid nonzero plain duration", yaml: "d: 2x", wantErr: true},
		{name: "invalid days suffix", yaml: "d: xd", wantErr: true},
		{name: "non scalar node", yaml: "d: [1, 2]", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var holder struct {
				D Duration `yaml:"d"`
			}
			err := yaml.Unmarshal([]byte(tt.yaml), &holder)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, holder.D.ToDuration())
		})
	}
}

func TestConfig_SetDefaults_LegacySocketDir(t *testing.T) {
	cfg := &Config{SocketDir: "/legacy/sockets"}

	cfg.SetDefaults()

	assert.Equal(t, "/legacy/sockets", cfg.Executor.SocketDir)
}

func TestConfig_Merge_OverrideBranches(t *testing.T) {
	base := &Config{}
	override := &Config{
		Executor: ExecutorConfig{
			InitrdPath:      "/initrd",
			RootfsDir:       "/rootfs",
			SocketDir:       "/sockets",
			DefaultVCPUs:    4,
			DefaultMemoryMB: 2048,
		},
		Network: NetworkConfig{
			MaxPacketsPerSec: 5000,
		},
	}

	result := base.Merge(override)

	assert.Equal(t, "/initrd", result.Executor.InitrdPath)
	assert.Equal(t, "/rootfs", result.Executor.RootfsDir)
	assert.Equal(t, "/sockets", result.Executor.SocketDir)
	assert.Equal(t, 4, result.Executor.DefaultVCPUs)
	assert.Equal(t, 2048, result.Executor.DefaultMemoryMB)
	assert.Equal(t, 5000, result.Network.MaxPacketsPerSec)
}

func TestConfig_Validate_OptionalBranches(t *testing.T) {
	validExecutor := ExecutorConfig{
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       "/var/lib/firecracker/rootfs",
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
	}

	tests := []struct {
		name    string
		config  *Config
		wantErr string
	}{
		{
			name:    "missing bridge name",
			config:  &Config{Executor: validExecutor},
			wantErr: "network.bridge_name is required",
		},
		{
			name: "rate limit without max packets",
			config: &Config{
				Executor: validExecutor,
				Network:  NetworkConfig{BridgeName: "swarm-br0", EnableRateLimit: true},
			},
			wantErr: "max_packets_per_sec must be > 0 when rate limiting is enabled",
		},
		{
			name: "jailer enabled with invalid config",
			config: &Config{
				Executor: func() ExecutorConfig {
					e := validExecutor
					e.EnableJailer = true
					return e
				}(),
				Network: NetworkConfig{BridgeName: "swarm-br0"},
			},
			wantErr: "jailer config invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNetworkConfig_Validate_InvalidIPMode(t *testing.T) {
	n := &NetworkConfig{BridgeName: "swarm-br0", IPMode: "bogus"}

	err := n.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ip_mode must be either 'static' or 'dhcp'")
}

func TestConfig_Save_ErrorBranches(t *testing.T) {
	t.Run("directory creation fails", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "not-a-dir")
		require.NoError(t, os.WriteFile(blocker, []byte("x"), 0600))

		err := (&Config{}).Save(filepath.Join(blocker, "config.yaml"))
		assert.Error(t, err)
	})

	t.Run("write fails on existing directory", func(t *testing.T) {
		dir := t.TempDir()

		err := (&Config{}).Save(dir)
		assert.Error(t, err)
	})
}

func TestLoadConfig_UnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 2\n"), 0600))

	cfg, err := LoadConfig(path)

	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "unsupported config version")
}
