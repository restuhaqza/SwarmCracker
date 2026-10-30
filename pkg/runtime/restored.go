package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The restored-instance record maps a task SwarmKit started from a migration
// artifact to the microVM that is actually running it.
//
// A migration is a SwarmKit replacement: the target node starts a *new* task and
// loads the source VM's snapshot under it. Firecracker's state file keeps the
// source task's host-side identity — the TAP device name, the vsock muxer path
// and the disk path — because the guest resumed mid-flight and those names are
// what its device model was configured with. Only the API socket and the console
// log are chosen fresh for the new task.
//
// So a restored task breaks the tree's usual rule that every host-side name is a
// pure function of the task ID (see naming.go). The node has to remember, per
// new task, which source VM it inherited, or exec would dial a vsock muxer that
// does not exist, logs would read a console nobody wrote, and stop would kill
// nothing while leaving the real VM running.

// RestoredInstanceDirName is the subdirectory of the node's state directory that
// holds one record file per restored task. It is a subdirectory of its own so
// that a daemon restart (or a reboot) finds exactly the records it wrote, and so
// that removing them is one directory, not a pattern match over the state file.
const RestoredInstanceDirName = "restored-instances"

// restoredRecordSuffix is the extension of one record: <taskId>.json. The task
// ID is the file name because a lookup is by task ID and nothing else.
const restoredRecordSuffix = ".json"

// restoredProbePattern names the throwaway file NewRestoredRegistry writes to
// prove the record directory accepts one. The leading dot and the ".probe"
// element keep it out of the ".json" record glob even if a crash leaves one
// behind, and the name says what wrote it.
const restoredProbePattern = ".restored-instances-writable.probe-*"

// RestoredInstance is the node-local record of one task started from a migration
// artifact. It is an internal record, not a public document: it holds node-local
// paths and never leaves the node, so it has no v1 envelope.
type RestoredInstance struct {
	// TaskID is the new task SwarmKit created for the move. It names the
	// record file, owns the API socket and the console log, and is what the
	// broker is asked about.
	TaskID string `json:"taskId"`
	// VMID is the source task whose VM this task is running. Firecracker named
	// the TAP device, the vsock muxer and the block device after it; nothing
	// may re-derive those names from TaskID.
	VMID string `json:"vmId"`
	// ArtifactID is the migration artifact the VM was restored from, kept so a
	// record can be traced back to the snapshot that produced it.
	ArtifactID string `json:"artifactId"`
	// SocketPath is the restored VM's Firecracker API socket. It belongs to the
	// new task (<socketDir>/<taskId>.sock): the socket is created when
	// Firecracker starts, so it is not part of the snapshot.
	SocketPath string `json:"socketPath"`
	// TapName is the TAP device recorded in the snapshot, i.e. the source
	// task's device. A restored VM has no other interface.
	TapName string `json:"tapName"`
	// VsockPath is the vsock muxer path recorded in the snapshot, i.e. the
	// source task's path. It is where the guest agent answers.
	VsockPath string `json:"vsockPath"`
	// DiskPath is the block device path recorded in the snapshot, where the
	// target materialised the delivered disk.
	DiskPath string `json:"diskPath"`
	// CreatedAt is when the restore completed and this record was written. It
	// is set by the caller (the import), not by the registry, so a retry does
	// not silently move the age of a VM.
	CreatedAt time.Time `json:"createdAt"`
}

// RestoredInstanceDir returns the record directory inside a node state
// directory. It returns "" for an empty (or blank) state directory, so a caller
// can tell "no state directory configured" from a path that happens to point at
// the current directory.
func RestoredInstanceDir(stateDir string) string {
	if strings.TrimSpace(stateDir) == "" {
		return ""
	}
	return filepath.Join(stateDir, RestoredInstanceDirName)
}

// RestoredRegistry is the node's store of restored-instance records.
//
// The filesystem is the state: there is no in-memory map, so a record written by
// the process that restored a VM is visible to the process that restarts after
// it, with no reconciliation step in between. Every operation is a single
// directory or file syscall on a path derived from the task ID, and Put replaces
// a record by atomic rename, so concurrent readers see either the old record or
// the new one. That is why the registry needs no lock: two Put calls for the same
// task write different temporary files and the last rename wins, and that is the
// only order either caller could have got from a lock anyway.
type RestoredRegistry struct {
	dir string
}

