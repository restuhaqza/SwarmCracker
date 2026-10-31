package broker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/rs/zerolog/log"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// vmSocketSuffix is the Firecracker API socket suffix the executor uses for a
// task (see pkg/runtime: <socketDir>/<taskID>.sock).
const vmSocketSuffix = ".sock"

// consoleLogSuffix is the console log suffix the executor uses for a task (see
// pkg/runtime: <logDir>/<taskID>.log). The Firecracker console merges stdout and
// stderr into this one file.
const consoleLogSuffix = ".log"

// consoleSocketSuffix is the interactive console socket suffix pkg/console
// creates for a running VM: <socketDir>/<taskID>.console.sock.
const consoleSocketSuffix = ".console.sock"

// Task is one local microVM the node broker can route to.
type Task struct {
	ID         string
	SocketPath string // Firecracker API socket, e.g. /var/run/firecracker/<id>.sock
	// Running reports whether the task's microVM was alive when the task was
	// resolved: true when its Firecracker API socket exists. A task that has
	// exited still keeps its console log, so it resolves with Running false and
	// no SocketPath or ConsolePath; logs remain readable, exec, files and attach
	// do not.
	Running bool
	// LogPath is the task's Firecracker console log, e.g.
	// /var/run/swarmcracker/<id>.log. It is empty when the source has no log
	// directory; the log handler then answers not_found rather than guessing.
	LogPath string
	// ConsolePath is the task's interactive console Unix socket, e.g.
	// /var/run/firecracker/<id>.console.sock. It is derived for a running task
	// and stays empty for a log-only one: a stopped VM has no console to attach
	// to, and the attach handler owns the not-found answer.
	ConsolePath string

	// restoredVsockPath is the muxer a restored task's VM answers on, set by the
	// resolver from the task's restored-instance record. A migration moves a VM
	// whose Firecracker state names the source task's muxer, so the path cannot
	// be derived from this task's ID; it is kept private because only the
	// resolver may say which VM a task runs — a caller that filled it in by hand
	// would be re-deriving host-side identity from the task ID, which is the
	// assumption a restored task breaks.
	restoredVsockPath string
	// restoredVMID is the task whose VM this task runs, i.e. the task whose
	// Firecracker state names the TAP device, the vsock muxer and the disk. It is
	// empty for a task this node booted itself, whose identity is its own ID.
	restoredVMID string
	// restoredDiskPath is the disk the restored VM's state file names. An export
	// must copy that file: <RootfsDir>/tasks/<this task's ID>.ext4 belongs to no
	// VM on a restored task, and a restore would load a state file naming a disk
	// whose bytes the artifact does not carry.
	restoredDiskPath string
}

// sourceVMIdentity answers what an export of this task has to name: the task whose
// ID the VM's host-side devices are derived from, the directory a vsock path for
// that ID is derived in, and the disk the state file records. For a task this node
// booted itself every answer is derived from the task, which is the rule the export
// has always applied; a restored task's VM belongs to the source task, so its
// record answers instead.
func (t Task) sourceVMIdentity(rootfsDir string) (vmID, socketDir, diskPath string) {
	if t.restoredVMID != "" {
		return t.restoredVMID, filepath.Dir(t.restoredVsockPath), t.restoredDiskPath
	}
	return t.ID, filepath.Dir(t.SocketPath), filepath.Join(rootfsDir, "tasks", t.ID+".ext4")
}

// VsockPath returns the Firecracker vsock muxer path for the task: the recorded
// muxer for a restored task, otherwise <socketDir>/<id>.sock ->
// <socketDir>/<id>.vsock. It returns "" when the task has no socket path or no
// ID, so a caller can distinguish "no VM" from a path that happens to live in the
// current directory.
func (t Task) VsockPath() string {
	if t.restoredVsockPath != "" {
		return t.restoredVsockPath
	}
	if t.SocketPath == "" || t.ID == "" {
		return ""
	}
	return scruntime.VsockPath(filepath.Dir(t.SocketPath), t.ID)
}

