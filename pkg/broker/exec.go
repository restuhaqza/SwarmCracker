package broker

// This file owns the node-side exec handler: the first real broker operation.
//
// A manager forwards POST /v1/tasks/{taskID}/exec; the handler dials the task's
// guest agent over the vsock muxer and streams the result back as NDJSON. The
// response is the documented exception to the v1 JSON envelope, so a caller can
// follow output while the command runs.
//
// Interactive TTY is a follow-up: the request is rejected with a typed 501
// before any dial. The public route rejects it too; this is defence in depth.
//
// Exec needs a live guest agent. A task that has exited keeps its console log
// (readable through the logs operation) but has no guest, so exec answers a
// typed 404 not_found instead of dialing a dead vsock path.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// execBodyLimit bounds one exec request body. A command and its environment
	// are small; the cap only stops a hostile caller from exhausting memory.
	execBodyLimit = 1 << 20
)

// GuestExecer is the subset of *agent.Client the handler needs.
type GuestExecer interface {
	ExecStatus(ctx context.Context, cmd []string, opts agent.ExecOptions) (agent.ExecResult, error)
}

// ExecDialer connects to a task's guest agent. The real implementation is
// DialAgent; tests substitute their own to script the guest.
type ExecDialer func(ctx context.Context, vsockPath string, port uint32) (GuestExecer, error)

// DialAgent is the production ExecDialer: agent.Dial over the Firecracker
// vsock Unix socket.
func DialAgent(ctx context.Context, vsockPath string, port uint32) (GuestExecer, error) {
	return agent.Dial(ctx, vsockPath, port)
}

// NewExecHandler builds the non-TTY exec operation. A nil dialer selects
// DialAgent, so the daemon wires the handler without repeating the default.
func NewExecHandler(dial ExecDialer) Handler {
	if dial == nil {
		dial = DialAgent
	}

	return func(ctx context.Context, task Task, w http.ResponseWriter, r *http.Request) {
		serveExec(ctx, dial, task, w, r)
	}
}

// serveExec validates the request, dials the guest, then streams frames.
func serveExec(ctx context.Context, dial ExecDialer, task Task, w http.ResponseWriter, r *http.Request) {
	req, status, err := decodeExecRequest(w, r)
	if err != nil {
		writeTypedError(w, status, typedExecError(err))
		return
	}

	// A stopped task keeps its console log but has no guest agent. Dialing the
	// vsock path of a dead VM would fail opaquely (or reach a path that was
	// reused), so it is not_found, like any other task the node cannot serve.
	if !task.guestAvailable() {
		writeError(w, http.StatusNotFound, codeNotFound,
			"task %q is not running", task.ID)
		return
	}
	vsockPath := task.VsockPath()

	// The deadline covers the dial and the whole command. A caller-supplied
	// timeout of 0 means no server-side deadline.
	if req.TimeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeoutFromMillis(req.TimeoutMs))
		defer cancel()
	}

	// Dial before the 200 header: a dial or version failure is still a typed
	// v1 error document, which the manager rewrites into the public envelope.
	guest, err := dial(ctx, vsockPath, agent.DefaultPort)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable,
			"task %q guest agent is unavailable", task.ID)
		return
	}
	if closer, ok := guest.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}

	stream := &execStream{w: w, flusher: flusherFor(w)}
	stream.writeHeader()

	result, err := guest.ExecStatus(ctx, req.Command, agent.ExecOptions{
		Stdout:  stream.writer(v1.FrameStdout),
		Stderr:  stream.writer(v1.FrameStderr),
		Env:     execEnv(req.Env),
		WorkDir: req.WorkDir,
	})

	// Nothing may be appended after this point by a guest writer that outlived
	// the command: the response belongs to the handler now.
	stream.closeGuest()

	if err != nil {
		_ = stream.terminal(v1.ErrorFrame(execErrorCode(ctx, err)))
		return
	}
	// A negative code is not a guest exit status. The guest agent reports one
	// when it could not read the process's wait status, which is what an agent
	// predating signal reporting does for a signal-killed command. Emitting it
	// as an exit frame would tell the caller that the command ran and returned
	// -1, so the stream fails instead.
	if result.ExitCode < 0 {
		_ = stream.terminal(v1.ErrorFrame(string(output.CodeInternal)))
		return
	}
	if result.Signal > 0 {
		_ = stream.terminal(v1.SignalExitFrame(result.Signal))
		return
	}
	_ = stream.terminal(v1.ExitFrame(result.ExitCode))
}

// decodeExecRequest reads one strict JSON body under the exec cap and validates
// it. The returned status is the HTTP status for a failure the taxonomy cannot
// infer; every code the handler returns is a typed *output.Error.
func decodeExecRequest(w http.ResponseWriter, r *http.Request) (v1.ExecRequest, int, error) {
	var req v1.ExecRequest

	r.Body = http.MaxBytesReader(w, r.Body, execBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return req, http.StatusRequestEntityTooLarge,
				output.NewErrorf(output.CodeInvalid, "the request body exceeds %d bytes", execBodyLimit).
					WithDetails(map[string]any{"reason": "payload_too_large", "limit": execBodyLimit})
		}
		return req, http.StatusBadRequest,
			output.NewErrorf(output.CodeInvalid, "read request body: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return req, http.StatusBadRequest,
			output.NewErrorf(output.CodeInvalid, "invalid JSON body: %v", err).
				WithDetails(map[string]any{"reason": "invalid_body"})
	}
	if err := requireSingleJSONValue(decoder); err != nil {
		return req, http.StatusBadRequest, err
	}

	status, err := validateExecRequest(req)
	return req, status, err
}

