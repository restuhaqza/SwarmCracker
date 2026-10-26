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
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/broker"
	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/spf13/cobra"
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

// TestSnapshotTimeoutFlagReachesTheOperationContext pins the deadline flag on
// both local snapshot operations. Create and restore copy the whole task disk, so
// the default is the daemon's own five minutes — a fixed 60 seconds would fail on
// any filesystem without reflinks — and an explicit value is what the operation
// context is built with.
func TestSnapshotTimeoutFlagReachesTheOperationContext(t *testing.T) {
	// The CLI and the daemon must not drift: one copies the disk in the foreground,
	// the other relays the same request.
	require.Equal(t, broker.DefaultSnapshotTimeout, defaultSnapshotTimeout)

	createCmd := newSnapshotCreateCommand()
	restoreCmd := newSnapshotRestoreCommand()

	for _, cmd := range []*cobra.Command{createCmd, restoreCmd} {
		flag := cmd.Flags().Lookup("timeout")
		require.NotNil(t, flag, "%q must expose --timeout", cmd.Name())
		assert.Equal(t, defaultSnapshotTimeout.String(), flag.DefValue,
			"%q must default to the disk-copy deadline", cmd.Name())
	}

	snapshotTestConfig(t)

	var got []time.Duration
	original := snapshotOperationContext
	t.Cleanup(func() { snapshotOperationContext = original })
	snapshotOperationContext = func(timeout time.Duration) (context.Context, context.CancelFunc) {
		got = append(got, timeout)
		// Fail the operation immediately: the deadline value is the assertion,
		// not the snapshot itself.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, func() {}
	}

	require.NoError(t, createCmd.Flags().Set("socket", fakeSnapshotAPISocket(t)))
	require.NoError(t, createCmd.Flags().Set("timeout", "42s"))
	require.Error(t, createCmd.RunE(createCmd, []string{"task-1"}),
		"a cancelled operation context must fail the create")

	writeSnapshotForRestore(t, snapshotDirFromConfig(t))

	require.NoError(t, restoreCmd.Flags().Set("timeout", "90s"))
	require.Error(t, restoreCmd.RunE(restoreCmd, []string{testRestoreSnapshotID}),
		"a cancelled operation context must fail the restore")

	assert.Equal(t, []time.Duration{42 * time.Second, 90 * time.Second}, got,
		"each command must build its operation context from the flag")
}

// testRestoreSnapshotID is the snapshot the timeout test restores.
const testRestoreSnapshotID = "snap-0123456789abcdef"

// TestSnapshotRestoreRefusesARunningTask pins the local half of the API's
// `restore_task_running` rule: a restore writes the snapshot's disk over the
// task's own rootfs, so a VM that is still running on this node must be refused
// before the copy. Otherwise the restore would overwrite a live disk.
func TestSnapshotRestoreRefusesARunningTask(t *testing.T) {
	snapshotTestConfig(t)
	writeSnapshotForRestore(t, snapshotDirFromConfig(t))

	original := taskVMSocketAlive
	t.Cleanup(func() { taskVMSocketAlive = original })
	var probed string
	taskVMSocketAlive = func(socketPath string, _ time.Duration) bool {
		probed = socketPath
		return true
	}

	cmd := newSnapshotRestoreCommand()
	err := cmd.RunE(cmd, []string{testRestoreSnapshotID})

	require.Error(t, err)
	var typed *output.Error
	require.ErrorAs(t, err, &typed, "the refusal must be a typed error")
	assert.Equal(t, output.CodeConflict, typed.Code)
	assert.Equal(t, "restore_task_running", typed.Details["reason"])
	assert.Contains(t, probed, "task-1.sock", "the guard must probe the task's own VM socket")
}

// snapshotDirFromConfig reads back the snapshot directory snapshotTestConfig
// wrote, so a test can place a snapshot where the command will look for it.
func snapshotDirFromConfig(t *testing.T) string {
	t.Helper()

	cfg, err := config.LoadConfig(cfgFile)
	require.NoError(t, err)
	return cfg.Snapshot.SnapshotDir
}

// writeSnapshotForRestore publishes the smallest snapshot a restore accepts: a
// state file, a memory file and a disk copy, with a recorded rootfs path.
func writeSnapshotForRestore(t *testing.T, snapshotDir string) {
	t.Helper()

	dir := filepath.Join(snapshotDir, testRestoreSnapshotID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vm.state"), []byte("state"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vm.mem"), []byte("memory"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rootfs.ext4"), []byte("disk"), 0o600))

	metadata, err := json.Marshal(&snapshot.SnapshotInfo{
		ID:         testRestoreSnapshotID,
		TaskID:     "task-1",
		StatePath:  "vm.state",
		MemoryPath: "vm.mem",
		DiskPath:   "rootfs.ext4",
		RootfsPath: filepath.Join(t.TempDir(), "task-1.ext4"),
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), metadata, 0o600))
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
