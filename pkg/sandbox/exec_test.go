package sandbox

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
)

// execTestMediaType is the API's request media type, repeated here because this package
// must not import pkg/apihttp (which imports it).
const execTestMediaType = "application/vnd.swarmcracker.v1+json"

// startGuest starts the guest side of a sandbox's muxer in this process: a unix socket
// that answers Firecracker's CONNECT handshake and then serves the guest agent protocol
// (pkg/agent).
//
// It is a real guest in everything exec depends on — the same handshake, the same hello,
// the same frames, the same exit status from a real process — so these tests exercise the
// path an exec takes rather than a stub of it. What it is not is a microVM: the command
// runs on this host, which is why it lives in a unit test and not in the evidence for the
// feature.
func startGuest(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sbx.vsock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveGuest(conn)
		}
	}()
	return path
}

// serveGuest speaks the muxer handshake and then the agent protocol on one connection.
func serveGuest(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(strings.TrimSpace(line), "CONNECT ") {
		return
	}
	// The host-side port the muxer answers with is arbitrary, and the client accepts any
	// decimal value; this one is deliberately not the requested port.
	if _, err := fmt.Fprintf(conn, "OK 1073741824\n"); err != nil {
		return
	}
	_ = agent.ServeConn(bufferedConn{Reader: reader, Conn: conn})
}

// bufferedConn keeps the bytes the handshake's buffered reader already holds: the agent
// protocol starts immediately after the OK line, and a reader that dropped its buffer
// would lose the first frame.
type bufferedConn struct {
	*bufio.Reader
	net.Conn
}

func (c bufferedConn) Read(p []byte) (int, error) { return c.Reader.Read(p) }

// readyWithGuest is a sandbox record whose muxer is a real guest.
func readyWithGuest(t *testing.T, f *fixture) Record {
	t.Helper()
	rec := testRecord("sbx-1")
	rec.VsockPath = startGuest(t)
	require.NoError(t, f.store.Put(rec))
	return rec
}

// TestExecRunsACommandAndReturnsItsStatus holds Exec to what `sandbox exec` promises: the
// command's standard output and standard error arrive as they are produced, and its exit
// status is the answer, not an error.
func TestExecRunsACommandAndReturnsItsStatus(t *testing.T) {
	f := newFixture(t)
	rec := readyWithGuest(t, f)

	// The node's clock moves between the record being written and the command running, so
	// "the sandbox was used" is observable rather than a same-instant write.
	f.clock.Advance(30 * time.Second)

	var stdout, stderr strings.Builder
	result, err := f.manager.Exec(context.Background(), rec.ID,
		[]string{"sh", "-c", "echo to-stdout; echo to-stderr >&2; exit 7"},
		ExecOptions{Stdout: &stdout, Stderr: &stderr})
	require.NoError(t, err)

	assert.Equal(t, 7, result.ExitCode, "a failing command is a successful exec with a status")
	assert.Equal(t, "to-stdout\n", stdout.String())
	assert.Equal(t, "to-stderr\n", stderr.String())

	// The node saw the sandbox used, which is what the reaper's idle clock is measured
	// from: without this, a sandbox someone is using would still be reaped.
	stored, found, err := f.store.Get(rec.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, f.clock.Now().UTC(), stored.LastActive)
	assert.True(t, stored.LastActive.After(rec.LastActive))
}

// TestExecResultCarriesASignal pins the second half of the status mapping: a command the
// guest kills with a signal reports 128+signal, as a shell does, and the signal itself.
func TestExecResultCarriesASignal(t *testing.T) {
	f := newFixture(t)
	rec := readyWithGuest(t, f)

	var discard strings.Builder
	result, err := f.manager.Exec(context.Background(), rec.ID,
		[]string{"sh", "-c", "kill -TERM $$"}, ExecOptions{Stdout: &discard, Stderr: &discard})
	require.NoError(t, err)
	assert.Greater(t, result.Signal, 0, "a signalled command reports the signal")
	assert.Equal(t, v1.SignalExitCode(result.Signal), result.ExitCode)
}

// TestServeExecStreamsTheBrokerFrames pins the API's path: the request is served by the
// node broker's own exec handler, so the response is the task route's NDJSON frames and a
// caller can follow it with the same decoder.
func TestServeExecStreamsTheBrokerFrames(t *testing.T) {
	f := newFixture(t)
	rec := readyWithGuest(t, f)

	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+rec.ID+"/exec",
		strings.NewReader(`{"command":["sh","-c","echo streamed; exit 0"]}`))
	request.Header.Set("Content-Type", execTestMediaType)
	recorder := httptest.NewRecorder()

	require.NoError(t, f.manager.ServeExec(context.Background(), rec.ID, recorder, request))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	stream := recorder.Body.String()
	assert.Contains(t, stream, `"type":"stdout"`)
	assert.Contains(t, stream, `"type":"exit"`, "a stream without an exit frame is not a finished command")
	assert.Contains(t, stream, base64.StdEncoding.EncodeToString([]byte("streamed\n")))
}

// TestServeExecRefusesAnEmptyCommand covers the body the broker handler refuses: it is
// answered as a typed failure, as the task route answers it, not as a panic or a hang.
func TestServeExecRefusesAnEmptyCommand(t *testing.T) {
	f := newFixture(t)
	rec := readyWithGuest(t, f)

	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+rec.ID+"/exec", strings.NewReader(`{"command":[]}`))
	request.Header.Set("Content-Type", execTestMediaType)
	recorder := httptest.NewRecorder()

	require.NoError(t, f.manager.ServeExec(context.Background(), rec.ID, recorder, request))
	// The handler refuses the body before it streams anything, so the answer is the v1
	// error document rather than a frame: a caller that cannot tell the two apart would
	// read a failed request as an empty command that ran.
	assert.GreaterOrEqual(t, recorder.Code, http.StatusBadRequest, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"code":"invalid"`)
}
