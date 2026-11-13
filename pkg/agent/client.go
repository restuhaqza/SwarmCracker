package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxHandshakeLine bounds the Firecracker vsock handshake response so a hostile
// peer cannot make the client allocate unbounded memory.
const maxHandshakeLine = 64

// dialRetryWindow bounds how long Dial retries the muxer handshake. Firecracker
// terminates a host-initiated connection when nothing is listening on the guest
// port yet (for example while the guest agent is still starting), so a freshly
// booted VM needs a short grace period before the CONNECT succeeds.
const dialRetryWindow = 5 * time.Second

// dialRetryInterval is the pause between muxer handshake attempts.
const dialRetryInterval = 100 * time.Millisecond

// Client is a connection to a guest agent, established through Firecracker's
// vsock Unix-socket proxy.
type Client struct {
	conn net.Conn
	mu   sync.Mutex // serialises frame writes

	// consumed is set once a TTY exec is started, and again when a streaming
	// operation ends somewhere the client cannot place. A TTY session ends the
	// connection server-side (raw stdin shares the byte stream with framed
	// requests, so the framed protocol cannot continue), so every subsequent
	// operation fails fast instead of reading a closed connection or risking a
	// protocol desync.
	consumed bool

	// hello is what the guest reported during the handshake.
	hello Hello
	// caps is hello.Caps as a set, so an operation the guest did not advertise
	// is refused before a single frame is written.
	caps map[string]bool
	// nextID numbers this connection's requests.
	nextID atomic.Uint64
}

// errConsumed is returned when a Client can no longer carry a request: a TTY exec
// shares the byte stream with framed requests, and a stream that was not read to
// its end leaves the connection at a frame boundary the client cannot trust.
var errConsumed = errors.New("client consumed: the session cannot carry another request")

// ExecOptions controls a single Client.Exec call.
type ExecOptions struct {
	TTY bool
	// Stdin is forwarded to the guest as raw bytes and is only supported for
	// TTY sessions: Exec returns an error when Stdin is non-nil and TTY is
	// false, because the protocol has no stdin frame for non-TTY commands.
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Env            []string
	WorkDir        string
}

// Dial connects to the guest agent behind a Firecracker vsock device. The host
// side always dials the muxer at udsPath itself — never "<udsPath>_<port>",
// which only exists for guest-initiated connections — sends
// "CONNECT <port>\n", and reads the "OK <host-port>\n" reply. Firecracker
// assigns that host-side port arbitrarily, so it is not necessarily port; any
// decimal value is accepted.
//
// Because Firecracker drops the connection when nothing is listening on the
// guest port yet (the guest agent may still be starting), the dial + CONNECT
// cycle is retried for up to dialRetryWindow before Dial gives up. After a
// successful CONNECT the v2 hello is negotiated, and an agent that cannot meet
// the contract — including every pre-v2 agent — is refused with
// CodeProtocolUnsupported rather than used with fewer capabilities than the
// caller asked for.
//
// When the node provisioned a per-boot token for this VM, its host-side copy
// sits next to the muxer (see TokenFilePath) and is presented in hello. A guest
// that rejects it closes the connection, which Dial reports as
// ErrTokenRejected; a guest that ignores it is refused as ErrTokenUnsupported
// rather than accepted unauthenticated. A VM with no token file dials exactly
// as before.
func Dial(ctx context.Context, udsPath string, port uint32) (*Client, error) {
	token, _, err := ReadTokenFile(TokenFilePath(udsPath))
	if err != nil {
		return nil, err
	}
	return DialWithToken(ctx, udsPath, port, token)
}

// DialWithToken is Dial with an explicit token. An empty token is an
// unauthenticated dial, which only works against a guest booted without token
// authentication.
func DialWithToken(ctx context.Context, udsPath string, port uint32, token string) (*Client, error) {
	conn, err := connectVsock(ctx, udsPath, port)
	if err != nil {
		return nil, err
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}

	c := &Client{conn: conn}
	hello, err := c.handshake(ctx, token)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	c.hello = hello
	c.caps = make(map[string]bool, len(hello.Caps))
	for _, op := range hello.Caps {
		c.caps[op] = true
	}
	return c, nil
}

