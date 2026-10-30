package migration

// This file owns the manager's spool: the one place an artifact's bytes rest
// between the node that produced them and the node that will restore them.
//
// A live migration moves the files of an artifact from the source node to the
// target node, and the manager is the only party both of them can reach. It is
// also the only party that must never hold a whole file: an artifact may carry a
// 64 GiB memory image (MaxFileSize), and the manager is a control plane that may
// be running other migrations at the same time. So each file is streamed into a
// file in the spool, and streamed out again, and the transfer is bounded by a
// fixed buffer at every hop (the broker's relay uses one; this package copies
// with io.Copy's).
//
// # One artifact, one directory, one file
//
// The spool has the same shape the nodes use for their own copy of an artifact -
// <root>/<artifactID>/<name> - because an artifact is addressed by its own ID on
// every hop. It deliberately holds only one file per artifact at a time: the
// transfer is a sequence (state file, memory image, disk), the manager has no use
// for two of them at once, and a slot of one file makes "the spool is empty" a
// single, checkable fact rather than a count. A second file for the same artifact
// is refused until the first is removed; the caller's protocol is write, hand
// over, remove.
//
// # The filesystem is the state
//
// As with the node's restored-instance records, nothing is cached in memory: a
// Writer publishes by rename, a reader opens by name, and Remove removes the
// directory. The one piece of in-memory state is the reservation of a slot while
// a Writer is open, so two concurrent writes for one artifact cannot both believe
// they own it; that state dies with the process, and Sweep is what removes what a
// crash left behind.
//
// # The spool is not a store
//
// A file is removed when the migration finishes, when it fails, or after the TTL,
// whichever comes first: the spool is a hand-off buffer, not a cache, and its
// contents are worthless once the target has the bytes. Sweep is therefore a
// backstop for the migration that never got to clean up, not a retention policy.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// SpoolDirName is the subdirectory of the manager's state directory that
	// holds artifacts in transit. It is a subdirectory of its own so a manager
	// restart finds exactly the files it wrote, and so removing them is one
	// directory rather than a pattern over the daemon's state.
	SpoolDirName = "migration-spool"

	// DefaultSpoolTTL is how long an artifact may rest in the spool untouched
	// before Sweep removes it. It is the migration's own rollback window: a
	// restore that keeps failing is retried against the same artifact, and after
	// this long a retry boots the workload fresh instead.
	DefaultSpoolTTL = 10 * time.Minute

	// spoolDirectoryMode is the mode of the spool root and of an artifact's
	// directory: an artifact carries a guest's memory image, so nobody but the
	// daemon reads one.
	spoolDirectoryMode = 0o700

	// spoolFileMode is the mode of a spooled file. A memory image may hold guest
	// secrets, so a spooled copy is never group- or world-readable.
	spoolFileMode = 0o600

	// spoolPartialPrefix starts the name of a temporary a Writer creates beside
	// the artifact directory. It carries the artifact ID so a Sweep - and Remove -
	// can find what a crash mid-write left behind; the leading dot keeps it from
	// ever being taken for an artifact directory.
	spoolPartialPrefix = ".spool-"
)

// SpoolRoot returns the spool directory for a manager whose state directory is
// stateDir, or "" when stateDir is empty. It is one rule so the daemon and a test
// cannot disagree about where the spool lives.
func SpoolRoot(stateDir string) string {
	if strings.TrimSpace(stateDir) == "" {
		return ""
	}
	return filepath.Join(stateDir, SpoolDirName)
}

// StoreConfig configures the manager's spool.
type StoreConfig struct {
	// Root is the spool directory, normally SpoolRoot(stateDir). It must be
	// absolute: a relative root would resolve against the daemon's working
	// directory, which is not where its state lives. It is created on first use.
	Root string
	// TTL is how long an artifact may rest untouched before Sweep removes it.
	// Zero means DefaultSpoolTTL; a negative value is refused rather than
	// silently treated as the default, because it would sweep everything at once.
	TTL time.Duration
	// Now is the clock Sweep measures ages against. Nil means time.Now; a test
	// sets it so a TTL can be reached without waiting for one.
	Now func() time.Time
}

// Store is the manager's spool of migration artifacts in transit. It is safe for
// concurrent use by different artifacts, and it refuses two writers for one
// artifact.
type Store struct {
	root string
	ttl  time.Duration
	now  func() time.Time

	// mu guards held, the in-process reservation of each artifact's one slot.
	// The filesystem is the durable state; this map is what makes the one-file
	// rule hold between two writers that both looked at an empty directory.
	mu   sync.Mutex
	held map[string]string
}

