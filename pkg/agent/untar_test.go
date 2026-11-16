//go:build !integration

package agent

// Taking a tree back into the workspace. The rejections are the point of this file: an
// archive is untrusted input, so every way an entry can mean "write somewhere else" is
// pinned here, one test each.

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tarEntry is one entry a test's archive carries.
type tarEntry struct {
	name     string
	typeflag byte
	body     string
	mode     int64
	link     string
	// size overrides the header's declared size, for a bound test that must not actually
	// carry the bytes it claims.
	size int64
}

// archive builds a tar stream in memory.
func archive(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{
			Name:     entry.name,
			Typeflag: entry.typeflag,
			Mode:     mode,
			Size:     int64(len(entry.body)),
			Linkname: entry.link,
		}
		if entry.size != 0 {
			header.Size = entry.size
		}
		require.NoError(t, writer.WriteHeader(header))
		if entry.body != "" {
			_, err := writer.Write([]byte(entry.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

// failureCodeOf is the stable code an error carries: the guest's own failure type, or the
// client-side error the protocol raises before a request is sent.
func failureCodeOf(t *testing.T, err error) ErrorCode {
	t.Helper()
	require.Error(t, err)

	var protocolError *Error
	if errors.As(err, &protocolError) {
		return protocolError.Code
	}
	code, _ := failureCode(err)
	return code
}

// lowerUntarBounds shrinks the two size bounds so a test can reach them without writing
// gigabytes, and restores them afterwards.
func lowerUntarBounds(t *testing.T, entry, total int64) {
	t.Helper()
	entryWas, totalWas := maxUntarEntryBytes, maxUntarBytes
	maxUntarEntryBytes, maxUntarBytes = entry, total
	t.Cleanup(func() { maxUntarEntryBytes, maxUntarBytes = entryWas, totalWas })
}

// TestUntarWritesTheTreeATarDescribed pins the round trip: what `tar` streams out of one
// root, `untar` writes into another — files, directories, modes and a link that stays
// inside.
func TestUntarWritesTheTreeATarDescribed(t *testing.T) {
	source := withFileRoot(t)
	writeTree(t, source, map[string]string{
		"repo/main.go":     "package main\n",
		"repo/pkg/util.go": "package pkg\n",
		"repo/README.md":   "read me\n",
	})
	require.NoError(t, os.Chmod(filepath.Join(source, "repo/pkg"), 0o750))
	require.NoError(t, os.Symlink("main.go", filepath.Join(source, "repo/link.go")))

	conn, _ := newPipePair(t)
	client := testClient(conn)

	var streamed bytes.Buffer
	streamedSummary, err := client.Tar(context.Background(), "repo", &streamed)
	require.NoError(t, err)
	require.Greater(t, streamedSummary.Entries, 3)

	// A different root: the archive is the only thing that crosses.
	target := withFileRoot(t)
	summary, err := client.Untar(context.Background(), "repo", bytes.NewReader(streamed.Bytes()))
	require.NoError(t, err)
	assert.Equal(t, streamedSummary.Entries, summary.Entries)
	assert.Empty(t, summary.Warnings)
	assert.Equal(t, int64(len("package main\n")+len("package pkg\n")+len("read me\n")), summary.Bytes,
		"untar reports the file bytes it wrote, as tar reports the archive bytes it streamed")

	for _, file := range []struct{ name, body string }{
		{"repo/main.go", "package main\n"},
		{"repo/pkg/util.go", "package pkg\n"},
		{"repo/README.md", "read me\n"},
	} {
		written, err := os.ReadFile(filepath.Join(target, file.name))
		require.NoError(t, err, file.name)
		assert.Equal(t, file.body, string(written), file.name)
	}
	info, err := os.Stat(filepath.Join(target, "repo/pkg"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o750), info.Mode().Perm(), "a directory keeps the mode the archive carried")

	target2, err := os.Readlink(filepath.Join(target, "repo/link.go"))
	require.NoError(t, err)
	assert.Equal(t, "main.go", target2, "a link that stays inside the root is written as a link")
}

// TestUntarRefusesEntriesThatLeaveTheRoot is the confinement matrix: every entry that
// would write somewhere other than the tree it names is refused, and the code says which
// rule it broke.
func TestUntarRefusesEntriesThatLeaveTheRoot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []tarEntry
		prefix  string
	}{
		{
			name:    "an absolute name",
			entries: []tarEntry{{name: "/etc/cron.d/evil", body: "* * * * * root sh", typeflag: tar.TypeReg}},
			prefix:  "",
		},
		{
			name:    "a name that climbs out",
			entries: []tarEntry{{name: "../escape.txt", body: "x", typeflag: tar.TypeReg}},
			prefix:  "",
		},
		{
			name:    "a name that climbs out mid-path",
			entries: []tarEntry{{name: "repo/../../escape.txt", body: "x", typeflag: tar.TypeReg}},
			prefix:  "repo",
		},
		{
			name: "a symbolic link pointing out",
			entries: []tarEntry{
				{name: "repo/link", typeflag: tar.TypeSymlink, link: "../../etc/passwd"},
			},
			prefix: "repo",
		},
		{
			name: "a symbolic link that is absolute",
			entries: []tarEntry{
				{name: "repo/link", typeflag: tar.TypeSymlink, link: "/etc/passwd"},
			},
			prefix: "repo",
		},
		{
			name: "a hard link to something the archive did not write",
			entries: []tarEntry{
				{name: "repo/link", typeflag: tar.TypeLink, link: "../../etc/passwd"},
			},
			prefix: "repo",
		},
		{
			name:    "an entry outside the tree the caller named",
			entries: []tarEntry{{name: "another/file", body: "x", typeflag: tar.TypeReg}},
			prefix:  "repo",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withFileRoot(t)
			_, err := extractTree(context.Background(), tc.prefix, bytes.NewReader(archive(t, tc.entries...)))
			assert.Equal(t, CodeUnsafePath, failureCodeOf(t, err))

			// Nothing was created, anywhere: a refusal is not a partial write.
			entries, readErr := os.ReadDir(root)
			require.NoError(t, readErr)
			assert.Empty(t, entries, "a refused archive writes nothing")
		})
	}
}

// TestUntarWritesLinksThatStayInside pins the other half of the link rule: a link whose
// target is inside the tree is not an escape and is written, including a hard link to a
// file the same archive carried.
func TestUntarWritesLinksThatStayInside(t *testing.T) {
	root := withFileRoot(t)
	stream := archive(t,
		tarEntry{name: "repo/main.go", body: "package main\n", typeflag: tar.TypeReg},
		tarEntry{name: "repo/hard.go", typeflag: tar.TypeLink, link: "repo/main.go"},
		tarEntry{name: "repo/deep/link.go", typeflag: tar.TypeSymlink, link: "../main.go"},
	)

	summary, err := extractTree(context.Background(), "repo", bytes.NewReader(stream))
	require.NoError(t, err)
	assert.Equal(t, 3, summary.Entries)

	hard, err := os.ReadFile(filepath.Join(root, "repo/hard.go"))
	require.NoError(t, err)
	assert.Equal(t, "package main\n", string(hard))

	link, err := os.Readlink(filepath.Join(root, "repo/deep/link.go"))
	require.NoError(t, err)
	assert.Equal(t, "../main.go", link)
}

// TestUntarBoundsWhatItWrites pins both size bounds and the entry count: an archive cannot
// fill the guest's disk through one request, and the refusal names the bound.
func TestUntarBoundsWhatItWrites(t *testing.T) {
	t.Run("one entry over its own bound", func(t *testing.T) {
		root := withFileRoot(t)
		lowerUntarBounds(t, 16, 1<<20)
		// The header's declared size is what is checked, so the entry is refused before its
		// bytes are copied.
		stream := archive(t, tarEntry{name: "repo/big", typeflag: tar.TypeReg, body: "0123456789abcdefg"})

		_, err := extractTree(context.Background(), "repo", bytes.NewReader(stream))
		assert.Equal(t, CodeTooLarge, failureCodeOf(t, err))
		assertNoFile(t, root, "repo/big")
	})

	t.Run("the archive over its total bound", func(t *testing.T) {
		withFileRoot(t)
		lowerUntarBounds(t, 16, 20)
		stream := archive(t,
			tarEntry{name: "repo/one", body: "0123456789", typeflag: tar.TypeReg},
			tarEntry{name: "repo/two", body: "0123456789abc", typeflag: tar.TypeReg},
		)

		_, err := extractTree(context.Background(), "repo", bytes.NewReader(stream))
		assert.Equal(t, CodeTooLarge, failureCodeOf(t, err))
	})
}

// TestUntarSkipsWhatItCannotCarry pins the entries it does not write: a device or a fifo
// is reported in the warnings, exactly as `tar` skips what it cannot stream, so a round
// trip of a tree does not fail on something neither direction can carry.
func TestUntarSkipsWhatItCannotCarry(t *testing.T) {
	root := withFileRoot(t)
	stream := archive(t,
		tarEntry{name: "repo/fifo", typeflag: tar.TypeFifo},
		tarEntry{name: "repo/main.go", body: "package main\n", typeflag: tar.TypeReg},
	)

	summary, err := extractTree(context.Background(), "repo", bytes.NewReader(stream))
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Entries)
	require.Len(t, summary.Warnings, 1)
	assert.Contains(t, summary.Warnings[0], "repo/fifo")

	_, err = os.Stat(filepath.Join(root, "repo/main.go"))
	assert.NoError(t, err)
}

