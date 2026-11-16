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
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
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
	// Checkpoint takes a consistent checkpoint of one sandbox under name.
	Checkpoint(ctx context.Context, id, name string) (sandbox.CheckpointRecord, error)
	// Checkpoints returns the node's checkpoints, narrowed to one sandbox when
	// sandboxID is not empty, plus the records that could not be read.
	Checkpoints(sandboxID string) ([]sandbox.CheckpointRecord, []string, error)
	// CheckpointByName returns one checkpoint by its node-unique name.
	CheckpointByName(name string) (sandbox.CheckpointRecord, error)
	// DeleteCheckpoint forgets one checkpoint, and reports what that freed and what
	// the node had to keep.
	DeleteCheckpoint(ctx context.Context, name string) (sandbox.CheckpointDeletion, error)
	// Fork creates clones from a checkpoint.
	Fork(ctx context.Context, req sandbox.ForkRequest) ([]sandbox.Record, error)
	// ForkFromSandbox takes a fresh checkpoint of a running sandbox and forks it.
	ForkFromSandbox(ctx context.Context, sandboxID string, req sandbox.ForkRequest) ([]sandbox.Record, error)
	// ServeExec serves one streaming exec request against a sandbox. An error it
	// returns is the pre-flight (no such sandbox, not ready); everything after the
	// dial is written to w.
	ServeExec(ctx context.Context, id string, w http.ResponseWriter, r *http.Request) error
	// DescribeFile reports what one guest path is, so a caller can choose the
	// operation a copy of it takes before that copy's response starts.
	DescribeFile(ctx context.Context, id, guestPath string) (sandbox.File, error)
	// SendFile streams one guest path into w: a regular file's bytes, or a tar
	// archive for a directory.
	SendFile(ctx context.Context, id, guestPath string, kind sandbox.FileKind, w io.Writer) (agent.TarSummary, error)
	// PutFile streams r into one guest path as a regular file, and reports how many
	// bytes the guest wrote. Zero mode means the guest's default.
	PutFile(ctx context.Context, id, guestPath string, mode uint32, r io.Reader) (int64, error)
	// ExtractTree streams the tar archive r carries into the tree one guest path
	// names. A template whose agent predates the operation refuses it with
	// sandbox.ErrMissingCapability.
	ExtractTree(ctx context.Context, id, guestPath string, r io.Reader) (agent.TarSummary, error)
	// Status reports what one sandbox is, from the guest's own answer about itself
	// rather than from parsing a command's output.
	Status(ctx context.Context, id string) (sandbox.Status, error)
	// ServerLogPath is where one sandbox's serial console is written, or an error
	// saying why there is none: a sandbox this node does not have, one that has
	// written none yet, or a node that records no console at all.
	ServerLogPath(id string) (string, error)
	// Expose serves one guest port of a ready sandbox on this node's host, and
	// returns the exposure with the token that opens it (pkg/sandbox/preview.go).
	Expose(ctx context.Context, id string, guestPort uint16, ttl time.Duration) (sandbox.Exposure, error)
	// Unexpose stops serving one exposed guest port.
	Unexpose(ctx context.Context, id string, guestPort uint16) error
}

// sandboxAuditAction is the audit action recorded for a sandbox's exec.
const sandboxAuditAction = "sandbox.exec"

// sandboxExposeAuditAction is the audit action recorded for exposing and un-exposing a
// guest port: both are one change to what the node serves, on one sandbox.
const sandboxExposeAuditAction = "sandbox.expose"

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

	// A credential bound to specific sandboxes may not read the node's inventory of them.
	// The list is every sandbox on the node — other tenants' included — and a token minted
	// for one sandbox is not a reader of the node: the acceptance rule is that such a token
	// cannot list sandboxes, and a list filtered to its own would still answer "what else is
	// here" one request at a time. Its own sandbox stays readable by id, and its status,
	// console and file routes answer for it; an operator or admin token with no sandbox
	// selector lists as before, and so does the local socket.
	if identity, ok := IdentityFrom(r.Context()); ok && !identity.Sandboxes.Empty() {
		s.writeError(w, r, output.NewError(output.CodeForbidden,
			"this credential is bound to specific sandboxes, and the node's sandbox list is not its to read").
			WithDetails(map[string]any{
				"reason":     "sandbox_list_not_permitted",
				"sandboxIds": identity.Sandboxes.SandboxIDs,
			}))
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
		// A create that got far enough to write a record leaves it failed with the
		// reason; that record is a sandbox that exists and is unusable, which is what
		// a watcher needs to see. A create refused before the record existed — a
		// quota, a template this host cannot boot — leaves nothing, so there is
		// nothing to announce.
		if rec.ID != "" && rec.State == sandbox.StateFailed {
			s.publishSandboxEvent(sandbox.ChangeFailed, rec)
		}
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.recordSandboxAudit(r, "sandbox.create", rec.ID, "ok")
	s.publishSandboxEvent(sandbox.ChangeCreated, rec)

	// 201 with the document: the sandbox exists and is ready (the create returns only
	// once the guest agent answered), which is what the status says.
	s.writeJSON(w, r, http.StatusCreated, v1.NewSandboxDocument(sandboxDocument(rec)))
}