// NewStore validates the configuration. The root is not created here: a manager
// that never migrates should not leave a directory behind, and the first Writer
// creates what it needs.
func NewStore(cfg StoreConfig) (*Store, error) {
	root := strings.TrimSpace(cfg.Root)
	if root == "" {
		return nil, errors.New("migration: the spool root is required: pass the manager state directory's spool, <state-dir>/" + SpoolDirName)
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("migration: spool root %q is relative: want an absolute directory under the manager's state directory", root)
	}
	if cfg.TTL < 0 {
		return nil, fmt.Errorf("migration: spool TTL %s is negative: want a positive duration, or zero for the %s default", cfg.TTL, DefaultSpoolTTL)
	}

	ttl := cfg.TTL
	if ttl == 0 {
		ttl = DefaultSpoolTTL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	return &Store{root: root, ttl: ttl, now: now, held: make(map[string]string)}, nil
}

// Root returns the spool directory.
func (s *Store) Root() string { return s.root }

// TTL returns the age after which Sweep removes an artifact.
func (s *Store) TTL() time.Duration { return s.ttl }

// Writer reserves the artifact's one slot and returns a writer that fills it. The
// file is published under its name only when Close sees exactly the declared size
// and digest; any other outcome removes the slot and returns a typed error, so a
// refused transfer leaves nothing a restore could mistake for a complete file.
//
// A second Writer for the same artifact - a different file, or a second attempt at
// the same one - is refused until the first is closed and the entry removed: the
// spool holds one file per artifact at a time. A closed Writer's file must be
// removed with Remove before the next file of the artifact can be spooled.
func (s *Store) Writer(artifactID string, file File) (*SpoolWriter, error) {
	if !ValidArtifactID(artifactID) {
		return nil, spoolInvalidArtifactID(artifactID)
	}
	if err := file.Validate(); err != nil {
		return nil, spoolInvalidFile(err)
	}

	s.mu.Lock()
	if held, ok := s.held[artifactID]; ok {
		s.mu.Unlock()
		return nil, spoolOccupied(artifactID, held)
	}
	s.held[artifactID] = file.Name
	s.mu.Unlock()

	writer, err := s.openWriter(artifactID, file)
	if err != nil {
		s.release(artifactID, file.Name)
		return nil, err
	}
	return writer, nil
}

// openWriter creates the artifact directory and the temporary file the transfer
// fills. The temporary lives beside the artifact directory rather than inside it,
// so the directory holds at most the one file the slot names, even mid-write.
func (s *Store) openWriter(artifactID string, file File) (*SpoolWriter, error) {
	if err := os.MkdirAll(s.root, spoolDirectoryMode); err != nil {
		return nil, spoolUnavailable("create the spool root")
	}
	dir := filepath.Join(s.root, artifactID)
	if err := ensureSpoolDir(dir); err != nil {
		return nil, err
	}
	if err := checkSpoolSlot(dir, file.Name); err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp(s.root, spoolPartialPrefix+artifactID+"-*")
	if err != nil {
		return nil, spoolUnavailable("create a spool file")
	}

	return &SpoolWriter{
		store:    s,
		artifact: artifactID,
		file:     file,
		dir:      dir,
		tmp:      tmp,
		tmpPath:  tmp.Name(),
		digest:   sha256.New(),
	}, nil
}

// SpoolWriter is one artifact file being written into the spool. It implements
// io.WriteCloser; Abort is the way to abandon a transfer the caller has already
// given up on, without letting a complete-looking file be published.
type SpoolWriter struct {
	store    *Store
	artifact string
	file     File
	dir      string

	tmp     *os.File
	tmpPath string
	digest  hash.Hash
	written int64

	done bool
}

// Write appends p to the spooled file. A write past the declared size is refused
// before it reaches the disk: the manifest says how large the file is, so a
// longer stream is a different file and the transfer stops there rather than
// writing it in full and calling it a mismatch afterwards.
func (w *SpoolWriter) Write(p []byte) (int, error) {
	if w.done {
		return 0, output.NewError(output.CodeInternal, "the spool file is already closed").
			WithDetails(map[string]any{"reason": "spool_closed"})
	}
	if remaining := w.file.Size - w.written; int64(len(p)) > remaining {
		return 0, spoolBodySizeMismatch(w.file.Name, w.written+int64(len(p)), w.file.Size)
	}

	n, err := w.tmp.Write(p)
	if n > 0 {
		w.written += int64(n)
		_, _ = w.digest.Write(p[:n])
	}
	if err != nil {
		return n, spoolUnavailable("write a spool file")
	}
	return n, nil
}

// Close verifies and publishes the file. It is a failure when the transfer wrote
// fewer bytes than the artifact describes or the digest is not the declared one;
// in both cases the artifact's directory is removed, so a reader can never find a
// file that would not survive the target's own checks.
func (w *SpoolWriter) Close() error {
	if w.done {
		return nil
	}

	if w.written != w.file.Size {
		w.done = true
		w.cleanup()
		return spoolBodySizeMismatch(w.file.Name, w.written, w.file.Size)
	}
	if sum := hex.EncodeToString(w.digest.Sum(nil)); sum != w.file.SHA256 {
		w.done = true
		w.cleanup()
		return spoolChecksumMismatch(w.file.Name, sum, w.file.SHA256)
	}

	// Durable before it is addressable: the target reads this file next, and a
	// rename that outlives the data it names would hand it a truncated image.
	if err := w.tmp.Sync(); err != nil {
		w.done = true
		w.cleanup()
		return spoolUnavailable("write a spool file")
	}
	if err := w.tmp.Close(); err != nil {
		w.done = true
		w.cleanup()
		return spoolUnavailable("write a spool file")
	}
	// CreateTemp opens 0600; the chmod makes that mode explicit rather than a
	// property of the standard library.
	if err := os.Chmod(w.tmpPath, spoolFileMode); err != nil {
		w.done = true
		w.cleanup()
		return spoolUnavailable("write a spool file")
	}
	if err := os.Rename(w.tmpPath, filepath.Join(w.dir, w.file.Name)); err != nil {
		w.done = true
		w.cleanup()
		return spoolUnavailable("publish a spool file")
	}

	w.done = true
	w.store.release(w.artifact, w.file.Name)
	return nil
}

// Abort abandons the transfer: the temporary is removed, the artifact's directory
// goes with it, and nothing is published. It is what a caller uses when the bytes
// came from somewhere that already failed - a source node that answered a
// different digest, a writer that stopped early - and it is idempotent, and a
// no-op after Close.
func (w *SpoolWriter) Abort() error {
	if w.done {
		return nil
	}
	w.done = true
	w.cleanup()
	return nil
}

// cleanup removes the temporary and the artifact directory. It is best effort on
// the temporary: the error a caller sees is the one that made the transfer fail,
// and a failed cleanup must not replace it. The directory removal is what makes
// the failure observable: the spool holds nothing a restore could use.
func (w *SpoolWriter) cleanup() {
	_ = w.tmp.Close()
	_ = os.Remove(w.tmpPath)
	_ = os.RemoveAll(w.dir)
	w.store.release(w.artifact, w.file.Name)
}

// release frees the artifact's slot reservation. It only clears the entry this
// writer set, so a later writer's reservation cannot be dropped by an earlier
// one's deferred cleanup.
func (s *Store) release(artifactID, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if held, ok := s.held[artifactID]; ok && held == name {
		delete(s.held, artifactID)
	}
}

// Open returns a reader over one spooled file. The reader verifies the bytes as
// it serves them: the file must be the declared size when it is opened, and the
// digest is checked at the end of the stream, so a file that changed after it was
// spooled fails the transfer instead of reaching the target under a checksum that
// describes something else.
//
// The caller closes the reader; the file itself stays in the spool until Remove.
func (s *Store) Open(artifactID string, file File) (*SpoolReader, error) {
	if !ValidArtifactID(artifactID) {
		return nil, spoolInvalidArtifactID(artifactID)
	}
	if err := file.Validate(); err != nil {
		return nil, spoolInvalidFile(err)
	}

	dir := filepath.Join(s.root, artifactID)
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, spoolMissing(artifactID, file.Name)
	case err != nil:
		return nil, spoolUnavailable("read the spool")
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		// A symbolic link where an artifact directory belongs would serve whatever
		// it points at under this artifact's name.
		return nil, spoolMissing(artifactID, file.Name)
	}

	// Opened without following a link, like the node's own file operation: a link
	// planted in the directory would serve another artifact's bytes.
	path := filepath.Join(dir, file.Name)
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ELOOP):
			return nil, spoolMissing(artifactID, file.Name)
		default:
			return nil, spoolUnavailable("open a spool file")
		}
	}

	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, spoolUnavailable("open a spool file")
	}
	if !stat.Mode().IsRegular() {
		_ = f.Close()
		return nil, spoolChanged(file.Name)
	}
	if stat.Size() != file.Size {
		_ = f.Close()
		return nil, spoolFileSizeMismatch(file.Name, stat.Size(), file.Size)
	}

	return &SpoolReader{
		f:      f,
		src:    io.LimitReader(f, file.Size),
		digest: sha256.New(),
		file:   file,
	}, nil
}

