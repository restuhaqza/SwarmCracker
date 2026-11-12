package snapshot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The exported copy, and the paths a filesystem on this host never reaches: the last
// fallback, the extent-free byte copy and the failures a caller must be told about
// rather than given a half-written disk for.

// TestCopyDiskGivesTheCallerItsOwnFile pins the exported contract the sandbox control
// plane relies on: the destination is a different file with the source's bytes, so the
// caller can write to it (a per-boot credential, in that caller's case) without touching
// the template it was built from.
func TestCopyDiskGivesTheCallerItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "golden-base.ext4")
	content := testDiskContent()
	require.NoError(t, os.WriteFile(src, content, 0o644))

	dst := filepath.Join(dir, "sandboxes", "sbx-1.ext4")
	mode, err := CopyDisk(context.Background(), src, dst)
	require.NoError(t, err)
	assert.NotEqual(t, CopyNone, mode, "a copy that succeeded reports how it was made")

	copied, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, copied), "the destination holds the source's bytes")

	// A different file, not a second name for the same one: writing the copy must leave
	// the template alone, which is the whole reason the copy exists.
	srcInfo, err := os.Stat(src)
	require.NoError(t, err)
	dstInfo, err := os.Stat(dst)
	require.NoError(t, err)
	assert.False(t, os.SameFile(srcInfo, dstInfo))
	require.NoError(t, os.WriteFile(dst, []byte("sandbox's own bytes"), 0o644))
	after, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, after), "the source must be untouched")

	// And the rule holds through the exported call, not only through copyFile.
	_, err = CopyDisk(context.Background(), src, src)
	assert.ErrorIs(t, err, ErrSharedDisk)
}

// TestCopyDiskNamesWhatItCannotCopy pins the failures a caller acts on: a source that is
// not there, one that is not a file, and a destination that is a directory.
func TestCopyDiskNamesWhatItCannotCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "task.ext4")
	require.NoError(t, os.WriteFile(src, testDiskContent(), 0o600))

	_, err := CopyDisk(context.Background(), filepath.Join(dir, "absent.ext4"), filepath.Join(dir, "copy.ext4"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open source")

	_, err = CopyDisk(context.Background(), dir, filepath.Join(dir, "copy.ext4"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")

	occupied := filepath.Join(dir, "occupied.ext4")
	require.NoError(t, os.MkdirAll(occupied, 0o755))
	_, err = CopyDisk(context.Background(), src, occupied)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create destination")

	// A destination that cannot even be stat'ed — a symlink loop — is refused rather
	// than reported as "does not exist".
	loop := filepath.Join(dir, "loop.ext4")
	require.NoError(t, os.Symlink(loop, loop))
	_, err = CopyDisk(context.Background(), src, loop)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stat destination")
}

// TestCopyAllIsTheLastFallback pins the copy every filesystem on this host can skip: when
// neither reflink nor extent reporting works, the bytes are copied and the holes come
// with them. It is called directly because ext4 and tmpfs both report extents, so no
// test here can make the fallback run by itself — and a refactor of copyRange that broke
// it would otherwise go unnoticed.
func TestCopyAllIsTheLastFallback(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "disk.ext4")
	content := bytes.Repeat([]byte("0123456789abcdef"), 64)
	require.NoError(t, os.WriteFile(srcPath, content, 0o600))

	dstPath := filepath.Join(dir, "copy.ext4")
	// A destination left longer than the source by a failed attempt: the fallback starts
	// from a clean slate, so its length is the source's.
	require.NoError(t, os.WriteFile(dstPath, bytes.Repeat([]byte("x"), len(content)+4096), 0o600))

	src, err := os.Open(srcPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	dst, err := os.OpenFile(dstPath, os.O_RDWR, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dst.Close() })

	require.NoError(t, copyAll(context.Background(), dst, src))

	copied, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, copied), "every byte, and nothing the destination had before")
}

// TestCopyRangeReportsWhatTheStreamsDid pins the three ways a chunked copy can go wrong
// quietly: a write that fails, a write that comes up short, and a source that ends before
// the byte count it was asked for.
func TestCopyRangeReportsWhatTheStreamsDid(t *testing.T) {
	ctx := context.Background()

	writeErr := errors.New("disk full")
	_, err := copyRange(ctx, failingWriter{err: writeErr}, strings.NewReader("data"), -1)
	assert.ErrorIs(t, err, writeErr)

	_, err = copyRange(ctx, shortWriter{}, strings.NewReader("data"), -1)
	assert.ErrorIs(t, err, io.ErrShortWrite)

	_, err = copyRange(ctx, io.Discard, strings.NewReader("abc"), 10)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "a short source is not a complete copy")

	readErr := errors.New("io error")
	_, err = copyRange(ctx, io.Discard, failingReader{err: readErr}, -1)
	assert.ErrorIs(t, err, readErr)
}

// TestResetForCopyReportsAFileItCannotRewind pins the fallback's precondition: a file that
// cannot be emptied or rewound fails the copy instead of leaving the extents of a previous
// attempt behind.
func TestResetForCopyReportsAFileItCannotRewind(t *testing.T) {
	src, err := os.CreateTemp(t.TempDir(), "src-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	dst, err := os.CreateTemp(t.TempDir(), "dst-*")
	require.NoError(t, err)
	require.NoError(t, dst.Close())

	assert.Error(t, resetForCopy(dst, src), "a closed destination cannot be truncated")
	assert.Error(t, resetForCopy(src, dst), "a closed source cannot be rewound")
}

// TestReplaceFilePublishesInOneStep pins the restore-side copy: the bytes are staged in a
// temporary beside the destination and renamed over it, so a failed copy leaves whatever
// was there untouched — and a failure is returned, not hidden.
func TestReplaceFilePublishesInOneStep(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "snapshot.ext4")
	content := testDiskContent()
	require.NoError(t, os.WriteFile(src, content, 0o600))

	dst := filepath.Join(dir, "task.ext4")
	require.NoError(t, os.WriteFile(dst, []byte("the disk an earlier restore left"), 0o600))

	require.NoError(t, replaceFile(context.Background(), src, dst))
	published, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, published), "the restore replaces the disk a previous VM left")

	// A source that is not there is a failure for the caller, and the destination it
	// could not produce is not left holding half of one.
	err = replaceFile(context.Background(), filepath.Join(dir, "absent.ext4"), dst)
	require.Error(t, err)
	stillThere, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, stillThere), "a failed restore leaves the destination as it was")

	// A destination that cannot be replaced — a directory — is named rather than
	// silently skipped.
	occupied := filepath.Join(dir, "occupied.ext4")
	require.NoError(t, os.MkdirAll(filepath.Join(occupied, "held"), 0o755))
	err = replaceFile(context.Background(), src, occupied)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish restored disk")
}

// failingWriter fails every write, which is how a full destination disk answers.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

// shortWriter accepts all but the last byte of every write.
type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

// failingReader fails every read, which is how a disk that reported an I/O error
// answers.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
