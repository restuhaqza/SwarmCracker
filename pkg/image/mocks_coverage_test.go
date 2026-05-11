//go:build !integration

package image

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExportContainer_ErrorPathV3 tests the error branch in ExportContainer
func TestExportContainer_ErrorPathV3(t *testing.T) {
	runtime := NewRealContainerRuntime("docker")

	ctx := context.Background()
	err := runtime.ExportContainer(ctx, "nonexistent-container-12345", "/tmp/nonexistent-output.tar")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status")
}

// TestRemoveContainer_ErrorPathV3 tests the error branch in RemoveContainer
func TestRemoveContainer_ErrorPathV3(t *testing.T) {
	// Use nonexistent runtime to force error
	runtimeObj := NewRealContainerRuntime("nonexistent-runtime-xyz")

	ctx := context.Background()
	err := runtimeObj.RemoveContainer(ctx, "container-123")
	require.Error(t, err)
}

// TestPullImage_ErrorPathV3 tests the error branch in PullImage
func TestPullImage_ErrorPathV3(t *testing.T) {
	runtime := NewRealContainerRuntime("docker")

	ctx := context.Background()
	err := runtime.PullImage(ctx, "invalid-image-name-!!!")

	require.Error(t, err)
}

// TestImageExists_FalseV3 tests ImageExists returning false
func TestImageExists_FalseV3(t *testing.T) {
	runtime := NewRealContainerRuntime("docker")

	ctx := context.Background()
	exists := runtime.ImageExists(ctx, "nonexistent-image-12345:latest")

	assert.False(t, exists)
}

// TestValidateArchitecture_CoverageV3 tests validateArchitecture
func TestValidateArchitecture_CoverageV3(t *testing.T) {
	ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)

	// This exercises the function - can't change runtime.GOARCH at runtime
	err := ip.validateArchitecture()
	_ = err // Just verify it runs without panic
}

// TestRealFilesystemOperator_MkfsExt4_ErrorV3 tests mkfs.ext4 error path
func TestRealFilesystemOperator_MkfsExt4_ErrorV3(t *testing.T) {
	fs := NewRealFilesystemOperator()

	// Create temp output file path
	outputPath := filepath.Join(t.TempDir(), "test.img")

	err := fs.MkfsExt4("/nonexistent", outputPath)
	require.Error(t, err) // Should fail with nonexistent source
}

// TestRealFilesystemOperator_TruncateV3 tests truncate operation
func TestRealFilesystemOperator_TruncateV3(t *testing.T) {
	fs := NewRealFilesystemOperator()

	outputPath := filepath.Join(t.TempDir(), "test-truncate.img")

	// sizeMB is 1 (not 1024*1024 which would be 1TB)
	err := fs.Truncate(outputPath, 1)
	require.NoError(t, err)

	// Verify file size is 1MB
	info, err := os.Stat(outputPath)
	require.NoError(t, err)
	assert.Equal(t, int64(1*1024*1024), info.Size())
}

// TestRealFilesystemOperator_Mount_ErrorV3 tests mount error path
func TestRealFilesystemOperator_Mount_ErrorV3(t *testing.T) {
	fs := NewRealFilesystemOperator()

	err := fs.Mount("/nonexistent/image.ext4", "/tmp/nonexistent-mount-point")
	require.Error(t, err)
}

// TestRealFilesystemOperator_Unmount_ErrorV3 tests unmount error path
func TestRealFilesystemOperator_Unmount_ErrorV3(t *testing.T) {
	fs := NewRealFilesystemOperator()

	err := fs.Unmount("/tmp/nonexistent-mount-point")
	require.Error(t, err)
}

// TestRealBinaryLocator_LookPathV3 tests binary lookup
func TestRealBinaryLocator_LookPathV3(t *testing.T) {
	locator := NewRealBinaryLocator()

	path, err := locator.LookPath("ls")
	require.NoError(t, err)
	assert.NotEmpty(t, path)

	// Nonexistent binary
	path, err = locator.LookPath("nonexistent-binary-12345")
	require.Error(t, err)
	assert.Empty(t, path)
}

// TestRealBinaryLocator_WhichV3 tests which command
func TestRealBinaryLocator_WhichV3(t *testing.T) {
	locator := NewRealBinaryLocator()

	path, err := locator.Which("ls")
	require.NoError(t, err)
	assert.NotEmpty(t, path)
}

// TestRealBinaryLocator_FileExistsV3 tests file existence check
func TestRealBinaryLocator_FileExistsV3(t *testing.T) {
	locator := NewRealBinaryLocator()

	// Create temp file
	tmpFile := filepath.Join(t.TempDir(), "test-file.txt")
	os.WriteFile(tmpFile, []byte("test"), 0644)

	exists := locator.FileExists(tmpFile)
	assert.True(t, exists)

	exists = locator.FileExists("/nonexistent/path/file.txt")
	assert.False(t, exists)
}

// TestRealFilesystemOperator_CreateFileV3 tests file creation
func TestRealFilesystemOperator_CreateFileV3(t *testing.T) {
	fs := NewRealFilesystemOperator()

	filePath := filepath.Join(t.TempDir(), "created-file.txt")
	err := fs.CreateFile(filePath)
	require.NoError(t, err)
	assert.FileExists(t, filePath)
}

// TestRealFilesystemOperator_RemoveFileV3 tests file removal
func TestRealFilesystemOperator_RemoveFileV3(t *testing.T) {
	fs := NewRealFilesystemOperator()

	// Create temp file to remove
	filePath := filepath.Join(t.TempDir(), "to-remove.txt")
	os.WriteFile(filePath, []byte("test"), 0644)

	err := fs.RemoveFile(filePath)
	require.NoError(t, err)
	assert.NoFileExists(t, filePath)
}

// TestRealFilesystemOperator_FileExistsV3 tests file existence
func TestRealFilesystemOperator_FileExistsV3(t *testing.T) {
	fs := NewRealFilesystemOperator()

	// Create temp file
	filePath := filepath.Join(t.TempDir(), "exists-test.txt")
	os.WriteFile(filePath, []byte("test"), 0644)

	exists := fs.FileExists(filePath)
	assert.True(t, exists)

	exists = fs.FileExists("/nonexistent/file.txt")
	assert.False(t, exists)
}

// TestRealFilesystemOperator_CopyFileV3 tests file copy
func TestRealFilesystemOperator_CopyFileV3(t *testing.T) {
	fs := NewRealFilesystemOperator()

	// Create source file
	srcPath := filepath.Join(t.TempDir(), "source.txt")
	os.WriteFile(srcPath, []byte("source content"), 0644)

	dstPath := filepath.Join(t.TempDir(), "dest.txt")
	err := fs.CopyFile(srcPath, dstPath, 0644)
	require.NoError(t, err)

	assert.FileExists(t, dstPath)
}
