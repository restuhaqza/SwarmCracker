package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restoredSocketDir seeds a socket directory with one <task>-restored.sock file
// per task name and returns it.
func restoredSocketDir(t *testing.T, tasks ...string) string {
	t.Helper()

	dir := t.TempDir()
	for _, task := range tasks {
		socket := filepath.Join(dir, task+"-restored.sock")
		require.NoError(t, os.WriteFile(socket, nil, 0o600))
	}
	return dir
}

// withRestoredPID makes the local stop resolve a supplied PID instead of reading
// procfs, which only exists on Linux.
func withRestoredPID(t *testing.T, pid int) {
	t.Helper()

	previous := restoredPIDBySocket
	restoredPIDBySocket = func(string) int { return pid }
	t.Cleanup(func() { restoredPIDBySocket = previous })
}

// TestStopRestoredVMKillsTheProcessAndRemovesTheSocket pins the local command's
// whole job on a real process: SIGTERM ends it, and the stale socket is removed.
func TestStopRestoredVMKillsTheProcessAndRemovesTheSocket(t *testing.T) {
	socketDir := restoredSocketDir(t, "task-1")
	socketPath := filepath.Join(socketDir, "task-1-restored.sock")

	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	withRestoredPID(t, cmd.Process.Pid)

	taskID, err := stopLocalRestoredVM("task-1", socketDir)
	require.NoError(t, err)
	assert.Equal(t, "task-1", taskID)

	_, waitErr := cmd.Process.Wait()
	require.NoError(t, waitErr, "the process must have exited")

	_, statErr := os.Stat(socketPath)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "the stopped VM's socket must be removed")
}

// TestStopRestoredVMResolvesAUniquePrefix pins the reference rule without a
// process: an exact task ID and a unique prefix both resolve, an ambiguous prefix
// is a usage error and an unknown one is a not-found.
func TestStopRestoredVMResolvesAUniquePrefix(t *testing.T) {
	dir := restoredSocketDir(t, "5f3a1b2c9d00", "5f3a1b2c9d01", "deadbeef0000")

	t.Run("exact id", func(t *testing.T) {
		id, socket, err := resolveRestoredSocket(dir, "deadbeef0000")
		require.NoError(t, err)
		assert.Equal(t, "deadbeef0000", id)
		assert.Equal(t, filepath.Join(dir, "deadbeef0000-restored.sock"), socket)
	})

	t.Run("unique prefix", func(t *testing.T) {
		id, socket, err := resolveRestoredSocket(dir, "dead")
		require.NoError(t, err)
		assert.Equal(t, "deadbeef0000", id)
		assert.Equal(t, filepath.Join(dir, "deadbeef0000-restored.sock"), socket)
	})

	t.Run("ambiguous prefix", func(t *testing.T) {
		_, _, err := resolveRestoredSocket(dir, "5f3a")
		require.Error(t, err)
		assert.True(t, isUsageError(err), "an ambiguous reference needs a longer prefix")
	})

	t.Run("unknown reference", func(t *testing.T) {
		_, _, err := resolveRestoredSocket(dir, "nope")
		require.Error(t, err)
		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, output.CodeNotFound, typed.Code)
		assert.Equal(t, "no_restored_vm", typed.Details["reason"])
	})
}

// TestStopRestoredVMWithoutAProcessIs404 pins the stale-socket case: the file
// exists but no Firecracker owns it, so the command reports no restored VM rather
// than claiming a stop.
func TestStopRestoredVMWithoutAProcessIs404(t *testing.T) {
	socketDir := restoredSocketDir(t, "task-1")
	withRestoredPID(t, 0)

	_, err := stopLocalRestoredVM("task-1", socketDir)
	require.Error(t, err)

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeNotFound, typed.Code)
	assert.Equal(t, "no_restored_vm", typed.Details["reason"])
}

// TestStopRestoredVMNeedsARefusedSocketDir pins the empty-directory case, which
// resolves before any signal is attempted.
func TestStopRestoredVMMissingSocketDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")

	_, _, err := resolveRestoredSocket(dir, "task-1")
	require.Error(t, err)

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeNotFound, typed.Code)
}

// TestStopRestoredRemoteCallsTheAPIRoute pins the -H half: the command goes
// through pkg/client, which sends DELETE /v1/tasks/{task}/restored.
func TestStopRestoredRemoteCallsTheAPIRoute(t *testing.T) {
	resetAPIFlags(t)

	var gotMethod, gotPath string
	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	apiHost, apiCA = endpoint, caFile

	cmd := newSnapshotStopRestoredCommand()
	require.NoError(t, cmd.RunE(cmd, []string{"task-1"}))

	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/v1/tasks/task-1/restored", gotPath)
}

// TestStopRestoredRemoteIsRemoteCapable pins the guard: `--host` must not be
// refused for this command, because the node that started the restored VM is the
// only one that can stop it.
func TestStopRestoredRemoteIsRemoteCapable(t *testing.T) {
	root := newRootCommand()
	cmd, _, err := root.Find([]string{"vm", "snapshot", "stop-restored"})
	require.NoError(t, err)
	require.Equal(t, "stop-restored", cmd.Name())
	assert.True(t, commandAllowsRemote(cmd))
}

// TestStopRestoredRemoteMapsNoRestoredVM pins the error relay: a 404 from the API
// stays a typed not-found with the reason intact.
func TestStopRestoredRemoteMapsNoRestoredVM(t *testing.T) {
	resetAPIFlags(t)

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeAPITestDocument(w, http.StatusNotFound, map[string]any{
			"schemaVersion": 1,
			"kind":          "Error",
			"error": map[string]any{
				"code":    "not_found",
				"message": "task has no restored VM on this node",
				"details": map[string]any{"reason": "no_restored_vm"},
			},
		})
	})
	apiHost, apiCA = endpoint, caFile

	cmd := newSnapshotStopRestoredCommand()
	err := cmd.RunE(cmd, []string{"task-1"})
	require.Error(t, err)

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeNotFound, typed.Code)
	assert.Equal(t, "no_restored_vm", typed.Details["reason"])
}

// TestSnapshotCommandRegistration pins the new subcommand on the group.
func TestSnapshotCommandRegistration(t *testing.T) {
	cmd := newSnapshotCommand()

	var found bool
	for _, sub := range cmd.Commands() {
		if sub.Name() == "stop-restored" {
			found = true
		}
	}
	assert.True(t, found, "vm snapshot must expose stop-restored")
}
