package agent

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteReadFrameRoundTripMessage(t *testing.T) {
	var buf bytes.Buffer
	code := 7
	want := Message{Type: "stdout", Data: []byte("hello world"), Code: &code}
	require.NoError(t, WriteFrame(&buf, want))

	got, err := ReadFrame(&buf, &Message{})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want.Type, got.Type)
	assert.Equal(t, want.Data, got.Data)
	require.NotNil(t, got.Code)
	assert.Equal(t, code, *got.Code)
}

func TestWriteReadFrameRoundTripRequest(t *testing.T) {
	var buf bytes.Buffer
	req := Request{
		Op:      "exec",
		Version: Version,
		Command: []string{"echo", "hi"},
		TTY:     true,
		Env:     []string{"A=B"},
		WorkDir: "/tmp",
	}
	require.NoError(t, WriteFrame(&buf, req))

	var got Request
	msg, err := ReadFrame(&buf, &got)
	require.NoError(t, err)
	assert.Nil(t, msg, "a Request target must not yield a Message")
	assert.Equal(t, req, got)
}

// TestMessageSignalRoundTrip proves an exit frame can carry a terminating
// signal, and that a normal exit omits it.
func TestMessageSignalRoundTrip(t *testing.T) {
	signal := 9
	code := 137
	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, Message{Type: "exit", Code: &code, Signal: &signal}))

	var msg Message
	_, err := ReadFrame(&buf, &msg)
	require.NoError(t, err)
	require.NotNil(t, msg.Code)
	require.NotNil(t, msg.Signal)
	assert.Equal(t, 137, *msg.Code)
	assert.Equal(t, 9, *msg.Signal)

	buf.Reset()
	code = 0
	require.NoError(t, WriteFrame(&buf, Message{Type: "exit", Code: &code}))
	msg = Message{}
	_, err = ReadFrame(&buf, &msg)
	require.NoError(t, err)
	assert.Nil(t, msg.Signal, "a normal exit must not carry a signal")
}

func TestReadFrameRejectsZeroLength(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, binary.Write(&buf, binary.BigEndian, uint32(0)))

	_, err := ReadFrame(&buf, &Message{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "0")
}

func TestReadFrameRejectsOversizeLength(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, binary.Write(&buf, binary.BigEndian, uint32(MaxFrameSize+1)))

	_, err := ReadFrame(&buf, &Message{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestReadFrameTruncatedBody(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, binary.Write(&buf, binary.BigEndian, uint32(16)))
	buf.WriteString(`{"type"`)

	_, err := ReadFrame(&buf, &Message{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, io.ErrUnexpectedEOF), "want unexpected EOF, got %v", err)
}

func TestReadFrameHeaderEOF(t *testing.T) {
	_, err := ReadFrame(bytes.NewReader(nil), &Message{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, io.EOF))
}

func TestReadFrameShortReads(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, Message{Type: "ok"}))

	// OneByteReader returns at most one byte per Read, exercising io.ReadFull.
	got, err := ReadFrame(iotest.OneByteReader(&buf), &Message{})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "ok", got.Type)
}