// SpoolReader streams one spooled file and verifies it as it goes. It implements
// io.ReadCloser.
type SpoolReader struct {
	f      *os.File
	src    io.Reader
	digest hash.Hash
	file   File
	read   int64
	err    error
}

// Read streams the next slice of the file. The digest is checked when the stream
// ends: a mismatch is reported as the error of the final read, after the bytes
// that were served, so a caller copying the file gets both the bytes and the
// refusal.
func (r *SpoolReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}

	n, err := r.src.Read(p)
	if n > 0 {
		r.read += int64(n)
		_, _ = r.digest.Write(p[:n])
	}
	if errors.Is(err, io.EOF) {
		if verifyErr := r.verify(); verifyErr != nil {
			r.err = verifyErr
			return n, verifyErr
		}
		r.err = io.EOF
		return n, io.EOF
	}
	if err != nil {
		r.err = spoolUnavailable("read a spool file")
		return n, r.err
	}
	return n, nil
}

// verify checks what the stream served against the manifest.
func (r *SpoolReader) verify() error {
	if r.read != r.file.Size {
		return spoolFileSizeMismatch(r.file.Name, r.read, r.file.Size)
	}
	if sum := hex.EncodeToString(r.digest.Sum(nil)); sum != r.file.SHA256 {
		return spoolChecksumMismatch(r.file.Name, sum, r.file.SHA256)
	}
	return nil
}

