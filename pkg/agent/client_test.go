package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortTempDir creates a temporary directory with a short path; Unix socket
// paths are limited to ~104 bytes, which t.TempDir() can exceed on macOS.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sca-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// assignedHostPort is the arbitrary host-side port the fake proxy reports in
// its "OK" reply. It is deliberately different from any requested guest port so
// the tests fail if the client compares the two.
const assignedHostPort = 1073741824

// fakeProxy emulates Firecracker's vsock Unix-socket muxer: it listens on the
// uds_path itself, performs the CONNECT/OK handshake, and then bridges to a
// guest agent via ServeConn.
type fakeProxy struct {
	t         *testing.T
	ln        net.Listener
	port      uint32
	okPort    uint32
	helloVer  string
	helloType string
	helloErr  string
	badOK     bool
	raw       func(net.Conn)

	// dropsRemaining models Firecracker dropping the host connection because
	// nothing is listening on the guest port yet: each dropped accept closes
	// without replying, so the client must retry.
	dropsRemaining atomic.Int32

	mu        sync.Mutex
	handshake string
}

type proxyOption func(*fakeProxy)

func startProxy(t *testing.T, port uint32, opts ...proxyOption) (*fakeProxy, string) {
	t.Helper()
	p := &fakeProxy{t: t, port: port, okPort: assignedHostPort, helloVer: Version}
	for _, opt := range opts {
		opt(p)
	}
	return p.listen(t, port)
}

// startRawProxy bridges the handshake and hello exchange to a caller-supplied
// handler, which can script arbitrary reply frames.
func startRawProxy(t *testing.T, port uint32, raw func(net.Conn)) (*fakeProxy, string) {
	t.Helper()
	p := &fakeProxy{t: t, port: port, okPort: assignedHostPort, helloVer: Version, raw: raw}
	return p.listen(t, port)
}

func (p *fakeProxy) listen(t *testing.T, port uint32) (*fakeProxy, string) {
	t.Helper()
	udsPath := filepath.Join(shortTempDir(t), "vm.vsock")
	ln, err := net.Listen("unix", udsPath)
	require.NoError(t, err)
	p.ln = ln
	t.Cleanup(func() { _ = ln.Close() })

	go p.serve()
	return p, udsPath
}

func (p *fakeProxy) serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(conn)
	}
}

func (p *fakeProxy) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	// Drop this connection without replying, as Firecracker does when the guest
	// has not started listening on the port yet.
	if p.dropsRemaining.Add(-1) >= 0 {
		return
	}

	line, err := readLine(conn)
	if err != nil {
		return
	}
	p.mu.Lock()
	p.handshake = line
	p.mu.Unlock()

	if p.badOK {
		_, _ = io.WriteString(conn, "ERR nope\n")
		return
	}
	if line != fmt.Sprintf("CONNECT %d", p.port) {
		_, _ = io.WriteString(conn, "ERR wrong port\n")
		return
	}
	if _, err := fmt.Fprintf(conn, "OK %d\n", p.okPort); err != nil {
		return
	}

	if p.helloVer != Version || p.helloType != "" || p.helloErr != "" {
		var req Request
		if _, err := ReadFrame(conn, &req); err != nil {
			return
		}
		if p.helloErr != "" {
			_ = WriteFrame(conn, Message{Type: "error", Error: p.helloErr})
			return
		}
		typ := p.helloType
		if typ == "" {
			typ = "hello"
		}
		_ = WriteFrame(conn, Message{Type: typ, Version: p.helloVer})
		return
	}

	if p.raw != nil {
		// Complete the hello exchange, then hand the connection to the script.
		var req Request
		if _, err := ReadFrame(conn, &req); err != nil {
			return
		}
		_ = WriteFrame(conn, Message{Type: "hello", Version: Version})
		p.raw(conn)
		return
	}
	_ = ServeConn(conn)
}

func (p *fakeProxy) handshakeLine() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handshake
}

func TestClientDialWritesHandshake(t *testing.T) {
	p, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	assert.Equal(t, fmt.Sprintf("CONNECT %d", DefaultPort), p.handshakeLine())
}

func TestClientExecStreamsOutputAndCode(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	var stdout, stderr bytes.Buffer
	code, err := c.Exec(context.Background(),
		[]string{"sh", "-c", "echo out; echo err 1>&2; exit 2"},
		ExecOptions{Stdout: &stdout, Stderr: &stderr})
	require.NoError(t, err)
	assert.Equal(t, 2, code)
	assert.Equal(t, "out\n", stdout.String())
	assert.Equal(t, "err\n", stderr.String())
}

func TestClientExecEnvAndWorkDir(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "marker"), []byte("m"), 0o644))

	var stdout bytes.Buffer
	code, err := c.Exec(context.Background(), []string{"sh", "-c", `printf '%s' "$FOO"; cat marker`},
		ExecOptions{Stdout: &stdout, Env: []string{"FOO=bar"}, WorkDir: dir})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "barm", stdout.String())
}

