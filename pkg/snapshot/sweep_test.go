package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewManagerSweepsOrphanSnapshotDirectories pins the second leftover shape: a
// create reserves snap-<id> with mkdir and publishes by renaming a complete
// staging directory over it. A SIGKILL between the two leaves an empty snap-*
// directory with no metadata.json that no listing shows, so the next manager
// start removes it once it is older than the sweep age.
func TestNewManagerSweepsOrphanSnapshotDirectories(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-2 * stagingSweepAge)

	// Stale reservation: the crash left it between reserve and publish.
	orphan := filepath.Join(root, "snap-0123456789abcdef")
	require.NoError(t, os.MkdirAll(orphan, 0o755))
	require.NoError(t, os.Chtimes(orphan, old, old))

	// Fresh reservation: a create in another process may be copying into it.
	fresh := filepath.Join(root, "snap-fedcba9876543210")
	require.NoError(t, os.MkdirAll(fresh, 0o755))

	// Published snapshot: metadata.json is the marker of a complete snapshot.
	published := filepath.Join(root, "snap-00ff00ff00ff00ff")
	require.NoError(t, os.MkdirAll(published, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(published, metadataFile),
		[]byte(`{"id":"snap-00ff00ff00ff00ff"}`), 0o600))
	require.NoError(t, os.Chtimes(published, old, old))

	// An unrelated directory is not a snapshot shape and must be left alone.
	unrelated := filepath.Join(root, "backup-logs")
	require.NoError(t, os.MkdirAll(unrelated, 0o755))
	require.NoError(t, os.Chtimes(unrelated, old, old))

	_, err := NewManager(SnapshotConfig{SnapshotDir: root})
	require.NoError(t, err)

	assert.NoDirExists(t, orphan, "a stale snap-* directory without metadata is an interrupted create")
	assert.DirExists(t, fresh, "a fresh reservation may belong to a create in flight")
	assert.DirExists(t, published, "a published snapshot must survive the sweep")
	assert.DirExists(t, unrelated, "an unrelated directory must never be swept")
}

// TestNewManagerKeepsAPublishedSnapshotWithoutMetadataAge is the boundary of the
// rule: age alone never removes a directory that carries metadata, even when the
// metadata is malformed. A partially readable snapshot is a data-loss decision,
// not a sweep decision.
func TestNewManagerKeepsAPublishedSnapshotWithBadMetadata(t *testing.T) {
	root := t.TempDir()

	dir := filepath.Join(root, "snap-0123456789abcdef")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, metadataFile), []byte("{not json"), 0o600))
	old := time.Now().Add(-2 * stagingSweepAge)
	require.NoError(t, os.Chtimes(dir, old, old))

	_, err := NewManager(SnapshotConfig{SnapshotDir: root})
	require.NoError(t, err)

	assert.DirExists(t, dir, "only the absence of metadata marks a reservation")
}
