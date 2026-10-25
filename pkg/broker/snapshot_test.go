package broker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

// fakeSnapshotStore is the narrow SnapshotStore the handler sees. It records
// every call and returns scripted results, so no test ever reaches the real
// Firecracker/exec path.
type fakeSnapshotStore struct {
	createInfo *snapshot.SnapshotInfo
	createErr  error
	gotTaskID  string
	gotSocket  string
	gotCreate  snapshot.CreateOptions

	list      []*snapshot.SnapshotInfo
	listErr   error
	gotFilter snapshot.SnapshotFilter

	restoreErr     error
	gotRestoreInfo *snapshot.SnapshotInfo
	gotRestoreSock string

	deleteErr error
	deleted   []string
}

func (f *fakeSnapshotStore) CreateSnapshot(
	_ context.Context, taskID, socketPath string, opts snapshot.CreateOptions,
) (*snapshot.SnapshotInfo, error) {
	f.gotTaskID, f.gotSocket, f.gotCreate = taskID, socketPath, opts
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.createInfo, nil
}

func (f *fakeSnapshotStore) ListSnapshots(filter snapshot.SnapshotFilter) ([]*snapshot.SnapshotInfo, error) {
	f.gotFilter = filter
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.list, nil
}

func (f *fakeSnapshotStore) RestoreFromSnapshot(
	_ context.Context, info *snapshot.SnapshotInfo, socketPath string,
) error {
	f.gotRestoreInfo, f.gotRestoreSock = info, socketPath
	return f.restoreErr
}

func (f *fakeSnapshotStore) DeleteSnapshot(_ context.Context, snapshotID string) error {
	f.deleted = append(f.deleted, snapshotID)
	return f.deleteErr
}

// snapshotTask is the running task every node-handler test serves.
func snapshotTask() Task {
	return Task{
		ID:         "task-1",
		SocketPath: "/var/run/firecracker/task-1.sock",
		Running:    true,
		LogPath:    "/var/run/swarmcracker/task-1.log",
	}
}

// serveSnapshot drives the handler directly: task resolution is the server's
// job and is exercised separately.
func serveSnapshot(t *testing.T, store SnapshotStore, task Task, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	rec := httptest.NewRecorder()
	NewSnapshotHandler(store)(context.Background(), task, rec, req)
	return rec
}

func decodeSnapshotError(t *testing.T, rec *httptest.ResponseRecorder) v1.ErrorDocument {
	t.Helper()

	var doc v1.ErrorDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc), rec.Body.String())
	return doc
}

func testSnapshotInfo(id, taskID string, createdAt time.Time) *snapshot.SnapshotInfo {
	return &snapshot.SnapshotInfo{
		ID:         id,
		TaskID:     taskID,
		ServiceID:  "svc-1",
		NodeID:     "node-1",
		CreatedAt:  createdAt,
		MemoryPath: "/var/lib/firecracker/snapshots/" + id + "/vm.mem",
		StatePath:  "/var/lib/firecracker/snapshots/" + id + "/vm.state",
		SizeBytes:  1024,
		VCPUCount:  2,
		MemoryMB:   512,
		RootfsPath: "/var/lib/firecracker/rootfs/task-1",
		Checksum:   "deadbeef",
	}
}

// TestSnapshotHandlerListSortsAndFilters pins the list contract: the node filters
// to the resolved task and answers ascending (createdAt, id), even though
// pkg/snapshot returns newest first.
func TestSnapshotHandlerListSortsAndFilters(t *testing.T) {
	base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	store := &fakeSnapshotStore{list: []*snapshot.SnapshotInfo{
		testSnapshotInfo("snap-b", "task-1", base.Add(2*time.Hour)),
		testSnapshotInfo("snap-c", "task-1", base.Add(time.Hour)),
		testSnapshotInfo("snap-a", "task-1", base.Add(time.Hour)),
		testSnapshotInfo("snap-other", "task-2", base),
	}}

	rec := serveSnapshot(t, store, snapshotTask(), http.MethodGet, "/v1/tasks/task-1/snapshots", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, publicMediaType, rec.Header().Get("Content-Type"))
	require.Equal(t, "task-1", store.gotFilter.TaskID)

	var doc v1.SnapshotList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, v1.KindSnapshotList, doc.Kind)
	require.Len(t, doc.Items, 3)
	require.Equal(t, []string{"snap-a", "snap-c", "snap-b"},
		[]string{doc.Items[0].ID, doc.Items[1].ID, doc.Items[2].ID})
	require.NotContains(t, rec.Body.String(), "vm.mem")
	require.NotContains(t, rec.Body.String(), "vm.state")
}

