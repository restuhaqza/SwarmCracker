package runtime

// The pending-restore directive is how the manager says, before the task exists,
// which artifact the next task of a service to start on this node restores.
//
// A live migration is a SwarmKit replacement: the manager stages an artifact on
// the target node and tells it "the next task of service S that starts here
// restores artifact A". SwarmKit creates the task later, and its ID is SwarmKit's
// to choose, so the directive is keyed by service rather than by task. The
// executor consumes it once, at task start, and a directive nobody claims expires.
//
// The record deliberately carries no artifact. pkg/migration imports pkg/runtime,
// so a record that described a manifest would invert that dependency; instead it
// names the staging directory the manager wrote the artifact's files into
// (<migration root>/<artifactID> on this node) and the executor reads the manifest
// from there, once. The directory is node-local state, like every other path a
// record here holds, so the record never leaves the node.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// PendingRestoreDirName is the subdirectory of the node's state directory that
	// holds one directive file per service. It is a directory of its own so that a
	// daemon restart finds exactly the directives it wrote, and so that clearing
	// them is one directory rather than a pattern over the rest of the state.
	PendingRestoreDirName = "pending-restores"

	// DefaultPendingRestoreTTL is how long a directive may wait for the task that
	// claims it. A prepare is sent while the manager stages the artifact and
	// replaces the task, which is seconds; ten minutes is long enough for a slow
	// artifact transfer and a scheduling delay, and short enough that a directive
	// whose migration was abandoned cannot restore a stale artifact later. A
	// directive older than this is ignored by Take and removed by Sweep.
	DefaultPendingRestoreTTL = 10 * time.Minute
)

const (
	// pendingRecordSuffix is the extension of one directive: <serviceId>.json. The
	// service ID is the file name because the lookup is by service and nothing
	// else, and the extension keeps a record apart from the claim below.
	pendingRecordSuffix = ".json"

	// pendingClaimSuffix is the extension of the file Take renames a directive to
	// while it reads it. A claim does not end in the record suffix, so List never
	// reports one and a crash mid-take leaves a file a human can tell apart from a
	// directive that is still waiting to be claimed.
	pendingClaimSuffix = ".taken"

	// pendingProbePattern names the throwaway file NewPendingRestores writes to
	// prove the directory accepts one. Like the restored registry's probe it starts
	// with a dot and never ends in a record suffix, so a crash cannot leave
	// something that reads as a directive.
	pendingProbePattern = ".pending-restores-writable.probe-*"
)

// PendingRestore is the node-local directive the manager prepares on the target of
// a migration. It is an internal record, not a public document: it names a
// node-local directory and never leaves the node, so it has no v1 envelope.
type PendingRestore struct {
	// ServiceID is the service whose next task on this node claims the directive.
	// It names the record file, and it is why one directive per service per node is
	// the whole index: the task's own ID does not exist yet when it is written.
	ServiceID string `json:"serviceId"`
	// ArtifactID is the artifact the claiming task restores. The executor records
	// it on the restored instance, so a running VM can be traced to its snapshot.
	ArtifactID string `json:"artifactId"`
	// Dir is the staging directory the manager wrote the artifact's files into on
	// this node: <migration root>/<artifactID>. It is absolute and node-local, and
	// the executor reads artifact.json and the files it describes from it. The
	// manager derives it from this node's migration root, so the record carries the
	// path instead of a rule that would have to guess the root.
	Dir string `json:"dir"`
	// CreatedAt is when the directive was prepared. It is set by the caller (the
	// broker's prepare, or the manager on the node's behalf), not by the registry,
	// so a retried prepare does not silently move the age of a directive.
	CreatedAt time.Time `json:"createdAt"`
}

// PendingRestoreDir returns the directive directory inside a node state directory.
// It returns "" for an empty (or blank) state directory, so a caller can tell "no
// state directory configured" from a path that happens to point at the current
// directory.
func PendingRestoreDir(stateDir string) string {
	if strings.TrimSpace(stateDir) == "" {
		return ""
	}
	return filepath.Join(stateDir, PendingRestoreDirName)
}

// PendingRestores is the node's store of pending-restore directives.
//
// The filesystem is the state, exactly like the restored-instance registry: a
// directive written by the process that received a prepare is visible to the
// process that consumes it, with no reconciliation step in between, and every
// operation is a single directory or file syscall on a path derived from the
// service ID. Put replaces a directive by atomic rename; Take claims one by
// renaming it to a name of its own, which is what makes exactly one of two tasks
// starting at the same instant consume it.
type PendingRestores struct {
	dir string
}

