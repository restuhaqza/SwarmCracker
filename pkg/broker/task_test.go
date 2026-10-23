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
	require.Equal(t, Task{ID: "task-1", SocketPath: socketPath}, task)
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
