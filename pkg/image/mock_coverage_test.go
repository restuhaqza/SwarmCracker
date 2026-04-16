package image

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMockContainerRuntime_Constructors tests mock constructors
func TestMockContainerRuntime_Constructors(t *testing.T) {
	mock := NewMockContainerRuntime()
	require.NotNil(t, mock)
	assert.NotNil(t, mock.Containers)
	assert.NotNil(t, mock.Images)
	assert.NotNil(t, mock.TarFiles)
	assert.NotNil(t, mock.Calls)
}

// TestMockContainerRuntime_AllMethods tests all mock container runtime methods
func TestMockContainerRuntime_AllMethods(t *testing.T) {
	mock := NewMockContainerRuntime()
	ctx := context.Background()

	// Test CreateContainer
	containerID, err := mock.CreateContainer(ctx, "alpine:latest", "/tmp/test")
	require.NoError(t, err)
	assert.NotEmpty(t, containerID)
	assert.Contains(t, mock.Calls, "CreateContainer:alpine:latest")

	// Test ImageExists
	exists := mock.ImageExists(ctx, "alpine:latest")
	assert.True(t, exists)

	// Test ExportContainer
	err = mock.ExportContainer(ctx, containerID, "/tmp/test.tar")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls, "ExportContainer:"+containerID)

	// Test PullImage
	err = mock.PullImage(ctx, "nginx:latest")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls, "PullImage:nginx:latest")

	// Test RemoveContainer
	err = mock.RemoveContainer(ctx, containerID)
	require.NoError(t, err)
	assert.Contains(t, mock.Calls, "RemoveContainer:"+containerID)
}

// TestMockContainerRuntime_Errors tests error scenarios
func TestMockContainerRuntime_Errors(t *testing.T) {
	mock := NewMockContainerRuntime()
	ctx := context.Background()

	mock.CreateErr = assert.AnError
	_, err := mock.CreateContainer(ctx, "test", "/tmp")
	require.Error(t, err)

	mock.ExportErr = assert.AnError
	err = mock.ExportContainer(ctx, "test", "/tmp")
	require.Error(t, err)

	mock.RemoveErr = assert.AnError
	err = mock.RemoveContainer(ctx, "test")
	require.Error(t, err)

	mock.PullErr = assert.AnError
	err = mock.PullImage(ctx, "test")
	require.Error(t, err)
}

// TestMockFilesystemOperator_Constructors tests mock filesystem operator constructor
func TestMockFilesystemOperator_Constructors(t *testing.T) {
	mock := NewMockFilesystemOperator()
	require.NotNil(t, mock)
	assert.NotNil(t, mock.Files)
	assert.NotNil(t, mock.Mounts)
	assert.NotNil(t, mock.Calls)
}

// TestMockFilesystemOperator_AllMethods tests all mock filesystem methods
func TestMockFilesystemOperator_AllMethods(t *testing.T) {
	mock := NewMockFilesystemOperator()
	tmpDir := t.TempDir()

	// Test MkfsExt4
	err := mock.MkfsExt4(tmpDir, filepath.Join(tmpDir, "test.ext4"))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(mock.Calls), 1) // Should have at least one call

	// Test Truncate
	err = mock.Truncate(filepath.Join(tmpDir, "test.ext4"), 100)
	require.NoError(t, err)

	// Test Mount
	err = mock.Mount(filepath.Join(tmpDir, "test.ext4"), filepath.Join(tmpDir, "mnt"))
	require.NoError(t, err)

	// Test Unmount
	err = mock.Unmount(filepath.Join(tmpDir, "mnt"))
	require.NoError(t, err)

	// Test CreateFile
	err = mock.CreateFile(filepath.Join(tmpDir, "test.txt"))
	require.NoError(t, err)

	// Test FileExists - mock always returns false unless Files map has entry
	mock.Files[filepath.Join(tmpDir, "test.txt")] = []byte("test")
	exists := mock.FileExists(filepath.Join(tmpDir, "test.txt"))
	assert.True(t, exists)

	// Test RemoveFile
	err = mock.RemoveFile(filepath.Join(tmpDir, "test.txt"))
	require.NoError(t, err)

	// After remove, file should be gone from Files map
	exists = mock.FileExists(filepath.Join(tmpDir, "test.txt"))
	assert.False(t, exists)

	// Test CopyFile - need to add source file to mock Files map
	srcPath := filepath.Join(tmpDir, "src.txt")
	dstPath := filepath.Join(tmpDir, "dst.txt")
	mock.Files[srcPath] = []byte("test content")
	err = mock.CopyFile(srcPath, dstPath, 0644)
	require.NoError(t, err)
}