// NewRestoredRegistry opens (creating when needed) a record directory and proves
// it can be written to. The directory is 0700 and the records inside it are
// 0600: they name host paths and say which VMs a node is running, which is not
// world-readable information. A daemon that cannot create its record directory
// is a daemon that could restore a VM it would later fail to resolve, so the
// error is returned rather than swallowed.
//
// Creating the directory is not enough to know that: MkdirAll succeeds on an
// existing directory whatever its mode or mount (a read-only remount, a full
// filesystem), and Put runs at the very end of an import, after the VM is
// already running. So the directory is probed with a real write here, while the
// daemon can still refuse to start.
func NewRestoredRegistry(dir string) (*RestoredRegistry, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("restored-instance registry: a directory is required")
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("restored-instance registry: create %s: %w", dir, err)
	}
	if err := probeWritable(dir); err != nil {
		return nil, err
	}
	return &RestoredRegistry{dir: dir}, nil
}

// probeWritable proves the record directory accepts a new file right now: it
// creates a clearly named temp file and removes it. A directory that cannot do
// that cannot keep the record of a restored VM, and a node that cannot keep the
// record must not start believing it can.
func probeWritable(dir string) error {
	return probeWritableWith(dir, os.CreateTemp, os.Remove)
}

// probeWritableWith is probeWritable with its two filesystem steps injected, so
// a test running as root — where a 0500 directory is still writable — can prove
// both failures are reported.
func probeWritableWith(dir string, createTemp func(string, string) (*os.File, error), remove func(string) error) error {
	probe, err := createTemp(dir, restoredProbePattern)
	if err != nil {
		return fmt.Errorf("restored-instance registry: %s is not writable: %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	if err := remove(name); err != nil {
		return fmt.Errorf("restored-instance registry: %s is not writable: remove the probe file %s: %w", dir, name, err)
	}
	return nil
}

// Put writes the record for one restored task, replacing any record already
// there. The record is validated first (an unusable record is refused before
// anything touches the disk), then written to a temporary file in the same
// directory, fsynced, and renamed into place: a reader never observes a partial
// record, and a failed write leaves no record at all. A retried import records
// the same task again, which is why Put is a replace and not an insert.
func (r *RestoredRegistry) Put(instance RestoredInstance) error {
	if err := r.usable(); err != nil {
		return err
	}
	if err := instance.validate(); err != nil {
		return fmt.Errorf("restored-instance record for task %q: %w", instance.TaskID, err)
	}

	data, err := json.MarshalIndent(instance, "", "  ")
	if err != nil {
		return fmt.Errorf("restored-instance record for task %q: encode: %w", instance.TaskID, err)
	}

	// A unique temporary name: two concurrent Puts for one task must not write
	// through the same file. The "." prefix and the ".tmp" suffix keep the file
	// out of both the record glob and a human's listing.
	tmp, err := os.CreateTemp(r.dir, "."+instance.TaskID+"-*.tmp")
	if err != nil {
		return fmt.Errorf("restored-instance record for task %q: create temporary file: %w", instance.TaskID, err)
	}
	tmpName := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		discard()
		return fmt.Errorf("restored-instance record for task %q: write %s: %w", instance.TaskID, tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return fmt.Errorf("restored-instance record for task %q: sync %s: %w", instance.TaskID, tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("restored-instance record for task %q: close %s: %w", instance.TaskID, tmpName, err)
	}
	if err := os.Rename(tmpName, r.path(instance.TaskID)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("restored-instance record for task %q: rename into place: %w", instance.TaskID, err)
	}

	syncDir(r.dir)
	return nil
}

// Get returns the record for a task. found is false for a task this node never
// restored; a record that exists but cannot be used — it does not decode, it
// names another task, or it fails the same rule Put applies — is an error, and
// the file is left in place so a human can read what the node believes. The
// caller decides what to log: the broker treats an unusable record as "this task
// does not resolve here" and a startup reconciliation warns.
func (r *RestoredRegistry) Get(taskID string) (RestoredInstance, bool, error) {
	if err := r.usable(); err != nil {
		return RestoredInstance{}, false, err
	}
	if err := taskIDProblem("taskId", taskID); err != nil {
		return RestoredInstance{}, false, err
	}

	path := r.path(taskID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return RestoredInstance{}, false, nil
		}
		return RestoredInstance{}, false, fmt.Errorf("restored-instance record for task %q: read %s: %w", taskID, path, err)
	}

	var instance RestoredInstance
	if err := json.Unmarshal(data, &instance); err != nil {
		return RestoredInstance{}, false, fmt.Errorf(
			"restored-instance record %s is corrupt: %w (left in place for inspection)", path, err)
	}
	if instance.TaskID != taskID {
		return RestoredInstance{}, false, fmt.Errorf(
			"restored-instance record %s belongs to task %q, not %q", path, instance.TaskID, taskID)
	}
	// Decoding is not evidence of a usable record: a hand-edited file, or one
	// written by another version, can decode and still describe no VM. Resolving
	// it would hand exec, logs and stop empty or foreign paths, so it is refused
	// by the same rule Put uses — and the file stays, like a corrupt one.
	if err := instance.validate(); err != nil {
		return RestoredInstance{}, false, fmt.Errorf(
			"restored-instance record %s is not valid: %w (left in place for inspection)", path, err)
	}
	return instance, true, nil
}

// Delete removes a task's record. A missing record is success: the task's stop
// path (executor teardown, a retried teardown, a task that never restored here)
// must not fail because the record is already gone. A corrupt record is removed
// without being decoded, so a bad file cannot wedge a stop.
func (r *RestoredRegistry) Delete(taskID string) error {
	if err := r.usable(); err != nil {
		return err
	}
	if err := taskIDProblem("taskId", taskID); err != nil {
		return err
	}

	path := r.path(taskID)
	err := os.Remove(path)
	switch {
	case err == nil:
		// Put flushes the directory after its rename so the entry survives a
		// crash; a removal needs the same flush. Without it a crash can bring
		// back the record of a VM that is gone, and the next teardown would
		// report work that is already done.
		syncDir(r.dir)
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("restored-instance record for task %q: delete %s: %w", taskID, path, err)
	}
}

// List returns every usable record sorted by task ID, plus the paths of the
// records it could not use, so a startup reconciliation is deterministic and
// complete. An unusable record (undecodable, another task's, or failing Put's
// rule) is skipped rather than failing the listing: one bad file must not hide
// the node's other restored VMs. The paths are what the daemon warns about once,
// and it warns without deleting them — the file is the only evidence a human
// has of what the node believes. Only a directory-level failure (an unreadable
// record directory) is an error.
func (r *RestoredRegistry) List() ([]RestoredInstance, []string, error) {
	if err := r.usable(); err != nil {
		return nil, nil, err
	}

	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, nil, fmt.Errorf("restored-instance registry: read %s: %w", r.dir, err)
	}

	out := make([]RestoredInstance, 0, len(entries))
	var skipped []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, restoredRecordSuffix) {
			continue
		}
		// The entry's own path, not one derived from a task ID: a file name
		// that is not a valid task ID is exactly one of the cases reported here.
		path := filepath.Join(r.dir, name)
		instance, found, err := r.Get(strings.TrimSuffix(name, restoredRecordSuffix))
		if err != nil {
			skipped = append(skipped, path)
			continue
		}
		if !found {
			continue
		}
		out = append(out, instance)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	sort.Strings(skipped)
	return out, skipped, nil
}

