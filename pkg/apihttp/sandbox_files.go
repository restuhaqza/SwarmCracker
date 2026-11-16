package apihttp

// This file owns the sandbox routes that reach inside one sandbox's guest: its files,
// its console and its structured status.
//
// All three are reads of the guest's own answers rather than parses of a command's
// output. Files are the agent's own operations — `stat` decides which one, `get` and
// `put` move a single file, `tar` and `untar` move a tree — and status is the agent's
// `info` and `net`. None of them needs a shell in the template, which is what makes them
// usable against a template that ships neither coreutils nor tar.
//
// # What crosses the wire
//
// A file read is the one place in this API where the response is not a JSON document: the
// bytes are the point. GET answers application/octet-stream with the file's bytes, or
// application/x-tar with an archive when the path is a directory — the content type is how
// a caller knows which without a second request. A write says which it is the other way
// round: the request's content type chooses the operation, so application/x-tar is a tree
// and application/octet-stream is one file. What a write did — bytes, entries, entries the
// guest refused — is the SandboxFileWrite document.
//
// # What may be copied
//
// A regular file and a directory, and nothing else. A link, a device and a socket are
// refused rather than followed: following a link would copy something other than what the
// caller named, and the caller that wants a link's target can name the target.
//
// # Who may do it
//
// files takes sandboxes:files (runner-level: writing a file into a sandbox is the power to
// run code in it), logs and status take sandboxes:read. All three reach inside one
// sandbox, so all three require the caller's sandbox selectors to name it: a token minted
// for sandbox A gets 403 for sandbox B, and a runner token with no selector at all gets 403
// "sandbox_selector_required". The attempt is audited (sandbox.files, sandbox.logs,
// sandbox.status) and a refused one is audited as an error, so what a token tried is
// recorded whether or not it was allowed.

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox"
)

const (
	// sandboxFileAuditAction is the audit action recorded for every file attempt, in
	// either direction — the same rule the task file route follows, because a read of a
	// file is as much an act as a write of one.
	sandboxFileAuditAction = "sandbox.files"
	// sandboxLogAuditAction is the audit action for reading a sandbox's console.
	sandboxLogAuditAction = "sandbox.logs"
	// sandboxStatusAuditAction is the audit action for asking a sandbox's guest what it
	// is. It is audited because it dials the guest with the node's own credential: a
	// caller that asks for it is reaching into a sandbox, not reading a record.
	sandboxStatusAuditAction = "sandbox.status"

	// tarMediaType is the media type of a tar archive, and therefore of a directory
	// copy in both directions.
	tarMediaType = "application/x-tar"
	// defaultSandboxFileMode is the mode a written file gets when the caller names none.
	defaultSandboxFileMode = 0o644
	// maxSandboxFileMode is the widest mode a caller may ask for: a setuid or setgid bit
	// is not a workspace file's business.
	maxSandboxFileMode = 0o777
)

// getSandboxFile serves GET /v1/sandboxes/{id}/files?path=...: one file's bytes, or a
// directory as a tar archive.
//
// The shape is decided before the response starts, because a response's headers are
// written before its body: the guest is asked what the path is (DescribeFile), and the
// answer chooses the media type and the operation. A path that is neither a file nor a
// directory is refused here, with nothing written.
//
// A failure after the headers are out cannot be turned into an error document — the
// status and the media type are already on the wire — so the copy stops and the failure is
// logged. That is the price of a streaming read, and it is the same one the task file
// route pays through the node broker.
func (s *Server) getSandboxFile(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}

	id := r.PathValue("id")
	guestPath, ok := s.authorizeSandboxFile(w, r, id, sandboxFileAuditAction)
	if !ok {
		return
	}

	file, err := s.cfg.Sandboxes.DescribeFile(r.Context(), id, guestPath)
	if err != nil {
		s.recordSandboxAudit(r, sandboxFileAuditAction, id, "error")
		s.writeError(w, r, sandboxAPIError(err))
		return
	}

	mediaType, err := sandboxFileMediaType(file.Kind)
	if err != nil {
		s.recordSandboxAudit(r, sandboxFileAuditAction, id, "error")
		s.writeErrorStatus(w, r, http.StatusBadRequest, output.NewError(output.CodeInvalid, err.Error()).
			WithDetails(map[string]any{"reason": "unsupported_file_kind", "path": guestPath, "kind": file.Entry.Type}))
		return
	}

	w.Header().Set("Content-Type", mediaType)
	// The kind is given as a header of its own as well, so a caller that stores the
	// body can record what it was without inferring it from a media type.
	w.Header().Set("X-Swarmcracker-File-Kind", string(file.Kind))
	w.WriteHeader(http.StatusOK)

	if _, err := s.cfg.Sandboxes.SendFile(r.Context(), id, guestPath, file.Kind, w); err != nil {
		// Nothing can be written now: the 200 and the media type are already sent. The
		// copy stopped partway, so the body the caller has is a fragment, and the log
		// says why. Truncating the response is the only honest end available.
		s.logSandboxFileFailure(r, "read", id, guestPath, err)
		s.recordSandboxAudit(r, sandboxFileAuditAction, id, "error")
		return
	}
	s.recordSandboxAudit(r, sandboxFileAuditAction, id, "ok")
}

