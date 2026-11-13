package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// DirName is the subdirectory of a node's state directory that holds sandbox
// records. It is a directory of its own so a daemon restart (or a reboot) finds
// exactly the records it wrote, and so that removing them is one directory rather
// than a pattern match over the node's other state.
const DirName = "sandboxes"

const (
	// recordSuffix is the extension of one record: <id>.json. The ID is the file
	// name because a lookup is by ID and nothing else.
	recordSuffix = ".json"

	// tempPrefix and tempSuffix frame the name of a record mid-write: Put writes
	// .sandbox-<id>-<random>.tmp and renames it into place. The leading dot keeps a
	// human's listing clean and, with the suffix, keeps a temporary out of both the
	// record glob and every reader's path.
	tempPrefix = ".sandbox-"
	tempSuffix = ".tmp"

	// probePattern names the throwaway file NewStore writes to prove the directory
	// accepts one. It never ends in an element a reader looks for.
	probePattern = ".sandbox-writable.probe-*"

	// dirMode is the mode of the record directory. A record names host paths and
	// the location of a sandbox's agent token; nobody but the daemon reads one.
	dirMode = 0o700

	// recordMode is the mode of one record. It is root-only for the same reason.
	recordMode = 0o600
)

// Dir returns the sandbox record directory for a node whose state directory is
// stateDir, or "" when stateDir is empty. It is one rule so the daemon and a test
// cannot disagree about where the records live.
func Dir(stateDir string) string {
	if strings.TrimSpace(stateDir) == "" {
		return ""
	}
	return filepath.Join(stateDir, DirName)
}

// StoreConfig configures a Store.
type StoreConfig struct {
	// Dir is the record directory, normally Dir(stateDir). It must be non-empty;
	// it is created (0700) when missing and write-probed, because a store that
	// cannot hold a record must be known when the daemon starts, not when a caller
	// asks for a sandbox.
	Dir string
}

// Store is the node's sandboxes, as records on disk. It is safe for concurrent use
// by different sandboxes and by concurrent readers of one sandbox; it is meant to
// have exactly one writing process (the node's daemon), which is what makes the
// record file, rather than a lock or an in-memory map, the whole state.
type Store struct {
	dir string
}

// NewStore opens — creating when needed — the record directory and proves it can be
// written to. The directory is 0700 and the records inside it are 0600.
//
// Creating the directory is not enough to know a record can be written: MkdirAll
// succeeds on an existing directory whatever its mode or mount (a read-only
// remount, a full filesystem), and a record is written while a sandbox is being
// built. So the directory is probed with a real write here, while the daemon can
// still refuse to start instead of running sandboxes nothing can find.
func NewStore(cfg StoreConfig) (*Store, error) {
	dir := strings.TrimSpace(cfg.Dir)
	if dir == "" {
		return nil, errors.New("sandbox store: a directory is required: pass " + DirName + " under the node's state directory")
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("sandbox store: create %s: %w", dir, err)
	}

	probe, err := os.CreateTemp(dir, probePattern)
	if err != nil {
		return nil, fmt.Errorf("sandbox store: %s is not writable: %w", dir, err)
	}
	probeName := probe.Name()
	_ = probe.Close()
	if err := os.Remove(probeName); err != nil {
		return nil, fmt.Errorf("sandbox store: %s is not writable: remove the probe file %s: %w", dir, probeName, err)
	}

	return &Store{dir: dir}, nil
}

// Dir returns the record directory.
func (s *Store) Dir() string { return s.dir }

// Put writes the record for one sandbox, replacing any record already there. The
// record is validated first — an unusable record is refused before anything touches
// the disk — then written to a unique temporary in the same directory, fsynced,
// renamed into place and the directory is fsynced: a reader sees the old record or
// the new one, and a failed write publishes nothing.
//
// Put is a replace, not an insert: the same sandbox is recorded again when it
// becomes ready, when it fails, and when it is touched, which is the whole of its
// life as the node sees it.
func (s *Store) Put(rec Record) error {
	if err := s.usable(); err != nil {
		return err
	}
	if err := rec.validate(); err != nil {
		return fmt.Errorf("sandbox record for %q: %w", rec.ID, err)
	}
	if err := s.write(rec); err != nil {
		// Name the directory. A full or unwritable store is a different problem
		// from a bad record, and the caller's next step differs.
		return fmt.Errorf("sandbox store %s: %w", s.dir, err)
	}
	return nil
}