// Hello returns what the guest reported during the handshake: the protocol
// versions it speaks, the operations it advertises, and its version, build and
// boot identity.
func (c *Client) Hello() Hello {
	h := c.hello
	h.Versions = append([]string(nil), c.hello.Versions...)
	h.Caps = append([]string(nil), c.hello.Caps...)
	return h
}

// handshake performs the v2 negotiation and holds the reply to the contract.
// Anything that does not describe a v2 agent — an error frame from a pre-v2
// guest, a reply that does not list this protocol version, or a missing boot
// identity — is refused, so an older agent fails here instead of failing an
// operation later or being used with a capability it never had.
//
// It is the only place a hello is built, so both entry points — Dial, which
// reads the token file next to the muxer, and DialWithToken, for a caller that
// carries a token explicitly — negotiate and authenticate identically. The token
// rides in the hello request; an empty one means an unauthenticated dial.
func (c *Client) handshake(ctx context.Context, token string) (Hello, error) {
	req := Request{Op: CapHello, Version: Version, Token: token}
	if err := c.begin(ctx, &req); err != nil {
		return Hello{}, err
	}
	if err := c.write(req); err != nil {
		return Hello{}, fmt.Errorf("send hello: %w", err)
	}

	var msg Message
	if _, err := ReadFrame(c.conn, &msg); err != nil {
		// The guest deliberately sends no frame when it rejects a token, so a
		// connection that dies here right after a token-bearing hello is the
		// only observable form of that rejection.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			if token != "" {
				return Hello{}, fmt.Errorf("%w: %v", ErrTokenRejected, err)
			}
			// The muxer accepted the connection and hello was written, so a
			// guest that hung up here is either requiring a token this node has
			// none for, or has no agent behind the port. Name both instead of
			// surfacing a bare EOF.
			return Hello{}, fmt.Errorf("read hello reply: %w (the guest may require a token this node has none for, or its agent stopped)", err)
		}
		return Hello{}, fmt.Errorf("read hello reply: %w", err)
	}
	if msg.Type == "error" {
		return Hello{}, helloFailure(msg)
	}
	if msg.Type != "hello" {
		return Hello{}, &Error{
			Code:    CodeProtocolUnsupported,
			Message: fmt.Sprintf("unexpected hello reply type %q", msg.Type),
		}
	}
	if msg.Version != Version || !slices.Contains(msg.Versions, Version) || msg.BootID == "" {
		return Hello{}, &Error{
			Code: CodeProtocolUnsupported,
			Message: fmt.Sprintf(
				"guest reports protocol %q (versions %v) and boot id %q; this host speaks %s; %s",
				msg.Version, msg.Versions, msg.BootID, Version, outdatedAgentHint),
		}
	}
	if token != "" && !msg.TokenOK {
		// The guest answered hello without acknowledging the token. It is an
		// agent that predates token authentication (or a template baked with
		// one); the node meant to authenticate this VM, so refusing is the only
		// safe answer.
		return Hello{}, ErrTokenUnsupported
	}
	if token == "" && msg.AuthRequired {
		return Hello{}, ErrTokenRequired
	}
	if msg.ID != req.ID {
		return Hello{}, &Error{
			Code:    CodeProtocolError,
			Message: fmt.Sprintf("hello reply id %q does not match request id %q", msg.ID, req.ID),
		}
	}
	return Hello{
		Version:      Version,
		Versions:     append([]string(nil), msg.Versions...),
		Caps:         append([]string(nil), msg.Caps...),
		AgentVersion: msg.AgentVersion,
		BuildID:      msg.BuildID,
		BootID:       msg.BootID,
	}, nil
}

// outdatedAgentHint is appended to a protocol-unsupported failure so the symptom
// arrives with the fix beside it. The agent is baked into the guest image, so an
// upgraded host keeps meeting the old agent in running guests until the image is
// rebuilt or the task is restarted; without this, an operator reads a bare
// version mismatch and reasonably suspects a bug.
const outdatedAgentHint = "the guest image predates this host; rebuild it or restart the task to inject the current agent"

