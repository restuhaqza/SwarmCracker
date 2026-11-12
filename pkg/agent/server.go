package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// errEndConn is an internal signal that a request ended the framed session and
// ServeConn must stop serving. It is not reported to the caller as an error.
var errEndConn = errors.New("agent: request ended the connection")

// ErrUnauthorized is returned by ServeConnAuth when a connection did not prove
// this boot's token. The guest sends no frame before dropping the connection,
// so a peer cannot tell a missing token from a wrong one.
var ErrUnauthorized = errors.New("agent: unauthenticated connection")

// maxDeadlineMs bounds a peer-supplied budget (24 hours). It only exists so the
// conversion to time.Duration cannot overflow on a hostile value.
const maxDeadlineMs = int64(24 * time.Hour / time.Millisecond)

// ServeConn serves the guest-agent protocol over rw. Requests are handled one
// at a time, in order, until the peer closes the connection, a protocol error
// occurs, or a request ends the session. A clean disconnect returns nil.
//
// A TTY exec ends the session: the guest attaches the client's raw stdin to the
// PTY, which shares the byte stream with framed requests, so the framed
// protocol cannot continue afterwards. ServeConn returns after sending the exit
// frame, leaving the caller to close rw.
//
// The caller is responsible for the connection lifetime; ServeConn does not
// close rw.
func ServeConn(rw io.ReadWriteCloser) error {
	return serveConn(rw, Capabilities(), nil, sharedTracker)
}

// ServeConnAuth is ServeConn with per-boot token authentication. When auth is
// non-nil every connection must authenticate with a hello carrying this boot's
// token before any other operation is served, and a missing or wrong token ends
// the connection with ErrUnauthorized and no reply frame — the guest never says
// why it hung up, so a peer cannot use the answer to probe.
//
// A nil auth serves the connection exactly like ServeConn, which is what a VM
// booted without token authentication gets.
func ServeConnAuth(rw io.ReadWriteCloser, auth *TokenAuthenticator) error {
	return serveConn(rw, Capabilities(), auth, sharedTracker)
}

// serveConn serves the protocol advertising caps, and requiring auth when it is
// non-nil, recording what it serves in trk. ServeConn passes the capability set
// this build implements and the tracker every connection of this agent shares;
// tests narrow the caps to prove that an operation outside the advertised set is
// refused, and pass their own tracker so one test's operations are not another
// test's activity. The gates themselves are the same code path either way.
func serveConn(rw io.ReadWriteCloser, caps []string, auth *TokenAuthenticator, trk *tracker) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &server{rw: rw, ctx: ctx, cancel: cancel, caps: caps, auth: auth, tracker: trk}
	for {
		var req Request
		if _, err := ReadFrame(rw, &req); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		if err := s.dispatch(&req); err != nil {
			if errors.Is(err, errEndConn) {
				return nil
			}
			return err
		}
	}
}

// server holds the connection state for one client. mu serialises frame
// writes so concurrent output streams (stdout/stderr) never interleave.
type server struct {
	rw     io.ReadWriteCloser
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex

	// auth is nil for a VM booted without token authentication. authed is set
	// once a hello proved the token; while auth is non-nil and authed is false,
	// every operation except hello is refused before it can touch the guest.
	auth   *TokenAuthenticator
	authed bool

	// caps is the closed set of operations this connection advertises.
	caps []string
	// tracker records what this agent is serving, across every connection it
	// serves, so `activity` describes the sandbox and not one of its sessions.
	tracker *tracker
	// id is the id of the request being served, stamped on every frame it
	// produces.
	id string
	// reqCtx bounds the request being served: ctx when it carried no deadline,
	// or a child context that expires when it does.
	reqCtx context.Context
}

// write sends a single frame, serialised against other writers and stamped with
// the id of the request being served.
func (s *server) write(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg, ok := v.(Message); ok {
		if msg.ID == "" {
			msg.ID = s.id
		}
		v = msg
	}
	return WriteFrame(s.rw, v)
}

// writeError reports a request failure to the client as an error frame. The
// connection stays usable, so a nil return is intentional.
func (s *server) writeError(format string, args ...any) error {
	return s.writeFailure(CodeInternal, format, args...)
}

// writeFailure reports a typed failure to the client. The code is the contract;
// the formatted message is for humans.
func (s *server) writeFailure(code ErrorCode, format string, args ...any) error {
	return s.write(Message{Type: "error", ErrorCode: code, Error: fmt.Sprintf(format, args...)})
}

