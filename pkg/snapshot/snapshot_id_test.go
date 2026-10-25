package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeleteSnapshotRejectsUnsafeIDs pins the defence-in-depth rule: a snapshot
// ID is one directory name, checked before any filesystem join. A traversal
// shape, an absolute path and a Windows-style separator never reach RemoveAll.
func TestDeleteSnapshotRejectsUnsafeIDs(t *testing.T) {
	root := t.TempDir()
	snapshotDir := filepath.Join(root, "snapshots")
	require.NoError(t, os.MkdirAll(snapshotDir, 0o755))

	// A sibling of the snapshot root that "../victim" and an absolute path would
	// address if the join were unvalidated.
	victim := filepath.Join(root, "victim")
	require.NoError(t, os.MkdirAll(victim, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(victim, "keep"), []byte("x"), 0o600))

	mgr, err := NewManager(SnapshotConfig{SnapshotDir: snapshotDir})
	require.NoError(t, err)

	cases := map[string]string{
		"parent traversal":      "../victim",
		"two levels":            "../../etc",
		"absolute path":         "/",
		"absolute path to dir":  snapshotDir,
		"windows traversal":     `..\..\victim`,
		"windows drive":         `C:\snap-0123456789abcdef`,
		"separator":             "snap-0123456789abcdef/child",
		"short hex":             "snap-0123456789abcde",
		"uppercase hex":         "snap-0123456789ABCDEF",
		"non-hex":               "snap-zzzzzzzzzzzzzzzz",
		"empty":                 "",
		"dot":                   ".",
		"dot dot":               "..",
		"looks valid but slash": "snap-0123456789abcdef/..",
	}

	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			err := mgr.DeleteSnapshot(context.Background(), id)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidSnapshotID)
		})
	}

	// Nothing outside the snapshot root was touched.
	_, statErr := os.Stat(filepath.Join(victim, "keep"))
	assert.NoError(t, statErr, "a rejected ID must not delete outside the snapshot root")
	_, statErr = os.Stat(snapshotDir)
	assert.NoError(t, statErr, "a rejected ID must not delete the snapshot root itself")
}

// TestDeleteSnapshotAcceptsTheGeneratedShape keeps the other side of the rule:
// an ID this package generates is valid, so a missing one is not found rather
// than invalid.
func TestDeleteSnapshotAcceptsTheGeneratedShape(t *testing.T) {
	snapshotDir := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: snapshotDir})
	require.NoError(t, err)

	generated := generateSnapshotID("task-1")
	require.True(t, ValidSnapshotID(generated), generated)
	require.Regexp(t, `^snap-[0-9a-f]{16}$`, generated)

	err = mgr.DeleteSnapshot(context.Background(), generated)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.False(t, errors.Is(err, ErrInvalidSnapshotID))
}

// TestValidSnapshotID pins the shape the package writes and accepts.
func TestValidSnapshotID(t *testing.T) {
	for _, id := range []string{
		"snap-0123456789abcdef",
		"snap-0000000000000000",
		"snap-ffffffffffffffff",
	} {
		assert.True(t, ValidSnapshotID(id), id)
	}

	for _, id := range []string{
		"snap-0123456789ABCDEF",
		"snap-0123456789abcde",
		"snap-0123456789abcdef0",
		"snap-0123456789abcdeg",
		"snap_0123456789abcdef",
		"0123456789abcdef",
		"snap-0123-4567-89ab",
	} {
		assert.False(t, ValidSnapshotID(id), id)
	}
}

// TestEnforceMaxSnapshotsSkipsAnInvalidMetadataID pins the retention guard: the
// sweep's input is a node-local metadata file, so an ID that is not one safe
// directory name must be skipped instead of being joined and removed. Without the
// guard, MaxSnapshots: 1 would delete root/victim via "../victim".
func TestEnforceMaxSnapshotsSkipsAnInvalidMetadataID(t *testing.T) {
	root := t.TempDir()
	snapshotDir := filepath.Join(root, "snapshots")
	require.NoError(t, os.MkdirAll(snapshotDir, 0o755))

	// A sibling of the snapshot root that "../victim" addresses once joined.
	victim := filepath.Join(root, "victim")
	require.NoError(t, os.MkdirAll(victim, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(victim, "keep"), []byte("x"), 0o600))

	mgr, err := NewManager(SnapshotConfig{SnapshotDir: snapshotDir, MaxSnapshots: 1})
	require.NoError(t, err)

	// The newer snapshot is well formed, so the sweep keeps it.
	newest := "snap-0123456789abcdef"
	createTestSnapshot(t, snapshotDir, &SnapshotInfo{
		ID:        newest,
		ServiceID: "nginx",
		CreatedAt: time.Now().UTC().Add(-time.Hour),
	})

	// The oldest lives in its own directory but claims a traversal ID, the shape
	// a hand-edited or foreign metadata file can produce. It is the entry the
	// sweep removes first, so it is the one that would escape the root.
	evilDir := filepath.Join(snapshotDir, "snap-ffffffffffffffff")
	require.NoError(t, os.MkdirAll(evilDir, 0o755))
	require.NoError(t, saveMetadata(evilDir, &SnapshotInfo{
		ID:        "../victim",
		ServiceID: "nginx",
		CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
	}))

	mgr.mu.Lock()
	mgr.enforceMaxSnapshots("nginx")
	mgr.mu.Unlock()

	_, statErr := os.Stat(filepath.Join(victim, "keep"))
	require.NoError(t, statErr, "retention must never remove a path outside the snapshot root")

	_, statErr = os.Stat(evilDir)
	require.NoError(t, statErr, "the invalid entry is skipped, not removed")

	_, statErr = os.Stat(filepath.Join(snapshotDir, newest))
	require.NoError(t, statErr, "MaxSnapshots: 1 only makes the oldest entry a candidate")

	snapshots, err := mgr.ListSnapshots(SnapshotFilter{ServiceID: "nginx"})
	require.NoError(t, err)
	assert.Len(t, snapshots, 2, "the skipped entry is still listed, nothing was removed")
}
