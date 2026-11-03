package vminstance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DirName is the subdirectory of a node's state directory that holds the
// registry. It is a subdirectory of its own so that a daemon restart (or a
// reboot) finds exactly the records it wrote, and so that removing them is one
// directory rather than a pattern match over the node's other state.
const DirName = "vminstances"

const (
	// recordSuffix is the extension of one record: <id>.json. The ID is the
	// file name because a lookup is by ID and nothing else.
	recordSuffix = ".json"

	// tempPrefix and tempSuffix frame the name of a record mid-write: Put
	// writes .instance-<id>-<random>.tmp and renames it into place. The
	// leading dot keeps a human's listing clean and, with the suffix, keeps a
	// temporary out of both the record glob and every reader's path.
	tempPrefix = ".instance-"
	tempSuffix = ".tmp"

	// probePattern names the throwaway file NewStore writes to prove the
	// registry directory accepts one. It never ends in an element a reader
	// looks for, so a crash mid-probe leaves something a human can identify
	// rather than a record.
	probePattern = ".vminstance-writable.probe-*"

	// dirMode is the mode of the registry directory. A record names host paths
	// and says which VMs a node is running; nobody but the daemon reads one.
	dirMode = 0o700
)

// Dir returns the registry directory for a node whose state directory is
// stateDir, or "" when stateDir is empty. It is one rule so the daemon and a
// test cannot disagree about where the registry lives.
func Dir(stateDir string) string {
	if strings.TrimSpace(stateDir) == "" {
		return ""
	}
	return filepath.Join(stateDir, DirName)
}

// Config configures a Store.
type Config struct {
	// Dir is the registry directory, normally Dir(stateDir). It must be
	// non-empty; it is created (0700) when missing and write-probed, because a
	// registry that cannot hold a record must be known when the node starts,
	// not when it boots a VM.
	Dir string
	// Alive reports whether pid still names a running process. Nil means the
	// platform's own answer; a test sets it so a fixture can describe a live
	// VM without spawning a process, and a dead one without killing it.
	Alive func(pid int) bool
}

// Store is the node's registry of running VMs. It is safe for concurrent use by
// different instances and by concurrent readers of one instance; it is meant to
// have exactly one writing process (the node's daemon), which is what makes the
// record file, rather than a lock or an in-memory map, the whole state.
type Store struct {
	dir   string
	alive func(pid int) bool
}

// NewStore opens — creating when needed — the registry and proves it can be
// written to. The directory is 0700 and the records inside it are 0600.
//
// Creating the directory is not enough to know a record can be written: MkdirAll
// succeeds on an existing directory whatever its mode or mount (a read-only
// remount, a full filesystem), and Put runs while a VM is starting. So the
// directory is probed with a real write here, while the daemon can still refuse
// to start instead of running VMs nothing can resolve after a restart.
func NewStore(cfg Config) (*Store, error) {
	dir := strings.TrimSpace(cfg.Dir)
	if dir == "" {
		return nil, errors.New("instance registry: a directory is required: pass " + DirName + " under the node's state directory")
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("instance registry: create %s: %w", dir, err)
	}

	probe, err := os.CreateTemp(dir, probePattern)
	if err != nil {
		return nil, fmt.Errorf("instance registry: %s is not writable: %w", dir, err)
	}
	probeName := probe.Name()
	_ = probe.Close()
	if err := os.Remove(probeName); err != nil {
		return nil, fmt.Errorf("instance registry: %s is not writable: remove the probe file %s: %w", dir, probeName, err)
	}

	alive := cfg.Alive
	if alive == nil {
		alive = processAlive
	}
	return &Store{dir: dir, alive: alive}, nil
}

// Dir returns the registry directory.
func (s *Store) Dir() string { return s.dir }

