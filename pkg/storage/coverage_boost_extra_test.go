//go:build !integration

package storage

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInjectSecrets_DebugfsFail tests InjectSecrets when debugfs command fails
// Note: debugfs exits with code 0 even for errors like missing ext4 file,
// so we mock execCommand to simulate actual failure
func TestInjectSecrets_DebugfsFail(t *testing.T) {
	origCommand := execCommand
	origMkdirTemp := osMkdirTemp
	origRemoveAll := osRemoveAllStore
	defer func() {
		execCommand = origCommand
		osMkdirTemp = origMkdirTemp
		osRemoveAllStore = origRemoveAll
	}()

	// Create temp dir for test
	tmpDir := t.TempDir()
	osMkdirTemp = func(dir string, pattern string) (string, error) {
		return tmpDir, nil
	}
	osRemoveAllStore = func(path string) error { return nil }

	// Mock execCommand to fail for debugfs
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "debugfs" {
			return exec.Command("false") // Will fail with exit code 1
		}
		return exec.Command(name, args...)
	}

	sm := NewSecretManager("", "")

	ctx := context.Background()
	secrets := []types.SecretRef{
		{Name: "secret1", Target: "/run/secrets/s1", Data: []byte("data")},
	}

	err := sm.InjectSecrets(ctx, "task-123", secrets, "/nonexistent/rootfs.ext4")
	assert.Error(t, err, "InjectSecrets should fail when debugfs fails")
	assert.Contains(t, err.Error(), "debugfs")
}

// TestInjectSecrets_DebugfsSuccess tests InjectSecrets with single secret using mock
func TestInjectSecrets_DebugfsSuccess(t *testing.T) {
	origCommand := execCommand
	origMkdirTemp := osMkdirTemp
	origRemoveAll := osRemoveAllStore
	defer func() {
		execCommand = origCommand
		osMkdirTemp = origMkdirTemp
		osRemoveAllStore = origRemoveAll
	}()

	tmpDir := t.TempDir()
	osMkdirTemp = func(dir string, pattern string) (string, error) {
		return tmpDir, nil
	}
	osRemoveAllStore = func(path string) error { return nil }

	// Mock execCommand to succeed for debugfs
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "debugfs" {
			return exec.Command("echo", "Allocated inode")
		}
		return exec.Command(name, args...)
	}

	sm := NewSecretManager("", "")
	ctx := context.Background()
	secrets := []types.SecretRef{
		{Name: "single-secret", Target: "/secrets/s1", Data: []byte("secret-data")},
	}

	err := sm.InjectSecrets(ctx, "task-success", secrets, "/tmp/rootfs.ext4")
	assert.NoError(t, err, "InjectSecrets should succeed with mock debugfs")
}

// TestInjectConfigs_DebugfsSuccess tests InjectConfigs with single config using mock
func TestInjectConfigs_DebugfsSuccess(t *testing.T) {
	origCommand := execCommand
	origMkdirTemp := osMkdirTemp
	origRemoveAll := osRemoveAllStore
	defer func() {
		execCommand = origCommand
		osMkdirTemp = origMkdirTemp
		osRemoveAllStore = origRemoveAll
	}()

	tmpDir := t.TempDir()
	osMkdirTemp = func(dir string, pattern string) (string, error) {
		return tmpDir, nil
	}
	osRemoveAllStore = func(path string) error { return nil }

	// Mock execCommand to succeed for debugfs
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "debugfs" {
			return exec.Command("echo", "Allocated inode")
		}
		return exec.Command(name, args...)
	}

	sm := NewSecretManager("", "")
	ctx := context.Background()
	configs := []types.ConfigRef{
		{Name: "single-config", Target: "/config/c1", Data: []byte("config-data")},
	}

	err := sm.InjectConfigs(ctx, "task-success", configs, "/tmp/rootfs.ext4")
	assert.NoError(t, err, "InjectConfigs should succeed with mock debugfs")
}

// TestInjectConfigs_DebugfsFail tests InjectConfigs when debugfs fails
func TestInjectConfigs_DebugfsFail(t *testing.T) {
	origCommand := execCommand
	origMkdirTemp := osMkdirTemp
	origRemoveAll := osRemoveAllStore
	defer func() {
		execCommand = origCommand
		osMkdirTemp = origMkdirTemp
		osRemoveAllStore = origRemoveAll
	}()

	tmpDir := t.TempDir()
	osMkdirTemp = func(dir string, pattern string) (string, error) {
		return tmpDir, nil
	}
	osRemoveAllStore = func(path string) error { return nil }

	// Mock execCommand to fail for debugfs
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "debugfs" {
			return exec.Command("false")
		}
		return exec.Command(name, args...)
	}

	sm := NewSecretManager("", "")
	ctx := context.Background()
	configs := []types.ConfigRef{
		{Name: "config1", Target: "/config/c1", Data: []byte("data")},
	}

	err := sm.InjectConfigs(ctx, "task-123", configs, "/nonexistent/rootfs.ext4")
	assert.Error(t, err, "InjectConfigs should fail when debugfs fails")
	assert.Contains(t, err.Error(), "debugfs")
}