// requestContext bounds one request. A positive budget caps the handler; a
// negative budget means the caller's deadline had already passed when the frame
// was sent, so the returned context is already done and the request is refused
// without being carried out. Zero means no deadline.
func requestContext(parent context.Context, deadlineMs int64) (context.Context, context.CancelFunc) {
	switch {
	case deadlineMs > 0:
		// A peer-supplied budget is clamped before it becomes a duration: a
		// value near the int64 limit would otherwise overflow into a negative
		// duration and expire the request immediately.
		if deadlineMs > maxDeadlineMs {
			deadlineMs = maxDeadlineMs
		}
		return context.WithTimeout(parent, time.Duration(deadlineMs)*time.Millisecond)
	case deadlineMs < 0:
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, func() {}
	default:
		return context.WithCancel(parent)
	}
}

func (s *server) dispatch(req *Request) error {
	// A VM booted with token authentication refuses every operation until a
	// hello proved the token, and refuses it before it can read a path or start
	// a process. The connection is dropped without a frame so a peer cannot
	// learn whether the token was missing or wrong.
	if s.auth != nil && !s.authed && req.Op != "hello" {
		return ErrUnauthorized
	}

	s.id = req.ID
	ctx, cancel := requestContext(s.ctx, req.DeadlineMs)
	defer cancel()
	s.reqCtx = ctx

	// The advertised set is the contract. An op outside it is refused with a
	// typed error instead of being attempted or approximated, so a host can
	// never mistake a missing operation for a successful one.
	if !slices.Contains(s.caps, req.Op) {
		return s.writeFailure(CodeUnsupportedOp,
			"op %q is not advertised by this agent (caps: %s)", req.Op, strings.Join(s.caps, ","))
	}
	if ctx.Err() != nil {
		return s.writeFailure(CodeDeadlineExceeded, "%s: deadline was already spent when the request arrived", req.Op)
	}

	// From here on the operation is one this agent will actually carry out, so it
	// is recorded as in flight for as long as it runs. The activity operation
	// excludes itself, which is what makes it answerable while the sandbox is
	// busy without ever making the sandbox look busy.
	done := s.tracker.begin(req.Op)
	defer done()

	switch req.Op {
	case CapHello:
		return s.handleHello(req)
	case CapExec:
		if err := s.handleExec(req); err != nil {
			return err
		}
		if req.TTY {
			// A TTY session shares the raw byte stream with framed requests,
			// so the framed protocol cannot continue. handleExec has already
			// sent the exit frame; end the connection.
			return errEndConn
		}
		return nil
	case CapPut:
		return s.handlePut(req)
	case CapGet:
		return s.handleGet(req)
	case CapMetrics:
		return s.handleMetrics()
	case CapPrepare:
		return s.handlePrepare()
	case CapInfo:
		return s.handleInfo()
	case CapNet:
		return s.handleNet()
	case CapStat:
		return s.handleStat(req)
	case CapList:
		return s.handleList(req)
	case CapTar:
		return s.handleTar(req)
	case CapActivity:
		return s.handleActivity()
	default:
		// Unreachable through the gate above; kept so an op added to this
		// switch without being advertised is still refused, not attempted.
		return s.writeFailure(CodeUnsupportedOp, "op %q has no handler", req.Op)
	}
}

func (s *server) handleHello(req *Request) error {
	if s.auth != nil {
		// Verify before the version check: an unauthenticated peer must not be
		// able to tell a version mismatch from a rejected token.
		if !s.auth.Verify(req.Token) {
			return ErrUnauthorized
		}
		s.authed = true
	}
	if req.Version != Version {
		// A pre-v2 agent answers a v2 hello with an error frame of its own,
		// which the host maps to CodeProtocolUnsupported. Answering with the
		// same code keeps both directions honest about what happened.
		return s.writeFailure(CodeProtocolUnsupported,
			"agent protocol unsupported: client speaks %s, guest speaks %s", req.Version, Version)
	}
	reply := s.helloReply()
	if s.auth != nil {
		// The greeting stays single-sourced: authentication only adds these two
		// flags, so a token-protected agent describes itself exactly like one
		// that is not.
		reply.AuthRequired = true
		reply.TokenOK = true
	}
	return s.write(reply)
}

