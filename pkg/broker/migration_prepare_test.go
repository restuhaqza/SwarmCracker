package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/output"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// prepareTestArtifact is the artifact ID the prepare fixtures name.
const prepareTestArtifact = "mig-0123456789abcdef"

// prepareFixture is one node that can accept a directive: its migration root, and
// the directive store the daemon would have opened beside it.
func prepareFixture(t *testing.T) (MigrationHandlerConfig, *scruntime.PendingRestores, string) {
	t.Helper()

	root := t.TempDir()
	store, err := scruntime.NewPendingRestores(filepath.Join(t.TempDir(), scruntime.PendingRestoreDirName))
	require.NoError(t, err)
	return MigrationHandlerConfig{ArtifactDir: root, Pending: store}, store, root
}

// prepareBody is the request the manager sends: the service whose next task
// restores, the artifact, and the directory the manager staged it in on this node.
func prepareBody(serviceID, artifactID, dir string) []byte {
	body, err := json.Marshal(map[string]string{
		"serviceId":  serviceID,
		"artifactId": artifactID,
		"dir":        dir,
	})
	if err != nil {
		panic(err)
	}
	return body
}

// servePrepare drives the prepare handler directly, with the empty task an
// artifact-scoped operation is given.
func servePrepare(t *testing.T, cfg MigrationHandlerConfig, method string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	handler := NewMigrationPrepareHandler(cfg)
	req := httptest.NewRequest(method, "/v1/tasks/pending-restore/migration/prepare", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	handler(context.Background(), Task{}, rec, req)
	return rec
}

// TestMigrationPrepareRecordsTheDirective pins the contract: a valid prepare is a
// 204 with no body, and the node now holds one directive saying that the next task
// of that service to start here restores that artifact from that directory.
func TestMigrationPrepareRecordsTheDirective(t *testing.T) {
	cfg, store, root := prepareFixture(t)
	dir := filepath.Join(root, prepareTestArtifact)

	before := time.Now().UTC()
	rec := servePrepare(t, cfg, http.MethodPost, prepareBody("svc-1", prepareTestArtifact, dir))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String(), "prepare answers 204 with no body")

	record, found, err := store.Take("svc-1")
	require.NoError(t, err)
	require.True(t, found, "a valid prepare must record a directive for the service")
	assert.Equal(t, "svc-1", record.ServiceID)
	assert.Equal(t, prepareTestArtifact, record.ArtifactID)
	assert.Equal(t, dir, record.Dir, "the directive names the staging directory, not a path the executor must guess")
	assert.False(t, record.CreatedAt.Before(before), "the node stamps the directive when it records it")
	assert.WithinDuration(t, time.Now().UTC(), record.CreatedAt, time.Minute)
}

// TestMigrationPrepareReplacesAnEarlierDirective pins the retry: the manager repeats
// a prepare it never saw the answer to, and the node must end up with one directive -
// the last one - rather than a queue of restores for the same service.
func TestMigrationPrepareReplacesAnEarlierDirective(t *testing.T) {
	cfg, store, root := prepareFixture(t)
	first := filepath.Join(root, prepareTestArtifact)
	secondID := "mig-fedcba9876543210"
	second := filepath.Join(root, secondID)

	rec := servePrepare(t, cfg, http.MethodPost, prepareBody("svc-1", prepareTestArtifact, first))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	rec = servePrepare(t, cfg, http.MethodPost, prepareBody("svc-1", secondID, second))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	all, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, all, 1, "one directive per service per node")

	record, found, err := store.Take("svc-1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, secondID, record.ArtifactID, "the last prepare is the one a task claims")
}

