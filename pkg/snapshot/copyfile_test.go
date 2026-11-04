package snapshot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancellingContext reports context.Canceled after a fixed number of successful
// checks, so a test can stop a copy at a known chunk without a sleep and without
// depending on timing.
type cancellingContext struct {
	context.Context

	mu        sync.Mutex
	remaining int
}

func newCancellingContext(afterChecks int) *cancellingContext {
	return &cancellingContext{Context: context.Background(), remaining: afterChecks}
}

func (c *cancellingContext) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.remaining <= 0 {
		return context.Canceled
	}
	c.remaining--
	return nil
}

// TestCopyRangeStopsBetweenChunks pins that a copy is interruptible: the context
// is checked before each chunk, so a cancelled request stops within one chunk
// instead of after a whole multi-GB image.
func TestCopyRangeStopsBetweenChunks(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789abcdef"), 4*(1<<20)/16) // 4 MiB
	require.Len(t, content, 4<<20)

	var dst bytes.Buffer
	ctx := newCancellingContext(2)

	written, err := copyRange(ctx, &dst, bytes.NewReader(content), -1)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int64(2*copyChunkSize), written, "the copy must stop on a chunk boundary")
	assert.Len(t, dst.Bytes(), 2*copyChunkSize)
}

// TestCopyRangeCopiesExactlyTheRequestedBytes is the other half: the chunking must
// not change what an extent copy produces.
func TestCopyRangeCopiesExactlyTheRequestedBytes(t *testing.T) {
	content := bytes.Repeat([]byte("x"), 2*copyChunkSize+17)

	var dst bytes.Buffer
	written, err := copyRange(context.Background(), &dst, bytes.NewReader(content), int64(len(content)))
	require.NoError(t, err)

	assert.Equal(t, int64(len(content)), written)
	assert.Equal(t, content, dst.Bytes())
}

// TestCopyFileStopsWhenTheContextIsAlreadyCancelled pins that a request that gave
// up before the copy started creates nothing at all.
func TestCopyFileStopsWhenTheContextIsAlreadyCancelled(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "task.ext4")
	dst := filepath.Join(dir, "copy.ext4")
	require.NoError(t, os.WriteFile(src, testDiskContent(), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := copyFile(ctx, src, dst)
	require.ErrorIs(t, err, context.Canceled)

	_, statErr := os.Stat(dst)
	assert.True(t, os.IsNotExist(statErr), "a copy that never started must not create its destination")
}

// TestCopyFileLeavesNothingWhenTheDestinationCannotBeCreated pins the failure
// path a full or read-only disk takes: the copy reports an error and no partial
// destination survives. The parent is a regular file, so the failure does not
// depend on running as a non-root user.
func TestCopyFileLeavesNothingWhenTheDestinationCannotBeCreated(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "task.ext4")
	require.NoError(t, os.WriteFile(src, testDiskContent(), 0o600))

	blocked := filepath.Join(dir, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o600))
	dst := filepath.Join(blocked, "copy.ext4")

	_, err := copyFile(context.Background(), src, dst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create destination directory")

	// The path cannot exist: its parent is a file. ENOTDIR is what the stat
	// reports, and it is the portable way to say "no destination here" without
	// depending on the failure the copy hit.
	_, statErr := os.Stat(dst)
	assert.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr) || errors.Is(statErr, syscall.ENOTDIR),
		"no partial destination may exist after a failed copy: %v", statErr)
}

// TestCopyFileRefusesADestinationThatIsItsSource pins the rule that makes a copy a
// copy: an instance or a snapshot gets its own file, and the disk it was copied from
// is never opened for writing. Truncating it would destroy the checkpoint's recorded
// disk — or, for a launch, the disk another instance is already running on.
func TestCopyFileRefusesADestinationThatIsItsSource(t *testing.T) {
	content := testDiskContent()

	cases := map[string]func(t *testing.T, src string) string{
		"the same path": func(_ *testing.T, src string) string {
			return src
		},
		"a hard link": func(t *testing.T, src string) string {
			link := filepath.Join(t.TempDir(), "link.ext4")
			require.NoError(t, os.Link(src, link))
			return link
		},
		"a symlink": func(t *testing.T, src string) string {
			link := filepath.Join(t.TempDir(), "link.ext4")
			require.NoError(t, os.Symlink(src, link))
			return link
		},
	}

	for name, destination := range cases {
		t.Run(name, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "task.ext4")
			require.NoError(t, os.WriteFile(src, content, 0o600))
			dst := destination(t, src)

			mode, err := copyFile(context.Background(), src, dst)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSharedDisk)
			assert.Equal(t, CopyNone, mode)

			// The point of refusing: the source still holds every byte.
			after, err := os.ReadFile(src)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(content, after), "the source must be untouched")
		})
	}
}
