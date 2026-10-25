package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ordered record of one create. The Firecracker calls are recorded by a fake
// server, the copy by the copyDiskFile seam; the order is the assertion.
const (
	eventPaused   = "vm-paused"
	eventSnapshot = "snapshot-create"
	eventCopy     = "disk-copy"
	eventResumed  = "vm-resumed"
)

// createTimeline records what happened during a create, in order, and whether the
// VM was paused at that moment.
type createTimeline struct {
	mu     sync.Mutex
	events []string
	paused bool
}

func (c *createTimeline) record(event string, paused bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.events = append(c.events, event)
	c.paused = paused
}

func (c *createTimeline) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.events...)
}

func (c *createTimeline) isPaused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.paused
}

// fakeFirecrackerTimeline serves the two endpoints a create uses over a real Unix
// socket, records each call in order, and writes the state and memory files the
// real Firecracker would write. A handler error is reported with a 500 instead of
// t.FailNow, which is illegal from a server goroutine.
func fakeFirecrackerTimeline(t *testing.T) (string, *createTimeline) {
	t.Helper()

	socketPath := testSocketPath(t, "fc.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	timeline := &createTimeline{}

	mux := http.NewServeMux()
	mux.HandleFunc("/vm", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			State string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		switch payload.State {
		case "Paused":
			timeline.record(eventPaused, true)
		case "Resumed":
			timeline.record(eventResumed, false)
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}

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

		timeline.record(eventSnapshot, timeline.isPaused())

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

	return socketPath, timeline
}

// publishedSnapshotDirs returns the snapshot directories under root: everything
// except the staging prefix a create builds in.
func publishedSnapshotDirs(t *testing.T, root string) []string {
	t.Helper()

	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), stagingPrefix) {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
}

// metadataFiles returns every metadata.json under root, so a test can prove a
// failed create published nothing.
func metadataFiles(t *testing.T, root string) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(root, "*", metadataFile))
	require.NoError(t, err)
	return matches
}

// TestCreateSnapshotCopiesTheDiskWhileTheVMIsPaused pins the ordering that makes a
// snapshot one consistent instant: pause, Firecracker snapshot, disk copy, resume.
// Copying after a resume would leave memory at T and the disk at T+copy, so a
// restore could flush page-cache data from the older memory image over newer disk
// state.
func TestCreateSnapshotCopiesTheDiskWhileTheVMIsPaused(t *testing.T) {
	root := t.TempDir()
	socketPath, timeline := fakeFirecrackerTimeline(t)

	rootfsPath := filepath.Join(t.TempDir(), "tasks", "task-1.ext4")
	require.NoError(t, os.MkdirAll(filepath.Dir(rootfsPath), 0o755))
	content := testDiskContent()
	require.NoError(t, os.WriteFile(rootfsPath, content, 0o600))

	mgr, err := NewManager(SnapshotConfig{SnapshotDir: root})
	require.NoError(t, err)

	original := copyDiskFile
	t.Cleanup(func() { copyDiskFile = original })

	var pausedDuringCopy bool
	var visibleBeforePublish []string
	copyDiskFile = func(ctx context.Context, src, dst string) error {
		pausedDuringCopy = timeline.isPaused()
		timeline.record(eventCopy, timeline.isPaused())

		// Publication is the final rename, so while the copy runs nothing may look
		// like a snapshot yet.
		visibleBeforePublish = publishedSnapshotDirs(t, root)

		return original(ctx, src, dst)
	}

	info, err := mgr.CreateSnapshot(context.Background(), "task-1", socketPath, CreateOptions{
		ServiceID:  "web",
		RootfsPath: rootfsPath,
	})
	require.NoError(t, err)

	assert.True(t, pausedDuringCopy, "the disk copy must run while the VM is paused")
	assert.Empty(t, visibleBeforePublish, "no snapshot directory may be visible before publication")
	assert.Equal(t, []string{eventPaused, eventSnapshot, eventCopy, eventResumed}, timeline.snapshot(),
		"the order must be pause, Firecracker snapshot, disk copy, resume")

	copied, err := os.ReadFile(info.DiskPath)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, copied))
}