// TestMountRootfs_DeprecatedStub tests mountRootfs deprecated stub behavior
func TestMountRootfs_DeprecatedStub(t *testing.T) {
	sm := NewSecretManager("", "")

	// mountRootfs is now a deprecated stub that just creates a temp dir
	// (replaced by injectFileViaDebugfs for CVR-1.6 fix)
	tmpFile := filepath.Join(t.TempDir(), "not-ext4.img")
	require.NoError(t, os.WriteFile(tmpFile, []byte("not an ext4 image"), 0644))

	mountDir, err := sm.mountRootfs(tmpFile)
	// Deprecated stub just creates temp dir - always succeeds
	assert.NoError(t, err, "mountRootfs deprecated stub should succeed")
	assert.NotEmpty(t, mountDir, "should return temp dir path")
	if mountDir != "" {
		os.RemoveAll(mountDir)
	}
}

// TestMountRootfs_StubSuccess tests mountRootfs deprecated stub with non-existent file
func TestMountRootfs_StubSuccess(t *testing.T) {
	sm := NewSecretManager("", "")

	// mountRootfs is now a deprecated stub - just creates temp dir regardless
	mountDir, err := sm.mountRootfs("/nonexistent/file.ext4")
	assert.NoError(t, err, "mountRootfs deprecated stub should succeed")
	assert.NotEmpty(t, mountDir, "should return temp dir path")
	os.RemoveAll(mountDir)
}

// TestUnmountRootfs_NilDir tests unmountRootfs with empty path
func TestUnmountRootfs_NilDir(t *testing.T) {
	sm := NewSecretManager("", "")

	// unmountRootfs should handle empty path gracefully
	sm.unmountRootfs("")
	// No error returned, just cleanup
}

// TestUnmountRootfs_NonExistent tests unmountRootfs with non-existent path
func TestUnmountRootfs_NonExistent(t *testing.T) {
	sm := NewSecretManager("", "")

	// unmountRootfs calls umount and RemoveAll
	sm.unmountRootfs("/nonexistent/mount")
	// No error returned, just cleanup
}

// TestGetDriverMeta_InvalidType tests getDriverMeta with invalid type
func TestGetDriverMeta_InvalidType(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	// Test with a type that has no driver
	_, err = vm.getDriverMeta(VolumeType("invalid-type"))
	assert.Error(t, err, "getDriverMeta should fail for invalid type")
	assert.Contains(t, err.Error(), "no driver")
}

// TestGetDriverMeta_EmptyType tests getDriverMeta with empty type (uses default)
func TestGetDriverMeta_EmptyType(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	// Empty type should use default (dir)
	meta, err := vm.getDriverMeta("")
	assert.NoError(t, err, "getDriverMeta should succeed for empty type")
	assert.NotNil(t, meta)
}

// TestMountVolume_NilVolume tests MountVolume with nil volume
func TestMountVolume_NilVolume(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	err = vm.MountVolume(context.Background(), nil, "/rootfs", "/data")
	assert.Error(t, err, "MountVolume should fail with nil volume")
	assert.Contains(t, err.Error(), "nil")
}

// TestMountVolume_Success tests MountVolume with valid volume
func TestMountVolume_Success(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	// Create a volume
	vol, err := vm.CreateVolume(context.Background(), "mount-test-vol", "task-1", 50)
	require.NoError(t, err)

	// Create a fake rootfs directory
	rootfs := t.TempDir()

	// Mount should copy volume data to rootfs
	err = vm.MountVolume(context.Background(), vol, rootfs, "/app/data")
	assert.NoError(t, err, "MountVolume should succeed")

	// Verify target directory created
	assert.DirExists(t, filepath.Join(rootfs, "app", "data"))
}

// TestUnmountVolume_Success tests UnmountVolume with valid volume
func TestUnmountVolume_Success(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	// Create a volume
	vol, err := vm.CreateVolume(context.Background(), "unmount-test-vol", "task-1", 50)
	require.NoError(t, err)

	// Create a fake rootfs directory with data
	rootfs := t.TempDir()
	dataDir := filepath.Join(rootfs, "app", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "file.txt"), []byte("data"), 0644))

	// Unmount (readOnly=false) should sync data back
	err = vm.UnmountVolume(context.Background(), vol, rootfs, "/app/data", false)
	assert.NoError(t, err, "UnmountVolume should succeed")
}

// TestUnmountVolume_ReadOnly tests UnmountVolume with readOnly=true
func TestUnmountVolume_ReadOnly(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	// Create a volume
	vol, err := vm.CreateVolume(context.Background(), "readonly-test-vol", "task-1", 50)
	require.NoError(t, err)

	// Create a fake rootfs directory
	rootfs := t.TempDir()
	dataDir := filepath.Join(rootfs, "app", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0755))

	// Unmount with readOnly=true should skip sync
	err = vm.UnmountVolume(context.Background(), vol, rootfs, "/app/data", true)
	assert.NoError(t, err, "UnmountVolume with readOnly should succeed")
}

// TestDeleteVolume_Success tests DeleteVolume with valid volume
func TestDeleteVolume_Success(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	// Create a volume
	vol, err := vm.CreateVolume(context.Background(), "delete-test-vol", "task-1", 50)
	require.NoError(t, err)

	// Delete should succeed
	err = vm.DeleteVolume(context.Background(), vol.Name)
	assert.NoError(t, err, "DeleteVolume should succeed")

	// Verify volume no longer exists
	_, err = vm.GetVolume(vol.Name)
	assert.Error(t, err, "Volume should not exist after delete")
}