// TestMigrationPrepareRefusesBadRequests pins the validation made before anything is
// recorded: the service must be an ID shape, the artifact must be one this build
// generates, and the directory must be the staging directory of that artifact under
// this node's migration root - a prepared record must not point anywhere else.
func TestMigrationPrepareRefusesBadRequests(t *testing.T) {
	cases := []struct {
		name    string
		body    func(root string) []byte
		want    output.Code
		reason  string
		param   string
		wantMsg string
	}{
		{
			name: "empty service",
			body: func(root string) []byte {
				return prepareBody("", prepareTestArtifact, filepath.Join(root, prepareTestArtifact))
			},
			want: output.CodeInvalid, reason: "invalid_service_id", param: "serviceId",
		},
		{
			name: "service with a path separator",
			body: func(root string) []byte {
				return prepareBody("svc/1", prepareTestArtifact, filepath.Join(root, prepareTestArtifact))
			},
			want: output.CodeInvalid, reason: "invalid_service_id", param: "serviceId",
		},
		{
			name: "service with a dot",
			body: func(root string) []byte {
				return prepareBody("..", prepareTestArtifact, filepath.Join(root, prepareTestArtifact))
			},
			want: output.CodeInvalid, reason: "invalid_service_id", param: "serviceId",
		},
		{
			name: "empty artifact",
			body: func(root string) []byte {
				return prepareBody("svc-1", "", filepath.Join(root, prepareTestArtifact))
			},
			want: output.CodeInvalid, reason: "invalid_artifact_id", param: "artifactId",
		},
		{
			name: "artifact that is a path",
			body: func(root string) []byte {
				return prepareBody("svc-1", "../../etc", filepath.Join(root, prepareTestArtifact))
			},
			want: output.CodeInvalid, reason: "invalid_artifact_id", param: "artifactId",
		},
		{
			name: "no directory",
			body: func(root string) []byte {
				return prepareBody("svc-1", prepareTestArtifact, "")
			},
			want: output.CodeInvalid, reason: "invalid_dir", param: "dir",
		},
		{
			name: "relative directory",
			body: func(root string) []byte {
				return prepareBody("svc-1", prepareTestArtifact, filepath.Join("migrations", prepareTestArtifact))
			},
			want: output.CodeInvalid, reason: "invalid_dir", param: "dir",
		},
		{
			name: "a directory outside the migration root",
			body: func(root string) []byte {
				return prepareBody("svc-1", prepareTestArtifact, "/etc")
			},
			want: output.CodeInvalid, reason: "dir_outside_migration_root", param: "dir",
		},
		{
			name: "the migration root itself",
			body: func(root string) []byte {
				return prepareBody("svc-1", prepareTestArtifact, root)
			},
			want: output.CodeInvalid, reason: "dir_outside_migration_root", param: "dir",
		},
		{
			name: "another artifact's directory",
			body: func(root string) []byte {
				return prepareBody("svc-1", prepareTestArtifact, filepath.Join(root, "mig-fedcba9876543210"))
			},
			want: output.CodeInvalid, reason: "dir_outside_migration_root", param: "dir",
		},
		{
			name: "a directory that climbs out of the root",
			body: func(root string) []byte {
				return prepareBody("svc-1", prepareTestArtifact, filepath.Join(root, prepareTestArtifact, "..", "..", "etc"))
			},
			want: output.CodeInvalid, reason: "dir_outside_migration_root", param: "dir",
		},
		{
			name: "a body that is not the document",
			body: func(string) []byte {
				return []byte("{not json")
			},
			want: output.CodeInvalid, reason: "body_unreadable",
		},
		{
			name: "a body with a field this build does not know",
			body: func(root string) []byte {
				return []byte(`{"serviceId":"svc-1","artifactId":"` + prepareTestArtifact +
					`","dir":"` + filepath.Join(root, prepareTestArtifact) + `","taskId":"task-1"}`)
			},
			want: output.CodeInvalid, reason: "body_unreadable",
		},
		{
			name: "a body carrying two documents",
			body: func(root string) []byte {
				one := prepareBody("svc-1", prepareTestArtifact, filepath.Join(root, prepareTestArtifact))
				return append(one, prepareBody("svc-2", prepareTestArtifact, filepath.Join(root, prepareTestArtifact))...)
			},
			want: output.CodeInvalid, reason: "body_unreadable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store, root := prepareFixture(t)

			rec := servePrepare(t, cfg, http.MethodPost, tc.body(root))

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			doc := decodeMigrationError(t, rec)
			assert.Equal(t, string(tc.want), doc.Error.Code)
			assert.Equal(t, tc.reason, doc.Error.Details["reason"])
			if tc.param != "" {
				assert.Equal(t, tc.param, doc.Error.Details["parameter"])
			}

			all, _, err := store.List()
			require.NoError(t, err)
			assert.Empty(t, all, "a refused prepare records nothing")
		})
	}
}