// putSandboxFile serves PUT /v1/sandboxes/{id}/files?path=...&mode=0644.
//
// The request's content type chooses the operation: application/octet-stream writes one
// regular file (with the mode from the query, defaulting to 0644), and application/x-tar
// extracts the archive as a tree. A tree needs the template's agent to speak `untar`; a
// template built before that operation answers 501 with details.capability "untar", and a
// single file still arrives there.
//
// Nothing is buffered: the body is streamed to the agent's own framing, so a workspace of
// any size costs a chunk of memory rather than its own size. The bound on how much may be
// written is the guest's (pkg/agent's untar bound, 16 GiB of file bytes for a tree), not a
// node-level cap: a copy of a workspace is expected to be large, and the guest is the side
// that would run out of disk.
func (s *Server) putSandboxFile(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}

	id := r.PathValue("id")
	guestPath, ok := s.authorizeSandboxFile(w, r, id, sandboxFileAuditAction)
	if !ok {
		return
	}

	kind, ok := s.sandboxFileWriteKind(w, r)
	if !ok {
		s.recordSandboxAudit(r, sandboxFileAuditAction, id, "error")
		return
	}

	var mode uint32 = defaultSandboxFileMode
	if raw := strings.TrimSpace(r.URL.Query().Get("mode")); raw != "" {
		parsed, err := strconv.ParseUint(strings.TrimPrefix(raw, "0o"), 8, 32)
		if err != nil || parsed > maxSandboxFileMode {
			s.recordSandboxAudit(r, sandboxFileAuditAction, id, "error")
			s.writeError(w, r, output.NewErrorf(output.CodeInvalid,
				"mode %q is not an octal permission set of at most %o", raw, maxSandboxFileMode).
				WithDetails(map[string]any{"reason": "invalid_mode", "mode": raw}))
			return
		}
		mode = uint32(parsed)
	}

	// The body is the caller's, and a caller that closes the connection stops the copy:
	// the request context is what carries that into the agent's stream.
	body := io.Reader(r.Body)
	var write v1.SandboxFileWrite
	switch kind {
	case sandbox.FileKindDir:
		summary, err := s.cfg.Sandboxes.ExtractTree(r.Context(), id, guestPath, body)
		if err != nil {
			s.recordSandboxAudit(r, sandboxFileAuditAction, id, "error")
			s.writeError(w, r, sandboxAPIError(err))
			return
		}
		write = v1.NewSandboxFileWrite(guestPath, summary.Bytes, summary.Entries, summary.Warnings)
	default:
		written, err := s.cfg.Sandboxes.PutFile(r.Context(), id, guestPath, mode, body)
		if err != nil {
			s.recordSandboxAudit(r, sandboxFileAuditAction, id, "error")
			s.writeError(w, r, sandboxAPIError(err))
			return
		}
		write = v1.NewSandboxFileWrite(guestPath, written, 0, nil)
	}

	s.recordSandboxAudit(r, sandboxFileAuditAction, id, "ok")
	s.writeJSON(w, r, http.StatusOK, write)
}