// getSandbox serves GET /v1/sandboxes/{id}.
func (s *Server) getSandbox(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}

	// A credential bound to specific sandboxes reads those and no others. The check runs
	// before the lookup, so a bound token asking about another sandbox gets the same 403
	// whether or not that sandbox exists — a refusal must not confirm what is on the node.
	id := r.PathValue("id")
	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeSandboxRead(id); err != nil {
		s.writeError(w, r, err)
		return
	}

	rec, err := s.cfg.Sandboxes.Get(id)
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
	// The event names the sandbox that was removed. It carries no object: the record is
	// deleted by the time this runs, and a sandbox's id is its whole identity — it has no
	// name and no labels — so there is nothing a watcher could learn from a document it
	// could no longer read back.
	s.publishSandboxRemoval(id)
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
//
// It deliberately does not hold sandboxes:files, even though copying a file into a
// sandbox is the same power as exec'ing in it: adding a capability to a credential that
// can leak is a decision that needs a user of it, not a symmetry argument, and no caller
// has needed a sandbox-scoped token to copy yet. The boundary is written down here and in
// the sandboxes guide so it reads as a decision rather than as an omission; a caller that
// needs it today uses a token that holds sandboxes:files (runner and up) or the local
// socket, and minting one with it is a one-line change when something needs it.
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
	rec, err := s.cfg.Sandboxes.Get(id)
	if err != nil {
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

	// The token cannot outlive the sandbox it is for: a credential for a sandbox the reaper
	// has taken back is a key to nothing, and a caller that asked for an expiry past the
	// sandbox's own deadline is told so rather than given one that will not be honoured.
	expiresAt, err := sandboxTokenExpiry(rec, req.ExpiresAt, time.Now())
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	secret, tok, err := s.cfg.Tokens.CreateWithOptions(name, RoleRunner, TokenOptions{
		Scopes:           []string{string(ScopeSandboxesRead), string(ScopeSandboxesExec)},
		SandboxSelectors: &SandboxSelectors{SandboxIDs: []string{id}},
		ExpiresAt:        expiresAt,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusCreated, v1.NewSandboxToken(tok.ID, tok.Name, id, secret, tok.ExpiresAt, tok.Scopes))
}

// exposeSandboxPort serves POST /v1/sandboxes/{id}/ports: one guest port of this sandbox,
// served on the node's host through the preview proxy.
//
// The token the response carries is the only time it exists outside the proxy's memory:
// the node keeps a digest of it, so the exposure's URL has to be kept by the caller that
// asked for it.
func (s *Server) exposeSandboxPort(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}

	id := r.PathValue("id")
	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeSandbox(id); err != nil {
		s.recordSandboxAudit(r, sandboxExposeAuditAction, id, "error")
		s.writeError(w, r, err)
		return
	}

	var req v1.SandboxPortRequest
	if status, err := readJSONWrite(w, r, &req); err != nil {
		s.writeErrorStatus(w, r, status, sandboxAPIError(err))
		return
	}
	// 0 is "the node's default", -1 is "never" and anything else negative is a mistake,
	// exactly as an idle TTL reads.
	if req.TTLSeconds < -1 {
		s.writeError(w, r, output.NewError(output.CodeInvalid,
			"ttlSeconds must be zero (the node's default), -1 (never) or positive").
			WithDetails(map[string]any{"reason": "invalid_ttl"}))
		return
	}
	if req.GuestPort == 0 {
		s.writeError(w, r, output.NewError(output.CodeInvalid, "guestPort is required").
			WithDetails(map[string]any{"reason": "invalid_port"}))
		return
	}

	exposure, err := s.cfg.Sandboxes.Expose(r.Context(), id, req.GuestPort, time.Duration(req.TTLSeconds)*time.Second)
	if err != nil {
		s.recordSandboxAudit(r, sandboxExposeAuditAction, id, "error")
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.recordSandboxAudit(r, sandboxExposeAuditAction, id, "ok")

	s.writeJSON(w, r, http.StatusCreated, v1.NewSandboxPortExposure(id, sandboxPortDocument(exposure.ExposedPort), exposure.Token, exposure.URL))
}

// unexposeSandboxPort serves DELETE /v1/sandboxes/{id}/ports/{port}: the port stops being
// served, and the sandbox's own table stops accepting it.
func (s *Server) unexposeSandboxPort(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}

	id := r.PathValue("id")
	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeSandbox(id); err != nil {
		s.recordSandboxAudit(r, sandboxExposeAuditAction, id, "error")
		s.writeError(w, r, err)
		return
	}

	port, err := strconv.ParseUint(r.PathValue("port"), 10, 16)
	if err != nil || port == 0 {
		s.recordSandboxAudit(r, sandboxExposeAuditAction, id, "error")
		s.writeError(w, r, output.NewErrorf(output.CodeInvalid, "port %q is not a port", r.PathValue("port")).
			WithDetails(map[string]any{"reason": "invalid_port"}))
		return
	}

	if err := s.cfg.Sandboxes.Unexpose(r.Context(), id, uint16(port)); err != nil {
		s.recordSandboxAudit(r, sandboxExposeAuditAction, id, "error")
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.recordSandboxAudit(r, sandboxExposeAuditAction, id, "ok")

	rec, err := s.cfg.Sandboxes.Get(id)
	if err != nil {
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.writeJSON(w, r, http.StatusOK, v1.NewSandboxDocument(sandboxDocument(rec)))
}

// sandboxTokenExpiry bounds a token's life by the sandbox it is bound to, and returns the
// expiry to store.
//
// The sandbox's idle TTL is the only deadline the node has for it: a sandbox nothing uses
// again is taken back at LastActive+TTL, so a credential for it must not be good for
// longer. The rule has two halves because both are what a caller means:
//
//   - an expiry the caller named is refused when it outlives the sandbox, because a request
//     the node cannot honour is answered rather than silently shortened;
//   - a token with no expiry gets the sandbox's own deadline, because "never expires" for a
//     credential bound to a sandbox that will is a claim about the sandbox that is not true.
//
// A sandbox with no TTL never idles out, so nothing bounds its tokens; removing it still
// revokes them (the manager revokes the tokens scoped to a sandbox when it frees it), and a
// token past its own expiry is refused by the store, so the two halves together are what
// keeps a credential from outliving what it authenticates.
func sandboxTokenExpiry(rec sandbox.Record, requested string, now time.Time) (string, error) {
	ttl := rec.TTL()
	if ttl <= 0 {
		return strings.TrimSpace(requested), nil
	}

	// The token store keeps an expiry at RFC3339's one-second resolution, so the deadline is
	// rounded down: an expiry is then never later than the sandbox's own. A deadline already
	// at or before now rounds to an instant in the past, which the store refuses — that case
	// is the next whole second, which is the honest answer for a sandbox the reaper is about
	// to take back.
	deadline := rec.LastActive.Add(ttl).Truncate(time.Second)
	if !deadline.After(now) {
		deadline = now.Truncate(time.Second).Add(time.Second)
	}

	requested = strings.TrimSpace(requested)
	if requested == "" {
		return deadline.UTC().Format(time.RFC3339), nil
	}

	asked, err := parseTokenExpiry(requested)
	if err != nil {
		return "", err
	}
	if asked.After(deadline) {
		return "", output.NewErrorf(output.CodeInvalid,
			"sandbox %s is taken back at %s if nothing uses it, so a token for it cannot expire at %s",
			rec.ID, deadline.UTC().Format(time.RFC3339), asked.UTC().Format(time.RFC3339)).
			WithDetails(map[string]any{
				"reason":           "token_outlives_sandbox",
				"sandboxExpiresAt": deadline.UTC().Format(time.RFC3339),
			})
	}
	return requested, nil
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
		Ports:          sandboxPortDocuments(rec.Ports),
		AgentVersion:   rec.AgentVersion,
		BootID:         rec.BootID,
	}
}

// sandboxPortDocuments maps a record's exposures to the read model. The address, the port
// and the deadline cross; the token's digest does not, because it is a credential and the
// document is not.
func sandboxPortDocuments(ports []sandbox.ExposedPort) []v1.SandboxPort {
	if len(ports) == 0 {
		return nil
	}
	out := make([]v1.SandboxPort, 0, len(ports))
	for _, p := range ports {
		out = append(out, sandboxPortDocument(p))
	}
	return out
}

// sandboxPortDocument is one exposed port as the API reports it.
func sandboxPortDocument(p sandbox.ExposedPort) v1.SandboxPort {
	return v1.SandboxPort{
		GuestPort: p.GuestPort,
		Address:   p.Host(),
		URL:       p.URL(),
		ExpiresAt: formatInstant(p.ExpiresAt),
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
		PreviewsClosed:         freed.Previews,
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

	// A refused admission is its own code and its own status, with the numbers a
	// caller acts on: which limit, what it allows, what was in use, and when asking
	// again is worth it. It is answered before the sandbox exists, so there is nothing
	// for the caller to clean up.
	var quota *sandbox.QuotaError
	if errors.As(err, &quota) {
		details := map[string]any{
			"resource":     quota.Resource,
			"limit":        quota.Limit,
			"used":         quota.Used,
			"requested":    quota.Requested,
			"retryAfterMs": retryAfterMillis(quota.RetryAfter),
		}
		if quota.Owner != "" || quota.Resource == sandbox.QuotaResourceOwnerSandboxes {
			details["owner"] = quota.Owner
		}
		return output.NewError(output.CodeQuotaExceeded, quota.Error()).
			WithDetails(details).
			WithRetryAfter(quota.RetryAfter)
	}

	switch {
	case errors.Is(err, sandbox.ErrMissingCapability):
		// The template's agent does not advertise the operation. It is a property of the
		// template, not of this node: a single file still arrives through the operation
		// that predates it, so the answer names the capability and leaves the caller a
		// way forward.
		missing := &sandbox.MissingCapabilityError{}
		errors.As(err, &missing)
		return output.NewError(output.CodeNotImplemented, err.Error()).
			WithDetails(map[string]any{"reason": "template_capability_missing", "capability": missing.Capability})
	case errors.Is(err, sandbox.ErrNotImplemented):
		// Configured without checkpoints: a capability this node does not serve, said so
		// explicitly. It must not surface as an internal error, and it must not look like
		// a store that broke.
		return output.NewError(output.CodeNotImplemented,
			"this node serves no sandbox checkpoints").
			WithDetails(map[string]any{"reason": "checkpoints_unavailable", "capability": "sandboxes:checkpoints"})
	case errors.Is(err, sandbox.ErrNotFound):
		return output.NewError(output.CodeNotFound, err.Error())
	case errors.Is(err, sandbox.ErrExists):
		return output.NewError(output.CodeConflict, err.Error())
	case errors.Is(err, sandbox.ErrNotReady):
		return output.NewError(output.CodeConflict, err.Error())
	case errors.Is(err, sandbox.ErrInvalid):
		return output.NewError(output.CodeInvalid, err.Error())
	case errors.Is(err, sandbox.ErrExposed):
		return output.NewError(output.CodeConflict, err.Error())
	case errors.Is(err, sandbox.ErrNotExposed):
		return output.NewError(output.CodeNotFound, err.Error())
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

// The checkpoint routes: taking an image of a sandbox, reading one, forking from one and
// forgetting one.
//
// They are two families over one object. The sandbox-scoped routes (RFC §6) name the
// sandbox a checkpoint belongs to, because that is how a caller reaches it while the
// sandbox exists; the node-scoped routes address a checkpoint by its own name, because a
// checkpoint outlives the sandbox it was taken from and `keepCheckpoint=true` on a
// sandbox delete would otherwise leave images nothing could list, fork or free.
//
// A checkpoint's name is unique on the node, so a sandbox-scoped route that names a
// checkpoint belonging to another sandbox answers 404: the caller asked about a resource
// that is not there, not about one it may not touch — a 403 would confirm the name.

// checkpointDocument maps a record to the wire document. Host layout stays on the node.
func checkpointDocument(rec sandbox.CheckpointRecord) v1.Checkpoint {
	return v1.Checkpoint{
		Name:          rec.Name,
		Sandbox:       rec.Sandbox,
		Template:      rec.Template,
		Kernel:        rec.Kernel,
		CreatedAt:     formatInstant(rec.Created),
		VCPUs:         rec.VCPUs,
		MemoryMB:      rec.MemoryMB,
		Egress:        rec.Egress,
		QuiesceMode:   rec.Quiesce.Mode,
		QuiesceMounts: rec.Quiesce.Mounts,
		SizeBytes:     rec.SizeBytes,
		AgentVersion:  rec.AgentVersion,
		BootID:        rec.BootID,
	}
}

// checkpointDocuments maps a list, and is where a listing's warnings are attached.
func checkpointDocuments(records []sandbox.CheckpointRecord) []v1.Checkpoint {
	out := make([]v1.Checkpoint, 0, len(records))
	for _, rec := range records {
		out = append(out, checkpointDocument(rec))
	}
	return out
}

// warningsForCheckpoints renders the record files a listing could not read.
func warningsForCheckpoints(skipped []string) []v1.Warning {
	warnings := make([]v1.Warning, 0, len(skipped))
	for _, path := range skipped {
		warnings = append(warnings, v1.Warning{
			Code:    "checkpoint_record_unreadable",
			Message: "a checkpoint record could not be read and is not listed",
			Details: map[string]any{"file": filepath.Base(path)},
		})
	}
	return warnings
}

// listCheckpoints serves GET /v1/checkpoints: every checkpoint on this node.
func (s *Server) listCheckpoints(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}
	records, skipped, err := s.cfg.Sandboxes.Checkpoints("")
	if err != nil {
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	doc := v1.NewCheckpointList(checkpointDocuments(records))
	doc.Warnings = warningsForCheckpoints(skipped)
	s.writeJSON(w, r, http.StatusOK, doc)
}

// getCheckpoint serves GET /v1/checkpoints/{name}.
func (s *Server) getCheckpoint(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}
	rec, err := s.cfg.Sandboxes.CheckpointByName(r.PathValue("name"))
	if err != nil {
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.writeJSON(w, r, http.StatusOK, v1.NewCheckpointDocument(checkpointDocument(rec)))
}

// forkFromCheckpoint serves POST /v1/checkpoints/{name}/fork: clones of an image, whether
// or not the sandbox it came from still exists.
func (s *Server) forkFromCheckpoint(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}
	name := r.PathValue("name")
	if _, err := s.cfg.Sandboxes.CheckpointByName(name); err != nil {
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.fork(w, r, func(req sandbox.ForkRequest) ([]sandbox.Record, error) {
		req.Checkpoint = name
		return s.cfg.Sandboxes.Fork(r.Context(), req)
	})
}

// deleteCheckpoint serves DELETE /v1/checkpoints/{name}.
func (s *Server) deleteCheckpoint(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}
	name := r.PathValue("name")
	released, err := s.cfg.Sandboxes.DeleteCheckpoint(r.Context(), name)
	if err != nil {
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.recordSandboxAudit(r, "checkpoint.delete", name, "ok")
	s.writeJSON(w, r, http.StatusOK, v1.NewCheckpointRemovalWithFreed(name, v1.CheckpointFreed{
		StateBytes:    released.StateBytes,
		DiskBytes:     released.DiskBytes,
		ChunkBytes:    released.ChunkBytes,
		Chunks:        released.Chunks,
		FreedBytes:    released.FreedBytes,
		RetainedBytes: released.RetainedBytes,
		Retained:      released.Retained,
	}))
}

// createSandboxCheckpoint serves POST /v1/sandboxes/{id}/checkpoints.
func (s *Server) createSandboxCheckpoint(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}
	id := r.PathValue("id")
	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeSandbox(id); err != nil {
		s.recordSandboxAudit(r, "sandbox.checkpoint", id, "error")
		s.writeError(w, r, err)
		return
	}
	var req v1.CheckpointCreate
	if status, err := readJSONWrite(w, r, &req); err != nil {
		s.writeErrorStatus(w, r, status, sandboxAPIError(err))
		return
	}
	rec, err := s.cfg.Sandboxes.Checkpoint(r.Context(), id, req.Name)
	if err != nil {
		s.recordSandboxAudit(r, "sandbox.checkpoint", id, "error")
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.recordSandboxAudit(r, "sandbox.checkpoint", id, "ok")
	// 201: the images exist and are consistent (the checkpoint returns only once the
	// guest was frozen, the images taken and the guest released).
	s.writeJSON(w, r, http.StatusCreated, v1.NewCheckpointDocument(checkpointDocument(rec)))
}

// listSandboxCheckpoints serves GET /v1/sandboxes/{id}/checkpoints.
func (s *Server) listSandboxCheckpoints(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}
	id := r.PathValue("id")
	records, skipped, err := s.cfg.Sandboxes.Checkpoints(id)
	if err != nil {
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	doc := v1.NewCheckpointList(checkpointDocuments(records))
	doc.Warnings = warningsForCheckpoints(skipped)
	s.writeJSON(w, r, http.StatusOK, doc)
}

// restoreCheckpoint serves POST /v1/sandboxes/{id}/checkpoints/{name}/restore: one clone
// of a checkpoint that belongs to that sandbox. The sandbox need not still exist — a
// checkpoint is an image — but a checkpoint belonging to another sandbox is not this
// route's resource, so it is a 404.
func (s *Server) restoreCheckpoint(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}
	id, name := r.PathValue("id"), r.PathValue("name")
	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeSandbox(id); err != nil {
		s.writeError(w, r, err)
		return
	}
	rec, err := s.cfg.Sandboxes.CheckpointByName(name)
	if err != nil {
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	if rec.Sandbox != id {
		s.writeError(w, r, output.NewErrorf(output.CodeNotFound,
			"checkpoint %s belongs to sandbox %s, not %s", name, rec.Sandbox, id))
		return
	}
	s.fork(w, r, func(req sandbox.ForkRequest) ([]sandbox.Record, error) {
		req.Checkpoint, req.Count, req.IDs = name, 1, req.IDs[:0:0]
		forks, err := s.cfg.Sandboxes.Fork(r.Context(), req)
		return forks, err
	})
}

// forkSandbox serves POST /v1/sandboxes/{id}/fork: clones of what that sandbox is running
// now. It takes the checkpoint the caller did not have to name, and records it.
func (s *Server) forkSandbox(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}
	id := r.PathValue("id")
	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeSandbox(id); err != nil {
		s.recordSandboxAudit(r, "sandbox.fork", id, "error")
		s.writeError(w, r, err)
		return
	}
	s.fork(w, r, func(req sandbox.ForkRequest) ([]sandbox.Record, error) {
		return s.cfg.Sandboxes.ForkFromSandbox(r.Context(), id, req)
	})
}

// fork reads the fork body, runs the fork and answers with the clones. The three fork
// routes differ only in where they get the checkpoint from, so the mapping, the status
// and the document are here once.
func (s *Server) fork(w http.ResponseWriter, r *http.Request, run func(sandbox.ForkRequest) ([]sandbox.Record, error)) {
	var req v1.SandboxFork
	if status, err := readJSONWrite(w, r, &req); err != nil {
		s.writeErrorStatus(w, r, status, sandboxAPIError(err))
		return
	}
	if req.IdleTTLSeconds < -1 {
		s.writeError(w, r, output.NewError(output.CodeInvalid,
			"idleTtlSeconds must be zero (the node's default), -1 (never) or positive").
			WithDetails(map[string]any{"reason": "invalid_ttl"}))
		return
	}
	forks, err := run(sandbox.ForkRequest{
		Count: req.Count,
		Owner: req.Owner,
		TTL:   time.Duration(req.IdleTTLSeconds) * time.Second,
		IDs:   req.IDs,
	})
	if err != nil {
		s.recordSandboxAudit(r, "sandbox.fork", "", "error")
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.recordSandboxAudit(r, "sandbox.fork", "", "ok")
	items := make([]v1.Sandbox, 0, len(forks))
	for _, rec := range forks {
		items = append(items, sandboxDocument(rec))
		// A clone is a sandbox that was created, so it announces itself as one: a
		// separate `sandbox.forked` type would make a consumer that watches sandboxes
		// appear handle two types for the same fact.
		s.publishSandboxEvent(sandbox.ChangeCreated, rec)
	}
	doc := v1.NewSandboxList(items)
	// A fork's checkpoint is part of its answer: the fork routes take (or name) an image,
	// and a caller that cannot see which one it left behind cannot free it.
	if len(forks) != 0 {
		doc.Checkpoint = forks[0].ForkedFrom
	}
	s.writeJSON(w, r, http.StatusCreated, doc)
}