// helloReply is the payload of a successful hello: what this agent speaks and
// what it can do, for the capability set this connection advertises.
func (s *server) helloReply() Message {
	return HelloReply(s.caps)
}

// handleMetrics samples the guest's own /proc and replies with one metrics
// frame. A read failure is reported as an error frame rather than a zeroed
// sample, so the host can leave the series unset instead of publishing 0.
func (s *server) handleMetrics() error {
	m, err := ReadGuestMetrics(procRoot)
	if err != nil {
		return s.writeError("metrics: %v", err)
	}
	return s.write(Message{Type: "metrics", Metrics: &m})
}

// handlePrepare runs the post-restore hook and replies with what the guest
// carries afterwards (prepare.go).
//
// A failure here is typed and fatal to the caller's launch: an instance wearing
// the identity of the machine its checkpoint came from is not the instance that
// was asked for, so "prepare did not work" must not look like "prepare was not
// needed". The guest keeps serving the connection, so the caller learns what went
// wrong before it tears the instance down.
func (s *server) handlePrepare() error {
	report, err := prepareGuest(s.reqCtx)
	if err != nil {
		// The op is not repeated here: the client prefixes it, and "prepare: prepare:
		// ..." reads like a bug in the thing being reported.
		return s.writeFailure(CodePrepareFailed, "%v", err)
	}
	return s.write(Message{Type: "prepared", Prepared: &report})
}

// handlePut receives a stream of file frames and writes them to req.Path.
//
// The bytes are written to a temporary file next to req.Path and renamed onto
// req.Path only after the terminal empty file frame arrives, so an interrupted
// or failed transfer cannot truncate an existing destination. The temporary
// file is removed on every error path.
func (s *server) handlePut(req *Request) error {
	if req.Path == "" {
		return s.writeFailure(CodeInvalidRequest, "put: empty path")
	}
	if dir := filepath.Dir(req.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return s.writeError("put: create parent directory for %s: %v", req.Path, err)
		}
	}

	mode := os.FileMode(req.Mode)
	if mode == 0 {
		mode = 0o644
	}

	tmpPath := req.Path + ".sc-put-tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return s.writeError("put: open temp file for %s: %v", req.Path, err)
	}
	// Best effort: a no-op once the rename below succeeds.
	defer func() { _ = os.Remove(tmpPath) }()

	for {
		if s.reqCtx.Err() != nil {
			_ = f.Close()
			return s.writeFailure(CodeDeadlineExceeded, "put: deadline exceeded before the transfer finished")
		}
		var msg Message
		// A put payload is a stream, so it is bounded by the stream limit
		// rather than the smaller control-frame cap.
		if _, err := ReadStreamFrame(s.rw, &msg); err != nil {
			_ = f.Close()
			return fmt.Errorf("put: read data frame: %w", err)
		}
		if msg.Type != "file" {
			_ = f.Close()
			return s.writeError("put: unexpected frame type %q", msg.Type)
		}
		if len(msg.Data) == 0 {
			break
		}
		if _, err := f.Write(msg.Data); err != nil {
			_ = f.Close()
			return s.writeError("put: write %s: %v", req.Path, err)
		}
	}
	if err := f.Close(); err != nil {
		return s.writeError("put: close %s: %v", req.Path, err)
	}
	// Apply the requested mode explicitly: OpenFile only honours it on create.
	if err := os.Chmod(tmpPath, mode); err != nil {
		return s.writeError("put: chmod %s: %v", req.Path, err)
	}
	if err := os.Rename(tmpPath, req.Path); err != nil {
		return s.writeError("put: rename temp file to %s: %v", req.Path, err)
	}
	return s.write(Message{Type: "ok"})
}

