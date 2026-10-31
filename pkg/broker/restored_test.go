package broker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

// serveRestored drives the restored-VM operation directly. The server's own
// method and 405 handling is exercised separately; this is the handler's view.
func serveRestored(t *testing.T, cfg SnapshotHandlerConfig, task Task, method, target string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	NewRestoredHandler(cfg)(context.Background(), task, rec, req)
	return rec
}

// TestRestoredHandlerMethodNotAllowed pins 405 with Allow on the one method the
// operation does not implement, independent of whether the store exists.
func TestRestoredHandlerMethodNotAllowed(t *testing.T) {
	store := &fakeSnapshotStore{}
	rec := serveRestored(t, SnapshotHandlerConfig{Store: store}, snapshotTask(), http.MethodGet, "/v1/tasks/task-1/restored")

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "DELETE", rec.Header().Get("Allow"))

	doc := decodeSnapshotError(t, rec)
	assert.Equal(t, "method_not_allowed", doc.Error.Details["reason"])
	assert.Empty(t, store.stoppedRestored, "a wrong method must not reach the store")
}

// TestRestoredHandlerDisabledIs501 pins the typed 501: a node with snapshots
// disabled keeps the route registered and names the capability, exactly like the
// snapshots operation.
func TestRestoredHandlerDisabledIs501(t *testing.T) {
	rec := serveRestored(t, SnapshotHandlerConfig{}, snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored")

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	doc := decodeSnapshotError(t, rec)
	assert.Equal(t, v1.KindError, doc.Kind)
	assert.Equal(t, "snapshots", doc.Error.Details["capability"])
	assert.Equal(t, "snapshots_disabled", doc.Error.Details["reason"])
}

// TestRestoredHandlerNoRestoredVMIs404 pins the second documented error: a node
// that tracks no restored VM for the task answers no_restored_vm, not a generic
// store failure.
func TestRestoredHandlerNoRestoredVMIs404(t *testing.T) {
	store := &fakeSnapshotStore{
		stopRestoredErr: errors.Join(snapshot.ErrNoRestoredVM, errors.New("nothing tracked")),
	}
	rec := serveRestored(t, SnapshotHandlerConfig{Store: store}, snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored")

	require.Equal(t, http.StatusNotFound, rec.Code)
	doc := decodeSnapshotError(t, rec)
	assert.Equal(t, v1.KindError, doc.Kind)
	assert.Equal(t, string(output.CodeNotFound), doc.Error.Code)
	assert.Equal(t, "no_restored_vm", doc.Error.Details["reason"])
	assert.Equal(t, []string{"task-1"}, store.stoppedRestored)
}

// TestRestoredHandlerStopsAndLeavesTheSocketToTheStore pins success: 204, the
// store stopped exactly the resolved task, and the handler itself does not touch
// the filesystem. The store owns the socket (it unlinks it inside StopRestored
// under the per-task restore lock), so a handler-side unlink would race a
// concurrent restore.
func TestRestoredHandlerStopsAndLeavesTheSocketToTheStore(t *testing.T) {
	socketDir := t.TempDir()
	socketPath := filepath.Join(socketDir, "task-1-restored.sock")
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))

	store := &fakeSnapshotStore{}
	rec := serveRestored(t, SnapshotHandlerConfig{Store: store, SocketDir: socketDir},
		snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored")

	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String(), "204 carries no document")
	assert.Equal(t, []string{"task-1"}, store.stoppedRestored)
	assert.FileExists(t, socketPath, "only the store may unlink the restored VM's socket")
}

// TestRestoredHandlerNeedsNoSocketDir pins that the handler has no filesystem
// work of its own: a node with no socket directory configured still answers 204
// for a store that stopped its VM.
func TestRestoredHandlerNeedsNoSocketDir(t *testing.T) {
	store := &fakeSnapshotStore{}
	rec := serveRestored(t, SnapshotHandlerConfig{Store: store},
		snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored")

	require.Equal(t, http.StatusNoContent, rec.Code)
}

// TestRestoredHandlerRealStoreRemovesTheSocket pins the ownership claim end to
// end: the handler relays, and the store — a real manager — ends the process and
// unlinks the socket it recorded.
func TestRestoredHandlerRealStoreRemovesTheSocket(t *testing.T) {
	socketDir := t.TempDir()
	socketPath := filepath.Join(socketDir, "task-1-restored.sock")
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))

	mgr, err := snapshot.NewManager(snapshot.SnapshotConfig{SnapshotDir: t.TempDir()})
	require.NoError(t, err)
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	mgr.TrackRestoredProcess("task-1", cmd.Process, socketPath)

	rec := serveRestored(t, SnapshotHandlerConfig{Store: mgr, SocketDir: socketDir},
		snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	_, statErr := os.Stat(socketPath)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "the store removes the socket it tracked")
}