// TestSnapshotVolume_Success tests SnapshotVolume with valid volume
func TestSnapshotVolume_Success(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	// Create a volume with some data
	vol, err := vm.CreateVolume(context.Background(), "snapshot-test-vol", "task-1", 50)
	require.NoError(t, err)

	// Get the directory driver to add data
	driver, err := vm.GetDriver(VolumeTypeDir)
	require.NoError(t, err)
	dirDriver := driver.(*DirectoryDriver)
	dataPath := dirDriver.dataPath(vol.Name)
	require.NoError(t, os.WriteFile(filepath.Join(dataPath, "file.txt"), []byte("snapshot data"), 0644))

	// Snapshot should succeed
	snap, err := vm.SnapshotVolume(context.Background(), vol.Name)
	assert.NoError(t, err, "SnapshotVolume should succeed")
	assert.NotNil(t, snap)
	assert.FileExists(t, snap.Path)
}

// TestRestoreVolume_Success tests RestoreVolume with valid snapshot
func TestRestoreVolume_Success(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	// Create a volume with some data
	vol, err := vm.CreateVolume(context.Background(), "restore-test-vol", "task-1", 50)
	require.NoError(t, err)

	// Get the directory driver to add data
	driver, err := vm.GetDriver(VolumeTypeDir)
	require.NoError(t, err)
	dirDriver := driver.(*DirectoryDriver)
	dataPath := dirDriver.dataPath(vol.Name)
	require.NoError(t, os.WriteFile(filepath.Join(dataPath, "file.txt"), []byte("original data"), 0644))

	// Create a snapshot
	snap, err := vm.SnapshotVolume(context.Background(), vol.Name)
	require.NoError(t, err)

	// Modify the data
	require.NoError(t, os.WriteFile(filepath.Join(dataPath, "file.txt"), []byte("modified data"), 0644))

	// Restore should succeed
	err = vm.RestoreVolume(context.Background(), vol.Name, snap)
	assert.NoError(t, err, "RestoreVolume should succeed")

	// Verify original data restored
	data, err := os.ReadFile(filepath.Join(dataPath, "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "original data", string(data))
}

// TestUnmountVolume_NilVolume tests UnmountVolume with nil volume
func TestUnmountVolume_NilVolume(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	err = vm.UnmountVolume(context.Background(), nil, "/rootfs", "/data", false)
	assert.Error(t, err, "UnmountVolume should fail with nil volume")
	assert.Contains(t, err.Error(), "nil")
}

// TestUnmountVolume_NotFound tests UnmountVolume with non-existent volume
func TestUnmountVolume_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	vol := &Volume{Name: "nonexistent-volume"}
	err = vm.UnmountVolume(context.Background(), vol, "/rootfs", "/data", false)
	assert.Error(t, err, "UnmountVolume should fail for non-existent volume")
	assert.Contains(t, err.Error(), "not found")
}

// TestDeleteVolume_NotFound tests DeleteVolume with non-existent volume
func TestDeleteVolume_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	err = vm.DeleteVolume(context.Background(), "nonexistent-volume")
	assert.Error(t, err, "DeleteVolume should fail for non-existent volume")
	assert.Contains(t, err.Error(), "not found")
}

// TestSnapshotVolume_NotFound tests SnapshotVolume with non-existent volume
func TestSnapshotVolume_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	_, err = vm.SnapshotVolume(context.Background(), "nonexistent-volume")
	assert.Error(t, err, "SnapshotVolume should fail for non-existent volume")
	assert.Contains(t, err.Error(), "not found")
}

// TestRestoreVolume_NotFound tests RestoreVolume with non-existent volume
func TestRestoreVolume_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	snap := &Snapshot{ID: "snap-1", Volume: "nonexistent-volume"}
	err = vm.RestoreVolume(context.Background(), "nonexistent-volume", snap)
	assert.Error(t, err, "RestoreVolume should fail for non-existent volume")
	assert.Contains(t, err.Error(), "not found")
}

// TestRestoreVolume_NilSnapshot tests RestoreVolume with nil snapshot
func TestRestoreVolume_NilSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	// Create a volume first
	_, err = vm.CreateVolume(context.Background(), "test-vol", "task-1", 100)
	require.NoError(t, err)

	err = vm.RestoreVolume(context.Background(), "test-vol", nil)
	assert.Error(t, err, "RestoreVolume should fail with nil snapshot")
}

// TestNewVolumeManager_EmptyDir tests NewVolumeManager with empty directory
func TestNewVolumeManager_EmptyDir(t *testing.T) {
	vm, err := NewVolumeManager("")
	assert.NoError(t, err, "NewVolumeManager should succeed with empty directory")
	assert.NotNil(t, vm)
	// Should use default directory
}

