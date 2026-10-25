package console

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// detachChunkReader returns data in fixed-size chunks, so the filter can be
// exercised with a sequence that straddles two reads.
type detachChunkReader struct {
	data []byte
	size int
	off  int
}

func (r *detachChunkReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := r.size
	if left := len(r.data) - r.off; n > left {
		n = left
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.data[r.off:r.off+n])
	r.off += n
	return n, nil
}

// drain reads r to the end and returns the bytes plus the terminal error.
func drain(t *testing.T, r io.Reader) ([]byte, error) {
	t.Helper()
	return io.ReadAll(r)
}

func TestDetachReaderForwardsInput(t *testing.T) {
	got, err := drain(t, NewDetachReader(strings.NewReader("hello")))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestDetachReaderInterceptsSequence(t *testing.T) {
	filter := NewDetachReader(strings.NewReader("a\x10\x11b"))

	got, err := drain(t, filter)
	require.ErrorIs(t, err, ErrDetached)
	assert.Equal(t, "a", string(got), "bytes before the sequence are delivered, nothing after it")
	assert.True(t, filter.Detached())
	assertClosed(t, filter.Done())
}

func TestDetachReaderInterceptsAcrossReads(t *testing.T) {
	filter := NewDetachReader(&detachChunkReader{data: []byte("\x10\x11"), size: 1})

	got, err := drain(t, filter)
	require.ErrorIs(t, err, ErrDetached)
	assert.Empty(t, got)
}

func TestDetachReaderPassesLoneCtrlP(t *testing.T) {
	got, err := drain(t, NewDetachReader(strings.NewReader("a\x10b")))
	require.NoError(t, err)
	assert.Equal(t, "a\x10b", string(got))
}

func TestDetachReaderPassesTrailingCtrlP(t *testing.T) {
	got, err := drain(t, NewDetachReader(strings.NewReader("a\x10")))
	require.NoError(t, err)
	assert.Equal(t, "a\x10", string(got), "a prefix at EOF is input, not a pending detach")
}

// TestDetachReaderDeliversPendingBytesBeforeTheError pins the read contract: a
// caller with a small buffer still receives the bytes typed before the
// sequence, and only the next call reports the detach.
func TestDetachReaderDeliversPendingBytesBeforeTheError(t *testing.T) {
	filter := NewDetachReader(strings.NewReader("abc\x10\x11"))

	buf := make([]byte, 2)
	n, err := filter.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "ab", string(buf[:n]))

	n, err = filter.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "c", string(buf[:n]))

	_, err = filter.Read(buf)
	require.ErrorIs(t, err, ErrDetached)
}

// TestDetachReaderStopsAfterDetach pins that the reader never yields bytes
// again once the sequence has been seen.
func TestDetachReaderStopsAfterDetach(t *testing.T) {
	filter := NewDetachReader(strings.NewReader("\x10\x11trailing"))

	_, err := drain(t, filter)
	require.ErrorIs(t, err, ErrDetached)

	buf := make([]byte, 4)
	n, err := filter.Read(buf)
	assert.ErrorIs(t, err, ErrDetached)
	assert.Zero(t, n)
}

func TestDetachReaderPropagatesReadError(t *testing.T) {
	boom := errors.New("boom")
	filter := NewDetachReader(io.MultiReader(strings.NewReader("x"), errReader{err: boom}))

	got, err := drain(t, filter)
	require.ErrorIs(t, err, boom)
	assert.Equal(t, "x", string(got))
}

// errReader fails every read with err.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// assertClosed fails unless ch is already closed.
func assertClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatal("channel should be closed")
	}
}

// TestDetachReaderDetachedIsReadableWhileReading pins the concurrency
// contract: the reading goroutine sets the detach flag while another goroutine
// polls Detached and watches Done. The attach client does exactly that, and
// under -race this fails if the flag is a plain bool.
func TestDetachReaderDetachedIsReadableWhileReading(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	filter := NewDetachReader(stdinR)

	readDone := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, filter); readDone <- err }()

	polled := make(chan struct{})
	go func() {
		defer close(polled)
		deadline := time.After(5 * time.Second)
		for {
			select {
			case <-filter.Done():
				return
			case <-deadline:
				return
			default:
				_ = filter.Detached()
			}
		}
	}()

	_, err := stdinW.Write([]byte("hello"))
	require.NoError(t, err)
	_, err = stdinW.Write([]byte("\x10\x11"))
	require.NoError(t, err)

	select {
	case err := <-readDone:
		require.ErrorIs(t, err, ErrDetached)
	case <-time.After(5 * time.Second):
		t.Fatal("the reader did not stop on the detach sequence")
	}

	<-polled
	assert.True(t, filter.Detached())
}

// TestDetachReaderByteIdenticalToPlainCopy checks the common case in bulk: with
// no sequence in the stream the filter is a pass-through, byte for byte.
func TestDetachReaderByteIdenticalToPlainCopy(t *testing.T) {
	input := bytes.Repeat([]byte("abc\x00\x11\x10"), 512) // no full sequence
	got, err := drain(t, NewDetachReader(bytes.NewReader(input)))
	require.NoError(t, err)
	assert.Equal(t, input, got)
}