// getSandboxLogs serves GET /v1/sandboxes/{id}/logs: the sandbox's serial console as the
// node recorded it.
//
// The console is the guest's own, written by the VMM as the guest boots and runs, and it
// exists whether or not the sandbox is ready — a sandbox that failed to boot is exactly the
// one whose console a caller needs. So this is not the agent's path and needs no dial: a
// record this node does not have is 404, a node configured with no log directory is 501,
// and a sandbox that has written no console yet is 404 with the sandbox's own name in it.
func (s *Server) getSandboxLogs(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}

	id := r.PathValue("id")
	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeSandbox(id); err != nil {
		s.recordSandboxAudit(r, sandboxLogAuditAction, id, "error")
		s.writeError(w, r, err)
		return
	}

	path, err := s.cfg.Sandboxes.ServerLogPath(id)
	if err != nil {
		s.recordSandboxAudit(r, sandboxLogAuditAction, id, "error")
		if errors.Is(err, sandbox.ErrNotImplemented) {
			// Not the checkpoint answer this node gives for a missing store: this node
			// records no console at all, which is a capability it does not serve.
			s.writeError(w, r, output.NewError(output.CodeNotImplemented, err.Error()).
				WithDetails(map[string]any{"reason": "logs_unavailable", "capability": "sandboxes:logs"}))
			return
		}
		s.writeError(w, r, sandboxAPIError(err))
		return
	}

	file, err := os.Open(path)
	if err != nil {
		s.recordSandboxAudit(r, sandboxLogAuditAction, id, "error")
		s.writeError(w, r, output.NewError(output.CodeNotFound, err.Error()))
		return
	}
	defer func() { _ = file.Close() }()

	// A console is bytes from the guest's serial port: text in practice, not in
	// guarantee, so it is sent as text and a caller treats it as a stream.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, file); err != nil {
		s.logSandboxFileFailure(r, "read the console of", id, path, err)
		s.recordSandboxAudit(r, sandboxLogAuditAction, id, "error")
		return
	}
	s.recordSandboxAudit(r, sandboxLogAuditAction, id, "ok")
}

// getSandboxStatus serves GET /v1/sandboxes/{id}/status: the sandbox's record plus what its
// guest says about itself.
//
// It is the alternative to reading a transcript. The guest reports its identity, kernel,
// resources and toolchain (`info`) and its own network view (`net`), both read inside the
// guest; a caller that needed those from an exec would also need the template to ship a
// shell and the parser to agree with the guest's, and would get a different answer every
// time a command's formatting changed.
func (s *Server) getSandboxStatus(w http.ResponseWriter, r *http.Request) {
	if !s.sandboxesAvailable(w, r) {
		return
	}

	id := r.PathValue("id")
	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeSandbox(id); err != nil {
		s.recordSandboxAudit(r, sandboxStatusAuditAction, id, "error")
		s.writeError(w, r, err)
		return
	}

	status, err := s.cfg.Sandboxes.Status(r.Context(), id)
	if err != nil {
		s.recordSandboxAudit(r, sandboxStatusAuditAction, id, "error")
		s.writeError(w, r, sandboxAPIError(err))
		return
	}
	s.recordSandboxAudit(r, sandboxStatusAuditAction, id, "ok")
	s.writeJSON(w, r, http.StatusOK, v1.NewSandboxStatus(
		sandboxDocument(status.Record), sandboxGuestInfoDocument(status.Info), sandboxGuestNetDocument(status.Net)))
}

// authorizeSandboxFile resolves the path a file request names and authorizes the caller.
// Every refusal is written and audited here, so a read and a write cannot disagree about
// which paths are acceptable or about who may ask.
func (s *Server) authorizeSandboxFile(w http.ResponseWriter, r *http.Request, id, action string) (string, bool) {
	identity, _ := IdentityFrom(r.Context())
	if err := identity.AuthorizeSandbox(id); err != nil {
		s.recordSandboxAudit(r, action, id, "error")
		s.writeError(w, r, err)
		return "", false
	}

	guestPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if guestPath == "" {
		s.recordSandboxAudit(r, action, id, "error")
		s.writeError(w, r, output.NewError(output.CodeInvalid,
			"a guest path is required: name the file or directory to copy").
			WithDetails(map[string]any{"reason": "missing_path", "parameter": "path"}))
		return "", false
	}
	return guestPath, true
}

