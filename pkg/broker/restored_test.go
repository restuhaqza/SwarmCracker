package broker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
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
