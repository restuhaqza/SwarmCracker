package v1

// This file owns the exec wire contract: the request a caller posts to
// POST /v1/tasks/{ref}/exec and the NDJSON frames the server streams back.
//
// Exec is the one v1 route whose response is not a JSON envelope: the stream is
// application/x-ndjson so output can be followed as it is produced. Frames are
// newline-delimited JSON objects; Data is base64 in JSON, exactly as Go encodes
// a []byte.

// NDJSONMediaType is the media type of an exec response stream. It is the
// documented exception to the v1 JSON envelope.
const NDJSONMediaType = "application/x-ndjson"

// Exec frame types.
const (
	FrameStdout = "stdout"
	FrameStderr = "stderr"
	FrameExit   = "exit"
	FrameError  = "error"
)

// ExecRequest is the body of POST /v1/tasks/{ref}/exec.
//
// Command is required and non-empty. Stdin requires TTY (a non-TTY exec has no
// stdin channel); TTY itself is not served by the API yet. TimeoutMs is a
// server-side deadline for the whole command, where 0 means none. Env values
// are passed to the guest as K=V after stable sorting.
type ExecRequest struct {
	Command   []string          `json:"command"`
	Env       map[string]string `json:"env,omitempty"`
	WorkDir   string            `json:"workdir,omitempty"`
	TTY       bool              `json:"tty,omitempty"`
	Stdin     bool              `json:"stdin,omitempty"`
	TimeoutMs int64             `json:"timeoutMs,omitempty"`
}

// ExecFrame is one frame of an exec response stream. Data carries raw output
// bytes (base64 in JSON); ExitCode and Signal are set on the terminal frame and
// Code is set on an error frame.
type ExecFrame struct {
	Type     string `json:"type"`           // stdout|stderr|exit|error
	Data     []byte `json:"data,omitempty"` // base64 in JSON
	ExitCode *int   `json:"exitCode,omitempty"`
	Signal   *int   `json:"signal,omitempty"`
	Code     string `json:"code,omitempty"`
}

// StdoutFrame returns one stdout frame carrying data.
func StdoutFrame(data []byte) ExecFrame {
	return ExecFrame{Type: FrameStdout, Data: data}
}

// StderrFrame returns one stderr frame carrying data.
func StderrFrame(data []byte) ExecFrame {
	return ExecFrame{Type: FrameStderr, Data: data}
}

// ExitFrame returns a normal-exit terminal frame.
func ExitFrame(exitCode int) ExecFrame {
	return ExecFrame{Type: FrameExit, ExitCode: &exitCode}
}

// SignalExitFrame returns a terminal frame for a guest process killed by
// signal. The reported exit code is 128+signal, so a caller that ignores Signal
// still sees the shell convention.
func SignalExitFrame(signal int) ExecFrame {
	code := SignalExitCode(signal)
	return ExecFrame{Type: FrameExit, ExitCode: &code, Signal: &signal}
}

// ErrorFrame returns an error frame. It is only sent for a failure after the
// stream started; a non-zero guest exit is a successful exit frame, not an
// error.
func ErrorFrame(code string) ExecFrame {
	return ExecFrame{Type: FrameError, Code: code}
}

// SignalExitCode maps a terminating signal to the conventional process status.
func SignalExitCode(signal int) int {
	return 128 + signal
}