// path is the record file for a task. Callers validate the task ID first: it
// becomes a file name, so a separator or a dot would escape the directory.
func (r *RestoredRegistry) path(taskID string) string {
	return filepath.Join(r.dir, taskID+restoredRecordSuffix)
}

// usable reports whether the registry was built by NewRestoredRegistry. A
// zero-value registry has no directory, and every operation would otherwise
// address the working directory.
func (r *RestoredRegistry) usable() error {
	if r == nil || strings.TrimSpace(r.dir) == "" {
		return errors.New("restored-instance registry: use NewRestoredRegistry")
	}
	return nil
}

// validate refuses a record the resolver could not use. The checks are the ones
// that keep a restored task pointing at its own VM: the socket must be the new
// task's, while the TAP device and the vsock muxer must belong to the source
// task — a record naming another task's device would make stop tear down a VM
// that is still serving.
func (i RestoredInstance) validate() error {
	if err := taskIDProblem("taskId", i.TaskID); err != nil {
		return err
	}
	if err := taskIDProblem("vmId", i.VMID); err != nil {
		return err
	}
	if strings.TrimSpace(i.ArtifactID) == "" {
		return errors.New("artifactId is empty: a restored record must name the artifact the VM came from")
	}

	switch {
	case strings.TrimSpace(i.SocketPath) == "":
		return errors.New("socketPath is empty: a restored record must name the VM's API socket")
	case !filepath.IsAbs(i.SocketPath):
		return fmt.Errorf("socketPath %q is relative: want the absolute API socket path <socketDir>/<taskId>%s",
			i.SocketPath, VMSocketSuffix)
	case TaskIDFromSocketPath(i.SocketPath) != i.TaskID:
		return fmt.Errorf("socketPath %q is not the API socket of task %q: want <socketDir>/%s%s",
			i.SocketPath, i.TaskID, i.TaskID, VMSocketSuffix)
	}

	switch {
	case strings.TrimSpace(i.TapName) == "":
		return errors.New("tapName is empty: a restored record must name the TAP device the snapshot recorded")
	case !tapNameBelongsTo(i.TapName, i.VMID):
		// Report the index that was parsed: a record may carry any interface
		// index, so a valid non-zero one must not be told the name ends in 0.
		if idx, ok := tapNameIndex(i.TapName); ok {
			return fmt.Errorf("tapName %q is not a device of task %q: want %s, the name recorded in the snapshot",
				i.TapName, i.VMID, TapName(i.VMID, idx))
		}
		return fmt.Errorf("tapName %q is not a device of task %q: want a TAP name of the form <digest>-<interface index> recorded in the snapshot",
			i.TapName, i.VMID)
	}

	switch {
	case strings.TrimSpace(i.VsockPath) == "":
		return errors.New("vsockPath is empty: a restored record must name the vsock muxer the snapshot recorded")
	case !filepath.IsAbs(i.VsockPath):
		return fmt.Errorf("vsockPath %q is relative: want the absolute muxer path <socketDir>/<vmId>.vsock", i.VsockPath)
	case VsockPath(filepath.Dir(i.VsockPath), i.VMID) != i.VsockPath:
		return fmt.Errorf("vsockPath %q is not the vsock muxer of task %q: want %s (the path recorded in the snapshot)",
			i.VsockPath, i.VMID, VsockPath(filepath.Dir(i.VsockPath), i.VMID))
	}

	switch {
	case strings.TrimSpace(i.DiskPath) == "":
		return errors.New("diskPath is empty: a restored record must name the disk the snapshot's guest runs from")
	case !filepath.IsAbs(i.DiskPath):
		return fmt.Errorf("diskPath %q is relative: want the absolute block-device path recorded in the snapshot", i.DiskPath)
	}

	if i.CreatedAt.IsZero() {
		return errors.New("createdAt is zero: set it to the instant the restore completed")
	}
	return nil
}

