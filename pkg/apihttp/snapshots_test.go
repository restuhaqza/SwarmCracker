package apihttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// fakeSnapshotBroker is the manager-side snapshot transport under test. It
// records what the public handler asked for and returns scripted results.
type fakeSnapshotBroker struct {
	list    []v1.Snapshot
	create  v1.Snapshot
	restore v1.Snapshot

	listErr    error
	createErr  error
	restoreErr error
	deleteErr  error

	// createBlock keeps CreateSnapshot blocked until the request context ends;
	// the test uses it to exercise a real deadline.
	createBlock bool

	calls       int
	gotTask     v1.Task
	gotRequest  v1.SnapshotCreateRequest
	gotID       string
	gotDeadline bool
}

func (f *fakeSnapshotBroker) CreateSnapshot(ctx context.Context, task v1.Task, req v1.SnapshotCreateRequest) (v1.Snapshot, error) {
	f.record(ctx, task)
	f.gotRequest = req
	if f.createBlock {
		<-ctx.Done()
		return v1.Snapshot{}, ctx.Err()
	}
	return f.create, f.createErr
}

func (f *fakeSnapshotBroker) ListTaskSnapshots(ctx context.Context, task v1.Task) ([]v1.Snapshot, error) {
	f.record(ctx, task)
	return f.list, f.listErr
}

func (f *fakeSnapshotBroker) RestoreSnapshot(ctx context.Context, task v1.Task, snapshotID string) (v1.Snapshot, error) {
	f.record(ctx, task)
	f.gotID = snapshotID
	return f.restore, f.restoreErr
}

func (f *fakeSnapshotBroker) DeleteSnapshot(ctx context.Context, task v1.Task, snapshotID string) error {
	f.record(ctx, task)
	f.gotID = snapshotID
	return f.deleteErr
}

func (f *fakeSnapshotBroker) record(ctx context.Context, task v1.Task) {
	f.calls++
	f.gotTask = task
	_, f.gotDeadline = ctx.Deadline()
}

// newSnapshotTestAPI builds a server with the snapshot capability wired. The
// broker is optional, and mutate may narrow the configuration (a disabled
// capability, a short deadline).
func newSnapshotTestAPI(
	t *testing.T, client *execFakeClient, broker *fakeSnapshotBroker, mutate func(*Config),
) *execTestAPI {
	t.Helper()
	if client == nil {
		client = &execFakeClient{fakeClient: &fakeClient{}}
	}
	if client.task.ID == "" {
		client.task = v1.Task{ID: "task-1", ServiceID: "svc-1", Name: "web.1"}
	}

	api := newExecTestAPIWithConfig(t, client, &fakeBroker{}, func(cfg *Config) {
		if broker != nil {
			cfg.Snapshots = broker
			cfg.SnapshotsEnabled = true
		}
		if mutate != nil {
			mutate(cfg)
		}
	})
	return api
}

func withSnapshotBody(body string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Content-Type", MediaType)
		r.Body = io.NopCloser(strings.NewReader(body))
	}
}

func withSnapshotToken(token string) func(*http.Request) {
	return withToken(token)
}

// TestSnapshotsDisabledIs501 pins the capability gate: a disabled or unconfigured
// capability answers 501 before the task is resolved or anything is audited.
func TestSnapshotsDisabledIs501(t *testing.T) {
	cases := []struct {
		name   string
		broker *fakeSnapshotBroker
		mutate func(*Config)
	}{
		{name: "no broker", broker: nil},
		{name: "disabled", broker: &fakeSnapshotBroker{}, mutate: func(cfg *Config) {
			cfg.SnapshotsEnabled = false
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newSnapshotTestAPI(t, nil, tc.broker, tc.mutate)

			for _, target := range []struct {
				method string
				path   string
			}{
				{http.MethodGet, "/v1/tasks/web-1/snapshots"},
				{http.MethodPost, "/v1/tasks/web-1/snapshots"},
				{http.MethodPost, "/v1/tasks/web-1/snapshots/snap-1/restore"},
				{http.MethodDelete, "/v1/tasks/web-1/snapshots/snap-1"},
				{http.MethodGet, "/v1/snapshots?task=web-1"},
				{http.MethodPost, "/v1/snapshots/snap-1/restore?task=web-1"},
				{http.MethodDelete, "/v1/snapshots/snap-1?task=web-1"},
			} {
				rec := api.do(t, target.method, target.path, withSnapshotToken(api.admin))
				require.Equal(t, http.StatusNotImplemented, rec.Code, target.method+" "+target.path)

				doc := decodeErrorBody(t, rec)
				require.Equal(t, string(output.CodeNotImplemented), doc.Error.Code)
				require.Equal(t, "snapshots", doc.Error.Details["capability"])
			}

			require.Empty(t, api.client.gotTaskRef, "a 501 must not resolve a task")
			records, _, err := api.audit.List(10, "")
			require.NoError(t, err)
			require.Empty(t, records, "a 501 must not be audited")
		})
	}
}