// TestSnapshotHandlerCreateUsesTheTaskSocket pins the create path: the handler
// snapshots the resolved task's own socket and never a caller-supplied path.
func TestSnapshotHandlerCreateUsesTheTaskSocket(t *testing.T) {
	t.Run("empty body", func(t *testing.T) {
		info := testSnapshotInfo("snap-1", "task-1", time.Now().UTC())
		store := &fakeSnapshotStore{createInfo: info}

		rec := serveSnapshot(t, store, snapshotTask(), http.MethodPost, "/v1/tasks/task-1/snapshots", "")

		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		require.Equal(t, "task-1", store.gotTaskID)
		require.Equal(t, "/var/run/firecracker/task-1.sock", store.gotSocket)
		require.Equal(t, snapshot.CreateOptions{}, store.gotCreate)

		var doc v1.SnapshotDocument
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
		require.Equal(t, v1.KindSnapshot, doc.Kind)
		require.Equal(t, "snap-1", doc.ID)
	})

	t.Run("explicit resources", func(t *testing.T) {
		store := &fakeSnapshotStore{createInfo: testSnapshotInfo("snap-1", "task-1", time.Now().UTC())}

		rec := serveSnapshot(t, store, snapshotTask(), http.MethodPost, "/v1/tasks/task-1/snapshots",
			`{"vcpus": 2, "memoryMB": 1024, "rootfsPath": "/var/lib/firecracker/rootfs/x"}`)

		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		require.Equal(t, snapshot.CreateOptions{
			VCPUCount:  2,
			MemoryMB:   1024,
			RootfsPath: "/var/lib/firecracker/rootfs/x",
		}, store.gotCreate)
	})

	t.Run("unknown field", func(t *testing.T) {
		store := &fakeSnapshotStore{}

		rec := serveSnapshot(t, store, snapshotTask(), http.MethodPost, "/v1/tasks/task-1/snapshots",
			`{"diskMB": 10}`)

		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Empty(t, store.gotTaskID, "an invalid body must not reach the store")
		require.Equal(t, string(output.CodeInvalid), decodeSnapshotError(t, rec).Error.Code)
	})
}

// TestSnapshotHandlerCreateLogOnlyTaskIs404 pins the not-found answer for a task
// whose VM has exited: snapshotting needs a live Firecracker API socket.
func TestSnapshotHandlerCreateLogOnlyTaskIs404(t *testing.T) {
	store := &fakeSnapshotStore{}
	logOnly := Task{ID: "task-1", LogPath: "/var/run/swarmcracker/task-1.log"}

	rec := serveSnapshot(t, store, logOnly, http.MethodPost, "/v1/tasks/task-1/snapshots", "")

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	doc := decodeSnapshotError(t, rec)
	require.Equal(t, string(output.CodeNotFound), doc.Error.Code)
	require.Equal(t, "task_not_running", doc.Error.Details["reason"])
	require.Empty(t, store.gotTaskID)
}

// TestSnapshotHandlerRestoreUsesTheRestoredSocket pins restore: the snapshot must
// belong to the task and the process is started on the restored socket name.
func TestSnapshotHandlerRestoreUsesTheRestoredSocket(t *testing.T) {
	info := testSnapshotInfo("snap-1", "task-1", time.Now().UTC())
	store := &fakeSnapshotStore{list: []*snapshot.SnapshotInfo{info}}

	rec := serveSnapshot(t, store, snapshotTask(), http.MethodPost,
		"/v1/tasks/task-1/snapshots?restore=snap-1", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Same(t, info, store.gotRestoreInfo)
	require.Equal(t, "/var/run/firecracker/task-1-restored.sock", store.gotRestoreSock)

	var doc v1.SnapshotDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, "snap-1", doc.ID)
}

