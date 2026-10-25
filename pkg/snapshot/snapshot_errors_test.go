package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSnapshotSentinelsClassifyStoreFailures pins the classification the broker
// uses instead of substring matching. Every failure site wraps its sentinel with
// %w, so errors.Is works while the descriptive text stays readable.
func TestSnapshotSentinelsClassifyStoreFailures(t *testing.T) {
	t.Run("socket missing is a create failure", func(t *testing.T) {
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: t.TempDir()})
		require.NoError(t, err)

		_, err = mgr.CreateSnapshot(context.Background(), "task-1",
			filepath.Join(t.TempDir(), "missing.sock"), CreateOptions{})

		require.ErrorIs(t, err, ErrSocketMissing)
		require.Contains(t, err.Error(), "firecracker socket not found")
	})

	t.Run("delete of an unknown snapshot", func(t *testing.T) {
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: t.TempDir()})
		require.NoError(t, err)

		err = mgr.DeleteSnapshot(context.Background(), "snap-7bca9e1459f61af0")

		require.ErrorIs(t, err, ErrNotFound)
		require.Contains(t, err.Error(), "snapshot not found")
	})

	t.Run("missing state file", func(t *testing.T) {
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: t.TempDir()})
		require.NoError(t, err)

		err = mgr.RestoreFromSnapshot(context.Background(), &SnapshotInfo{
			ID:         "snap-9f2460b56367bc4e",
			StatePath:  filepath.Join(t.TempDir(), "vm.state"),
			MemoryPath: filepath.Join(t.TempDir(), "vm.mem"),
		}, filepath.Join(t.TempDir(), "restored.sock"))

		require.ErrorIs(t, err, ErrSnapshotIncomplete)
		require.Contains(t, err.Error(), "state file not found")
	})

	t.Run("missing memory file", func(t *testing.T) {
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: t.TempDir()})
		require.NoError(t, err)

		dir := t.TempDir()
		statePath := filepath.Join(dir, "vm.state")
		require.NoError(t, os.WriteFile(statePath, []byte("state"), 0o600))

		err = mgr.RestoreFromSnapshot(context.Background(), &SnapshotInfo{
			ID:         "snap-9f2460b56367bc4e",
			StatePath:  statePath,
			MemoryPath: filepath.Join(dir, "vm.mem"),
		}, filepath.Join(t.TempDir(), "restored.sock"))

		require.ErrorIs(t, err, ErrSnapshotIncomplete)
		require.Contains(t, err.Error(), "memory file not found")
	})

	t.Run("checksum mismatch", func(t *testing.T) {
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: t.TempDir()})
		require.NoError(t, err)

		dir := t.TempDir()
		statePath := filepath.Join(dir, "vm.state")
		memoryPath := filepath.Join(dir, "vm.mem")
		require.NoError(t, os.WriteFile(statePath, []byte("state"), 0o600))
		require.NoError(t, os.WriteFile(memoryPath, []byte("memory"), 0o600))

		err = mgr.RestoreFromSnapshot(context.Background(), &SnapshotInfo{
			ID:         "snap-9f2460b56367bc4e",
			StatePath:  statePath,
			MemoryPath: memoryPath,
			Checksum:   "deadbeef",
		}, filepath.Join(t.TempDir(), "restored.sock"))

		require.ErrorIs(t, err, ErrChecksumMismatch)
		require.Contains(t, err.Error(), "checksum mismatch")
	})

	t.Run("missing rootfs", func(t *testing.T) {
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: t.TempDir()})
		require.NoError(t, err)

		dir := t.TempDir()
		statePath := filepath.Join(dir, "vm.state")
		memoryPath := filepath.Join(dir, "vm.mem")
		require.NoError(t, os.WriteFile(statePath, []byte("state"), 0o600))
		require.NoError(t, os.WriteFile(memoryPath, []byte("memory"), 0o600))
		checksum, err := sha256File(statePath)
		require.NoError(t, err)

		err = mgr.RestoreFromSnapshot(context.Background(), &SnapshotInfo{
			ID:         "snap-9f2460b56367bc4e",
			StatePath:  statePath,
			MemoryPath: memoryPath,
			Checksum:   checksum,
			RootfsPath: filepath.Join(dir, "missing-rootfs"),
		}, filepath.Join(t.TempDir(), "restored.sock"))

		require.ErrorIs(t, err, ErrSnapshotIncomplete)
		require.Contains(t, err.Error(), "rootfs not found")
	})
}

// TestSnapshotSentinelsAreDistinct guards against a copy-paste that would make
// every classification collapse into one.
func TestSnapshotSentinelsAreDistinct(t *testing.T) {
	sentinels := []error{ErrNotFound, ErrSocketMissing, ErrSnapshotIncomplete, ErrChecksumMismatch}
	for i, sentinel := range sentinels {
		for j, other := range sentinels {
			if i == j {
				continue
			}
			require.False(t, errors.Is(sentinel, other),
				"%v must not match %v", sentinel, other)
		}
	}

	// A wrapped sentinel still matches only its own class.
	wrapped := errors.Join(ErrSnapshotIncomplete, os.ErrNotExist)
	require.False(t, errors.Is(wrapped, ErrNotFound))
	require.True(t, errors.Is(wrapped, ErrSnapshotIncomplete))
}