// TestSnapshotsRequireScope pins the tasks:snapshots scope.
func TestSnapshotsRequireScope(t *testing.T) {
	api := newSnapshotTestAPI(t, nil, &fakeSnapshotBroker{}, nil)

	rec := api.do(t, http.MethodGet, "/v1/tasks/web-1/snapshots", withSnapshotToken(api.reader))

	require.Equal(t, http.StatusForbidden, rec.Code)
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeForbidden), doc.Error.Code)
	assert.Equal(t, string(ScopeTasksSnapshots), doc.Error.Details["requiredScope"])
}

// TestSnapshotsTaskSelectorGates pins selector enforcement: the same rules exec
// uses, including a task-ID selector that matches without service labels.
func TestSnapshotsTaskSelectorGates(t *testing.T) {
	fc := &execFakeClient{
		fakeClient: &fakeClient{},
		serviceErr: output.NewError(output.CodeNotFound, "no such service"),
	}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1", Name: "web.1"}
	api := newSnapshotTestAPI(t, fc, &fakeSnapshotBroker{list: []v1.Snapshot{{ID: "snap-1"}}}, nil)

	rec := api.do(t, http.MethodGet, "/v1/tasks/web-1/snapshots", withSnapshotToken(api.byTask))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = api.do(t, http.MethodGet, "/v1/tasks/web-1/snapshots", withSnapshotToken(api.byLabel))
	require.Equal(t, http.StatusForbidden, rec.Code,
		"a label selector with no service labels fails closed")

	fc.task = v1.Task{ID: "task-2", ServiceID: "svc-1", Name: "web.2"}
	rec = api.do(t, http.MethodGet, "/v1/tasks/web-2/snapshots", withSnapshotToken(api.byTask))
	require.Equal(t, http.StatusForbidden, rec.Code)
}

// TestSnapshotsCreateListRestoreDelete pins the task-scoped happy paths, their
// statuses and the document kinds. It also proves /v1/tasks/{id} does not shadow
// the nested routes.
func TestSnapshotsCreateListRestoreDelete(t *testing.T) {
	broker := &fakeSnapshotBroker{
		create:  v1.Snapshot{ID: "snap-created", TaskID: "task-1"},
		restore: v1.Snapshot{ID: "snap-1", TaskID: "task-1"},
		list: []v1.Snapshot{
			{ID: "snap-1", TaskID: "task-1", CreatedAt: "2026-10-24T10:00:00Z"},
			{ID: "snap-2", TaskID: "task-1", CreatedAt: "2026-10-24T11:00:00Z"},
			{ID: "snap-3", TaskID: "task-1", CreatedAt: "2026-10-24T12:00:00Z"},
		},
	}
	api := newSnapshotTestAPI(t, nil, broker, nil)

	rec := api.do(t, http.MethodPost, "/v1/tasks/web-1/snapshots",
		withSnapshotToken(api.admin), withSnapshotBody(`{"vcpus": 2, "memoryMB": 1024}`))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.False(t, broker.gotDeadline, "the manager owns the 30 s deadline; the handler adds none")
	assert.Equal(t, v1.SnapshotCreateRequest{VCPUs: 2, MemoryMB: 1024}, broker.gotRequest,
		"the parsed body must reach the transport, not be validated and dropped")

	var created v1.SnapshotDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.Equal(t, v1.KindSnapshot, created.Kind)
	assert.Equal(t, "snap-created", created.ID)

	rec = api.do(t, http.MethodGet, "/v1/tasks/web-1/snapshots", withSnapshotToken(api.admin))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var list v1.SnapshotList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Equal(t, v1.KindSnapshotList, list.Kind, "the nested route must not be served as a task")
	require.Len(t, list.Items, 3)
	assert.Equal(t, []string{"snap-1", "snap-2", "snap-3"},
		[]string{list.Items[0].ID, list.Items[1].ID, list.Items[2].ID})

	rec = api.do(t, http.MethodPost, "/v1/tasks/web-1/snapshots/snap-1/restore",
		withSnapshotToken(api.admin))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "snap-1", broker.gotID)

	var restored v1.SnapshotDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &restored))
	assert.Equal(t, v1.KindSnapshot, restored.Kind)

	rec = api.do(t, http.MethodDelete, "/v1/tasks/web-1/snapshots/snap-1", withSnapshotToken(api.admin))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String())
	assert.Equal(t, "snap-1", broker.gotID)
}