// TestSnapshotHandlerRestoreOtherTaskSnapshotIs404 proves one task cannot restore
// another task's snapshot through its own route.
func TestSnapshotHandlerRestoreOtherTaskSnapshotIs404(t *testing.T) {
	store := &fakeSnapshotStore{list: []*snapshot.SnapshotInfo{
		testSnapshotInfo("snap-other", "task-2", time.Now().UTC()),
	}}

	rec := serveSnapshot(t, store, snapshotTask(), http.MethodPost,
		"/v1/tasks/task-1/snapshots?restore=snap-other", "")

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	doc := decodeSnapshotError(t, rec)
	require.Equal(t, "snapshot_not_found", doc.Error.Details["reason"])
	require.Nil(t, store.gotRestoreInfo)
}

// TestSnapshotHandlerDeleteRemovesTheTaskSnapshot pins the 204 delete.
func TestSnapshotHandlerDeleteRemovesTheTaskSnapshot(t *testing.T) {
	store := &fakeSnapshotStore{list: []*snapshot.SnapshotInfo{
		testSnapshotInfo("snap-1", "task-1", time.Now().UTC()),
	}}

	rec := serveSnapshot(t, store, snapshotTask(), http.MethodDelete,
		"/v1/tasks/task-1/snapshots?id=snap-1", "")

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Empty(t, rec.Body.String(), "a 204 has no body")
	require.Equal(t, []string{"snap-1"}, store.deleted)
}

// TestSnapshotHandlerDeleteMissingIs404 pins the missing snapshot answer.
func TestSnapshotHandlerDeleteMissingIs404(t *testing.T) {
	store := &fakeSnapshotStore{}

	rec := serveSnapshot(t, store, snapshotTask(), http.MethodDelete,
		"/v1/tasks/task-1/snapshots?id=snap-missing", "")

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	doc := decodeSnapshotError(t, rec)
	require.Equal(t, string(output.CodeNotFound), doc.Error.Code)
	require.Equal(t, "snapshot_not_found", doc.Error.Details["reason"])
	require.Empty(t, store.deleted, "a snapshot the node does not know must not be deleted")
}

// TestSnapshotHandlerDisabledStoreIs501 pins the defence-in-depth gate: a nil
// store keeps the route but implements nothing.
func TestSnapshotHandlerDisabledStoreIs501(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rec := serveSnapshot(t, nil, snapshotTask(), method, "/v1/tasks/task-1/snapshots", "")

			require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
			doc := decodeSnapshotError(t, rec)
			require.Equal(t, string(output.CodeNotImplemented), doc.Error.Code)
			require.Equal(t, "snapshots", doc.Error.Details["capability"])
			require.Equal(t, "snapshots_disabled", doc.Error.Details["reason"])
		})
	}
}

// TestSnapshotHandlerMalformedQueryIs400 pins the query rules: only the
// `restore` and `id` parameters exist, once each, and each method owns its own.
func TestSnapshotHandlerMalformedQueryIs400(t *testing.T) {
	cases := []struct {
		name   string
		method string
		target string
	}{
		{"unknown parameter", http.MethodGet, "/v1/tasks/task-1/snapshots?foo=bar"},
		{"duplicate parameter", http.MethodDelete, "/v1/tasks/task-1/snapshots?id=snap-1&id=snap-2"},
		{"list with restore", http.MethodGet, "/v1/tasks/task-1/snapshots?restore=snap-1"},
		{"create with id", http.MethodPost, "/v1/tasks/task-1/snapshots?id=snap-1"},
		{"empty restore id", http.MethodPost, "/v1/tasks/task-1/snapshots?restore="},
		{"malformed restore id", http.MethodPost, "/v1/tasks/task-1/snapshots?restore=../etc"},
		{"delete without id", http.MethodDelete, "/v1/tasks/task-1/snapshots"},
		{"delete with restore", http.MethodDelete, "/v1/tasks/task-1/snapshots?restore=snap-1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeSnapshotStore{}

			rec := serveSnapshot(t, store, snapshotTask(), tc.method, tc.target, "")

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Equal(t, string(output.CodeInvalid), decodeSnapshotError(t, rec).Error.Code)
			require.Empty(t, store.deleted)
			require.Empty(t, store.gotTaskID)
		})
	}
}

