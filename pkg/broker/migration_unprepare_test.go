package broker

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/output"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// unprepareFixture is one node that holds a directive for each service named, and the
// migration root those records were written for.
func unprepareFixture(t *testing.T, serviceIDs ...string) (MigrationHandlerConfig, *scruntime.PendingRestores, string) {
	t.Helper()

	cfg, store, root := prepareFixture(t)
	stagePrepareArtifact(t, root, prepareTestArtifact)
	for _, serviceID := range serviceIDs {
		require.NoError(t, store.Put(scruntime.PendingRestore{
			ServiceID:  serviceID,
			ArtifactID: prepareTestArtifact,
			Dir:        filepath.Join(root, prepareTestArtifact),
			CreatedAt:  time.Now().UTC(),
		}))
	}
	return cfg, store, root
}

// unprepareQuery is the well-formed query for the fixture's directive: the service
// and the artifact the caller says it owns.
func unprepareQuery(serviceID string) string {
	return "?serviceId=" + url.QueryEscape(serviceID) + "&artifactId=" + prepareTestArtifact
}

// serveUnprepare drives the unprepare handler directly, with the empty task an
// artifact-scoped operation is given.
func serveUnprepare(
	t *testing.T, cfg MigrationHandlerConfig, method, query string, body []byte,
) *httptest.ResponseRecorder {
	t.Helper()

	handler := NewMigrationUnprepareHandler(cfg)
	req := httptest.NewRequest(method, "/v1/tasks/pending-restore/migration/unprepare"+query, bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	handler(context.Background(), Task{}, rec, req)
	return rec
}

// TestMigrationUnprepareRemovesTheDirective pins the operation an abandoned migration
// needs: the manager says the migration is off, and the directive that would make the
// service's next task restore a discarded artifact is gone. Only that service's
// directive goes, and a second call - the retried rollback - is a success.
func TestMigrationUnprepareRemovesTheDirective(t *testing.T) {
	cfg, store, _ := unprepareFixture(t, "svc-1", "svc-2")

	rec := serveUnprepare(t, cfg, http.MethodDelete, unprepareQuery("svc-1"), nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String(), "unprepare answers 204 with no body")

	all, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, all, 1, "another service's directive is not this service's to remove")
	assert.Equal(t, "svc-2", all[0].ServiceID)

	// Idempotent: a rollback may run twice, and the second run has nothing to remove.
	rec = serveUnprepare(t, cfg, http.MethodDelete, unprepareQuery("svc-1"), nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	// A service nothing was ever prepared for is the same answer again.
	rec = serveUnprepare(t, cfg, http.MethodDelete, unprepareQuery("never-prepared"), nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

// TestMigrationUnprepareRefusesAnotherArtifactsDirective is ruling I1(b): the node keeps
// one directive per service, so the manager that loses a leadership race must not
// delete the winner's. A removal that names another artifact is a 409, and the
// directive stays for the migration that prepared it.
func TestMigrationUnprepareRefusesAnotherArtifactsDirective(t *testing.T) {
	cfg, store, _ := unprepareFixture(t, "svc-1")

	query := "?serviceId=svc-1&artifactId=mig-fedcba9876543210"
	rec := serveUnprepare(t, cfg, http.MethodDelete, query, nil)

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	doc := decodeMigrationError(t, rec)
	assert.Equal(t, string(output.CodeConflict), doc.Error.Code)
	assert.Equal(t, "directive_replaced", doc.Error.Details["reason"])

	all, _, err := store.List()
	require.NoError(t, err)
	require.Len(t, all, 1, "the winner's directive is untouched")
	assert.Equal(t, prepareTestArtifact, all[0].ArtifactID)
}

// TestMigrationUnprepareRefusesBadRequests pins the query grammar: serviceId once, and
// a shape that can name a record file. A refusal records nothing and removes nothing.
func TestMigrationUnprepareRefusesBadRequests(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		body    []byte
		reason  string
		param   string
		wantSub string
	}{
		{
			name:   "no serviceId",
			query:  "",
			reason: "invalid_service_id", param: "serviceId", wantSub: "serviceId",
		},
		{
			name:   "an empty serviceId",
			query:  "?serviceId=",
			reason: "invalid_service_id", param: "serviceId", wantSub: "serviceId",
		},
		{
			name:   "a service ID with a dot",
			query:  "?serviceId=svc.1",
			reason: "invalid_service_id", param: "serviceId", wantSub: "serviceId",
		},
		{
			name:   "a service ID with a path separator",
			query:  "?serviceId=" + url.QueryEscape("svc/1"),
			reason: "invalid_service_id", param: "serviceId", wantSub: "serviceId",
		},
		{
			name:   "a service ID with whitespace",
			query:  "?serviceId=" + url.QueryEscape("svc 1"),
			reason: "invalid_service_id", param: "serviceId", wantSub: "serviceId",
		},
		{
			name:   "a service ID past the file name bound",
			query:  "?serviceId=" + strings.Repeat("s", scruntime.MaxIDLength+1),
			reason: "invalid_service_id", param: "serviceId", wantSub: "serviceId",
		},
		{
			name:    "a parameter this operation does not take",
			query:   "?serviceId=svc-1&artifactId=" + prepareTestArtifact + "&force=true",
			param:   "force",
			wantSub: "force",
		},
		{
			name:    "serviceId set twice",
			query:   "?serviceId=svc-1&serviceId=svc-2&artifactId=" + prepareTestArtifact,
			param:   "serviceId",
			wantSub: "serviceId",
		},
		{
			name:   "no artifactId",
			query:  "?serviceId=svc-1",
			reason: "invalid_artifact_id", param: "artifactId", wantSub: "artifactId",
		},
		{
			name:   "an artifact ID that is not one this build generates",
			query:  "?serviceId=svc-1&artifactId=" + url.QueryEscape("../etc/passwd"),
			reason: "invalid_artifact_id", param: "artifactId", wantSub: "artifactId",
		},
		{
			name:    "artifactId set twice",
			query:   "?serviceId=svc-1&artifactId=" + prepareTestArtifact + "&artifactId=mig-fedcba9876543210",
			param:   "artifactId",
			wantSub: "artifactId",
		},
		{
			name:   "a body on an operation that takes none",
			query:  "?serviceId=svc-1&artifactId=" + prepareTestArtifact,
			body:   []byte(`{"serviceId":"svc-1"}`),
			reason: "unexpected_body", wantSub: "takes no body",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store, _ := unprepareFixture(t, "svc-1")

			rec := serveUnprepare(t, cfg, http.MethodDelete, tc.query, tc.body)

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			doc := decodeMigrationError(t, rec)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
			if tc.reason != "" {
				assert.Equal(t, tc.reason, doc.Error.Details["reason"])
			}
			if tc.param != "" {
				assert.Equal(t, tc.param, doc.Error.Details["parameter"])
			}
			assert.Contains(t, doc.Error.Message, tc.wantSub)

			all, _, err := store.List()
			require.NoError(t, err)
			assert.Len(t, all, 1, "a refused unprepare removes nothing")
		})
	}
}

// TestMigrationUnprepareRefusesAnotherMethod pins the method set: unprepare is a
// DELETE, and the operation answers 405 with its own Allow rather than acting on a
// request it was not promised.
func TestMigrationUnprepareRefusesAnotherMethod(t *testing.T) {
	cfg, store, _ := unprepareFixture(t, "svc-1")

	rec := serveUnprepare(t, cfg, http.MethodPost, unprepareQuery("svc-1"), nil)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	assert.Equal(t, http.MethodDelete, rec.Header().Get("Allow"))

	all, _, err := store.List()
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

// TestMigrationUnprepareNeedsTheDirectiveStore pins the one capability it needs: a node
// whose store never opened must not answer as though the directive were gone.
func TestMigrationUnprepareNeedsTheDirectiveStore(t *testing.T) {
	cfg, _, _ := unprepareFixture(t)
	cfg.Pending = nil

	rec := serveUnprepare(t, cfg, http.MethodDelete, unprepareQuery("svc-1"), nil)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	doc := decodeMigrationError(t, rec)
	assert.Equal(t, string(output.CodeUnavailable), doc.Error.Code)
	assert.Equal(t, "pending_restore_unavailable", doc.Error.Details["reason"])
}

// TestServerRoutesUnprepareWithoutATask pins the routing ruling the other directive
// operation relies on: the task a migration abandons may already be gone, so the task
// segment of the route is a placeholder nothing resolves.
func TestServerRoutesUnprepareWithoutATask(t *testing.T) {
	cfg, store, _ := unprepareFixture(t, "svc-1")
	src := &fakeTaskSource{found: false}
	srv := newRouteServer(t, src)
	require.NoError(t, srv.Handle(MigrationUnprepareOperation, NewMigrationUnprepareHandler(cfg)))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/v1/tasks/task-gone/migration/unprepare"+unprepareQuery("svc-1"), nil))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Empty(t, src.gotID, "an artifact-scoped operation must not resolve the task in its path")

	all, _, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, all)
}

// TestManagerUnprepareAsksTheTargetNode pins the manager's call: a DELETE on the
// node-scoped directive route, addressed to the node that holds the directive, with
// the service in the query and nothing in the body.
func TestManagerUnprepareAsksTheTargetNode(t *testing.T) {
	pki := newTestPKI(t)

	type observed struct {
		method string
		path   string
		query  string
		body   string
	}
	var got observed

	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationUnprepareOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			got = observed{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(body)}
			w.WriteHeader(http.StatusNoContent)
		},
	})

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	require.NoError(t, mgr.Unprepare(context.Background(), "node-2", "svc-1", prepareTestArtifact))

	assert.Equal(t, http.MethodDelete, got.method)
	assert.Equal(t, "/v1/tasks/"+migrationDirectiveRouteTask+"/migration/unprepare", got.path)
	assert.Equal(t, "artifactId="+prepareTestArtifact+"&serviceId=svc-1", got.query)
	assert.Empty(t, got.body, "unprepare names the service in the query and sends no document")
}