// Close closes the underlying file. It does not remove it: the spool entry is
// removed by Remove, once the target has the bytes.
func (r *SpoolReader) Close() error {
	return r.f.Close()
}

// Remove deletes everything the spool holds for one artifact: its directory and
// any temporary a crashed write left beside it. Removing an artifact the spool
// does not hold is a success, which is what lets a success path, a failure path
// and a rollback all run it.
//
// It is not synchronised with a writer: removing an artifact that is still being
// written is how a cancelled transfer is abandoned, and the writer then fails when
// it closes rather than publishing a file into a directory that is gone.
func (s *Store) Remove(artifactID string) error {
	if !ValidArtifactID(artifactID) {
		return spoolInvalidArtifactID(artifactID)
	}

	if err := os.RemoveAll(filepath.Join(s.root, artifactID)); err != nil {
		return spoolUnavailable("remove a spooled artifact")
	}
	partials, _ := filepath.Glob(filepath.Join(s.root, spoolPartialPrefix+artifactID+"-*"))
	for _, partial := range partials {
		_ = os.Remove(partial)
	}
	return nil
}

// Sweep removes every artifact older than the TTL, and every temporary a crashed
// write left behind. It returns how many entries it removed. An entry is aged by
// its own modification time - a directory's changes when a file is published into
// it - so an artifact that is being written right now is not swept: a transfer
// longer than the TTL is exactly what the reservation check protects.
//
// One failure does not stop the sweep: the entries after it still go, and the
// first failure is returned so a caller can log that the spool may still hold
// what it asked to remove.
func (s *Store) Sweep() (int, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, spoolUnavailable("read the spool")
	}

	now := s.now()
	removed := 0
	var firstErr error

	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(s.root, name)

		switch {
		case ValidArtifactID(name):
			if s.isHeld(name) || !s.expired(path, now) {
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				if firstErr == nil {
					firstErr = spoolUnavailable("sweep the spool")
				}
				continue
			}
			removed++
		case strings.HasPrefix(name, spoolPartialPrefix):
			if !s.expired(path, now) {
				continue
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				if firstErr == nil {
					firstErr = spoolUnavailable("sweep the spool")
				}
				continue
			}
			removed++
		default:
			// Not the spool's: leaving it alone is the only safe answer for an
			// entry this package did not create.
		}
	}

	return removed, firstErr
}

// isHeld reports whether a writer currently owns the artifact's slot.
func (s *Store) isHeld(artifactID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.held[artifactID]
	return ok
}

// expired reports whether path was last modified at least one TTL ago. A path
// that cannot be stat'ed is treated as not expired: the sweep must not remove
// what it cannot inspect.
func (s *Store) expired(path string, now time.Time) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return now.Sub(info.ModTime()) >= s.ttl
}

