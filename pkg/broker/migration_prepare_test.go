package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// prepareTestArtifact is the artifact ID the prepare fixtures name.
const prepareTestArtifact = "mig-0123456789abcdef"

// prepareTestManifest builds the manifest a prepare carries: the artifact document
// the target writes as artifact.json, which is what a restore reads at task start.
// artifactID is the identity the document names, so a test can hand one that
// disagrees with the directive.
func prepareTestManifest(t *testing.T, artifactID string) migration.Artifact {
	t.Helper()

	artifact := migrationArtifactFixture(t, []byte("state"), migrationTestBody(64, 1), nil)
	artifact.ID = artifactID
	require.NoError(t, artifact.Validate())
	return artifact
}

// prepareFixture is one node that can accept a directive: its migration root and the
// directive store the daemon would have opened beside it. The root starts empty, so a
// test that expects a 204 must stage the artifact into it first.
func prepareFixture(t *testing.T) (MigrationHandlerConfig, *scruntime.PendingRestores, string) {
	t.Helper()

	root := t.TempDir()
	store, err := scruntime.NewPendingRestores(filepath.Join(t.TempDir(), scruntime.PendingRestoreDirName))
	require.NoError(t, err)
	return MigrationHandlerConfig{ArtifactDir: root, Pending: store}, store, root
}

// stagePrepareArtifact puts the artifact's staged directory under the node's migration
// root, the way a completed stage leaves it.
func stagePrepareArtifact(t *testing.T, root, artifactID string) string {
	t.Helper()

	dir := filepath.Join(root, artifactID)
	require.NoError(t, os.Mkdir(dir, 0o700))
	return dir
}

// prepareBody is the request the manager sends: the service whose next task
// restores, the artifact the directive names, and the artifact's own manifest -
// the document the target writes into the staging directory. The directory is not
// in it: each node has its own migration root, so only the node can say where the
// files are.
//
// The manifest names the artifact the directive does, unless the ID is not one this
// build can name: those requests are refused for the ID before the manifest is
// looked at, and the body still has to decode.
func prepareBody(t *testing.T, serviceID, artifactID string) []byte {
	t.Helper()

	artifact := prepareTestManifest(t, prepareTestArtifact)
	if migration.ValidArtifactID(artifactID) {
		artifact.ID = artifactID
	}
	return prepareBodyWithManifest(t, serviceID, artifactID, artifact)
}

// prepareBodyWithManifest is prepareBody with the manifest named explicitly, so a
// test can send one that disagrees with the directive or that fails validation.
func prepareBodyWithManifest(
	t *testing.T, serviceID, artifactID string, artifact migration.Artifact,
) []byte {
	t.Helper()

	body, err := json.Marshal(struct {
		ServiceID  string             `json:"serviceId"`
		ArtifactID string             `json:"artifactId"`
		Artifact   migration.Artifact `json:"artifact"`
	}{serviceID, artifactID, artifact})
	require.NoError(t, err)
	return body
}

// readPrepareManifest reads the manifest a prepare left in dir with the reader the
// restore path uses, so the test asserts the document a task would actually read
// rather than the bytes this file wrote.
func readPrepareManifest(t *testing.T, dir string) migration.Artifact {
	t.Helper()

	artifact, err := migration.ReadArtifact(dir)
	require.NoError(t, err)
	return artifact
}

// onlyEntryNames lists the names inside dir, so a test can assert that the write's
// temporary file is gone.
func onlyEntryNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
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
// of that service to start here restores that artifact from the staging directory
// the node derived itself.
func TestMigrationPrepareRecordsTheDirective(t *testing.T) {
	cfg, store, root := prepareFixture(t)
	stagePrepareArtifact(t, root, prepareTestArtifact)

	before := time.Now().UTC()
	rec := servePrepare(t, cfg, http.MethodPost, prepareBody(t, "svc-1", prepareTestArtifact))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String(), "prepare answers 204 with no body")

	record, found, err := store.Take("svc-1", root)
	require.NoError(t, err)
	require.True(t, found, "a valid prepare must record a directive for the service")
	assert.Equal(t, "svc-1", record.ServiceID)
	assert.Equal(t, prepareTestArtifact, record.ArtifactID)
	assert.Equal(t, filepath.Join(root, prepareTestArtifact), record.Dir,
		"the directive names <this node's migration root>/<artifactID>, which only this node can derive")
	assert.False(t, record.CreatedAt.Before(before), "the node stamps the directive when it records it")
	assert.WithinDuration(t, time.Now().UTC(), record.CreatedAt, time.Minute)
}