// TestNewVolumeManager_InvalidDir tests NewVolumeManager with invalid directory
func TestNewVolumeManager_InvalidDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("Skipping: running as root can create directories anywhere")
	}

	// Create a read-only parent to force MkdirAll failure
	tmpDir := t.TempDir()
	readOnlyParent := filepath.Join(tmpDir, "readonly")
	require.NoError(t, os.Mkdir(readOnlyParent, 0555))

	vm, err := NewVolumeManager(filepath.Join(readOnlyParent, "volumes"))
	assert.Error(t, err, "NewVolumeManager should fail with read-only parent")
	assert.Nil(t, vm)
}

// TestCopyDirectory_NonExistentSrc tests copyDirectory with non-existent source
func TestCopyDirectory_NonExistentSrc(t *testing.T) {
	err := copyDirectory("/nonexistent/src", t.TempDir())
	assert.Error(t, err, "copyDirectory should fail for non-existent source")
}

// TestCopyDirectory_NonExistentDst tests copyDirectory with invalid destination
func TestCopyDirectory_NonExistentDst(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("Skipping: running as root can create directories anywhere")
	}

	src := t.TempDir()
	// Create a file in source
	require.NoError(t, os.WriteFile(filepath.Join(src, "file.txt"), []byte("data"), 0644))

	// Create a read-only parent for destination
	tmpDir := t.TempDir()
	readOnlyParent := filepath.Join(tmpDir, "readonly")
	require.NoError(t, os.Mkdir(readOnlyParent, 0555))

	dst := filepath.Join(readOnlyParent, "dst")
	err := copyDirectory(src, dst)
	assert.Error(t, err, "copyDirectory should fail for read-only destination parent")
}

// TestCopyDirectory_RecursiveCopy tests copyDirectory with nested structure
func TestCopyDirectory_RecursiveCopy(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Create nested structure
	nestedDir := filepath.Join(src, "nested", "deep")
	require.NoError(t, os.MkdirAll(nestedDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(nestedDir, "file.txt"), []byte("nested data"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "top.txt"), []byte("top data"), 0644))

	err := copyDirectory(src, filepath.Join(dst, "copied"))
	require.NoError(t, err, "copyDirectory should succeed")

	// Verify structure copied
	assert.FileExists(t, filepath.Join(dst, "copied", "top.txt"))
	assert.FileExists(t, filepath.Join(dst, "copied", "nested", "deep", "file.txt"))
}

// TestCopyFile_NonExistentSrc tests copyFile with non-existent source
func TestCopyFile_NonExistentSrc(t *testing.T) {
	err := copyFile("/nonexistent/file.txt", filepath.Join(t.TempDir(), "dst.txt"))
	assert.Error(t, err, "copyFile should fail for non-existent source")
}

// TestCopyFile_Success tests successful file copy
func TestCopyFile_Success(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.txt")
	dst := filepath.Join(t.TempDir(), "dst.txt")

	require.NoError(t, os.WriteFile(src, []byte("test content"), 0644))

	err := copyFile(src, dst)
	require.NoError(t, err, "copyFile should succeed")
	assert.FileExists(t, dst)

	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "test content", string(data))
}

// TestCopyFile_PreserveMode tests copyFile preserves file mode
func TestCopyFile_PreserveMode(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.sh")
	dst := filepath.Join(t.TempDir(), "dst.sh")

	require.NoError(t, os.WriteFile(src, []byte("#!/bin/bash"), 0755))

	err := copyFile(src, dst)
	require.NoError(t, err, "copyFile should succeed")

	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())
}

// TestCopyFile_Overwrite tests copyFile can overwrite existing file
func TestCopyFile_Overwrite(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.txt")
	dst := filepath.Join(t.TempDir(), "dst.txt")

	require.NoError(t, os.WriteFile(src, []byte("new content"), 0644))
	require.NoError(t, os.WriteFile(dst, []byte("old content"), 0644))

	err := copyFile(src, dst)
	require.NoError(t, err, "copyFile should succeed")

	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "new content", string(data))
}

// TestCopyFile_SrcStatError tests copyFile when source stat fails
func TestCopyFile_SrcStatError(t *testing.T) {
	// Test stat error by using a path that will fail
	err := copyFile("/proc/nonexistent", filepath.Join(t.TempDir(), "dst.txt"))
	// Should fail due to stat or readfile error
	assert.Error(t, err, "copyFile should fail for invalid source")
}

// TestDirSizeBytes_NonExistent tests dirSizeBytes with non-existent path
func TestDirSizeBytes_NonExistent(t *testing.T) {
	_, err := dirSizeBytes("/nonexistent/path")
	assert.Error(t, err, "dirSizeBytes should fail for non-existent path")
}

// TestDirSizeBytes_EmptyDir tests dirSizeBytes with empty directory
func TestDirSizeBytes_EmptyDir(t *testing.T) {
	size, err := dirSizeBytes(t.TempDir())
	require.NoError(t, err, "dirSizeBytes should succeed for empty dir")
	assert.Equal(t, int64(0), size, "Empty directory should have 0 size")
}

// TestDirSizeBytes_WithFiles tests dirSizeBytes with files
func TestDirSizeBytes_WithFiles(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("12345"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("67890"), 0644))

	size, err := dirSizeBytes(tmpDir)
	require.NoError(t, err, "dirSizeBytes should succeed")
	assert.Equal(t, int64(10), size, "Size should be sum of file sizes")
}