// TestManagerUnprepareRefusesWhatItCannotSend pins the local refusal: an ID that cannot
// name a record file, or an artifact ID that is not one this build generates, never
// becomes a request.
func TestManagerUnprepareRefusesWhatItCannotSend(t *testing.T) {
	pki := newTestPKI(t)

	var called atomic.Bool
	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationUnprepareOperation: func(_ context.Context, _ Task, _ http.ResponseWriter, _ *http.Request) {
			called.Store(true)
		},
	})
	mgr := newMigrationTestManager(t, pki, baseURL, 0)

	for _, serviceID := range []string{"", "svc.1", "svc/1", "svc 1", strings.Repeat("s", scruntime.MaxIDLength+1)} {
		t.Run("service "+serviceID, func(t *testing.T) {
			err := mgr.Unprepare(context.Background(), "node-2", serviceID, prepareTestArtifact)

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, output.CodeInvalid, typed.Code, typed.Message)
			assert.False(t, called.Load(), "a service this build cannot name never becomes a request")
		})
	}
	for _, artifactID := range []string{"", "mig-123", "../mig-0123456789abcdef", "mig-0123456789ABCDEF"} {
		t.Run("artifact "+artifactID, func(t *testing.T) {
			err := mgr.Unprepare(context.Background(), "node-2", "svc-1", artifactID)

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, output.CodeInvalid, typed.Code, typed.Message)
			assert.Equal(t, "invalid_artifact_id", typed.Details["reason"], typed.Message)
			assert.False(t, called.Load(), "an artifact this build cannot name never becomes a request")
		})
	}
}