func TestClientExecServerError(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	_, err = c.Exec(context.Background(), []string{"definitely-not-a-real-binary-xyz"}, ExecOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exec:")
}

func TestClientExecEmptyCommand(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	_, err = c.Exec(context.Background(), nil, ExecOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty command")
}

func TestClientExecNilWriters(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	// Output is discarded when no writers are supplied.
	code, err := c.Exec(context.Background(), []string{"echo", "ignored"}, ExecOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
}

// TestClientNonTTYExecKeepsConnectionUsable asserts that a non-TTY exec does
// not end the session: the same client can run a second command.
func TestClientNonTTYExecKeepsConnectionUsable(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	var first bytes.Buffer
	code, err := c.Exec(context.Background(), []string{"echo", "one"}, ExecOptions{Stdout: &first})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "one\n", first.String())

	var second bytes.Buffer
	code, err = c.Exec(context.Background(), []string{"echo", "two"}, ExecOptions{Stdout: &second})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "two\n", second.String())
}

func TestClientExecNonTTYStdinRejected(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	_, err = c.Exec(context.Background(), []string{"cat"},
		ExecOptions{Stdin: strings.NewReader("data"), TTY: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin forwarding requires a TTY")

	// Nothing was sent, so the connection is still usable: had Exec written the
	// request or the stdin bytes, this next exec would read stale frames.
	var stdout bytes.Buffer
	code, err := c.Exec(context.Background(), []string{"echo", "ok"}, ExecOptions{Stdout: &stdout})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "ok\n", stdout.String())
}

func TestClientCopyToAndCopyFrom(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	path := filepath.Join(t.TempDir(), "uploaded.txt")
	require.NoError(t, c.CopyTo(context.Background(), path, strings.NewReader("payload"), 0o600))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(got))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	var buf bytes.Buffer
	require.NoError(t, c.CopyFrom(context.Background(), path, &buf))
	assert.Equal(t, "payload", buf.String())
}

func TestClientCopyToEmptyPath(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	err = c.CopyTo(context.Background(), "", strings.NewReader("x"), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty guest path")
}

func TestClientCopyFromMissingFile(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	var buf bytes.Buffer
	err = c.CopyFrom(context.Background(), filepath.Join(t.TempDir(), "nope"), &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copy:")
}

func TestClientCopyFromEmptyPath(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	err = c.CopyFrom(context.Background(), "", &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty guest path")
}

func TestClientDialVersionMismatch(t *testing.T) {
	_, uds := startProxy(t, DefaultPort, func(p *fakeProxy) { p.helloVer = "2" })

	_, err := Dial(context.Background(), uds, DefaultPort)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent version mismatch: client "+Version+", guest 2")
}

// retryDialCtx bounds a Dial that is expected to fail, so the retry window does
// not make the test suite slow.
func retryDialCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func TestClientDialBadHandshake(t *testing.T) {
	_, uds := startProxy(t, DefaultPort, func(p *fakeProxy) { p.badOK = true })

	_, err := Dial(retryDialCtx(t), uds, DefaultPort)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handshake")
}

// TestClientDialAcceptsAssignedHostPort asserts that the client dials the muxer
// and accepts the arbitrary host-side port Firecracker assigns, rather than
// requiring it to equal the requested guest port.
func TestClientDialAcceptsAssignedHostPort(t *testing.T) {
	p, uds := startProxy(t, DefaultPort)
	require.NotEqual(t, p.port, p.okPort,
		"the fake proxy must report a host-side port different from the guest port")

	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	assert.Equal(t, fmt.Sprintf("CONNECT %d", DefaultPort), p.handshakeLine())
}

// TestClientDialRetriesUntilAgentListens models a guest agent that is still
// starting: Firecracker drops the first host connection, and the client must
// retry the handshake until the agent answers.
func TestClientDialRetriesUntilAgentListens(t *testing.T) {
	p, uds := startProxy(t, DefaultPort)
	p.dropsRemaining.Store(1)

	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	assert.Equal(t, fmt.Sprintf("CONNECT %d", DefaultPort), p.handshakeLine())
}

func TestClientDialNoSocket(t *testing.T) {
	_, err := Dial(retryDialCtx(t), filepath.Join(shortTempDir(t), "missing.vsock"), DefaultPort)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dial")
}

func TestParseOKLine(t *testing.T) {
	tests := []struct {
		line string
		port uint32
		ok   bool
	}{
		{"OK 52", 52, true},
		{"OK 1073741824", 1073741824, true},
		{"OK 0", 0, true},
		{"OK", 0, false},
		{"OK ", 0, false},
		{"OK abc", 0, false},
		{"OK -1", 0, false},
		{"ERR nope", 0, false},
		{"RST", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			port, ok := parseOKLine(tt.line)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.port, port)
		})
	}
}

func TestClientClose(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)

	assert.NoError(t, c.Close())
}

func TestReadLineTooLong(t *testing.T) {
	_, err := readLine(strings.NewReader(strings.Repeat("a", maxHandshakeLine+1)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestClientDialUnexpectedHelloType(t *testing.T) {
	_, uds := startProxy(t, DefaultPort, func(p *fakeProxy) { p.helloType = "ok" })

	_, err := Dial(context.Background(), uds, DefaultPort)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected hello reply type")
}

func TestClientDialHelloError(t *testing.T) {
	_, uds := startProxy(t, DefaultPort, func(p *fakeProxy) { p.helloErr = "agent version mismatch: client 1, guest 9" })

	_, err := Dial(context.Background(), uds, DefaultPort)
	require.Error(t, err)
	assert.EqualError(t, err, "agent version mismatch: client 1, guest 9")
}

func TestClientExecExitFrameMissingCode(t *testing.T) {
	_, uds := startRawProxy(t, DefaultPort, func(conn net.Conn) {
		var req Request
		_, _ = ReadFrame(conn, &req)
		_ = WriteFrame(conn, Message{Type: "exit"})
	})
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	_, err = c.Exec(context.Background(), []string{"true"}, ExecOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing code")
}

func TestClientExecUnexpectedMessageType(t *testing.T) {
	_, uds := startRawProxy(t, DefaultPort, func(conn net.Conn) {
		var req Request
		_, _ = ReadFrame(conn, &req)
		_ = WriteFrame(conn, Message{Type: "hello", Version: Version})
	})
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	_, err = c.Exec(context.Background(), []string{"true"}, ExecOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected message type")
}

func TestClientExecStdoutWriteError(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	_, err = c.Exec(context.Background(), []string{"echo", "hi"},
		ExecOptions{Stdout: errWriter{err: errors.New("sink full")}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write stdout")
}

func TestClientCopyToSourceError(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	err = c.CopyTo(context.Background(), filepath.Join(t.TempDir(), "f"), errReader{err: errors.New("source broke")}, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read source")
}

func TestClientCopyToServerError(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	// A directory is not writable with O_CREATE|O_TRUNC, so the guest replies
	// with an error frame.
	err = c.CopyTo(context.Background(), t.TempDir(), strings.NewReader("x"), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copy:")
}

func TestClientCopyToUnexpectedReply(t *testing.T) {
	_, uds := startRawProxy(t, DefaultPort, func(conn net.Conn) {
		var req Request
		_, _ = ReadFrame(conn, &req)
		// Consume the terminal empty file frame, then reply with the wrong type.
		var msg Message
		_, _ = ReadFrame(conn, &msg)
		_ = WriteFrame(conn, Message{Type: "hello", Version: Version})
	})
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	err = c.CopyTo(context.Background(), "/guest/f", strings.NewReader(""), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected reply type")
}

func TestClientCopyFromWriteError(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	path := filepath.Join(t.TempDir(), "download.txt")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o644))

	err = c.CopyFrom(context.Background(), path, errWriter{err: errors.New("sink full")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write destination")
}

func TestClientCopyFromUnexpectedMessageType(t *testing.T) {
	_, uds := startRawProxy(t, DefaultPort, func(conn net.Conn) {
		var req Request
		_, _ = ReadFrame(conn, &req)
		_ = WriteFrame(conn, Message{Type: "stdout", Data: []byte("nope")})
	})
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	err = c.CopyFrom(context.Background(), "/guest/f", &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected message type")
}

func TestWatchContextCancellation(t *testing.T) {
	local, remote := net.Pipe()
	defer func() { _ = local.Close() }()
	defer func() { _ = remote.Close() }()

	c := &Client{conn: local}
	ctx, cancel := context.WithCancel(context.Background())
	stop := c.watchContext(ctx)

	readErr := make(chan error, 1)
	go func() {
		_, err := local.Read(make([]byte, 1))
		readErr <- err
	}()

	cancel()
	select {
	case err := <-readErr:
		require.Error(t, err, "a blocked read must be unblocked by context cancellation")
	case <-time.After(2 * time.Second):
		t.Fatal("read was not unblocked")
	}
	stop()
}

func TestWatchContextStopWithoutCancel(t *testing.T) {
	local, remote := net.Pipe()
	defer func() { _ = local.Close() }()
	defer func() { _ = remote.Close() }()

	c := &Client{conn: local}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// stop must clear the deadline and stop the watcher without panicking.
	c.watchContext(ctx)()
}

// errWriter always fails, exercising client write-error paths.
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

// errReader always fails, exercising client read-error paths.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