// TestDirSizeMB_NonExistent tests dirSizeMB with non-existent path
func TestDirSizeMB_NonExistent(t *testing.T) {
	_, err := dirSizeMB("/nonexistent/path")
	assert.Error(t, err, "dirSizeMB should fail for non-existent path")
}

// TestGetDriver_InvalidType tests getDriver with invalid type
func TestGetDriver_InvalidType(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	_, err = vm.getDriver(VolumeType("invalid"))
	assert.Error(t, err, "getDriver should fail for invalid type")
}

// TestGetDriver_EmptyType tests getDriver with empty type (uses default)
func TestGetDriver_EmptyType(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	driver, err := vm.getDriver("")
	assert.NoError(t, err, "getDriver should succeed for empty type (uses default)")
	assert.NotNil(t, driver)
	assert.Equal(t, VolumeTypeDir, driver.Type())
}

// TestGetVolume_NotFound tests GetVolume with non-existent volume
func TestGetVolume_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	_, err = vm.GetVolume("nonexistent-volume")
	assert.Error(t, err, "GetVolume should fail for non-existent volume")
}

// TestCreateVolumeWithOptions_EmptyName tests CreateVolumeWithOptions with empty name
func TestCreateVolumeWithOptions_EmptyName(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	_, err = vm.CreateVolumeWithOptions(context.Background(), "", "task-1", CreateOptions{})
	assert.Error(t, err, "CreateVolumeWithOptions should fail with empty name")
}

// TestGetVolumeInfo_InvalidType tests GetVolumeInfo with invalid type
func TestGetVolumeInfo_InvalidType(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	_, err = vm.GetVolumeInfo(context.Background(), "vol", VolumeType("invalid"))
	assert.Error(t, err, "GetVolumeInfo should fail for invalid type")
}

// TestSanitizeVolumeName_CoverageExtra tests sanitizeVolumeName with various inputs
func TestSanitizeVolumeName_CoverageExtra(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"with/slash", "with_slash"},
		{"with\\backslash", "with_backslash"},
		{"with:colon", "with_colon"},
		{"with..dots", "with_dots"},
		{"  trimmed  ", "trimmed"},
		{"", "unnamed"},
		{"..", "_"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeVolumeName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestIsVolumeReference_CoverageExtra tests IsVolumeReference
func TestIsVolumeReference_CoverageExtra(t *testing.T) {
	assert.True(t, IsVolumeReference("volume://myvol"))
	assert.True(t, IsVolumeReference("simple-name"))
	assert.False(t, IsVolumeReference("/absolute/path"))
	assert.False(t, IsVolumeReference("./relative/path"))
	assert.False(t, IsVolumeReference("../parent/path"))
}

// TestExtractVolumeName_CoverageExtra tests ExtractVolumeName
func TestExtractVolumeName_CoverageExtra(t *testing.T) {
	assert.Equal(t, "myvol", ExtractVolumeName("volume://myvol"))
	assert.Equal(t, "simple", ExtractVolumeName("simple"))
	assert.Equal(t, "/path", ExtractVolumeName("/path"))
}

// TestNowUTC tests nowUTC returns UTC time
func TestNowUTC(t *testing.T) {
	now := nowUTC()
	assert.True(t, now.Equal(now.UTC()), "nowUTC should return UTC time")
}

// TestEnsureAbsolutePath_CoverageExtra tests ensureAbsolutePath
func TestEnsureAbsolutePath_CoverageExtra(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "/"},
		{"relative", "/relative"},
		{"relative/path", "/relative/path"},
		{"/absolute", "/absolute"},
		{"/absolute/path", "/absolute/path"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ensureAbsolutePath(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestVolumeManager_GetDriver_CoverageExtra tests GetDriver method
func TestVolumeManager_GetDriver_CoverageExtra(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	driver, err := vm.GetDriver(VolumeTypeDir)
	assert.NoError(t, err, "GetDriver should succeed for valid type")
	assert.NotNil(t, driver)
}

// TestListVolumes_Empty tests ListVolumes with no volumes
func TestListVolumes_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	volumes, err := vm.ListVolumes(context.Background())
	assert.NoError(t, err, "ListVolumes should succeed even with no volumes")
	assert.Empty(t, volumes)
}

// TestListVolumeInfos_Empty tests ListVolumeInfos with no volumes
func TestListVolumeInfos_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	infos, err := vm.ListVolumeInfos(context.Background())
	assert.NoError(t, err, "ListVolumeInfos should succeed even with no volumes")
	assert.Empty(t, infos)
}

// TestNewDirectoryDriver_EmptyDir tests NewDirectoryDriver with empty directory
func TestNewDirectoryDriver_EmptyDir(t *testing.T) {
	driver, err := NewDirectoryDriver("")
	assert.Error(t, err, "NewDirectoryDriver should fail with empty directory")
	assert.Nil(t, driver)
}

// TestNewDirectoryDriver_InvalidDir tests NewDirectoryDriver with invalid directory
func TestNewDirectoryDriver_InvalidDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("Skipping: running as root can create directories anywhere")
	}

	tmpDir := t.TempDir()
	readOnlyParent := filepath.Join(tmpDir, "readonly")
	require.NoError(t, os.Mkdir(readOnlyParent, 0555))

	driver, err := NewDirectoryDriver(filepath.Join(readOnlyParent, "driver"))
	assert.Error(t, err, "NewDirectoryDriver should fail with read-only parent")
	assert.Nil(t, driver)
}

