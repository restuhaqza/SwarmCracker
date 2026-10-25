package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// fakeGuest is a GuestExecer whose behaviour the test scripts.
type fakeGuest struct {
	result  agent.ExecResult
	err     error
	run     func(ctx context.Context, opts agent.ExecOptions)
	gotCmd  []string
	gotOpts agent.ExecOptions
	closed  bool
}

func (f *fakeGuest) ExecStatus(ctx context.Context, cmd []string, opts agent.ExecOptions) (agent.ExecResult, error) {
	f.gotCmd = cmd
	f.gotOpts = opts
	if f.run != nil {
		f.run(ctx, opts)
	}
	return f.result, f.err
}

func (f *fakeGuest) Close() error {
	f.closed = true
	return nil
}

// fakeDialer records the vsock address and hands back the scripted guest.
type fakeDialer struct {
	guest   GuestExecer
	err     error
	gotPath string
	gotPort uint32
}

func (d *fakeDialer) dial(_ context.Context, vsockPath string, port uint32) (GuestExecer, error) {
	d.gotPath = vsockPath
	d.gotPort = port
	if d.err != nil {
		return nil, d.err
	}
	return d.guest, nil
}

func testExecTask() Task {
	return Task{ID: "task-1", SocketPath: "/var/run/firecracker/task-1.sock", Running: true}
}

func execRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "/v1/tasks/task-1/exec", strings.NewReader(body))
}

// serveExec runs the handler and returns the recorded response.
func runExecHandler(t *testing.T, dial *fakeDialer, body string) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewExecHandler(dial.dial)
	rec := httptest.NewRecorder()
	handler(context.Background(), testExecTask(), rec, execRequest(t, body))
	return rec
}

// decodeFrames parses every NDJSON frame in a response body.
func decodeFrames(t *testing.T, body []byte) []v1.ExecFrame {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	frames := []v1.ExecFrame{}
	for {
		var frame v1.ExecFrame
		err := decoder.Decode(&frame)
		if errors.Is(err, io.EOF) {
			return frames
		}
		require.NoError(t, err)
		frames = append(frames, frame)
	}
}

