package agent

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
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
	return newCappedPipePair(t, Capabilities())
}

// newCappedPipePair is newPipePair with a narrowed capability set, so a test can
// prove how the agent answers an operation it never advertised.
func newCappedPipePair(t *testing.T, caps []string) (net.Conn, <-chan error) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveConn(server, caps, nil) }()
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

// TestServeConnHello pins the v2 handshake: the reply names the protocol the
// guest speaks, the operations it advertises, and the agent build and boot
// identity, and it echoes the request id.
func TestServeConnHello(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "hello", ID: "7", Version: Version}))

	msg := readMessage(t, conn)
	assert.Equal(t, "hello", msg.Type)
	assert.Equal(t, "7", msg.ID)
	assert.Equal(t, Version, msg.Version)
	assert.Equal(t, []string{Version}, msg.Versions)
	assert.Equal(t, Capabilities(), msg.Caps)
	assert.Equal(t, AgentVersion, msg.AgentVersion)
	assert.Equal(t, BuildID, msg.BuildID)
	assert.Equal(t, BootID(), msg.BootID)
	assert.NotEmpty(t, msg.BootID)
}

// TestServeConnHelloVersionMismatch proves a peer speaking another protocol is
// refused with the typed code the host maps to an unusable agent.
func TestServeConnHelloVersionMismatch(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: VersionLegacy}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Equal(t, CodeProtocolUnsupported, msg.ErrorCode)
	assert.Contains(t, msg.Error, "agent protocol unsupported")
	assert.Contains(t, msg.Error, "client speaks "+VersionLegacy)
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

// TestServeConnPutBadFrameKeepsDestination verifies that a stream error before
// the terminal frame leaves a pre-existing destination untouched and returns an
// error frame instead of truncating it.
func TestServeConnPutBadFrameKeepsDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	require.NoError(t, os.WriteFile(path, []byte("precious"), 0o644))

	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "put", Path: path}))
	require.NoError(t, WriteFrame(conn, Message{Type: "file", Data: []byte("partial")}))
	require.NoError(t, WriteFrame(conn, Message{Type: "stdout", Data: []byte("bad")}))

	msg := readMessage(t, conn)
	require.Equal(t, "error", msg.Type)
	assert.Contains(t, msg.Error, "unexpected frame type")

	// A follow-up request only gets served after handlePut returned, so its
	// reply proves the deferred temp-file cleanup has already run.
	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: Version}))
	require.Equal(t, "hello", readMessage(t, conn).Type)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "precious", string(got))
	_, err = os.Stat(path + ".sc-put-tmp")
	assert.True(t, os.IsNotExist(err), "temporary put file must be removed")
}

// TestServeConnPutInterruptedStreamKeepsDestination verifies that dropping the
// connection mid-stream leaves a pre-existing destination untouched.
func TestServeConnPutInterruptedStreamKeepsDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	require.NoError(t, os.WriteFile(path, []byte("precious"), 0o644))

	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeConn(server) }()

	require.NoError(t, WriteFrame(client, Request{Op: "put", Path: path}))
	require.NoError(t, WriteFrame(client, Message{Type: "file", Data: []byte("partial")}))
	require.NoError(t, client.Close())

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ServeConn did not return after the stream was interrupted")
	}

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "precious", string(got))
	_, err = os.Stat(path + ".sc-put-tmp")
	assert.True(t, os.IsNotExist(err), "temporary put file must be removed")
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

// TestServeConnUnknownOp covers an op no version of this agent serves.
func TestServeConnUnknownOp(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "frobnicate"}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Equal(t, CodeUnsupportedOp, msg.ErrorCode)
	assert.Contains(t, msg.Error, "not advertised")
}

