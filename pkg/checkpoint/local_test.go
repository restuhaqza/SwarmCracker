package checkpoint

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalChunkLayout(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)
	content := body(1024, 2)
	digest := testDigest(content)

	require.NoError(t, store.PutChunk(ctx, digest, bytes.NewReader(content)))

	// The address is the layout: <root>/<first two characters>/<digest>.
	path := filepath.Join(store.Root(), string(digest)[:2], string(digest))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, got)

	info, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(dirMode), info.Mode().Perm(),
		"a memory image may hold guest secrets, so only the daemon reads it")
	assert.Zero(t, tempCount(t, store.Root()), "a published chunk leaves no temporary behind")
}

func TestDedupSharesOneBlobBetweenCheckpoints(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)
	image := body(2*ChunkSize+5, 4)

	first, err := Write(ctx, store, "ckpt-one", testPin, bytes.NewReader(image))
	require.NoError(t, err)
	second, err := Write(ctx, store, "ckpt-two", testPin, bytes.NewReader(image))
	require.NoError(t, err)

	require.Len(t, first.Chunks, 3)
	assert.Equal(t, first.Chunks, second.Chunks, "the same memory is the same addresses")
	assert.Equal(t, 3, blobCount(t, store.Root()), "a second checkpoint of identical memory stores no second blob")

	for _, id := range []string{"ckpt-one", "ckpt-two"} {
		m, err := store.Manifest(ctx, id)
		require.NoError(t, err)
		r, err := Read(ctx, store, m)
		require.NoError(t, err)
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())
		assert.Equal(t, image, got)
	}
}

func TestRepeatedChunksShareOneBlob(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)

	// A zero-filled image is two chunks with one address: one blob, two entries.
	image := make([]byte, 2*ChunkSize)
	m, err := Write(ctx, store, "ckpt-zero", testPin, bytes.NewReader(image))
	require.NoError(t, err)

	require.Len(t, m.Chunks, 2)
	assert.Equal(t, m.Chunks[0], m.Chunks[1])
	assert.Equal(t, 1, blobCount(t, store.Root()))
}

func TestConcurrentWritesOfOneChunk(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)
	content := body(ChunkSize, 6)
	digest := testDigest(content)

	const writers = 8
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = store.PutChunk(ctx, digest, bytes.NewReader(content))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "writer %d", i)
	}
	assert.Equal(t, 1, blobCount(t, store.Root()), "racing writes of one content leave one blob")
	assert.Zero(t, tempCount(t, store.Root()), "every writer that lost the race removed its temporary")

	stored, err := os.ReadFile(blobPath(store.Root(), digest))
	require.NoError(t, err)
	assert.Equal(t, content, stored)
}

func TestReadRejectsACorruptChunk(t *testing.T) {
	// A damaged first chunk fails before the image is handed out, a damaged last
	// chunk after most of it: both must be an error, never a short image.
	for _, tc := range []struct {
		name  string
		index int
	}{
		{"first chunk", 0},
		{"last chunk", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			store := newTestLocal(t)
			image := body(ChunkSize+256, 11)

			m, err := Write(ctx, store, "ckpt-corrupt", testPin, bytes.NewReader(image))
			require.NoError(t, err)
			require.Len(t, m.Chunks, 2)

			digest := m.Chunks[tc.index]
			path := blobPath(store.Root(), digest)
			damaged := append([]byte(nil), imageAt(t, path)...)
			damaged[0] ^= 0xff
			writeBlob(t, path, damaged)

			r, err := Read(ctx, store, m)
			require.NoError(t, err)
			_, err = io.Copy(io.Discard, r)
			require.NoError(t, r.Close())

			require.Error(t, err, "a flipped byte in a chunk must not read as an image")
			assert.ErrorIs(t, err, ErrCorrupt)
			assert.Contains(t, err.Error(), string(digest), "the error names the address that does not match")

			// The driver refuses the blob on its own too, not only under a reader.
			chunk, err := store.Chunk(ctx, digest)
			require.NoError(t, err)
			_, err = io.ReadAll(chunk)
			require.NoError(t, chunk.Close())
			assert.ErrorIs(t, err, ErrCorrupt)
		})
	}
}

func TestReadRejectsAChunkShorterThanTheManifestSays(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)
	image := body(ChunkSize+256, 12)

	m, err := Write(ctx, store, "ckpt-truncated", testPin, bytes.NewReader(image))
	require.NoError(t, err)

	digest := m.Chunks[1]
	path := blobPath(store.Root(), digest)
	require.NoError(t, os.Truncate(path, int64(len(image)-ChunkSize-1)))

	r, err := Read(ctx, store, m)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, r)
	require.NoError(t, r.Close())

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCorrupt)
}

