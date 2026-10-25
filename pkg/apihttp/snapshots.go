// This file owns the experimental snapshot surface of API v1:
//
//	POST   /v1/tasks/{ref}/snapshots
//	GET    /v1/tasks/{ref}/snapshots
//	POST   /v1/tasks/{ref}/snapshots/{id}/restore
//	DELETE /v1/tasks/{ref}/snapshots/{id}
//	GET    /v1/snapshots?task={ref}
//	POST   /v1/snapshots/{id}/restore?task={ref}
//	DELETE /v1/snapshots/{id}?task={ref}
//
// The handlers are deliberately thin: gate the capability, resolve the task
// reference server-side, enforce the caller's task selectors exactly as exec
// does, audit the attempt, then hand the work to the manager's snapshot
// transport. A snapshot is node-local, so the manager relays to the node that
// ran the task; the caller never picks a host.
//
// The flat /v1/snapshots forms are an address convenience, not a second
// namespace: they require ?task= because a snapshot ID alone does not name the
// node that holds it. The task-scoped forms are the ergonomic path.
//
// Restore is experimental. It starts a Firecracker process outside SwarmKit's
// task lifecycle, so a restored VM may outlive the task it came from.
package apihttp

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// snapshotCapability is the advertised capability name and the
	// details.capability value of the 501.
	snapshotCapability = "snapshots"
	// snapshotAuditAction is the audit action recorded for every attempt.
	snapshotAuditAction = "task.snapshot"
	// maxSnapshotBodyBytes bounds one optional create body.
	maxSnapshotBodyBytes = 64 << 10
)

// SnapshotBroker is the manager's snapshot transport. It is a second optional
// interface rather than four more methods on Broker, so exec, logs, files and
// health fakes stay untouched.
type SnapshotBroker interface {
	CreateSnapshot(ctx context.Context, task v1.Task, req v1.SnapshotCreateRequest) (v1.Snapshot, error)
	ListTaskSnapshots(ctx context.Context, task v1.Task) ([]v1.Snapshot, error)
	RestoreSnapshot(ctx context.Context, task v1.Task, snapshotID string) (v1.Snapshot, error)
	DeleteSnapshot(ctx context.Context, task v1.Task, snapshotID string) error
}

// snapshotsAvailable is the single snapshot predicate. The route gate and the
// capability advertisement both call it, so the version document can never
// promise a snapshot route that answers 501, or hide one that works.
func snapshotsAvailable(cfg Config) bool {
	return cfg.SnapshotsEnabled && cfg.Snapshots != nil
}

// requireSnapshots answers 501 when the capability or its transport is missing
// and reports whether the request may continue. It runs before task resolution
// and before any audit record: a request to a disabled capability did not touch
// a task.
func (s *Server) requireSnapshots(w http.ResponseWriter, r *http.Request) bool {
	if snapshotsAvailable(s.cfg) {
		return true
	}

	s.writeError(w, r, output.NewError(output.CodeNotImplemented,
		"snapshots are not enabled on this manager").
		WithDetails(map[string]any{
			"capability": snapshotCapability,
			"reason":     "snapshots_disabled",
		}))
	return false
}

// listTaskSnapshots serves GET /v1/tasks/{ref}/snapshots.
func (s *Server) listTaskSnapshots(w http.ResponseWriter, r *http.Request) {
	if !s.requireSnapshots(w, r) {
		return
	}
	s.serveSnapshotList(w, r, r.PathValue("id"), false)
}

// listSnapshots serves GET /v1/snapshots?task={ref}.
func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request) {
	if !s.requireSnapshots(w, r) {
		return
	}
	s.serveSnapshotList(w, r, r.URL.Query().Get("task"), true)
}

// serveSnapshotList is the shared list path. flat marks the /v1/snapshots form,
// whose task parameter is required.
func (s *Server) serveSnapshotList(w http.ResponseWriter, r *http.Request, ref string, flat bool) {
	if flat && ref == "" {
		s.writeSnapshotMissingTask(w, r)
		return
	}

	task, resource, ok := s.resolveSnapshotTask(w, r, ref)
	if !ok {
		return
	}

	items, err := s.cfg.Snapshots.ListTaskSnapshots(r.Context(), task)
	if err != nil {
		s.recordSnapshotAudit(r, resource, "error")
		s.writeError(w, r, err)
		return
	}

	page, next, err := paginateRequest(r, items, func(item v1.Snapshot) (string, string) {
		return item.CreatedAt, item.ID
	}, task.ID)
	if err != nil {
		s.recordSnapshotAudit(r, resource, "error")
		s.writeError(w, r, err)
		return
	}

	doc := v1.NewSnapshotList(page)
	doc.Next = next
	s.recordSnapshotAudit(r, resource, "ok")
	s.writeJSON(w, r, http.StatusOK, doc)
}