// NewPendingRestores opens (creating when needed) a directive directory and proves
// it can be written to. The directory is 0700 and the directives inside it are
// 0600: they name host paths and say which artifact a node is about to restore.
//
// Creating the directory is not enough to know it is usable: MkdirAll succeeds on
// an existing directory whatever its mode or mount, and a prepare arrives after the
// manager has already staged the artifact here. So the directory is probed with a
// real write now, while the daemon can still refuse to start.
func NewPendingRestores(dir string) (*PendingRestores, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("pending-restore registry: a directory is required")
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("pending-restore registry: create %s: %w", dir, err)
	}
	if err := probeWritableAs("pending-restore registry", pendingProbePattern, dir, os.CreateTemp, os.Remove); err != nil {
		return nil, err
	}
	return &PendingRestores{dir: dir}, nil
}

// Put records a directive for one service, replacing any directive already waiting
// for it. The record is validated first (an unusable directive is refused before
// anything touches the disk), then written to a temporary file in the same
// directory, fsynced, and renamed into place: a reader never observes a partial
// record, and a failed write leaves no record at all.
//
// Replacing is deliberate: the manager retries a prepare it never saw the answer
// to, and the second directive describes the same migration. Two directives for one
// service would let a task restore the artifact the manager has already abandoned.
func (p *PendingRestores) Put(record PendingRestore) error {
	if err := p.usable(); err != nil {
		return err
	}
	if err := record.validate(); err != nil {
		return fmt.Errorf("pending-restore directive for service %q: %w", record.ServiceID, err)
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("pending-restore directive for service %q: encode: %w", record.ServiceID, err)
	}

	// A unique temporary name: two concurrent Puts for one service must not write
	// through the same file. The "." prefix and the ".tmp" suffix keep the file out
	// of the record glob and out of a claim's shape.
	tmp, err := os.CreateTemp(p.dir, "."+record.ServiceID+"-*.tmp")
	if err != nil {
		return fmt.Errorf("pending-restore directive for service %q: create temporary file: %w", record.ServiceID, err)
	}
	tmpName := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		discard()
		return fmt.Errorf("pending-restore directive for service %q: write %s: %w", record.ServiceID, tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return fmt.Errorf("pending-restore directive for service %q: sync %s: %w", record.ServiceID, tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("pending-restore directive for service %q: close %s: %w", record.ServiceID, tmpName, err)
	}
	if err := os.Rename(tmpName, p.path(record.ServiceID)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("pending-restore directive for service %q: rename into place: %w", record.ServiceID, err)
	}

	syncDir(p.dir)
	return nil
}

// Take consumes the directive waiting for a service, if any. found is false for a
// service nothing was prepared for, and for a directive past DefaultPendingRestoreTTL
// - which is deleted rather than returned, because a migration whose transfer took
// longer than the TTL has been abandoned or retried.
//
// The claim is what makes Take atomic: the directive is renamed to a name only this
// caller knows before it is read, so of two tasks starting at the same instant
// exactly one renames it and the other answers not-found. Renaming first also means
// a directive is consumed at most once even if this process dies mid-read.
//
// A record that cannot be used - it does not decode, it names another service, or
// it fails the same rule Put applies - is an error, and the file is put back so a
// human can read what the node believes; List reports it by path. The caller
// decides what to log: the executor treats an unusable directive as "nothing to
// restore" and starts the task cold, and a startup reconciliation warns.
func (p *PendingRestores) Take(serviceID string) (PendingRestore, bool, error) {
	var none PendingRestore

	if err := p.usable(); err != nil {
		return none, false, err
	}
	if err := idProblem("serviceId", "service", serviceID); err != nil {
		return none, false, err
	}

	path := p.path(serviceID)
	claim, err := p.claim(serviceID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return none, false, nil
		}
		return none, false, err
	}

	record, err := readPendingDirective(claim, path, serviceID)
	if err != nil {
		// Put it back: a record this build cannot use is evidence of what the node
		// believes, not something a failed take may destroy. A rename that fails
		// leaves the claim on disk, which is still the same bytes under a name a
		// human can find.
		if restoreErr := os.Rename(claim, path); restoreErr != nil {
			return none, false, fmt.Errorf("%w (the directive could not be moved back from %s: %w)",
				err, claim, restoreErr)
		}
		return none, false, err
	}

	if err := os.Remove(claim); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// The directive is already consumed; failing here must not resurrect it.
		return none, false, fmt.Errorf(
			"pending-restore directive for service %q: remove the claim %s: %w", serviceID, claim, err)
	}
	syncDir(p.dir)

	if expired(record, time.Now()) {
		return none, false, nil
	}
	return record, true, nil
}