// helloFailure turns the hello reply's error frame into a typed error. A pre-v2
// agent rejects the v2 version string with a bare error frame, so a frame that
// carries no code is exactly the "agent too old" case.
func helloFailure(msg Message) error {
	code := msg.ErrorCode
	if code == "" {
		code = CodeProtocolUnsupported
	}
	message := "hello: " + msg.Error
	if code == CodeProtocolUnsupported {
		message += "; " + outdatedAgentHint
	}
	return &Error{Code: code, Message: message}
}

// begin stamps a request with its id and the caller's remaining budget.
//
// The budget crosses the wire as a relative duration, never as an absolute
// instant: the guest's wall clock can be wrong after a restore while its
// monotonic timer is fine, so only elapsed time is meaningful across the
// boundary.
func (c *Client) begin(ctx context.Context, req *Request) error {
	req.ID = strconv.FormatUint(c.nextID.Add(1), 10)
	d, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	remaining := time.Until(d)
	if remaining <= 0 {
		return &Error{
			Code:    CodeDeadlineExceeded,
			Message: fmt.Sprintf("%s: context deadline already passed", req.Op),
		}
	}
	// A sub-millisecond budget would round to 0, which the guest reads as "no
	// deadline", so it is raised to the smallest budget the wire can carry.
	req.DeadlineMs = max(int64(remaining/time.Millisecond), 1)
	return nil
}

// requireCap refuses an operation this guest did not advertise, before any frame
// is written. There is no fallback: an operation the agent cannot serve is an
// error, never a silent substitution with one it can.
func (c *Client) requireCap(op string) error {
	if c.caps[op] {
		return nil
	}
	return &Error{
		Code: CodeUnsupportedOp,
		Message: fmt.Sprintf("guest agent does not advertise op %q (caps: %s)",
			op, strings.Join(c.hello.Caps, ",")),
	}
}

// readReply reads one frame and holds the guest to the id it echoed, so a frame
// left over from an abandoned request can never be attributed to this one.
// stream selects the streaming frame bound.
func (c *Client) readReply(id string, stream bool) (Message, error) {
	var msg Message
	var err error
	if stream {
		_, err = ReadStreamFrame(c.conn, &msg)
	} else {
		_, err = ReadFrame(c.conn, &msg)
	}
	if err != nil {
		return Message{}, err
	}
	if msg.ID != id {
		return Message{}, &Error{
			Code:    CodeProtocolError,
			Message: fmt.Sprintf("reply id %q does not match request id %q", msg.ID, id),
		}
	}
	return msg, nil
}

// frameError converts an error frame into a typed error. A frame without a code
// came from a peer that cannot type its failures, which is a guest-side fault
// rather than a caller mistake.
func frameError(op string, msg Message) error {
	code := msg.ErrorCode
	if code == "" {
		code = CodeInternal
	}
	return &Error{Code: code, Message: op + ": " + msg.Error}
}

// readError labels a failed read. A caller deadline that expired while waiting is
// reported as a typed deadline failure: the guest kills the operation at the same
// moment the host stops waiting, so whichever side notices first, the caller
// sees the same code.
func readError(ctx context.Context, op string, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Code: CodeDeadlineExceeded, Message: op + ": deadline exceeded"}
	}
	return fmt.Errorf("%s: read frame: %w", op, err)
}

// connectVsock dials the Firecracker vsock muxer at udsPath and performs the
// "CONNECT <port>" / "OK <host-port>" handshake, returning the ready
// connection. It retries while Firecracker keeps dropping the connection
// (nothing listening on the guest port), bounded by dialRetryWindow and ctx.
func connectVsock(ctx context.Context, udsPath string, port uint32) (net.Conn, error) {
	deadline := time.Now().Add(dialRetryWindow)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	var lastErr error
retry:
	for {
		conn, err := attemptConnect(ctx, udsPath, port)
		if err == nil {
			return conn, nil
		}
		lastErr = err

		// Stop once another full backoff would run past the window.
		if time.Now().Add(dialRetryInterval).After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			break retry
		case <-time.After(dialRetryInterval):
		}
	}
	return nil, fmt.Errorf("dial vsock socket %s: %w", udsPath, lastErr)
}

// attemptConnect makes a single dial + CONNECT attempt. A returned connection
// has its handshake deadline cleared and is ready for the protocol hello.
func attemptConnect(ctx context.Context, udsPath string, port uint32) (net.Conn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", udsPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", udsPath, err)
	}

	// Bound the handshake so an unresponsive muxer cannot block forever; keep
	// the caller's deadline when it is sooner.
	handshakeDeadline := time.Now().Add(dialRetryWindow)
	if d, ok := ctx.Deadline(); ok && d.Before(handshakeDeadline) {
		handshakeDeadline = d
	}
	_ = conn.SetDeadline(handshakeDeadline)

	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write vsock handshake: %w", err)
	}
	line, err := readLine(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read vsock handshake: %w", err)
	}
	if _, ok := parseOKLine(line); !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("vsock handshake failed: got %q, want \"OK <port>\"", line)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// parseOKLine reports whether line is Firecracker's "OK <decimal>" reply to a
// host-side CONNECT and returns the assigned host-side port. Firecracker assigns
// that port arbitrarily, so callers must not compare it with the requested
// guest port.
func parseOKLine(line string) (uint32, bool) {
	rest, ok := strings.CutPrefix(line, "OK ")
	if !ok || rest == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(rest, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}

// readLine reads a single '\n'-terminated line, discarding a trailing '\r'.
func readLine(r io.Reader) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for len(line) < maxHandshakeLine {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		if buf[0] == '\n' {
			return strings.TrimRight(string(line), "\r"), nil
		}
		line = append(line, buf[0])
	}
	return "", fmt.Errorf("handshake line exceeds %d bytes", maxHandshakeLine)
}

// write sends one frame, serialised against other writers.
func (c *Client) write(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return WriteFrame(c.conn, v)
}

// writeStdin writes raw stdin bytes to the connection. It takes the same mutex
// as framed writes, so a stdin chunk can never interleave with a frame, and it
// re-checks done after acquiring the mutex, so it never writes once Exec has
// returned (even if it was waiting for the mutex when done was closed).
func (c *Client) writeStdin(p []byte, done <-chan struct{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-done:
		return errStdinDone
	default:
	}
	_, err := c.conn.Write(p)
	return err
}

// errStdinDone reports that the stdin pump stopped because Exec returned.
var errStdinDone = errors.New("stdin pump stopped")