// guestAvailable reports whether the task has a live VM whose guest agent can
// be dialed. Exec and files answer not_found for a task that is not running: a
// stopped task has no vsock path to dial, and dialing an empty or recycled path
// would be wrong twice over.
func (t Task) guestAvailable() bool {
	return t.Running && t.VsockPath() != ""
}

// consoleAvailable reports whether the task has a console to attach to. Only a
// running task keeps one: a stopped task's console socket is removed when the
// console server closes, so the attach handler answers not_found like exec does.
func (t Task) consoleAvailable() bool {
	return t.Running && t.ConsolePath != ""
}

// stillRunning reports whether the task's microVM is still alive. The console
// log cannot answer that: the executor closes it but never removes it, so it
// outlives the task. The Firecracker API socket can, because it is removed when
// the VMM exits. Running is the snapshot SocketDirSource took at lookup; the
// stat is what makes a task that exits mid-stream observable.
func (t Task) stillRunning() bool {
	if !t.Running || t.SocketPath == "" {
		return false
	}
	info, err := os.Stat(t.SocketPath)
	return err == nil && !info.IsDir()
}

// TaskSource resolves a task ID to a local task. A running task has a live
// Firecracker socket; a task that has exited but whose console log is still on
// disk resolves with Running false, no SocketPath and LogPath set, so its logs
// stay readable while exec and files refuse it. A missing task is
// (zero, false, nil); a lookup failure is (zero, false, err).
type TaskSource interface {
	Lookup(ctx context.Context, taskID string) (Task, bool, error)
}

// RestoredInstanceSource is the node's map from a task to the microVM it
// inherited from a migration. *runtime.RestoredRegistry is the production
// implementation; the interface keeps the resolver testable without a filesystem.
//
// Its Get contract is the registry's: a task this node never restored is
// (zero, false, nil), and a record that exists but cannot be used is
// (zero, false, err) with the file left in place.
type RestoredInstanceSource interface {
	Get(taskID string) (scruntime.RestoredInstance, bool, error)
}

// SocketDirSource resolves <dir>/<taskID>.sock, <dir>/<taskID>.console.sock for
// a running task and, when LogDir is set, <logDir>/<taskID>.log. When the socket
// is gone but the log file remains, the task still resolves as a log-only task,
// so a completed task's console output is reachable through the API. It rejects
// empty IDs and any ID containing a path separator or a dot: task IDs come from
// an untrusted URL path, and without that check ".." (or "a/../../b") would
// address a file outside the socket directory.
type SocketDirSource struct {
	Dir string
	// LogDir holds the per-task console logs. Empty leaves Task.LogPath empty:
	// a broker that does not know the log directory must not read a path
	// relative to its working directory.
	LogDir string
	// IsRunning, when set, decides whether an existing socket file belongs to a
	// live VMM. A crashed Firecracker leaves its socket file behind, and a bare
	// stat would then report the dead task as running: exec would dial a dead
	// vsock path (503 instead of 404) and restore would refuse a snapshot for a
	// task that no longer exists. Nil keeps the stat-only behaviour tests and
	// embedders rely on; the daemon wires a process probe.
	IsRunning func(socketPath string) bool
	// Restored, when set, is the node's store of restored-instance records: a
	// task SwarmKit started from a migration artifact runs a VM whose TAP device,
	// vsock muxer and disk belong to the source task, so its host-side identity
	// cannot be derived from its own ID. A record wins over the derivation below,
	// including its "not running" answer (see restoredTask). Nil, or a task with
	// no record, resolves exactly as before.
	//
	// A typed-nil store (*runtime.RestoredRegistry)(nil) is not the same as nil:
	// it would answer every lookup with an error. The daemon assigns this field
	// only once the registry was opened.
	Restored RestoredInstanceSource

	// staleWarned latches the one warning a stale restored-instance record
	// produces, so a task whose VM is gone is reported once rather than on every
	// exec, logs and health request that resolves it. A source built as a literal
	// has nowhere to keep it and warns on every lookup; NewSocketDirSource is the
	// construction the daemon uses.
	staleWarned *staleRecordWarnings
}

