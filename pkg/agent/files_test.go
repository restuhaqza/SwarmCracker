//go:build !integration

package agent

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bufferSink adapts a buffer to the sink a tar stream writes into.
func bufferSink(buf *bytes.Buffer) tarSink {
	return func(p []byte) error {
		_, err := buf.Write(p)
		return err
	}
}

// withFileRoot points the file operations at a fresh directory, which stands in
// for the sandbox's /workspace.
func withFileRoot(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	original := workspaceRoot
	workspaceRoot = dir
	t.Cleanup(func() { workspaceRoot = original })
	return dir
}

// writeTree writes files (path → content) under root, creating directories.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()

	for name, body := range files {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o640))
	}
}

// TestStatReportsTheEntryAsItIs covers the fields a copy needs, and that a link is
// described as a link with its target rather than resolved.
func TestStatReportsTheEntryAsItIs(t *testing.T) {
	root := withFileRoot(t)
	writeTree(t, root, map[string]string{"repo/main.go": "package main\n"})
	require.NoError(t, os.Symlink("repo/main.go", filepath.Join(root, "link")))
	require.NoError(t, os.Mkdir(filepath.Join(root, "dir"), 0o750))

	entry, err := statGuestFile("repo/main.go")
	require.NoError(t, err)
	assert.Equal(t, "main.go", entry.Name)
	assert.Equal(t, "file", entry.Type)
	assert.Equal(t, int64(len("package main\n")), entry.Size)
	assert.Equal(t, "-rw-r-----", entry.Mode)
	assert.NotZero(t, entry.ModTime)
	assert.Empty(t, entry.Target)

	dir, err := statGuestFile("dir")
	require.NoError(t, err)
	assert.Equal(t, "dir", dir.Name)
	assert.Equal(t, "dir", dir.Type)
	assert.Equal(t, "drwxr-x---", dir.Mode)

	// The link is reported as a link. Its target is read, and the file behind it is
	// not: `stat` here is the entry's own description, and a caller that wants the
	// target's decides to follow it.
	link, err := statGuestFile("link")
	require.NoError(t, err)
	assert.Equal(t, "symlink", link.Type)
	assert.Equal(t, "repo/main.go", link.Target)
	assert.NotEqual(t, int64(len("package main\n")), link.Size)

	// The root itself is the empty path, and is a directory.
	top, err := statGuestFile("")
	require.NoError(t, err)
	assert.Equal(t, "dir", top.Type)
}

// TestListIsSortedAndDescribesEachEntry is the listing's contract: one directory's
// own entries, in name order, each with the type and link target a copy branches
// on.
func TestListIsSortedAndDescribesEachEntry(t *testing.T) {
	root := withFileRoot(t)
	// Written in an order that is not the sorted one, so a listing that returns
	// the filesystem's own order fails here.
	writeTree(t, root, map[string]string{
		"zeta.txt":   "z",
		"alpha.txt":  "a",
		"middle.txt": "m",
	})
	require.NoError(t, os.Mkdir(filepath.Join(root, "nested"), 0o755))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(root, "escape")))

	listing, err := listGuestDir("")
	require.NoError(t, err)
	assert.False(t, listing.Truncated)

	names := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		names = append(names, entry.Name)
	}
	assert.Equal(t, []string{"alpha.txt", "escape", "middle.txt", "nested", "zeta.txt"}, names)

	byName := make(map[string]FileEntry, len(listing.Entries))
	for _, entry := range listing.Entries {
		byName[entry.Name] = entry
	}
	assert.Equal(t, "file", byName["alpha.txt"].Type)
	assert.Equal(t, int64(1), byName["alpha.txt"].Size)
	assert.Equal(t, "dir", byName["nested"].Type)
	// A link that points outside the root is still described as a link: reading
	// the target's content is what the rule forbids, not naming the target.
	assert.Equal(t, "symlink", byName["escape"].Type)
	assert.Equal(t, "/etc/passwd", byName["escape"].Target)
}