// TestSnapshotHandlerStoreErrorIs503WithoutPaths proves a store failure is a
// stable, retryable message: pkg/snapshot errors carry absolute paths, socket
// names and checksums, none of which may reach the wire.
func TestSnapshotHandlerStoreErrorIs503WithoutPaths(t *testing.T) {
	leaky := errors.New("failed to create snapshot via Firecracker API: " +
		"unexpected status 400 for /snapshot/create: /var/lib/firecracker/snapshots/snap-1/vm.state checksum deadbeef")

	t.Run("create", func(t *testing.T) {
		store := &fakeSnapshotStore{createErr: leaky}

		rec := serveSnapshot(t, store, snapshotTask(), http.MethodPost, "/v1/tasks/task-1/snapshots", "")

		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
		doc := decodeSnapshotError(t, rec)
		require.Equal(t, string(output.CodeUnavailable), doc.Error.Code)
		require.True(t, doc.Error.Retryable)
		requireNoSnapshotLeak(t, rec.Body.String())
	})

	t.Run("list", func(t *testing.T) {
		store := &fakeSnapshotStore{listErr: errors.New("failed to read snapshot directory: /var/lib/firecracker/snapshots")}

		rec := serveSnapshot(t, store, snapshotTask(), http.MethodGet, "/v1/tasks/task-1/snapshots", "")

		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
		requireNoSnapshotLeak(t, rec.Body.String())
	})

	t.Run("restore", func(t *testing.T) {
		store := &fakeSnapshotStore{
			list:       []*snapshot.SnapshotInfo{testSnapshotInfo("snap-1", "task-1", time.Now().UTC())},
			restoreErr: errors.New("firecracker API not ready after restore: /var/run/firecracker/task-1-restored.sock"),
		}

		rec := serveSnapshot(t, store, snapshotTask(), http.MethodPost,
			"/v1/tasks/task-1/snapshots?restore=snap-1", "")

		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
		requireNoSnapshotLeak(t, rec.Body.String())
	})
}

// requireNoSnapshotLeak asserts an error body names no node path, socket file or
// checksum.
func requireNoSnapshotLeak(t *testing.T, body string) {
	t.Helper()

	for _, forbidden := range []string{
		"/var/", "vm.state", "vm.mem", "task-1.sock", "deadbeef",
	} {
		require.NotContains(t, body, forbidden, "the error body must not leak %q", forbidden)
	}
}

// TestSnapshotHandlerMethodNotAllowed pins the 405 answer.
func TestSnapshotHandlerMethodNotAllowed(t *testing.T) {
	rec := serveSnapshot(t, &fakeSnapshotStore{}, snapshotTask(), http.MethodPut,
		"/v1/tasks/task-1/snapshots", "")

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	require.Equal(t, "GET, POST, DELETE", rec.Header().Get("Allow"))
}

// TestSnapshotHandlerCreateBodyCap pins the 64 KiB create cap.
func TestSnapshotHandlerCreateBodyCap(t *testing.T) {
	store := &fakeSnapshotStore{}

	body := `{"rootfsPath": "` + strings.Repeat("a", 64<<10) + `"}`
	rec := serveSnapshot(t, store, snapshotTask(), http.MethodPost, "/v1/tasks/task-1/snapshots", body)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	require.Empty(t, store.gotTaskID)
}

// TestNewSnapshotHandlerDuplicateRegistration proves the node cannot silently
// overwrite the operation: the registry's duplicate error is returned.
func TestNewSnapshotHandlerDuplicateRegistration(t *testing.T) {
	srv := newRouteServer(t, &fakeTaskSource{})
	require.NoError(t, srv.Handle("snapshots", NewSnapshotHandler(&fakeSnapshotStore{})))
	require.Error(t, srv.Handle("snapshots", NewSnapshotHandler(&fakeSnapshotStore{})))
}

// --- manager side ---