// TestServeConnRefusesUnadvertisedOp proves the advertised set is the contract:
// an op the agent implements but does not advertise is refused, never served.
// That is what stops a host from depending on an operation the agent did not
// promise.
func TestServeConnRefusesUnadvertisedOp(t *testing.T) {
	conn, _ := newCappedPipePair(t, []string{CapHello, CapExec})

	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: Version}))
	hello := readMessage(t, conn)
	require.Equal(t, "hello", hello.Type)
	assert.Equal(t, []string{CapHello, CapExec}, hello.Caps)

	require.NoError(t, WriteFrame(conn, Request{Op: CapGet, Path: "/etc/hostname"}))
	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Equal(t, CodeUnsupportedOp, msg.ErrorCode)
	assert.Contains(t, msg.Error, CapGet)

	// The connection stays usable for what was advertised.
	_, _, code := runExec(t, conn, Request{Op: CapExec, Command: []string{"sh", "-c", "exit 5"}})
	assert.Equal(t, 5, code)
}

// TestServeConnEchoesRequestID proves every frame of a response carries the id
// of the request that produced it, including the streamed ones.
func TestServeConnEchoesRequestID(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: CapExec, ID: "req-42", Command: []string{"sh", "-c", "echo out"}}))

	sawStdout := false
	for {
		msg := readMessage(t, conn)
		assert.Equal(t, "req-42", msg.ID, "frame type %q", msg.Type)
		switch msg.Type {
		case "stdout":
			sawStdout = true
		case "exit":
			require.True(t, sawStdout, "the command should have produced output")
			return
		}
	}
}

// TestServeConnRefusesSpentDeadline proves a request whose budget was already
// spent is refused before anything runs, so the guest does not spend work the
// host has stopped waiting for.
func TestServeConnRefusesSpentDeadline(t *testing.T) {
	conn, _ := newPipePair(t)
	cmd := []string{"sh", "-c", "echo ran"}
	require.NoError(t, WriteFrame(conn, Request{Op: CapExec, DeadlineMs: -1, Command: cmd}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type, "the command must not run, so no stdout frame may arrive first")
	assert.Equal(t, CodeDeadlineExceeded, msg.ErrorCode)
	assert.Contains(t, msg.Error, "deadline")
}

// TestServeConnExecDeadlineKillsCommand proves a positive budget bounds the
// operation itself: the guest kills the command and reports the typed failure
// instead of letting it finish.
func TestServeConnExecDeadlineKillsCommand(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: CapExec, DeadlineMs: 100, Command: []string{"sleep", "30"}}))

	start := time.Now()
	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Equal(t, CodeDeadlineExceeded, msg.ErrorCode)
	assert.Less(t, time.Since(start), 10*time.Second, "the command must be killed at its deadline")
}

// TestServeConnClampsHugeDeadline proves a hostile budget cannot overflow the
// duration conversion and turn into an immediately expired request.
func TestServeConnClampsHugeDeadline(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{
		Op:         CapExec,
		DeadlineMs: math.MaxInt64,
		Command:    []string{"sh", "-c", "echo ran"},
	}))

	var stdout bytes.Buffer
	for {
		msg := readMessage(t, conn)
		switch msg.Type {
		case "stdout":
			stdout.Write(msg.Data)
		case "exit":
			assert.Equal(t, 0, *msg.Code)
			assert.Equal(t, "ran\n", stdout.String())
			return
		case "error":
			t.Fatalf("a huge budget must not expire the request: %s", msg.Error)
		}
	}
}

// TestServeConnHelloReportsRotatedBootID proves the boot identity the host uses
// to recognise a new guest instance is republished after a rotation, which is how
// a restore invalidates the sessions from before it.
func TestServeConnHelloReportsRotatedBootID(t *testing.T) {
	before := BootID()
	t.Cleanup(func() {
		bootMu.Lock()
		bootID = before
		bootMu.Unlock()
	})

	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: Version}))
	assert.Equal(t, before, readMessage(t, conn).BootID)

	rotated := RotateBootID()
	require.NotEqual(t, before, rotated)

	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: Version}))
	assert.Equal(t, rotated, readMessage(t, conn).BootID)
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
