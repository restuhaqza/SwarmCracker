// This file owns GET and PUT /v1/tasks/{ref}/files: regular-file transfer to and
// from a task's guest.
//
// The handler validates the request against the contract, resolves the task
// reference, enforces the caller's task selectors, audits the attempt and hands
// the request to the broker, which proxies it to the node that runs the task.
// Bytes are raw: a GET response is application/octet-stream and a PUT body is the
// file, the two documented exceptions to the v1 JSON envelope. Nothing here
// buffers a file: the proxy streams the response and the upload.
package apihttp

import (
	"net/http"
	"path"
	"strconv"
	"strings"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// filesOperation is the broker operation name for a file transfer.
	filesOperation = "files"
	// filesAuditAction is the audit action recorded for every file attempt.
	filesAuditAction = "task.files"
	// octetStreamMediaType is the media type of a raw file.
	octetStreamMediaType = "application/octet-stream"
	// maxFileUploadBytes bounds one upload at the manager too, so a caller cannot
	// make the manager stream an unbounded body towards a node.
	maxFileUploadBytes = 1 << 30
	// maxFileMode is the widest mode a caller may request.
	maxFileMode = 0o777
)

// getTaskFile serves GET /v1/tasks/{ref}/files?path=...
func (s *Server) getTaskFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireBroker(w, r, filesOperation) {
		s.recordFilesAudit(r, auditResourceForRef(r.PathValue("id")), "error")
		return
	}

	if err := validateFileTarget(r, false); err != nil {
		s.recordFilesAudit(r, auditResourceForRef(r.PathValue("id")), "error")
		s.writeError(w, r, err)
		return
	}

	task, ok := s.authorizeTaskFile(w, r)
	if !ok {
		return
	}
	s.proxyTaskFile(w, r, task, v1.AuditResource{Kind: v1.KindTask, ID: task.ID, Name: task.Name})
}

// putTaskFile serves PUT /v1/tasks/{ref}/files?path=...&mode=0644
func (s *Server) putTaskFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireBroker(w, r, filesOperation) {
		s.recordFilesAudit(r, auditResourceForRef(r.PathValue("id")), "error")
		return
	}

	if err := validateFileTarget(r, true); err != nil {
		s.recordFilesAudit(r, auditResourceForRef(r.PathValue("id")), "error")
		s.writeError(w, r, err)
		return
	}

	if status, err := requireContentType(r, octetStreamMediaType); err != nil {
		s.recordFilesAudit(r, auditResourceForRef(r.PathValue("id")), "error")
		s.writeErrorStatus(w, r, status, err)
		return
	}

	// A declared length above the cap is refused here; the node also bounds the
	// body it actually reads, which covers a chunked upload.
	if r.ContentLength > maxFileUploadBytes {
		s.recordFilesAudit(r, auditResourceForRef(r.PathValue("id")), "error")
		s.writeErrorStatus(w, r, http.StatusRequestEntityTooLarge, output.NewErrorf(output.CodeInvalid,
			"the request body exceeds %d bytes", maxFileUploadBytes).
			WithDetails(map[string]any{"reason": "payload_too_large", "limit": maxFileUploadBytes}))
		return
	}

	task, ok := s.authorizeTaskFile(w, r)
	if !ok {
		return
	}
	s.proxyTaskFile(w, r, task, v1.AuditResource{Kind: v1.KindTask, ID: task.ID, Name: task.Name})
}

// authorizeTaskFile resolves the task reference, loads the labels a selector may
// match and authorizes the caller. Every failure is written and audited here, so
// the two handlers share one rule.
func (s *Server) authorizeTaskFile(w http.ResponseWriter, r *http.Request) (v1.Task, bool) {
	ref := r.PathValue("id")

	task, err := s.cfg.Client.GetTask(r.Context(), ref)
	if err != nil {
		s.recordFilesAudit(r, auditResourceForRef(ref), "error")
		s.writeError(w, r, err)
		return v1.Task{}, false
	}
	resource := v1.AuditResource{Kind: v1.KindTask, ID: task.ID, Name: task.Name}

	labels, err := s.taskServiceLabels(r.Context(), task)
	if err != nil {
		s.recordFilesAudit(r, resource, "error")
		s.writeError(w, r, err)
		return v1.Task{}, false
	}

	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeTask(task.ServiceID, task.ID, labels); err != nil {
		s.recordFilesAudit(r, resource, "error")
		s.writeError(w, r, err)
		return v1.Task{}, false
	}

	return task, true
}

