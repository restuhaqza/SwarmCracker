package agent

import (
	"bytes"
	"encoding/binary"
	"errors"
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
	assert.Equal(t, "1", Version)
	assert.Equal(t, uint32(52), DefaultPort)
	assert.Equal(t, 3, GuestCID)
	assert.Equal(t, 1<<20, MaxFrameSize)
}
