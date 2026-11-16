package agent

// Taking a tree back into the workspace: `untar`.
//
// `tar` is how this protocol hands a tree to a caller; `untar` is how a caller hands one
// back. It is the tenth operation, and it is advertised as an optional capability rather
// than added to the set a template must have: a template whose agent predates it keeps
// working for everything it did before — a single file still arrives through `put` — and
// the only thing it cannot do is have a directory written into it, which it says with
// `unsupported_op` and the caller reports as the missing capability it is.
//
// # What it writes, and what it refuses
//
// Everything it writes is inside the file root: a name is cleaned with the same rule
// `stat`, `list` and `tar` use (`confine`), and the file is then opened through
// `os.OpenRoot`'s root, so a path that survived cleaning but escaped anyway — through a
// race, or a link planted between two entries — is refused by the kernel's own openat
// walk rather than by a string check.
//
// An archive is untrusted input even when the caller is trusted, because it can carry
// entries that mean "write somewhere else":
//
//   - an absolute name, or one whose cleaning leaves the root (`../x`), is refused;
//   - a symbolic link is written only when its target is relative and stays inside the
//     root, because a link that points out of the workspace is a way to have the next
//     entry write through it;
//   - a hard link is written only when it names a regular file this archive has already
//     extracted, for the same reason;
//   - a device, a fifo or a socket is not extracted at all: it is skipped and reported in
//     the summary's warnings, which is what `tar` does with the entries it cannot carry,
//     so a round trip of a tree reports the same entries in both directions;
//   - the bytes written are bounded per entry and in total, so a hostile archive cannot
//     fill the guest's disk through one request.

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

const (
	// maxUntarEntries bounds how many entries one archive may carry. It is the same
	// bound the outgoing direction applies, so a tree this node streams out is one it
	// takes back.
	maxUntarEntries = maxTarEntries
)

// The size bounds are variables so a test can lower them and reach the refusal without
// writing gigabytes; production uses the values here.
var (
	// maxUntarEntryBytes bounds one regular file this operation writes. A single entry
	// larger than this is not a file a workspace copy is meant to carry; the caller
	// splits it or an operator changes the bound, and neither happens by accident.
	maxUntarEntryBytes int64 = 4 << 30
	// maxUntarBytes bounds the sum of the regular files one archive writes. It is
	// deliberately larger than the two-gigabyte workspace the acceptance scenario
	// copies — the bound is a guard against a request that would fill the guest's disk,
	// not a statement about the size of a workspace.
	maxUntarBytes int64 = 16 << 30
)

// extractTree writes the tar archive r reads into the file root, and reports what it
// wrote. It is the whole of `untar` apart from the framing, which is server.go's.
//
// The returned summary counts what was *written*: entries are counted after they are
// created, and a skipped entry is counted in Warnings instead — the same shape `tar`
// reports, so a caller comparing the two directions sees the same tree.
func extractTree(ctx context.Context, prefix string, r io.Reader) (TarSummary, error) {
	var summary TarSummary

	// The tree the caller asked to write. Every entry is checked against it, so a
	// request that named one directory cannot write another: the confinement below keeps
	// the archive in the workspace, and this keeps it in the tree the caller named.
	target, err := confine(prefix)
	if err != nil {
		return summary, err
	}
	if target == "." {
		target = ""
	}

	root, err := openFileRoot()
	if err != nil {
		return TarSummary{}, err
	}
	defer func() { _ = root.Close() }()

	// tree is the state one archive writes through: the summaries and the set of regular
	// files this archive has created, which is what a hard link is checked against.
	tree := &tree{root: root, summary: &summary, extracted: map[string]bool{}}

	reader := tar.NewReader(r)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return summary, fail(CodeInvalidRequest, "untar: read the archive: %v", err)
		}
		if summary.Entries >= maxUntarEntries {
			return summary, fail(CodeTooLarge, "untar: the archive reached its %d-entry bound", maxUntarEntries)
		}

		if err := ctx.Err(); err != nil {
			return summary, fail(CodeDeadlineExceeded, "untar: deadline exceeded after %d entries", summary.Entries)
		}

		// The name is confined before anything is created: a header that would write
		// outside the root is refused, not skipped, because a caller that sent one is
		// either confused about the workspace or trying something.
		rel, err := confine(header.Name)
		if err != nil {
			return summary, err
		}
		if target != "" && rel != target && !strings.HasPrefix(rel, target+"/") {
			return summary, fail(CodeUnsafePath,
				"untar: %s is outside the tree this request asked for (%s)", rel, target)
		}

		if err := tree.entry(rel, header, reader); err != nil {
			return summary, err
		}
	}

	// The list is bounded like the outgoing one's; the last warning carries the exact
	// number of entries the archive did not write, so a caller is never left with a list
	// that looks complete.
	if tree.skipped > len(summary.Warnings) {
		summary.Warnings = append(summary.Warnings, fmt.Sprintf("and %d more entries this archive cannot carry", tree.skipped-len(summary.Warnings)))
	}
	// What the workspace grew by, reported the way `tar` reports the archive it streamed:
	// each direction answers with what it moved.
	summary.Bytes = tree.written
	return summary, nil
}