// TestSnapshotsPagination pins the shared pagination behavior: limit, cursor and
// order preservation.
func TestSnapshotsPagination(t *testing.T) {
	broker := &fakeSnapshotBroker{list: []v1.Snapshot{
		{ID: "snap-1", CreatedAt: "2026-10-24T10:00:00Z"},
		{ID: "snap-2", CreatedAt: "2026-10-24T11:00:00Z"},
		{ID: "snap-3", CreatedAt: "2026-10-24T12:00:00Z"},
	}}
	api := newSnapshotTestAPI(t, nil, broker, nil)

	rec := api.do(t, http.MethodGet, "/v1/tasks/web-1/snapshots?limit=2", withSnapshotToken(api.admin))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var first v1.SnapshotList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &first))
	require.Len(t, first.Items, 2)
	require.NotNil(t, first.Next)

	rec = api.do(t, http.MethodGet,
		"/v1/tasks/web-1/snapshots?limit=2&cursor="+url.QueryEscape(*first.Next),
		withSnapshotToken(api.admin))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var second v1.SnapshotList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &second))
	require.Len(t, second.Items, 1)
	assert.Equal(t, "snap-3", second.Items[0].ID)
	assert.Nil(t, second.Next)
}

// TestSnapshotsFlatFormsRequireTask pins the flat route's task parameter: a
// snapshot lives on one node, so the caller must name the task.
func TestSnapshotsFlatFormsRequireTask(t *testing.T) {
	broker := &fakeSnapshotBroker{}
	api := newSnapshotTestAPI(t, nil, broker, nil)

	for _, target := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/snapshots"},
		{http.MethodPost, "/v1/snapshots/snap-1/restore"},
		{http.MethodDelete, "/v1/snapshots/snap-1"},
	} {
		rec := api.do(t, target.method, target.path, withSnapshotToken(api.admin))
		require.Equal(t, http.StatusBadRequest, rec.Code, target.method+" "+target.path)

		doc := decodeErrorBody(t, rec)
		assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
		assert.Equal(t, "task", doc.Error.Details["parameter"])
		assert.Contains(t, strings.ToLower(doc.Error.Message), "task")
	}

	require.Zero(t, broker.calls, "a missing task parameter must not reach a node")
	require.Empty(t, api.client.gotTaskRef)
}

// TestSnapshotsFlatFormsWorkWithTask pins the flat forms once ?task= is present.
func TestSnapshotsFlatFormsWorkWithTask(t *testing.T) {
	broker := &fakeSnapshotBroker{
		list:    []v1.Snapshot{{ID: "snap-1"}},
		restore: v1.Snapshot{ID: "snap-1"},
	}
	api := newSnapshotTestAPI(t, nil, broker, nil)

	rec := api.do(t, http.MethodGet, "/v1/snapshots?task=web-1", withSnapshotToken(api.admin))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "task-1", broker.gotTask.ID)

	rec = api.do(t, http.MethodPost, "/v1/snapshots/snap-1/restore?task=web-1",
		withSnapshotToken(api.admin))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "snap-1", broker.gotID)

	rec = api.do(t, http.MethodDelete, "/v1/snapshots/snap-1?task=web-1", withSnapshotToken(api.admin))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, "snap-1", broker.gotID)
}

