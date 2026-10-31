package client

// The migration call is the one write this package makes against the manager
// API alone, so its tests are about the wire (one POST, the two fields), the
// manager's typed refusal crossing unchanged, and the deadline: the client's own
// request timeout was chosen for reads, and a move must not be cut off by it.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// migrationReport is the document the manager answers.
func migrationReport() v1.Migration {
	return v1.Migration{
		SchemaVersion: v1.SchemaVersion,
		Kind:          v1.KindMigration,
		ID:            "mig-0123456789abcdef",
		TaskID:        "task-1",
		ServiceID:     "svc-1",
		SourceNode:    "node-a",
		TargetNode:    "node-b",
		Phases: []v1.MigrationPhase{{
			Name: v1.MigrationPhaseValidate, StartedAt: "2026-10-31T12:00:00Z",
			FinishedAt: "2026-10-31T12:00:00.010Z", Result: v1.MigrationPhaseOK,
		}},
		DowntimeMs: 1234,
		Outcome:    v1.MigrationOutcomeMoved,
	}
}

// TestMigratePostsTheRequest pins the one request a migration makes: the two
// fields the manager resolves, on the route that owns the document.
func TestMigratePostsTheRequest(t *testing.T) {
	var got capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		got = captureRequest(t, r)
		writeTestDocument(w, http.StatusCreated, migrationReport())
	})

	doc, err := c.Migrate(context.Background(), "task-1", "node-b")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/v1/migrations", got.path)
	assert.Equal(t, testAccept, got.contentType)

	var body v1.MigrationRequest
	require.NoError(t, json.Unmarshal(got.body, &body))
	assert.Equal(t, v1.MigrationRequest{TaskID: "task-1", TargetNode: "node-b"}, body)

	assert.Equal(t, v1.KindMigration, doc.Kind)
	assert.Equal(t, "mig-0123456789abcdef", doc.ID)
	assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)
	assert.Equal(t, int64(1234), doc.DowntimeMs)
	require.Len(t, doc.Phases, 1)
	assert.Equal(t, v1.MigrationPhaseValidate, doc.Phases[0].Name)
}

// TestMigrateSurfacesTheManagerRefusal pins the failure contract: the manager's
// typed error is the answer, with the phase and the leader it names, so the CLI
// prints it and exits with its code instead of inventing a reason.
func TestMigrateSurfacesTheManagerRefusal(t *testing.T) {
	cases := []struct {
		name   string
		err    *output.Error
		status int
	}{
		{
			name: "a migration already in flight",
			err: output.NewError(output.CodeConflict, "a migration of service \"svc-1\" is already in flight").
				WithDetails(map[string]any{"reason": "migration_in_flight", "phase": v1.MigrationPhaseValidate}),
			status: http.StatusConflict,
		},
		{
			name: "a follower refusing the move",
			err: output.NewError(output.CodeUnavailable,
				`node "manager-1" is the swarm leader, not this manager: retry the migration there`).
				WithDetails(map[string]any{"reason": "not_leader", "leader": "manager-1"}),
			status: http.StatusServiceUnavailable,
		},
		{
			name: "a manager that cannot migrate",
			err: output.NewError(output.CodeNotImplemented, "live migration is not enabled on this manager").
				WithDetails(map[string]any{"capability": "migrations"}),
			status: http.StatusNotImplemented,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
				writeTestDocument(w, tc.status, v1.NewErrorDocument(v1.Error{
					Code:    string(tc.err.Code),
					Message: tc.err.Message,
					Details: tc.err.Details,
				}))
			})

			_, err := c.Migrate(context.Background(), "task-1", "node-b")
			typed := codeOf(t, err)
			assert.Equal(t, tc.err.Code, typed.Code)
			assert.Equal(t, tc.err.Message, typed.Message, "the manager's message is the answer")
			assert.Equal(t, tc.err.Details["phase"], typed.Details["phase"])
			assert.Equal(t, tc.err.Details["leader"], typed.Details["leader"])
		})
	}
}

// TestMigrateOverridesTheClientRequestTimeout pins the one deadline that matters:
// the client was built for reads (a millisecond here), and a move that takes
// longer - every move does - still completes because the call carries its own.
func TestMigrateOverridesTheClientRequestTimeout(t *testing.T) {
	c := newTestRemote(t, Config{Timeout: time.Millisecond}, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		writeTestDocument(w, http.StatusCreated, migrationReport())
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	doc, err := c.Migrate(ctx, "task-1", "node-b")
	require.NoError(t, err)
	assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)
}

// TestMigrateUsesTheCallersDeadline pins the override's source: the caller's
// context is the deadline, so a move the caller no longer waits for is not kept
// alive by the call's own default.
func TestMigrateUsesTheCallersDeadline(t *testing.T) {
	release := make(chan struct{})
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := c.Migrate(ctx, "task-1", "node-b")
	typed := codeOf(t, err)
	assert.Equal(t, output.CodeTimeout, typed.Code)
	assert.Less(t, time.Since(started), 2*time.Second)
}

// TestMigrateNeedsTheManagerAPI pins the local refusal: the route exists on the
// manager only, so a client with no endpoint says so rather than dialing a
// cluster socket that cannot serve it.
func TestMigrateNeedsTheManagerAPI(t *testing.T) {
	c := &Client{}

	_, err := c.Migrate(context.Background(), "task-1", "node-b")
	typed := codeOf(t, err)
	assert.Equal(t, output.CodeUsage, typed.Code)
	assert.Contains(t, typed.Message, "--host")
}

// TestMigrateRefusesAnotherDocument pins the kind check: an answer that is not a
// migration report is a manager this build cannot read, and printing it as a
// report would state an outcome nobody stated.
func TestMigrateRefusesAnotherDocument(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		writeTestDocument(w, http.StatusCreated, v1.NewServiceDocument(v1.Service{ID: "svc-1"}))
	})

	_, err := c.Migrate(context.Background(), "task-1", "node-b")
	typed := codeOf(t, err)
	assert.Equal(t, output.CodeInternal, typed.Code)
	assert.Contains(t, typed.Message, "Service")
}