// tapNameBelongsTo reports whether name is a TAP device of vmID. It parses the
// interface index out of the name and asks the one rule that owns the name
// (naming.go), rather than re-deriving the digest here.
func tapNameBelongsTo(name, vmID string) bool {
	idx, ok := tapNameIndex(name)
	if !ok {
		return false
	}
	return TapName(vmID, idx) == name
}

// tapNameIndex returns the trailing interface index of a TAP name.
func tapNameIndex(name string) (int, bool) {
	sep := strings.LastIndexByte(name, '-')
	if sep < 0 || sep == len(name)-1 {
		return 0, false
	}
	idx, err := strconv.Atoi(name[sep+1:])
	if err != nil || idx < 0 {
		return 0, false
	}
	return idx, true
}

// taskIDProblem refuses an ID that cannot name a record file, naming the field
// that is wrong. The rule is CheckTaskID — the same rule the broker applies to
// an untrusted URL path — so only the sentence differs between the two callers.
func taskIDProblem(field, id string) error {
	err := CheckTaskID(id)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrTaskIDEmpty):
		return fmt.Errorf("%s is empty: want a SwarmKit task ID", field)
	case errors.Is(err, ErrTaskIDPathSeparator):
		return fmt.Errorf("%s %q must not contain a path separator: a task ID is alphanumeric", field, id)
	case errors.Is(err, ErrTaskIDDot):
		return fmt.Errorf("%s %q must not contain a dot: a task ID is alphanumeric", field, id)
	default:
		return err
	}
}

// syncDir flushes a directory after an entry in it changed (Put's rename,
// Delete's removal), so the entry survives a crash. It is best effort: the
// record itself is already complete at this point, and a filesystem that cannot
// sync a directory (or a data race with another process removing it) must not
// turn a successful change into an error.
//
// It is a variable so a test can prove both Put and Delete call it: a directory
// fsync is not otherwise observable from a unit test.
var syncDir = func(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
