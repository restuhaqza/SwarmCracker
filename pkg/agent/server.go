package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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

// consoleLog is where a file operation's failure is reported for an operator to find. It is
// the guest's serial console — the same stream a boot failure explains itself on, and the one
// `sandbox logs` reads — because the alternative is what a full disk looked like before: a
// caller that hung with no error, no exit status, and no line anywhere.
//
// A failed write here is not allowed to change the request's outcome: the typed failure is
// still the answer the caller gets, and a console that cannot be written is not a reason to
// turn a refusal into a different failure.
var consoleLog io.Writer = func() io.Writer {
	f, err := os.OpenFile("/dev/console", os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return os.Stderr
	}
	return f
}()

// logFailure writes one line to the console for a file operation that failed. The message
// carries the operation, the path and the guest's own error, which is what an operator needs
// to tell a full disk from a refused path.
func (s *server) logFailure(op, path string, err error) {
	if consoleLog == nil {
		return
	}
	_, _ = fmt.Fprintf(consoleLog, "sc-agent: %s %q failed: %v\n", op, path, err)
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

	return s.serve(req)
}

// serve hands one admitted request to the handler for its operation.
//
// The operations are grouped by the plane D9 gives them: the session operations reach
// a caller's files and processes, and the machine operations describe or change the
// guest itself. The split is not cosmetic — it is the division the guest's privilege
// model is written in — and it keeps one function from growing a branch per operation
// for as long as the protocol is extended.
func (s *server) serve(req *Request) error {
	switch req.Op {
	case CapExec:
		return s.serveExec(req)
	case CapPut:
		return s.handlePut(req)
	case CapGet:
		return s.handleGet(req)
	case CapStat:
		return s.handleStat(req)
	case CapList:
		return s.handleList(req)
	case CapTar:
		return s.handleTar(req)
	case CapUntar:
		return s.handleUntar(req)
	default:
		return s.serveMachine(req)
	}
}

// serveExec runs one command. A TTY session shares the raw byte stream with framed
// requests, so the framed protocol cannot continue after it: handleExec has already
// sent the exit frame, and the connection ends here.
func (s *server) serveExec(req *Request) error {
	if err := s.handleExec(req); err != nil {
		return err
	}
	if req.TTY {
		return errEndConn
	}
	return nil
}