// TestMigrationPrepareAcceptsACleanedDirectory proves the root check is about where
// the path resolves, not how it is spelled: a trailing separator and a redundant
// ".." that climbs back to the same artifact directory describe the staging
// directory the manager staged into.
func TestMigrationPrepareAcceptsACleanedDirectory(t *testing.T) {
	cfg, store, root := prepareFixture(t)
	spelled := filepath.Join(root, prepareTestArtifact, "..", prepareTestArtifact) + string(filepath.Separator)

	rec := servePrepare(t, cfg, http.MethodPost, prepareBody("svc-1", prepareTestArtifact, spelled))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	record, found, err := store.Take("svc-1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, spelled, record.Dir)
}

// TestMigrationPrepareRefusesAnotherMethod pins the method set: prepare is a POST,
// and the operation answers 405 with its own Allow rather than reading a body it was
// not promised.
func TestMigrationPrepareRefusesAnotherMethod(t *testing.T) {
	cfg, _, root := prepareFixture(t)

	rec := servePrepare(t, cfg, http.MethodGet,
		prepareBody("svc-1", prepareTestArtifact, filepath.Join(root, prepareTestArtifact)))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	assert.Equal(t, http.MethodPost, rec.Header().Get("Allow"))
}

// TestMigrationPrepareNeedsThisNodesStores pins the two capabilities prepare cannot
// work without: the migration root it validates a directory against, and the
// directive store the daemon opens beside it.
func TestMigrationPrepareNeedsThisNodesStores(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*MigrationHandlerConfig)
		reason string
	}{
		{
			name:   "no migration root",
			mutate: func(cfg *MigrationHandlerConfig) { cfg.ArtifactDir = "" },
			reason: "migration_dir_unconfigured",
		},
		{
			name:   "no directive store",
			mutate: func(cfg *MigrationHandlerConfig) { cfg.Pending = nil },
			reason: "pending_restore_unavailable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _, root := prepareFixture(t)
			tc.mutate(&cfg)

			rec := servePrepare(t, cfg, http.MethodPost,
				prepareBody("svc-1", prepareTestArtifact, filepath.Join(root, prepareTestArtifact)))

			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			doc := decodeMigrationError(t, rec)
			assert.Equal(t, string(output.CodeUnavailable), doc.Error.Code)
			assert.Equal(t, tc.reason, doc.Error.Details["reason"], rec.Body.String())
		})
	}
}

// TestServerRoutesPrepareWithoutATask pins the routing ruling the other artifact
// operations already rely on: prepare answers for a task this node has never run,
// because the task that will restore the artifact does not exist yet.
func TestServerRoutesPrepareWithoutATask(t *testing.T) {
	cfg, store, root := prepareFixture(t)
	src := &fakeTaskSource{found: false}
	srv := newRouteServer(t, src)
	require.NoError(t, srv.Handle(MigrationPrepareOperation, NewMigrationPrepareHandler(cfg)))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/v1/tasks/task-gone/migration/prepare",
		bytes.NewReader(prepareBody("svc-1", prepareTestArtifact, filepath.Join(root, prepareTestArtifact)))))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Empty(t, src.gotID, "an artifact-scoped operation must not resolve the task in its path")

	_, found, err := store.Take("svc-1")
	require.NoError(t, err)
	assert.True(t, found, "the directive was recorded for a task that does not exist yet")
}