// tree is one archive being written into the file root: what it has created, and what it
// reports. The entry types are separate methods rather than one long switch so each rule —
// a directory's mode, a file's two bounds, a link's target — is readable on its own.
type tree struct {
	root *os.Root
	// summary is the caller's, filled in as entries land.
	summary *TarSummary
	// extracted is every regular file this archive has written, by cleaned name: a hard
	// link may only name one of those, never something the workspace already had.
	extracted map[string]bool
	// written is the sum of the file bytes created, and skipped the entries the root
	// cannot hold.
	written int64
	skipped int
}

// entry writes one archive entry.
func (t *tree) entry(rel string, header *tar.Header, reader io.Reader) error {
	switch header.Typeflag {
	case tar.TypeDir:
		return t.directory(rel, header)
	case tar.TypeReg:
		return t.regular(rel, header, reader)
	case tar.TypeSymlink:
		return t.symlink(rel, header)
	case tar.TypeLink:
		return t.hardLink(rel, header)
	default:
		t.skip(rel, header)
		return nil
	}
}

// directory creates one directory entry.
func (t *tree) directory(rel string, header *tar.Header) error {
	if err := mkdirAllIn(t.root, rel); err != nil {
		return fail(CodeInternal, "untar: create %s: %v", rel, err)
	}
	// A directory the workspace already had keeps its own mode until the archive says
	// otherwise: the tree an archive describes is the tree it leaves.
	if err := t.root.Chmod(rel, fileModeOf(header)); err != nil {
		return fail(CodeInternal, "untar: chmod %s: %v", rel, err)
	}
	t.summary.Entries++
	return nil
}

// regular creates one regular file entry, within the two size bounds.
func (t *tree) regular(rel string, header *tar.Header, reader io.Reader) error {
	if header.Size < 0 || header.Size > maxUntarEntryBytes {
		return fail(CodeTooLarge,
			"untar: %s is %d bytes, over the %d-byte bound for one entry", rel, header.Size, int64(maxUntarEntryBytes))
	}
	if t.written+header.Size > maxUntarBytes {
		return fail(CodeTooLarge,
			"untar: the archive reached its %d-byte bound at %s", int64(maxUntarBytes), rel)
	}
	// The root creates the absence of a parent directory as an error, so the parents are
	// made first: an archive does not have to carry a directory entry before the files
	// inside it.
	if dir := path.Dir(rel); dir != "." && dir != "" {
		if err := mkdirAllIn(t.root, dir); err != nil {
			return fail(CodeInternal, "untar: create %s: %v", dir, err)
		}
	}
	n, err := writeFileIn(t.root, rel, reader, fileModeOf(header))
	if err != nil {
		return fail(CodeInternal, "untar: write %s: %v", rel, err)
	}
	t.written += n
	t.extracted[rel] = true
	t.summary.Entries++
	return nil
}

// symlink creates one symbolic link entry, only when its target stays inside the root.
func (t *tree) symlink(rel string, header *tar.Header) error {
	if !linkStaysInside(rel, header.Linkname) {
		return fail(CodeUnsafePath,
			"untar: %s is a symbolic link to %q, which leaves the file root", rel, header.Linkname)
	}
	if err := replaceWithLink(t.root, rel, func(name string) error {
		return t.root.Symlink(header.Linkname, name)
	}); err != nil {
		return fail(CodeInternal, "untar: link %s: %v", rel, err)
	}
	t.summary.Entries++
	return nil
}

