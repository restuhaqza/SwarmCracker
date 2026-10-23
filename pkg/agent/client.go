package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
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

	// consumed is set once a TTY exec is started. A TTY session ends the
	// connection server-side (raw stdin shares the byte stream with framed
	// requests, so the framed protocol cannot continue), so every subsequent
	// operation fails fast instead of reading a closed connection or risking a
	// protocol desync.
	consumed bool
}

// errConsumed is returned when a Client is used after a TTY exec.
var errConsumed = errors.New("client consumed by a TTY exec")

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
// successful CONNECT the protocol hello is exchanged and a version mismatch
// fails clearly.
func Dial(ctx context.Context, udsPath string, port uint32) (*Client, error) {
	conn, err := connectVsock(ctx, udsPath, port)
	if err != nil {
		return nil, err
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}

	c := &Client{conn: conn}
	if err := c.write(Request{Op: "hello", Version: Version}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("send hello: %w", err)
	}

	var msg Message
	if _, err := ReadFrame(conn, &msg); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read hello reply: %w", err)
	}
	if msg.Type == "error" {
		_ = conn.Close()
		return nil, errors.New(msg.Error)
	}
	if msg.Type != "hello" {
		_ = conn.Close()
		return nil, fmt.Errorf("unexpected hello reply type %q", msg.Type)
	}
	if msg.Version != Version {
		_ = conn.Close()
		return nil, fmt.Errorf("agent version mismatch: client %s, guest %s", Version, msg.Version)
	}
	return c, nil
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
// be reused. Any later Exec/ExecStatus/CopyTo/CopyFrom fails with errConsumed.
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
	stop := c.watchContext(ctx)
	defer stop()

	req := Request{Op: "exec", Command: cmd, TTY: opts.TTY, Env: opts.Env, WorkDir: opts.WorkDir}
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
		var msg Message
		if _, err := ReadFrame(c.conn, &msg); err != nil {
			return ExecResult{}, fmt.Errorf("exec: read frame: %w", err)
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
			return ExecResult{}, fmt.Errorf("exec: %s", msg.Error)
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

// CopyTo streams src to guestPath with the given mode (0644 when mode is 0).
func (c *Client) CopyTo(ctx context.Context, guestPath string, src io.Reader, mode uint32) error {
	if c.consumed {
		return errConsumed
	}
	if guestPath == "" {
		return fmt.Errorf("copy: empty guest path")
	}
	stop := c.watchContext(ctx)
	defer stop()

	if err := c.write(Request{Op: "put", Path: guestPath, Mode: mode}); err != nil {
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

	var msg Message
	if _, err := ReadFrame(c.conn, &msg); err != nil {
		return fmt.Errorf("copy: read reply: %w", err)
	}
	switch msg.Type {
	case "ok":
		return nil
	case "error":
		return fmt.Errorf("copy: %s", msg.Error)
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

	if err := c.write(Request{Op: "get", Path: guestPath}); err != nil {
		return fmt.Errorf("copy: send get request: %w", err)
	}

	for {
		var msg Message
		if _, err := ReadFrame(c.conn, &msg); err != nil {
			return fmt.Errorf("copy: read frame: %w", err)
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
			return fmt.Errorf("copy: %s", msg.Error)
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