func TestExecHandlerStreamsOutputAndExit(t *testing.T) {
	guest := &fakeGuest{
		result: agent.ExecResult{ExitCode: 3},
		run: func(_ context.Context, opts agent.ExecOptions) {
			_, _ = opts.Stdout.Write([]byte("hello\n"))
			_, _ = opts.Stderr.Write([]byte("warning\n"))
		},
	}
	dial := &fakeDialer{guest: guest}

	rec := runExecHandler(t, dial, `{"command":["sh","-c","echo hi"],"env":{"B":"2","A":"1"},"workdir":"/srv"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, v1.NDJSONMediaType, rec.Header().Get("Content-Type"))
	assert.Equal(t, "/var/run/firecracker/task-1.vsock", dial.gotPath)
	assert.Equal(t, agent.DefaultPort, dial.gotPort)
	assert.Equal(t, []string{"sh", "-c", "echo hi"}, guest.gotCmd)
	assert.Equal(t, []string{"A=1", "B=2"}, guest.gotOpts.Env, "env keys must be stable sorted")
	assert.Equal(t, "/srv", guest.gotOpts.WorkDir)
	assert.True(t, guest.closed, "the handler must close a dialed guest")

	frames := decodeFrames(t, rec.Body.Bytes())
	require.Len(t, frames, 3)
	assert.Equal(t, v1.FrameStdout, frames[0].Type)
	assert.Equal(t, []byte("hello\n"), frames[0].Data)
	assert.Equal(t, v1.FrameStderr, frames[1].Type)
	assert.Equal(t, []byte("warning\n"), frames[1].Data)
	assert.Equal(t, v1.FrameExit, frames[2].Type)
	require.NotNil(t, frames[2].ExitCode)
	assert.Equal(t, 3, *frames[2].ExitCode)
	assert.Nil(t, frames[2].Signal, "a normal exit carries no signal")
}

func TestExecHandlerSignalExit(t *testing.T) {
	guest := &fakeGuest{result: agent.ExecResult{ExitCode: 137, Signal: 9}}
	rec := runExecHandler(t, &fakeDialer{guest: guest}, `{"command":["sleep","100"]}`)

	require.Equal(t, http.StatusOK, rec.Code)
	frames := decodeFrames(t, rec.Body.Bytes())
	require.Len(t, frames, 1)
	assert.Equal(t, v1.FrameExit, frames[0].Type)
	require.NotNil(t, frames[0].ExitCode)
	require.NotNil(t, frames[0].Signal)
	assert.Equal(t, 137, *frames[0].ExitCode)
	assert.Equal(t, 9, *frames[0].Signal)
}

func TestExecHandlerDialFailureIs503(t *testing.T) {
	dial := &fakeDialer{err: errors.New("no such vsock socket /var/run/firecracker/task-1.vsock")}
	rec := runExecHandler(t, dial, `{"command":["true"]}`)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.NotContains(t, rec.Body.String(), "/var/run/firecracker",
		"an internal path must not reach the caller")
	doc := decodeErrorDocument(t, rec.Body)
	assert.Equal(t, string(codeUnavailable), doc.Error.Code)
}

func TestExecHandlerMidStreamErrorHasNoExitFrame(t *testing.T) {
	guest := &fakeGuest{
		err: errors.New("guest agent went away"),
		run: func(_ context.Context, opts agent.ExecOptions) {
			_, _ = opts.Stdout.Write([]byte("partial\n"))
		},
	}
	rec := runExecHandler(t, &fakeDialer{guest: guest}, `{"command":["tail","-f","/log"]}`)

	require.Equal(t, http.StatusOK, rec.Code)
	frames := decodeFrames(t, rec.Body.Bytes())
	require.Len(t, frames, 2)
	assert.Equal(t, v1.FrameStdout, frames[0].Type)
	assert.Equal(t, v1.FrameError, frames[1].Type)
	assert.Equal(t, string(output.CodeInternal), frames[1].Code)
	for _, frame := range frames {
		assert.NotEqual(t, v1.FrameExit, frame.Type, "a failed exec must not fabricate an exit frame")
	}
}

func TestExecHandlerTimeoutFrame(t *testing.T) {
	guest := &fakeGuest{
		run: func(ctx context.Context, _ agent.ExecOptions) {
			<-ctx.Done()
		},
		err: context.DeadlineExceeded,
	}
	rec := runExecHandler(t, &fakeDialer{guest: guest}, `{"command":["sleep","60"],"timeoutMs":10}`)

	require.Equal(t, http.StatusOK, rec.Code)
	frames := decodeFrames(t, rec.Body.Bytes())
	require.Len(t, frames, 1)
	assert.Equal(t, v1.FrameError, frames[0].Type)
	assert.Equal(t, string(output.CodeTimeout), frames[0].Code)
}

func TestExecHandlerTTYIsNotImplemented(t *testing.T) {
	dial := &fakeDialer{guest: &fakeGuest{}}
	rec := runExecHandler(t, dial, `{"command":["sh"],"tty":true}`)

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	require.Empty(t, dial.gotPath, "a TTY request must not dial")

	doc := decodeErrorDocument(t, rec.Body)
	assert.Equal(t, string(codeNotImplemented), doc.Error.Code)
	assert.Equal(t, "tty_not_supported", doc.Error.Details["reason"])
}

func TestExecHandlerStdinRequiresTTY(t *testing.T) {
	rec := runExecHandler(t, &fakeDialer{guest: &fakeGuest{}}, `{"command":["cat"],"stdin":true}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	doc := decodeErrorDocument(t, rec.Body)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	assert.Equal(t, "stdin_requires_tty", doc.Error.Details["reason"])
}

func TestExecHandlerRejectsEmptyCommand(t *testing.T) {
	rec := runExecHandler(t, &fakeDialer{guest: &fakeGuest{}}, `{"command":[]}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	doc := decodeErrorDocument(t, rec.Body)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
}

func TestExecHandlerRejectsNegativeTimeout(t *testing.T) {
	rec := runExecHandler(t, &fakeDialer{guest: &fakeGuest{}}, `{"command":["true"],"timeoutMs":-1}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	doc := decodeErrorDocument(t, rec.Body)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
}

func TestExecHandlerRejectsMalformedBody(t *testing.T) {
	rec := runExecHandler(t, &fakeDialer{guest: &fakeGuest{}}, `{"command":`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	doc := decodeErrorDocument(t, rec.Body)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
}

func TestExecHandlerRejectsInvalidEnvKey(t *testing.T) {
	for _, body := range []string{
		`{"command":["env"],"env":{"A=B":"x"}}`,
		`{"command":["env"],"env":{"":"x"}}`,
	} {
		rec := runExecHandler(t, &fakeDialer{guest: &fakeGuest{}}, body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		doc := decodeErrorDocument(t, rec.Body)
		assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	}
}

// TestExecHandlerRejectsNonRunningTask pins that exec needs a live guest: an
// exited task keeps its log but has no VM, so dialing would be wrong. It is a
// typed 404, not a 503 and not an empty vsock path.
func TestExecHandlerRejectsNonRunningTask(t *testing.T) {
	cases := map[string]Task{
		"no task at all":     {ID: "task-1"},
		"log-only task":      {ID: "task-1", LogPath: "/var/run/swarmcracker/task-1.log"},
		"running flag clear": {ID: "task-1", SocketPath: "/var/run/firecracker/task-1.sock"},
	}

	for name, task := range cases {
		t.Run(name, func(t *testing.T) {
			dial := &fakeDialer{guest: &fakeGuest{}}
			handler := NewExecHandler(dial.dial)
			rec := httptest.NewRecorder()
			handler(context.Background(), task, rec, execRequest(t, `{"command":["true"]}`))

			require.Equal(t, http.StatusNotFound, rec.Code)
			doc := decodeErrorDocument(t, rec.Body)
			assert.Equal(t, string(codeNotFound), doc.Error.Code)
			assert.Empty(t, dial.gotPath, "a non-running task must not be dialed")
		})
	}
}

// flushRecorder counts flushes so the test can prove a frame is not buffered.
type flushRecorder struct {
	header  http.Header
	body    bytes.Buffer
	status  int
	flushes int
}

func (f *flushRecorder) Header() http.Header { return f.header }

func (f *flushRecorder) Write(p []byte) (int, error) { return f.body.Write(p) }

func (f *flushRecorder) WriteHeader(status int) { f.status = status }

func (f *flushRecorder) Flush() { f.flushes++ }

func TestExecHandlerFlushesEveryFrame(t *testing.T) {
	guest := &fakeGuest{
		run: func(_ context.Context, opts agent.ExecOptions) {
			_, _ = opts.Stdout.Write([]byte("one\n"))
			_, _ = opts.Stdout.Write([]byte("two\n"))
		},
	}
	rec := &flushRecorder{header: make(http.Header), status: http.StatusOK}
	handler := NewExecHandler((&fakeDialer{guest: guest}).dial)
	handler(context.Background(), testExecTask(), rec, execRequest(t, `{"command":["echo"]}`))

	assert.Equal(t, http.StatusOK, rec.status)
	assert.GreaterOrEqual(t, rec.flushes, 3, "stdout, stdout and exit must each be flushed")
}

// TestExecHandlerDoesNotWriteAfterError pins that a timed-out command cannot
// keep streaming output through a finished response.
func TestExecHandlerDoesNotWriteAfterError(t *testing.T) {
	lateWrite := make(chan error, 1)
	guest := &fakeGuest{
		err: context.DeadlineExceeded,
		run: func(_ context.Context, opts agent.ExecOptions) {
			// A writer that outlives the handler must fail, not append frames.
			go func() {
				time.Sleep(10 * time.Millisecond)
				_, err := opts.Stdout.Write([]byte("late\n"))
				lateWrite <- err
			}()
		},
	}
	rec := runExecHandler(t, &fakeDialer{guest: guest}, `{"command":["sleep","60"],"timeoutMs":5}`)

	require.Error(t, <-lateWrite, "a write after the response finished must fail")
	frames := decodeFrames(t, rec.Body.Bytes())
	for _, frame := range frames {
		assert.NotEqual(t, v1.FrameStdout, frame.Type, "output after the error frame must be dropped")
	}
}