// TestMigrationPrepareReplacesAnEarlierDirective pins the retry: the manager repeats
// a prepare it never saw the answer to, and the node must end up with one directive -
// the last one - rather than a queue of restores for the same service.
func TestMigrationPrepareReplacesAnEarlierDirective(t *testing.T) {
	cfg, store, root := prepareFixture(t)
	stagePrepareArtifact(t, root, prepareTestArtifact)
	secondID := "mig-fedcba9876543210"
	stagePrepareArtifact(t, root, secondID)

	rec := servePrepare(t, cfg, http.MethodPost, prepareBody(t, "svc-1", prepareTestArtifact))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	rec = servePrepare(t, cfg, http.MethodPost, prepareBody(t, "svc-1", secondID))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	all, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, all, 1, "one directive per service per node")

	record, found, err := store.Take("svc-1", root)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, secondID, record.ArtifactID, "the last prepare is the one a task claims")
	assert.Equal(t, filepath.Join(root, secondID), record.Dir)
}

// TestMigrationPrepareRefusesBadRequests pins the validation made before anything is
// recorded: the service must be an ID shape - one safe path segment, bounded, with no
// whitespace - and the artifact must be one this build generates. A refusal is a 400,
// never the 503 a file name the filesystem rejects would produce further down.
func TestMigrationPrepareRefusesBadRequests(t *testing.T) {
	cases := []struct {
		name    string
		body    func(t *testing.T, root string) []byte
		want    output.Code
		reason  string
		param   string
		wantMsg string
	}{
		{
			name: "empty service",
			body: func(t *testing.T, _ string) []byte { return prepareBody(t, "", prepareTestArtifact) },
			want: output.CodeInvalid, reason: "invalid_service_id", param: "serviceId",
		},
		{
			name: "service with a path separator",
			body: func(t *testing.T, _ string) []byte { return prepareBody(t, "svc/1", prepareTestArtifact) },
			want: output.CodeInvalid, reason: "invalid_service_id", param: "serviceId",
		},
		{
			name: "service with a dot",
			body: func(t *testing.T, _ string) []byte { return prepareBody(t, "..", prepareTestArtifact) },
			want: output.CodeInvalid, reason: "invalid_service_id", param: "serviceId",
		},
		{
			name: "service with whitespace",
			body: func(t *testing.T, _ string) []byte { return prepareBody(t, "svc 1", prepareTestArtifact) },
			want: output.CodeInvalid, reason: "invalid_service_id", param: "serviceId",
		},
		{
			// The record file is <serviceId>.json, so an ID past the bound would fail
			// the write with ENAMETOOLONG. That is the caller's mistake, and it must
			// read as one.
			name: "service past the file name bound",
			body: func(t *testing.T, _ string) []byte {
				return prepareBody(t, strings.Repeat("s", scruntime.MaxIDLength+1), prepareTestArtifact)
			},
			want: output.CodeInvalid, reason: "invalid_service_id", param: "serviceId",
		},
		{
			name: "empty artifact",
			body: func(t *testing.T, _ string) []byte { return prepareBody(t, "svc-1", "") },
			want: output.CodeInvalid, reason: "invalid_artifact_id", param: "artifactId",
		},
		{
			name: "artifact that is a path",
			body: func(t *testing.T, _ string) []byte { return prepareBody(t, "svc-1", "../../etc") },
			want: output.CodeInvalid, reason: "invalid_artifact_id", param: "artifactId",
		},
		{
			name: "a body that is not the document",
			body: func(*testing.T, string) []byte { return []byte("{not json") },
			want: output.CodeInvalid, reason: "body_unreadable",
		},
		{
			// A directory is the node's to derive. A caller that sends one is
			// describing a node it does not run.
			name: "a body that names a directory",
			body: func(*testing.T, string) []byte {
				return []byte(`{"serviceId":"svc-1","artifactId":"` + prepareTestArtifact +
					`","dir":"/var/lib/firecracker/migrations/` + prepareTestArtifact + `"}`)
			},
			want: output.CodeInvalid, reason: "body_unreadable",
		},
		{
			name: "a body with a field this build does not know",
			body: func(*testing.T, string) []byte {
				return []byte(`{"serviceId":"svc-1","artifactId":"` + prepareTestArtifact + `","taskId":"task-1"}`)
			},
			want: output.CodeInvalid, reason: "body_unreadable",
		},
		{
			name: "a body carrying two documents",
			body: func(t *testing.T, _ string) []byte {
				one := prepareBody(t, "svc-1", prepareTestArtifact)
				return append(one, prepareBody(t, "svc-2", prepareTestArtifact)...)
			},
			want: output.CodeInvalid, reason: "body_unreadable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store, root := prepareFixture(t)

			rec := servePrepare(t, cfg, http.MethodPost, tc.body(t, root))

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

// TestMigrationPrepareRefusesAnArtifactThisNodeDoesNotHold pins the other half of the
// ruling that dropped the directory from the body: the node derives the staging
// directory and requires it to be a real directory of its own. A missing directory, a
// symbolic link and a plain file are all "no artifact here", never something to
// record - a directive that survived would point a restore at a path it cannot use.
func TestMigrationPrepareRefusesAnArtifactThisNodeDoesNotHold(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, root string)
	}{
		{
			name:    "the artifact was never staged",
			prepare: func(*testing.T, string) {},
		},
		{
			name: "the artifact directory is a symbolic link",
			prepare: func(t *testing.T, root string) {
				require.NoError(t, os.RemoveAll(filepath.Join(root, prepareTestArtifact)))
				require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, prepareTestArtifact)))
			},
		},
		{
			name: "the artifact directory is a file",
			prepare: func(t *testing.T, root string) {
				path := filepath.Join(root, prepareTestArtifact)
				require.NoError(t, os.RemoveAll(path))
				require.NoError(t, os.WriteFile(path, []byte("not an artifact"), 0o600))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store, root := prepareFixture(t)
			tc.prepare(t, root)

			rec := servePrepare(t, cfg, http.MethodPost, prepareBody(t, "svc-1", prepareTestArtifact))

			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			doc := decodeMigrationError(t, rec)
			assert.Equal(t, string(output.CodeNotFound), doc.Error.Code)
			assert.Equal(t, "artifact_not_available", doc.Error.Details["reason"])

			_, statErr := os.Stat(filepath.Join(root, prepareTestArtifact, migrationArtifactDocumentName))
			require.Error(t, statErr,
				"a prepare that names no artifact this node holds leaves no readable manifest")

			all, _, err := store.List()
			require.NoError(t, err)
			assert.Empty(t, all, "a directive that names no artifact this node holds is not recorded")
		})
	}
}

