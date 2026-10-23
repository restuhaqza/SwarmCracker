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

// Task is one local microVM the node broker can route to.
type Task struct {
	ID         string
	SocketPath string // Firecracker API socket, e.g. /var/run/firecracker/<id>.sock
	// LogPath is the task's Firecracker console log, e.g.
	// /var/run/swarmcracker/<id>.log. It is empty when the source has no log
	// directory; the log handler then answers not_found rather than guessing.
	LogPath string
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

// TaskSource resolves a task ID to a running local task. A missing task is
// (zero, false, nil); a lookup failure is (zero, false, err).
type TaskSource interface {
	Lookup(ctx context.Context, taskID string) (Task, bool, error)
}

// SocketDirSource resolves <dir>/<taskID>.sock and, when LogDir is set,
// <logDir>/<taskID>.log. It rejects empty IDs and any ID containing a path
// separator or a dot: task IDs come from an untrusted URL path, and without that
// check ".." (or "a/../../b") would address a file outside the socket directory.
type SocketDirSource struct {
	Dir string
	// LogDir holds the per-task console logs. Empty leaves Task.LogPath empty:
	// a broker that does not know the log directory must not read a path
	// relative to its working directory.
	LogDir string
}

// Lookup returns the task whose API socket exists in the configured directory.
func (s SocketDirSource) Lookup(_ context.Context, taskID string) (Task, bool, error) {
	if s.Dir == "" {
		return Task{}, false, fmt.Errorf("broker: socket directory must not be empty")
	}
	if err := validateTaskID(taskID); err != nil {
		return Task{}, false, err
	}

	socketPath := filepath.Join(s.Dir, taskID+vmSocketSuffix)
	info, err := os.Stat(socketPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Task{}, false, nil
		}
		return Task{}, false, fmt.Errorf("stat task socket %s: %w", socketPath, err)
	}
	if info.IsDir() {
		return Task{}, false, fmt.Errorf("task socket %s is a directory", socketPath)
	}

	task := Task{ID: taskID, SocketPath: socketPath}
	if s.LogDir != "" {
		// Derived, not probed: a task resolves while its console log is still
		// being created, and the log handler owns the not-found answer.
		task.LogPath = filepath.Join(s.LogDir, taskID+consoleLogSuffix)
	}

	return task, true, nil
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
