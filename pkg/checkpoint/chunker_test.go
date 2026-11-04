package checkpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteReadRoundTrip(t *testing.T) {
	// Every case is a different edge of the chunk arithmetic: no image at all,
	// an image smaller than one chunk, an image that ends exactly on a boundary,
	// and one that ends inside the last chunk.
	tests := []struct {
		name string
		size int
	}{
		{"empty image", 0},
		{"one byte", 1},
		{"one byte short of a chunk", ChunkSize - 1},
		{"exactly one chunk", ChunkSize},
		{"one chunk and one byte", ChunkSize + 1},
		{"two chunks and a remainder", 2*ChunkSize + 12345},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			store := newTestLocal(t)
			id := "ckpt-0123456789abcdef"
			src := body(tc.size, 3)

			m, err := Write(ctx, store, id, testPin, bytes.NewReader(testState), bytes.NewReader(src))
			require.NoError(t, err)

			assert.Equal(t, FormatVersion, m.FormatVersion)
			assert.Equal(t, id, m.ID)
			assert.Equal(t, ChunkSize, m.ChunkSize)
			assert.Equal(t, int64(tc.size), m.TotalSize)
			assert.Equal(t, testPin, m.Pin)
			assert.Equal(t, stateOf(testState), m.State,
				"the manifest binds the state document the image resumes from")
			assert.Equal(t, chunkDigests(src, ChunkSize), m.Chunks,
				"the manifest names each chunk by the SHA-256 of its bytes, in image order")

			ids, err := store.List(ctx)
			require.NoError(t, err)
			assert.Equal(t, []string{id}, ids)

			// One blob per distinct chunk: the empty image stores none, and
			// repeated content would share.
			assert.Len(t, m.Chunks, len(chunkDigests(src, ChunkSize)))
			assert.Equal(t, len(uniqueDigests(m.Chunks)), blobCount(t, store.Root()))

			stored, err := store.Manifest(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, m, stored, "the published manifest is what a reader gets back")

			r, err := Read(ctx, store, stored, bytes.NewReader(testState))
			require.NoError(t, err)
			defer func() { assert.NoError(t, r.Close()) }()

			got, err := io.ReadAll(r)
			require.NoError(t, err)
			if !bytes.Equal(got, src) {
				t.Fatalf("the image read back differs: %d bytes read, want %d", len(got), len(src))
			}
		})
	}
}

func TestReadAcrossChunkBoundaries(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)
	src := body(ChunkSize+17, 5)

	m, err := Write(ctx, store, "ckpt-boundary", testPin, bytes.NewReader(testState), bytes.NewReader(src))
	require.NoError(t, err)

	r, err := Read(ctx, store, m, bytes.NewReader(testState))
	require.NoError(t, err)
	defer func() { assert.NoError(t, r.Close()) }()

	// 1000 is not a divisor of ChunkSize and not of the last chunk, so most
	// reads cross a chunk boundary or end exactly on one.
	buf := make([]byte, 1000)
	sum := sha256.New()
	total := 0
	for {
		n, err := r.Read(buf)
		total += n
		_, _ = sum.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if n > 0 && total > len(src) {
			t.Fatalf("the reader handed out %d bytes, want %d", total, len(src))
		}
	}

	want := sha256.Sum256(src)
	assert.Equal(t, len(src), total)
	assert.Equal(t, want[:], sum.Sum(nil))
}

func TestReadRefusesAManifestWrittenWithAnotherChunkSize(t *testing.T) {
	// The chunk size is part of the format, not a hint: a reader that honoured
	// any size would split another build's image with this build's arithmetic
	// and hand out bytes that look like an image and are not one.
	m := validManifest()
	m.ChunkSize = 1024
	m.TotalSize = int64(2 * 1024)
	m.Chunks = chunkDigests(body(2*1024, 7), 1024)

	_, err := Read(t.Context(), newTestLocal(t), m, bytes.NewReader(testState))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidManifest)
	assert.Contains(t, err.Error(), "chunkSize is 1024")
	assert.Contains(t, err.Error(), "4194304")
}

