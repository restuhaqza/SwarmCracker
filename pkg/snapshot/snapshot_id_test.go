package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
	require.True(t, validSnapshotID(generated), generated)
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
		assert.True(t, validSnapshotID(id), id)
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
		assert.False(t, validSnapshotID(id), id)
	}
}
