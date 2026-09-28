package golden

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureBaseDirs(t *testing.T) {
	rootfs := t.TempDir()

	require.NoError(t, ensureBaseDirs(rootfs))

	for _, d := range []string{"tmp", "var/tmp", "run", "var/log", "root"} {
		info, err := os.Stat(filepath.Join(rootfs, d))
		require.NoError(t, err, d)
		assert.True(t, info.IsDir(), d)
	}
	info, err := os.Stat(filepath.Join(rootfs, "tmp"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o777), info.Mode().Perm())
	assert.NotZero(t, info.Mode()&os.ModeSticky, "tmp must be sticky")
}

func TestEnsurePolicyRCD_CreatesAndCleansUp(t *testing.T) {
	rootfs := t.TempDir()

	cleanup := ensurePolicyRCD(rootfs)
	path := filepath.Join(rootfs, "usr", "sbin", "policy-rc.d")
	require.FileExists(t, path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "exit 101")

	cleanup()
	assert.NoFileExists(t, path)
}

func TestEnsurePolicyRCD_PreservesExisting(t *testing.T) {
	rootfs := t.TempDir()
	path := filepath.Join(rootfs, "usr", "sbin", "policy-rc.d")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755))

	cleanup := ensurePolicyRCD(rootfs)
	cleanup() // must not remove a pre-existing file

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "exit 0")
}

func TestTail(t *testing.T) {
	assert.Equal(t, "hello", tail("hello", 10))
	assert.Equal(t, "lo", tail("hello", 2))
	assert.Equal(t, "", tail("", 5))
}
