//go:build !integration

package image

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateExt4Image_Basic tests basic ext4 image creation
func TestCreateExt4Image_Basic(t *testing.T) {
	// Create source directory with some files
	sourceDir := t.TempDir()
	os.WriteFile(filepath.Join(sourceDir, "file.txt"), []byte("hello world"), 0644)
	os.MkdirAll(filepath.Join(sourceDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(sourceDir, "subdir", "nested.txt"), []byte("nested content"), 0644)

	// Create output path
	outputPath := filepath.Join(t.TempDir(), "test.ext4")

	ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)

	err := ip.createExt4Image(sourceDir, outputPath)
	require.NoError(t, err)

	// Verify image file was created
	assert.FileExists(t, outputPath)

	// Verify it's a valid ext4 image (check file size > 0)
	info, err := os.Stat(outputPath)
	require.NoError(t, err)
	assert.True(t, info.Size() > 0, "Image should have non-zero size")
}

// TestCreateExt4Image_EmptyDirectory tests with empty source
func TestCreateExt4Image_EmptyDirectory(t *testing.T) {
	sourceDir := t.TempDir()
	outputPath := filepath.Join(t.TempDir(), "empty.ext4")

	ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)

	err := ip.createExt4Image(sourceDir, outputPath)
	require.NoError(t, err)
	assert.FileExists(t, outputPath)
}

// TestCreateExt4Image_MinSize verifies a requested minimum rootfs size is
// honoured, so a VM can be given a guaranteed amount of free disk space.
func TestCreateExt4Image_MinSize(t *testing.T) {
	sourceDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "file.txt"), []byte("hi"), 0644))
	outputPath := filepath.Join(t.TempDir(), "sized.ext4")

	ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)

	// Larger than the 100 MB default so the requested minimum is what applies.
	const minSize = int64(120 * 1024 * 1024)
	require.NoError(t, ip.createExt4Image(sourceDir, outputPath, minSize))

	info, err := os.Stat(outputPath)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, info.Size(), minSize, "rootfs should be at least the requested size")
}

// TestCreateExt4Image_NestedDirectories tests deeply nested directories
func TestCreateExt4Image_NestedDirectories(t *testing.T) {
	sourceDir := t.TempDir()

	// Create deep nesting
	deepPath := filepath.Join(sourceDir, "a", "b", "c", "d", "e")
	os.MkdirAll(deepPath, 0755)
	os.WriteFile(filepath.Join(deepPath, "deep.txt"), []byte("deep file"), 0644)

	outputPath := filepath.Join(t.TempDir(), "nested.ext4")

	ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)

	err := ip.createExt4Image(sourceDir, outputPath)
	require.NoError(t, err)
	assert.FileExists(t, outputPath)
}

// TestCreateExt4Image_Symlinks tests symlinks in source
func TestCreateExt4Image_Symlinks(t *testing.T) {
	sourceDir := t.TempDir()

	// Create file and symlink
	os.WriteFile(filepath.Join(sourceDir, "target.txt"), []byte("target"), 0644)
	os.Symlink("target.txt", filepath.Join(sourceDir, "link.txt"))

	outputPath := filepath.Join(t.TempDir(), "symlink.ext4")

	ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)

	err := ip.createExt4Image(sourceDir, outputPath)
	require.NoError(t, err)
	assert.FileExists(t, outputPath)
}

// TestCreateExt4Image_SpecialFiles tests special file types
func TestCreateExt4Image_SpecialFiles(t *testing.T) {
	sourceDir := t.TempDir()

	// Create various file types
	os.WriteFile(filepath.Join(sourceDir, "regular.txt"), []byte("regular"), 0644)
	os.MkdirAll(filepath.Join(sourceDir, "dir"), 0755)
	os.Symlink("regular.txt", filepath.Join(sourceDir, "symlink"))

	outputPath := filepath.Join(t.TempDir(), "special.ext4")

	ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)

	err := ip.createExt4Image(sourceDir, outputPath)
	require.NoError(t, err)
	assert.FileExists(t, outputPath)
}

// TestCreateExt4Image_LargeDirectory tests with larger content
func TestCreateExt4Image_LargeDirectory(t *testing.T) {
	sourceDir := t.TempDir()

	// Create many files
	for i := 0; i < 100; i++ {
		os.WriteFile(filepath.Join(sourceDir, "file"+string(rune(i))+".txt"), []byte("content"), 0644)
	}

	outputPath := filepath.Join(t.TempDir(), "large.ext4")

	ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)

	err := ip.createExt4Image(sourceDir, outputPath)
	require.NoError(t, err)
	assert.FileExists(t, outputPath)
}
