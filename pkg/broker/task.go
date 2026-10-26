package broker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
}

// VsockPath returns the Firecracker vsock muxer path for the task:
// <socketDir>/<id>.sock -> <socketDir>/<id>.vsock. It returns "" when the task
// has no socket path or no ID, so a caller can distinguish "no VM" from a path
// that happens to live in the current directory.
func (t Task) VsockPath() string {
	if t.SocketPath == "" || t.ID == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(t.SocketPath), t.ID+".vsock")
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
}

// Lookup returns the task whose API socket exists in the configured directory,
// or, once the VM has exited, the log-only task its console log still
// describes.
func (s SocketDirSource) Lookup(_ context.Context, taskID string) (Task, bool, error) {
	if s.Dir == "" {
		return Task{}, false, fmt.Errorf("broker: socket directory must not be empty")
	}
	if err := validateTaskID(taskID); err != nil {
		return Task{}, false, err
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

// validateTaskID rejects the shapes that could escape the socket directory.
func validateTaskID(taskID string) error {
	switch {
	case taskID == "":
		return fmt.Errorf("task ID must not be empty")
	case strings.ContainsAny(taskID, `/\`):
		return fmt.Errorf("task ID %q must not contain a path separator", taskID)
	case strings.Contains(taskID, "."):
		// SwarmKit task IDs are alphanumeric. A dot buys nothing and is the
		// building block of "." and "..", so refuse it outright.
		return fmt.Errorf("task ID %q must not contain a dot", taskID)
	}
	return nil
}