// createTaskSnapshot serves POST /v1/tasks/{ref}/snapshots.
func (s *Server) createTaskSnapshot(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("id")
	if !s.requireSnapshots(w, r) {
		return
	}

	request, status, err := s.validateSnapshotCreateBody(w, r)
	if err != nil {
		s.recordSnapshotAudit(r, auditResourceForRef(ref), "error")
		s.writeErrorStatus(w, r, status, err)
		return
	}

	task, resource, ok := s.resolveSnapshotTask(w, r, ref)
	if !ok {
		return
	}

	snapshot, err := s.cfg.Snapshots.CreateSnapshot(r.Context(), task, request)
	if err != nil {
		s.recordSnapshotAudit(r, resource, "error")
		s.writeError(w, r, err)
		return
	}

	s.recordSnapshotAudit(r, resource, "ok")
	s.writeJSON(w, r, http.StatusCreated, v1.NewSnapshotDocument(snapshot))
}

// restoreTaskSnapshot serves POST /v1/tasks/{ref}/snapshots/{id}/restore.
func (s *Server) restoreTaskSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.requireSnapshots(w, r) {
		return
	}
	s.serveSnapshotRestore(w, r, r.PathValue("id"), r.PathValue("sid"), false)
}

// restoreSnapshot serves POST /v1/snapshots/{id}/restore?task={ref}.
func (s *Server) restoreSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.requireSnapshots(w, r) {
		return
	}
	s.serveSnapshotRestore(w, r, r.URL.Query().Get("task"), r.PathValue("id"), true)
}

// serveSnapshotRestore is the shared restore path.
func (s *Server) serveSnapshotRestore(w http.ResponseWriter, r *http.Request, ref, snapshotID string, flat bool) {
	if flat && ref == "" {
		s.writeSnapshotMissingTask(w, r)
		return
	}
	if !v1.ValidSnapshotID(snapshotID) {
		s.writeSnapshotInvalidID(w, r, ref)
		return
	}

	task, resource, ok := s.resolveSnapshotTask(w, r, ref)
	if !ok {
		return
	}

	snapshot, err := s.cfg.Snapshots.RestoreSnapshot(r.Context(), task, snapshotID)
	if err != nil {
		s.recordSnapshotAudit(r, resource, "error")
		s.writeError(w, r, err)
		return
	}

	s.recordSnapshotAudit(r, resource, "ok")
	s.writeJSON(w, r, http.StatusOK, v1.NewSnapshotDocument(snapshot))
}

// deleteTaskSnapshot serves DELETE /v1/tasks/{ref}/snapshots/{id}.
func (s *Server) deleteTaskSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.requireSnapshots(w, r) {
		return
	}
	s.serveSnapshotDelete(w, r, r.PathValue("id"), r.PathValue("sid"), false)
}

// deleteSnapshot serves DELETE /v1/snapshots/{id}?task={ref}.
func (s *Server) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.requireSnapshots(w, r) {
		return
	}
	s.serveSnapshotDelete(w, r, r.URL.Query().Get("task"), r.PathValue("id"), true)
}

// serveSnapshotDelete is the shared delete path.
func (s *Server) serveSnapshotDelete(w http.ResponseWriter, r *http.Request, ref, snapshotID string, flat bool) {
	if flat && ref == "" {
		s.writeSnapshotMissingTask(w, r)
		return
	}
	if !v1.ValidSnapshotID(snapshotID) {
		s.writeSnapshotInvalidID(w, r, ref)
		return
	}

	task, resource, ok := s.resolveSnapshotTask(w, r, ref)
	if !ok {
		return
	}

	if err := s.cfg.Snapshots.DeleteSnapshot(r.Context(), task, snapshotID); err != nil {
		s.recordSnapshotAudit(r, resource, "error")
		s.writeError(w, r, err)
		return
	}

	s.recordSnapshotAudit(r, resource, "ok")
	w.WriteHeader(http.StatusNoContent)
}

