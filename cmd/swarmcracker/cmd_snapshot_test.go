package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshotTestConfig points the snapshot commands at a throwaway snapshot
// directory, so a command that gets past the ID check cannot touch the real
// default path.
func snapshotTestConfig(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "version: 1\nsnapshot:\n  enabled: false\n  snapshot_dir: " + filepath.Join(dir, "snapshots") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	previous := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = previous })
}

// TestSnapshotCommandsRejectUnsafeIDs pins item 6 at the CLI: a snapshot ID is
// validated before any configuration or filesystem work, and a violation is a
// usage error (exit 2), not a not-found or an internal failure.
func TestSnapshotCommandsRejectUnsafeIDs(t *testing.T) {
	snapshotTestConfig(t)

	commands := map[string]func() (run func([]string) error, name string){
		"delete": func() (func([]string) error, string) {
			cmd := newSnapshotDeleteCommand()
			return func(args []string) error { return cmd.RunE(cmd, args) }, cmd.Name()
		},
		"restore": func() (func([]string) error, string) {
			cmd := newSnapshotRestoreCommand()
			return func(args []string) error { return cmd.RunE(cmd, args) }, cmd.Name()
		},
	}

	ids := []string{
		"../victim",
		"/",
		`C:\snap-0123456789abcdef`,
		`..\..\victim`,
		"snap-0123456789abcdef/child",
		"snap-0123456789ABCEDF",
	}

	for name, build := range commands {
		run, commandName := build()
		require.Equal(t, name, commandName)

		for _, id := range ids {
			t.Run(name+" "+id, func(t *testing.T) {
				err := run([]string{id})

				require.Error(t, err)
				var typed *output.Error
				require.ErrorAs(t, err, &typed, "the CLI must answer a typed error")
				assert.Equal(t, output.CodeUsage, typed.Code)
				assert.Equal(t, 2, output.ExitCode(err))
				assert.Equal(t, "snapshot-id", typed.Details["parameter"])
			})
		}
	}
}

// TestSnapshotCommandsAcceptTheGeneratedShape is the other half: a well-formed
// ID that does not exist is a store failure, not a usage error, so the
// validation never rejects a real snapshot.
func TestSnapshotCommandsAcceptTheGeneratedShape(t *testing.T) {
	snapshotTestConfig(t)

	const missing = "snap-00000000000000ff"

	deleteCmd := newSnapshotDeleteCommand()
	err := deleteCmd.RunE(deleteCmd, []string{missing})
	require.Error(t, err)
	assert.False(t, isUsageError(err), "a missing snapshot is not a usage error")
	assert.True(t, errors.Is(err, snapshot.ErrNotFound), "the store's not-found sentinel must survive")

	restoreCmd := newSnapshotRestoreCommand()
	err = restoreCmd.RunE(restoreCmd, []string{missing})
	require.Error(t, err)
	assert.False(t, isUsageError(err), "a missing snapshot is not a usage error")
}

// isUsageError reports whether err carries the usage code.
func isUsageError(err error) bool {
	var typed *output.Error
	return errors.As(err, &typed) && typed.Code == output.CodeUsage
}

// TestTaskRootfsPathDerivesTheExecutorDisk pins the default that makes a local
// CLI snapshot self-contained: the executor keeps one disk per task, so the CLI
// derives <rootfs-dir>/tasks/<task id>.ext4 from the socket name. A disk that is
// not there leaves the option empty, which keeps the historical "no disk
// recorded" behaviour instead of failing the create on a guess.
func TestTaskRootfsPathDerivesTheExecutorDisk(t *testing.T) {
	rootfsDir := t.TempDir()
	tasksDir := filepath.Join(rootfsDir, "tasks")
	require.NoError(t, os.MkdirAll(tasksDir, 0o755))

	taskDisk := filepath.Join(tasksDir, "task-1.ext4")
	require.NoError(t, os.WriteFile(taskDisk, []byte("task disk"), 0o600))

	cfg := &config.Config{}
	cfg.Executor.RootfsDir = rootfsDir

	t.Run("the task disk exists", func(t *testing.T) {
		assert.Equal(t, taskDisk, taskRootfsPath(cfg, "/var/run/firecracker/task-1.sock"))
	})

	t.Run("a socket without the .sock suffix", func(t *testing.T) {
		assert.Equal(t, taskDisk, taskRootfsPath(cfg, "/run/task-1"))
	})

	t.Run("the task disk is gone", func(t *testing.T) {
		assert.Empty(t, taskRootfsPath(cfg, "/var/run/firecracker/task-2.sock"))
	})

	t.Run("the rootfs dir is unset", func(t *testing.T) {
		assert.Empty(t, taskRootfsPath(&config.Config{}, "/var/run/firecracker/task-1.sock"))
	})
}

// TestSnapshotCreateDerivesTheTaskDiskFromTheSocket pins the wiring, not just the
// helper: with no --rootfs, `create` must record the derived disk and copy it, so
// the snapshot the CLI produces is one the executor's cleanup cannot break.
func TestSnapshotCreateDerivesTheTaskDiskFromTheSocket(t *testing.T) {
	dir := t.TempDir()
	rootfsDir := filepath.Join(dir, "rootfs")
	require.NoError(t, os.MkdirAll(filepath.Join(rootfsDir, "tasks"), 0o755))

	content := []byte("task disk bytes")
	require.NoError(t, os.WriteFile(filepath.Join(rootfsDir, "tasks", "task-1.ext4"), content, 0o600))

	snapshotDir := filepath.Join(dir, "snapshots")

	cfgPath := filepath.Join(dir, "config.yaml")
	body := "version: 1\nrootfs_dir: " + rootfsDir +
		"\nsnapshot:\n  enabled: true\n  snapshot_dir: " + snapshotDir + "\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(body), 0o600))

	previous := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = previous })

	socketPath := fakeSnapshotAPISocket(t)

	cmd := newSnapshotCreateCommand()
	require.NoError(t, cmd.Flags().Set("socket", socketPath))

	require.NoError(t, cmd.RunE(cmd, []string{"task-1"}))

	mgr, err := snapshot.NewManager(snapshot.SnapshotConfig{SnapshotDir: snapshotDir})
	require.NoError(t, err)

	snapshots, err := mgr.ListSnapshots(snapshot.SnapshotFilter{})
	require.NoError(t, err)
	require.Len(t, snapshots, 1)

	info := snapshots[0]
	require.NotEmpty(t, info.DiskPath, "the snapshot must own the task disk")
	assert.Equal(t, filepath.Join(rootfsDir, "tasks", "task-1.ext4"), info.RootfsPath)

	copied, err := os.ReadFile(info.DiskPath)
	require.NoError(t, err)
	assert.Equal(t, content, copied)
}

// fakeSnapshotAPISocket serves the two calls a create makes, so a CLI test can
// run end to end without Firecracker. The socket lives in a short directory: a
// Unix socket path is limited to roughly 104 bytes.
func fakeSnapshotAPISocket(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "sc-sock-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socketPath := filepath.Join(dir, "task-1.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/vm", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/snapshot/create", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			SnapshotPath string `json:"snapshot_path"`
			MemFilepath  string `json:"mem_file_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(payload.SnapshotPath, []byte("mock state"), 0o600); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(payload.MemFilepath, []byte("mock memory"), 0o600); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Shutdown(context.Background())
		_ = listener.Close()
	})

	return socketPath
}