// TestMigrationPrepareWritesTheManifest pins the ruling: the artifact document is
// carried by prepare, and the node writes it into the staging directory as
// artifact.json before it records the directive. That file is what a restore reads
// at task start, so a target that staged the bytes alone could not restore them.
func TestMigrationPrepareWritesTheManifest(t *testing.T) {
	cfg, _, root := prepareFixture(t)
	dir := stagePrepareArtifact(t, root, prepareTestArtifact)
	artifact := prepareTestManifest(t, prepareTestArtifact)

	rec := servePrepare(t, cfg, http.MethodPost,
		prepareBodyWithManifest(t, "svc-1", prepareTestArtifact, artifact))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	path := filepath.Join(dir, migrationArtifactDocumentName)
	info, err := os.Stat(path)
	require.NoError(t, err, "prepare must leave the manifest a restore reads")

	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the manifest names the guest's devices and its disk, so only the daemon reads it")
	assert.Equal(t, artifact, readPrepareManifest(t, dir),
		"the manifest on disk is the document the request carried")
	assert.Equal(t, []string{migrationArtifactDocumentName}, onlyEntryNames(t, dir),
		"the temporary the atomic write used is gone")
}

// TestMigrationPrepareReplacesTheManifest pins the retry: a second prepare writes
// the manifest again rather than refusing a name that already exists, because the
// manager repeats a prepare it never saw the answer to.
func TestMigrationPrepareReplacesTheManifest(t *testing.T) {
	cfg, _, root := prepareFixture(t)
	dir := stagePrepareArtifact(t, root, prepareTestArtifact)

	first := prepareTestManifest(t, prepareTestArtifact)
	rec := servePrepare(t, cfg, http.MethodPost,
		prepareBodyWithManifest(t, "svc-1", prepareTestArtifact, first))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Equal(t, first, readPrepareManifest(t, dir))

	second := first
	second.CreatedAt = first.CreatedAt.Add(time.Minute)
	require.NoError(t, second.Validate())
	rec = servePrepare(t, cfg, http.MethodPost,
		prepareBodyWithManifest(t, "svc-1", prepareTestArtifact, second))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	assert.Equal(t, second, readPrepareManifest(t, dir), "the last prepare is the manifest a restore reads")
	assert.Equal(t, []string{migrationArtifactDocumentName}, onlyEntryNames(t, dir),
		"replacing the manifest leaves one file and no temporary")
}

