package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// maxHandshakeLine bounds the Firecracker vsock handshake response so a hostile
// peer cannot make the client allocate unbounded memory.
const maxHandshakeLine = 64

// Client is a connection to a guest agent, established through Firecracker's
// vsock Unix-socket proxy.
type Client struct {
	conn net.Conn
	mu   sync.Mutex // serialises frame writes
}

// ExecOptions controls a single Client.Exec call.
type ExecOptions struct {
	TTY            bool
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Env            []string
	WorkDir        string
}

// Dial connects to the guest agent behind a Firecracker vsock device. It dials
// the proxy socket at "<udsPath>_<port>", performs the "CONNECT <port>" /
// "OK <port>" handshake and exchanges the protocol hello, failing on a version
// mismatch.
func Dial(ctx context.Context, udsPath string, port uint32) (*Client, error) {
	addr := fmt.Sprintf("%s_%d", udsPath, port)
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", addr)
	if err != nil {
		return nil, fmt.Errorf("dial vsock socket %s: %w", addr, err)
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}

	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write vsock handshake: %w", err)
	}
	line, err := readLine(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read vsock handshake: %w", err)
	}
	if want := fmt.Sprintf("OK %d", port); line != want {
		_ = conn.Close()
		return nil, fmt.Errorf("vsock handshake failed: got %q, want %q", line, want)
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

// Exec runs cmd in the guest and streams its output to the writers in opts. It
// returns the guest process exit code. A guest-side failure (for example an
// unknown binary) is reported as an error, not an exit code.
//
// When opts.Stdin is non-nil it is copied to the connection as raw bytes; that
// is only meaningful for an interactive TTY session, and such a session must
// not reuse the client for further requests.
func (c *Client) Exec(ctx context.Context, cmd []string, opts ExecOptions) (int, error) {
	if len(cmd) == 0 {
		return 0, fmt.Errorf("exec: empty command")
	}
	stop := c.watchContext(ctx)
	defer stop()

	req := Request{Op: "exec", Command: cmd, TTY: opts.TTY, Env: opts.Env, WorkDir: opts.WorkDir}
	if err := c.write(req); err != nil {
		return 0, fmt.Errorf("exec: send request: %w", err)
	}
	if opts.Stdin != nil {
		go func() { _, _ = io.Copy(c.conn, opts.Stdin) }()
	}

	for {
		var msg Message
		if _, err := ReadFrame(c.conn, &msg); err != nil {
			return 0, fmt.Errorf("exec: read frame: %w", err)
		}
		switch msg.Type {
		case "stdout":
			if opts.Stdout != nil {
				if _, err := opts.Stdout.Write(msg.Data); err != nil {
					return 0, fmt.Errorf("exec: write stdout: %w", err)
				}
			}
		case "stderr":
			if opts.Stderr != nil {
				if _, err := opts.Stderr.Write(msg.Data); err != nil {
					return 0, fmt.Errorf("exec: write stderr: %w", err)
				}
			}
		case "exit":
			if msg.Code == nil {
				return 0, fmt.Errorf("exec: exit frame missing code")
			}
			return *msg.Code, nil
		case "error":
			return 0, fmt.Errorf("exec: %s", msg.Error)
		default:
			return 0, fmt.Errorf("exec: unexpected message type %q", msg.Type)
		}
	}
}

// CopyTo streams src to guestPath with the given mode (0644 when mode is 0).
func (c *Client) CopyTo(ctx context.Context, guestPath string, src io.Reader, mode uint32) error {
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
