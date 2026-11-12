package apihttp

// This file owns the sandbox routes: the node's sandbox control plane as the
// programmatic API reaches it (pkg/sandbox).
//
// The shape is the one every other resource has — a list, a single document, a
// create, a delete — plus two operations only a sandbox has: exec in one, and mint a
// credential scoped to one. Exec is the API's streaming shape (application/x-ndjson,
// the same frames as POST /v1/tasks/{id}/exec), because a caller that can follow a
// task's exec stream should not learn a second one.
//
// # Who may do what
//
// The routes take sandboxes:read, sandboxes:write, sandboxes:exec and
// sandboxes:token. On top of the scope, the two operations that reach inside one
// sandbox — exec and removal — ask the caller's sandbox selectors (Identity.
// AuthorizeSandbox), so a token minted for sandbox A cannot exec in sandbox B even
// though it holds sandboxes:exec. A refusal is a 403 whose details.reason says which
// of the two things went wrong (a selector that names other sandboxes, or a runner
// token with no selector at all), and never a silent success.

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox"
)

// Sandboxes is the node's sandbox control plane as this package reaches it. It is an
// interface for the same reason Broker is: the HTTP layer depends on the behaviour,
// not on pkg/sandbox's construction, and a test can serve the routes without a KVM.
//
// *sandbox.Manager is the production implementation, and it is the same value the
// node's reaper runs against — so an exec that the API serves is an exec the reaper
// counts as activity, and a sandbox removed over the API is one the reaper no longer
// sees.
type Sandboxes interface {
	// List returns every sandbox on the node, plus the record files that could not
	// be read at all.
	List() ([]sandbox.Record, []string, error)
	// Get returns one sandbox, or a sandbox.ErrNotFound failure.
	Get(id string) (sandbox.Record, error)
	// Create builds one sandbox and returns it once its guest agent has answered.
	Create(ctx context.Context, req sandbox.CreateRequest) (sandbox.Record, error)
	// Remove ends one sandbox and frees everything it holds.
	Remove(ctx context.Context, id string) (sandbox.Freed, error)
	// ServeExec serves one streaming exec request against a sandbox. An error it
	// returns is the pre-flight (no such sandbox, not ready); everything after the
	// dial is written to w.
	ServeExec(ctx context.Context, id string, w http.ResponseWriter, r *http.Request) error
}

// sandboxAuditAction is the audit action recorded for a sandbox's exec.
const sandboxAuditAction = "sandbox.exec"

// sandboxCapability is the advertised capability name of the sandbox control plane:
// the version document lists it exactly when this node serves it, so a client can
// tell "no sandboxes here" from "a sandbox route that answers 501".
const sandboxCapability = "sandboxes"

// listSandboxes serves GET /v1/sandboxes.
//
// A record file that cannot be read is a warning, not a failure: one bad file must
// not hide the node's other sandboxes, and a listing that silently dropped it would
// be worse than one that says so.
func (s *Server) listSandboxes(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}

	records, skipped, err := s.cfg.Sandboxes.List()
	if err != nil {
		s.writeError(w, r, sandboxAPIError(err))
		return
	}

	doc := v1.NewSandboxList(sandboxDocuments(records))
	for _, path := range skipped {
		doc.Warnings = append(doc.Warnings, v1.Warning{
			Code:    "sandbox_record_unreadable",
			Message: "a sandbox record could not be read and is not listed",
			// The file name, not its path: a listing is a read a token holder can
			// ask for, and where a node keeps its records is not its business.
			Details: map[string]any{"file": filepath.Base(path)},
		})
	}
	s.writeJSON(w, r, http.StatusOK, doc)
}

// createSandbox serves POST /v1/sandboxes.
func (s *Server) createSandbox(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}

	var req v1.SandboxCreate
	if status, err := readJSONWrite(w, r, &req); err != nil {
		s.writeErrorStatus(w, r, status, sandboxAPIError(err))
		return
	}
	// 0 is "the node's default", -1 is "never" and anything else negative is a
	// mistake: the two meanings a caller needs must not collapse into one value.
	if req.IdleTTLSeconds < -1 {
		s.writeError(w, r, output.NewError(output.CodeInvalid,
			"idleTtlSeconds must be zero (the node's default), -1 (never) or positive").
			WithDetails(map[string]any{"reason": "invalid_ttl"}))
		return
	}

	rec, err := s.cfg.Sandboxes.Create(r.Context(), sandbox.CreateRequest{
		Template: req.Template,
		ID:       req.ID,
		Owner:    req.Owner,
		Allow:    req.Egress,
		TTL:      time.Duration(req.IdleTTLSeconds) * time.Second,
		VCPUs:    req.VCPUs,
		MemoryMB: req.MemoryMB,
	})
	if err != nil {
		s.recordSandboxAudit(r, "sandbox.create", req.ID, "error")
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.recordSandboxAudit(r, "sandbox.create", rec.ID, "ok")

	// 201 with the document: the sandbox exists and is ready (the create returns only
	// once the guest agent answered), which is what the status says.
	s.writeJSON(w, r, http.StatusCreated, v1.NewSandboxDocument(sandboxDocument(rec)))
}