// TestCreateSnapshotAbortedCopyLeavesNoSnapshotAndResumes pins both halves of a
// create that dies before publication: no <id> directory, no metadata and no
// staging directory survive, and the VM is resumed even though the request
// context was cancelled mid-copy. The seam writes half a disk first, standing in
// for the partial destination a real interrupted copy leaves.
func TestCreateSnapshotAbortedCopyLeavesNoSnapshotAndResumes(t *testing.T) {
	root := t.TempDir()
	socketPath, timeline := fakeFirecrackerTimeline(t)

	rootfsPath := filepath.Join(t.TempDir(), "task-1.ext4")
	content := testDiskContent()
	require.NoError(t, os.WriteFile(rootfsPath, content, 0o600))

	mgr, err := NewManager(SnapshotConfig{SnapshotDir: root})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	original := copyDiskFile
	t.Cleanup(func() { copyDiskFile = original })

	interrupted := errors.New("disk copy interrupted")
	copyDiskFile = func(_ context.Context, _, dst string) error {
		require.NoError(t, os.WriteFile(dst, content[:len(content)/2], 0o600))
		cancel()
		return interrupted
	}

	_, err = mgr.CreateSnapshot(ctx, "task-1", socketPath, CreateOptions{RootfsPath: rootfsPath})
	require.Error(t, err)
	require.ErrorIs(t, err, interrupted)
	assert.Contains(t, err.Error(), "failed to copy task disk")

	assert.Empty(t, publishedSnapshotDirs(t, root),
		"a create that fails before publication must leave no snapshot directory")
	assert.Empty(t, metadataFiles(t, root),
		"a create that fails before publication must leave no metadata")

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), stagingPrefix),
			"the staging directory of a failed create must be removed: %s", entry.Name())
	}

	events := timeline.snapshot()
	require.NotEmpty(t, events)
	assert.Equal(t, eventResumed, events[len(events)-1],
		"the VM must be resumed on the failure path, even with a cancelled context")
}

// TestNewManagerSweepsStaleStagingDirectories pins the bound on a crashed create.
// A staging directory is invisible to every listing (no metadata), so without a
// sweep it would leak disk forever.
func TestNewManagerSweepsStaleStagingDirectories(t *testing.T) {
	root := t.TempDir()

	stale := filepath.Join(root, stagingPrefix+"stale")
	require.NoError(t, os.MkdirAll(stale, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stale, diskFileName), []byte("half a disk"), 0o600))
	old := time.Now().Add(-2 * stagingSweepAge)
	require.NoError(t, os.Chtimes(stale, old, old))

	// A create that is still running must survive: the sweep bounds leftovers, it
	// does not race a copy in flight in another process.
	fresh := filepath.Join(root, stagingPrefix+"fresh")
	require.NoError(t, os.MkdirAll(fresh, 0o755))

	// A published snapshot must never be touched.
	published := filepath.Join(root, "snap-0123456789abcdef")
	require.NoError(t, os.MkdirAll(published, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(published, metadataFile), []byte(`{"id":"snap-0123456789abcdef"}`), 0o600))

	_, err := NewManager(SnapshotConfig{SnapshotDir: root})
	require.NoError(t, err)

	_, err = os.Stat(stale)
	assert.True(t, os.IsNotExist(err), "a stale staging directory must be swept")
	assert.DirExists(t, fresh, "a fresh staging directory may belong to a running create")
	assert.DirExists(t, published, "a published snapshot must survive the sweep")
}

// TestRestoreFromSnapshotFailsWhenTheDiskCannotBeWritten pins the restore half of
// the cleanup: a copy that cannot create its destination stops the restore before
// any process is started and leaves the snapshot intact. The destination's parent
// is a regular file, so the failure does not depend on permissions (the suite may
// run as root).
func TestRestoreFromSnapshotFailsWhenTheDiskCannotBeWritten(t *testing.T) {
	root := t.TempDir()
	snapshotDir := filepath.Join(root, "snapshots")
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: snapshotDir})
	require.NoError(t, err)

	id := "snap-0123456789abcdef"
	dir := filepath.Join(snapshotDir, id)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	statePath := filepath.Join(dir, "vm.state")
	memoryPath := filepath.Join(dir, "vm.mem")
	diskPath := filepath.Join(dir, diskFileName)
	require.NoError(t, os.WriteFile(statePath, []byte("state"), 0o600))
	require.NoError(t, os.WriteFile(memoryPath, []byte("memory"), 0o600))
	require.NoError(t, os.WriteFile(diskPath, testDiskContent(), 0o600))

	sum, err := sha256File(statePath)
	require.NoError(t, err)

	blocked := filepath.Join(root, "rootfs")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o600))

	info := &SnapshotInfo{
		ID:         id,
		TaskID:     "task-1",
		StatePath:  statePath,
		MemoryPath: memoryPath,
		DiskPath:   diskPath,
		RootfsPath: filepath.Join(blocked, "tasks", "task-1.ext4"),
		Checksum:   sum,
	}

	t.Setenv("PATH", t.TempDir())

	err = mgr.RestoreFromSnapshot(context.Background(), info, filepath.Join(root, "run", "fc.sock"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to restore task disk")

	assert.FileExists(t, diskPath, "a failed restore must not consume the snapshot copy")
}