func TestReadRejectsAChunkLongerThanTheManifestSays(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)

	// A blob can be longer than a chunk: a manifest that names it for a shorter
	// chunk is a manifest of another image. The reader must stop at the length
	// the manifest implies, not hand the extra bytes out.
	long := body(2*ChunkSize, 13)
	digest := testDigest(long)
	require.NoError(t, store.PutChunk(ctx, digest, bytes.NewReader(long)))

	m := Manifest{
		FormatVersion: FormatVersion,
		ID:            "ckpt-wrong-length",
		TotalSize:     int64(ChunkSize),
		ChunkSize:     ChunkSize,
		Chunks:        []Digest{digest},
		Pin:           testPin,
	}
	require.NoError(t, store.PutManifest(ctx, m))

	r, err := Read(ctx, store, m)
	require.NoError(t, err)
	got, err := io.ReadAll(r)
	require.NoError(t, r.Close())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "longer than")
	assert.LessOrEqual(t, len(got), ChunkSize, "the reader must not hand out past the manifest's length")
}

func TestMissingChunkIsReported(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)
	image := body(ChunkSize+256, 14)

	m, err := Write(ctx, store, "ckpt-missing", testPin, bytes.NewReader(image))
	require.NoError(t, err)
	missing := m.Chunks[1]
	require.NoError(t, os.Remove(blobPath(store.Root(), missing)))

	r, err := Read(ctx, store, m)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, r)
	require.NoError(t, r.Close())

	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrNotExist, "a chunk this node never had is distinguishable from a damaged one")

	_, err = store.Chunk(ctx, missing)
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestManifestIsReadableWithoutChunks(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)
	image := body(ChunkSize+256, 15)

	m, err := Write(ctx, store, "ckpt-index", testPin, bytes.NewReader(image))
	require.NoError(t, err)

	// Remove every blob and keep the index: a caller deciding whether it wants
	// the image reads kilobytes, never the image.
	entries, err := os.ReadDir(store.Root())
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != manifestsDirName {
			require.NoError(t, os.RemoveAll(filepath.Join(store.Root(), entry.Name())))
		}
	}

	stored, err := store.Manifest(ctx, "ckpt-index")
	require.NoError(t, err)
	assert.Equal(t, m, stored)

	r, err := Read(ctx, store, stored)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, r)
	require.NoError(t, r.Close())
	assert.ErrorIs(t, err, fs.ErrNotExist, "the image itself is gone")
}

func TestDeleteAndList(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)

	for _, id := range []string{"ckpt-c", "ckpt-a", "ckpt-b"} {
		_, err := Write(ctx, store, id, testPin, bytes.NewReader(body(64, 1)))
		require.NoError(t, err)
	}

	ids, err := store.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"ckpt-a", "ckpt-b", "ckpt-c"}, ids, "a listing is sorted")

	blobs := blobCount(t, store.Root())
	require.NoError(t, store.Delete(ctx, "ckpt-b"))
	require.NoError(t, store.Delete(ctx, "ckpt-b"), "deleting a checkpoint that is gone is success")

	ids, err = store.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"ckpt-a", "ckpt-c"}, ids)

	_, err = store.Manifest(ctx, "ckpt-b")
	assert.ErrorIs(t, err, fs.ErrNotExist)

	assert.Equal(t, blobs, blobCount(t, store.Root()),
		"a delete removes the checkpoint, not content another checkpoint may name")
}

func TestPutChunkRejectsBytesThatDoNotMatchTheirDigest(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)

	digest := testDigest(body(1024, 7))
	err := store.PutChunk(ctx, digest, bytes.NewReader(body(1024, 8)))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCorrupt)

	_, err = os.Stat(blobPath(store.Root(), digest))
	assert.ErrorIs(t, err, fs.ErrNotExist, "a refused stream publishes nothing")
	assert.Zero(t, tempCount(t, store.Root()), "a refused stream leaves no temporary behind")
}

func TestPutChunkRejectsAMalformedDigest(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)

	content := body(16, 1)
	for _, digest := range []Digest{"", "abc", Digest(hexDigest(content)[:63]), Digest("AB" + hexDigest(content)[2:])} {
		err := store.PutChunk(ctx, digest, bytes.NewReader(content))
		require.Error(t, err, "digest %q", digest)
		assert.Contains(t, err.Error(), "not a chunk digest")
	}
	assert.Zero(t, blobCount(t, store.Root()))

	_, err := store.Chunk(ctx, "not-a-digest")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a chunk digest")
}