// TestManagerUnprepareMapsTheNodesAnswer pins the error contract of a cleanup that must
// not lie: a node's typed document crosses the relay with its code, and an answer that
// is not the 204 the operation promises is this side's failure to notice a broken node.
func TestManagerUnprepareMapsTheNodesAnswer(t *testing.T) {
	pki := newTestPKI(t)

	cases := []struct {
		name        string
		status      int
		code        output.Code
		message     string
		document    bool
		wantCode    output.Code
		wantMessage string
	}{
		{
			name:     "the directive is gone",
			status:   http.StatusNoContent,
			document: false,
		},
		{
			name:        "a typed conflict",
			status:      http.StatusConflict,
			code:        output.CodeConflict,
			message:     "the directive is being consumed",
			document:    true,
			wantCode:    output.CodeConflict,
			wantMessage: "the directive is being consumed",
		},
		{
			name:        "a typed unavailable",
			status:      http.StatusServiceUnavailable,
			code:        output.CodeUnavailable,
			message:     "the pending restore could not be removed on this node",
			document:    true,
			wantCode:    output.CodeUnavailable,
			wantMessage: "the pending restore could not be removed on this node",
		},
		{
			name:        "a body that is not a typed error",
			status:      http.StatusInternalServerError,
			document:    false,
			wantCode:    codeUnavailable,
			wantMessage: "unavailable",
		},
		{
			name:        "a 2xx that did not answer the operation",
			status:      http.StatusOK,
			document:    false,
			wantCode:    output.CodeInternal,
			wantMessage: "returned status 200",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
				MigrationUnprepareOperation: func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
					if tc.document {
						writeNodeError(t, w, tc.status, tc.code, tc.message)
						return
					}
					w.WriteHeader(tc.status)
					if tc.status != http.StatusNoContent {
						_, _ = io.WriteString(w, "boom")
					}
				},
			})

			mgr := newMigrationTestManager(t, pki, baseURL, 0)
			err := mgr.Unprepare(context.Background(), "node-2", "svc-1", prepareTestArtifact)
			if tc.wantCode == "" {
				require.NoError(t, err)
				return
			}

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, tc.wantCode, typed.Code, typed.Message)
			assert.Contains(t, typed.Message, tc.wantMessage)
			if tc.document {
				assert.Equal(t, tc.message, typed.Message, "a typed error crosses the relay verbatim")
			}
		})
	}
}
