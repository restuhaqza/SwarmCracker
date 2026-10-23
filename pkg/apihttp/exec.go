// This file owns POST /v1/tasks/{ref}/exec: the public non-TTY exec path.
//
// The handler is deliberately thin. It validates the request against the
// contract, resolves the task reference, enforces the caller's task selectors,
// audits the attempt and hands the request to the broker manager, which proxies
// it to the node that runs the task. The response is application/x-ndjson (the
// documented exception to the v1 JSON envelope) and is never buffered here.
package apihttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// maxExecBodyBytes bounds one exec request body.
	maxExecBodyBytes = 1 << 20
	// execOperation is the broker operation name for exec.
	execOperation = "exec"
	// execAuditAction is the audit action recorded for every attempt.
	execAuditAction = "task.exec"
)

// execTask serves POST /v1/tasks/{ref}/exec.
func (s *Server) execTask(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("id")

	if s.cfg.Broker == nil {
		s.recordExecAudit(r, v1.AuditResource{Kind: v1.KindTask, Name: ref}, "error")
		s.writeError(w, r, output.NewError(output.CodeNotImplemented,
			"this manager has no node broker configured").
			WithDetails(map[string]any{"capability": execOperation}))
		return
	}

	if status, err := requireContentType(r, MediaType); err != nil {
		s.recordExecAudit(r, v1.AuditResource{Kind: v1.KindTask, Name: ref}, "error")
		s.writeErrorStatus(w, r, status, err)
		return
	}

	if status, err := parseExecRequest(w, r); err != nil {
		s.recordExecAudit(r, v1.AuditResource{Kind: v1.KindTask, Name: ref}, "error")
		s.writeErrorStatus(w, r, status, err)
		return
	}

	// Resolve the reference server-side: the broker only ever receives a
	// concrete task ID, never user input.
	task, err := s.cfg.Client.GetTask(r.Context(), ref)
	if err != nil {
		s.recordExecAudit(r, v1.AuditResource{Kind: v1.KindTask, Name: ref}, "error")
		s.writeError(w, r, err)
		return
	}
	resource := v1.AuditResource{Kind: v1.KindTask, ID: task.ID, Name: task.Name}

	labels, err := s.execServiceLabels(r.Context(), task)
	if err != nil {
		s.recordExecAudit(r, resource, "error")
		s.writeError(w, r, err)
		return
	}

	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeTask(task.ServiceID, task.ID, labels); err != nil {
		s.recordExecAudit(r, resource, "error")
		s.writeError(w, r, err)
		return
	}

	// Carry the public request ID on the outbound call so a relayed node error
	// can name it.
	r.Header.Set(requestIDHeader, requestIDFrom(r))

	// Wrap the writer so the relayed status is observable: Proxy reports only a
	// resolve failure, so a node 4xx/5xx would otherwise be audited as success.
	recorder := &execStatusRecorder{ResponseWriter: w}
	if err := s.cfg.Broker.Proxy(r.Context(), recorder, r, task.ID, execOperation); err != nil {
		// A resolve failure happens before anything is written, so it is still
		// safe to answer with a typed document.
		s.recordExecAudit(r, resource, "error")
		s.writeError(w, r, err)
		return
	}

	// The stream was served only when the node accepted the exec. A relayed
	// 4xx/5xx (a node 503 for an unavailable guest agent, a relayed 404 or 501)
	// means the command never ran; a broker that wrote nothing served nothing.
	if status := recorder.Status(); status == 0 || status >= http.StatusBadRequest {
		s.recordExecAudit(r, resource, "error")
		return
	}

	s.recordExecAudit(r, resource, "ok")
}

// execStatusRecorder records the status the broker relayed while passing every
// write straight through. It is deliberately not a buffer: the response is an
// NDJSON stream that is flushed frame by frame.
//
// Unwrap exposes the underlying writer so http.NewResponseController finds its
// Flush (and any other optional interface); without it the wrapper would hide
// streaming from the reverse proxy.
type execStatusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