// hardLink creates one hard link entry, only to a regular file this same archive wrote.
func (t *tree) hardLink(rel string, header *tar.Header) error {
	target, err := confine(header.Linkname)
	if err != nil {
		return err
	}
	if !t.extracted[target] {
		return fail(CodeUnsafePath,
			"untar: %s is a hard link to %q, which this archive did not write", rel, header.Linkname)
	}
	if err := replaceWithLink(t.root, rel, func(name string) error {
		return t.root.Link(target, name)
	}); err != nil {
		return fail(CodeInternal, "untar: link %s: %v", rel, err)
	}
	t.summary.Entries++
	return nil
}

// skip reports an entry this root cannot hold: a device, a fifo, a socket. Nothing a
// workspace copy needs, and nothing the root may create — reported, exactly as `tar`
// reports what it cannot carry.
func (t *tree) skip(rel string, header *tar.Header) {
	t.skipped++
	if len(t.summary.Warnings) < maxTarWarnings-1 {
		t.summary.Warnings = append(t.summary.Warnings, skipWarning(rel, header.Typeflag))
	}
}

// fileModeOf is the permission bits of one entry, with the directory bit applied to a
// directory and nothing else. Ownership is not applied: a workspace belongs to the
// guest's own user, and an archive that claimed to own a file does not make it so.
func fileModeOf(header *tar.Header) os.FileMode {
	// Only the twelve permission bits are wanted, and masking them first is what bounds
	// the conversion: an archive may carry anything in this field.
	mode := os.FileMode(header.Mode & 0o7777).Perm() //nolint:gosec // G115: masked to twelve bits above
	if header.Typeflag == tar.TypeDir {
		return mode | fs.ModeDir
	}
	return mode
}

// linkStaysInside reports whether a symbolic link at rel pointing at target is still
// inside the file root. The link is not followed — nothing here resolves a path — so the
// question is only what the link would mean to a reader: a relative target that, joined
// with the link's directory, does not climb out.
func linkStaysInside(rel, target string) bool {
	if target == "" || path.IsAbs(target) || strings.ContainsRune(target, 0) {
		return false
	}
	joined := path.Clean(path.Join(path.Dir(rel), target))
	return joined != ".." && !strings.HasPrefix(joined, "../")
}

// mkdirAllIn creates dir and every missing parent inside the root, ignoring the ones that
// already exist. It walks the components rather than calling MkdirAll because the root is
// what refuses a path that leaves it: every component goes through the same openat walk.
func mkdirAllIn(root *os.Root, dir string) error {
	clean, err := confine(dir)
	if err != nil {
		return err
	}
	if clean == "." {
		return nil
	}
	at := ""
	for _, part := range strings.Split(clean, "/") {
		at = path.Join(at, part)
		if err := root.Mkdir(at, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return nil
}

// writeFileIn writes one regular file inside the root and returns how many bytes it took.
// An entry that already exists is replaced, which is what extracting the same archive
// twice means.
func writeFileIn(root *os.Root, name string, r io.Reader, mode os.FileMode) (int64, error) {
	if mode == 0 {
		mode = 0o644
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	// A reader that ends early (a truncated archive) must not be reported as success, so
	// the copy's error is the answer and a short file is removed rather than left.
	n, err := io.Copy(file, r)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = root.Remove(name)
		return n, err
	}
	// The file is opened with the mode, but the mode of an existing file is not changed by
	// the open: an archive that replaces a file sets what it says the file is.
	if err := root.Chmod(name, mode); err != nil {
		return n, err
	}
	return n, nil
}

// replaceWithLink creates one link entry, removing whatever the workspace had at that
// name first: a link cannot replace a directory, and an archive that renames a file to a
// link means the file is gone.
func replaceWithLink(root *os.Root, name string, create func(string) error) error {
	if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if dir := path.Dir(name); dir != "." && dir != "" {
		if err := mkdirAllIn(root, dir); err != nil {
			return err
		}
	}
	return create(name)
}

// skipWarning is one entry this operation did not write, in the same voice the outgoing
// direction uses for the entries it cannot carry.
func skipWarning(rel string, kind byte) string {
	return fmt.Sprintf("%s: an entry of type %q is not extracted", rel, rune(kind))
}