// ensureSpoolDir creates one artifact's directory and refuses a path that exists
// as anything else: a symbolic link or a plain file where an artifact directory
// belongs would put the spooled file somewhere other than the artifact it names.
func ensureSpoolDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(dir, spoolDirectoryMode); err != nil {
			return spoolUnavailable("create the artifact directory")
		}
		return nil
	case err != nil:
		return spoolUnavailable("read the artifact directory")
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return spoolChanged(filepath.Base(dir))
	default:
		return nil
	}
}

// checkSpoolSlot refuses a write while the artifact's directory holds a file of
// another name: the spool holds one file per artifact at a time. A file of the
// same name is the one this write replaces - a retry of the same file - and is
// allowed.
func checkSpoolSlot(dir, name string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return spoolUnavailable("read the artifact directory")
	}
	for _, entry := range entries {
		if entry.Name() != name {
			return spoolOccupied(filepath.Base(dir), entry.Name())
		}
	}
	return nil
}

// spoolInvalidArtifactID is the typed 400 for an ID that is not one safe path
// segment. It is the same shape the node's operations accept, because the manager
// forwards the ID unchanged.
func spoolInvalidArtifactID(artifactID string) *output.Error {
	return output.NewErrorf(output.CodeInvalid,
		"artifact ID %q is not a migration artifact ID: want %q followed by 16 lowercase hex characters",
		artifactID, artifactIDPrefix).
		WithDetails(map[string]any{"reason": "invalid_artifact_id"})
}

// spoolInvalidFile is the typed 400 for a description this package cannot verify
// against.
func spoolInvalidFile(err error) *output.Error {
	return output.NewErrorf(output.CodeInvalid, "the spool entry is not one file of an artifact: %v", err).
		WithDetails(map[string]any{"reason": "invalid_file"})
}

// spoolMissing is the typed 404 for a file the spool does not hold.
func spoolMissing(artifactID, name string) *output.Error {
	return output.NewErrorf(output.CodeNotFound,
		"artifact %s file %q is not in the spool", artifactID, name).
		WithDetails(map[string]any{"reason": "spool_missing", "name": name})
}

// spoolOccupied is the typed 409 for a write while the artifact's slot is taken.
func spoolOccupied(artifactID, held string) *output.Error {
	return output.NewErrorf(output.CodeConflict,
		"artifact %s already holds %q in the spool: a migration spools one file at a time, so remove that file before writing another",
		artifactID, held).
		WithDetails(map[string]any{"reason": "spool_occupied", "held": held})
}

// spoolChanged is the typed 409 for a spool entry that is not the plain file the
// manifest describes.
func spoolChanged(name string) *output.Error {
	return output.NewErrorf(output.CodeConflict,
		"spool entry %q is not a regular file: the artifact describes one", name).
		WithDetails(map[string]any{"reason": "spool_changed", "name": name})
}

// spoolBodySizeMismatch is the typed failure for a body that is not the length the
// artifact declares. It is invalid rather than a conflict because the bytes are the
// caller's: it passed something other than the file it said it was passing, which is
// the same answer the node gives a stage whose body does not match its size.
func spoolBodySizeMismatch(name string, got, want int64) *output.Error {
	return output.NewErrorf(output.CodeInvalid,
		"spool file %q is %d bytes: the artifact describes %d", name, got, want).
		WithDetails(map[string]any{"reason": "size_mismatch", "name": name, "size": want})
}

// spoolFileSizeMismatch is the typed failure for a spooled file that is not the
// length it was written as: the file changed after it was published, which is a
// conflict between what the manifest describes and what is on disk.
func spoolFileSizeMismatch(name string, got, want int64) *output.Error {
	return output.NewErrorf(output.CodeConflict,
		"spool file %q is %d bytes: the artifact describes %d", name, got, want).
		WithDetails(map[string]any{"reason": "size_mismatch", "name": name, "size": want})
}

// spoolChecksumMismatch is the typed failure for bytes that are not the digest the
// artifact declares.
func spoolChecksumMismatch(name, got, want string) *output.Error {
	return output.NewErrorf(output.CodeConflict,
		"spool file %q has SHA-256 %s: the artifact declares %s", name, got, want).
		WithDetails(map[string]any{"reason": "checksum_mismatch", "name": name, "sha256": want})
}

// spoolUnavailable is the typed 503 for a spool this daemon cannot read or write.
// It names the operation and never the path: the spool lives under the manager's
// state directory, and a message that named it would tell a caller where the
// manager keeps the memory image of somebody's guest.
func spoolUnavailable(operation string) *output.Error {
	return output.NewErrorf(output.CodeUnavailable, "the migration spool could not %s", operation).
		WithDetails(map[string]any{"reason": "spool_unavailable"})
}