// getSandbox serves GET /v1/sandboxes/{id}.
func (s *Server) getSandbox(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}

	rec, err := s.cfg.Sandboxes.Get(r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.writeJSON(w, r, http.StatusOK, v1.NewSandboxDocument(sandboxDocument(rec)))
}

// removeSandbox serves DELETE /v1/sandboxes/{id}.
func (s *Server) removeSandbox(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}

	id := r.PathValue("id")
	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeSandbox(id); err != nil {
		s.recordSandboxAudit(r, "sandbox.remove", id, "error")
		s.writeError(w, r, err)
		return
	}

	freed, err := s.cfg.Sandboxes.Remove(r.Context(), id)
	if err != nil {
		s.recordSandboxAudit(r, "sandbox.remove", id, "error")
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.recordSandboxAudit(r, "sandbox.remove", id, "ok")
	s.writeJSON(w, r, http.StatusOK, v1.NewSandboxRemoval(id, sandboxFreedDocument(freed)))
}

// execSandbox serves POST /v1/sandboxes/{id}/exec.
//
// The response is the exec stream (application/x-ndjson), so nothing here may write
// a document once the handler has been handed the request. The pre-flight errors —
// a token meant for another sandbox, a sandbox that is not ready — happen before
// anything is written, which is what keeps them typed documents.
func (s *Server) execSandbox(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}

	id := r.PathValue("id")
	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeSandbox(id); err != nil {
		s.recordSandboxAudit(r, sandboxAuditAction, id, "error")
		s.writeError(w, r, err)
		return
	}

	if status, err := requireContentType(r, MediaType); err != nil {
		s.recordSandboxAudit(r, sandboxAuditAction, id, "error")
		s.writeErrorStatus(w, r, status, err)
		return
	}

	// Carry the public request ID on the outbound call so a failure inside the node's
	// exec handler can name it, exactly as the task route does.
	r.Header.Set(requestIDHeader, requestIDFrom(r))

	recorder := &relayStatusRecorder{ResponseWriter: w}
	if err := s.cfg.Sandboxes.ServeExec(r.Context(), id, recorder, r); err != nil {
		s.recordSandboxAudit(r, sandboxAuditAction, id, "error")
		s.writeError(w, r, sandboxAPIError(err))
		return
	}

	// The stream was served only when the guest was reached. A non-2xx the handler
	// wrote itself means the command never ran.
	if status := recorder.Status(); status == 0 || status >= http.StatusBadRequest {
		s.recordSandboxAudit(r, sandboxAuditAction, id, "error")
		return
	}
	s.recordSandboxAudit(r, sandboxAuditAction, id, "ok")
}

// createSandboxToken serves POST /v1/sandboxes/{id}/tokens: a token scoped to one
// sandbox.
//
// The token it mints carries exactly what a caller needs to use one sandbox and
// nothing else: sandboxes:read and sandboxes:exec, narrowed by a sandbox selector that
// names this sandbox. It cannot exec in another sandbox (the selector), cannot create
// or remove a sandbox (no sandboxes:write) and cannot mint another token (no
// sandboxes:token), and each of those refusals says so.
func (s *Server) createSandboxToken(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}
	if s.cfg.Tokens == nil {
		s.writeError(w, r, output.NewError(output.CodeNotImplemented,
			"this node has no token store configured").WithDetails(map[string]any{"capability": "tokens"}))
		return
	}

	id := r.PathValue("id")
	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeSandbox(id); err != nil {
		s.writeError(w, r, err)
		return
	}

	// A token for a sandbox this node does not have would be a credential for
	// nothing, and would be inherited by the next sandbox minted with the same ID.
	if _, err := s.cfg.Sandboxes.Get(id); err != nil {
		s.writeError(w, r, sandboxAPIError(err))
		return
	}

	var req v1.SandboxTokenRequest
	if status, err := readJSONWrite(w, r, &req); err != nil {
		s.writeErrorStatus(w, r, status, sandboxAPIError(err))
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "sandbox " + id
	}

	secret, tok, err := s.cfg.Tokens.CreateWithOptions(name, RoleRunner, TokenOptions{
		Scopes:           []string{string(ScopeSandboxesRead), string(ScopeSandboxesExec)},
		SandboxSelectors: &SandboxSelectors{SandboxIDs: []string{id}},
		ExpiresAt:        req.ExpiresAt,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusCreated, v1.NewSandboxToken(tok.ID, tok.Name, id, secret, tok.ExpiresAt, tok.Scopes))
}