// snapshotTestManager dials the test broker at baseURL. The snapshot timeout and
// the request-ID source are explicit so a test can pin both.
func snapshotTestManager(t *testing.T, pki testPKI, baseURL string, timeout time.Duration,
	requestIDFrom func(context.Context) string,
) *Manager {
	t.Helper()

	host, port := brokerTarget(t, baseURL)
	mgr, err := NewManager(ManagerConfig{
		Tasks:           &stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		Nodes:           &stubNodeReader{node: v1.Node{ID: "node-1", Address: host}},
		Port:            port,
		Client:          pki.httpClient(),
		SnapshotTimeout: timeout,
		RequestIDFrom:   requestIDFrom,
	})
	require.NoError(t, err)
	return mgr
}

func encodeNodeSnapshot(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()

	body, err := encodeDocument(value)
	require.NoError(t, err)
	w.Header().Set("Content-Type", publicMediaType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// TestManagerCreateSnapshotSendsPost pins the create request: POST the task's
// snapshot operation, no query, the caller's request ID and a 201 document back.
func TestManagerCreateSnapshotSendsPost(t *testing.T) {
	pki := newTestPKI(t)

	var gotMethod, gotPath, gotQuery, gotRequestID string
	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"snapshots": func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
			gotRequestID = r.Header.Get(requestIDHeader)
			encodeNodeSnapshot(t, w, http.StatusCreated, v1.NewSnapshotDocument(v1.Snapshot{
				ID: "snap-1", TaskID: "task-1", CreatedAt: "2026-10-24T12:00:00Z",
			}))
		},
	})

	mgr := snapshotTestManager(t, pki, baseURL, 0, func(context.Context) string {
		return "req_abc123def456"
	})
	got, err := mgr.CreateSnapshot(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"})

	require.NoError(t, err)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/v1/tasks/task-1/snapshots", gotPath)
	require.Empty(t, gotQuery)
	require.Equal(t, "req_abc123def456", gotRequestID)
	require.Equal(t, "snap-1", got.ID)
	require.Equal(t, "task-1", got.TaskID)
}

// TestManagerListTaskSnapshotsSendsGet pins the list request and its document.
func TestManagerListTaskSnapshotsSendsGet(t *testing.T) {
	pki := newTestPKI(t)

	var gotMethod, gotQuery string
	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"snapshots": func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			gotMethod, gotQuery = r.Method, r.URL.RawQuery
			doc := v1.NewSnapshotList([]v1.Snapshot{{ID: "snap-1"}, {ID: "snap-2"}})
			encodeNodeSnapshot(t, w, http.StatusOK, doc)
		},
	})

	got, err := snapshotTestManager(t, pki, baseURL, 0, nil).
		ListTaskSnapshots(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"})

	require.NoError(t, err)
	require.Equal(t, http.MethodGet, gotMethod)
	require.Empty(t, gotQuery)
	require.Len(t, got, 2)
	require.Equal(t, "snap-2", got[1].ID)
}

// TestManagerRestoreSnapshotSendsQuery pins restore: POST with restore=<id>, and
// a 200 document back.
func TestManagerRestoreSnapshotSendsQuery(t *testing.T) {
	pki := newTestPKI(t)

	var gotMethod, gotQuery string
	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"snapshots": func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			gotMethod, gotQuery = r.Method, r.URL.RawQuery
			encodeNodeSnapshot(t, w, http.StatusOK, v1.NewSnapshotDocument(v1.Snapshot{ID: "snap-1"}))
		},
	})

	got, err := snapshotTestManager(t, pki, baseURL, 0, nil).
		RestoreSnapshot(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"}, "snap-1")

	require.NoError(t, err)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "restore=snap-1", gotQuery)
	require.Equal(t, "snap-1", got.ID)
}

// TestManagerDeleteSnapshotSendsDelete pins delete: DELETE with id=<id> and a 204
// back.
func TestManagerDeleteSnapshotSendsDelete(t *testing.T) {
	pki := newTestPKI(t)

	var gotMethod, gotQuery string
	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"snapshots": func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			gotMethod, gotQuery = r.Method, r.URL.RawQuery
			w.WriteHeader(http.StatusNoContent)
		},
	})

	err := snapshotTestManager(t, pki, baseURL, 0, nil).
		DeleteSnapshot(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"}, "snap-1")

	require.NoError(t, err)
	require.Equal(t, http.MethodDelete, gotMethod)
	require.Equal(t, "id=snap-1", gotQuery)
}

