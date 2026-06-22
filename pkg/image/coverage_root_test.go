//go:build root
// +build root

package image

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Root-required tests for mount-dependent functions
// Run with: sudo go test -tags root -short ./pkg/image/...
// ============================================================================

func createExt4(t *testing.T, path string, sizeMB int) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(int64(sizeMB)*1024*1024))
	f.Close()
	out, err := exec.Command("mkfs.ext4", "-F", "-q", path).CombinedOutput()
	require.NoError(t, err, "mkfs.ext4 failed: %s", string(out))
}

func mountExt4Helper(t *testing.T, imagePath string) string {
	t.Helper()
	mountDir := t.TempDir()
	out, err := exec.Command("mount", "-o", "loop", imagePath, mountDir).CombinedOutput()
	require.NoError(t, err, "mount failed: %s", string(out))
	t.Cleanup(func() { exec.Command("umount", mountDir).Run() })
	return mountDir
}

// ============================================================================
// injectInitSystem — full mount + copy path
// ============================================================================

func TestInjectInitSystem_WithRealExt4_Tini(t *testing.T) {
	// Ensure tini binary exists on host (symlinked from tini-static)
	if _, err := os.Stat("/usr/bin/tini"); err != nil {
		t.Skip("tini binary not available")
	}

	ip := &ImagePreparer{
		config: &PreparerConfig{
			InitSystem:      "tini",
			InitGracePeriod: 10,
		},
		initInjector: NewInitInjector(&InitSystemConfig{Type: InitSystemTini, GracePeriodSec: 10}),
	}

	ext4Path := filepath.Join(t.TempDir(), "test.ext4")
	createExt4(t, ext4Path, 10)

	mountDir := mountExt4Helper(t, ext4Path)
	require.NoError(t, os.MkdirAll(filepath.Join(mountDir, "sbin"), 0755))
	exec.Command("umount", mountDir).Run()

	err := ip.injectInitSystem(ext4Path)
	require.NoError(t, err)

	// Verify init was injected
	mountDir2 := mountExt4Helper(t, ext4Path)
	defer exec.Command("umount", mountDir2).Run()

	_, err = os.Stat(filepath.Join(mountDir2, "sbin", "tini"))
	assert.NoError(t, err, "tini binary should have been copied into rootfs")
}

func TestInjectInitSystem_NoBinary(t *testing.T) {
	// Uses a non-existent init type that won't find a binary
	ip := &ImagePreparer{
		config: &PreparerConfig{
			InitSystem:      "custom-unknown",
			InitGracePeriod: 10,
		},
		initInjector: NewInitInjector(&InitSystemConfig{Type: "custom-unknown", GracePeriodSec: 10}),
	}

	ext4Path := filepath.Join(t.TempDir(), "test.ext4")
	createExt4(t, ext4Path, 10)

	// No binary found → returns nil gracefully
	err := ip.injectInitSystem(ext4Path)
	assert.NoError(t, err)
}

// ============================================================================
// handleMounts — full mount + mount processing
// ============================================================================

func TestHandleMounts_BindMount_Success(t *testing.T) {
	ip := &ImagePreparer{config: &PreparerConfig{}}

	ext4Path := filepath.Join(t.TempDir(), "test.ext4")
	createExt4(t, ext4Path, 10)

	// Pre-create directory structure in rootfs
	mountDir := mountExt4Helper(t, ext4Path)
	require.NoError(t, os.MkdirAll(filepath.Join(mountDir, "mnt"), 0755))
	exec.Command("umount", mountDir).Run()

	// Create a source directory with files
	sourceDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "hello.txt"), []byte("world"), 0644))

	mounts := []types.Mount{
		{Source: sourceDir, Target: "/mnt/data"},
	}

	err := ip.handleMounts(nil, nil, ext4Path, mounts)
	require.NoError(t, err)

	// Verify file was copied into rootfs
	mountDir2 := mountExt4Helper(t, ext4Path)
	defer exec.Command("umount", mountDir2).Run()

	data, err := os.ReadFile(filepath.Join(mountDir2, "mnt", "data", "hello.txt"))
	require.NoError(t, err)
	assert.Equal(t, "world", string(data))
}

func TestHandleMounts_EmptyTarget(t *testing.T) {
	ip := &ImagePreparer{config: &PreparerConfig{}}

	ext4Path := filepath.Join(t.TempDir(), "test.ext4")
	createExt4(t, ext4Path, 10)

	mounts := []types.Mount{
		{Source: "/tmp", Target: ""},
	}

	err := ip.handleMounts(nil, nil, ext4Path, mounts)
	assert.NoError(t, err)
}

func TestHandleMounts_NilAndEmpty(t *testing.T) {
	ip := &ImagePreparer{config: &PreparerConfig{}}

	ext4Path := filepath.Join(t.TempDir(), "test.ext4")
	createExt4(t, ext4Path, 10)

	err := ip.handleMounts(nil, nil, ext4Path, nil)
	assert.NoError(t, err)

	err = ip.handleMounts(nil, nil, ext4Path, []types.Mount{})
	assert.NoError(t, err)
}

func TestHandleMounts_NonexistentSource(t *testing.T) {
	ip := &ImagePreparer{config: &PreparerConfig{}}

	ext4Path := filepath.Join(t.TempDir(), "test.ext4")
	createExt4(t, ext4Path, 10)

	mounts := []types.Mount{
		{Source: "/nonexistent/source/path", Target: "/data"},
	}

	// Non-existent source → skipped with warning, no error
	err := ip.handleMounts(nil, nil, ext4Path, mounts)
	assert.NoError(t, err)
}

// ============================================================================
// mountExt4 / unmountExt4 — full mount+unmount cycle
// ============================================================================

func TestMountExt4_FullCycle(t *testing.T) {
	ip := &ImagePreparer{config: &PreparerConfig{}}

	ext4Path := filepath.Join(t.TempDir(), "test.ext4")
	createExt4(t, ext4Path, 10)

	mountDir, err := ip.mountExt4(ext4Path)
	require.NoError(t, err)

	// Verify mount point exists and is writable
	_, err = os.Stat(mountDir)
	assert.NoError(t, err)

	testFile := filepath.Join(mountDir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("hello"), 0644))

	// Unmount
	err = ip.unmountExt4(mountDir)
	assert.NoError(t, err)
}

// ============================================================================
// VerifyBootable — test with properly populated ext4
// ============================================================================

func TestVerifyBootable_Populated(t *testing.T) {
	ext4Path := filepath.Join(t.TempDir(), "test.ext4")
	createExt4(t, ext4Path, 10)

	mountDir := mountExt4Helper(t, ext4Path)
	defer exec.Command("umount", mountDir).Run()

	require.NoError(t, os.MkdirAll(filepath.Join(mountDir, "sbin"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(mountDir, "bin"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(mountDir, "etc"), 0755))

	require.NoError(t, os.Symlink("/sbin/init", filepath.Join(mountDir, "init")))
	require.NoError(t, os.WriteFile(filepath.Join(mountDir, "sbin", "init"), []byte("#!/bin/sh"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(mountDir, "sbin", "tini"), []byte("x"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(mountDir, "bin", "sh"), []byte("#!/bin/sh"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(mountDir, "etc", "resolv.conf"), []byte("nameserver 8.8.8.8"), 0644))

	exec.Command("umount", mountDir).Run()

	err := VerifyBootable(ext4Path)
	assert.NoError(t, err)
}
