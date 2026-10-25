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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

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
	// defaultSnapshotTimeout bounds one create or restore. Config.SnapshotTimeout
	// overrides it for tests and for an operator with a slow node.
	defaultSnapshotTimeout = 30 * time.Second
)

// SnapshotBroker is the manager's snapshot transport. It is a second optional
// interface rather than four more methods on Broker, so exec, logs, files and
// health fakes stay untouched.
type SnapshotBroker interface {
	CreateSnapshot(ctx context.Context, task v1.Task) (v1.Snapshot, error)
	ListTaskSnapshots(ctx context.Context, task v1.Task) ([]v1.Snapshot, error)
	RestoreSnapshot(ctx context.Context, task v1.Task, snapshotID string) (v1.Snapshot, error)
	DeleteSnapshot(ctx context.Context, task v1.Task, snapshotID string) error
}

// requireSnapshots answers 501 when the capability or its transport is missing
// and reports whether the request may continue. It runs before task resolution
// and before any audit record: a request to a disabled capability did not touch
// a task.
func (s *Server) requireSnapshots(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.SnapshotsEnabled && s.cfg.Snapshots != nil {
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

// snapshotTimeout is the per-request deadline for create and restore.
func (s *Server) snapshotTimeout() time.Duration {
	if s.cfg.SnapshotTimeout <= 0 {
		return defaultSnapshotTimeout
	}
	return s.cfg.SnapshotTimeout
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
	})
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

	status, err := s.validateSnapshotCreateBody(w, r)
	if err != nil {
		s.recordSnapshotAudit(r, auditResourceForRef(ref), "error")
		s.writeErrorStatus(w, r, status, err)
		return
	}

	task, resource, ok := s.resolveSnapshotTask(w, r, ref)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.snapshotTimeout())
	defer cancel()

	snapshot, err := s.cfg.Snapshots.CreateSnapshot(ctx, task)
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

	ctx, cancel := context.WithTimeout(r.Context(), s.snapshotTimeout())
	defer cancel()

	snapshot, err := s.cfg.Snapshots.RestoreSnapshot(ctx, task, snapshotID)
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
// The body is validated here even though the transport snapshots the task's own
// VM: accepting a field the API cannot honour would be worse than rejecting it,
// and the node records the resources the running VM actually has.
func (s *Server) validateSnapshotCreateBody(w http.ResponseWriter, r *http.Request) (int, error) {
	var request v1.SnapshotCreateRequest

	r.Body = http.MaxBytesReader(w, r.Body, maxSnapshotBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return http.StatusRequestEntityTooLarge,
				output.NewErrorf(output.CodeInvalid, "the request body exceeds %d bytes", maxSnapshotBodyBytes).
					WithDetails(map[string]any{
						"reason": "payload_too_large",
						"limit":  maxSnapshotBodyBytes,
					})
		}
		return http.StatusBadRequest,
			output.NewErrorf(output.CodeInvalid, "read request body: %v", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return 0, nil
	}

	if status, err := requireContentType(r, MediaType); err != nil {
		return status, err
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return http.StatusBadRequest,
			output.NewError(output.CodeInvalid,
				"invalid JSON body: the snapshot request has unknown or malformed fields").
				WithDetails(map[string]any{"reason": "invalid_body"})
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"the request body must contain exactly one JSON object")
	}
	if request.VCPUs < 0 || request.MemoryMB < 0 {
		return http.StatusBadRequest,
			output.NewError(output.CodeInvalid, "vcpus and memoryMB must be zero or positive").
				WithDetails(map[string]any{"reason": "invalid_resources"})
	}
	return 0, nil
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