// write publishes one already-validated record.
func (s *Store) write(rec Record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("sandbox record for %q: encode: %w", rec.ID, err)
	}

	// A unique temporary name: two Puts for one sandbox must not write through the
	// same file, and a crash must not leave something a reader could take for a
	// record.
	tmp, err := os.CreateTemp(s.dir, tempPrefix+rec.ID+"-*"+tempSuffix)
	if err != nil {
		return fmt.Errorf("sandbox record for %q: create temporary file: %w", rec.ID, err)
	}
	tmpName := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	if err := tmp.Chmod(recordMode); err != nil {
		discard()
		return fmt.Errorf("sandbox record for %q: mode %s: %w", rec.ID, tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		discard()
		return fmt.Errorf("sandbox record for %q: write %s: %w", rec.ID, tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return fmt.Errorf("sandbox record for %q: sync %s: %w", rec.ID, tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("sandbox record for %q: close %s: %w", rec.ID, tmpName, err)
	}
	if err := os.Rename(tmpName, recordPath(s.dir, rec.ID)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("sandbox record for %q: rename into place: %w", rec.ID, err)
	}

	syncDir(s.dir)
	return nil
}

// Get returns the record for a sandbox, as stored. found is false for an ID this
// node has never recorded; a record that exists but cannot be used — it does not
// decode, or it fails the rule Put enforces — is an error, and the file is left in
// place so a human can read what the node believes.
//
// Get does not ask whether the sandbox's VM is still running: that is the instance
// registry's question (pkg/vminstance), and the manager reconciles the two rather
// than making every reader of a control-plane record depend on a live process. A
// sandbox whose VMM is gone is Get's answer and Reconcile's to mark.
func (s *Store) Get(id string) (Record, bool, error) {
	if err := s.usable(); err != nil {
		return Record{}, false, err
	}
	if err := recordIDProblem(id); err != nil {
		return Record{}, false, err
	}
	return s.read(id)
}

// List returns every record sorted by ID, plus the paths of the records it could
// not use, so a listing is deterministic and complete. An unusable record is
// skipped rather than failing the listing, because one bad file must not hide the
// node's other sandboxes; the paths are what the daemon can warn about, and it warns
// without deleting them. Only a directory-level failure is an error.
func (s *Store) List() ([]Record, []string, error) {
	if err := s.usable(); err != nil {
		return nil, nil, err
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox store: read %s: %w", s.dir, err)
	}

	out := make([]Record, 0, len(entries))
	var skipped []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, recordSuffix) {
			continue
		}
		id := strings.TrimSuffix(name, recordSuffix)
		rec, found, err := s.read(id)
		if err != nil {
			skipped = append(skipped, filepath.Join(s.dir, name))
			continue
		}
		if !found {
			continue
		}
		out = append(out, rec)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	sort.Strings(skipped)
	return out, skipped, nil
}

// Delete removes a sandbox's record. A missing record is success: a rm path, a
// retried teardown and the reaper all run it, and none of them should fail because
// the record is already gone. A corrupt record is removed without being decoded, so
// a bad file cannot wedge a removal.
func (s *Store) Delete(id string) error {
	if err := s.usable(); err != nil {
		return err
	}
	if err := recordIDProblem(id); err != nil {
		return err
	}

	path := recordPath(s.dir, id)
	err := os.Remove(path)
	switch {
	case err == nil:
		// Put flushes the directory after its rename so the entry survives a
		// crash; a removal needs the same flush, or a crash could bring back the
		// record of a sandbox that is gone.
		syncDir(s.dir)
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("sandbox record for %q: delete %s: %w", id, path, err)
	}
}

// read decodes one record without validating it against the world: the rules a
// record must satisfy to be written are checked again here, because a file a human
// edited, or one written by an older build, must not be handed out as if it were a
// record this build could have written.
func (s *Store) read(id string) (Record, bool, error) {
	data, err := os.ReadFile(recordPath(s.dir, id))
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("sandbox record for %q: read: %w", id, err)
	}

	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, false, fmt.Errorf("sandbox record for %q: decode: %w", id, err)
	}
	if rec.ID != id {
		return Record{}, false, fmt.Errorf("sandbox record %s names sandbox %q: one record is one sandbox", recordPath(s.dir, id), rec.ID)
	}
	if err := rec.validate(); err != nil {
		return Record{}, false, fmt.Errorf("sandbox record for %q: %w", id, err)
	}
	return rec, true, nil
}

// usable reports whether the store has a directory at all.
func (s *Store) usable() error {
	if s == nil || s.dir == "" {
		return errors.New("sandbox store: not opened")
	}
	return nil
}

// recordPath is the file one sandbox's record lives in. One rule, so Put, Get and
// Delete cannot disagree.
func recordPath(dir, id string) string {
	return filepath.Join(dir, id+recordSuffix)
}

// syncDir flushes a directory after a rename or a removal, so the change survives a
// crash. A directory that cannot be flushed is not worth failing the write over: the
// file is already in place, and the next write repeats the flush.
func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}

// recordIDProblem refuses an ID the store could not name a record after. The rule is
// the node's own rule for "this string can name a file" (runtime.CheckTaskID), which
// is also the rule the launcher and the network namespace apply to the same ID, so
// the record, the jail and the namespace cannot disagree about what the sandbox is
// called.
func recordIDProblem(id string) error {
	if err := runtime.CheckTaskID(id); err != nil {
		return fmt.Errorf("sandbox ID %q cannot name a sandbox: %w", id, err)
	}
	return nil
}