// handleGet streams req.Path to the client as file frames, terminated by an
// empty file frame.
func (s *server) handleGet(req *Request) error {
	if req.Path == "" {
		return s.writeFailure(CodeInvalidRequest, "get: empty path")
	}
	f, err := os.Open(req.Path)
	if err != nil {
		return s.writeError("get: open %s: %v", req.Path, err)
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, fileChunkSize)
	for {
		if s.reqCtx.Err() != nil {
			return s.writeFailure(CodeDeadlineExceeded, "get: deadline exceeded before the transfer finished")
		}
		n, err := f.Read(buf)
		if n > 0 {
			if werr := s.write(Message{Type: "file", Data: buf[:n]}); werr != nil {
				return werr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return s.writeError("get: read %s: %v", req.Path, err)
		}
	}
	return s.write(Message{Type: "file"})
}

// handleInfo replies with what the guest knows about itself (info.go).
//
// A read failure is the agent's own trouble and is reported as such: a report
// with empty fields would read as "this guest has no kernel", which is never
// true and would mislead the scheduler that asked.
func (s *server) handleInfo() error {
	info, err := readGuestInfo()
	if err != nil {
		return s.writeFailure(CodeInternal, "info: %v", err)
	}
	return s.write(Message{Type: "info", Info: &info})
}

// handleNet replies with the guest's own view of its network (netinfo.go).
func (s *server) handleNet() error {
	view, err := readGuestNet()
	if err != nil {
		return s.writeFailure(CodeInternal, "net: %v", err)
	}
	return s.write(Message{Type: "net", Net: &view})
}

// handleStat replies with the entry at the request's path (files.go).
func (s *server) handleStat(req *Request) error {
	entry, err := statGuestFile(req.Path)
	if err != nil {
		return s.writeFileFailure(err)
	}
	return s.write(Message{Type: "stat", Stat: &entry})
}

// handleList streams the entries of the directory at the request's path.
//
// The listing is read in full first and then sent in batches, because a listing
// is sorted and its bound has to cut it at a name a caller can predict; the bound
// is what keeps that whole answer small enough to hold in memory on both sides.
func (s *server) handleList(req *Request) error {
	listing, err := listGuestDir(req.Path)
	if err != nil {
		return s.writeFileFailure(err)
	}
	for start := 0; start < len(listing.Entries); {
		if err := s.reqCtx.Err(); err != nil {
			return s.writeFailure(CodeDeadlineExceeded, "list: deadline exceeded before the listing finished")
		}
		end, size := start, 0
		for end < len(listing.Entries) && end-start < listBatchEntries {
			next := entryWireSize(listing.Entries[end])
			if end > start && size+next > listBatchBytes {
				break
			}
			size += next
			end++
		}
		if err := s.write(Message{Type: "entries", Entries: listing.Entries[start:end]}); err != nil {
			return err
		}
		start = end
	}
	// The terminal empty batch ends the listing and carries whether the bound cut
	// it short, so a caller never has to guess whether the last non-empty frame
	// was also the last entry.
	return s.write(Message{Type: "entries", Truncated: listing.Truncated})
}

const (
	// listBatchEntries and listBatchBytes bound one batch frame. A batch is an
	// ordinary control frame, so it must stay well below MaxFrameSize; both
	// bounds are what makes that true for any name the guest's filesystem can
	// hold, rather than true for the names this happened to be tested with.
	listBatchEntries = 512
	listBatchBytes   = 128 << 10
)

// entryWireSize is an upper bound on how many bytes of a frame one entry adds:
// its three variable-length strings, whose lengths are known here, plus more than
// the fixed fields and their JSON can take.
func entryWireSize(entry FileEntry) int {
	return len(entry.Name) + len(entry.Mode) + len(entry.Target) + 128
}

// handleTar streams the tree at the request's path as a tar archive: data frames
// carry the archive and the terminal empty frame carries its summary (files.go).
//
// A failure mid-archive is reported as an error frame instead of the terminal
// one, and the archive is left unclosed, so a caller that ignores the error still
// holds a stream that cannot be mistaken for a complete archive.
func (s *server) handleTar(req *Request) error {
	summary, err := streamGuestTar(s.reqCtx, req.Path, func(p []byte) error {
		return s.write(Message{Type: "file", Data: p})
	})
	if err != nil {
		return s.writeFileFailure(err)
	}
	return s.write(Message{Type: "file", Tar: &summary})
}

// handleActivity replies with what this sandbox is doing right now
// (activity.go). It is the one operation that reports on the agent itself rather
// than on the guest's kernel, and the one that must never fail: a scheduler that
// cannot ask whether a sandbox is idle has to assume it is not.
func (s *server) handleActivity() error {
	activity := s.tracker.snapshot(procRoot)
	return s.write(Message{Type: "activity", Activity: &activity})
}

// writeFileFailure reports a file operation's failure with the code the failure
// carries, so a caller can tell a path it may not use (unsafe_path) from one that
// is not there (not_found) from the guest's own trouble (internal).
func (s *server) writeFileFailure(err error) error {
	code, message := failureCode(err)
	return s.writeFailure(code, "%s", message)
}