// TestListTruncatesAtItsBound proves the bound is reported rather than applied
// silently: a directory one entry over the limit comes back as the first
// maxListEntries names and says it was cut short.
func TestListTruncatesAtItsBound(t *testing.T) {
	if testing.Short() {
		t.Skip("creating more entries than the listing bound takes a moment")
	}
	root := withFileRoot(t)

	names := make([]string, 0, maxListEntries+1)
	for i := 0; i <= maxListEntries; i++ {
		names = append(names, "f"+leftPad(i))
	}
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), nil, 0o644))
	}
	sort.Strings(names)

	listing, err := listGuestDir("")
	require.NoError(t, err)
	assert.True(t, listing.Truncated)
	require.Len(t, listing.Entries, maxListEntries)
	assert.Equal(t, names[0], listing.Entries[0].Name)
	assert.Equal(t, names[maxListEntries-1], listing.Entries[maxListEntries-1].Name)
}

// leftPad renders i as six digits, so the names sort the way they were written.
func leftPad(i int) string {
	const digits = 6
	out := []byte("000000")
	for pos := digits - 1; pos >= 0 && i > 0; pos-- {
		out[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(out)
}

// TestFileOpsRefusePathsOutsideTheRoot is the boundary itself: a traversal, an
// absolute path and a walk through a link that leaves the root are all refused,
// each with a code a caller branches on and a message that says which rule it
// broke.
func TestFileOpsRefusePathsOutsideTheRoot(t *testing.T) {
	root := withFileRoot(t)
	writeTree(t, root, map[string]string{"inside.txt": "in"})
	require.NoError(t, os.Symlink("/etc", filepath.Join(root, "etclink")))

	cases := []struct {
		name string
		path string
		// rule is what the message must name, so an operator reading a failure
		// learns what to change rather than only that something was refused.
		rule string
	}{
		{name: "parent traversal", path: "../../etc/passwd", rule: "may not leave it"},
		{name: "traversal that cleans to a parent", path: "sub/../../etc/passwd", rule: "may not leave it"},
		{name: "absolute path", path: "/etc/passwd", rule: "may not leave it"},
		{name: "link that leaves the root", path: "etclink/hostname", rule: "escapes"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, statErr := statGuestFile(tc.path)
			require.Error(t, statErr)
			code, message := failureCode(statErr)
			assert.Equal(t, CodeUnsafePath, code)
			assert.Contains(t, message, tc.rule)

			// The listing and the archive refuse it too: the boundary is one rule,
			// not a check on one operation.
			_, listErr := listGuestDir(tc.path)
			require.Error(t, listErr)
			code, _ = failureCode(listErr)
			assert.Equal(t, CodeUnsafePath, code)

			_, tarErr := streamGuestTar(context.Background(), tc.path, func([]byte) error { return nil })
			require.Error(t, tarErr)
			code, _ = failureCode(tarErr)
			assert.Equal(t, CodeUnsafePath, code)
		})
	}

	// A path that cleans to somewhere inside the root is not a traversal: "a/../b"
	// is "b", and refusing it would refuse a path the guest can serve.
	writeTree(t, root, map[string]string{"b": "bee"})
	entry, err := statGuestFile("sub/../b")
	require.NoError(t, err)
	assert.Equal(t, "b", entry.Name)
}

// TestFileOpsReportAMissingPath distinguishes "not there" from "not allowed": they
// are different answers to a caller, and only one of them is the caller's fault.
func TestFileOpsReportAMissingPath(t *testing.T) {
	withFileRoot(t)

	_, err := statGuestFile("absent")
	require.Error(t, err)
	code, _ := failureCode(err)
	assert.Equal(t, CodeNotFound, code)
}

// TestFileOpsReportAMissingRoot covers a template with no workspace at all.
func TestFileOpsReportAMissingRoot(t *testing.T) {
	original := workspaceRoot
	workspaceRoot = filepath.Join(t.TempDir(), "no-workspace")
	t.Cleanup(func() { workspaceRoot = original })

	_, err := listGuestDir("")
	require.Error(t, err)
	code, _ := failureCode(err)
	assert.Equal(t, CodeNotFound, code)
}

// TestStreamGuestTarArchivesTheTreeItWasAskedFor is the round trip: a tree with
// nested directories, an empty file, an executable and a link streams as an
// archive whose names, contents, modes and link targets are the tree's own.
func TestStreamGuestTarArchivesTheTreeItWasAskedFor(t *testing.T) {
	root := withFileRoot(t)
	writeTree(t, root, map[string]string{
		"repo/README.md":    "# repo\n",
		"repo/empty":        "",
		"repo/sub/deep.txt": "deep\n",
		"repo/sub/run.sh":   "#!/bin/sh\nexit 0\n",
		// A sibling of the requested path: it is inside the root and outside the
		// archive, which is the difference the naming rule has to keep.
		"sibling.txt": "not archived",
	})
	require.NoError(t, os.Chmod(filepath.Join(root, "repo/sub/run.sh"), 0o750))
	require.NoError(t, os.Symlink("README.md", filepath.Join(root, "repo/link.md")))

	var archive bytes.Buffer
	summary, err := streamGuestTar(context.Background(), "repo", bufferSink(&archive))
	require.NoError(t, err)

	assert.Equal(t, 7, summary.Entries, "repo, README.md, empty, link.md, sub, deep.txt, run.sh")
	assert.Equal(t, int64(archive.Len()), summary.Bytes)
	assert.Empty(t, summary.Warnings)

	entries := readArchive(t, archive.Bytes())
	require.Len(t, entries, 7)
	// Directory entries carry the trailing slash tar writes them with.
	assert.Equal(t, []string{
		"repo/", "repo/README.md", "repo/empty", "repo/link.md",
		"repo/sub/", "repo/sub/deep.txt", "repo/sub/run.sh",
	}, sortedNames(entries))

	assert.Equal(t, "# repo\n", entries["repo/README.md"].body)
	assert.Equal(t, "", entries["repo/empty"].body)
	assert.Equal(t, "deep\n", entries["repo/sub/deep.txt"].body)
	assert.Equal(t, "640", entries["repo/sub/deep.txt"].mode)
	assert.Equal(t, "750", entries["repo/sub/run.sh"].mode)
	assert.Equal(t, "link", entries["repo/link.md"].typeflag)
	assert.Equal(t, "README.md", entries["repo/link.md"].linkname)
}

// TestStreamGuestTarOfTheRootHasNoTopLevelEntry covers the other half of the
// naming rule: an archive of the root itself holds the root's children, so
// extracting it fills a directory instead of replacing one.
func TestStreamGuestTarOfTheRootHasNoTopLevelEntry(t *testing.T) {
	root := withFileRoot(t)
	writeTree(t, root, map[string]string{"top.txt": "top", "nested/inner.txt": "inner"})

	var archive bytes.Buffer
	summary, err := streamGuestTar(context.Background(), "", bufferSink(&archive))
	require.NoError(t, err)
	assert.Equal(t, 3, summary.Entries)

	entries := readArchive(t, archive.Bytes())
	assert.Equal(t, []string{"nested/", "nested/inner.txt", "top.txt"}, sortedNames(entries))
}

// TestStreamGuestTarStopsAtItsByteBound proves the byte bound stops the stream
// rather than the walk: the counter refuses a chunk that would cross it, and the
// stream fails with the code that says so.
func TestStreamGuestTarStopsAtItsByteBound(t *testing.T) {
	counter := &tarCounter{
		ctx:     context.Background(),
		sink:    func([]byte) error { return nil },
		written: maxTarBytes - 10,
	}
	_, err := counter.Write(make([]byte, 11))
	require.Error(t, err)
	code, _ := failureCode(err)
	assert.Equal(t, CodeTooLarge, code)

	// One byte fewer fits exactly: the bound is the size the archive may reach.
	n, err := counter.Write(make([]byte, 10))
	require.NoError(t, err)
	assert.Equal(t, 10, n)
	assert.Equal(t, int64(maxTarBytes), counter.written)
}

// TestStreamGuestTarStopsAtItsEntryBound is the entry bound's own boundary: the
// entry that would take the archive over its limit is refused, and the one that
// lands exactly on it is not.
func TestStreamGuestTarStopsAtItsEntryBound(t *testing.T) {
	root := withFileRoot(t)
	writeTree(t, root, map[string]string{"one": "1"})

	st := &tarStreamer{
		ctx:     context.Background(),
		out:     &tarCounter{ctx: context.Background(), sink: func([]byte) error { return nil }},
		summary: TarSummary{Entries: maxTarEntries},
	}
	st.buf = bufio.NewWriterSize(st.out, fileChunkSize)
	st.tw = tar.NewWriter(st.buf)

	err := st.writeEntry("one", mustLstat(t, filepath.Join(root, "one")), "")
	require.Error(t, err)
	code, _ := failureCode(err)
	assert.Equal(t, CodeTooLarge, code)
}

// TestStreamGuestTarStopsCleanlyWhenItsSinkFails is the clean stop: a sink that
// refuses a chunk ends the stream with the sink's own error, and what it already
// received is a fragment that no reader can mistake for a complete archive.
func TestStreamGuestTarStopsCleanlyWhenItsSinkFails(t *testing.T) {
	root := withFileRoot(t)
	// Enough content to span several chunks, so the failure lands mid-stream.
	writeTree(t, root, map[string]string{"big": strings.Repeat("x", 4*fileChunkSize)})

	full := errors.New("the caller's disk is full")
	written := 0
	sink := func(p []byte) error {
		written += len(p)
		if written > fileChunkSize {
			return full
		}
		return nil
	}

	_, err := streamGuestTar(context.Background(), "", sink)
	require.Error(t, err)
	assert.ErrorIs(t, err, full, "the caller's own failure is what it reads, not a rewritten one")

	// What reached the sink is a fragment, and a fragment is visibly one: it is
	// shorter than the same archive streamed whole, and the entries a reader can
	// get out of it are a strict subset of that archive's.
	var whole bytes.Buffer
	_, err = streamGuestTar(context.Background(), "", bufferSink(&whole))
	require.NoError(t, err)
	assert.Less(t, written, whole.Len())

	complete := readArchive(t, whole.Bytes())
	partial := readArchivePrefix(t, whole.Bytes()[:written])
	for name := range partial {
		assert.Contains(t, complete, name, "a fragment must not invent an entry")
	}
	assert.Less(t, len(partial), len(complete), "the fragment is missing entries a complete archive has")
}

// TestStreamGuestTarStopsAtADeadline covers the other clean stop: an archive of a
// tree that would take longer than the caller's budget stops when the budget does.
func TestStreamGuestTarStopsAtADeadline(t *testing.T) {
	root := withFileRoot(t)
	writeTree(t, root, map[string]string{"many/a": strings.Repeat("y", fileChunkSize)})

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	_, err := streamGuestTar(ctx, "many", func([]byte) error { return nil })
	require.Error(t, err)
	code, _ := failureCode(err)
	assert.Equal(t, CodeDeadlineExceeded, code)
}

// archiveEntry is one entry read back from an archive.
type archiveEntry struct {
	body     string
	mode     string
	typeflag string
	linkname string
}

// readArchive reads an archive back into the entries it holds, keyed by name.
func readArchive(t *testing.T, data []byte) map[string]archiveEntry {
	t.Helper()

	out := make(map[string]archiveEntry)
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err)
		body, err := io.ReadAll(reader)
		require.NoError(t, err)
		out[hdr.Name] = archiveEntry{
			body:     string(body),
			mode:     fmt.Sprintf("%o", hdr.Mode&0o7777),
			typeflag: typeName(hdr.Typeflag),
			linkname: hdr.Linkname,
		}
	}
}

