// This file owns the public live-migration surface of API v1:
//
//	POST /v1/migrations
//
// One call starts and finishes one move: the manager exports the task on its
// node, relays the artifact to the target, prepares the target's pending restore
// and replaces the service. There is no status route, because the call is
// synchronous and its answer is the report; a migration that outlives the verify
// deadline answers pending, and the manager's own retry (the replaced service) is
// what finishes it.
//
// The handler is deliberately thin: gate the capability, decode the one body,
// hand the request to the manager-side transport, audit the attempt. Every
// decision about which task may move and where lives in the daemon's
// orchestration, which is the only place that can see the cluster.
package apihttp

import (
	"context"
	"net/http"
	"strings"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// migrationCapability is the advertised capability name and the
	// details.capability value of the 501.
	migrationCapability = "migrations"

	// migrationAuditAction is the audit action recorded for every attempt. It
	// names the task, never an artifact or a path: a migration's bytes are
	// node-local.
	migrationAuditAction = "task.migrate"
)

// MigrationBroker is the manager-side migration transport: one call that moves
// one task and answers how far it got. It is a third optional interface rather
// than more methods on Broker or SnapshotBroker, so the exec, logs, files, health
// and snapshot fakes stay untouched.
type MigrationBroker interface {
	Migrate(ctx context.Context, taskRef, targetNode string) (v1.Migration, error)
}

// migrationsAvailable is the single migration predicate: the route gate and the
// capability advertisement both call it, so the version document can never
// promise a migration route that answers 501.
func migrationsAvailable(cfg Config) bool {
	return cfg.Migrations != nil
}

// requireMigrations answers 501 when the manager has no migration transport and
// reports whether the request may continue.
func (s *Server) requireMigrations(w http.ResponseWriter, r *http.Request) bool {
	if migrationsAvailable(s.cfg) {
		return true
	}

	s.writeError(w, r, output.NewError(output.CodeNotImplemented,
		"live migration is not enabled on this manager").
		WithDetails(map[string]any{
			"capability": migrationCapability,
			"reason":     "migrations_disabled",
		}))
	return false
}

// createMigration serves POST /v1/migrations.
//
// It is an admin route: the scope migrations:write is held by the admin role only,
// because a migration replaces a service's task and moves a guest's memory image
// between hosts. The body is a MigrationRequest with both fields required; the
// response is the migration report as a 201, whatever its outcome, because the
// request did run a migration. A failure that left nothing behind is the typed
// error document with the failing phase in details.phase.
func (s *Server) createMigration(w http.ResponseWriter, r *http.Request) {
	if !s.requireMigrations(w, r) {
		return
	}

	if status, err := requireContentType(r, MediaType); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	var body v1.MigrationRequest
	if status, err := readJSONWrite(w, r, &body); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}
	if problem := migrationRequestBodyProblem(body); problem != nil {
		s.writeError(w, r, problem)
		return
	}

	doc, err := s.cfg.Migrations.Migrate(r.Context(), body.TaskID, body.TargetNode)
	if err != nil {
		s.writeNamedAudit(r, migrationAuditAction,
			namedAuditResource(v1.KindTask, "", "", body.TaskID), "error")
		s.writeWriteError(w, r, err)
		return
	}

	s.writeNamedAudit(r, migrationAuditAction,
		namedAuditResource(v1.KindTask, doc.TaskID, "", body.TaskID), "ok")
	s.writeJSON(w, r, http.StatusCreated, doc)
}

// migrationRequestBodyProblem checks the two fields a migration cannot infer. A
// reference is resolved by the manager, so only presence is checked here: an
// unknown task is the manager's 404, and a blank field is this layer's 400.
func migrationRequestBodyProblem(body v1.MigrationRequest) error {
	fields := make(map[string]any, 2)
	if strings.TrimSpace(body.TaskID) == "" {
		fields["taskId"] = "must not be empty"
	}
	if strings.TrimSpace(body.TargetNode) == "" {
		fields["targetNode"] = "must not be empty"
	}
	if len(fields) == 0 {
		return nil
	}
	return output.NewError(output.CodeInvalid, "the migration request is incomplete").
		WithDetails(map[string]any{"fields": fields})
}