// sandboxFileWriteKind reads the content type that chooses the write's operation.
func (s *Server) sandboxFileWriteKind(w http.ResponseWriter, r *http.Request) (sandbox.FileKind, bool) {
	mediaType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil {
		mediaType = parsed
	}

	switch mediaType {
	case "application/octet-stream":
		return sandbox.FileKindFile, true
	case tarMediaType:
		return sandbox.FileKindDir, true
	default:
		s.writeErrorStatus(w, r, http.StatusUnsupportedMediaType, output.NewErrorf(output.CodeInvalid,
			"a write must be %s (one file) or %s (a tree), not %q", "application/octet-stream", tarMediaType, mediaType).
			WithDetails(map[string]any{"reason": "unsupported_media_type",
				"parameter": "Content-Type", "contentType": mediaType}))
		return "", false
	}
}

// sandboxFileMediaType is the media type a read of one kind of guest path answers with.
func sandboxFileMediaType(kind sandbox.FileKind) (string, error) {
	switch kind {
	case sandbox.FileKindFile:
		return "application/octet-stream", nil
	case sandbox.FileKindDir:
		return tarMediaType, nil
	default:
		return "", errors.New("this path is not a file or a directory, and only those can be copied")
	}
}

// logSandboxFileFailure reports a copy that failed after its response had started, where a
// typed document can no longer be written. It is a log line and not an answer, because the
// answer — a status and a media type — is already on the wire.
func (s *Server) logSandboxFileFailure(r *http.Request, verb, id, path string, err error) {
	log.Warn().Err(err).
		Str("sandbox_id", id).Str("path", path).Str("operation", verb).
		Str("request_id", requestIDFrom(r)).
		Msg("Sandbox copy failed after its response started; the body the caller has is a fragment")
}

// sandboxGuestInfoDocument maps the guest's own report of itself.
func sandboxGuestInfoDocument(info agent.GuestInfo) v1.SandboxGuestInfo {
	return v1.SandboxGuestInfo{
		Hostname:         info.Hostname,
		MachineID:        info.MachineID,
		KernelRelease:    info.KernelRelease,
		KernelVersion:    info.KernelVersion,
		Machine:          info.Machine,
		OSName:           info.OSName,
		OSID:             info.OSID,
		CPUs:             info.CPUs,
		MemoryTotalBytes: info.MemoryTotalBytes,
		UptimeSeconds:    info.UptimeSeconds,
		Protocol:         info.Protocol,
		AgentVersion:     info.AgentVersion,
		BuildID:          info.BuildID,
		BootID:           info.BootID,
		Tools:            info.Tools,
	}
}

// sandboxGuestNetDocument maps the guest's own view of its network.
func sandboxGuestNetDocument(net agent.GuestNet) v1.SandboxGuestNet {
	out := v1.SandboxGuestNet{
		Interfaces: make([]v1.SandboxNetInterface, 0, len(net.Interfaces)),
		Routes:     make([]v1.SandboxNetRoute, 0, len(net.Routes)),
		Resolvers:  append([]string{}, net.Resolvers...),
		Search:     append([]string(nil), net.Search...),
	}
	for _, iface := range net.Interfaces {
		out.Interfaces = append(out.Interfaces, v1.SandboxNetInterface{
			Name: iface.Name, Index: iface.Index, MAC: iface.MAC, MTU: iface.MTU,
			Up: iface.Up, Loopback: iface.Loopback, Addresses: append([]string(nil), iface.Addresses...),
		})
	}
	for _, route := range net.Routes {
		out.Routes = append(out.Routes, v1.SandboxNetRoute{
			Family: route.Family, Destination: route.Destination, Gateway: route.Gateway,
			Device: route.Device, Metric: route.Metric,
		})
	}
	return out
}