// staleRecordWarnings is a set of the restored-instance records that have already
// been reported stale, keyed by the record's identity: its task and the socket it
// named. It is a pointer because SocketDirSource is a value copied per lookup, and
// small because a lookup is one map access.
type staleRecordWarnings struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// first reports whether this is the first time the record is seen stale, and
// remembers it.
func (w *staleRecordWarnings) first(taskID, socketPath string) bool {
	key := taskID + "\x00" + socketPath

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, seen := w.seen[key]; seen {
		return false
	}
	if w.seen == nil {
		w.seen = make(map[string]struct{})
	}
	w.seen[key] = struct{}{}
	return true
}

// NewSocketDirSource builds the task source the daemon uses: the directories its
// sockets and logs live in, the probe that decides whether an existing socket
// belongs to a live VMM, and the node's restored-instance store.
//
// It differs from a struct literal in one way, which is why it exists: it latches
// the one warning a stale restored-instance record produces. Resolution runs for
// every exec, logs, files and health request, and a record whose VM is gone would
// otherwise repeat the same warning for as long as the record lives.
//
// restored is an interface; a nil *runtime.RestoredRegistry must reach it as a nil
// interface, which the caller does by assigning only when its registry is non-nil.
func NewSocketDirSource(dir, logDir string, isRunning func(socketPath string) bool, restored RestoredInstanceSource) SocketDirSource {
	return SocketDirSource{
		Dir:         dir,
		LogDir:      logDir,
		IsRunning:   isRunning,
		Restored:    restored,
		staleWarned: &staleRecordWarnings{},
	}
}

// Lookup returns the task whose API socket exists in the configured directory,
// or, once the VM has exited, the log-only task its console log still describes.
// A task with a restored-instance record is answered from that record instead.
func (s SocketDirSource) Lookup(_ context.Context, taskID string) (Task, bool, error) {
	if s.Dir == "" {
		return Task{}, false, fmt.Errorf("broker: socket directory must not be empty")
	}
	if err := validateTaskID(taskID); err != nil {
		return Task{}, false, err
	}

	// A restored task is resolved from its record before anything is derived: the
	// record is the only thing that says which VM this task runs.
	if s.Restored != nil {
		restored, found, handled, err := s.restoredTask(taskID)
		if handled {
			return restored, found, err
		}
	}

	socketPath := filepath.Join(s.Dir, taskID+vmSocketSuffix)
	info, err := os.Stat(socketPath)
	switch {
	case err == nil:
		if info.IsDir() {
			return Task{}, false, fmt.Errorf("task socket %s is a directory", socketPath)
		}
		if s.IsRunning == nil || s.IsRunning(socketPath) {
			return Task{
				ID:         taskID,
				SocketPath: socketPath,
				Running:    true,
				// Derived, not probed: a task resolves while its console log is
				// still being created, and the log handler owns the not-found
				// answer.
				LogPath:     s.logPath(taskID),
				ConsolePath: s.consolePath(taskID),
			}, true, nil
		}
		// The file is a leftover from a VMM that is no longer alive. Fall
		// through to the log-only answer: the console log is still the best
		// evidence of why it stopped.
	case !os.IsNotExist(err):
		return Task{}, false, fmt.Errorf("stat task socket %s: %w", socketPath, err)
	}

	// The VM is gone. Its console log outlives it, and "why did it crash" is
	// the primary reason to read one, so a leftover log still resolves the
	// task — as a log-only task that exec and files refuse.
	logPath := s.logPath(taskID)
	if logPath == "" {
		return Task{}, false, nil
	}
	//nolint:gosec // G703: the task ID was validated (no separators or dots) above.
	logInfo, err := os.Stat(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Task{}, false, nil
		}
		return Task{}, false, fmt.Errorf("stat task log %s: %w", logPath, err)
	}
	if !logInfo.Mode().IsRegular() {
		return Task{}, false, nil
	}

	return Task{ID: taskID, LogPath: logPath}, true, nil
}