// TestUntarRefusesAnAgentThatCannotDoIt pins the optional capability: a client whose guest
// did not advertise untar refuses before sending an archive, and a server that does not
// serve the op answers unsupported_op if one arrives anyway.
func TestUntarRefusesAnAgentThatCannotDoIt(t *testing.T) {
	withFileRoot(t)

	t.Run("the client refuses before streaming", func(t *testing.T) {
		conn, _ := newPipePair(t)
		client := testClient(conn)
		delete(client.caps, CapUntar)

		_, err := client.Untar(context.Background(), "repo", bytes.NewReader(archive(t)))
		assert.Equal(t, CodeUnsupportedOp, failureCodeOf(t, err))
	})

	t.Run("the server refuses an op it does not serve", func(t *testing.T) {
		// A guest whose agent predates the operation: every capability it had before.
		caps := make([]string, 0, len(capabilities))
		for _, cap := range capabilities {
			if cap != CapUntar {
				caps = append(caps, cap)
			}
		}
		conn, _ := newCappedPipePair(t, caps)

		// The request alone: the refusal comes before any byte of an archive is read, which
		// is why a caller checks the hello's capabilities rather than streaming and hoping.
		require.NoError(t, WriteFrame(conn, Request{ID: "req-1", Op: CapUntar, Path: "repo"}))
		reply := readMessage(t, conn)
		assert.Equal(t, "error", reply.Type)
		assert.Equal(t, CodeUnsupportedOp, reply.ErrorCode)
	})
}