// TestManagerPrepareAsksTheTargetNode pins the manager's call: a POST on the
// node-scoped prepare route, addressed to the node that will restore, with the
// directive as its body. The route still carries a task segment - it is the one
// route shape every node operation has - but it is a placeholder the node never
// resolves.
func TestManagerPrepareAsksTheTargetNode(t *testing.T) {
	pki := newTestPKI(t)

	type observed struct {
		method      string
		path        string
		contentType string
		body        []byte
	}
	var got observed

	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationPrepareOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			got = observed{method: r.Method, path: r.URL.Path, contentType: r.Header.Get("Content-Type"), body: body}
			w.WriteHeader(http.StatusNoContent)
		},
	})

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	dir := "/var/lib/firecracker/migrations/" + prepareTestArtifact
	require.NoError(t, mgr.Prepare(context.Background(), "node-2", "svc-1", prepareTestArtifact, dir))

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/v1/tasks/"+migrationPrepareRouteTask+"/migration/prepare", got.path)
	assert.Equal(t, "application/json", got.contentType)

	var sent map[string]string
	require.NoError(t, json.Unmarshal(got.body, &sent))
	assert.Equal(t, map[string]string{
		"serviceId":  "svc-1",
		"artifactId": prepareTestArtifact,
		"dir":        dir,
	}, sent)
}

// TestManagerPrepareRefusesWhatItCannotSend pins the local refusals: the same rules
// the node applies are applied before a request, so a malformed directive never
// reaches the wire.
func TestManagerPrepareRefusesWhatItCannotSend(t *testing.T) {
	pki := newTestPKI(t)

	var called atomic.Bool
	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationPrepareOperation: func(_ context.Context, _ Task, _ http.ResponseWriter, _ *http.Request) {
			called.Store(true)
		},
	})
	mgr := newMigrationTestManager(t, pki, baseURL, 0)

	cases := []struct {
		name       string
		serviceID  string
		artifactID string
		dir        string
	}{
		{name: "empty service", serviceID: "", artifactID: prepareTestArtifact, dir: "/var/lib/firecracker/migrations/" + prepareTestArtifact},
		{name: "service with a dot", serviceID: "svc.1", artifactID: prepareTestArtifact, dir: "/var/lib/firecracker/migrations/" + prepareTestArtifact},
		{name: "artifact that is a path", serviceID: "svc-1", artifactID: "../etc", dir: "/var/lib/firecracker/migrations/" + prepareTestArtifact},
		{name: "relative directory", serviceID: "svc-1", artifactID: prepareTestArtifact, dir: "migrations/" + prepareTestArtifact},
		{name: "no directory", serviceID: "svc-1", artifactID: prepareTestArtifact, dir: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mgr.Prepare(context.Background(), "node-2", tc.serviceID, tc.artifactID, tc.dir)

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, output.CodeInvalid, typed.Code, typed.Message)
			assert.False(t, called.Load(), "a directive this build cannot send never becomes a request")
		})
	}
}

// TestManagerPrepareMapsTheNodesAnswer pins the error contract: a node's typed
// document crosses the relay with its code, and an answer that is not the 204 the
// operation promises is this side's failure to notice a broken node.
func TestManagerPrepareMapsTheNodesAnswer(t *testing.T) {
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
			name:        "a typed conflict",
			status:      http.StatusConflict,
			code:        output.CodeConflict,
			message:     "a directive for this service is being consumed",
			document:    true,
			wantCode:    output.CodeConflict,
			wantMessage: "a directive for this service is being consumed",
		},
		{
			name:        "a typed unavailable",
			status:      http.StatusServiceUnavailable,
			code:        output.CodeUnavailable,
			message:     "the pending restore could not be recorded on this node",
			document:    true,
			wantCode:    output.CodeUnavailable,
			wantMessage: "the pending restore could not be recorded on this node",
		},
		{
			name:        "a body that is not a typed error",
			status:      http.StatusInternalServerError,
			document:    false,
			wantCode:    codeUnavailable,
			wantMessage: "unavailable",
		},
		{
			name:        "a 2xx that did not record the directive",
			status:      http.StatusOK,
			document:    false,
			wantCode:    output.CodeInternal,
			wantMessage: "returned status 200",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
				MigrationPrepareOperation: func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
					if tc.document {
						writeNodeError(t, w, tc.status, tc.code, tc.message)
						return
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, "boom")
				},
			})

			mgr := newMigrationTestManager(t, pki, baseURL, 0)
			err := mgr.Prepare(context.Background(), "node-2", "svc-1", prepareTestArtifact,
				"/var/lib/firecracker/migrations/"+prepareTestArtifact)

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
