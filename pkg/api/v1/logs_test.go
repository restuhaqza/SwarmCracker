package v1

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogFrameJSONShape pins the wire shape: a data frame carries the stream and
// the approximate timestamp, and Data is base64 in JSON.
func TestLogFrameJSONShape(t *testing.T) {
	at := time.Date(2026, 10, 24, 12, 0, 0, 123456789, time.UTC)

	data, err := json.Marshal(LogDataFrame(at, []byte("hello\n")))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"log","stream":"stdout","time":"2026-10-24T12:00:00.123456789Z","data":"aGVsbG8K"}`,
		string(data))
}

// TestLogFrameOmitsUnsetKeys pins that a frame without data does not carry an
// empty data/stream/time key.
func TestLogFrameOmitsUnsetKeys(t *testing.T) {
	data, err := json.Marshal(LogFrame{Type: LogFrameTypeEOF})
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"eof"}`, string(data))

	data, err = json.Marshal(LogEOFFrame())
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"eof"}`, string(data))
}

// TestLogFrameDecodesBase64 pins the other direction: bytes arrive as base64 and
// decode back to the raw console bytes.
func TestLogFrameDecodesBase64(t *testing.T) {
	var frame LogFrame
	require.NoError(t, json.Unmarshal(
		[]byte(`{"type":"log","stream":"stdout","time":"2026-10-24T12:00:00Z","data":"aGVsbG8K"}`), &frame))

	assert.Equal(t, LogFrameTypeLog, frame.Type)
	assert.Equal(t, LogStreamStdout, frame.Stream)
	assert.Equal(t, "2026-10-24T12:00:00Z", frame.Time)
	assert.Equal(t, []byte("hello\n"), frame.Data)
}

// TestLogFrameTypes pins the two frame types and the merged stream name.
func TestLogFrameTypes(t *testing.T) {
	assert.Equal(t, "log", LogFrameTypeLog)
	assert.Equal(t, "eof", LogFrameTypeEOF)
	assert.Equal(t, "stdout", LogStreamStdout)
	assert.Equal(t, "application/x-ndjson", NDJSONMediaType,
		"a task log stream uses the documented NDJSON exception")
}

// TestLogDataFrameRoundTripsTime pins the RFC3339Nano timestamp format the
// contract promises.
func TestLogDataFrameRoundTripsTime(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	frame := LogDataFrame(at, []byte("x"))

	parsed, err := time.Parse(time.RFC3339Nano, frame.Time)
	require.NoError(t, err)
	assert.True(t, at.Equal(parsed))
	assert.Equal(t, LogStreamStdout, frame.Stream)
	assert.Equal(t, LogFrameTypeLog, frame.Type)
}