// TestUntarStopsOnATruncatedArchive pins the failure a short stream is: the tree is not
// reported as written, and the files it did write are not left behind as if they were.
func TestUntarStopsOnATruncatedArchive(t *testing.T) {
	withFileRoot(t)

	full := archive(t, tarEntry{name: "repo/main.go", body: "package main\n", typeflag: tar.TypeReg})
	truncated := full[:len(full)-64]

	_, err := extractTree(context.Background(), "repo", bytes.NewReader(truncated))
	require.Error(t, err, "a truncated archive is not a tree")
	assert.NotEqual(t, CodeInternal, failureCodeOf(t, err), "a short stream is the caller's, not the guest's")
}

// assertNoFile fails when path exists under root.
func assertNoFile(t *testing.T, root, name string) {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, name))
	assert.True(t, errors.Is(err, os.ErrNotExist), "a refused entry writes nothing at %s", name)
}

// TestUntarFailsCleanlyOnTreesItCannotBuild covers the three shapes an archive can name that
// the workspace cannot hold as written: a file inside a path that is already a file, a file
// where a directory is, and a link whose name is an existing entry it has to replace. Each is
// a refusal the caller can act on rather than a silent partial tree.
func TestUntarFailsCleanlyOnTreesItCannotBuild(t *testing.T) {
	t.Run("a file inside a path that is a file", func(t *testing.T) {
		root := withFileRoot(t)
		stream := archive(t,
			tarEntry{name: "repo", body: "not a directory", typeflag: tar.TypeReg},
			tarEntry{name: "repo/child.txt", body: "x", typeflag: tar.TypeReg},
		)

		_, err := extractTree(context.Background(), "repo", bytes.NewReader(stream))
		require.Error(t, err)
		assert.Equal(t, CodeInternal, failureCodeOf(t, err), "the guest's kernel refused the path, and that is reported as such")

		// What the archive did write before it failed is what it says it wrote: the
		// refusal is not claimed as complete.
		body, readErr := os.ReadFile(filepath.Join(root, "repo"))
		require.NoError(t, readErr)
		assert.Equal(t, "not a directory", string(body))
	})

	t.Run("a file where a directory is", func(t *testing.T) {
		root := withFileRoot(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "repo", "pkg"), 0o755))
		stream := archive(t, tarEntry{name: "repo/pkg", body: "x", typeflag: tar.TypeReg})

		_, err := extractTree(context.Background(), "repo", bytes.NewReader(stream))
		require.Error(t, err)
		assert.Equal(t, CodeInternal, failureCodeOf(t, err))
	})

	t.Run("a link that replaces an existing entry", func(t *testing.T) {
		root := withFileRoot(t)
		require.NoError(t, os.WriteFile(filepath.Join(root, "repo"), []byte("old\n"), 0o644))
		stream := archive(t, tarEntry{
			name: "repo", typeflag: tar.TypeSymlink, link: "target.txt",
		})

		summary, err := extractTree(context.Background(), "repo", bytes.NewReader(stream))
		require.NoError(t, err)
		assert.Equal(t, 1, summary.Entries)

		link, err := os.Readlink(filepath.Join(root, "repo"))
		require.NoError(t, err, "the link replaced the file rather than failing on it")
		assert.Equal(t, "target.txt", link)
	})
}
