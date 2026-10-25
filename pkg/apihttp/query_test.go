package apihttp

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adminToken mints a token with every scope, so a case exercises the query rule
// rather than the authorization rule of its route.
func adminToken(t *testing.T, api *testAPI) func(*http.Request) {
	t.Helper()
	secret, _, err := api.store.Create("query-test-admin", RoleAdmin)
	require.NoError(t, err)
	return withToken(secret)
}

// TestUnknownQueryParameterIsInvalid pins the hardening rule: every public route
// declares the query names it implements, and anything else is a typed 400 that
// names the parameter. A typo must never degrade into a silently different
// request.
func TestUnknownQueryParameterIsInvalid(t *testing.T) {
	api := newTestAPI(t, &fakeClient{
		task:  v1.Task{ID: "task-1", ServiceID: "svc-1", NodeID: "node-1"},
		tasks: []v1.Task{{ID: "task-1", CreatedAt: "2026-01-01T00:00:00Z"}},
	})
	admin := adminToken(t, api)

	cases := []struct {
		name   string
		method string
		target string
	}{
		{"tasks", http.MethodGet, "/v1/tasks?bogus=1"},
		{"services", http.MethodGet, "/v1/services?bogus=1"},
		{"nodes", http.MethodGet, "/v1/nodes?bogus=1"},
		{"networks", http.MethodGet, "/v1/networks?bogus=1"},
		{"configs", http.MethodGet, "/v1/configs?bogus=1"},
		{"secrets", http.MethodGet, "/v1/secrets?bogus=1"},
		{"events", http.MethodGet, "/v1/events?bogus=1"},
		{"audit", http.MethodGet, "/v1/audit?bogus=1"},
		{"task", http.MethodGet, "/v1/tasks/task-1?bogus=1"},
		{"service tasks", http.MethodGet, "/v1/services/svc-1/tasks?bogus=1"},
		{"logs", http.MethodGet, "/v1/tasks/task-1/logs?bogus=1"},
		{"file read", http.MethodGet, "/v1/tasks/task-1/files?path=/etc/hostname&bogus=1"},
		{"file write", http.MethodPut, "/v1/tasks/task-1/files?path=/tmp/x&bogus=1"},
		{"exec", http.MethodPost, "/v1/tasks/task-1/exec?bogus=1"},
		{"service create", http.MethodPost, "/v1/services?bogus=1"},
		{"version", http.MethodGet, "/v1/version?bogus=1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, tc.method, tc.target, admin)

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			doc := decodeError(t, rec)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
			assert.Equal(t, "bogus", doc.Error.Details["parameter"])
			assert.False(t, doc.Error.Retryable)
		})
	}
}

// TestUnknownQueryParameterOnSnapshotsRoutes keeps the experimental surface
// under the same rule, in both the task-scoped and the flat form.
func TestUnknownQueryParameterOnSnapshotsRoutes(t *testing.T) {
	api := newSnapshotTestAPI(t, nil, &fakeSnapshotBroker{}, nil)

	for _, target := range []string{
		"/v1/tasks/task-1/snapshots?bogus=1",
		"/v1/snapshots?task=task-1&bogus=1",
	} {
		t.Run(target, func(t *testing.T) {
			rec := api.do(t, http.MethodGet, target, withSnapshotToken(api.admin))

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			doc := decodeErrorBody(t, rec)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
			assert.Equal(t, "bogus", doc.Error.Details["parameter"])
		})
	}
}

// TestDocumentedQueryParametersStillWork is the other half of the rule: the
// parameters the contract documents keep reaching their handler. The stream and
// write routes are covered by their own suites; this pins the read lists.
func TestDocumentedQueryParametersStillWork(t *testing.T) {
	api := newTestAPI(t, &fakeClient{
		tasks: []v1.Task{
			{ID: "task-1", CreatedAt: "2026-01-01T00:00:00Z"},
			{ID: "task-2", CreatedAt: "2026-01-02T00:00:00Z"},
		},
		services: []v1.Service{
			{ID: "svc-1", CreatedAt: "2026-01-01T00:00:00Z"},
			{ID: "svc-2", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	})
	admin := adminToken(t, api)

	// limit + label + health are all declared on the task list. label is
	// accepted because both SDKs document forwarding it; the manager does not
	// filter tasks by it, exactly as before.
	rec := api.do(t, http.MethodGet,
		"/v1/tasks?limit=1&health=false&label=team%3Dweb", admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var tasks v1.TaskList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tasks))
	require.NotNil(t, tasks.Next, "limit=1 of two tasks leaves a next cursor")

	// The cursor the route handed out pages the same route.
	rec = api.do(t, http.MethodGet,
		"/v1/tasks?limit=1&health=false&label=team%3Dweb&cursor="+url.QueryEscape(*tasks.Next), admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var next v1.TaskList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &next))
	require.Len(t, next.Items, 1)
	assert.Equal(t, "task-2", next.Items[0].ID)

	// Services accept the same pagination contract.
	rec = api.do(t, http.MethodGet, "/v1/services?limit=1&label=team", admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The audit log declares limit and cursor.
	rec = api.do(t, http.MethodGet, "/v1/audit?limit=10", admin)
	require.NotEqual(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