// TestRestoredHandlerStoreErrorMaps pins that any other store failure goes
// through the shared snapshot mapping rather than leaking pkg/snapshot's text.
func TestRestoredHandlerStoreErrorMaps(t *testing.T) {
	store := &fakeSnapshotStore{stopRestoredErr: errors.New("permission denied on /var/run/firecracker")}
	rec := serveRestored(t, SnapshotHandlerConfig{Store: store}, snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored")

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	doc := decodeSnapshotError(t, rec)
	assert.Equal(t, "snapshot_store_error", doc.Error.Details["reason"])
	assert.NotContains(t, doc.Error.Message, "/var/run/firecracker", "the node path must never cross the wire")
}

// TestRestoredHandlerSecondStopIs404 pins idempotence through the real store: the
// first stop ends a live process and removes the entry, the second answers the
// documented 404.
func TestRestoredHandlerSecondStopIs404(t *testing.T) {
	mgr, err := snapshot.NewManager(snapshot.SnapshotConfig{SnapshotDir: t.TempDir()})
	require.NoError(t, err)

	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	mgr.TrackRestoredProcess("task-1", cmd.Process, "")

	handler := NewRestoredHandler(SnapshotHandlerConfig{Store: mgr, SocketDir: t.TempDir()})

	first := serveHandler(t, handler, snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored", "")
	require.Equal(t, http.StatusNoContent, first.Code, first.Body.String())
	_, _ = cmd.Process.Wait()

	second := serveHandler(t, handler, snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored", "")
	require.Equal(t, http.StatusNotFound, second.Code, second.Body.String())

	doc := decodeSnapshotError(t, second)
	assert.Equal(t, "no_restored_vm", doc.Error.Details["reason"])
}

// TestRestoredHandlerStopsAndForgetsTheRecord pins the whole stop of a restored
// task: the VM is stopped through the store, and the record that named it is
// deleted in the same request. Leaving it would make the record mean "the task this
// node restored" with no VM behind it, forever.
func TestRestoredHandlerStopsAndForgetsTheRecord(t *testing.T) {
	record := brokerRestoredRecord(t, "task-1", "source-task")
	store := &fakeSnapshotStore{}
	records := &fakeRestoredSource{record: record, found: true}

	rec := serveRestored(t, SnapshotHandlerConfig{Store: store, Records: records},
		snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored")

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"task-1"}, store.stoppedRestored, "the VM the record names is stopped")
	assert.Equal(t, []string{"task-1"}, records.deletes, "a confirmed stop forgets the record")
}

// TestRestoredHandlerKeepsARecordWhoseVMIsStillRunning pins the daemon restart: the
// store's process table is in memory, so "no such restored VM" does not mean the VM
// is gone. A recorded socket that still answers proves a live Firecracker, so the
// record survives and the answer says an operator has to stop it.
func TestRestoredHandlerKeepsARecordWhoseVMIsStillRunning(t *testing.T) {
	record := brokerRestoredRecord(t, "task-1", "source-task")
	liveAPISocket(t, record.SocketPath)

	store := &fakeSnapshotStore{stopRestoredErr: snapshot.ErrNoRestoredVM}
	records := &fakeRestoredSource{record: record, found: true}

	rec := serveRestored(t, SnapshotHandlerConfig{Store: store, Records: records},
		snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored")

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	doc := decodeSnapshotError(t, rec)
	assert.Equal(t, v1.KindError, doc.Kind)
	assert.Equal(t, string(output.CodeConflict), doc.Error.Code)
	assert.Equal(t, "restored_vm_untracked", doc.Error.Details["reason"])
	assert.Contains(t, doc.Error.Message, "manually",
		"the answer must say what the operator has to do")
	assert.NotContains(t, doc.Error.Message, record.SocketPath,
		"a node path must never cross the wire")
	assert.Empty(t, records.deletes, "a live VM's record is the only pointer to its process")
}

// TestRestoredHandlerForgetsARecordWhoseVMIsGone pins the other half of the same
// rule: the store tracks no process *and* the recorded socket no longer answers, so
// the VM really is gone. The record goes and the stop succeeds — retrying it can
// never do better.
func TestRestoredHandlerForgetsARecordWhoseVMIsGone(t *testing.T) {
	record := brokerRestoredRecord(t, "task-1", "source-task")
	store := &fakeSnapshotStore{stopRestoredErr: snapshot.ErrNoRestoredVM}
	records := &fakeRestoredSource{record: record, found: true}

	rec := serveRestored(t, SnapshotHandlerConfig{Store: store, Records: records},
		snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored")

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"task-1"}, records.deletes)
}

// TestRestoredHandlerReportsARecordItCannotDelete pins the last step of the rule:
// the VM stopped, so the caller must learn that only the record cleanup failed.
func TestRestoredHandlerReportsARecordItCannotDelete(t *testing.T) {
	record := brokerRestoredRecord(t, "task-1", "source-task")
	records := &fakeRestoredSource{
		record: record, found: true, deleteErr: errors.New("read-only file system"),
	}

	rec := serveRestored(t, SnapshotHandlerConfig{Store: &fakeSnapshotStore{}, Records: records},
		snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored")

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	doc := decodeSnapshotError(t, rec)
	assert.Equal(t, "snapshot_store_error", doc.Error.Details["reason"])
	assert.NotContains(t, doc.Error.Message, "read-only", "the store's text stays on the node")
}

// TestRestoredHandlerUnreadableRecordIsAnError pins the corrupt record: it names no
// VM the handler may act on, the record is kept for inspection, and the store is
// never asked to stop a process the node cannot identify.
func TestRestoredHandlerUnreadableRecordIsAnError(t *testing.T) {
	store := &fakeSnapshotStore{}
	records := &fakeRestoredSource{err: errors.New("restored-instance record is corrupt")}

	rec := serveRestored(t, SnapshotHandlerConfig{Store: store, Records: records},
		snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored")

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	doc := decodeSnapshotError(t, rec)
	assert.Equal(t, string(output.CodeInternal), doc.Error.Code)
	assert.NotContains(t, doc.Error.Message, "corrupt")
	assert.Empty(t, store.stoppedRestored, "no store call for a record nobody can read")
	assert.Empty(t, records.deletes, "an unusable record is left in place for inspection")
}

// TestRestoredHandlerWithoutRecordsRelaysTheStore pins the node that keeps no
// records — the experimental snapshots operation restores a VM without one — and
// the store's own answer is the whole answer.
func TestRestoredHandlerWithoutRecordsRelaysTheStore(t *testing.T) {
	store := &fakeSnapshotStore{}
	rec := serveRestored(t, SnapshotHandlerConfig{Store: store},
		snapshotTask(), http.MethodDelete, "/v1/tasks/task-1/restored")

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"task-1"}, store.stoppedRestored)
}

// brokerRestoredRecord is the record a node writes for a restored task, with the
// API socket under a short directory: a Unix socket path is capped at ~104 bytes,
// and the liveness probe needs a real one.
func brokerRestoredRecord(t *testing.T, taskID, sourceID string) runtime.RestoredInstance {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "sc-broker-restored-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return runtime.RestoredInstance{
		TaskID:     taskID,
		VMID:       sourceID,
		ArtifactID: "mig-0123456789abcdef",
		SocketPath: filepath.Join(dir, taskID+".sock"),
		TapName:    runtime.TapName(sourceID, 0),
		VsockPath:  runtime.VsockPath(dir, sourceID),
		DiskPath:   filepath.Join(dir, sourceID+".ext4"),
		CreatedAt:  time.Now().UTC(),
	}
}

// liveAPISocket publishes a Unix socket that answers the Firecracker liveness
// probe, which is what a restored VM's socket looks like while its process runs.
func liveAPISocket(t *testing.T, socketPath string) {
	t.Helper()

	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// TestRestoredHandlerRejectsQuery pins the empty query grammar: the operation
// takes no parameter, so a caller's typo is 400 rather than silently ignored.
func TestRestoredHandlerRejectsQuery(t *testing.T) {
	store := &fakeSnapshotStore{}
	rec := serveRestored(t, SnapshotHandlerConfig{Store: store}, snapshotTask(),
		http.MethodDelete, "/v1/tasks/task-1/restored?id=snap-1")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	doc := decodeSnapshotError(t, rec)
	assert.Equal(t, "id", doc.Error.Details["parameter"])
	assert.Empty(t, store.stoppedRestored)
}