// resolveSnapshotTask resolves the caller's reference, enforces the task
// selectors and returns the task and its audit resource. A failure is already
// written and audited as an error.
func (s *Server) resolveSnapshotTask(w http.ResponseWriter, r *http.Request, ref string) (v1.Task, v1.AuditResource, bool) {
	task, err := s.cfg.Client.GetTask(r.Context(), ref)
	if err != nil {
		s.recordSnapshotAudit(r, auditResourceForRef(ref), "error")
		s.writeError(w, r, err)
		return v1.Task{}, v1.AuditResource{}, false
	}
	resource := v1.AuditResource{Kind: v1.KindTask, ID: task.ID, Name: task.Name}

	labels, err := s.taskServiceLabels(r.Context(), task)
	if err != nil {
		s.recordSnapshotAudit(r, resource, "error")
		s.writeError(w, r, err)
		return v1.Task{}, v1.AuditResource{}, false
	}

	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeTask(task.ServiceID, task.ID, labels); err != nil {
		s.recordSnapshotAudit(r, resource, "error")
		s.writeError(w, r, err)
		return v1.Task{}, v1.AuditResource{}, false
	}
	return task, resource, true
}

// validateSnapshotCreateBody reads the optional strict create body. An empty
// body is valid and needs no content type; a body must declare MediaType.
//
// The parsed request is returned and forwarded to the manager, which sends it as
// the node's JSON body: validating a field the transport then drops would accept
// an instruction the API cannot honour.
func (s *Server) validateSnapshotCreateBody(w http.ResponseWriter, r *http.Request) (v1.SnapshotCreateRequest, int, error) {
	var request v1.SnapshotCreateRequest

	present, status, err := readOptionalJSONBody(w, r, maxSnapshotBodyBytes, MediaType, &request)
	if err != nil {
		return request, status, err
	}
	if !present {
		return request, 0, nil
	}

	if request.VCPUs < 0 {
		return request, http.StatusBadRequest,
			output.NewError(output.CodeInvalid, "vcpus must be zero or positive").
				WithDetails(map[string]any{"parameter": "vcpus"})
	}
	if request.MemoryMB < 0 {
		return request, http.StatusBadRequest,
			output.NewError(output.CodeInvalid, "memoryMB must be zero or positive").
				WithDetails(map[string]any{"parameter": "memoryMB"})
	}
	if request.RootfsPath != "" && !validSnapshotRootfsPath(request.RootfsPath) {
		return request, http.StatusBadRequest,
			output.NewError(output.CodeInvalid,
				"rootfsPath must be an absolute path without a .. segment").
				WithDetails(map[string]any{"parameter": "rootfsPath"})
	}
	return request, 0, nil
}

// validSnapshotRootfsPath accepts any absolute path that cannot leave its
// directory. The path is recorded in the node's snapshot metadata, so only two
// things are unsafe: a relative path, which depends on the daemon's working
// directory, and a .. segment, which traverses upwards. Redundant separators, a
// trailing slash and . segments all name the same absolute directory and are
// accepted: the schema says only relative and .. are invalid, so a caller that
// concatenates a path is not punished for it.
func validSnapshotRootfsPath(raw string) bool {
	if strings.ContainsAny(raw, "\x00\n\r") {
		return false
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return false
		}
	}
	// The schema's rule is about the target the path names, so normalize the way
	// the OS would: the cleaned value must still be absolute and must name
	// something below the filesystem root.
	cleaned := filepath.Clean(raw)
	return filepath.IsAbs(cleaned) && cleaned != "/"
}

// writeSnapshotMissingTask is the 400 for a flat form with no ?task=. No audit
// record is written: the request named no task, so there is no task resource to
// record, and a record with an empty id would be noise in the log.
func (s *Server) writeSnapshotMissingTask(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, output.NewError(output.CodeInvalid,
		"task is required: a snapshot lives on the node that ran the task").
		WithDetails(map[string]any{"parameter": "task"}))
}

// writeSnapshotInvalidID is the 400 for an id that is not one safe path segment.
func (s *Server) writeSnapshotInvalidID(w http.ResponseWriter, r *http.Request, ref string) {
	s.recordSnapshotAudit(r, auditResourceForRef(ref), "error")
	s.writeError(w, r, output.NewError(output.CodeInvalid, "snapshot ID is malformed").
		WithDetails(map[string]any{"parameter": "id"}))
}

// recordSnapshotAudit writes one task.snapshot record. The resource is the task:
// a snapshot path, checksum or file size is never recorded.
func (s *Server) recordSnapshotAudit(r *http.Request, resource v1.AuditResource, result string) {
	_ = s.recordAudit(r, snapshotAuditAction, resource, result)
}
