package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRestoreFromSnapshot_MissingRootfs verifies the rootfs existence check that
// runs before a fresh Firecracker process is started.
func TestRestoreFromSnapshot_MissingRootfs(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: dir})
	require.NoError(t, err)

	statePath := filepath.Join(dir, "vm.state")
	memPath := filepath.Join(dir, "vm.mem")
	require.NoError(t, os.WriteFile(statePath, []byte("state"), 0o644))
	require.NoError(t, os.WriteFile(memPath, []byte("mem"), 0o644))

	sum, err := sha256File(statePath)
	require.NoError(t, err)

	info := &SnapshotInfo{
		ID:         "snap-x",
		TaskID:     "task-1",
		StatePath:  statePath,
		MemoryPath: memPath,
		Checksum:   sum,
		RootfsPath: filepath.Join(dir, "does-not-exist.ext4"),
	}

	err = mgr.RestoreFromSnapshot(context.Background(), info, filepath.Join(dir, "fc.sock"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rootfs not found")
}