// pumpStdin copies r to the guest's TTY as raw bytes until r is exhausted, a
// write fails, or done is closed. Reading r may block past Exec's return (an
// arbitrary reader cannot be interrupted), but after done is closed no further
// bytes are written.
func (c *Client) pumpStdin(r io.Reader, done <-chan struct{}) {
	buf := make([]byte, fileChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if werr := c.writeStdin(buf[:n], done); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// ExecResult is the outcome of one guest command: its exit status and, when the
// process was terminated by a signal, that signal.
type ExecResult struct {
	ExitCode int
	Signal   int
}

// Exec runs cmd in the guest and streams its output to the writers in opts. It
// returns the guest process exit code. A guest-side failure (for example an
// unknown binary) is reported as an error, not an exit code.
//
// It is the single-shot wrapper around ExecStatus, kept for callers that only
// need the status.
func (c *Client) Exec(ctx context.Context, cmd []string, opts ExecOptions) (int, error) {
	result, err := c.ExecStatus(ctx, cmd, opts)
	if err != nil {
		return 0, err
	}
	return result.ExitCode, nil
}

// ExecStatus is Exec with the terminating signal preserved. A signal-terminated
// guest reports ExitCode 128+signal and Signal set; a normal exit reports
// Signal 0.
//
// Stdin forwarding is only supported for TTY sessions: opts.Stdin non-nil with
// opts.TTY false returns an error, because the protocol carries no stdin frame
// for non-TTY commands. When opts.Stdin is set, the bytes are written raw to the
// connection under the client's write mutex, and the pump stops when ExecStatus
// returns.
//
// A TTY exec always consumes the client: the guest ends the session after it
// (raw stdin shares the stream with framed requests), so the connection cannot
// be reused. Any later operation on it fails with errConsumed.
// A non-TTY exec leaves the client usable.
func (c *Client) ExecStatus(ctx context.Context, cmd []string, opts ExecOptions) (ExecResult, error) {
	if c.consumed {
		return ExecResult{}, errConsumed
	}
	if len(cmd) == 0 {
		return ExecResult{}, fmt.Errorf("exec: empty command")
	}
	if opts.Stdin != nil && !opts.TTY {
		return ExecResult{}, fmt.Errorf("stdin forwarding requires a TTY")
	}
	if err := c.requireCap(CapExec); err != nil {
		return ExecResult{}, err
	}
	stop := c.watchContext(ctx)
	defer stop()

	req := Request{Op: CapExec, Command: cmd, TTY: opts.TTY, Env: opts.Env, WorkDir: opts.WorkDir}
	if err := c.begin(ctx, &req); err != nil {
		return ExecResult{}, err
	}
	if err := c.write(req); err != nil {
		return ExecResult{}, fmt.Errorf("exec: send request: %w", err)
	}
	if opts.TTY {
		// The guest ends the connection after a TTY exec, so the client is no
		// longer safe to reuse regardless of whether stdin was forwarded.
		c.consumed = true
	}
	if opts.Stdin != nil {
		done := make(chan struct{})
		defer close(done)
		go c.pumpStdin(opts.Stdin, done)
	}

	for {
		msg, err := c.readReply(req.ID, true)
		if err != nil {
			return ExecResult{}, readError(ctx, "exec", err)
		}
		switch msg.Type {
		case "stdout":
			if opts.Stdout != nil {
				if _, err := opts.Stdout.Write(msg.Data); err != nil {
					return ExecResult{}, fmt.Errorf("exec: write stdout: %w", err)
				}
			}
		case "stderr":
			if opts.Stderr != nil {
				if _, err := opts.Stderr.Write(msg.Data); err != nil {
					return ExecResult{}, fmt.Errorf("exec: write stderr: %w", err)
				}
			}
		case "exit":
			return exitResult(msg)
		case "error":
			return ExecResult{}, frameError("exec", msg)
		default:
			return ExecResult{}, fmt.Errorf("exec: unexpected message type %q", msg.Type)
		}
	}
}

// exitResult converts an exit frame into the caller's result. A signal wins over
// the frame's code: the guest may report 128+N, and the client recomputes it so
// both agree even with an older guest.
func exitResult(msg Message) (ExecResult, error) {
	if msg.Signal != nil && *msg.Signal > 0 {
		signal := *msg.Signal
		return ExecResult{ExitCode: signalExitBase + signal, Signal: signal}, nil
	}
	if msg.Code == nil {
		return ExecResult{}, fmt.Errorf("exec: exit frame missing code")
	}
	return ExecResult{ExitCode: *msg.Code}, nil
}

// start is the prologue every operation shares: it refuses a client a TTY exec
// already consumed, refuses an operation the guest did not advertise (before a
// single frame is written), arms the caller's deadline, stamps the request's id
// and budget, and sends it.
//
// It returns the function that disarms the deadline watcher, which the caller must
// call once no further frame for this request can be read.
func (c *Client) start(ctx context.Context, req *Request) (func(), error) {
	if c.consumed {
		return nil, errConsumed
	}
	if err := c.requireCap(req.Op); err != nil {
		return nil, err
	}
	stop := c.watchContext(ctx)
	if err := c.begin(ctx, req); err != nil {
		stop()
		return nil, err
	}
	if err := c.write(*req); err != nil {
		stop()
		return nil, fmt.Errorf("%s: send request: %w", req.Op, err)
	}
	return stop, nil
}

// callExpect runs one request whose reply is a single frame of type want, and
// returns that frame. An error frame becomes the typed error it carries, and any
// other frame type is a protocol failure rather than a reply to be guessed at.
func (c *Client) callExpect(ctx context.Context, req Request, want string) (Message, error) {
	stop, err := c.start(ctx, &req)
	if err != nil {
		return Message{}, err
	}
	defer stop()

	msg, err := c.readReply(req.ID, false)
	if err != nil {
		return Message{}, readError(ctx, req.Op, err)
	}
	switch msg.Type {
	case want:
		return msg, nil
	case "error":
		return Message{}, frameError(req.Op, msg)
	default:
		return Message{}, fmt.Errorf("%s: unexpected message type %q", req.Op, msg.Type)
	}
}

// Metrics samples the guest's resource use through the agent's metrics op.
//
// The series it feeds are cumulative, so a single failed sample loses one
// interval rather than corrupting the value; the caller is expected to retry on
// the next tick and to treat an error as "no data", not as zeros.
func (c *Client) Metrics(ctx context.Context) (GuestMetrics, error) {
	msg, err := c.callExpect(ctx, Request{Op: CapMetrics}, "metrics")
	if err != nil {
		return GuestMetrics{}, err
	}
	if msg.Metrics == nil {
		return GuestMetrics{}, fmt.Errorf("metrics: reply carried no sample")
	}
	return *msg.Metrics, nil
}

// Prepare runs the post-restore hook: the guest reads the identity its node
// published to the metadata service and applies it, and the reply is what the
// guest carries afterwards (pkg/agent's prepare.go).
//
// The agent must advertise the operation. An agent that does not is refused here,
// before a frame is written, with CodeUnsupportedOp — never skipped, which would
// leave the caller with an instance still wearing its source's identity while
// believing the hook had run.
func (c *Client) Prepare(ctx context.Context) (PrepareReport, error) {
	msg, err := c.callExpect(ctx, Request{Op: CapPrepare}, "prepared")
	if err != nil {
		return PrepareReport{}, err
	}
	if msg.Prepared == nil {
		return PrepareReport{}, fmt.Errorf("prepare: reply carried no report")
	}
	return *msg.Prepared, nil
}

// Info reports what the guest knows about itself: its identity, kernel,
// architecture, memory and vCPUs, uptime, and the toolchain its template ships
// (pkg/agent's info.go).
//
// The agent must advertise the operation; an agent that does not is refused here
// with CodeUnsupportedOp rather than answered with a narrower one, so a caller can
// never mistake a missing report for a guest that has nothing to say.
func (c *Client) Info(ctx context.Context) (GuestInfo, error) {
	msg, err := c.callExpect(ctx, Request{Op: CapInfo}, "info")
	if err != nil {
		return GuestInfo{}, err
	}
	if msg.Info == nil {
		return GuestInfo{}, fmt.Errorf("info: reply carried no report")
	}
	return *msg.Info, nil
}

// Net reports the guest's own view of its network: its interfaces and addresses,
// the routes it would use, and the resolver it consults (pkg/agent's netinfo.go).
func (c *Client) Net(ctx context.Context) (GuestNet, error) {
	msg, err := c.callExpect(ctx, Request{Op: CapNet}, "net")
	if err != nil {
		return GuestNet{}, err
	}
	if msg.Net == nil {
		return GuestNet{}, fmt.Errorf("net: reply carried no report")
	}
	return *msg.Net, nil
}

// Stat describes the entry at path, which is relative to the guest's file root
// (/workspace) and may not leave it. A symbolic link is reported as a link, with
// its target and without its content (pkg/agent's files.go).
func (c *Client) Stat(ctx context.Context, path string) (FileEntry, error) {
	msg, err := c.callExpect(ctx, Request{Op: CapStat, Path: path}, "stat")
	if err != nil {
		return FileEntry{}, err
	}
	if msg.Stat == nil {
		return FileEntry{}, fmt.Errorf("stat: reply carried no entry")
	}
	return *msg.Stat, nil
}

// List lists the directory at path, which is relative to the guest's file root
// (/workspace) and may not leave it. Entries are sorted by name, and a directory
// with more than the operation's bound of them is reported as truncated rather
// than silently shortened.
//
// The listing arrives in batches, which are collected here: a caller reads one
// answer, and the frame size never depends on how many entries the directory
// holds.
func (c *Client) List(ctx context.Context, path string) (Listing, error) {
	req := Request{Op: CapList, Path: path}
	stop, err := c.start(ctx, &req)
	if err != nil {
		return Listing{}, err
	}
	defer stop()

	var listing Listing
	for {
		msg, err := c.readReply(req.ID, false)
		if err != nil {
			return Listing{}, readError(ctx, req.Op, err)
		}
		switch msg.Type {
		case "entries":
			if len(msg.Entries) == 0 {
				// The terminal batch ends the listing and is the only frame that
				// carries whether the bound cut it short.
				listing.Truncated = msg.Truncated
				return listing, nil
			}
			listing.Entries = append(listing.Entries, msg.Entries...)
		case "error":
			return Listing{}, frameError(req.Op, msg)
		default:
			return Listing{}, fmt.Errorf("%s: unexpected message type %q", req.Op, msg.Type)
		}
	}
}

// Tar streams the tree at path as a POSIX pax tar archive into w, and returns the
// summary that ended the stream: how many entries it carried, how many bytes, and
// which entries it could not carry and why.
//
// The path is relative to the guest's file root (/workspace) and may not leave
// it; names inside the archive are relative to the requested path, so extracting
// it into a directory lands the tree under the name that was asked for.
//
// A stream that fails partway returns an error and no summary, and the bytes
// already written to w are a fragment, never an archive padded into looking
// complete. The client is left with its stream state unknown, so it refuses every
// later operation unless it could read the stream to its end: a caller that
// abandons an archive has to dial again.
func (c *Client) Tar(ctx context.Context, path string, w io.Writer) (TarSummary, error) {
	req := Request{Op: CapTar, Path: path}
	stop, err := c.start(ctx, &req)
	if err != nil {
		return TarSummary{}, err
	}
	defer stop()

	for {
		msg, err := c.readReply(req.ID, true)
		if err != nil {
			// Frames of this archive may still be in flight, so the id of the
			// next frame this connection reads cannot be trusted to be the next
			// request's. The client says so rather than returning a session that
			// looks usable.
			c.consumed = true
			return TarSummary{}, readError(ctx, req.Op, err)
		}
		switch msg.Type {
		case "file":
			if len(msg.Data) == 0 {
				if msg.Tar == nil {
					return TarSummary{}, fmt.Errorf("tar: stream ended without a summary")
				}
				return *msg.Tar, nil
			}
			if _, err := w.Write(msg.Data); err != nil {
				return TarSummary{}, c.abandon(req.ID, err)
			}
		case "error":
			return TarSummary{}, frameError(req.Op, msg)
		default:
			c.consumed = true
			return TarSummary{}, fmt.Errorf("%s: unexpected message type %q", req.Op, msg.Type)
		}
	}
}

// abandon ends a tar stream the caller's own writer refused. The guest is still
// sending, so the rest of the stream is read and dropped: that is what leaves the
// connection at a frame boundary, and a session a caller can go on using after a
// full disk is worth the read. When the stream cannot be drained the client is
// left unusable instead of guessing where the next frame starts.
func (c *Client) abandon(id string, cause error) error {
	for {
		msg, err := c.readReply(id, true)
		if err != nil {
			c.consumed = true
			return fmt.Errorf("tar: write archive: %w (the stream could not be drained: %v)", cause, err)
		}
		if msg.Type == "error" {
			return fmt.Errorf("tar: write archive: %w (the guest stopped the archive: %s)", cause, msg.Error)
		}
		if msg.Type != "file" {
			c.consumed = true
			return fmt.Errorf("tar: write archive: %w (unexpected message type %q)", cause, msg.Type)
		}
		if len(msg.Data) == 0 {
			return fmt.Errorf("tar: write archive: %w", cause)
		}
	}
}

// Activity reports what the sandbox is doing right now: the operations the agent
// is serving, how long it has been idle, and how many processes the guest has
// (pkg/agent's activity.go). It is what an idle reaper asks before it takes a
// sandbox's resources away.
func (c *Client) Activity(ctx context.Context) (Activity, error) {
	msg, err := c.callExpect(ctx, Request{Op: CapActivity}, "activity")
	if err != nil {
		return Activity{}, err
	}
	if msg.Activity == nil {
		return Activity{}, fmt.Errorf("activity: reply carried no report")
	}
	return *msg.Activity, nil
}

// Quiesce stops (QuiesceFreeze) or releases (QuiesceThaw) the guest's writers, and
// reports which filesystems it acted on (pkg/agent's quiesce.go). A node calls it
// around a checkpoint: freeze, take the images, thaw.
//
// A refusal is an error rather than a report: an image captured while the guest was
// still writing is a pair of images that disagree, so a caller that cannot freeze must
// not checkpoint.
func (c *Client) Quiesce(ctx context.Context, action string) (QuiesceReport, error) {
	msg, err := c.callExpect(ctx, Request{Op: CapQuiesce, Quiesce: action}, "quiesced")
	if err != nil {
		return QuiesceReport{}, err
	}
	if msg.Quiesced == nil {
		return QuiesceReport{}, fmt.Errorf("quiesce: reply carried no report")
	}
	return *msg.Quiesced, nil
}

// CopyTo streams src to guestPath with the given mode (0644 when mode is 0).
func (c *Client) CopyTo(ctx context.Context, guestPath string, src io.Reader, mode uint32) error {
	if c.consumed {
		return errConsumed
	}
	if guestPath == "" {
		return fmt.Errorf("copy: empty guest path")
	}
	if err := c.requireCap(CapPut); err != nil {
		return err
	}
	stop := c.watchContext(ctx)
	defer stop()

	req := Request{Op: CapPut, Path: guestPath, Mode: mode}
	if err := c.begin(ctx, &req); err != nil {
		return err
	}
	if err := c.write(req); err != nil {
		return fmt.Errorf("copy: send put request: %w", err)
	}

	buf := make([]byte, fileChunkSize)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if werr := c.write(Message{Type: "file", Data: buf[:n]}); werr != nil {
				return fmt.Errorf("copy: send data frame: %w", werr)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("copy: read source: %w", err)
		}
	}
	if err := c.write(Message{Type: "file"}); err != nil {
		return fmt.Errorf("copy: send end-of-file frame: %w", err)
	}

	msg, err := c.readReply(req.ID, false)
	if err != nil {
		return readError(ctx, "copy", err)
	}
	switch msg.Type {
	case "ok":
		return nil
	case "error":
		return frameError("copy", msg)
	default:
		return fmt.Errorf("copy: unexpected reply type %q", msg.Type)
	}
}

// CopyFrom streams guestPath from the guest into dst.
func (c *Client) CopyFrom(ctx context.Context, guestPath string, dst io.Writer) error {
	if c.consumed {
		return errConsumed
	}
	if guestPath == "" {
		return fmt.Errorf("copy: empty guest path")
	}
	stop := c.watchContext(ctx)
	defer stop()

	if err := c.requireCap(CapGet); err != nil {
		return err
	}
	req := Request{Op: CapGet, Path: guestPath}
	if err := c.begin(ctx, &req); err != nil {
		return err
	}
	if err := c.write(req); err != nil {
		return fmt.Errorf("copy: send get request: %w", err)
	}

	for {
		msg, err := c.readReply(req.ID, true)
		if err != nil {
			return readError(ctx, "copy", err)
		}
		switch msg.Type {
		case "file":
			if len(msg.Data) == 0 {
				return nil
			}
			if dst != nil {
				if _, err := dst.Write(msg.Data); err != nil {
					return fmt.Errorf("copy: write destination: %w", err)
				}
			}
		case "error":
			return frameError("copy", msg)
		default:
			return fmt.Errorf("copy: unexpected message type %q", msg.Type)
		}
	}
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// watchContext arranges for ctx cancellation to unblock the connection. The
// returned function stops the watcher and clears any deadline it set.
func (c *Client) watchContext(ctx context.Context) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.conn.SetDeadline(time.Now())
		case <-done:
		}
	}()
	return func() {
		close(done)
		_ = c.conn.SetDeadline(time.Time{})
	}
}