// Put writes the record for one instance, replacing any record already there.
// The record is validated first — an unusable record is refused before anything
// touches the disk — and it is refused when its process is not running, so the
// registry never gains a record that resolves to a dead PID. Then it is written
// to a unique temporary in the same directory, fsynced, renamed into place and
// the directory is fsynced: a reader sees the old record or the new one, and a
// failed write publishes nothing — the previous record, if any, is what stays.
//
// Put is a replace, not an insert: the same instance is recorded again when it
// finishes booting (Ready flips) and when its address or jail uid is assigned
// later than the paths were known.
func (s *Store) Put(inst Instance) error {
	if err := s.usable(); err != nil {
		return err
	}
	if err := inst.validate(); err != nil {
		return fmt.Errorf("instance record for %q: %w", inst.ID, err)
	}
	if !s.alive(inst.PID) {
		return fmt.Errorf("instance record for %q: process %d is not running: a record must never resolve to a dead process", inst.ID, inst.PID)
	}

	data, err := json.MarshalIndent(inst, "", "  ")
	if err != nil {
		return fmt.Errorf("instance record for %q: encode: %w", inst.ID, err)
	}

	// A unique temporary name: two Puts for one instance must not write through
	// the same file, and a crash must not leave something a reader could take
	// for a record.
	tmp, err := os.CreateTemp(s.dir, tempPrefix+inst.ID+"-*"+tempSuffix)
	if err != nil {
		return fmt.Errorf("instance record for %q: create temporary file: %w", inst.ID, err)
	}
	tmpName := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		discard()
		return fmt.Errorf("instance record for %q: write %s: %w", inst.ID, tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return fmt.Errorf("instance record for %q: sync %s: %w", inst.ID, tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("instance record for %q: close %s: %w", inst.ID, tmpName, err)
	}
	if err := os.Rename(tmpName, recordPath(s.dir, inst.ID)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("instance record for %q: rename into place: %w", inst.ID, err)
	}

	syncDir(s.dir)
	return nil
}

// Get returns the record for an instance. found is false for an ID this node
// never recorded, and for a record whose process is no longer running: a dead
// VM must not resolve, or a caller would dial a socket nobody listens on. A
// record that exists but cannot be used — it does not decode, it names another
// instance, or it fails the rule Put enforces — is an error, and the file is
// left in place so a human can read what the node believes.
//
// Get does not delete a stale record: a reader must not mutate what it inspects,
// and the file is the only handle to what the dead VM left behind. Recover
// reports those records at load time, and Delete removes one after its
// resources are gone.
func (s *Store) Get(id string) (Instance, bool, error) {
	if err := s.usable(); err != nil {
		return Instance{}, false, err
	}
	if err := instanceIDProblem(id); err != nil {
		return Instance{}, false, err
	}

	inst, found, err := s.read(id)
	if err != nil || !found {
		return Instance{}, found, err
	}
	if !s.alive(inst.PID) {
		return Instance{}, false, nil
	}
	return inst, true, nil
}

// List returns every live instance sorted by ID, plus the paths of the records
// it could not use, so a listing is deterministic and complete. A record whose
// process is gone is not listed — it must not resolve — and is not reported as
// a skipped path either: it is a usable record that describes a dead VM, which
// Recover reports as stale. An unusable record is skipped rather than failing
// the listing, because one bad file must not hide the node's other VMs; the
// paths are what the daemon can warn about, and it warns without deleting them.
// Only a directory-level failure is an error.
func (s *Store) List() ([]Instance, []string, error) {
	if err := s.usable(); err != nil {
		return nil, nil, err
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil, fmt.Errorf("instance registry: read %s: %w", s.dir, err)
	}

	out := make([]Instance, 0, len(entries))
	var skipped []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, recordSuffix) {
			continue
		}
		id := strings.TrimSuffix(name, recordSuffix)
		inst, found, err := s.read(id)
		if err != nil {
			skipped = append(skipped, filepath.Join(s.dir, name))
			continue
		}
		if !found || !s.alive(inst.PID) {
			continue
		}
		out = append(out, inst)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	sort.Strings(skipped)
	return out, skipped, nil
}

// Delete removes an instance's record. A missing record is success: a stop
// path, a retried teardown, and Recover all run it, and none of them should fail
// because the record is already gone. A corrupt record is removed without being
// decoded, so a bad file cannot wedge a stop.
func (s *Store) Delete(id string) error {
	if err := s.usable(); err != nil {
		return err
	}
	if err := instanceIDProblem(id); err != nil {
		return err
	}

	path := recordPath(s.dir, id)
	err := os.Remove(path)
	switch {
	case err == nil:
		// Put flushes the directory after its rename so the entry survives a
		// crash; a removal needs the same flush, or a crash could bring back
		// the record of a VM that is gone and the next teardown would report
		// work that is already done.
		syncDir(s.dir)
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("instance record for %q: delete %s: %w", id, path, err)
	}
}

// Recovery is what a load-time scan of the registry found.
type Recovery struct {
	// Stale are the usable records whose VMM process is no longer running: the
	// VMs a previous daemon life left behind when it died outside a stop path.
	// They are left on disk and not resolved, because the record names the
	// netns, TAP device and disk still to be torn down; a caller tears those
	// down and then Delete the record.
	Stale []Instance
	// Skipped are the records that could not be used at all — undecodable,
	// another instance's, or failing Put's rule. They are left in place: the
	// file is the only evidence a human has of what the node believes.
	Skipped []string
	// Temporaries is how many half-written records a crashed Put left behind.
	// They are removed, because a temporary has no identity to tear down.
	Temporaries int
}