// TestSnapshotsCreateBodyRules pins the body contract: strict fields, an empty
// body is valid and a 64 KiB cap.
func TestSnapshotsCreateBodyRules(t *testing.T) {
	api := newSnapshotTestAPI(t, nil, &fakeSnapshotBroker{create: v1.Snapshot{ID: "snap-1"}}, nil)

	rec := api.do(t, http.MethodPost, "/v1/tasks/web-1/snapshots", withSnapshotToken(api.admin))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	rec = api.do(t, http.MethodPost, "/v1/tasks/web-1/snapshots",
		withSnapshotToken(api.admin), withSnapshotBody(`{"diskMB": 10}`))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, string(output.CodeInvalid), decodeErrorBody(t, rec).Error.Code)

	// A body requires the v1 media type.
	rec = api.do(t, http.MethodPost, "/v1/tasks/web-1/snapshots",
		withSnapshotToken(api.admin),
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"vcpus": 2}`)) })
	require.Equal(t, http.StatusUnsupportedMediaType, rec.Code, rec.Body.String())

	rec = api.do(t, http.MethodPost, "/v1/tasks/web-1/snapshots",
		withSnapshotToken(api.admin), withSnapshotBody(`{"vcpus": -1}`))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "vcpus", decodeErrorBody(t, rec).Error.Details["parameter"])

	rec = api.do(t, http.MethodPost, "/v1/tasks/web-1/snapshots",
		withSnapshotToken(api.admin), withSnapshotBody(`{"memoryMB": -1}`))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "memoryMB", decodeErrorBody(t, rec).Error.Details["parameter"])

	for _, rootfs := range []string{
		"relative/rootfs",
		"/var/lib/firecracker/rootfs/../escape",
		"/",
	} {
		rec = api.do(t, http.MethodPost, "/v1/tasks/web-1/snapshots",
			withSnapshotToken(api.admin), withSnapshotBody(`{"rootfsPath": "`+rootfs+`"}`))
		require.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", rootfs, rec.Body.String())
		assert.Equal(t, "rootfsPath", decodeErrorBody(t, rec).Error.Details["parameter"])
	}

	oversized := `{"rootfsPath": "` + strings.Repeat("a", 64<<10) + `"}`
	rec = api.do(t, http.MethodPost, "/v1/tasks/web-1/snapshots",
		withSnapshotToken(api.admin), withSnapshotBody(oversized))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
}

// TestSnapshotsCreateBodyAcceptsUncleanAbsoluteRootfs pins the schema's rule: only
// a relative path or a .. segment is invalid. A redundant trailing slash, a
// doubled separator or a . segment all name the same absolute directory and are
// accepted, so a caller that builds the path by concatenation is not punished.
func TestSnapshotsCreateBodyAcceptsUncleanAbsoluteRootfs(t *testing.T) {
	for _, rootfs := range []string{
		"/var/lib/firecracker/rootfs/task-1/",
		"/var/lib//firecracker/rootfs/task-1",
		"/var/lib/firecracker/./rootfs/task-1",
	} {
		t.Run(rootfs, func(t *testing.T) {
			broker := &fakeSnapshotBroker{create: v1.Snapshot{ID: "snap-1"}}
			api := newSnapshotTestAPI(t, nil, broker, nil)

			rec := api.do(t, http.MethodPost, "/v1/tasks/web-1/snapshots",
				withSnapshotToken(api.admin), withSnapshotBody(`{"rootfsPath": "`+rootfs+`"}`))

			require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
			assert.Equal(t, rootfs, broker.gotRequest.RootfsPath)
		})
	}
}

// TestSnapshotsCreateBodyReachesTheTransport pins I1: every validated field,
// including rootfsPath, is carried to the manager and therefore to the node's
// snapshot.CreateOptions.
func TestSnapshotsCreateBodyReachesTheTransport(t *testing.T) {
	broker := &fakeSnapshotBroker{create: v1.Snapshot{ID: "snap-1"}}
	api := newSnapshotTestAPI(t, nil, broker, nil)

	const rootfs = "/var/lib/firecracker/rootfs/task-1"
	rec := api.do(t, http.MethodPost, "/v1/tasks/web-1/snapshots",
		withSnapshotToken(api.admin),
		withSnapshotBody(`{"vcpus": 4, "memoryMB": 2048, "rootfsPath": "`+rootfs+`"}`))

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, v1.SnapshotCreateRequest{VCPUs: 4, MemoryMB: 2048, RootfsPath: rootfs},
		broker.gotRequest)
}

// TestSnapshotsInvalidIDIs400 pins id validation: an id is one safe path
// segment, so something path-like never reaches a node.
func TestSnapshotsInvalidIDIs400(t *testing.T) {
	broker := &fakeSnapshotBroker{}
	api := newSnapshotTestAPI(t, nil, broker, nil)

	for _, id := range []string{"notasnap", "snap-1%2F..%2Fetc"} {
		rec := api.do(t, http.MethodDelete, "/v1/tasks/web-1/snapshots/"+id,
			withSnapshotToken(api.admin))
		require.Equal(t, http.StatusBadRequest, rec.Code, id)
		assert.Equal(t, "id", decodeErrorBody(t, rec).Error.Details["parameter"])
	}
	require.Zero(t, broker.calls)
}

// TestSnapshotsAudit pins task.snapshot on both outcomes, with the task as the
// resource and no path, checksum or size in the record.
func TestSnapshotsAudit(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1", Name: "web.1"}
	broker := &fakeSnapshotBroker{create: v1.Snapshot{
		ID:         "snap-1",
		RootfsPath: "/var/lib/firecracker/rootfs/task-1",
		Checksum:   "deadbeef",
		SizeBytes:  4096,
	}}
	api := newSnapshotTestAPI(t, fc, broker, nil)

	rec := api.do(t, http.MethodPost, "/v1/tasks/web-1/snapshots",
		withSnapshotToken(api.admin), withSnapshotBody(`{}`))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.snapshot", records[0].Action)
	assert.Equal(t, "ok", records[0].Result)
	assert.Equal(t, v1.KindTask, records[0].Resource.Kind)
	assert.Equal(t, "task-1", records[0].Resource.ID)
	assert.Equal(t, "web.1", records[0].Resource.Name)

	broker.listErr = output.NewError(output.CodeUnavailable, "node broker is unavailable")
	rec = api.do(t, http.MethodGet, "/v1/tasks/web-1/snapshots", withSnapshotToken(api.admin))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	records, _, err = api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "task.snapshot", records[1].Action)
	assert.Equal(t, "error", records[1].Result)

	raw, err := os.ReadFile(api.audit.path)
	require.NoError(t, err)
	for _, forbidden := range []string{"rootfs", "deadbeef", "4096"} {
		assert.NotContains(t, string(raw), forbidden,
			"the audit log must not carry a path, checksum or size")
	}
}

// TestSnapshotsTimeoutIs504 exercises a real deadline: the blocking fake returns
// when the request context expires, and the handler maps context.DeadlineExceeded
// to 504 timeout. The manager owns the 30 s default; this test supplies the
// request's own deadline.
func TestSnapshotsTimeoutIs504(t *testing.T) {
	broker := &fakeSnapshotBroker{createBlock: true}
	api := newSnapshotTestAPI(t, nil, broker, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/web-1/snapshots", nil)
	req = req.WithContext(ctx)
	req.Header.Set("Accept", MediaType)
	req.Header.Set("Authorization", "Bearer "+api.admin)
	rec := httptest.NewRecorder()

	started := time.Now()
	api.handler.ServeHTTP(rec, req)
	require.Less(t, time.Since(started), time.Second, "the handler must return when the deadline expires")

	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())
	assert.Equal(t, string(output.CodeTimeout), decodeErrorBody(t, rec).Error.Code)
}

// TestSnapshotsMethodNotAllowed pins the typed 405 on a snapshot path.
func TestSnapshotsMethodNotAllowed(t *testing.T) {
	api := newSnapshotTestAPI(t, nil, &fakeSnapshotBroker{}, nil)

	rec := api.do(t, http.MethodPut, "/v1/tasks/web-1/snapshots", withSnapshotToken(api.admin))

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, string(output.CodeInvalid), decodeErrorBody(t, rec).Error.Code)
}

// TestSnapshotsVersionCapability pins the advertisement rule: snapshots is
// advertised only when the capability and its transport are both wired.
func TestSnapshotsVersionCapability(t *testing.T) {
	enabled := newSnapshotTestAPI(t, nil, &fakeSnapshotBroker{}, nil)
	rec := enabled.do(t, http.MethodGet, "/v1/version", withSnapshotToken(enabled.admin))

	var info v1.VersionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Contains(t, info.Capabilities, "snapshots")

	disabled := newSnapshotTestAPI(t, nil, &fakeSnapshotBroker{}, func(cfg *Config) {
		cfg.SnapshotsEnabled = false
	})
	rec = disabled.do(t, http.MethodGet, "/v1/version", withSnapshotToken(disabled.admin))
	info = v1.VersionInfo{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.NotContains(t, info.Capabilities, "snapshots")

	// A supplied list cannot claim a disabled capability: the version document
	// must never promise a route that answers 501.
	claimed := newSnapshotTestAPI(t, nil, nil, func(cfg *Config) {
		cfg.Capabilities = []string{"read", "snapshots"}
	})
	rec = claimed.do(t, http.MethodGet, "/v1/version", withSnapshotToken(claimed.admin))
	info = v1.VersionInfo{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.NotContains(t, info.Capabilities, "snapshots")
}

// TestSnapshotsMissingTaskIs400BeforeResolution pins that a flat request with no
// task is answered without a lookup.
func TestSnapshotsMissingTaskIs400BeforeResolution(t *testing.T) {
	api := newSnapshotTestAPI(t, nil, &fakeSnapshotBroker{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/snapshots?task=", nil)
	req.Header.Set("Accept", MediaType)
	req.Header.Set("Authorization", "Bearer "+api.admin)
	rec := httptest.NewRecorder()
	api.handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Empty(t, api.client.gotTaskRef)
}