// Delete removes a service's directive. A missing directive is success: a rollback,
// an abandoned migration and a retried cleanup all run it, and none of them may
// fail because another ran first. A corrupt record is removed without being
// decoded, so a bad file cannot wedge a cleanup.
func (p *PendingRestores) Delete(serviceID string) error {
	if err := p.usable(); err != nil {
		return err
	}
	if err := idProblem("serviceId", "service", serviceID); err != nil {
		return err
	}

	path := p.path(serviceID)
	err := os.Remove(path)
	switch {
	case err == nil:
		// Put flushes the directory after its rename so the entry survives a crash;
		// a removal needs the same flush, or a crash can bring back a directive the
		// manager has already abandoned.
		syncDir(p.dir)
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("pending-restore directive for service %q: delete %s: %w", serviceID, path, err)
	}
}

// List returns every directive that decodes and validates, sorted by service ID,
// plus the paths of the records it could not use, so a startup reconciliation is
// deterministic and complete. An unusable record is skipped rather than failing the
// listing: one bad file must not hide the node's other directives, and the paths
// are what the daemon warns about once - without deleting them, because the file is
// the only evidence a human has of what the node believes.
//
// Age is not a usability failure: an expired directive is still what the node
// believes until Sweep removes it, and List shows it so a stale prepare is visible
// rather than silently absent. Take is what refuses it.
func (p *PendingRestores) List() ([]PendingRestore, []string, error) {
	if err := p.usable(); err != nil {
		return nil, nil, err
	}

	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return nil, nil, fmt.Errorf("pending-restore registry: read %s: %w", p.dir, err)
	}

	out := make([]PendingRestore, 0, len(entries))
	var skipped []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, pendingRecordSuffix) {
			continue
		}
		// The entry's own path, not one derived from a service ID: a file name that
		// is not a valid service ID is exactly one of the cases reported here.
		path := filepath.Join(p.dir, name)
		record, err := readPendingDirective(path, path, strings.TrimSuffix(name, pendingRecordSuffix))
		if err != nil {
			skipped = append(skipped, path)
			continue
		}
		out = append(out, record)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ServiceID < out[j].ServiceID })
	sort.Strings(skipped)
	return out, skipped, nil
}

// Sweep removes the directives and abandoned claims older than olderThan, and
// returns how many entries it removed. It is the housekeeping half of the TTL: Take
// refuses an expired directive but only meets one when a task starts, so a service
// that is never replaced again would leave its directive behind for ever.
//
// A record that cannot be decoded is left in place and not counted: Sweep is not
// the place that decides a node's belief is wrong, and List reports it. A negative
// age is a caller error rather than "sweep nothing" or "sweep everything"; zero is
// deliberate and sweeps every entry, so only a caller that has decided no task is
// starting may pass it.
//
// A claim is aged from the moment it was taken, not from when the directive was
// written (rename preserves the modification time), so a Sweep running beside a
// Take cannot remove the directive the Take is reading.
func (p *PendingRestores) Sweep(olderThan time.Duration) (int, error) {
	if err := p.usable(); err != nil {
		return 0, err
	}
	if olderThan < 0 {
		return 0, fmt.Errorf("pending-restore registry: sweep age %s is negative: pass how old an entry must be", olderThan)
	}

	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return 0, fmt.Errorf("pending-restore registry: read %s: %w", p.dir, err)
	}

	now := time.Now()
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(p.dir, name)

		switch {
		case strings.HasSuffix(name, pendingRecordSuffix):
			record, err := readPendingDirective(path, path, strings.TrimSuffix(name, pendingRecordSuffix))
			if err != nil {
				continue // reported by List; a human decides what it was
			}
			if !expiredFor(now, record.CreatedAt, olderThan) {
				continue
			}
		case strings.HasSuffix(name, pendingClaimSuffix):
			info, err := entry.Info()
			if err != nil || !expiredFor(now, info.ModTime(), olderThan) {
				continue
			}
		default:
			continue
		}

		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, fmt.Errorf("pending-restore registry: remove %s: %w", path, err)
		}
		removed++
	}

	if removed > 0 {
		syncDir(p.dir)
	}
	return removed, nil
}