func TestManifestOfAnotherCheckpointIsRefused(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)
	_, err := Write(ctx, store, "ckpt-a", testPin, bytes.NewReader(body(32, 1)))
	require.NoError(t, err)

	// A file copied or renamed on disk must not answer for an id it does not
	// name, or a caller would resume an image it did not ask for.
	original := filepath.Join(store.Root(), manifestsDirName, "ckpt-a"+manifestSuffix)
	copied := filepath.Join(store.Root(), manifestsDirName, "ckpt-b"+manifestSuffix)
	data, err := os.ReadFile(original)
	require.NoError(t, err)
	writeBlob(t, copied, data)

	_, err = store.Manifest(ctx, "ckpt-b")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidManifest)
	assert.Contains(t, err.Error(), "belongs to")
}

func TestNewLocal(t *testing.T) {
	t.Run("a root is required", func(t *testing.T) {
		_, err := NewLocal(LocalConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "root directory is required")
	})

	t.Run("the directories are created and left clean", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), DirName)
		store, err := NewLocal(LocalConfig{Root: root})
		require.NoError(t, err)
		assert.Equal(t, root, store.Root())

		for _, dir := range []string{root, filepath.Join(root, manifestsDirName)} {
			info, err := os.Stat(dir)
			require.NoError(t, err)
			assert.Equal(t, fs.FileMode(dirMode), info.Mode().Perm())
		}

		entries, err := os.ReadDir(filepath.Join(root, manifestsDirName))
		require.NoError(t, err)
		assert.Empty(t, entries, "the write probe is removed")
	})
}

// imageAt reads a blob the test is about to damage.
func imageAt(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	return data
}

func TestLocalRefusesToBeUsedUnopened(t *testing.T) {
	ctx := t.Context()
	digest := testDigest(body(16, 1))

	for name, store := range map[string]*Local{"a zero value": {}, "a nil pointer": nil} {
		t.Run(name, func(t *testing.T) {
			errs := map[string]error{
				"PutManifest": store.PutManifest(ctx, validManifest()),
				"PutChunk":    store.PutChunk(ctx, digest, bytes.NewReader(body(16, 1))),
				"Delete":      store.Delete(ctx, "ckpt-x"),
			}
			_, errs["Manifest"] = store.Manifest(ctx, "ckpt-x")
			_, errs["Chunk"] = store.Chunk(ctx, digest)
			_, errs["List"] = store.List(ctx)

			for op, err := range errs {
				require.Error(t, err, "%s on an unopened store", op)
				assert.Contains(t, err.Error(), "use NewLocal", "%s on an unopened store", op)
			}
		})
	}
}

func TestLocalReportsACancelledContext(t *testing.T) {
	store := newTestLocal(t)
	digest := testDigest(body(16, 1))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	errs := map[string]error{
		"PutManifest": store.PutManifest(ctx, validManifest()),
		"PutChunk":    store.PutChunk(ctx, digest, bytes.NewReader(body(16, 1))),
		"Delete":      store.Delete(ctx, "ckpt-x"),
	}
	_, errs["Manifest"] = store.Manifest(ctx, "ckpt-x")
	_, errs["Chunk"] = store.Chunk(ctx, digest)
	_, errs["List"] = store.List(ctx)

	for op, err := range errs {
		assert.ErrorIs(t, err, context.Canceled, "%s must not start work for a cancelled caller", op)
	}
}

func TestPutManifestRejectsAnInvalidManifest(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)

	m := validManifest()
	m.FormatVersion = FormatVersion + 1
	err := store.PutManifest(ctx, m)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidManifest)

	ids, listErr := store.List(ctx)
	require.NoError(t, listErr)
	assert.Empty(t, ids, "an unusable manifest publishes nothing")
}

func TestMalformedCheckpointIDsAreRefused(t *testing.T) {
	ctx := t.Context()
	store := newTestLocal(t)

	ids := []string{"", ".", "..", "a/b", "a\x00b", strings.Repeat("c", maxIDBytes+1)}
	for _, id := range ids {
		require.Error(t, store.Delete(ctx, id), "delete %q", id)
		_, err := store.Manifest(ctx, id)
		require.Error(t, err, "manifest %q", id)
		assert.Contains(t, err.Error(), "not a checkpoint ID")
	}
}

func TestPutChunkRequiresASource(t *testing.T) {
	err := newTestLocal(t).PutChunk(t.Context(), testDigest(body(16, 1)), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source is required")
}

func TestNewLocalRejectsARootThatIsAFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "checkpoints")
	require.NoError(t, os.WriteFile(root, []byte("not a directory"), 0o600))

	_, err := NewLocal(LocalConfig{Root: root})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create")
}

func TestDir(t *testing.T) {
	assert.Equal(t, "", Dir(""))
	assert.Equal(t, "", Dir("   "))
	assert.Equal(t, filepath.Join("/var/lib/swarmcracker", DirName), Dir("/var/lib/swarmcracker"))
}