// TestMockFilesystemOperator_Errors tests filesystem error scenarios
func TestMockFilesystemOperator_Errors(t *testing.T) {
	mock := NewMockFilesystemOperator()

	mock.MkfsErr = assert.AnError
	err := mock.MkfsExt4("/tmp", "/tmp/test.ext4")
	require.Error(t, err)

	mock.MountErr = assert.AnError
	err = mock.Mount("/tmp/test", "/tmp/mnt")
	require.Error(t, err)

	mock.UnmountErr = assert.AnError
	err = mock.Unmount("/tmp/mnt")
	require.Error(t, err)

	mock.CopyErr = assert.AnError
	err = mock.CopyFile("/tmp/src", "/tmp/dst", 0644)
	require.Error(t, err)
}

// TestMockBinaryLocator_Constructors tests mock binary locator constructor
func TestMockBinaryLocator_Constructors(t *testing.T) {
	mock := NewMockBinaryLocator()
	require.NotNil(t, mock)
	assert.NotNil(t, mock.Binaries)
	assert.NotNil(t, mock.Calls)
}

// TestMockBinaryLocator_AllMethods tests all mock binary locator methods
func TestMockBinaryLocator_AllMethods(t *testing.T) {
	mock := NewMockBinaryLocator()
	mock.Binaries["test-binary"] = "/usr/bin/test-binary"

	// Test LookPath
	path, err := mock.LookPath("test-binary")
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin/test-binary", path)
	assert.Contains(t, mock.Calls, "LookPath:test-binary")

	// Test Which
	path, err = mock.Which("test-binary")
	require.NoError(t, err)
	assert.NotEmpty(t, path)

	// Test FileExists
	exists := mock.FileExists("/usr/bin/test-binary")
	// May or may not exist
	_ = exists
}

// TestMockBinaryLocator_Errors tests binary locator errors
func TestMockBinaryLocator_Errors(t *testing.T) {
	mock := NewMockBinaryLocator()

	_, err := mock.LookPath("nonexistent")
	require.Error(t, err)

	mock.WhichErr = assert.AnError
	_, err = mock.Which("test")
	require.Error(t, err)
}

// TestRealBinaryLocator tests real binary locator (if binaries exist)
func TestRealBinaryLocator(t *testing.T) {
	locator := NewRealBinaryLocator()
	require.NotNil(t, locator)

	// Test LookPath for common binary
	path, err := locator.LookPath("ls")
	if err == nil {
		assert.NotEmpty(t, path)
	}

	// Test FileExists
	exists := locator.FileExists("/bin/ls")
	// May or may not exist depending on system
	_ = exists

	// Test Which
	path, err = locator.Which("ls")
	// May error on some systems
	_ = path
	_ = err
}

// TestRealFilesystemOperator tests real filesystem operator
func TestRealFilesystemOperator(t *testing.T) {
	fs := NewRealFilesystemOperator()
	require.NotNil(t, fs)

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	// Test CreateFile
	err := fs.CreateFile(testFile)
	require.NoError(t, err)

	// Test FileExists
	exists := fs.FileExists(testFile)
	assert.True(t, exists)

	// Test RemoveFile
	err = fs.RemoveFile(testFile)
	require.NoError(t, err)

	// Test FileExists after removal
	exists = fs.FileExists(testFile)
	assert.False(t, exists)

	// Test CopyFile
	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")
	require.NoError(t, os.WriteFile(src, []byte("content"), 0644))
	err = fs.CopyFile(src, dst, 0644)
	require.NoError(t, err)
}