// sandboxesAvailable answers 501 when this node serves no sandbox control plane, and
// reports whether the caller may continue. A route that exists but has no manager
// behind it is a capability, not a bug, and saying so is what the version document's
// capability list promises.
func (s *Server) sandboxesAvailable(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.Sandboxes != nil {
		return true
	}
	s.writeError(w, r, output.NewError(output.CodeNotImplemented,
		"this node has no sandbox control plane configured").
		WithDetails(map[string]any{"capability": "sandboxes"}))
	return false
}

// recordSandboxAudit records one sandbox operation, if this node keeps an audit log.
// It is best effort for the same reason every other audit call is: an audit failure
// must not change the outcome of the operation it describes.
func (s *Server) recordSandboxAudit(r *http.Request, action, id, result string) {
	_ = s.recordAudit(r, action, v1.AuditResource{Kind: v1.KindSandbox, ID: id}, result)
}

// sandboxDocuments maps records to the wire documents.
func sandboxDocuments(records []sandbox.Record) []v1.Sandbox {
	out := make([]v1.Sandbox, 0, len(records))
	for _, rec := range records {
		out = append(out, sandboxDocument(rec))
	}
	return out
}

// sandboxDocument is the read model of one sandbox. Host layout — the jail, the disk,
// the token path — stays on the node: what crosses the API is what a caller acts on,
// which is where the sandbox answers and what it may reach.
func sandboxDocument(rec sandbox.Record) v1.Sandbox {
	return v1.Sandbox{
		ID:             rec.ID,
		State:          string(rec.State),
		Template:       rec.Template,
		Owner:          rec.Owner,
		CreatedAt:      formatInstant(rec.Created),
		UpdatedAt:      formatInstant(rec.Updated),
		ReadyAt:        formatInstant(rec.ReadyAt),
		LastActiveAt:   formatInstant(rec.LastActive),
		IdleTTLSeconds: rec.IdleTTLSeconds,
		Reason:         rec.Reason,
		GuestIP:        rec.GuestIP,
		MAC:            rec.MAC,
		Egress:         rec.Egress,
		DiskCopy:       rec.DiskCopy,
		AgentVersion:   rec.AgentVersion,
		BootID:         rec.BootID,
	}
}

// sandboxFreedDocument maps a teardown report to the wire document.
func sandboxFreedDocument(freed sandbox.Freed) v1.SandboxFreed {
	return v1.SandboxFreed{
		ProcessStopped:         freed.Process,
		SocketRemoved:          freed.Socket != "",
		CgroupRemoved:          freed.Cgroup != "",
		JailRemoved:            freed.JailRoot != "",
		NetNSRemoved:           freed.NetNS != "",
		TAPRemoved:             freed.TAP != "",
		DiskRemoved:            freed.Disk != "",
		AgentCredentialRemoved: freed.TokenFile != "",
		InstanceRecordRemoved:  freed.InstanceRecord,
		RecordRemoved:          freed.Record,
		APITokensRevoked:       freed.APITokens,
	}
}

// formatInstant renders a node clock reading as the contract's timestamp, or "" for
// the zero time — which is how "not yet" is said.
func formatInstant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// sandboxAPIError maps pkg/sandbox's failures onto the stable taxonomy. A failure
// that already carries a code keeps it; the sentinels become the status a caller can
// act on; a template's own refusal (a pin mismatch, an agent this host cannot drive)
// is reported with the code the template package defined, not as an internal error.
func sandboxAPIError(err error) error {
	if err == nil {
		return nil
	}
	var typed *output.Error
	if errors.As(err, &typed) {
		return typed
	}

	switch {
	case errors.Is(err, sandbox.ErrNotFound):
		return output.NewError(output.CodeNotFound, err.Error())
	case errors.Is(err, sandbox.ErrExists):
		return output.NewError(output.CodeConflict, err.Error())
	case errors.Is(err, sandbox.ErrNotReady):
		return output.NewError(output.CodeConflict, err.Error())
	case errors.Is(err, sandbox.ErrInvalid):
		return output.NewError(output.CodeInvalid, err.Error())
	}

	// A template's refusal carries its own stable code (pkg/golden). Reading it from
	// the error rather than matching every type keeps the template package free to
	// add one.
	var coded interface{ Code() string }
	if errors.As(err, &coded) {
		return output.NewError(output.Code(coded.Code()), err.Error())
	}

	return err
}
