//go:build linux

package snapshot

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCopyDataExtentsKeepsHoles exercises the fallback that runs on a Linux
// filesystem without reflink support: only data extents are copied, so a sparse
// disk image keeps its holes. The comparison is on content, because a bug in the
// SEEK_DATA/SEEK_HOLE loop shows up as bytes that are missing or duplicated.
func TestCopyDataExtentsKeepsHoles(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "sparse.ext4")
	dstPath := filepath.Join(dir, "copy.ext4")

	// 2 MiB with data at the start, in the middle and at the very end, and holes
	// between them. A hole in the middle is the case a plain "read until EOF"
	// implementation gets right, but a bad extent loop does not.
	const size = 2 << 20
	src, err := os.OpenFile(srcPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	require.NoError(t, err)

	head := bytes.Repeat([]byte("head"), 1024)
	middle := bytes.Repeat([]byte("middle"), 1024)
	tail := bytes.Repeat([]byte("tail"), 1024)

	require.NoError(t, writeAt(src, head, 0))
	require.NoError(t, writeAt(src, middle, size/2))
	require.NoError(t, writeAt(src, tail, int64(size-len(tail))))
	require.NoError(t, src.Truncate(size))
	require.NoError(t, src.Close())

	want, err := os.ReadFile(srcPath)
	require.NoError(t, err)
	require.Len(t, want, size)

	srcFile, err := os.Open(srcPath)
	require.NoError(t, err)
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	require.NoError(t, err)

	require.NoError(t, copyDataExtents(dstFile, srcFile))
	require.NoError(t, dstFile.Close())

	got, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Len(t, got, size)
	assert.True(t, bytes.Equal(want, got), "the sparse copy must reproduce every byte, holes included")
}

// TestCopyDataExtentsAllHole pins the degenerate case: a file that is nothing but
// a hole still has to come out with the right length.
func TestCopyDataExtentsAllHole(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "hole.ext4")
	dstPath := filepath.Join(dir, "copy.ext4")

	srcFile, err := os.OpenFile(srcPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	require.NoError(t, srcFile.Truncate(1<<20))
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	require.NoError(t, err)

	require.NoError(t, copyDataExtents(dstFile, srcFile))
	require.NoError(t, dstFile.Close())

	info, err := os.Stat(dstPath)
	require.NoError(t, err)
	assert.Equal(t, int64(1<<20), info.Size())
}

func writeAt(f *os.File, data []byte, offset int64) error {
	_, err := f.WriteAt(data, offset)
	return err
}