func TestWriteRefusesBeforeItWrites(t *testing.T) {
	tests := []struct {
		name string
		id   string
		pin  Pin
	}{
		{name: "an id that is a path", id: "../ckpt", pin: testPin},
		{name: "no Firecracker pin", id: "ckpt-nopin", pin: Pin{Kernel: testPin.Kernel}},
		{name: "no kernel pin", id: "ckpt-nopin", pin: Pin{Firecracker: testPin.Firecracker}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			store := newTestLocal(t)

			_, err := Write(ctx, store, tc.id, tc.pin, bytes.NewReader(testState), bytes.NewReader(body(2*ChunkSize, 1)))
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidManifest)

			ids, listErr := store.List(ctx)
			require.NoError(t, listErr)
			assert.Empty(t, ids, "a checkpoint that cannot be published must not cost writes first")
			assert.Zero(t, blobCount(t, store.Root()), "not one chunk may be stored")
		})
	}
}

func TestWritePublishesNothingWhenTheSourceFails(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)

	src := &failingReader{prefix: body(ChunkSize, 9), err: errors.New("the dump stopped")}
	_, err := Write(ctx, store, "ckpt-failed", testPin, bytes.NewReader(testState), src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the dump stopped")

	ids, listErr := store.List(ctx)
	require.NoError(t, listErr)
	assert.Empty(t, ids, "a checkpoint is published only when the whole image was read")

	// The chunk that was already stored stays, addressed and unreferenced: a
	// retried capture reuses it.
	assert.Equal(t, 1, blobCount(t, store.Root()))
}

func TestReadRefusesAnInvalidManifest(t *testing.T) {
	m := validManifest()
	m.TotalSize = 5000 // the chunk list no longer matches the size

	_, err := Read(t.Context(), newTestLocal(t), m, bytes.NewReader(testState))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidManifest)
}

func TestWriteStreamsWithoutHoldingTheImage(t *testing.T) {
	// Sixteen megabytes through a four-megabyte chunk buffer: an implementation
	// that read the image into memory would allocate the whole size.
	const imageSize = 4*ChunkSize + 7

	ctx := t.Context()
	store := newTestLocal(t)
	src := &countingReader{remaining: int64(imageSize)}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	m, err := Write(ctx, store, "ckpt-stream", testPin, bytes.NewReader(testState), src)
	require.NoError(t, err)

	runtime.ReadMemStats(&after)

	require.Equal(t, int64(imageSize), m.TotalSize)
	assert.LessOrEqual(t, src.maxRead, ChunkSize, "the source was asked for more than one chunk at a time")
	allocated := after.TotalAlloc - before.TotalAlloc
	assert.Less(t, allocated, uint64(imageSize/2),
		"writing %d bytes allocated %d: the image must be held one chunk at a time", imageSize, allocated)
}

func TestReadStreamsWithoutHoldingTheImage(t *testing.T) {
	const imageSize = 4*ChunkSize + 7

	ctx := t.Context()
	store := newTestLocal(t)
	_, err := Write(ctx, store, "ckpt-stream", testPin, bytes.NewReader(testState), &countingReader{remaining: int64(imageSize)})
	require.NoError(t, err)
	m, err := store.Manifest(ctx, "ckpt-stream")
	require.NoError(t, err)

	r, err := Read(ctx, store, m, bytes.NewReader(testState))
	require.NoError(t, err)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	n, err := io.Copy(io.Discard, r)
	require.NoError(t, err)

	runtime.ReadMemStats(&after)

	require.Equal(t, int64(imageSize), n)
	allocated := after.TotalAlloc - before.TotalAlloc
	assert.Less(t, allocated, uint64(imageSize/2),
		"reading %d bytes allocated %d: the image must be streamed one chunk at a time", imageSize, allocated)

	require.NoError(t, r.Close())
}

