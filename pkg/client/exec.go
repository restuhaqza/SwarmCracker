// This file owns the remote exec client: POST /v1/tasks/{ref}/exec over the
// public HTTPS API, consuming its NDJSON frame stream.
//
// The local control socket has no exec operation; a local caller uses the guest
// agent (pkg/agent) directly. The API path exists so a CI runner can exec in a
// task without cluster certificates.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// ExecResult is the guest outcome of one exec: its exit status and, when the
// process was terminated by a signal, that signal.
type ExecResult struct {
	ExitCode int
	Signal   int
}

// ExecTask runs one non-interactive command in a task through the public API and
// streams its output to stdout and stderr. A non-zero guest exit is a successful
// call; only a missing exit frame is a failure.
func (c *Client) ExecTask(ctx context.Context, ref string, req v1.ExecRequest, stdout, stderr io.Writer) (ExecResult, error) {
	if c.remote == nil {
		return ExecResult{}, output.NewError(output.CodeUsage,
			"exec over the API requires --host; without it the local guest-agent socket is used")
	}
	return c.remote.execTask(ctx, ref, req, stdout, stderr)
}

// execTask posts the request and decodes the frame stream.
func (r *remoteClient) execTask(ctx context.Context, ref string, req v1.ExecRequest, stdout, stderr io.Writer) (ExecResult, error) {
	if err := validateSegments([]string{ref}); err != nil {
		return ExecResult{}, err
	}

	body, err := json.Marshal(req)
	if err != nil {
		return ExecResult{}, output.NewErrorf(output.CodeInternal, "encode exec request: %v", err)
	}

	u := r.baseURL.JoinPath("v1", "tasks", url.PathEscape(ref), "exec")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return ExecResult{}, output.NewErrorf(output.CodeInternal, "build exec request: %v", err)
	}
	httpReq.Header.Set("Accept", v1.NDJSONMediaType)
	httpReq.Header.Set("Content-Type", mediaTypeV1)
	if r.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+r.token)
	}

	// The stream client has no whole-response timeout: an exec may outlive the
	// generic request timeout, and its own context bounds it.
	resp, err := r.stream.Do(httpReq)
	if err != nil {
		return ExecResult{}, transportError("POST /v1/tasks/"+ref+"/exec", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return ExecResult{}, decodeErrorDocument(http.MethodPost,
			"/v1/tasks/"+ref+"/exec", resp.StatusCode, resp.Status, errorBody)
	}

	return decodeExecStream(resp.Body, stdout, stderr)
}

// decodeExecStream consumes NDJSON frames until the exit frame. A stream that
// ends without one is partial, never success.
func decodeExecStream(body io.Reader, stdout, stderr io.Writer) (ExecResult, error) {
	decoder := json.NewDecoder(body)
	for {
		var frame v1.ExecFrame
		if err := decoder.Decode(&frame); err != nil {
			if errors.Is(err, io.EOF) {
				return ExecResult{}, output.NewError(output.CodePartial,
					"exec stream ended without an exit frame")
			}
			return ExecResult{}, transportError("read exec stream", err)
		}

		switch frame.Type {
		case v1.FrameStdout:
			if err := writeOutput(stdout, frame.Data); err != nil {
				return ExecResult{}, err
			}
		case v1.FrameStderr:
			if err := writeOutput(stderr, frame.Data); err != nil {
				return ExecResult{}, err
			}
		case v1.FrameExit:
			return execFrameResult(frame)
		case v1.FrameError:
			code := output.Code(frame.Code)
			if code == "" {
				code = output.CodeInternal
			}
			return ExecResult{}, output.NewError(code, "the guest exec stream failed")
		default:
			return ExecResult{}, output.NewErrorf(output.CodePartial,
				"exec stream carried an unknown frame type %q", frame.Type)
		}
	}
}

// writeOutput writes one frame's bytes to the caller's sink.
func writeOutput(w io.Writer, data []byte) error {
	if w == nil || len(data) == 0 {
		return nil
	}
	if _, err := w.Write(data); err != nil {
		return output.NewErrorf(output.CodeInternal, "write exec output: %v", err)
	}
	return nil
}

// execFrameResult converts the terminal frame into the caller's result. A
// signal wins: 128+signal is the shell convention, even if the server omitted
// the code.
func execFrameResult(frame v1.ExecFrame) (ExecResult, error) {
	if frame.Signal != nil && *frame.Signal > 0 {
		signal := *frame.Signal
		return ExecResult{ExitCode: v1.SignalExitCode(signal), Signal: signal}, nil
	}
	if frame.ExitCode == nil {
		return ExecResult{}, output.NewError(output.CodePartial,
			"exec stream carried an exit frame without an exit code")
	}
	return ExecResult{ExitCode: *frame.ExitCode}, nil
}
