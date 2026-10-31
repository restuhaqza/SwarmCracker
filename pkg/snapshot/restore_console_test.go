package snapshot

// The console log a restored VM writes. It is the file `vm logs` on the task the VM
// runs as reads, so the two things pinned here are its path (<logDir>/<taskID>.log) and
// that a restore into an existing log never silently truncates what the earlier run
// wrote.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenRestoredConsoleCreatesTheTaskLog pins the file a restore opens: created when
// absent, with no boundary marker (there is nothing earlier to separate from).
func TestOpenRestoredConsoleCreatesTheTaskLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "task-1.log")

	console, err := openRestoredConsole(path)
	require.NoError(t, err)
	require.NotNil(t, console)
	defer func() { _ = console.Close() }()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
	assert.Zero(t, info.Size(), "a fresh log holds no boundary marker")
}

// TestOpenRestoredConsoleKeepsEarlierOutput pins the second restore into the same path:
// the earlier output is kept and a marker says where the new run begins, because a task
// that is restored twice writes one file and the two lifetimes must be told apart.
func TestOpenRestoredConsoleKeepsEarlierOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task-1.log")
	require.NoError(t, os.WriteFile(path, []byte("earlier console output\n"), 0o644))

	console, err := openRestoredConsole(path)
	require.NoError(t, err)
	require.NotNil(t, console)
	defer func() { _ = console.Close() }()

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(body), "earlier console output\n"), "the earlier run is kept: %s", body)
	assert.Contains(t, string(body), "console restarted at", "the boundary says where the new run starts")
}

// TestOpenRestoredConsoleWithNoPath pins the option's off state: a restore outside a task
// (the experimental API) writes no console and asks for no file.
func TestOpenRestoredConsoleWithNoPath(t *testing.T) {
	console, err := openRestoredConsole("")
	require.NoError(t, err)
	assert.Nil(t, console)
}

// TestOpenRestoredConsoleReportsAnUnopenablePath pins the honest failure: the caller asked
// for the task's logs, so a path that cannot be created fails the restore before a
// process exists rather than leaving a moved task whose logs answer 404.
func TestOpenRestoredConsoleReportsAnUnopenablePath(t *testing.T) {
	// A file where a directory must be: MkdirAll cannot create the parent.
	parent := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(parent, []byte("x"), 0o644))

	_, err := openRestoredConsole(filepath.Join(parent, "task-1.log"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "console log")
}
