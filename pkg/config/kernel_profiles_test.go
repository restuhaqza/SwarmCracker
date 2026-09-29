package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetDefaults_KernelProfiles(t *testing.T) {
	c := &Config{}
	c.SetDefaults()

	require.NotNil(t, c.Executor.KernelProfiles)
	assert.Equal(t, DefaultGuestRuntimeKernel, c.Executor.KernelProfiles[KernelProfileGuestRuntime])
}

func TestSetDefaults_PreservesConfiguredKernelProfiles(t *testing.T) {
	c := &Config{Executor: ExecutorConfig{KernelProfiles: map[string]string{"custom": "/k"}}}
	c.SetDefaults()

	assert.Equal(t, "/k", c.Executor.KernelProfiles["custom"])
	assert.NotContains(t, c.Executor.KernelProfiles, KernelProfileGuestRuntime,
		"an explicit registry must not be merged with the defaults")
}

func TestLoadConfig_KernelProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
version: 1
executor:
  kernel_path: /k
  rootfs_dir: /r
  default_vcpus: 1
  default_memory_mb: 512
  kernel_profiles:
    guest-runtime-6.1: /usr/share/firecracker/vmlinux-runtime
    custom: /opt/vmlinux-custom
network:
  bridge_name: br0
`), 0o600))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	assert.Equal(t, "/usr/share/firecracker/vmlinux-runtime", cfg.Executor.KernelProfiles[KernelProfileGuestRuntime])
	assert.Equal(t, "/opt/vmlinux-custom", cfg.Executor.KernelProfiles["custom"])
}

func TestMerge_KernelProfiles(t *testing.T) {
	base := &Config{Executor: ExecutorConfig{
		KernelProfiles: map[string]string{"a": "1", "b": "2"},
	}}
	override := &Config{Executor: ExecutorConfig{
		KernelProfiles: map[string]string{"b": "3", "c": "4"},
	}}

	got := base.Merge(override)

	assert.Equal(t, "1", got.Executor.KernelProfiles["a"])
	assert.Equal(t, "3", got.Executor.KernelProfiles["b"], "override wins")
	assert.Equal(t, "4", got.Executor.KernelProfiles["c"])
	assert.Equal(t, "2", base.Executor.KernelProfiles["b"], "base must not be mutated")
}

func TestMerge_EmptyKernelProfilesKeepsBase(t *testing.T) {
	base := &Config{Executor: ExecutorConfig{
		KernelProfiles: map[string]string{"a": "1"},
	}}
	got := base.Merge(&Config{})
	assert.Equal(t, "1", got.Executor.KernelProfiles["a"])
}

func TestSave_EmitsKernelProfiles(t *testing.T) {
	c := &Config{
		Executor: ExecutorConfig{
			KernelPath:      "/k",
			RootfsDir:       "/r",
			DefaultVCPUs:    1,
			DefaultMemoryMB: 512,
			KernelProfiles: map[string]string{
				KernelProfileGuestRuntime: DefaultGuestRuntimeKernel,
			},
		},
		Network: NetworkConfig{BridgeName: "br0"},
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, c.Save(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "kernel_profiles:")
	assert.Contains(t, string(data), "guest-runtime-6.1: /usr/share/firecracker/vmlinux-runtime")
}