// proxyTaskFile forwards the request to the node and audits the outcome. The
// status is observed through a writer that passes every byte straight through,
// because the proxy reports only a resolve failure.
func (s *Server) proxyTaskFile(w http.ResponseWriter, r *http.Request, task v1.Task, resource v1.AuditResource) {
	r.Header.Set(requestIDHeader, requestIDFrom(r))

	recorder := &relayStatusRecorder{ResponseWriter: w}
	if err := s.cfg.Broker.Proxy(r.Context(), recorder, r, task.ID, filesOperation); err != nil {
		s.recordFilesAudit(r, resource, "error")
		s.writeError(w, r, err)
		return
	}

	if status := recorder.Status(); status < http.StatusOK || status >= http.StatusMultipleChoices {
		s.recordFilesAudit(r, resource, "error")
		return
	}
	s.recordFilesAudit(r, resource, "ok")
}

// validateFileTarget enforces the path rules and, for a PUT, the mode. The
// request is forwarded unchanged, so the node validates the same values again
// (defence in depth) and it is the node that owns the cleaned path it uses.
func validateFileTarget(r *http.Request, put bool) error {
	if err := validateGuestPath(r.URL.Query().Get("path")); err != nil {
		return err
	}
	if put {
		return validateFileMode(r.URL.Query().Get("mode"))
	}
	return nil
}

// validateGuestPath enforces the contract: an absolute, cleaned path with no ".."
// segment. A traversal attempt is refused, never collapsed into a different
// path, so a caller cannot probe outside the path it named.
func validateGuestPath(raw string) error {
	invalid := output.NewError(output.CodeInvalid,
		"path must be an absolute regular-file path without a .. segment").
		WithDetails(map[string]any{"parameter": "path"})

	if raw == "" || !strings.HasPrefix(raw, "/") {
		return invalid
	}
	if strings.ContainsAny(raw, "\x00\n\r") {
		return invalid
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return invalid
		}
	}

	cleaned := path.Clean(raw)
	if cleaned == "/" || cleaned == "." {
		return invalid
	}
	return nil
}

// validateFileMode enforces the octal mode: 0000-0777, with 0644 as the default
// the node applies when the parameter is absent.
func validateFileMode(raw string) error {
	if raw == "" {
		return nil
	}

	mode, err := strconv.ParseUint(raw, 8, 32)
	if err != nil || mode > maxFileMode {
		return output.NewError(output.CodeInvalid,
			"mode must be an octal value between 0000 and 0777").
			WithDetails(map[string]any{"parameter": "mode"})
	}
	return nil
}

// requireBroker answers 501 when the manager has no node broker and reports
// whether the request may continue. A manager without a broker still serves the
// read surface, so the failure is a typed capability answer, not a 500.
func (s *Server) requireBroker(w http.ResponseWriter, r *http.Request, capability string) bool {
	if s.cfg.Broker != nil {
		return true
	}
	s.writeError(w, r, output.NewError(output.CodeNotImplemented,
		"this manager has no node broker configured").
		WithDetails(map[string]any{"capability": capability}))
	return false
}

// recordFilesAudit writes one task.files record. The resource is the task: the
// path and the file contents are never recorded.
func (s *Server) recordFilesAudit(r *http.Request, resource v1.AuditResource, result string) {
	_ = s.recordAudit(r, filesAuditAction, resource, result)
}

// auditResourceForRef is the resource of a failure that happened before the task
// was resolved, so the reference the caller used is all there is.
func auditResourceForRef(ref string) v1.AuditResource {
	return v1.AuditResource{Kind: v1.KindTask, Name: ref}
}