// TestDirectoryDriver_Create_EmptyName tests DirectoryDriver Create with empty name
func TestDirectoryDriver_Create_EmptyName(t *testing.T) {
	driver, err := NewDirectoryDriver(t.TempDir())
	require.NoError(t, err)

	_, err = driver.Create(context.Background(), "", CreateOptions{})
	assert.Error(t, err, "Create should fail with empty name")
}

// TestDirectoryDriver_Delete_NonExistent tests DirectoryDriver Delete with non-existent volume
func TestDirectoryDriver_Delete_NonExistent(t *testing.T) {
	driver, err := NewDirectoryDriver(t.TempDir())
	require.NoError(t, err)

	// Delete uses os.RemoveAll which succeeds even for non-existent paths
	err = driver.Delete(context.Background(), "nonexistent")
	assert.NoError(t, err, "Delete uses os.RemoveAll which succeeds for non-existent")
}

// TestDirectoryDriver_Mount_NonExistent tests DirectoryDriver Mount with non-existent volume
func TestDirectoryDriver_Mount_NonExistent(t *testing.T) {
	driver, err := NewDirectoryDriver(t.TempDir())
	require.NoError(t, err)

	err = driver.Mount(context.Background(), "nonexistent", t.TempDir(), "/data")
	assert.Error(t, err, "Mount should fail for non-existent volume")
}

// TestDirectoryDriver_Stat_NonExistent tests DirectoryDriver Stat with non-existent volume
func TestDirectoryDriver_Stat_NonExistent(t *testing.T) {
	driver, err := NewDirectoryDriver(t.TempDir())
	require.NoError(t, err)

	_, err = driver.Stat(context.Background(), "nonexistent")
	assert.Error(t, err, "Stat should fail for non-existent volume")
	assert.True(t, os.IsNotExist(err), "Stat should return os.ErrNotExist")
}

// TestDirectoryDriver_Capacity_NonExistent tests DirectoryDriver Capacity with non-existent volume
func TestDirectoryDriver_Capacity_NonExistent(t *testing.T) {
	driver, err := NewDirectoryDriver(t.TempDir())
	require.NoError(t, err)

	_, _, err = driver.Capacity(context.Background(), "nonexistent")
	assert.Error(t, err, "Capacity should fail for non-existent volume")
}

// TestDirectoryDriver_Snapshot_NonExistent tests DirectoryDriver Snapshot with non-existent volume
func TestDirectoryDriver_Snapshot_NonExistent(t *testing.T) {
	driver, err := NewDirectoryDriver(t.TempDir())
	require.NoError(t, err)

	_, err = driver.Snapshot(context.Background(), "nonexistent")
	assert.Error(t, err, "Snapshot should fail for non-existent volume")
}

// TestDirectoryDriver_Restore_NilSnapshot tests DirectoryDriver Restore with nil snapshot
func TestDirectoryDriver_Restore_NilSnapshot(t *testing.T) {
	driver, err := NewDirectoryDriver(t.TempDir())
	require.NoError(t, err)

	// Create a volume first
	_, err = driver.Create(context.Background(), "test-vol", CreateOptions{SizeMB: 100})
	require.NoError(t, err)

	err = driver.Restore(context.Background(), "test-vol", nil)
	assert.Error(t, err, "Restore should fail with nil snapshot")
}

// TestDirectoryDriver_Export_NonExistent tests DirectoryDriver Export with non-existent volume
func TestDirectoryDriver_Export_NonExistent(t *testing.T) {
	driver, err := NewDirectoryDriver(t.TempDir())
	require.NoError(t, err)

	err = driver.Export(context.Background(), "nonexistent", io.Discard)
	assert.Error(t, err, "Export should fail for non-existent volume")
}

// TestDirectoryDriver_Import tests DirectoryDriver Import
func TestDirectoryDriver_Import(t *testing.T) {
	driver, err := NewDirectoryDriver(t.TempDir())
	require.NoError(t, err)

	// Import with empty reader should still create volume
	// Note: Import expects tar.gz stream
	err = driver.Import(context.Background(), "imported-vol", io.LimitReader(io.Reader(nil), 0), 100)
	// This will likely fail due to gzip reader expecting valid data
	if err != nil {
		t.Logf("Import failed (expected with empty stream): %v", err)
	}
}

// TestBlockDriver_Type_CoverageExtra tests BlockDriver Type method
func TestBlockDriver_Type_CoverageExtra(t *testing.T) {
	// We can't create BlockDriver without root, but we can test the type
	// If BlockDriver is available, Type() should return VolumeTypeBlock
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	driver, err := vm.GetDriver(VolumeTypeBlock)
	if err != nil {
		t.Skip("Block driver not available (requires root)")
	}
	assert.Equal(t, VolumeTypeBlock, driver.Type())
}

// TestBlockDriver_Stat_NonExistent tests BlockDriver Stat with non-existent volume
func TestBlockDriver_Stat_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	driver, err := vm.GetDriver(VolumeTypeBlock)
	if err != nil {
		t.Skip("Block driver not available")
	}

	_, err = driver.Stat(context.Background(), "nonexistent-block")
	assert.Error(t, err, "Stat should fail for non-existent volume")
}

