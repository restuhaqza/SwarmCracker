// This file owns POST /v1/tasks/{ref}/attach: the public interactive console
// path (R18).
//
// The transport is one HTTP/1.1 request, full duplex: the chunked request body is
// the caller's stdin and the chunked response body is the guest console. No
// WebSocket and no second connection. The handler is as thin as exec's: it
// resolves the task reference, enforces the caller's selectors, audits the
// attempt and hands the request to the broker manager, which relays it to the
// node that runs the task.
//
// The response is application/octet-stream — raw console bytes, the documented
// exception to the v1 JSON envelope — and is never buffered here.
package apihttp

import (
	"net/http"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// attachOperation is the broker operation name for attach.
	attachOperation = "attach"
	// attachAuditAction is the audit action recorded for every attempt.
	attachAuditAction = "task.attach"
)

// attachTask serves POST /v1/tasks/{ref}/attach.
func (s *Server) attachTask(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("id")

	if s.cfg.Broker == nil {
		s.recordAttachAudit(r, v1.AuditResource{Kind: v1.KindTask, Name: ref}, "error")
		s.writeError(w, r, output.NewError(output.CodeNotImplemented,
			"this manager has no node broker configured").
			WithDetails(map[string]any{"capability": attachOperation}))
		return
	}

	// Resolve the reference server-side: the broker only ever receives a
	// concrete task ID, never user input.
	task, err := s.cfg.Client.GetTask(r.Context(), ref)
	if err != nil {
		s.recordAttachAudit(r, v1.AuditResource{Kind: v1.KindTask, Name: ref}, "error")
		s.writeError(w, r, err)
		return
	}
	resource := v1.AuditResource{Kind: v1.KindTask, ID: task.ID, Name: task.Name}

	labels, err := s.taskServiceLabels(r.Context(), task)
	if err != nil {
		s.recordAttachAudit(r, resource, "error")
		s.writeError(w, r, err)
		return
	}

	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeTask(task.ServiceID, task.ID, labels); err != nil {
		s.recordAttachAudit(r, resource, "error")
		s.writeError(w, r, err)
		return
	}

	// Carry the public request ID on the outbound call so a relayed node error
	// can name it.
	r.Header.Set(requestIDHeader, requestIDFrom(r))

	// Wrap the writer so the relayed status is observable: Proxy reports only a
	// resolve failure, so a node 4xx/5xx would otherwise be audited as success.
	recorder := &relayStatusRecorder{ResponseWriter: w}

	// Full duplex must be enabled before the body is read. The caller may still
	// be sending stdin when the console answers, and without this net/http would
	// consume the whole body before writing the first response byte.
	// ErrNotSupported (an in-memory writer, HTTP/2) is not fatal: the relay still
	// works one direction at a time.
	_ = http.NewResponseController(recorder).EnableFullDuplex()

	if err := s.cfg.Broker.Proxy(r.Context(), recorder, r, task.ID, attachOperation); err != nil {
		// A resolve failure happens before anything is written, so it is still
		// safe to answer with a typed document.
		s.recordAttachAudit(r, resource, "error")
		s.writeError(w, r, err)
		return
	}

	// The session was served only when the node accepted the attach. A relayed
	// 404 (the task has no console), 501 or 503 means no console was reached; a
	// broker that wrote nothing served nothing.
	if status := recorder.Status(); status == 0 || status >= http.StatusBadRequest {
		s.recordAttachAudit(r, resource, "error")
		return
	}

	s.recordAttachAudit(r, resource, "ok")
}

// recordAttachAudit writes one task.attach record. It never carries console
// bytes: the action and the task identity are the record.
func (s *Server) recordAttachAudit(r *http.Request, resource v1.AuditResource, result string) {
	_ = s.recordAudit(r, attachAuditAction, resource, result)
}