// requireSingleJSONValue rejects a body that carries more than one JSON value.
func requireSingleJSONValue(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return output.NewError(output.CodeInvalid,
			"the request body must contain exactly one JSON object")
	}
	return nil
}

// validateExecRequest enforces the contract's exec rules. The TTY rule runs
// after the stdin rule so `stdin:true` without a TTY is reported as such.
func validateExecRequest(req v1.ExecRequest) (int, error) {
	switch {
	case len(req.Command) == 0:
		return http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"command must not be empty")
	case req.Stdin && !req.TTY:
		return http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"stdin requires tty").
			WithDetails(map[string]any{"reason": "stdin_requires_tty"})
	case req.TTY:
		return http.StatusNotImplemented, output.NewError(output.CodeNotImplemented,
			"interactive exec over the API is not implemented").
			WithDetails(map[string]any{"reason": "tty_not_supported"})
	case req.TimeoutMs < 0:
		return http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"timeoutMs must be zero or positive")
	}

	for key := range req.Env {
		if key == "" || strings.Contains(key, "=") {
			return http.StatusBadRequest, output.NewErrorf(output.CodeInvalid,
				"invalid environment variable name %q", key).
				WithDetails(map[string]any{"reason": "invalid_env"})
		}
	}
	return 0, nil
}

// timeoutFromMillis converts a request deadline in milliseconds to a duration.
func timeoutFromMillis(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

// execEnv renders the request's environment as sorted K=V entries, so the guest
// sees a deterministic order.
func execEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	pairs := make([]string, 0, len(env))
	for key, value := range env {
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs)
	return pairs
}

// execErrorCode classifies a mid-stream failure. A deadline is a timeout; every
// other failure is internal. The guest's own exit status is never an error.
func execErrorCode(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return string(output.CodeTimeout)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return string(output.CodeTimeout)
	}
	return string(output.CodeInternal)
}

// typedExecError keeps a typed error's details, which writeError cannot carry.
func typedExecError(err error) *output.Error {
	var typed *output.Error
	if errors.As(err, &typed) {
		return typed
	}
	return output.NewError(output.CodeInternal, err.Error())
}

// execStream writes NDJSON frames to a response and flushes each one. The mutex
// serialises stdout and stderr, and the closed flag keeps a writer that outlives
// the command from appending bytes to a finished response.
type execStream struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	flusher http.Flusher
	closed  bool
}

// writeHeader sends the stream headers and 200. It runs after a successful
// dial, so a failure before this point is still a typed error document.
func (s *execStream) writeHeader() {
	s.w.Header().Set("Content-Type", v1.NDJSONMediaType)
	s.w.Header().Set("X-Content-Type-Options", "nosniff")
	s.w.WriteHeader(http.StatusOK)
	s.flushLocked()
}

// closeGuest closes the stream to guest writers. The handler's own terminal
// frame is still written afterwards.
func (s *execStream) closeGuest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

// frame writes one guest output frame. A writer that outlived the command is
// refused so it cannot append bytes to a finished response.
func (s *execStream) frame(frame v1.ExecFrame) error {
	return s.emit(frame, true)
}

// terminal writes the handler's final frame, after guest writers are closed.
func (s *execStream) terminal(frame v1.ExecFrame) error {
	return s.emit(frame, false)
}

// emit writes and flushes one frame. checkClosed rejects a late guest writer.
func (s *execStream) emit(frame v1.ExecFrame, checkClosed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if checkClosed && s.closed {
		return errors.New("exec stream is closed")
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(frame); err != nil {
		return err
	}
	if _, err := s.w.Write(buf.Bytes()); err != nil {
		return err
	}
	s.flushLocked()
	return nil
}

// flushLocked flushes the response. The caller holds the mutex or is the only
// goroutine running (writeHeader).
func (s *execStream) flushLocked() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// writer returns an io.Writer that frames every chunk with typ.
func (s *execStream) writer(typ string) io.Writer {
	return execFrameWriter{stream: s, typ: typ}
}

// execFrameWriter adapts one frame type to io.Writer for agent.ExecOptions.
type execFrameWriter struct {
	stream *execStream
	typ    string
}

func (w execFrameWriter) Write(p []byte) (int, error) {
	// Copy: the caller may reuse p as soon as Write returns.
	data := append([]byte(nil), p...)
	if err := w.stream.frame(v1.ExecFrame{Type: w.typ, Data: data}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// flusherFor returns the response's Flusher, or nil for a writer that cannot
// flush (an in-memory test recorder without Flusher).
func flusherFor(w http.ResponseWriter) http.Flusher {
	if flusher, ok := w.(http.Flusher); ok {
		return flusher
	}
	return nil
}
