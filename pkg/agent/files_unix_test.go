//go:build unix && !integration

package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStreamGuestTarSkipsWhatItCannotCarry covers the entries that are neither
// files, directories nor links. A FIFO has no content and opening one for reading
// blocks until something writes to it, so the walk must not touch it: it is left
// out of the archive, the stream still finishes, and the caller is told which
// paths are missing rather than handed an archive that silently omits them.
func TestStreamGuestTarSkipsWhatItCannotCarry(t *testing.T) {
	root := withFileRoot(t)
	writeTree(t, root, map[string]string{"tree/keep.txt": "kept"})
	require.NoError(t, syscall.Mkfifo(filepath.Join(root, "tree/pipe"), 0o600))

	var archive bytes.Buffer
	summary, err := streamGuestTar(context.Background(), "tree", bufferSink(&archive))
	require.NoError(t, err)

	assert.Equal(t, 2, summary.Entries, "the directory and the file it holds")
	require.Len(t, summary.Warnings, 1)
	assert.Equal(t, "tree/pipe: fifo", summary.Warnings[0])

	entries := readArchive(t, archive.Bytes())
	assert.Equal(t, []string{"tree/", "tree/keep.txt"}, sortedNames(entries))
}

// TestStreamGuestTarReportsMoreSkippedEntriesThanItLists proves the count stays
// exact when the reported list is cut short: the last element carries how many
// were left out, so a caller can never read a capped list as the whole truth.
func TestStreamGuestTarReportsMoreSkippedEntriesThanItLists(t *testing.T) {
	root := withFileRoot(t)
	writeTree(t, root, map[string]string{"tree/keep.txt": "kept"})
	pipes := maxTarWarnings + 3
	for i := 0; i < pipes; i++ {
		require.NoError(t, syscall.Mkfifo(filepath.Join(root, "tree", "pipe"+leftPad(i)), 0o600))
	}

	var archive bytes.Buffer
	summary, err := streamGuestTar(context.Background(), "tree", bufferSink(&archive))
	require.NoError(t, err)

	require.Len(t, summary.Warnings, maxTarWarnings+1, "the capped list plus the count of the rest")
	assert.Equal(t, "... and 3 more entries the archive does not carry", summary.Warnings[maxTarWarnings])
	assert.Equal(t, 2, summary.Entries)
}

// TestOpenFileRootRefusesALinkThatLeavesTheRoot is the enforcement half of the
// boundary, checked where it is enforced: the operating system's own resolution of
// a path that leaves the root fails, so a walk cannot reach outside it even when
// the path looked confined.
func TestOpenFileRootRefusesALinkThatLeavesTheRoot(t *testing.T) {
	root := withFileRoot(t)
	require.NoError(t, os.Symlink("/etc", filepath.Join(root, "etclink")))

	fileRoot := mustOpenFileRoot(t)
	_, err := fileRoot.Lstat("etclink/hostname")
	require.Error(t, err, "a path through a link that leaves the root is never resolved")
}