// claim renames the directive for a service to a name of this caller's own, and
// returns that name. Creating the temporary first is what makes the name unique
// without a lock: two Takes create two files, and only one rename of the directive
// onto its own file succeeds - the other finds the directive gone.
func (p *PendingRestores) claim(serviceID string) (string, error) {
	tmp, err := os.CreateTemp(p.dir, "."+serviceID+"-*"+pendingClaimSuffix)
	if err != nil {
		return "", fmt.Errorf("pending-restore directive for service %q: create a claim: %w", serviceID, err)
	}
	claim := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(claim)
		return "", fmt.Errorf("pending-restore directive for service %q: close the claim %s: %w", serviceID, claim, err)
	}

	path := p.path(serviceID)
	if err := os.Rename(path, claim); err != nil {
		_ = os.Remove(claim)
		if errors.Is(err, fs.ErrNotExist) {
			return "", fs.ErrNotExist
		}
		return "", fmt.Errorf("pending-restore directive for service %q: claim %s: %w", serviceID, path, err)
	}

	// Age the claim from now: rename keeps the directive's modification time, and
	// Sweep must not remove a claim while this Take is reading it.
	now := time.Now()
	_ = os.Chtimes(claim, now, now)
	return claim, nil
}

// readPendingDirective reads and validates one directive file. name is the path the
// error text uses: Take reads a claim but reports the directive's own file, which is
// where it puts the record back. wantService is the service the file name claims, and
// a record naming another one is refused rather than returned: the file is named after
// the service whose next task will claim it, so a mismatch means the file is not a
// directive this node wrote.
func readPendingDirective(path, name, wantService string) (PendingRestore, error) {
	var record PendingRestore

	//nolint:gosec // G304: the path is one entry of this node's directive directory.
	data, err := os.ReadFile(path)
	if err != nil {
		return record, fmt.Errorf("pending-restore directive %s: read: %w", name, err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, fmt.Errorf("pending-restore directive %s is corrupt: %w (left in place for inspection)", name, err)
	}
	if record.ServiceID != wantService {
		return record, fmt.Errorf("pending-restore directive %s belongs to service %q, not %q",
			name, record.ServiceID, wantService)
	}
	// Decoding is not evidence of a usable directive: a hand-edited file, or one
	// written by another version, can decode and still name no artifact or a
	// directory the executor cannot use.
	if err := record.validate(); err != nil {
		return record, fmt.Errorf("pending-restore directive %s is not valid: %w (left in place for inspection)", name, err)
	}
	return record, nil
}

// path is the directive file for a service. Callers validate the service ID first:
// it becomes a file name, so a separator or a dot would escape the directory.
func (p *PendingRestores) path(serviceID string) string {
	return filepath.Join(p.dir, serviceID+pendingRecordSuffix)
}

// usable reports whether the registry was built by NewPendingRestores. A zero-value
// registry has no directory, and every operation would otherwise address the
// working directory.
func (p *PendingRestores) usable() error {
	if p == nil || strings.TrimSpace(p.dir) == "" {
		return errors.New("pending-restore registry: use NewPendingRestores")
	}
	return nil
}

// validate refuses a directive the executor could not act on: it must name a
// service, an artifact and the absolute directory the artifact was staged in, and
// say when it was prepared so the TTL can be judged. The artifact ID's shape is the
// broker's rule (migration.ValidArtifactID): this package holds the record, not the
// naming rule, and it is the broker that accepts the ID from an untrusted body.
func (r PendingRestore) validate() error {
	if err := idProblem("serviceId", "service", r.ServiceID); err != nil {
		return err
	}
	if strings.TrimSpace(r.ArtifactID) == "" {
		return errors.New("artifactId is empty: a pending restore must name the artifact to restore")
	}

	switch {
	case strings.TrimSpace(r.Dir) == "":
		return errors.New("dir is empty: a pending restore must name the directory the artifact was staged in")
	case !filepath.IsAbs(r.Dir):
		return fmt.Errorf("dir %q is relative: want the absolute staging directory <migration root>/<artifactID>", r.Dir)
	}

	if r.CreatedAt.IsZero() {
		return errors.New("createdAt is zero: set it to the instant the directive was prepared")
	}
	return nil
}

// expired reports whether a directive is past the TTL and must not be claimed.
func expired(record PendingRestore, now time.Time) bool {
	return expiredFor(now, record.CreatedAt, DefaultPendingRestoreTTL)
}

// expiredFor reports whether an entry stamped at createdAt is older than age. A
// timestamp in the future (a clock that stepped back between the write and the
// read) is not expired: refusing it would strand a directive the manager just
// prepared.
func expiredFor(now, createdAt time.Time, age time.Duration) bool {
	return now.Sub(createdAt) > age
}