// TestBlockDriver_Delete_NonExistent tests BlockDriver Delete with non-existent volume
func TestBlockDriver_Delete_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	driver, err := vm.GetDriver(VolumeTypeBlock)
	if err != nil {
		t.Skip("Block driver not available")
	}

	// Delete uses os.RemoveAll which succeeds even for non-existent
	err = driver.Delete(context.Background(), "nonexistent-block")
	// May or may not error depending on implementation
	if err != nil {
		t.Logf("Delete returned error: %v", err)
	}
}

// TestBlockDriver_Capacity_NonExistent tests BlockDriver Capacity with non-existent volume
func TestBlockDriver_Capacity_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	driver, err := vm.GetDriver(VolumeTypeBlock)
	if err != nil {
		t.Skip("Block driver not available")
	}

	_, _, err = driver.Capacity(context.Background(), "nonexistent-block")
	assert.Error(t, err, "Capacity should fail for non-existent volume")
}

// TestBlockDriver_Snapshot_NonExistent tests BlockDriver Snapshot with non-existent volume
func TestBlockDriver_Snapshot_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	driver, err := vm.GetDriver(VolumeTypeBlock)
	if err != nil {
		t.Skip("Block driver not available")
	}

	_, err = driver.Snapshot(context.Background(), "nonexistent-block")
	assert.Error(t, err, "Snapshot should fail for non-existent volume")
}

// TestBlockDriver_Restore_NilSnapshot_CoverageExtra tests BlockDriver Restore with nil snapshot
func TestBlockDriver_Restore_NilSnapshot_CoverageExtra(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	driver, err := vm.GetDriver(VolumeTypeBlock)
	if err != nil {
		t.Skip("Block driver not available")
	}

	err = driver.Restore(context.Background(), "test-block", nil)
	assert.Error(t, err, "Restore should fail with nil snapshot")
}

// TestBlockDriver_Export_NonExistent tests BlockDriver Export with non-existent volume
func TestBlockDriver_Export_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	vm, err := NewVolumeManager(tmpDir)
	require.NoError(t, err)

	driver, err := vm.GetDriver(VolumeTypeBlock)
	if err != nil {
		t.Skip("Block driver not available")
	}

	err = driver.Export(context.Background(), "nonexistent-block", io.Discard)
	assert.Error(t, err, "Export should fail for non-existent volume")
}

// TestClearDirectory_CoverageExtra tests clearDirectory function
func TestClearDirectory_CoverageExtra(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some files
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("data1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("data2"), 0644))
	require.NoError(t, os.Mkdir(filepath.Join(tmpDir, "subdir"), 0755))

	err := clearDirectory(tmpDir)
	assert.NoError(t, err, "clearDirectory should succeed")

	// Verify directory is empty
	entries, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "Directory should be empty after clear")
}

// TestClearDirectory_NonExistent_CoverageExtra tests clearDirectory with non-existent directory
func TestClearDirectory_NonExistent_CoverageExtra(t *testing.T) {
	err := clearDirectory("/nonexistent/directory")
	assert.Error(t, err, "clearDirectory should fail for non-existent directory")
}

// TestCreateSparseFile_CoverageExtra tests createSparseFile function
func TestCreateSparseFile_CoverageExtra(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "sparse.img")

	err := createSparseFile(tmpFile, 1024*1024) // 1 MB
	assert.NoError(t, err, "createSparseFile should succeed")

	// Verify file exists and has correct size
	info, err := os.Stat(tmpFile)
	require.NoError(t, err)
	assert.Equal(t, int64(1024*1024), info.Size())
}

// TestCreateSparseFile_InvalidPath_CoverageExtra tests createSparseFile with invalid path
func TestCreateSparseFile_InvalidPath_CoverageExtra(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("Skipping: running as root can create files anywhere")
	}

	// Create a read-only parent
	tmpDir := t.TempDir()
	readOnlyParent := filepath.Join(tmpDir, "readonly")
	require.NoError(t, os.Mkdir(readOnlyParent, 0555))

	err := createSparseFile(filepath.Join(readOnlyParent, "sparse.img"), 1024)
	assert.Error(t, err, "createSparseFile should fail in read-only directory")
}

// TestMetaStore_Write tests MetaStore Write
func TestMetaStore_Write(t *testing.T) {
	meta, err := NewMetaStore(t.TempDir())
	require.NoError(t, err)

	m := &volumeMeta{
		Name:      "test-vol",
		Type:      VolumeTypeDir,
		SizeMB:    100,
		CreatedAt: nowUTC(),
	}

	err = meta.Write(context.Background(), m)
	assert.NoError(t, err, "Write should succeed")

	// Verify file exists
	assert.FileExists(t, meta.metaPath("test-vol"))
}

// TestMetaStore_Read_NonExistent tests MetaStore Read with non-existent volume
func TestMetaStore_Read_NonExistent(t *testing.T) {
	meta, err := NewMetaStore(t.TempDir())
	require.NoError(t, err)

	_, err = meta.Read(context.Background(), "nonexistent")
	assert.Error(t, err, "Read should fail for non-existent volume")
	assert.True(t, os.IsNotExist(err), "Read should return os.ErrNotExist")
}

