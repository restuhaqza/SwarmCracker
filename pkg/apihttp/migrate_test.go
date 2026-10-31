package apihttp

// The migration route is thin by design, so its tests are about the edges: who may
// call it, what a bad body answers, and that a report and a failure cross the wire
// as their own documents.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMigrationTransport is the manager-side migration transport under test.
type fakeMigrationTransport struct {
	doc v1.Migration
	err error

	calls      int
	gotTaskRef string
	gotTarget  string
}

func (f *fakeMigrationTransport) Migrate(_ context.Context, taskRef, targetNode string) (v1.Migration, error) {
	f.calls++
	f.gotTaskRef = taskRef
	f.gotTarget = targetNode
	return f.doc, f.err
}

func newMigrationTestAPI(
	t *testing.T, transport *fakeMigrationTransport, mutate func(*Config),
) *execTestAPI {
	t.Helper()

	return newExecTestAPIWithConfig(t, nil, nil, func(cfg *Config) {
		if transport != nil {
			cfg.Migrations = transport
		}
		if mutate != nil {
			mutate(cfg)
		}
	})
}

func postMigration(t *testing.T, api *execTestAPI, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodPost, "/v1/migrations", withExecToken(token), withExecBody(body))
}

func migrationTestDocument() v1.Migration {
	return v1.Migration{
		SchemaVersion: v1.SchemaVersion,
		Kind:          v1.KindMigration,
		ID:            "mig-0123456789abcdef",
		TaskID:        "task-1",
		ServiceID:     "svc-1",
		SourceNode:    "node-a",
		TargetNode:    "node-b",
		Phases: []v1.MigrationPhase{
			{Name: v1.MigrationPhaseValidate, StartedAt: "2026-10-31T12:00:00Z", FinishedAt: "2026-10-31T12:00:00.010Z", Result: v1.MigrationPhaseOK},
			{Name: v1.MigrationPhaseExport, StartedAt: "2026-10-31T12:00:00.010Z", FinishedAt: "2026-10-31T12:00:00.020Z", Result: v1.MigrationPhaseOK},
		},
		DowntimeMs: 1234,
		Outcome:    v1.MigrationOutcomeMoved,
	}
}

func TestCreateMigrationIsAdminOnly(t *testing.T) {
	transport := &fakeMigrationTransport{doc: migrationTestDocument()}
	api := newMigrationTestAPI(t, transport, nil)

	body := `{"taskId":"task-1","targetNode":"node-b"}`
	for _, token := range []string{api.operator, api.reader, api.byTask} {
		rec := postMigration(t, api, token, body)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

		doc := decodeErrorBody(t, rec)
		assert.Equal(t, string(output.CodeForbidden), doc.Error.Code)
		assert.Equal(t, string(ScopeMigrationsWrite), doc.Error.Details["requiredScope"])
	}
	assert.Zero(t, transport.calls, "an unauthorized caller never starts a migration")

	rec := api.do(t, http.MethodPost, "/v1/migrations", withExecBody(body))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Zero(t, transport.calls)
}

func TestCreateMigrationAnswers501WithoutATransport(t *testing.T) {
	api := newMigrationTestAPI(t, nil, nil)

	rec := postMigration(t, api, api.admin, `{"taskId":"task-1","targetNode":"node-b"}`)
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())

	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeNotImplemented), doc.Error.Code)
	assert.Equal(t, migrationCapability, doc.Error.Details["capability"])
}

func TestCreateMigrationRefusesABadBody(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		field  string
	}{
		{name: "missing task", body: `{"targetNode":"node-b"}`, status: http.StatusBadRequest, field: "taskId"},
		{name: "missing target", body: `{"taskId":"task-1"}`, status: http.StatusBadRequest, field: "targetNode"},
		{name: "blank target", body: `{"taskId":"task-1","targetNode":" "}`, status: http.StatusBadRequest, field: "targetNode"},
		{name: "unknown field", body: `{"taskId":"task-1","targetNode":"node-b","bogus":true}`, status: http.StatusBadRequest},
		{name: "not an object", body: `[]`, status: http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := &fakeMigrationTransport{doc: migrationTestDocument()}
			api := newMigrationTestAPI(t, transport, nil)

			rec := postMigration(t, api, api.admin, tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Zero(t, transport.calls, "a refused request never reaches the manager")

			if tc.field != "" {
				doc := decodeErrorBody(t, rec)
				fields, ok := doc.Error.Details["fields"].(map[string]any)
				require.True(t, ok, "details.fields must name the field")
				assert.Contains(t, fields, tc.field)
			}
		})
	}

	t.Run("wrong content type", func(t *testing.T) {
		api := newMigrationTestAPI(t, &fakeMigrationTransport{}, nil)
		rec := api.do(t, http.MethodPost, "/v1/migrations",
			withExecToken(api.admin),
			func(r *http.Request) {
				r.Body = http.NoBody
			})
		require.Equal(t, http.StatusUnsupportedMediaType, rec.Code, rec.Body.String())
	})
}

func TestCreateMigrationAnswers201WithTheReport(t *testing.T) {
	transport := &fakeMigrationTransport{doc: migrationTestDocument()}
	api := newMigrationTestAPI(t, transport, nil)

	rec := postMigration(t, api, api.admin, `{"taskId":"task-1","targetNode":"node-b"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, MediaType, rec.Header().Get("Content-Type"))

	assert.Equal(t, 1, transport.calls)
	assert.Equal(t, "task-1", transport.gotTaskRef)
	assert.Equal(t, "node-b", transport.gotTarget)

	var doc v1.Migration
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, v1.KindMigration, doc.Kind)
	assert.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
	assert.Equal(t, migrationTestDocument().ID, doc.ID)
	assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)
	require.Len(t, doc.Phases, 2)
	assert.Equal(t, v1.MigrationPhaseValidate, doc.Phases[0].Name)
}

func TestCreateMigrationCarriesTheTransportFailure(t *testing.T) {
	transport := &fakeMigrationTransport{
		err: output.NewError(output.CodeConflict, "the service changed while the artifact was in transit").
			WithDetails(map[string]any{"phase": v1.MigrationPhaseReplace}),
	}
	api := newMigrationTestAPI(t, transport, nil)

	rec := postMigration(t, api, api.admin, `{"taskId":"task-1","targetNode":"node-b"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeConflict), doc.Error.Code)
	assert.Equal(t, v1.MigrationPhaseReplace, doc.Error.Details["phase"])
}

// TestVersionAdvertisesMigrationOnlyWhenWired pins the capability rule the version
// document promises: a capability that is advertised is served.
func TestVersionAdvertisesMigrationOnlyWhenWired(t *testing.T) {
	wired := newMigrationTestAPI(t, &fakeMigrationTransport{}, nil)
	rec := wired.do(t, http.MethodGet, "/v1/version", withExecToken(wired.admin))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var withMigration v1.VersionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &withMigration))
	assert.Contains(t, withMigration.Capabilities, migrationCapability)

	unwired := newMigrationTestAPI(t, nil, nil)
	rec = unwired.do(t, http.MethodGet, "/v1/version", withExecToken(unwired.admin))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var without v1.VersionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &without))
	assert.NotContains(t, without.Capabilities, migrationCapability)
}
