package agent

// The file operations: `stat`, `list` and `tar` (RFC D9, SBX-086).
//
// These three exist because a minimal template ships neither coreutils nor tar,
// so "what is in this sandbox" and "give me this workspace" cannot be answered
// over `exec` without depending on the very tools that may be missing. They also
// exist because `sandbox cp` has to move a tree in and out, and a tree does not
// fit in a frame.
//
// # Confinement
//
// Every path a caller sends is resolved inside one directory — the file root,
// /workspace, the mount point every sandbox template gives its workspace — and
// nothing outside it is reachable, ever:
//
//   - a path is relative to the root, and a lexical pass refuses an absolute path
//     or a ".." component that leaves the root before the root is even opened;
//   - the resolution itself is os.Root's, which refuses any path that leaves the
//     root by any route — a symlink whose target is outside, a ".." that only
//     escapes after a link is followed — so the check above is the explanation and
//     os.Root is the enforcement;
//   - a symbolic link inside the root is followed while it stays inside, so a
//     workspace that links to another part of itself works, and a link pointing at
//     /etc is refused rather than read;
//   - the walk `tar` does never follows a link below the path it was asked for:
//     links are archived as links, so an archive carries the link and never the
//     content behind it.
//
// The confinement is a boundary of the *operation*, not a privilege boundary of
// the agent: the agent runs as root in the guest and its legacy `put`/`get`/`exec`
// reach the whole filesystem, as the node's own bootstrap needs. What the file
// root adds is that a caller who only holds the file operations — the session
// plane of RFC D9, which a template's own user may reach — cannot read or archive
// anything outside the workspace, however it spells the path.
//
// # Bounds
//
// Each operation declares its bound and hits it deterministically: a listing
// stops at maxListEntries and says so, and an archive stops at maxTarEntries or
// maxTarBytes and fails rather than returning a short answer. A truncated listing
// is a usable answer to "what is here"; a truncated archive is a corrupt artifact
// a caller would extract believing it complete.

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	// WorkspaceRoot is the directory every file operation is confined to: the
	// workspace mount point every sandbox template ships (RFC §5.4, `/workspace`
	// as a block device mounted by the guest).
	WorkspaceRoot = "/workspace"

	// maxListEntries is how many entries one `list` returns before it reports
	// Truncated. It is the bound the plan fixes for the operation, and it keeps a
	// listing's whole reply — which is held in memory on both sides — bounded.
	maxListEntries = 10000

	// maxTarEntries and maxTarBytes bound one `tar` stream. The byte bound is the
	// archive's own size, so it bounds the transfer; the entry bound stops a walk
	// that would otherwise be measured only in the bytes of its headers.
	maxTarEntries = 1 << 20
	maxTarBytes   = 5 << 30

	// maxTarWarnings bounds the report of entries an archive could not carry, so
	// a tree full of sockets cannot grow the terminal frame without limit. The
	// count of skipped entries is always exact, even when the list is cut short.
	maxTarWarnings = 32
)

// workspaceRoot is the directory the file operations are rooted at. It is a
// variable so a test can point them at a fixture tree; the guest agent always
// uses WorkspaceRoot.
var workspaceRoot = WorkspaceRoot

// FileEntry describes one file, directory or symbolic link.
//
// It is what both `stat` and each entry of `list` answer with, so a caller reads
// the same fields from both, and it is deliberately the metadata a copy needs and
// no more: nothing here is content, and nothing here is resolved. Name is the base
// name of the path in question — the caller already knows the path it asked for.
type FileEntry struct {
	Name string `json:"name"`
	// Type is "file", "dir", "symlink" or "other". A caller that copies a tree
	// branches on it, so it never has to read the mode string.
	Type string `json:"type"`
	Size int64  `json:"size"`
	// Mode is the file's mode as `ls -l` writes it ("-rw-r--r--"), including its
	// type character.
	Mode string `json:"mode"`
	// UID and GID are the owner and group the guest's kernel records. They are
	// the guest's own numbers; a node mapping its sandbox users to guest users
	// reads them to know what a file will be to its workload.
	UID int `json:"uid"`
	GID int `json:"gid"`
	// ModTime is the entry's modification time.
	ModTime time.Time `json:"mod_time"`
	// Target is what a symbolic link points at, exactly as it is stored, and is
	// empty for everything else. A link is never resolved to fill it.
	Target string `json:"target,omitempty"`
}