// TestMigrationPrepareRefusesAnArtifactThatNamesAnotherID pins the one identity the
// directive and the document share. A prepare whose two halves name different
// artifacts would write a manifest for an artifact the directive does not name, so a
// restore would read another migration's instructions.
func TestMigrationPrepareRefusesAnArtifactThatNamesAnotherID(t *testing.T) {
	cfg, store, root := prepareFixture(t)
	dir := stagePrepareArtifact(t, root, prepareTestArtifact)

	otherID := "mig-fedcba9876543210"
	rec := servePrepare(t, cfg, http.MethodPost, prepareBodyWithManifest(t, "svc-1",
		prepareTestArtifact, prepareTestManifest(t, otherID)))

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	doc := decodeMigrationError(t, rec)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	assert.Equal(t, "artifact_mismatch", doc.Error.Details["reason"])
	assert.Equal(t, "artifact", doc.Error.Details["parameter"])

	assert.Equal(t, []string{}, onlyEntryNames(t, dir), "a refused prepare writes no manifest")

	all, _, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, all, "a refused prepare records nothing")
}

// TestMigrationPrepareRefusesAnArtifactThatDoesNotValidate pins the rest of the
// document's validation: only a manifest this build can restore may become the
// node's artifact.json, whichever field is wrong.
func TestMigrationPrepareRefusesAnArtifactThatDoesNotValidate(t *testing.T) {
	missingSnapshot := func(t *testing.T) migration.Artifact {
		artifact := prepareTestManifest(t, prepareTestArtifact)
		artifact.SnapshotID = ""
		return artifact
	}
	missingRequestedTask := func(t *testing.T) migration.Artifact {
		artifact := prepareTestManifest(t, prepareTestArtifact)
		artifact.RequestedTaskID = ""
		return artifact
	}

	cases := []struct {
		name     string
		artifact func(t *testing.T) migration.Artifact
	}{
		{name: "no document at all", artifact: func(*testing.T) migration.Artifact { return migration.Artifact{} }},
		{name: "a manifest with no snapshot", artifact: missingSnapshot},
		{name: "a manifest with no requested task", artifact: missingRequestedTask},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store, root := prepareFixture(t)
			dir := stagePrepareArtifact(t, root, prepareTestArtifact)

			rec := servePrepare(t, cfg, http.MethodPost,
				prepareBodyWithManifest(t, "svc-1", prepareTestArtifact, tc.artifact(t)))

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			doc := decodeMigrationError(t, rec)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
			assert.Equal(t, "artifact_invalid", doc.Error.Details["reason"])
			assert.Equal(t, "artifact", doc.Error.Details["parameter"])

			assert.Empty(t, onlyEntryNames(t, dir), "an unusable manifest is never written")

			all, _, err := store.List()
			require.NoError(t, err)
			assert.Empty(t, all, "a refused prepare records nothing")
		})
	}
}

// TestMigrationPrepareRefusesAManifestItCannotWrite pins what a failed write means:
// a typed 503 and nothing recorded. The directive is the node promising a restore, so
// a node that could not keep the manifest must not promise one - and a restore would
// otherwise read a directory with no artifact.json.
func TestMigrationPrepareRefusesAManifestItCannotWrite(t *testing.T) {
	cfg, store, root := prepareFixture(t)
	dir := stagePrepareArtifact(t, root, prepareTestArtifact)
	// A directory in the manifest's place makes the rename that publishes it fail on
	// this node, however the daemon's uid is privileged.
	require.NoError(t, os.Mkdir(filepath.Join(dir, migrationArtifactDocumentName), 0o700))

	rec := servePrepare(t, cfg, http.MethodPost, prepareBody(t, "svc-1", prepareTestArtifact))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	doc := decodeMigrationError(t, rec)
	assert.Equal(t, string(output.CodeUnavailable), doc.Error.Code)
	assert.Equal(t, "manifest_unrecorded", doc.Error.Details["reason"])

	all, _, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, all, "a directive whose manifest could not be written is not recorded")
}