// serveMachine is the machine plane: the guest's own identity, resources and state,
// which only root inside the guest can observe or change.
func (s *server) serveMachine(req *Request) error {
	switch req.Op {
	case CapHello:
		return s.handleHello(req)
	case CapMetrics:
		return s.handleMetrics()
	case CapPrepare:
		return s.handlePrepare()
	case CapInfo:
		return s.handleInfo()
	case CapNet:
		return s.handleNet()
	case CapActivity:
		return s.handleActivity()
	case CapQuiesce:
		return s.handleQuiesce(req)
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

// handleQuiesce stops or releases the guest's writers, and replies with what it did.
//
// A failure is typed and fatal to the checkpoint the caller is taking: an image
// captured while the guest was still writing is a pair of images that disagree, and a
// caller that treated this as a warning would store it as if it were consistent.
func (s *server) handleQuiesce(req *Request) error {
	report, err := quiesceGuest(s.reqCtx, req.Quiesce)
	if err != nil {
		// The op is not repeated here: the client prefixes it, and "quiesce:
		// quiesce: ..." reads like a bug in the thing being reported.
		return s.writeFailure(CodeQuiesceFailed, "%v", err)
	}
	return s.write(Message{Type: "quiesced", ID: req.ID, Quiesced: &report})
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
	// A resumed guest may still have its filesystems frozen: the freeze lives in the
	// superblock, so the memory image carries it, and the first writer in a clone is
	// this hook. Release it before anything writes — including this agent's own log and
	// the token, machine-id, host keys and clock the hook applies — and fail here,
	// naming the mount, rather than hanging on an unrelated write a moment later.
	//
	// Only a guest that advertises quiesce is asked: one that does not cannot have been
	// frozen by it, and an older agent must still restore.
	if slices.Contains(s.caps, CapQuiesce) {
		if _, err := thawAll(s.reqCtx); err != nil {
			return s.writeFailure(CodePrepareFailed,
				"release the freeze this image was captured with: %v", err)
		}
	}
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
// The path is confined exactly as `stat`, `list`, `tar` and `untar` confine theirs —
// lexical refusal of an absolute name or a ".." that leaves the root, then os.Root for
// links — so `put` cannot write outside the workspace. It could before this: the path
// went straight to OpenFile, relative to whichever directory the agent was started in,
// which made `sandbox cp` a way out of the guest's workspace and not merely a way in.
//
// The bytes are written to a temporary file beside req.Path and renamed onto it only
// after the terminal empty file frame arrives, so an interrupted or failed transfer
// cannot truncate an existing destination. The temporary file is removed on every
// error path.
func (s *server) handlePut(req *Request) error {
	if req.Path == "" {
		return s.writeFailure(CodeInvalidRequest, "put: empty path")
	}

	mode := os.FileMode(req.Mode)
	if mode == 0 {
		mode = 0o644
	}

	write, err := s.openPut(req.Path, mode, req.Rooted)
	if err != nil {
		// A refused path is the caller's mistake, not this agent's trouble: it is
		// answered with the same code `stat` gives it, so one rule covers both. The
		// payload is drained first — the caller is still sending it, and a reply sent
		// while it holds those bytes would deadlock both sides on a full pipe.
		s.drainPutStream()
		return s.writeFileFailure(err)
	}
	// Best effort: a no-op after Commit, and the file a failed transfer left is not a
	// file a later read should find.
	defer write.Abort()

	for {
		if s.reqCtx.Err() != nil {
			return s.writeFailure(CodeDeadlineExceeded, "put: deadline exceeded before the transfer finished")
		}
		var msg Message
		// A put payload is a stream, so it is bounded by the stream limit
		// rather than the smaller control-frame cap.
		if _, err := ReadStreamFrame(s.rw, &msg); err != nil {
			return fmt.Errorf("put: read data frame: %w", err)
		}
		if msg.Type != "file" {
			return s.writeError("put: unexpected frame type %q", msg.Type)
		}
		if len(msg.Data) == 0 {
			break
		}
		if _, err := write.Write(msg.Data); err != nil {
			// The guest's disk took the bytes up to here and no more. Reported at once,
			// not after draining: a caller whose write has failed is blocked sending the
			// rest, so waiting for the end of its stream is waiting forever. Logged
			// because this is the failure an operator meets as `sandbox cp` failing on a
			// full disk, and the console is where they look.
			s.logFailure("put", req.Path, err)
			return s.writeFileFailure(err)
		}
	}

	if err := write.Commit(mode); err != nil {
		s.logFailure("put", req.Path, err)
		return s.writeFileFailure(err)
	}
	return s.write(Message{Type: "ok"})
}

// openPut opens a put's destination.
//
// A rooted request is the sandbox plane: its path is confined to the workspace, so a
// credential scoped to one sandbox cannot write the guest's own files with it. A
// request that does not ask to be rooted is the older plane — `vm cp` and the task file
// routes — where the caller names an absolute guest path and there is no scoped
// credential in front of the operation.
func (s *server) openPut(path string, mode os.FileMode, rooted bool) (*guestWrite, error) {
	return createGuestFile(path, mode, rooted)
}

// openGet opens a get's source, with the same two planes openPut has.
func (s *server) openGet(path string, rooted bool) (io.ReadCloser, error) {
	return guestRead(path, rooted)
}

// drainPutStream reads a put's payload to its end and discards it, so a refusal that
// was decided before the first byte can still be answered. It is what `untar` does
// with its own reader, and for the same reason: the peer is blocked writing frames
// nobody will read.
//
// A failure to drain is not reported: the connection is already unusable, and the
// caller is about to be told why the path was refused.
func (s *server) drainPutStream() {
	for {
		var msg Message
		if _, err := ReadStreamFrame(s.rw, &msg); err != nil {
			return
		}
		if msg.Type != "file" || len(msg.Data) == 0 {
			return
		}
	}
}

// handleGet streams req.Path to the client as file frames, terminated by an
// empty file frame.
//
// The path is confined through the same seam as `put`, so a read cannot reach outside
// the workspace either — a sandbox-scoped `sandboxes:files` credential is a way into
// one sandbox's workspace, not a way to read the guest's own files.
func (s *server) handleGet(req *Request) error {
	if req.Path == "" {
		return s.writeFailure(CodeInvalidRequest, "get: empty path")
	}
	file, err := s.openGet(req.Path, req.Rooted)
	if err != nil {
		return s.writeFileFailure(err)
	}
	defer func() { _ = file.Close() }()

	buf := make([]byte, fileChunkSize)
	for {
		if s.reqCtx.Err() != nil {
			return s.writeFailure(CodeDeadlineExceeded, "get: deadline exceeded before the transfer finished")
		}
		n, err := file.Read(buf)
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

// handleUntar writes the archive the request streams into the workspace, and answers with
// what it wrote in the same terminal frame `tar` uses.
//
// req.Path is the directory the archive must stay inside, as a path relative to the file
// root: every entry is compared against it before anything is created, so a caller that
// asked to write one tree cannot write another. The names in the archive are themselves
// relative to the root — which is what `tar` produces — so a GET of a tree and a PUT of
// the archive it returned describe the same files.
func (s *server) handleUntar(req *Request) error {
	if req.Path == "" {
		return s.writeFailure(CodeInvalidRequest, "untar: empty path")
	}
	reader := &frameReader{s: s}
	summary, err := extractTree(s.reqCtx, req.Path, reader)
	if err != nil {
		// The failure is reported *before* the rest of the archive is drained, and the
		// drain is bounded by the read deadline the peer set.
		//
		// Reporting it afterwards was a deadlock: a guest that filled its disk stopped
		// consuming the archive, so the caller blocked writing the rest of it, so the
		// guest never reached the drain — a 2 GiB copy into a smaller root hung forever
		// with no error, no exit status and no log line. The caller learns why as soon as
		// the write fails; what it does with the rest of its archive is its own business.
		s.logFailure("untar", req.Path, err)
		return s.writeFileFailure(err)
	}
	// A transfer that succeeded still has to reach its end before the reply, because a
	// reply sent while the connection holds the archive's tail would leave the next
	// request reading bytes of this one.
	if drainErr := reader.drain(); drainErr != nil {
		s.logFailure("untar", req.Path, drainErr)
		return s.writeFileFailure(drainErr)
	}
	return s.write(Message{Type: "file", Tar: &summary})
}

// frameReader is one request's archive as the connection carries it: file frames in
// order, until the empty frame that ends the stream. It is a reader rather than a buffer
// because archive/tar wants one, and because the point of the operation is that neither
// side ever holds the tree.
type frameReader struct {
	s    *server
	left []byte
	done bool
}

// drain reads whatever the archive still has, discarding it, so the connection is left at
// a request boundary. An archive this operation refused early is still arriving, and a
// peer that is blocked writing it cannot be told anything.
func (f *frameReader) drain() error {
	_, err := io.Copy(io.Discard, f)
	return err
}

func (f *frameReader) Read(p []byte) (int, error) {
	for len(f.left) == 0 {
		if f.done {
			return 0, io.EOF
		}
		if err := f.s.reqCtx.Err(); err != nil {
			return 0, fail(CodeDeadlineExceeded, "untar: deadline exceeded before the archive finished arriving")
		}
		var msg Message
		// An archive is a stream, so it is bounded by the stream limit rather than the
		// smaller control-frame cap.
		if _, err := ReadStreamFrame(f.s.rw, &msg); err != nil {
			return 0, fmt.Errorf("untar: read data frame: %w", err)
		}
		if msg.Type != "file" {
			return 0, fmt.Errorf("untar: unexpected frame type %q", msg.Type)
		}
		if len(msg.Data) == 0 {
			f.done = true
			return 0, io.EOF
		}
		f.left = msg.Data
	}
	n := copy(p, f.left)
	f.left = f.left[n:]
	return n, nil
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
