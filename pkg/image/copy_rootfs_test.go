package image

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyRootfs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.ext4")
	dst := filepath.Join(dir, "dst.ext4")
	require.NoError(t, os.WriteFile(src, []byte("rootfs-data"), 0o644))

	ip := &ImagePreparer{}

	require.NoError(t, ip.copyRootfs(context.Background(), src, dst))
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, []byte("rootfs-data"), got)

	// A stale destination is replaced.
	require.NoError(t, os.WriteFile(dst, []byte("stale"), 0o644))
	require.NoError(t, ip.copyRootfs(context.Background(), src, dst))
	got, err = os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, []byte("rootfs-data"), got)
}

func TestCopyRootfs_MissingSource(t *testing.T) {
	dir := t.TempDir()
	ip := &ImagePreparer{}
	err := ip.copyRootfs(context.Background(), filepath.Join(dir, "nope.ext4"), filepath.Join(dir, "dst.ext4"))
	assert.Error(t, err)
}