// readArchivePrefix reads what a cut archive still holds: every entry whose body
// was complete before the cut. It is what a caller would salvage from a stream
// that ended early, and it is compared against the whole archive's entries.
func readArchivePrefix(t *testing.T, data []byte) map[string]archiveEntry {
	t.Helper()

	out := make(map[string]archiveEntry)
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := reader.Next()
		if err != nil {
			return out
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			// The entry's header was in the fragment and its body was not, so the
			// entry is not salvageable and is not reported.
			return out
		}
		out[hdr.Name] = archiveEntry{body: string(body), mode: fmt.Sprintf("%o", hdr.Mode&0o7777), typeflag: typeName(hdr.Typeflag), linkname: hdr.Linkname}
	}
}

// typeName names a header's type the way the test's expectations read.
func typeName(flag byte) string {
	switch flag {
	case tar.TypeLink:
		return "hardlink"
	case tar.TypeSymlink:
		return "link"
	case tar.TypeDir:
		return "dir"
	case tar.TypeReg:
		return "file"
	default:
		return string(flag)
	}
}

// sortedNames returns the entry names of an archive in sorted order.
func sortedNames(entries map[string]archiveEntry) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// mustOpenFileRoot opens the file root or fails the test.
func mustOpenFileRoot(t *testing.T) *os.Root {
	t.Helper()

	root, err := openFileRoot()
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// mustLstat is os.Lstat for a test's fixture path.
func mustLstat(t *testing.T, path string) os.FileInfo {
	t.Helper()

	info, err := os.Lstat(path)
	require.NoError(t, err)
	return info
}

// TestConfineCleansAndRefuses pins the lexical rule on its own, including the two
// spellings of "the root".
func TestConfineCleansAndRefuses(t *testing.T) {
	withFileRoot(t)

	for _, tc := range []struct{ in, want string }{
		{"", "."},
		{".", "."},
		{"a", "a"},
		{"./a", "a"},
		{"a/./b", "a/b"},
		{"a/../b", "b"},
		{"a/b/", "a/b"},
	} {
		got, err := confine(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}

	for _, in := range []string{"..", "../x", "a/../../x", "/x", "/", "\x00"} {
		_, err := confine(in)
		require.Error(t, err, in)
		code, _ := failureCode(err)
		assert.Equal(t, CodeUnsafePath, code, in)
	}
}

// TestEntryWireSizeBoundsAFrame is what keeps a listing batch inside a control
// frame: the estimate must never be smaller than the entry's own encoding.
func TestEntryWireSizeBoundsAFrame(t *testing.T) {
	entry := FileEntry{
		Name:    strings.Repeat("n", 255),
		Mode:    "-rw-r--r--",
		Target:  "/" + strings.Repeat("t", 4095),
		ModTime: time.Now(),
	}
	// The entry above is the largest one a Linux filesystem can hold: a 255-byte
	// NAME_MAX name and a PATH_MAX-1 link target. A batch holds entries until the
	// next one would cross listBatchBytes, so a batch frame is that budget plus at
	// most one entry, and one entry always fits on its own.
	encoded, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, entryWireSize(entry), len(encoded))
	assert.Less(t, entryWireSize(entry), MaxFrameSize, "a single entry must fit a control frame")
	assert.Less(t, listBatchBytes+entryWireSize(entry), MaxFrameSize,
		"a batch frame is its byte budget plus at most the entry that crossed it")
}

// TestModeStringMatchesLs is what makes the report comparable with the tools a
// caller already has: the mode of every kind of entry, including the special bits
// ls writes in place of the execute letter.
func TestModeStringMatchesLs(t *testing.T) {
	dir := withFileRoot(t)
	for _, tc := range []struct {
		name string
		mode os.FileMode
		want string
	}{
		{"file.go", 0o644, "-rw-r--r--"},
		{"exec", 0o755, "-rwxr-xr-x"},
		{"private", 0o600, "-rw-------"},
		{"setuid", os.ModeSetuid | 0o755, "-rwsr-xr-x"},
		{"setuid-no-exec", os.ModeSetuid | 0o644, "-rwSr--r--"},
		{"setgid", os.ModeSetgid | 0o2755, "-rwxr-sr-x"},
		{"sticky", os.ModeSticky | 0o1777, "-rwxrwxrwt"},
		{"sticky-no-exec", os.ModeSticky | 0o666, "-rw-rw-rwT"},
	} {
		path := filepath.Join(dir, tc.name)
		require.NoError(t, os.WriteFile(path, nil, tc.mode.Perm()))
		if tc.mode&os.ModeSetuid != 0 {
			require.NoError(t, os.Chmod(path, tc.mode.Perm()|os.ModeSetuid))
		}
		if tc.mode&os.ModeSetgid != 0 {
			require.NoError(t, os.Chmod(path, tc.mode.Perm()|os.ModeSetgid))
		}
		if tc.mode&os.ModeSticky != 0 {
			require.NoError(t, os.Chmod(path, tc.mode.Perm()|os.ModeSticky))
		}
		info, err := os.Lstat(path)
		require.NoError(t, err)
		assert.Equal(t, tc.want, modeString(info.Mode()), tc.name)
	}

	// The three kinds of entry whose type character differs from a file's.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "adir"), 0o750))
	require.NoError(t, os.Symlink("file.go", filepath.Join(dir, "alink")))
	for name, want := range map[string]string{"adir": "drwxr-x---", "alink": "lrwxrwxrwx"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, want, modeString(info.Mode()), name)
	}
}

// TestFileEntryNamesTheRootAsItsOwnEntry covers path.Base's answer for the root,
// which a caller listing "" sees in `stat`.
func TestFileEntryNamesTheRootAsItsOwnEntry(t *testing.T) {
	withFileRoot(t)

	entry, err := statGuestFile(".")
	require.NoError(t, err)
	assert.Equal(t, path.Base("."), entry.Name)
}
