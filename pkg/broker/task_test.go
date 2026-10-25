package broker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSocketDirSourceLookupFindsRunningTask(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "task-1.sock")
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))

	task, ok, err := SocketDirSource{Dir: dir}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, Task{
		ID: "task-1", SocketPath: socketPath, Running: true,
		ConsolePath: filepath.Join(dir, "task-1.console.sock"),
	}, task)
}

// TestSocketDirSourceLookupFindsExitedTaskByLog pins that a task whose VM is
// gone still resolves while its console log exists: reading the log is the
// "why did it crash" case, and the log handler must be able to serve it.
func TestSocketDirSourceLookupFindsExitedTaskByLog(t *testing.T) {
	dir := t.TempDir()
	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "task-1.log")
	require.NoError(t, os.WriteFile(logPath, []byte("boom\n"), 0o600))

	task, ok, err := SocketDirSource{Dir: dir, LogDir: logDir}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, Task{ID: "task-1", LogPath: logPath}, task,
		"a log-only task has no socket path and is not running")
}

// TestSocketDirSourceExitedTaskWithoutLogIsMissing pins the other half: once
// neither the socket nor the log exists, the task is gone.
func TestSocketDirSourceExitedTaskWithoutLogIsMissing(t *testing.T) {
	dir := t.TempDir()
	logDir := t.TempDir()

	task, ok, err := SocketDirSource{Dir: dir, LogDir: logDir}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, task)
}

// TestSocketDirSourceExitedTaskLogMustBeRegular pins that a directory named like
// a log does not resolve a task the log handler could not serve.
func TestSocketDirSourceExitedTaskLogMustBeRegular(t *testing.T) {
	dir := t.TempDir()
	logDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(logDir, "task-1.log"), 0o700))

	_, ok, err := SocketDirSource{Dir: dir, LogDir: logDir}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.False(t, ok)
}

// TestSocketDirSourceTreatsAStaleSocketAsExited pins the crash case: the VMM
// dies without removing its socket file, so a stat alone would call the task
// running and exec would dial a dead vsock path. The injected probe is what the
// daemon wires to the Firecracker process table.
func TestSocketDirSourceTreatsAStaleSocketAsExited(t *testing.T) {
	dir := t.TempDir()
	logDir := t.TempDir()
	socketPath := filepath.Join(dir, "task-1.sock")
	logPath := filepath.Join(logDir, "task-1.log")
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))
	require.NoError(t, os.WriteFile(logPath, []byte("boom\n"), 0o600))

	src := SocketDirSource{
		Dir:       dir,
		LogDir:    logDir,
		IsRunning: func(string) bool { return false },
	}
	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, Task{ID: "task-1", LogPath: logPath}, task,
		"a stale socket must not resolve as a running task")
}

// TestSocketDirSourceStaleSocketWithoutLogIsMissing pins that the leftover
// socket alone does not keep a dead task alive.
func TestSocketDirSourceStaleSocketWithoutLogIsMissing(t *testing.T) {
	dir := t.TempDir()
	logDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task-1.sock"), nil, 0o600))

	src := SocketDirSource{
		Dir:       dir,
		LogDir:    logDir,
		IsRunning: func(string) bool { return false },
	}
	_, ok, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.False(t, ok)
}

// TestSocketDirSourceLiveProbeKeepsTheTaskRunning pins that a probe which says
// the VMM is alive keeps the socket path and Running true.
func TestSocketDirSourceLiveProbeKeepsTheTaskRunning(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "task-1.sock")
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))

	src := SocketDirSource{Dir: dir, IsRunning: func(string) bool { return true }}
	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, Task{
		ID: "task-1", SocketPath: socketPath, Running: true,
		ConsolePath: filepath.Join(dir, "task-1.console.sock"),
	}, task)
}

func TestSocketDirSourceLookupMissingTask(t *testing.T) {
	task, ok, err := SocketDirSource{Dir: t.TempDir()}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, task, "a missing task must not leak a partial Task")
}