// TestMigrationPrepareRefusesAnotherMethod pins the method set: prepare is a POST,
// and the operation answers 405 with its own Allow rather than reading a body it was
// not promised.
func TestMigrationPrepareRefusesAnotherMethod(t *testing.T) {
	cfg, _, _ := prepareFixture(t)

	rec := servePrepare(t, cfg, http.MethodGet, prepareBody(t, "svc-1", prepareTestArtifact))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	assert.Equal(t, http.MethodPost, rec.Header().Get("Allow"))
}

// TestMigrationPrepareNeedsThisNodesStores pins the two capabilities prepare cannot
// work without: the migration root it derives a directory under, and the directive
// store the daemon opens beside it.
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
			cfg, _, _ := prepareFixture(t)
			tc.mutate(&cfg)

			rec := servePrepare(t, cfg, http.MethodPost, prepareBody(t, "svc-1", prepareTestArtifact))

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
	stagePrepareArtifact(t, root, prepareTestArtifact)
	src := &fakeTaskSource{found: false}
	srv := newRouteServer(t, src)
	require.NoError(t, srv.Handle(MigrationPrepareOperation, NewMigrationPrepareHandler(cfg)))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/v1/tasks/task-gone/migration/prepare",
		bytes.NewReader(prepareBody(t, "svc-1", prepareTestArtifact))))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Empty(t, src.gotID, "an artifact-scoped operation must not resolve the task in its path")

	_, found, err := store.Take("svc-1", root)
	require.NoError(t, err)
	assert.True(t, found, "the directive was recorded for a task that does not exist yet")
}

// TestManagerPrepareAsksTheTargetNode pins the manager's call: a POST on the
// node-scoped prepare route, addressed to the node that will restore, with the
// directive and the artifact's manifest as its body. The route still carries a task
// segment - it is the one route shape every node operation has - but it is a
// placeholder the node never resolves. The directory is not in the body: the manager
// cannot say where this node staged the files, and the manifest is what the target
// writes there for its restore to read.
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

	artifact := prepareTestManifest(t, prepareTestArtifact)
	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	require.NoError(t, mgr.Prepare(context.Background(), "node-2", "svc-1", artifact))

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/v1/tasks/"+migrationDirectiveRouteTask+"/migration/prepare", got.path)
	assert.Equal(t, "application/json", got.contentType)

	var sent map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got.body, &sent))
	require.Len(t, sent, 3, "the manager sends the directive and the manifest, and no directory")
	assert.JSONEq(t, `"svc-1"`, string(sent["serviceId"]))
	assert.JSONEq(t, `"`+prepareTestArtifact+`"`, string(sent["artifactId"]))

	var sentArtifact migration.Artifact
	require.NoError(t, json.Unmarshal(sent["artifact"], &sentArtifact))
	assert.Equal(t, artifact, sentArtifact,
		"the target writes this document as artifact.json: it is the manifest its restore reads")
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
		name      string
		serviceID string
		artifact  migration.Artifact
	}{
		{name: "empty service", serviceID: "", artifact: prepareTestManifest(t, prepareTestArtifact)},
		{name: "service with a dot", serviceID: "svc.1", artifact: prepareTestManifest(t, prepareTestArtifact)},
		{name: "service with whitespace", serviceID: "svc 1", artifact: prepareTestManifest(t, prepareTestArtifact)},
		{name: "service past the file name bound", serviceID: strings.Repeat("s", scruntime.MaxIDLength+1), artifact: prepareTestManifest(t, prepareTestArtifact)},
		{name: "artifact that is a path", serviceID: "svc-1", artifact: migration.Artifact{ID: "../etc"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mgr.Prepare(context.Background(), "node-2", tc.serviceID, tc.artifact)

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
			err := mgr.Prepare(context.Background(), "node-2", "svc-1",
				prepareTestManifest(t, prepareTestArtifact))

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