func TestReadFrameInvalidJSON(t *testing.T) {
	var buf bytes.Buffer
	body := []byte("{not valid json")
	require.NoError(t, binary.Write(&buf, binary.BigEndian, uint32(len(body))))
	buf.Write(body)

	_, err := ReadFrame(&buf, &Message{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestWriteFrameRejectsOversize(t *testing.T) {
	err := WriteFrame(io.Discard, Message{Type: "file", Data: make([]byte, MaxFrameSize+1)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too large")
}

func TestWriteFrameMarshalError(t *testing.T) {
	err := WriteFrame(io.Discard, make(chan int))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal")
}

func TestWriteFrameHeaderWriteError(t *testing.T) {
	err := WriteFrame(errWriter{err: errors.New("boom")}, Message{Type: "ok"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write frame header")
}

func TestWriteFrameBodyWriteError(t *testing.T) {
	err := WriteFrame(&failAfterWriter{}, Message{Type: "ok"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write frame body")
}

// failAfterWriter succeeds on the first Write (the header) and fails on the
// second (the body).
type failAfterWriter struct{ calls int }

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == 1 {
		return len(p), nil
	}
	return 0, errors.New("boom")
}

func TestConstants(t *testing.T) {
	assert.Equal(t, "2", Version)
	assert.Equal(t, "1", VersionLegacy)
	assert.Equal(t, uint32(52), DefaultPort)
	assert.Equal(t, 3, GuestCID)
	assert.Equal(t, 1<<20, MaxFrameSize)
	assert.Equal(t, 4<<20, MaxStreamFrameSize)
}

// TestCapabilitiesIsACopy proves a caller cannot change what the agent serves by
// mutating the slice it was handed.
func TestCapabilitiesIsACopy(t *testing.T) {
	first := Capabilities()
	require.Contains(t, first, CapHello)
	first[0] = "tampered"

	assert.Equal(t, CapHello, Capabilities()[0])
}

// TestHelloSupports pins the single place a host decides whether an operation is
// allowed: the capability list it negotiated.
func TestHelloSupports(t *testing.T) {
	h := Hello{Caps: []string{CapHello, CapExec}}
	assert.True(t, h.Supports(CapHello))
	assert.True(t, h.Supports(CapExec))
	assert.False(t, h.Supports(CapGet))
	assert.False(t, Hello{}.Supports(CapHello))
}

// TestCodeOf covers the two shapes a caller branches on: a typed protocol error
// keeps its code, and an error from anywhere else has none.
func TestCodeOf(t *testing.T) {
	assert.Equal(t, CodeUnsupportedOp, CodeOf(&Error{Code: CodeUnsupportedOp, Message: "x"}))
	assert.Equal(t, CodeDeadlineExceeded, CodeOf(fmt.Errorf("wrapped: %w", &Error{Code: CodeDeadlineExceeded})))
	assert.Equal(t, ErrorCode(""), CodeOf(errors.New("plain")))
	assert.Equal(t, ErrorCode(""), CodeOf(nil))
}

// TestFrameBoundsAreSeparate pins that the 1 MiB control cap still rejects a
// frame a streaming operation may carry: a stream limit can be raised without
// widening what a control frame is allowed to allocate.
func TestFrameBoundsAreSeparate(t *testing.T) {
	var buf bytes.Buffer
	payload := make([]byte, MaxFrameSize+1024)
	require.NoError(t, writeFrame(&buf, Message{Type: "file", Data: payload}, MaxStreamFrameSize))

	body := buf.Bytes()
	_, err := ReadFrame(bytes.NewReader(body), &Message{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")

	msg, err := ReadStreamFrame(bytes.NewReader(body), &Message{})
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Len(t, msg.Data, len(payload))
}

// TestRequestRoundTripCarriesIDAndDeadline pins the v2 request fields on the
// wire, including a negative budget, which is meaningful rather than absent.
func TestRequestRoundTripCarriesIDAndDeadline(t *testing.T) {
	var buf bytes.Buffer
	req := Request{Op: "exec", ID: "9", DeadlineMs: -1, Command: []string{"true"}}
	require.NoError(t, WriteFrame(&buf, req))

	var got Request
	_, err := ReadFrame(&buf, &got)
	require.NoError(t, err)
	assert.Equal(t, "9", got.ID)
	assert.Equal(t, int64(-1), got.DeadlineMs)
}

// TestErrorMessageRoundTripCarriesTypedCode pins the error frame shape: a typed
// code next to the human message, and no collision with an exit frame's code.
func TestErrorMessageRoundTripCarriesTypedCode(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, Message{
		Type:      "error",
		ID:        "3",
		ErrorCode: CodeUnsupportedOp,
		Error:     "op \"tar\" is not advertised",
	}))

	var msg Message
	_, err := ReadFrame(&buf, &msg)
	require.NoError(t, err)
	assert.Equal(t, CodeUnsupportedOp, msg.ErrorCode)
	assert.Equal(t, "3", msg.ID)
	assert.Nil(t, msg.Code, "an error frame must not carry an exit status")
	assert.Contains(t, msg.Error, "not advertised")
}