// restoredTask answers a task from its restored-instance record. handled is false
// only when the node has no record for the task, which is what makes the caller
// fall through to the derived paths unchanged.
//
// The record's answer wins over the derivation, including when the recorded socket
// is gone: after a SwarmKit ID reuse another task's VM can occupy
// <socketDir>/<taskID>.sock, so falling back would address a VM that belongs to
// nobody the caller asked about. A record whose VM is gone therefore resolves as a
// log-only task — the console log is the new task's own and outlives its VM — and
// the one warning says why the task is not running.
func (s SocketDirSource) restoredTask(taskID string) (Task, bool, bool, error) {
	record, found, err := s.Restored.Get(taskID)
	if err != nil {
		// An unusable record names no VM this resolver may guess at: the derived
		// paths belong to a VM the record says nothing about.
		return Task{}, false, true, fmt.Errorf("resolve the restored instance of task %q: %w", taskID, err)
	}
	if !found {
		return Task{}, false, false, nil
	}

	if running, err := s.recordedSocketAlive(record.SocketPath); err != nil {
		return Task{}, false, true, fmt.Errorf("resolve the restored instance of task %q: %w", taskID, err)
	} else if !running {
		if s.staleWarned == nil || s.staleWarned.first(taskID, record.SocketPath) {
			log.Warn().
				Str("task_id", taskID).
				Str("socket", record.SocketPath).
				Str("vm_id", record.VMID).
				Msg("A restored-instance record names an API socket that is gone; reporting the task as not running and keeping the record for inspection")
		}
		return Task{ID: taskID, LogPath: s.logPath(taskID)}, true, true, nil
	}

	return Task{
		ID:         taskID,
		SocketPath: record.SocketPath,
		Running:    true,
		// The console log and the console socket are the new task's: the executor
		// starts Firecracker for it, and only the guest-side devices came from the
		// snapshot.
		LogPath:           s.logPath(taskID),
		ConsolePath:       s.consolePath(taskID),
		restoredVsockPath: record.VsockPath,
		// The VM's host-side identity belongs to the source task, so an export of
		// this task has to name that task's devices and copy the disk its state
		// file records.
		restoredVMID:     record.VMID,
		restoredDiskPath: record.DiskPath,
	}, true, true, nil
}

// recordedSocketAlive reports whether a restored VM's API socket is a live one:
// the file exists, is not a directory, and — when the daemon wired a probe — belongs
// to a running VMM. It differs from the derived-path branch on purpose: a stat error
// other than "not exist" there is a resolve failure, while here the record already
// said which file to look at, and an unreadable one is reported the same way.
func (s SocketDirSource) recordedSocketAlive(socketPath string) (bool, error) {
	info, err := os.Stat(socketPath)
	switch {
	case err == nil:
		if info.IsDir() {
			return false, nil
		}
		return s.IsRunning == nil || s.IsRunning(socketPath), nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, fmt.Errorf("stat the restored VM socket %s: %w", socketPath, err)
	}
}

// logPath derives a task's console log path, or "" when the source has no log
// directory. It never probes the file: the log handler owns the not-found
// answer.
func (s SocketDirSource) logPath(taskID string) string {
	if s.LogDir == "" {
		return ""
	}
	return filepath.Join(s.LogDir, taskID+consoleLogSuffix)
}

// consolePath derives a running task's console socket path. It never probes the
// file: a console that has not been created yet is the attach handler's
// not-found answer, not a lookup failure.
func (s SocketDirSource) consolePath(taskID string) string {
	if s.Dir == "" {
		return ""
	}
	return filepath.Join(s.Dir, taskID+consoleSocketSuffix)
}

// validateTaskID rejects the shapes that could escape the socket directory. The
// rule itself is runtime.CheckTaskID, shared with the node's restored-instance
// registry; only the sentence is the broker's, because this one is read by
// whoever typed the request URL.
func validateTaskID(taskID string) error {
	err := scruntime.CheckTaskID(taskID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, scruntime.ErrTaskIDEmpty):
		return errors.New("task ID must not be empty")
	case errors.Is(err, scruntime.ErrTaskIDPathSeparator):
		return fmt.Errorf("task ID %q must not contain a path separator", taskID)
	case errors.Is(err, scruntime.ErrTaskIDDot):
		// SwarmKit task IDs are alphanumeric. A dot buys nothing and is the
		// building block of "." and "..", so refuse it outright.
		return fmt.Errorf("task ID %q must not contain a dot", taskID)
	default:
		return err
	}
}