// TestManagerSnapshotRelaysTypedNodeError pins the relay rule: a node's 404 keeps
// its code, message and details.
func TestManagerSnapshotRelaysTypedNodeError(t *testing.T) {
	pki := newTestPKI(t)

	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"snapshots": func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
			writeTypedError(w, http.StatusNotFound, output.NewError(output.CodeNotFound,
				"the snapshot is not available on this node").
				WithDetails(map[string]any{"reason": "snapshot_not_found"}))
		},
	})

	_, err := snapshotTestManager(t, pki, baseURL, 0, nil).
		RestoreSnapshot(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"}, "snap-1")

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, output.CodeNotFound, typed.Code)
	require.Equal(t, "the snapshot is not available on this node", typed.Message)
	require.Equal(t, "snapshot_not_found", typed.Details["reason"])
}

// TestManagerSnapshotTransportFailureIsUnavailable proves a failed dial is
// unavailable plus details.nodeId, never unreachable and never a raw address.
func TestManagerSnapshotTransportFailureIsUnavailable(t *testing.T) {
	port := freePort(t)

	mgr, err := NewManager(ManagerConfig{
		Tasks:  &stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		Nodes:  &stubNodeReader{node: v1.Node{ID: "node-1", Address: "127.0.0.1"}},
		Port:   port,
		Client: &http.Client{Transport: &http.Transport{}},
	})
	require.NoError(t, err)

	_, err = mgr.ListTaskSnapshots(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"})

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, output.CodeUnavailable, typed.Code)
	require.Equal(t, "node-1", typed.Details["nodeId"])
	require.True(t, typed.Retryable)
	require.NotContains(t, typed.Message, "127.0.0.1")
}

// TestManagerSnapshotTimesOut pins the per-request deadline: a node that never
// answers cannot hold the caller open.
func TestManagerSnapshotTimesOut(t *testing.T) {
	pki := newTestPKI(t)

	release := make(chan struct{})
	defer close(release)

	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"snapshots": func(ctx context.Context, _ Task, _ http.ResponseWriter, _ *http.Request) {
			select {
			case <-release:
			case <-ctx.Done():
			}
		},
	})

	mgr := snapshotTestManager(t, pki, baseURL, 50*time.Millisecond, nil)

	started := time.Now()
	_, err := mgr.CreateSnapshot(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"})
	require.Less(t, time.Since(started), 5*time.Second, "CreateSnapshot must honour its own deadline")

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, output.CodeTimeout, typed.Code)
	require.True(t, typed.Retryable)
}

// TestManagerSnapshotTaskWithoutNodeIsUnavailable pins the resolution rule: a
// task the manager cannot place is not dialed anywhere.
func TestManagerSnapshotTaskWithoutNodeIsUnavailable(t *testing.T) {
	cases := map[string]struct {
		task v1.Task
		node v1.Node
	}{
		"no node id": {
			task: v1.Task{ID: "task-1"},
			node: v1.Node{ID: "node-1", Address: "192.168.1.10"},
		},
		"node without an address": {
			task: v1.Task{ID: "task-1", NodeID: "node-1"},
			node: v1.Node{ID: "node-1"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mgr := newTestManager(t,
				&stubTaskReader{task: tc.task},
				&stubNodeReader{node: tc.node},
				0,
				&http.Client{},
			)

			_, err := mgr.ListTaskSnapshots(context.Background(), tc.task)

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			require.Equal(t, output.CodeUnavailable, typed.Code)
		})
	}
}

// TestManagerSnapshotRejectsAMalformedID proves a snapshot ID is validated before
// it reaches a URL or a node directory: `..` must never become a path segment.
func TestManagerSnapshotRejectsAMalformedID(t *testing.T) {
	mgr := newTestManager(t,
		&stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		&stubNodeReader{node: v1.Node{ID: "node-1", Address: "192.168.1.10"}},
		0,
		&http.Client{},
	)

	for _, id := range []string{"", "../etc", "snap-1/../../x", "snap-1 dot"} {
		_, err := mgr.RestoreSnapshot(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"}, id)
		var typed *output.Error
		require.ErrorAs(t, err, &typed, "id %q must be rejected", id)
		require.Equal(t, output.CodeInvalid, typed.Code)
	}
}