// TestMetaStore_Delete_CoverageExtra tests MetaStore Delete
func TestMetaStore_Delete_CoverageExtra(t *testing.T) {
	meta, err := NewMetaStore(t.TempDir())
	require.NoError(t, err)

	// Create metadata first
	m := &volumeMeta{Name: "test-vol", Type: VolumeTypeDir, CreatedAt: nowUTC()}
	require.NoError(t, meta.Write(context.Background(), m))

	err = meta.Delete(context.Background(), "test-vol")
	assert.NoError(t, err, "Delete should succeed")

	// Verify file removed
	assert.NoFileExists(t, meta.metaPath("test-vol"))
}

// TestMetaStore_Delete_NonExistent tests MetaStore Delete with non-existent volume
func TestMetaStore_Delete_NonExistent(t *testing.T) {
	meta, err := NewMetaStore(t.TempDir())
	require.NoError(t, err)

	err = meta.Delete(context.Background(), "nonexistent")
	assert.NoError(t, err, "Delete should succeed even for non-existent metadata")
}

// TestMetaStore_List_CoverageExtra tests MetaStore List
func TestMetaStore_List_CoverageExtra(t *testing.T) {
	meta, err := NewMetaStore(t.TempDir())
	require.NoError(t, err)

	// Create some volumes
	m1 := &volumeMeta{Name: "vol1", Type: VolumeTypeDir, CreatedAt: nowUTC()}
	m2 := &volumeMeta{Name: "vol2", Type: VolumeTypeDir, CreatedAt: nowUTC()}
	require.NoError(t, meta.Write(context.Background(), m1))
	require.NoError(t, meta.Write(context.Background(), m2))

	metas, err := meta.List(context.Background())
	require.NoError(t, err, "List should succeed")
	assert.Len(t, metas, 2)
}

// TestMetaStore_TouchLastUsed_CoverageExtra tests MetaStore TouchLastUsed
func TestMetaStore_TouchLastUsed_CoverageExtra(t *testing.T) {
	meta, err := NewMetaStore(t.TempDir())
	require.NoError(t, err)

	// Create metadata first
	m := &volumeMeta{Name: "test-vol", Type: VolumeTypeDir, CreatedAt: nowUTC()}
	require.NoError(t, meta.Write(context.Background(), m))

	err = meta.TouchLastUsed(context.Background(), "test-vol")
	assert.NoError(t, err, "TouchLastUsed should succeed")

	// Verify LastUsedAt updated
	readM, err := meta.Read(context.Background(), "test-vol")
	require.NoError(t, err)
	assert.NotZero(t, readM.LastUsedAt)
}

// TestMetaStore_TouchLastUsed_NonExistent tests MetaStore TouchLastUsed with non-existent volume
func TestMetaStore_TouchLastUsed_NonExistent(t *testing.T) {
	meta, err := NewMetaStore(t.TempDir())
	require.NoError(t, err)

	err = meta.TouchLastUsed(context.Background(), "nonexistent")
	assert.Error(t, err, "TouchLastUsed should fail for non-existent volume")
}

// TestMetaStore_UpdateTaskID_CoverageExtra tests MetaStore UpdateTaskID
func TestMetaStore_UpdateTaskID_CoverageExtra(t *testing.T) {
	meta, err := NewMetaStore(t.TempDir())
	require.NoError(t, err)

	// Create metadata first
	m := &volumeMeta{Name: "test-vol", Type: VolumeTypeDir, CreatedAt: nowUTC()}
	require.NoError(t, meta.Write(context.Background(), m))

	err = meta.UpdateTaskID(context.Background(), "test-vol", "task-123")
	assert.NoError(t, err, "UpdateTaskID should succeed")

	// Verify TaskID updated
	readM, err := meta.Read(context.Background(), "test-vol")
	require.NoError(t, err)
	assert.Equal(t, "task-123", readM.TaskID)
}

// TestMetaStore_RemoveVolumeDir_CoverageExtra tests MetaStore RemoveVolumeDir
func TestMetaStore_RemoveVolumeDir_CoverageExtra(t *testing.T) {
	meta, err := NewMetaStore(t.TempDir())
	require.NoError(t, err)

	// Create volume directory
	volDir := meta.volumeDir("test-vol")
	require.NoError(t, os.MkdirAll(volDir, 0755))

	err = meta.RemoveVolumeDir("test-vol")
	assert.NoError(t, err, "RemoveVolumeDir should succeed")

	// Verify directory removed
	assert.NoDirExists(t, volDir)
}

// TestMetaStore_RemoveVolumeDir_NonExistent_CoverageExtra tests MetaStore RemoveVolumeDir with non-existent volume
func TestMetaStore_RemoveVolumeDir_NonExistent_CoverageExtra(t *testing.T) {
	meta, err := NewMetaStore(t.TempDir())
	require.NoError(t, err)

	// RemoveVolumeDir calls os.RemoveAll which succeeds even for non-existent
	err = meta.RemoveVolumeDir("nonexistent")
	assert.NoError(t, err, "RemoveVolumeDir should succeed for non-existent directory")
}
