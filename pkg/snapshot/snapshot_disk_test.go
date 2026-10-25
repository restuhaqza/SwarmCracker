package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFirecracker serves the two endpoints CreateSnapshot uses, over a real Unix
// socket, and writes the state and memory files Firecracker would write. A
// handler error is reported with a 500 instead of t.FailNow, which is illegal
// from a server goroutine.
func fakeFirecracker(t *testing.T) string {
	t.Helper()

	socketPath := testSocketPath(t, "fc.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/vm", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/snapshot/create", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			SnapshotPath string `json:"snapshot_path"`
			MemFilepath  string `json:"mem_file_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(payload.SnapshotPath, []byte("mock state"), 0o600); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(payload.MemFilepath, []byte("mock memory"), 0o600); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Shutdown(context.Background())
		_ = listener.Close()
	})

	require.Eventually(t, func() bool {
		_, statErr := os.Stat(socketPath)
		return statErr == nil
	}, 2*time.Second, 25*time.Millisecond)

	return socketPath
}

// testDiskContent returns 1 MiB of deterministic, non-repeating bytes so a copy
// that drops, truncates or reorders blocks is visible in a comparison.
func testDiskContent() []byte {
	content := make([]byte, 1<<20)
	source := rand.New(rand.NewSource(1)) //nolint:gosec // test fixture, not a secret
	_, _ = source.Read(content)
	return content
}

// createSnapshotWithDisk creates the shape every create now produces: memory,
// state and a snapshot-owned copy of the task disk.
func createSnapshotWithDisk(t *testing.T, cfg SnapshotConfig) (*Manager, *SnapshotInfo) {
	t.Helper()

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	rootfsPath := filepath.Join(t.TempDir(), "task-1.ext4")
	require.NoError(t, os.WriteFile(rootfsPath, testDiskContent(), 0o600))

	info, err := mgr.CreateSnapshot(context.Background(), "task-1", fakeFirecracker(t), CreateOptions{
		ServiceID:  "web",
		RootfsPath: rootfsPath,
	})
	require.NoError(t, err)

	return mgr, info
}

// TestCreateSnapshotCopiesTheTaskDisk pins the point of the disk copy: the
// snapshot holds the disk bytes itself, so it can be restored after the executor
// deletes the task.
func TestCreateSnapshotCopiesTheTaskDisk(t *testing.T) {
	root := t.TempDir()

	diskDir := t.TempDir()
	rootfsPath := filepath.Join(diskDir, "tasks", "task-1.ext4")
	require.NoError(t, os.MkdirAll(filepath.Dir(rootfsPath), 0o755))
	content := testDiskContent()
	require.NoError(t, os.WriteFile(rootfsPath, content, 0o600))

	mgr, err := NewManager(SnapshotConfig{SnapshotDir: root})
	require.NoError(t, err)

	info, err := mgr.CreateSnapshot(context.Background(), "task-1", fakeFirecracker(t), CreateOptions{
		ServiceID:  "web",
		RootfsPath: rootfsPath,
	})
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(root, info.ID, diskFileName), info.DiskPath)

	copied, err := os.ReadFile(info.DiskPath)
	require.NoError(t, err)
	assert.Len(t, copied, len(content))
	assert.True(t, bytes.Equal(content, copied), "the disk copy must be byte-identical to the task disk")

	// SizeBytes is what cleanup reports as freed, so it has to include the copy.
	stateSize, err := fileSize(info.StatePath)
	require.NoError(t, err)
	memorySize, err := fileSize(info.MemoryPath)
	require.NoError(t, err)
	diskSize, err := fileSize(info.DiskPath)
	require.NoError(t, err)
	assert.Equal(t, stateSize+memorySize+diskSize, info.SizeBytes)

	// The metadata records the copy, so a restore reads it back from disk.
	loaded, err := loadMetadata(filepath.Join(root, info.ID))
	require.NoError(t, err)
	assert.Equal(t, info.DiskPath, loaded.DiskPath)

	// The original is a read-only input: it is still there, unchanged.
	original, err := os.ReadFile(rootfsPath)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, original))
}

// TestCreateSnapshotDiskCopyFailureLeavesNoSnapshot pins that a disk that cannot
// be copied fails the create instead of producing a snapshot that cannot be
// restored: no directory, no metadata.
func TestCreateSnapshotDiskCopyFailureLeavesNoSnapshot(t *testing.T) {
	root := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: root})
	require.NoError(t, err)

	_, err = mgr.CreateSnapshot(context.Background(), "task-1", fakeFirecracker(t), CreateOptions{
		RootfsPath: filepath.Join(t.TempDir(), "removed-with-the-task.ext4"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to copy task disk")

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, entry.IsDir(), "a failed create must leave no snapshot directory: %s", entry.Name())
	}

	snapshots, err := mgr.ListSnapshots(SnapshotFilter{})
	require.NoError(t, err)
	assert.Empty(t, snapshots, "a failed create must leave no metadata")
}

// TestCreateSnapshotRefusesAnExistingSnapshotDirectory pins that a generated ID
// that already has a directory is retried and then refused. Reusing the
// directory would overwrite a valid restore point.
func TestCreateSnapshotRefusesAnExistingSnapshotDirectory(t *testing.T) {
	root := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: root})
	require.NoError(t, err)

	// Force every generated ID to the same value, so the create cannot escape the
	// collision by drawing a different one.
	original := snapshotIDSource
	t.Cleanup(func() { snapshotIDSource = original })
	snapshotIDSource = func(buf []byte) (int, error) {
		for i := range buf {
			buf[i] = 0x2a
		}
		return len(buf), nil
	}

	id := generateSnapshotID()
	require.True(t, ValidSnapshotID(id))

	existingDir := filepath.Join(root, id)
	require.NoError(t, os.MkdirAll(existingDir, 0o755))
	sentinel := filepath.Join(existingDir, "keep")
	require.NoError(t, os.WriteFile(sentinel, []byte("existing snapshot"), 0o600))

	rootfsPath := filepath.Join(t.TempDir(), "task-1.ext4")
	require.NoError(t, os.WriteFile(rootfsPath, []byte("disk"), 0o600))

	_, err = mgr.CreateSnapshot(context.Background(), "task-1", fakeFirecracker(t), CreateOptions{
		RootfsPath: rootfsPath,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unique snapshot directory")

	kept, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	assert.Equal(t, "existing snapshot", string(kept), "the existing snapshot must not be overwritten")

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the refused create leaves no new directory")
}

// TestRestoreFromSnapshotCopiesTheDiskBack pins the restore half: the executor
// deleted the task disk and its directory, and the snapshot puts both back.
func TestRestoreFromSnapshotCopiesTheDiskBack(t *testing.T) {
	root := t.TempDir()
	snapshotDir := filepath.Join(root, "snapshots")
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: snapshotDir})
	require.NoError(t, err)

	content := testDiskContent()
	id := "snap-0123456789abcdef"
	dir := filepath.Join(snapshotDir, id)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	statePath := filepath.Join(dir, "vm.state")
	memoryPath := filepath.Join(dir, "vm.mem")
	diskPath := filepath.Join(dir, diskFileName)
	require.NoError(t, os.WriteFile(statePath, []byte("state"), 0o600))
	require.NoError(t, os.WriteFile(memoryPath, []byte("memory"), 0o600))
	require.NoError(t, os.WriteFile(diskPath, content, 0o600))

	sum, err := sha256File(statePath)
	require.NoError(t, err)

	// The parent directory is gone too, as it is after task cleanup.
	rootfsPath := filepath.Join(root, "rootfs", "tasks", "task-1.ext4")
	info := &SnapshotInfo{
		ID:         id,
		TaskID:     "task-1",
		StatePath:  statePath,
		MemoryPath: memoryPath,
		DiskPath:   diskPath,
		RootfsPath: rootfsPath,
		Checksum:   sum,
	}

	// Hide firecracker: the test proves the disk is back before the VMM starts,
	// and it must not depend on a real binary being absent from PATH.
	t.Setenv("PATH", t.TempDir())

	err = mgr.RestoreFromSnapshot(context.Background(), info, filepath.Join(root, "run", "fc.sock"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "firecracker binary not found")

	restored, err := os.ReadFile(rootfsPath)
	require.NoError(t, err)
	assert.Len(t, restored, len(content))
	assert.True(t, bytes.Equal(content, restored), "the task disk must come back byte-identical")
}

// TestRestoreFromSnapshotKeepsAnExistingTaskDisk pins the other side: a disk that
// is still there is the live VM disk and is never overwritten by the copy.
func TestRestoreFromSnapshotKeepsAnExistingTaskDisk(t *testing.T) {
	root := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: filepath.Join(root, "snapshots")})
	require.NoError(t, err)

	id := "snap-0123456789abcdef"
	dir := filepath.Join(root, "snapshots", id)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	statePath := filepath.Join(dir, "vm.state")
	memoryPath := filepath.Join(dir, "vm.mem")
	diskPath := filepath.Join(dir, diskFileName)
	require.NoError(t, os.WriteFile(statePath, []byte("state"), 0o600))
	require.NoError(t, os.WriteFile(memoryPath, []byte("memory"), 0o600))
	require.NoError(t, os.WriteFile(diskPath, []byte("snapshot disk"), 0o600))

	sum, err := sha256File(statePath)
	require.NoError(t, err)

	rootfsPath := filepath.Join(root, "task-1.ext4")
	require.NoError(t, os.WriteFile(rootfsPath, []byte("live disk"), 0o600))

	info := &SnapshotInfo{
		ID:         id,
		TaskID:     "task-1",
		StatePath:  statePath,
		MemoryPath: memoryPath,
		DiskPath:   diskPath,
		RootfsPath: rootfsPath,
		Checksum:   sum,
	}

	t.Setenv("PATH", t.TempDir())

	err = mgr.RestoreFromSnapshot(context.Background(), info, filepath.Join(root, "run", "fc.sock"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "firecracker binary not found")

	kept, err := os.ReadFile(rootfsPath)
	require.NoError(t, err)
	assert.Equal(t, "live disk", string(kept), "an existing task disk must not be replaced")
}

// TestRestoreFromSnapshotIncompleteWithoutDiskCopy pins that a snapshot whose
// copy and whose task disk are both gone fails before any process is started.
func TestRestoreFromSnapshotIncompleteWithoutDiskCopy(t *testing.T) {
	root := t.TempDir()
	snapshotDir := filepath.Join(root, "snapshots")
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: snapshotDir})
	require.NoError(t, err)

	id := "snap-0123456789abcdef"
	dir := filepath.Join(snapshotDir, id)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	statePath := filepath.Join(dir, "vm.state")
	memoryPath := filepath.Join(dir, "vm.mem")
	require.NoError(t, os.WriteFile(statePath, []byte("state"), 0o600))
	require.NoError(t, os.WriteFile(memoryPath, []byte("memory"), 0o600))

	info := &SnapshotInfo{
		ID:         id,
		TaskID:     "task-1",
		StatePath:  statePath,
		MemoryPath: memoryPath,
		DiskPath:   filepath.Join(dir, diskFileName),
		RootfsPath: filepath.Join(root, "rootfs", "tasks", "task-1.ext4"),
	}

	err = mgr.RestoreFromSnapshot(context.Background(), info, filepath.Join(root, "run", "fc.sock"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSnapshotIncomplete)
	assert.Contains(t, err.Error(), "disk copy")
}

// TestDeleteSnapshotRemovesTheDiskCopy pins that deleting a snapshot frees the
// disk copy, not just the memory and state files.
func TestDeleteSnapshotRemovesTheDiskCopy(t *testing.T) {
	root := t.TempDir()
	mgr, info := createSnapshotWithDisk(t, SnapshotConfig{SnapshotDir: root})

	require.FileExists(t, info.DiskPath)

	require.NoError(t, mgr.DeleteSnapshot(context.Background(), info.ID))

	_, err := os.Stat(info.DiskPath)
	assert.True(t, os.IsNotExist(err), "the disk copy must be removed with the snapshot")
	_, err = os.Stat(filepath.Join(root, info.ID))
	assert.True(t, os.IsNotExist(err))
}

// TestCleanupOldSnapshotsRemovesTheDiskCopy pins the same for the age sweep, and
// that the freed byte count includes the disk.
func TestCleanupOldSnapshotsRemovesTheDiskCopy(t *testing.T) {
	root := t.TempDir()
	mgr, info := createSnapshotWithDisk(t, SnapshotConfig{
		SnapshotDir: root,
		MaxAge:      24 * time.Hour,
	})

	// Age the snapshot so the sweep selects it.
	snapDir := filepath.Join(root, info.ID)
	aged, err := loadMetadata(snapDir)
	require.NoError(t, err)
	aged.CreatedAt = time.Now().UTC().Add(-48 * time.Hour)
	require.NoError(t, saveMetadata(snapDir, aged))

	removed, freed, err := mgr.CleanupOldSnapshots(context.Background(), 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.Equal(t, info.SizeBytes, freed, "freed bytes include the disk copy")

	_, err = os.Stat(info.DiskPath)
	assert.True(t, os.IsNotExist(err), "the disk copy must be removed with the snapshot")
}