// Recover is the load-time pass over the registry: the node runs it once when
// it opens the store. It reports every record whose VM is gone and removes the
// temporaries a crash left behind, and it changes nothing else — live records
// stay, unusable ones stay for inspection, and stale ones stay until their
// resources are torn down (see Recovery.Stale).
//
// It is idempotent, and it is not required for correctness: Get and List refuse
// a dead record on their own, so forgetting to run Recover costs a cleanup
// report, never a dangling resolution. A cleanup pass that runs later than
// startup calls it again and gets the same stale records, because Recover leaves
// them in place. One failure does not stop the scan — the entries after it still
// go, and the first failure is returned so a caller can log that the registry may
// still hold what it asked to remove.
func (s *Store) Recover() (Recovery, error) {
	var report Recovery
	if err := s.usable(); err != nil {
		return report, err
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return report, fmt.Errorf("instance registry: read %s: %w", s.dir, err)
	}

	var firstErr error
	removedTemp := false
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			continue
		}

		switch {
		case strings.HasPrefix(name, tempPrefix) && strings.HasSuffix(name, tempSuffix):
			path := filepath.Join(s.dir, name)
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				if firstErr == nil {
					firstErr = fmt.Errorf("instance registry: remove the half-written record %s: %w", path, err)
				}
				continue
			}
			report.Temporaries++
			removedTemp = true
		case strings.HasSuffix(name, recordSuffix):
			id := strings.TrimSuffix(name, recordSuffix)
			inst, found, err := s.read(id)
			if err != nil {
				report.Skipped = append(report.Skipped, filepath.Join(s.dir, name))
				continue
			}
			if found && !s.alive(inst.PID) {
				report.Stale = append(report.Stale, inst)
			}
		}
	}

	if removedTemp {
		syncDir(s.dir)
	}
	sort.Slice(report.Stale, func(i, j int) bool { return report.Stale[i].ID < report.Stale[j].ID })
	sort.Strings(report.Skipped)
	return report, firstErr
}

// read decodes one record and holds it to the rule Put enforces. found is false
// when the file is not there, which is the ordinary "this instance was never
// recorded here" answer; an error means the file is there and cannot be used —
// it cannot be read, it does not decode, it names another instance, or it
// describes no VM — and the file is left in place. This is the one read
// sequence behind Get, List and Recover: a second copy would drift, and the
// copy that drifts is the one that resolves a record the node cannot use.
func (s *Store) read(id string) (Instance, bool, error) {
	path := recordPath(s.dir, id)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Instance{}, false, nil
		}
		return Instance{}, false, fmt.Errorf("instance record for %q: read %s: %w", id, path, err)
	}

	var inst Instance
	if err := json.Unmarshal(data, &inst); err != nil {
		return Instance{}, false, fmt.Errorf("instance record %s is corrupt: %w (left in place for inspection)", path, err)
	}
	if inst.ID != id {
		return Instance{}, false, fmt.Errorf("instance record %s belongs to %q, not %q", path, inst.ID, id)
	}
	// Decoding is not evidence of a usable record: a hand-edited file, or one
	// written by another version, can decode and still describe no VM.
	// Resolving it would hand a caller a socket that is not this instance's, so
	// it is refused by the same rule Put uses — and the file stays, like a
	// corrupt one.
	if err := inst.validate(); err != nil {
		return Instance{}, false, fmt.Errorf("instance record %s is not valid: %w (left in place for inspection)", path, err)
	}
	return inst, true, nil
}

// usable reports whether the store was built by NewStore. A zero-value store
// has neither a directory nor a liveness check, and every operation would
// otherwise address the working directory.
func (s *Store) usable() error {
	if s == nil || strings.TrimSpace(s.dir) == "" || s.alive == nil {
		return errors.New("instance registry: use NewStore")
	}
	return nil
}

// recordPath is the record file for an instance: <dir>/<id>.json. Callers
// validate the ID first: it becomes a file name, so a separator or a dot would
// escape the directory.
func recordPath(dir, id string) string {
	return filepath.Join(dir, id+recordSuffix)
}

// syncDir flushes a directory after an entry in it changed (Put's rename, a
// removal), so the entry survives a crash. It is best effort: the record itself
// is already complete at this point, and a filesystem that cannot sync a
// directory (or a race with another process removing it) must not turn a
// successful change into an error.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