// Listing is the answer to `list`: the entries of one directory, sorted by name,
// and whether the bound cut them short.
type Listing struct {
	Entries []FileEntry `json:"entries"`
	// Truncated reports that the directory held more than maxListEntries entries
	// and that Entries is its first maxListEntries names, in order.
	Truncated bool `json:"truncated"`
}

// TarSummary is the answer to `tar`, sent on the frame that ends the archive.
type TarSummary struct {
	// Entries is how many entries the archive carries, and Bytes how many bytes
	// of archive were streamed.
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	// Warnings names the entries the archive does not carry, and why. It is
	// capped at maxTarWarnings; a tree with more of them still reports their
	// exact count in the last element.
	Warnings []string `json:"warnings,omitempty"`
}

// failure is a file failure inside the guest, carrying the code it must be
// reported with. It stays in the guest: the frame that reports it carries the
// code and the message and nothing else.
type failure struct {
	code ErrorCode
	msg  string
	// cause is the failure this one came from, when there is one. It never
	// crosses the wire; it is kept so a caller inside the guest (a test, an
	// operator's log) can still see what actually went wrong.
	cause error
}

func (e *failure) Error() string { return e.msg }

// Unwrap exposes the underlying failure.
func (e *failure) Unwrap() error { return e.cause }

// fail builds a typed failure.
func fail(code ErrorCode, format string, args ...any) error {
	return &failure{code: code, msg: fmt.Sprintf(format, args...)}
}

// failUnder is fail with the failure that caused it, kept in the chain.
func failUnder(code ErrorCode, cause error, format string, args ...any) error {
	return &failure{code: code, msg: fmt.Sprintf(format, args...), cause: cause}
}

// failureCode reports how a guest-side file failure must reach the caller: its
// own code when it has one, and the internal code when the agent itself failed.
func failureCode(err error) (ErrorCode, string) {
	var f *failure
	if errors.As(err, &f) {
		return f.code, f.msg
	}
	return CodeInternal, err.Error()
}

// classifyPathError names the failure a file root returned.
//
// The interesting case is os.Root's own refusal to resolve a path that leaves the
// root: it is the enforcement of the boundary above, and Go exposes no sentinel
// for it (os.errPathEscapes is unexported), so it is recognised by elimination —
// every other error a Root method returns carries the errno the kernel gave, and
// this one does not. Should a future Go release label it differently, the path is
// still refused; only this code would become the internal one.
func classifyPathError(err error) error {
	var errno syscall.Errno
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return fail(CodeNotFound, "%v", err)
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.ELOOP):
		return fail(CodeNotPermitted, "%v", err)
	case errors.As(err, &errno):
		return fail(CodeInternal, "%v", err)
	default:
		return fail(CodeUnsafePath, "%v", err)
	}
}

// confine turns a caller-supplied path into the path the file root resolves.
//
// The lexical pass is the explanation, not the enforcement: "a/../b" is cleaned to
// "b" because it stays inside the root, and only a path that still leaves it —
// an absolute path, or a ".." that survives cleaning — is refused here. Anything
// subtler, such as a link whose target is outside, is os.Root's to refuse, and it
// does.
func confine(rel string) (string, error) {
	if rel == "" {
		return ".", nil
	}
	if strings.ContainsRune(rel, 0) {
		return "", fail(CodeUnsafePath, "path %q is not a file name", rel)
	}
	clean := path.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", fail(CodeUnsafePath,
			"path %q is outside the file root: paths are relative to %s and may not leave it",
			rel, workspaceRoot)
	}
	return clean, nil
}

// openFileRoot opens the directory the file operations are confined to. A missing
// root is reported as not_found, which is what a sandbox whose template has no
// workspace mount at all should say.
func openFileRoot() (*os.Root, error) {
	root, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return nil, classifyPathError(err)
	}
	return root, nil
}

// statGuestFile describes the entry at rel as it is on disk.
//
// It never resolves a symbolic link — the entry is reported as a link, with its
// target — so `stat`, `list` and `tar` all describe a tree the same way, and a
// caller that wants to follow a link decides to, exactly as it would with the
// three syscalls those operations are named after.
func statGuestFile(rel string) (FileEntry, error) {
	clean, err := confine(rel)
	if err != nil {
		return FileEntry{}, err
	}
	root, err := openFileRoot()
	if err != nil {
		return FileEntry{}, err
	}
	defer func() { _ = root.Close() }()

	fi, err := root.Lstat(clean)
	if err != nil {
		return FileEntry{}, classifyPathError(err)
	}
	return entryFromInfo(root, clean, fi)
}

