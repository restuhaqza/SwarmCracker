package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// execTestServer writes the given NDJSON frames and returns the request it saw.
func execTestServer(t *testing.T, frames ...v1.ExecFrame) (*Client, <-chan *http.Request, <-chan []byte) {
	t.Helper()

	reqs := make(chan *http.Request, 1)
	bodies := make(chan []byte, 1)

	c := newTestRemote(t, Config{Token: "s3cret"}, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		reqs <- r.Clone(context.Background())
		bodies <- body

		w.Header().Set("Content-Type", v1.NDJSONMediaType)
		w.WriteHeader(http.StatusOK)
		encoder := json.NewEncoder(w)
		for _, frame := range frames {
			_ = encoder.Encode(frame)
		}
	})
	return c, reqs, bodies
}

func TestExecTaskStreamsOutputAndExit(t *testing.T) {
	c, reqs, bodies := execTestServer(t,
		v1.StdoutFrame([]byte("hello\n")),
		v1.StderrFrame([]byte("warning\n")),
		v1.ExitFrame(0),
	)

	var stdout, stderr bytes.Buffer
	result, err := c.ExecTask(context.Background(), "web-1", v1.ExecRequest{
		Command:   []string{"sh", "-c", "echo hello"},
		Env:       map[string]string{"APP": "prod"},
		WorkDir:   "/srv",
		TimeoutMs: 1500,
	}, &stdout, &stderr)
	require.NoError(t, err)

	assert.Equal(t, 0, result.ExitCode)
	assert.Zero(t, result.Signal)
	assert.Equal(t, "hello\n", stdout.String())
	assert.Equal(t, "warning\n", stderr.String())

	req := <-reqs
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, "/v1/tasks/web-1/exec", req.URL.Path)
	assert.Equal(t, v1.NDJSONMediaType, req.Header.Get("Accept"))
	assert.Equal(t, mediaTypeV1, req.Header.Get("Content-Type"))
	assert.Equal(t, "Bearer s3cret", req.Header.Get("Authorization"))

	var sent v1.ExecRequest
	require.NoError(t, json.Unmarshal(<-bodies, &sent))
	assert.Equal(t, []string{"sh", "-c", "echo hello"}, sent.Command)
	assert.Equal(t, map[string]string{"APP": "prod"}, sent.Env)
	assert.Equal(t, "/srv", sent.WorkDir)
	assert.Equal(t, int64(1500), sent.TimeoutMs)
}

func TestExecTaskSignalResult(t *testing.T) {
	code := 137
	signal := 9
	frame := v1.ExecFrame{Type: v1.FrameExit, ExitCode: &code, Signal: &signal}
	c, _, _ := execTestServer(t, frame)

	result, err := c.ExecTask(context.Background(), "web-1", v1.ExecRequest{Command: []string{"sleep", "100"}}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 137, result.ExitCode)
	assert.Equal(t, 9, result.Signal)
}

func TestExecTaskNonZeroExitIsSuccess(t *testing.T) {
	c, _, _ := execTestServer(t, v1.ExitFrame(3))

	result, err := c.ExecTask(context.Background(), "web-1", v1.ExecRequest{Command: []string{"false"}}, nil, nil)
	require.NoError(t, err, "a non-zero guest exit is a successful API call")
	assert.Equal(t, 3, result.ExitCode)
}

func TestExecTaskTruncatedStreamIsPartial(t *testing.T) {
	c, _, _ := execTestServer(t, v1.StdoutFrame([]byte("started\n")))

	var stdout bytes.Buffer
	_, err := c.ExecTask(context.Background(), "web-1", v1.ExecRequest{Command: []string{"tail"}}, &stdout, nil)
	require.Error(t, err)
	assert.Equal(t, output.CodePartial, codeOf(t, err).Code)
	assert.Equal(t, "started\n", stdout.String())
}

func TestExecTaskErrorFrame(t *testing.T) {
	c, _, _ := execTestServer(t, v1.ErrorFrame(string(output.CodeTimeout)))

	_, err := c.ExecTask(context.Background(), "web-1", v1.ExecRequest{Command: []string{"sleep"}}, nil, nil)
	require.Error(t, err)
	typed := codeOf(t, err)
	assert.Equal(t, output.CodeTimeout, typed.Code)
}

func TestExecTaskNon2xxErrorDocument(t *testing.T) {
	c := newTestRemote(t, Config{Token: "s3cret"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", testAccept)
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(v1.NewErrorDocument(v1.Error{
			Code:      string(output.CodeForbidden),
			Message:   "token task selectors do not match this task",
			RequestID: "req_abc",
			Details:   map[string]any{"reason": "selector_mismatch"},
		}))
	})

	_, err := c.ExecTask(context.Background(), "web-1", v1.ExecRequest{Command: []string{"true"}}, nil, nil)
	require.Error(t, err)
	typed := codeOf(t, err)
	assert.Equal(t, output.CodeForbidden, typed.Code)
	assert.Equal(t, "req_abc", typed.RequestID)
	assert.Equal(t, "selector_mismatch", typed.Details["reason"])
}

func TestExecTaskRequiresRemoteEndpoint(t *testing.T) {
	c := &Client{}

	_, err := c.ExecTask(context.Background(), "web-1", v1.ExecRequest{Command: []string{"true"}}, nil, nil)
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, codeOf(t, err).Code)
}

func TestExecTaskRejectsInvalidReference(t *testing.T) {
	c, _, _ := execTestServer(t, v1.ExitFrame(0))

	_, err := c.ExecTask(context.Background(), "..", v1.ExecRequest{Command: []string{"true"}}, nil, nil)
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, codeOf(t, err).Code)
}

func TestExecTaskStdoutWriteError(t *testing.T) {
	c, _, _ := execTestServer(t, v1.StdoutFrame([]byte("hello\n")), v1.ExitFrame(0))

	_, err := c.ExecTask(context.Background(), "web-1", v1.ExecRequest{Command: []string{"true"}},
		errWriter{err: io.ErrClosedPipe}, nil)
	require.Error(t, err)
	assert.Equal(t, output.CodeInternal, codeOf(t, err).Code)
}

// errWriter is a sink that always fails, for the write-error path.
type errWriter struct {
	err error
}

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }
