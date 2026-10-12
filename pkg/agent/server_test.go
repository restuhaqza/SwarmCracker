package agent

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPipePair returns a connected client/server pair with ServeConn running on
// the server end. The client end is closed on cleanup, which unblocks ServeConn.
func newPipePair(t *testing.T) (net.Conn, <-chan error) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeConn(server) }()
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("ServeConn did not return after the client closed")
		}
	})
	return client, done
}

func readMessage(t *testing.T, r net.Conn) Message {
	t.Helper()
	var msg Message
	got, err := ReadFrame(r, &msg)
	require.NoError(t, err)
	require.NotNil(t, got)
	return msg
}

// runExec writes an exec request and collects output until the exit frame.
func runExec(t *testing.T, conn net.Conn, req Request) (stdout, stderr string, code int) {
	t.Helper()
	require.NoError(t, WriteFrame(conn, req))
	var out, errBuf bytes.Buffer
	for {
		msg := readMessage(t, conn)
		switch msg.Type {
		case "stdout":
			out.Write(msg.Data)
		case "stderr":
			errBuf.Write(msg.Data)
		case "exit":
			require.NotNil(t, msg.Code, "exit frame must carry a code")
			return out.String(), errBuf.String(), *msg.Code
		case "error":
			t.Fatalf("unexpected error frame: %s", msg.Error)
		default:
			t.Fatalf("unexpected message type %q", msg.Type)
		}
	}
}

func TestServeConnHello(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: Version}))

	msg := readMessage(t, conn)
	assert.Equal(t, "hello", msg.Type)
	assert.Equal(t, Version, msg.Version)
}

func TestServeConnHelloVersionMismatch(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: "2"}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Contains(t, msg.Error, "agent version mismatch")
	assert.Contains(t, msg.Error, "client 2, guest "+Version)
}

func TestServeConnExecStreamsOutputAndExit(t *testing.T) {
	conn, _ := newPipePair(t)
	stdout, stderr, code := runExec(t, conn, Request{
		Op:      "exec",
		Command: []string{"sh", "-c", "echo out; echo err 1>&2; exit 3"},
	})

	assert.Equal(t, "out\n", stdout)
	assert.Equal(t, "err\n", stderr)
	assert.Equal(t, 3, code)
}

func TestServeConnExecSuccessCode(t *testing.T) {
	conn, _ := newPipePair(t)
	stdout, stderr, code := runExec(t, conn, Request{
		Op:      "exec",
		Command: []string{"sh", "-c", "echo hi"},
	})
	assert.Equal(t, "hi\n", stdout)
	assert.Empty(t, stderr)
	assert.Equal(t, 0, code)
}

func TestServeConnExecEnvAndWorkDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("marker"), 0o644))

	conn, _ := newPipePair(t)
	stdout, _, code := runExec(t, conn, Request{
		Op:      "exec",
		Command: []string{"sh", "-c", `printf '%s' "$FOO"; cat marker.txt`},
		Env:     []string{"FOO=bar"},
		WorkDir: dir,
	})
	assert.Equal(t, "barmarker", stdout)
	assert.Equal(t, 0, code)
}

func TestServeConnExecEmptyCommand(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "exec"}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Contains(t, msg.Error, "empty command")
}

func TestServeConnExecMissingBinary(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "exec", Command: []string{"definitely-not-a-real-binary-xyz"}}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Contains(t, msg.Error, "start")
}

func TestServeConnPutThenGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file.txt")
	content := []byte("hello guest agent")

	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "put", Path: path, Mode: 0o640}))
	require.NoError(t, WriteFrame(conn, Message{Type: "file", Data: content[:5]}))
	require.NoError(t, WriteFrame(conn, Message{Type: "file", Data: content[5:]}))
	require.NoError(t, WriteFrame(conn, Message{Type: "file"}))

	msg := readMessage(t, conn)
	require.Equal(t, "ok", msg.Type)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, got)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())

	// get round-trips the same bytes.
	require.NoError(t, WriteFrame(conn, Request{Op: "get", Path: path}))
	var out bytes.Buffer
	for {
		m := readMessage(t, conn)
		require.Equal(t, "file", m.Type)
		if len(m.Data) == 0 {
			break
		}
		out.Write(m.Data)
	}
	assert.Equal(t, content, out.Bytes())
}

func TestServeConnPutDefaultMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "default.txt")

	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "put", Path: path}))
	require.NoError(t, WriteFrame(conn, Message{Type: "file", Data: []byte("x")}))
	require.NoError(t, WriteFrame(conn, Message{Type: "file"}))
	require.Equal(t, "ok", readMessage(t, conn).Type)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestServeConnPutEmptyPath(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "put"}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Contains(t, msg.Error, "empty path")
}

func TestServeConnPutUnexpectedFrameType(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "put", Path: filepath.Join(t.TempDir(), "f")}))
	require.NoError(t, WriteFrame(conn, Message{Type: "stdout", Data: []byte("nope")}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Contains(t, msg.Error, "unexpected frame type")
}

func TestServeConnGetMissingFile(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "get", Path: filepath.Join(t.TempDir(), "nope.txt")}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Contains(t, msg.Error, "open")
}

func TestServeConnGetEmptyPath(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "get"}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Contains(t, msg.Error, "empty path")
}

func TestServeConnUnknownOp(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "frobnicate"}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Contains(t, msg.Error, "unknown op")
}

func TestServeConnHandlesMultipleRequests(t *testing.T) {
	conn, _ := newPipePair(t)

	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: Version}))
	require.Equal(t, "hello", readMessage(t, conn).Type)

	stdout, _, code := runExec(t, conn, Request{Op: "exec", Command: []string{"echo", "again"}})
	assert.Equal(t, "again\n", stdout)
	assert.Equal(t, 0, code)

	// A third request still works on the same connection.
	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: Version}))
	require.Equal(t, "hello", readMessage(t, conn).Type)
}

func TestServeConnReturnsOnClientClose(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeConn(server) }()

	require.NoError(t, client.Close())
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ServeConn did not return on client close")
	}
}

func TestServeConnInvalidRequestFrame(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeConn(server) }()
	defer func() { _ = client.Close() }()

	body := []byte("{not valid json")
	var buf bytes.Buffer
	require.NoError(t, binary.Write(&buf, binary.BigEndian, uint32(len(body))))
	buf.Write(body)
	_, err := client.Write(buf.Bytes())
	require.NoError(t, err)

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unmarshal")
	case <-time.After(2 * time.Second):
		t.Fatal("ServeConn did not return on an invalid request frame")
	}
}

func TestServeConnClientGoneDuringExec(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeConn(server) }()

	require.NoError(t, WriteFrame(client, Request{Op: "exec", Command: []string{"sh", "-c", "echo hi"}}))
	// Closing without reading makes the agent's output write fail, which must
	// surface as an error rather than hanging.
	require.NoError(t, client.Close())

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("ServeConn did not return after the client vanished")
	}
}

func TestServeConnPutParentIsFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "put", Path: filepath.Join(blocker, "child")}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Contains(t, msg.Error, "parent directory")
}

func TestServeConnGetDirectory(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "get", Path: t.TempDir()}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Contains(t, msg.Error, "read")
}

func TestExitCode(t *testing.T) {
	assert.Equal(t, 0, exitCode(nil))
	assert.Equal(t, -1, exitCode(errors.New("not an exit error")))
}