// listGuestDir lists the directory at rel: its entries, sorted by name, up to the
// operation's bound.
//
// The directory itself is opened through the root, so a link that stays inside the
// root is followed to it and a link that leaves is refused. Its entries are
// reported as they are, links included and unresolved — the same rule `stat` uses,
// and the same one `ls -l` follows.
func listGuestDir(rel string) (Listing, error) {
	clean, err := confine(rel)
	if err != nil {
		return Listing{}, err
	}
	root, err := openFileRoot()
	if err != nil {
		return Listing{}, err
	}
	defer func() { _ = root.Close() }()

	dir, err := root.Open(clean)
	if err != nil {
		return Listing{}, classifyPathError(err)
	}
	defer func() { _ = dir.Close() }()

	entries, err := dir.ReadDir(-1)
	if err != nil {
		return Listing{}, classifyPathError(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	// ReadDir returns a directory's entries in its own order, which differs
	// between filesystems and changes as the directory is used. The listing is
	// sorted so that two reads of an unchanged directory agree, and so that the
	// entries a bound cuts the listing at are always the same ones.
	sort.Strings(names)

	out := Listing{Entries: make([]FileEntry, 0, min(len(names), maxListEntries))}
	for _, name := range names {
		if len(out.Entries) >= maxListEntries {
			out.Truncated = true
			break
		}
		child := path.Join(clean, name)
		fi, err := root.Lstat(child)
		if err != nil {
			// The entry was in the directory a moment ago and is gone now: the
			// listing fails rather than quietly omitting it, because a caller
			// reading a listing as "the files that exist" must not be handed one
			// that is missing an entry it will later ask for by name.
			return Listing{}, classifyPathError(err)
		}
		entry, err := entryFromInfo(root, child, fi)
		if err != nil {
			return Listing{}, err
		}
		entry.Name = name
		out.Entries = append(out.Entries, entry)
	}
	return out, nil
}

// entryFromInfo describes one entry. rel is the path inside the root, child is
// always a direct name of a directory it is called for, and only a symbolic link
// is read further — for its target, never through it.
func entryFromInfo(root *os.Root, rel string, fi os.FileInfo) (FileEntry, error) {
	entry := FileEntry{
		Name:    path.Base(rel),
		Type:    entryType(fi.Mode()),
		Size:    fi.Size(),
		Mode:    modeString(fi.Mode()),
		ModTime: fi.ModTime(),
	}
	if uid, gid, ok := fileOwner(fi); ok {
		entry.UID, entry.GID = uid, gid
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := root.Readlink(rel)
		if err != nil {
			return FileEntry{}, classifyPathError(err)
		}
		entry.Target = target
	}
	return entry, nil
}

// modeString renders a mode the way `ls -l` writes it, type character included.
//
// os.FileMode.String() is Go's own notation — it writes a symbolic link as
// "Lrwxrwxrwx" and a directory as "drwxr-x---" — and a report a tool compares
// against `ls -l` should agree with it character for character, so the type
// character and the setuid/setgid/sticky letters are written the way ls writes
// them.
func modeString(mode os.FileMode) string {
	out := []byte("----------")
	switch {
	case mode.IsDir():
		out[0] = 'd'
	case mode&os.ModeSymlink != 0:
		out[0] = 'l'
	case mode&os.ModeNamedPipe != 0:
		out[0] = 'p'
	case mode&os.ModeSocket != 0:
		out[0] = 's'
	case mode&os.ModeCharDevice != 0:
		out[0] = 'c'
	case mode&os.ModeDevice != 0:
		out[0] = 'b'
	}

	const letters = "rwxrwxrwx"
	for i, bit := range []os.FileMode{0o400, 0o200, 0o100, 0o040, 0o020, 0o010, 0o004, 0o002, 0o001} {
		if mode&bit != 0 {
			out[i+1] = letters[i]
		}
	}

	// The setuid, setgid and sticky bits take the place of the execute letter
	// they modify, which is how ls reports them: lowercase when the execute bit is
	// also set, uppercase when it is not.
	if mode&os.ModeSetuid != 0 {
		out[3] = "Ss"[boolToInt(out[3] == 'x')]
	}
	if mode&os.ModeSetgid != 0 {
		out[6] = "Ss"[boolToInt(out[6] == 'x')]
	}
	if mode&os.ModeSticky != 0 {
		out[9] = "Tt"[boolToInt(out[9] == 'x')]
	}
	return string(out)
}

// boolToInt is 0 for false and 1 for true, which indexes the two-letter pairs
// above.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// entryType names an entry's kind for a caller that branches on it.
func entryType(mode os.FileMode) string {
	switch {
	case mode.IsRegular():
		return "file"
	case mode.IsDir():
		return "dir"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	default:
		return "other"
	}
}

// tarSink receives an archive one chunk at a time. It is the frame writer on the
// guest side and a caller's writer on the host side, and it is the only thing
// that sees the archive, so a stream is never held in memory.
type tarSink func(p []byte) error

// streamGuestTar archives the tree at rel into sink as a POSIX pax tar stream.
//
// Names inside the archive are relative to the requested path: an archive of
// "repo" holds "repo", "repo/sub" and "repo/sub/f", and an archive of the root
// itself holds "sub" and "sub/f", so extracting one lands the tree under the name
// it was asked for and extracting the root's archive fills a directory rather
// than overwriting it.
//
// Every failure aborts the stream: the archive is never closed after an error, so
// a short stream can never look like a finished one.
func streamGuestTar(ctx context.Context, rel string, sink tarSink) (TarSummary, error) {
	clean, err := confine(rel)
	if err != nil {
		return TarSummary{}, err
	}
	root, err := openFileRoot()
	if err != nil {
		return TarSummary{}, err
	}
	defer func() { _ = root.Close() }()

	st := &tarStreamer{ctx: ctx, root: root, out: &tarCounter{ctx: ctx, sink: sink}}
	st.buf = bufio.NewWriterSize(st.out, fileChunkSize)
	st.tw = tar.NewWriter(st.buf)

	if err := st.add(clean, archiveName(clean), true); err != nil {
		// The archive is left unclosed on purpose: the terminal frame carries the
		// failure instead, and a caller that ignores it still holds a stream that
		// is obviously short rather than one padded into looking complete.
		return TarSummary{}, err
	}
	if err := st.tw.Close(); err != nil {
		return TarSummary{}, st.classify(err)
	}
	if err := st.buf.Flush(); err != nil {
		return TarSummary{}, st.classify(err)
	}

	summary := st.summary
	summary.Bytes = st.out.written
	if st.skipped > len(summary.Warnings) {
		summary.Warnings = append(summary.Warnings,
			fmt.Sprintf("... and %d more entries the archive does not carry", st.skipped-len(summary.Warnings)))
	}
	return summary, nil
}

// tarStreamer walks one tree and writes it as an archive.
type tarStreamer struct {
	ctx  context.Context
	root *os.Root
	out  *tarCounter
	buf  *bufio.Writer
	tw   *tar.Writer

	summary TarSummary
	// skipped counts the entries the archive does not carry, which is exact even
	// when the reported list of them is cut short.
	skipped int
}

// archiveName is the name the entry at rel gets inside the archive. The root has
// no name of its own: an archive of it holds its children.
func archiveName(rel string) string {
	if rel == "." || rel == "/" {
		return ""
	}
	return rel
}

// add archives the entry at rel under name, and for a directory its contents.
//
// walkRoot says that rel is the path the caller asked for, which is the one place
// a symbolic link is resolved: asking to archive "repo" where repo is a link to a
// directory inside the root archives that directory, while a link met during the
// walk below it is archived as a link and never followed.
func (s *tarStreamer) add(rel, name string, walkRoot bool) error {
	if err := s.deadline(); err != nil {
		return err
	}
	fi, err := s.root.Lstat(rel)
	if err != nil {
		return classifyPathError(err)
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		if walkRoot {
			if resolved, err := s.root.Stat(rel); err == nil {
				switch {
				case resolved.IsDir():
					return s.addDir(rel, name, resolved)
				case resolved.Mode().IsRegular():
					return s.addFile(rel, name, resolved)
				}
			}
		}
		target, err := s.root.Readlink(rel)
		if err != nil {
			return classifyPathError(err)
		}
		return s.addLink(name, fi, target)
	}
	switch {
	case fi.IsDir():
		return s.addDir(rel, name, fi)
	case fi.Mode().IsRegular():
		return s.addFile(rel, name, fi)
	default:
		return s.skip(rel, fi)
	}
}

// addDir writes a directory's own entry and then its contents, in name order.
func (s *tarStreamer) addDir(rel, name string, fi os.FileInfo) error {
	// The archive of the root itself has no top-level entry of its own: its
	// contents are the archive.
	if name != "" {
		if err := s.writeEntry(name+"/", fi, ""); err != nil {
			return err
		}
	}

	dir, err := s.root.Open(rel)
	if err != nil {
		return classifyPathError(err)
	}
	defer func() { _ = dir.Close() }()

	entries, err := dir.ReadDir(-1)
	if err != nil {
		return classifyPathError(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, child := range names {
		if err := s.add(path.Join(rel, child), path.Join(name, child), false); err != nil {
			return err
		}
	}
	return nil
}

// addFile writes a regular file's entry and then its content.
func (s *tarStreamer) addFile(rel, name string, fi os.FileInfo) error {
	if err := s.writeEntry(name, fi, ""); err != nil {
		return err
	}
	f, err := s.root.Open(rel)
	if err != nil {
		return classifyPathError(err)
	}
	defer func() { _ = f.Close() }()

	// The content goes through the archive writer, which is what tracks how much
	// of the entry it has written: it hands complete 512-byte blocks to the
	// buffer, and the buffer is what turns them into frames of fileChunkSize, far
	// below any frame bound. The counter behind the buffer stops the stream at the
	// archive's own bound.
	if _, err := io.Copy(s.tw, f); err != nil {
		return s.classify(err)
	}
	return nil
}

// addLink writes a symbolic link's entry: the link, and never what it points at.
func (s *tarStreamer) addLink(name string, fi os.FileInfo, target string) error {
	return s.writeEntry(name, fi, target)
}

// writeEntry writes one header, holding the archive to its entry and byte bounds.
//
// The header is pax, always: pax carries long names, deep paths, sub-second
// timestamps and large owners, so no entry is silently reshaped to fit a 100-byte
// name or a 20-bit uid, and the same tree always produces the same archive.
func (s *tarStreamer) writeEntry(name string, fi os.FileInfo, link string) error {
	if s.summary.Entries >= maxTarEntries {
		return fail(CodeTooLarge, "tar: the archive reached its %d-entry bound", maxTarEntries)
	}
	hdr, err := tar.FileInfoHeader(fi, link)
	if err != nil {
		return fail(CodeInternal, "tar: %s: %v", name, err)
	}
	hdr.Name = name
	hdr.Format = tar.FormatPAX
	if err := s.tw.WriteHeader(hdr); err != nil {
		return s.classify(err)
	}
	s.summary.Entries++
	return nil
}

// skip records an entry the archive does not carry. A socket, a FIFO, a device
// node and anything else that is not a file, directory or link carries no content
// this operation is about, and opening one could block the walk for as long as
// nothing writes to it, so it is left out and named on the terminal frame.
func (s *tarStreamer) skip(rel string, fi os.FileInfo) error {
	s.skipped++
	if len(s.summary.Warnings) < maxTarWarnings {
		s.summary.Warnings = append(s.summary.Warnings, fmt.Sprintf("%s: %s", rel, specialType(fi.Mode())))
	}
	return nil
}

// specialType names what kind of special file an entry is, which is what makes a
// skipped entry's warning actionable: a socket and a device node are both "other"
// to a caller deciding how to copy a tree, but they are different problems for
// whoever owns the files.
func specialType(mode os.FileMode) string {
	switch {
	case mode&os.ModeNamedPipe != 0:
		return "fifo"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeDevice != 0:
		return "device"
	case mode&os.ModeCharDevice != 0:
		return "character device"
	default:
		return "other"
	}
}

// deadline bounds the stream by the caller's budget, checked before every entry
// and before every chunk, so an archive of a very large tree stops when its
// deadline does instead of running to its end.
func (s *tarStreamer) deadline() error {
	if err := s.ctx.Err(); err != nil {
		return fail(CodeDeadlineExceeded, "tar: deadline exceeded before the archive finished")
	}
	return nil
}

// classify turns a failure from the archive writer or the sink into a typed one.
// A failure that is already typed is passed through; anything else — a frame the
// connection refused, an archive the writer could not finish — is the agent's own
// failure, and the error it came from stays in the chain so a caller that inspects
// it still finds the reason.
func (s *tarStreamer) classify(err error) error {
	var f *failure
	if errors.As(err, &f) {
		return err
	}
	return failUnder(CodeInternal, err, "tar: %v", err)
}

// tarCounter counts the archive as it is written, stops the stream at the
// operation's byte bound, and checks the caller's budget before every chunk so a
// single large file cannot outlive its deadline.
type tarCounter struct {
	ctx     context.Context
	sink    tarSink
	written int64
}

func (c *tarCounter) Write(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, fail(CodeDeadlineExceeded, "tar: deadline exceeded before the archive finished")
	}
	if c.written+int64(len(p)) > maxTarBytes {
		return 0, fail(CodeTooLarge, "tar: the archive reached its %d-byte bound", int64(maxTarBytes))
	}
	if err := c.sink(p); err != nil {
		return 0, err
	}
	c.written += int64(len(p))
	return len(p), nil
}
