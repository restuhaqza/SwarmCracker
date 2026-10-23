package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExecRequestJSONShape pins the wire shape: the command is always present,
// and every optional field is omitted when it is unset.
func TestExecRequestJSONShape(t *testing.T) {
	empty := ExecRequest{Command: []string{"echo", "hi"}}
	data, err := json.Marshal(empty)
	require.NoError(t, err)
	assert.JSONEq(t, `{"command":["echo","hi"]}`, string(data))

	full := ExecRequest{
		Command:   []string{"env"},
		Env:       map[string]string{"APP": "prod"},
		WorkDir:   "/srv",
		TTY:       true,
		Stdin:     true,
		TimeoutMs: 1500,
	}
	data, err = json.Marshal(full)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"command":["env"],
		"env":{"APP":"prod"},
		"workdir":"/srv",
		"tty":true,
		"stdin":true,
		"timeoutMs":1500
	}`, string(data))
}

// TestExecRequestDecodeRoundTrip proves a decoded request keeps its values,
// including the zero values an omitted key produces.
func TestExecRequestDecodeRoundTrip(t *testing.T) {
	var req ExecRequest
	require.NoError(t, json.Unmarshal([]byte(`{"command":["sh"],"timeoutMs":0}`), &req))

	assert.Equal(t, []string{"sh"}, req.Command)
	assert.False(t, req.TTY)
	assert.False(t, req.Stdin)
	assert.Equal(t, int64(0), req.TimeoutMs)
}

// TestExecFrameJSONShape pins the base64 encoding of Data and the omission of
// unset optional keys.
func TestExecFrameJSONShape(t *testing.T) {
	data, err := json.Marshal(StdoutFrame([]byte("hello\n")))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"stdout","data":"aGVsbG8K"}`, string(data))

	data, err = json.Marshal(StderrFrame([]byte("warning\n")))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"stderr","data":"d2FybmluZwo="}`, string(data))

	data, err = json.Marshal(ExitFrame(0))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"exit","exitCode":0}`, string(data))

	data, err = json.Marshal(ErrorFrame("internal"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"error","code":"internal"}`, string(data))
}

// TestExecFrameDecodesBase64 pins the other direction: a JSON frame carries
// base64 and the decoded Data is the raw output bytes.
func TestExecFrameDecodesBase64(t *testing.T) {
	var frame ExecFrame
	require.NoError(t, json.Unmarshal([]byte(`{"type":"stdout","data":"aGVsbG8K"}`), &frame))

	assert.Equal(t, FrameStdout, frame.Type)
	assert.Equal(t, []byte("hello\n"), frame.Data)
	assert.Nil(t, frame.ExitCode)
	assert.Nil(t, frame.Signal)
	assert.Empty(t, frame.Code)
}

// TestSignalExitFrame pins the 128+signal rule on the wire.
func TestSignalExitFrame(t *testing.T) {
	frame := SignalExitFrame(9)
	require.NotNil(t, frame.ExitCode)
	require.NotNil(t, frame.Signal)
	assert.Equal(t, FrameExit, frame.Type)
	assert.Equal(t, 137, *frame.ExitCode)
	assert.Equal(t, 9, *frame.Signal)

	assert.Equal(t, 137, SignalExitCode(9))
	assert.Equal(t, 128, SignalExitCode(0))
}

// TestExecFrameMediaType pins the documented streaming exception.
func TestExecMediaType(t *testing.T) {
	assert.Equal(t, "application/x-ndjson", NDJSONMediaType)
}