func TestSocketDirSourceRequiresDirectory(t *testing.T) {
	_, ok, err := SocketDirSource{}.Lookup(context.Background(), "task-1")
	require.Error(t, err, "an empty socket directory must not resolve a path relative to the daemon's CWD")
	require.False(t, ok)
}

// TestSocketDirSourceRejectsUntrustedIDs covers the path shapes a client can
// put in the request URL: traversal and separators must never reach the
// filesystem.
func TestSocketDirSourceRejectsUntrustedIDs(t *testing.T) {
	dir := t.TempDir()
	src := SocketDirSource{Dir: dir}

	for _, id := range []string{"", ".", "..", "../x", "a/b", `a\b`, "a.b", "..%2F"} {
		t.Run(id, func(t *testing.T) {
			_, ok, err := src.Lookup(context.Background(), id)
			require.Error(t, err, "task ID %q must be rejected", id)
			require.False(t, ok)
		})
	}
}

// TestSocketDirSourceDerivesLogPath pins that a resolved task carries the
// console log path the daemon's VMM manager writes:
// <logDir>/<taskID>.log.
func TestSocketDirSourceDerivesLogPath(t *testing.T) {
	dir := t.TempDir()
	logDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task-1.sock"), nil, 0o600))

	task, ok, err := SocketDirSource{Dir: dir, LogDir: logDir}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, filepath.Join(logDir, "task-1.log"), task.LogPath)
}

// TestSocketDirSourceWithoutLogDirHasNoLogPath pins that a source with no log
// directory never guesses one: the log handler then answers not_found rather
// than reading a path relative to the daemon's working directory.
func TestSocketDirSourceWithoutLogDirHasNoLogPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task-1.sock"), nil, 0o600))

	task, ok, err := SocketDirSource{Dir: dir}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, task.LogPath)
}

// TestSocketDirSourceLogPathIsIndependentOfTheFile pins that the path is
// derived, not probed: whether the log file exists yet is the log handler's
// decision, and a task must resolve while its console log is still being
// created.
func TestSocketDirSourceLogPathIsIndependentOfTheFile(t *testing.T) {
	dir := t.TempDir()
	logDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task-1.sock"), nil, 0o600))

	task, ok, err := SocketDirSource{Dir: dir, LogDir: logDir}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoFileExists(t, task.LogPath)
}

// TestSocketDirSourceDerivesConsolePath pins the interactive console socket a
// running task carries: <socketDir>/<taskID>.console.sock, the path pkg/console
// listens on. A log-only task has none, because a stopped VM has no console.
func TestSocketDirSourceDerivesConsolePath(t *testing.T) {
	dir := t.TempDir()
	logDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task-1.sock"), nil, 0o600))

	task, ok, err := SocketDirSource{Dir: dir, LogDir: logDir}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, filepath.Join(dir, "task-1.console.sock"), task.ConsolePath)
	require.NoFileExists(t, task.ConsolePath, "the path is derived, not probed")

	require.NoError(t, os.WriteFile(filepath.Join(logDir, "task-2.log"), []byte("boom\n"), 0o600))
	exited, ok, err := SocketDirSource{Dir: dir, LogDir: logDir}.Lookup(context.Background(), "task-2")
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, exited.ConsolePath, "a log-only task has no console to attach to")
	require.False(t, exited.consoleAvailable())
}

func TestTaskVsockPath(t *testing.T) {
	cases := []struct {
		name string
		task Task
		want string
	}{
		{
			name: "derives the muxer next to the API socket",
			task: Task{ID: "task-1", SocketPath: "/var/run/firecracker/task-1.sock"},
			want: "/var/run/firecracker/task-1.vsock",
		},
		{
			name: "jailer-style nested socket directory",
			task: Task{ID: "abc", SocketPath: "/srv/jail/abc/root/run/abc.sock"},
			want: "/srv/jail/abc/root/run/abc.vsock",
		},
		{
			name: "no socket path means no vsock path",
			task: Task{ID: "task-1"},
			want: "",
		},
		{
			name: "no task ID means no vsock path",
			task: Task{SocketPath: "/var/run/firecracker/task-1.sock"},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.task.VsockPath())
		})
	}
}