// WriteHeader records the first final status and forwards it. Informational
// responses (1xx except 101 Switching Protocols) precede the final one and must
// not latch the recorded outcome.
func (r *execStatusRecorder) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		r.ResponseWriter.WriteHeader(status)
		return
	}
	if !r.wrote {
		r.status = status
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(status)
}

// Write records the implicit 200 of a handler that writes without a header and
// forwards the bytes unchanged.
func (r *execStatusRecorder) Write(data []byte) (int, error) {
	if !r.wrote {
		r.status = http.StatusOK
		r.wrote = true
	}
	return r.ResponseWriter.Write(data)
}

// Unwrap returns the wrapped writer for http.ResponseController.
func (r *execStatusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Status is the relayed HTTP status, or 0 when nothing was written.
func (r *execStatusRecorder) Status() int { return r.status }

// parseExecRequest reads and validates one exec body, restoring the body for
// the proxy. The returned status is the HTTP status for a failure the taxonomy
// cannot infer (400/413/415).
func parseExecRequest(w http.ResponseWriter, r *http.Request) (int, error) {
	var req v1.ExecRequest

	r.Body = http.MaxBytesReader(w, r.Body, maxExecBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return http.StatusRequestEntityTooLarge,
				output.NewErrorf(output.CodeInvalid, "the request body exceeds %d bytes", maxExecBodyBytes).
					WithDetails(map[string]any{"reason": "payload_too_large", "limit": maxExecBodyBytes})
		}
		return http.StatusBadRequest,
			output.NewErrorf(output.CodeInvalid, "read request body: %v", err)
	}
	// Restore the bytes the caller sent: the broker forwards this body to the
	// node, so it must be the one that was validated.
	r.Body = io.NopCloser(bytes.NewReader(body))

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return http.StatusBadRequest,
			output.NewErrorf(output.CodeInvalid, "invalid JSON body: %v", err).
				WithDetails(map[string]any{"reason": "invalid_body"})
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"the request body must contain exactly one JSON object")
	}

	return validateExecRequest(req)
}

// validateExecRequest enforces the contract's exec rules. The stdin rule is
// checked before the TTY rule, so `stdin:true` without a TTY reports the reason
// that explains the rejected request rather than the follow-up limitation.
func validateExecRequest(req v1.ExecRequest) (int, error) {
	switch {
	case len(req.Command) == 0:
		return http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"command must not be empty")
	case req.Stdin && !req.TTY:
		return http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"stdin requires tty").
			WithDetails(map[string]any{"reason": "stdin_requires_tty"})
	case req.TTY:
		// Interactive exec is a WebSocket follow-up. The contract still defines
		// the request, so the answer is a typed 501 rather than a 400.
		return http.StatusNotImplemented, output.NewError(output.CodeNotImplemented,
			"interactive exec over the API is not implemented").
			WithDetails(map[string]any{"reason": "tty_not_supported"})
	case req.TimeoutMs < 0:
		return http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"timeoutMs must be zero or positive")
	}

	for key := range req.Env {
		if key == "" || strings.Contains(key, "=") {
			return http.StatusBadRequest, output.NewErrorf(output.CodeInvalid,
				"invalid environment variable name %q", key).
				WithDetails(map[string]any{"reason": "invalid_env"})
		}
	}
	return 0, nil
}

// execServiceLabels returns the labels of the task's service. A task without a
// service has none. A missing service is not an error: a selector that matches
// on task ID still works, while one that needs labels fails closed because the
// label map is empty. Any other lookup failure is returned.
func (s *Server) execServiceLabels(ctx context.Context, task v1.Task) (map[string]string, error) {
	if task.ServiceID == "" {
		return nil, nil
	}

	service, err := s.cfg.Client.GetService(ctx, task.ServiceID)
	if err != nil {
		if typedError(err).Code == output.CodeNotFound {
			return nil, nil
		}
		return nil, err
	}
	return service.Labels, nil
}

// recordExecAudit writes one task.exec record. It never carries the command or
// the environment: the action and the task identity are the record.
func (s *Server) recordExecAudit(r *http.Request, resource v1.AuditResource, result string) {
	_ = s.recordAudit(r, execAuditAction, resource, result)
}