// countingReader streams a deterministic image of a given size without holding
// it, and records the largest buffer any consumer asked it for: a writer that
// reads the whole image at once asks for all of it.
type countingReader struct {
	remaining int64
	offset    int64
	maxRead   int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRead {
		r.maxRead = len(p)
	}
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	for i := range p {
		p[i] = byte(r.offset + int64(i))
	}
	r.offset += int64(len(p))
	r.remaining -= int64(len(p))
	return len(p), nil
}

// failingReader yields its prefix and then fails, as a memory dump that stopped
// being written would.
type failingReader struct {
	prefix []byte
	err    error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.prefix) == 0 {
		return 0, r.err
	}
	n := copy(p, r.prefix)
	r.prefix = r.prefix[n:]
	return n, nil
}

func TestWriteAndReadRequireTheirArguments(t *testing.T) {
	ctx := t.Context()

	_, err := Write(ctx, nil, "ckpt-x", testPin, bytes.NewReader(testState), strings.NewReader(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "store is required")

	_, err = Write(ctx, newTestLocal(t), "ckpt-x", testPin, nil, strings.NewReader(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state document is required")

	_, err = Write(ctx, newTestLocal(t), "ckpt-x", testPin, bytes.NewReader(testState), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "memory image is required")

	_, err = Read(ctx, nil, validManifest(), bytes.NewReader(testState))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "store is required")
}

func TestWriteStopsForACancelledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	store := newTestLocal(t)
	_, err := Write(ctx, store, "ckpt-x", testPin, bytes.NewReader(testState), &countingReader{remaining: int64(ChunkSize)})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, blobCount(t, store.Root()), "a cancelled capture stores nothing")
}

func TestReadRejectsAnotherCheckpointsState(t *testing.T) {
	// A checkpoint is a pair. Reading an image against the wrong state document
	// is refused here because the alternative is a VM resuming another VM's
	// devices, addresses and clocks with no error anywhere.
	ctx := t.Context()
	store := newTestLocal(t)
	image := body(ChunkSize+1, 21)

	m, err := Write(ctx, store, "ckpt-a", testPin, bytes.NewReader(testState), bytes.NewReader(image))
	require.NoError(t, err)

	// Another checkpoint's state, the same size: only the bytes differ, so a
	// size check alone would let this through.
	_, err = Read(ctx, store, m, bytes.NewReader(otherState))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateMismatch)
	assert.Contains(t, err.Error(), m.State.SHA256)

	// A state document that changed after the capture: truncated, and extended.
	_, err = Read(ctx, store, m, bytes.NewReader(testState[:len(testState)-1]))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateMismatch)

	longer := append(append([]byte(nil), testState...), 0)
	_, err = Read(ctx, store, m, bytes.NewReader(longer))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateMismatch)

	// The checkpoint's own state opens it.
	r, err := Read(ctx, store, m, bytes.NewReader(testState))
	require.NoError(t, err)
	require.NoError(t, r.Close())
}

func TestWriteRefusesAStateDocumentItCannotDescribe(t *testing.T) {
	tests := []struct {
		name  string
		state io.Reader
		want  string
	}{
		{name: "an empty document", state: bytes.NewReader(nil), want: "empty"},
		{name: "a document past the ceiling", state: io.LimitReader(zeros{}, maxStateSize+1), want: "larger than"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestLocal(t)
			_, err := Write(t.Context(), store, "ckpt-bad-state", testPin, tc.state, strings.NewReader(""))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)

			ids, listErr := store.List(t.Context())
			require.NoError(t, listErr)
			assert.Empty(t, ids, "a checkpoint with no state document is not published")
			assert.Zero(t, blobCount(t, store.Root()))
		})
	}
}

func TestReadReportsAStateDocumentItCannotRead(t *testing.T) {
	store := newTestLocal(t)
	m, err := Write(t.Context(), store, "ckpt-a", testPin, bytes.NewReader(testState), bytes.NewReader(body(16, 1)))
	require.NoError(t, err)

	_, err = Read(t.Context(), store, m, &failingReader{err: errors.New("the disk went away")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the disk went away")
	assert.NotErrorIs(t, err, ErrStateMismatch, "a read failure is not a pairing failure")
}
