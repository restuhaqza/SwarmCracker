package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateConfigPath verifies that `config validate <path>` validates the
// given file (and rejects a missing or malformed one) instead of silently
// falling back to the default config.
func TestValidateConfigPath(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{}
	cfg.SetDefaults()
	cfg.Executor.KernelPath = "/usr/share/firecracker/vmlinux"
	cfg.Executor.RootfsDir = "/var/lib/firecracker/rootfs"
	require.NoError(t, cfg.Save(good))

	require.NoError(t, validateConfig(good), "an explicit valid path should pass")

	err := validateConfig(filepath.Join(dir, "missing.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	bad := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("executor: [not-a-map"), 0o600))
	require.Error(t, validateConfig(bad))
}
