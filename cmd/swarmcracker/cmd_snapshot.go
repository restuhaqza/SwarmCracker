package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newSnapshotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage VM snapshots",
		Long: `Create, restore, list, delete, and clean up VM snapshots.

Snapshots capture the full memory and state of a running microVM,
enabling fast restore (faster than cold boot) and future live migration.

Example:
  swarmcracker vm snapshot create <task-id>
  swarmcracker vm snapshot restore <snapshot-id>
  swarmcracker vm snapshot list
  swarmcracker vm snapshot delete <snapshot-id>
  swarmcracker vm snapshot cleanup`,
	}

	cmd.AddCommand(newSnapshotCreateCommand())
	cmd.AddCommand(newSnapshotRestoreCommand())
	cmd.AddCommand(newSnapshotStopRestoredCommand())
	cmd.AddCommand(newSnapshotListCommand())
	cmd.AddCommand(newSnapshotDeleteCommand())
	cmd.AddCommand(newSnapshotCleanupCommand())

	return cmd
}

// --- create ---

// defaultSnapshotTimeout is the deadline for one local snapshot create or
// restore. It matches the daemon's --snapshot-timeout default (pkg/broker's
// DefaultSnapshotTimeout): both operations copy the whole task disk, so a fixed
// 60 seconds would fail on any host whose filesystem lacks reflinks.
const defaultSnapshotTimeout = 5 * time.Minute

// snapshotOperationContext builds the context one local snapshot operation runs
// under. It is a variable only so a test can observe the value the --timeout flag
// delivered; production never replaces it.
var snapshotOperationContext = func(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultSnapshotTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}

func newSnapshotCreateCommand() *cobra.Command {
	var (
		socketPath string
		serviceID  string
		nodeID     string
		vcpus      int
		memoryMB   int
		rootfsPath string
		timeout    time.Duration
	)

	cmd := &cobra.Command{
		Use:   "create <task-id>",
		Short: "Create a snapshot of a running VM",
		Long: `Snapshot a running microVM identified by its task ID.

The VM must be running and accessible via its Firecracker API socket.

The snapshot owns a copy of the task disk, taken while the VM is paused, so the
operation lasts as long as that copy. --timeout (default 5m, the daemon's own
snapshot default) bounds the whole create: raise it on a filesystem without
reflinks, where ten gigabytes is not a thirty-second copy.

Example:
  swarmcracker vm snapshot create task-123
  swarmcracker vm snapshot create task-123 --socket /var/run/firecracker/task-123.sock
  swarmcracker vm snapshot create task-123 --timeout 15m`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID := args[0]

			cfg, err := loadSnapshotConfig(cfgFile)
			if err != nil {
				return err
			}

			mgr, err := snapshot.NewManager(toSnapshotConfig(cfg.Snapshot))
			if err != nil {
				return fmt.Errorf("failed to create snapshot manager: %w", err)
			}

			// Resolve socket path
			if socketPath == "" {
				socketPath = filepath.Join(cfg.Executor.SocketDir, taskID+".sock")
			}

			// A snapshot owns a copy of the task disk, so create needs the disk path
			// even when the operator did not pass --rootfs: the executor keeps one
			// disk per task, and the socket names the task.
			if rootfsPath == "" {
				rootfsPath = taskRootfsPath(cfg, socketPath)
			}

			opts := snapshot.CreateOptions{
				ServiceID:  serviceID,
				NodeID:     nodeID,
				VCPUCount:  vcpus,
				MemoryMB:   memoryMB,
				RootfsPath: rootfsPath,
			}

			ctx, cancel := snapshotOperationContext(timeout)
			defer cancel()

			info, err := mgr.CreateSnapshot(ctx, taskID, socketPath, opts)
			if err != nil {
				return fmt.Errorf("failed to create snapshot: %w", err)
			}

			fmt.Printf("Snapshot created successfully\n")
			fmt.Printf("  ID:        %s\n", info.ID)
			fmt.Printf("  Task ID:   %s\n", info.TaskID)
			fmt.Printf("  Created:   %s\n", info.CreatedAt.Format(time.RFC3339))
			fmt.Printf("  Size:      %s\n", snapshotFormatBytes(info.SizeBytes))
			fmt.Printf("  Checksum:  %s\n", info.Checksum)

			return nil
		},
	}

	cmd.Flags().StringVar(&socketPath, "socket", "", "Path to Firecracker API socket (default: <socket-dir>/<task-id>.sock)")
	cmd.Flags().StringVar(&serviceID, "service", "", "SwarmKit service ID for metadata")
	cmd.Flags().StringVar(&nodeID, "node", "", "Node ID for metadata")
	cmd.Flags().IntVar(&vcpus, "vcpus", 0, "vCPU count (for metadata)")
	cmd.Flags().IntVar(&memoryMB, "memory", 0, "Memory in MB (for metadata)")
	cmd.Flags().StringVar(&rootfsPath, "rootfs", "", "Rootfs path (default: <rootfs-dir>/tasks/<task-id>.ext4 when it exists)")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultSnapshotTimeout,
		"Deadline for the whole create, including the task-disk copy")

	return cmd
}

// taskRootfsPath derives the disk the executor keeps for the task behind a
// Firecracker API socket: <rootfs dir>/tasks/<task id>.ext4, where the task ID is
// the socket's file name without its .sock suffix. The broker derives the same
// path for an API-created snapshot, so the two entry points produce the same
// self-contained restore point.
//
// It returns "" when the file does not exist, which keeps the historical "no disk
// recorded" shape for a VM started from a disk the executor does not manage: a
// wrong guess would fail the create instead of recording a snapshot.
func taskRootfsPath(cfg *config.Config, socketPath string) string {
	if cfg.Executor.RootfsDir == "" {
		return ""
	}

	taskID := strings.TrimSuffix(filepath.Base(socketPath), ".sock")
	if taskID == "" || taskID == "." || taskID == string(filepath.Separator) {
		return ""
	}

	candidate := filepath.Join(cfg.Executor.RootfsDir, "tasks", taskID+".ext4")
	if _, err := os.Stat(candidate); err != nil {
		return ""
	}

	return candidate
}

// --- restore ---

// snapshotProbeTimeout bounds the liveness probe of the task's own API socket.
// A live Firecracker answers a machine-config request in microseconds; the
// timeout only covers a wedged process.
const snapshotProbeTimeout = 500 * time.Millisecond

// taskVMSocketAlive is runtime.IsVMSocketAlive. It is a variable only so a test
// can answer for the node without starting a Firecracker process; production
// never replaces it.
var taskVMSocketAlive = runtime.IsVMSocketAlive

// restoreSocketPath is where a restore of this task puts its VM: the task's own
// API socket, <socketDir>/<taskID>.sock.
//
// The snapshot belongs to the task, so the VM it starts belongs to the task too —
// same socket, same vsock muxer, same console log, and `vm exec <task>` reaches
// it without being told a second name. A restore used to start the VM at
// <taskID>-restored.sock instead, which made the restored VM a second identity
// whose muxer no caller could derive.
func restoreSocketPath(socketDir, taskID string) string {
	return runtime.SocketPath(socketDir, taskID)
}

// restoredVMID is the identity a VM restored at socketPath has: the task whose
// snapshot it loaded, which is also the task whose API socket that path is.
//
// --socket can place the task's socket in a directory of the operator's choosing,
// so the socket's own name is read first and the snapshot's task ID is the
// fallback for a name that is not a task ID at all. What it never does is invent
// one: the "-restored" identity a restore used to add is gone, and a restored VM
// is listed, stopped and executed as the task.
func restoredVMID(target *snapshot.SnapshotInfo, socketPath string) string {
	if id := runtime.TaskIDFromSocketPath(socketPath); id != "" {
		return id
	}
	return target.TaskID
}

// restoreTaskVM is the one call the restore command makes into pkg/snapshot. It is
// a variable only so a test can prove which console path the command hands the
// restore; production never replaces it.
//
// The console path is the reason this seam exists at all: a restore that is given
// none starts Firecracker with its serial output detached, so the restored VM has
// no logs to read and `vm logs` answers not-found for a VM that is running.
var restoreTaskVM = func(
	ctx context.Context, mgr *snapshot.Manager, target *snapshot.SnapshotInfo,
	socketPath, consolePath string,
) error {
	return mgr.RestoreFromSnapshotWithLog(ctx, target, socketPath, consolePath)
}

// restoreConsolePath is where a CLI restore captures the restored VM's serial
// console: <logDir>/<vm id>.log, the file `vm logs` reads and the same shape the
// daemon's restore uses (pkg/swarmkit's vmConsolePath). It is computed here, once,
// and handed to both the restore and the state entry that follows it, so the file
// the VM writes and the file `vm logs` opens cannot disagree.
func restoreConsolePath(logDir, vmID string) string {
	return filepath.Join(logDir, vmID+".log")
}

func newSnapshotRestoreCommand() *cobra.Command {
	var (
		socketPath string
		timeout    time.Duration
	)

	cmd := &cobra.Command{
		Use:   "restore <snapshot-id>",
		Short: "Restore a VM from a snapshot",
		Long: `Restore a microVM from a previously created snapshot.

The restored VM will start with the exact memory state and configuration
it had when the snapshot was taken.

The snapshot's disk copy is written back to the recorded rootfs path first, which
can take as long as the disk copy took at create time. --timeout (default 5m, the
daemon's own snapshot default) bounds the whole restore.

Example:
  swarmcracker vm snapshot restore snap-abc123def4567890
  swarmcracker vm snapshot restore snap-abc123def4567890 --socket /var/run/firecracker/task-123.sock
  swarmcracker vm snapshot restore snap-abc123def4567890 --timeout 15m`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshotID := args[0]
			if err := validateSnapshotIDArg(snapshotID); err != nil {
				return err
			}

			cfg, err := loadSnapshotConfig(cfgFile)
			if err != nil {
				return err
			}

			mgr, err := snapshot.NewManager(toSnapshotConfig(cfg.Snapshot))
			if err != nil {
				return fmt.Errorf("failed to create snapshot manager: %w", err)
			}

			// Find snapshot
			snapshots, err := mgr.ListSnapshots(snapshot.SnapshotFilter{})
			if err != nil {
				return fmt.Errorf("failed to list snapshots: %w", err)
			}

			var target *snapshot.SnapshotInfo
			for _, s := range snapshots {
				if s.ID == snapshotID {
					target = s
					break
				}
			}
			if target == nil {
				return fmt.Errorf("snapshot not found: %s", snapshotID)
			}

			// Resolve socket path. A restore puts the task's VM back at the task's
			// own API socket: the snapshot is the task's own, so the VM it starts
			// is the task's VM under the task's identity. Minting a second name
			// beside it (`<task>-restored.sock`) is what left `vm exec <task>`
			// dialing a muxer nobody bound.
			if socketPath == "" {
				socketPath = restoreSocketPath(cfg.Executor.SocketDir, target.TaskID)
			}
			// A restore given no place to put the socket would start Firecracker
			// with an empty --api-sock, which fails inside the VMM with nothing
			// an operator can act on. The node broker answers the same case as
			// socket_dir_unconfigured.
			if socketPath == "" {
				return output.NewError(output.CodeUsage,
					"no socket directory is configured: pass --socket or set executor.socket_dir in the config file")
			}

			// A restore writes the snapshot's disk over the task's own rootfs, so a
			// task whose VM is still running on this node must be refused before
			// that copy: the live VM owns the file. The API path answers the same
			// case with 409 restore_task_running, and a second Firecracker (the old
			// outcome) corrupts both VMs anyway.
			if cfg.Executor.SocketDir != "" {
				taskSocket := restoreSocketPath(cfg.Executor.SocketDir, target.TaskID)
				if taskVMSocketAlive(taskSocket, snapshotProbeTimeout) {
					return output.NewErrorf(output.CodeConflict,
						"task %q is still running; restoring its snapshot would overwrite the live rootfs", target.TaskID).
						WithDetails(map[string]any{"reason": "restore_task_running"})
				}
			}

			// Register the restored VM so it can be listed, stopped, and have
			// its logs viewed like any other CLI-managed VM. Its identity is the
			// task's: the socket's name is that task's ID, and `--socket` only
			// decides where that ID's socket lives.
			restoredID := restoredVMID(target, socketPath)

			// The VM's console is captured from its first byte, so the path is
			// decided before the process exists: a console opened after the fact
			// would miss the boot output `vm logs` is read for. A state manager
			// that cannot be opened still leaves a log directory (the default one),
			// because losing the console over a state-file problem would be the
			// bug this path fixes.
			stateMgr, stateErr := runtime.NewStateManager("")
			if stateErr != nil {
				log.Warn().Err(stateErr).Msg("Failed to open the VM state file; the restore continues and only its console is kept")
			}
			logDir := runtime.DefaultLogDir()
			if stateMgr != nil {
				logDir = stateMgr.GetLogDir()
			}
			consolePath := restoreConsolePath(logDir, restoredID)

			ctx, cancel := snapshotOperationContext(timeout)
			defer cancel()

			if err := restoreTaskVM(ctx, mgr, target, socketPath, consolePath); err != nil {
				return fmt.Errorf("failed to restore snapshot: %w", err)
			}

			if stateMgr != nil {
				pid := runtime.FindFirecrackerPID(restoredID)
				if pid == 0 {
					pid = runtime.FindFirecrackerPIDBySocket(socketPath)
				}
				if addErr := stateMgr.Add(&runtime.VMState{
					ID:         restoredID,
					PID:        pid,
					SocketPath: socketPath,
					Status:     "running",
					Image:      "snapshot:" + target.ID,
					LogPath:    consolePath,
				}); addErr != nil {
					log.Warn().Err(addErr).Msg("Failed to register restored VM state")
				}
			}

			fmt.Printf("VM restored from snapshot\n")
			fmt.Printf("  Snapshot: %s\n", target.ID)
			fmt.Printf("  VM ID:    %s\n", restoredID)
			fmt.Printf("  Socket:   %s\n", socketPath)
			fmt.Printf("  Console:  %s\n", consolePath)

			return nil
		},
	}

	cmd.Flags().StringVar(&socketPath, "socket", "", "Path for new Firecracker API socket")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultSnapshotTimeout,
		"Deadline for the whole restore, including writing the task disk back")

	return cmd
}

// --- stop-restored ---

// defaultRestoredSocketDir mirrors the daemon's default --socket-dir. A restore
// starts the task's VM at <socket-dir>/<taskID>.sock, so the same directory is
// where a local stop looks.
const defaultRestoredSocketDir = "/var/run/firecracker"

func newSnapshotStopRestoredCommand() *cobra.Command {
	var socketDir string

	cmd := &cobra.Command{
		Use:   "stop-restored <task>",
		Short: "Stop the VM a snapshot restore started (deprecated)",
		Deprecated: "a restored VM is the task's VM: use 'swarmcracker vm stop <task>' for a VM this CLI " +
			"restored; a VM restored through the node still needs this command with -H",
		Long: `Stop the microVM a snapshot restore started for a task.

Deprecated: a restore now starts the VM of the task itself, at the task's own API
socket (<socket-dir>/<task>.sock), so the restored VM *is* the task's VM and no
longer has an identity of its own. Which command stops it depends on who
restored it:

  swarmcracker vm stop <task>
      A VM this CLI restored. The restore registers it under the task ID, so
      "vm stop" finds its state and ends it.

  swarmcracker vm snapshot stop-restored -H ...
      A VM restored through the node: the API route, or a restore done by an
      earlier daemon lifetime. "vm stop" reads this CLI's local state, finds no
      entry for it, and refuses the VM as SwarmKit-managed, so it cannot end
      this one.

This command itself still works locally — SIGTERM to the process behind the
task's own socket, a ten-second grace, SIGKILL, socket removed — and with -H it
is the only caller of the node's DELETE /v1/tasks/{id}/restored route.

<task> is a task ID or any unique prefix of one. With -H the manager asks the
node that holds the VM through the API instead, which needs the experimental
snapshot capability on that node.`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			// A remote stop goes through the manager: only the node that started
			// the process has its handle, and pkg/client is the single API client.
			if apiHost != "" {
				c, err := newContractClient()
				if err != nil {
					return err
				}
				defer func() { _ = c.Close() }()

				if err := c.StopRestoredTask(ctx, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Stopped the restored VM for %s.\n", args[0])
				return nil
			}

			taskID, err := stopLocalRestoredVM(args[0], socketDir)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Stopped the restored VM for %s.\n", taskID)
			return nil
		},
	}

	cmd.Flags().StringVar(&socketDir, "socket-dir", defaultRestoredSocketDir,
		"Directory holding Firecracker API sockets (<socket-dir>/<task>.sock)")

	return cmd
}

// restoredPIDBySocket is runtime.FindFirecrackerPIDBySocket. It is a variable so
// a test can hand back the PID of a process it started: the real lookup reads
// procfs and therefore only works on Linux.
var restoredPIDBySocket = runtime.FindFirecrackerPIDBySocket

// stopLocalRestoredVM resolves a task reference on this node, stops the
// Firecracker process behind its restored socket and removes that socket. It
// returns the resolved task ID.
func stopLocalRestoredVM(ref, socketDir string) (string, error) {
	taskID, socketPath, err := resolveRestoredSocket(socketDir, ref)
	if err != nil {
		return "", err
	}

	pid := restoredPIDBySocket(socketPath)
	if pid <= 0 {
		// The socket exists but no Firecracker process owns it: a stale file from
		// a crashed or already-stopped VM. Removing it is safe and is what a
		// later restore would do anyway, but the caller asked to stop a VM, so
		// say there is none instead of claiming success.
		return "", output.NewErrorf(output.CodeNotFound,
			"task %q has no running restored VM on this node (stale socket %s)", taskID, socketPath).
			WithDetails(map[string]any{"reason": "no_restored_vm"})
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return "", output.NewErrorf(output.CodeInternal,
			"find the restored VM process for task %q: %v", taskID, err)
	}
	if err := snapshot.StopProcess(proc); err != nil {
		return "", output.NewErrorf(output.CodeInternal,
			"stop the restored VM for task %q: %v", taskID, err)
	}

	// The store owns socket removal: pkg/snapshot.StopRestored unlinks the socket
	// under the per-task lock that serializes a restore and a stop, so it cannot
	// delete a socket a concurrent restore just created. This command runs in a
	// different process with its own manager, so it only clears the stale file it
	// resolved here; that is a convenience, never the primary mechanism, and a
	// failure does not turn a completed stop into an error.
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Debug().Err(err).Str("socket", socketPath).
			Msg("Failed to remove the stale restored VM socket")
	}
	return taskID, nil
}

// resolveRestoredSocket maps a task reference to a task ID and that task's API
// socket under socketDir. An exact ID wins; otherwise the reference is a unique
// ID prefix, the same rule `vm attach` and the task reads use. Ambiguity is
// reported with the candidates so the operator can retype a longer prefix.
//
// The socket is the task's own: a restore no longer mints a second identity, so
// a candidate is any <task>.sock in the directory (a console socket is not an API
// socket and is skipped by the shared rule). That also means this command can
// address the VM of a task this node booted itself — which is what it is, now
// that a restore runs the task's VM rather than a VM beside it.
func resolveRestoredSocket(socketDir, ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", output.NewError(output.CodeUsage, "a task reference is required")
	}

	entries, err := os.ReadDir(socketDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", output.NewErrorf(output.CodeNotFound,
				"%q does not match any restored VM: %s does not exist", ref, socketDir).
				WithDetails(map[string]any{"reason": "no_restored_vm"})
		}
		return "", "", fmt.Errorf("read socket directory %s: %w", socketDir, err)
	}

	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		id := runtime.TaskIDFromSocketPath(name)
		if id == "" {
			continue
		}
		if id == ref {
			return id, filepath.Join(socketDir, name), nil
		}
		ids = append(ids, id)
	}

	sort.Strings(ids)
	var matches []string
	for _, id := range ids {
		if strings.HasPrefix(id, ref) {
			matches = append(matches, id)
		}
	}

	switch len(matches) {
	case 0:
		if len(ids) == 0 {
			return "", "", output.NewErrorf(output.CodeNotFound,
				"no restored VM found under %s", socketDir).
				WithDetails(map[string]any{"reason": "no_restored_vm"})
		}
		return "", "", output.NewErrorf(output.CodeNotFound,
			"%q does not match any restored VM (available: %s)", ref, strings.Join(ids, ", ")).
			WithDetails(map[string]any{"reason": "no_restored_vm"})
	case 1:
		return matches[0], runtime.SocketPath(socketDir, matches[0]), nil
	default:
		return "", "", output.NewErrorf(output.CodeUsage,
			"%q matches multiple restored VMs: %s", ref, strings.Join(matches, ", ")).
			WithDetails(map[string]any{"parameter": "task"})
	}
}

// --- list ---

func newSnapshotListCommand() *cobra.Command {
	var (
		serviceID string
		taskID    string
		nodeID    string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List VM snapshots",
		Long: `List all VM snapshots, optionally filtered by service, task, or node.

Example:
  swarmcracker vm snapshot list
  swarmcracker vm snapshot list --service nginx
  swarmcracker vm snapshot list --task task-123`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadSnapshotConfig(cfgFile)
			if err != nil {
				return err
			}

			mgr, err := snapshot.NewManager(toSnapshotConfig(cfg.Snapshot))
			if err != nil {
				return fmt.Errorf("failed to create snapshot manager: %w", err)
			}

			filter := snapshot.SnapshotFilter{
				TaskID:    taskID,
				ServiceID: serviceID,
				NodeID:    nodeID,
			}

			snapshots, err := mgr.ListSnapshots(filter)
			if err != nil {
				return fmt.Errorf("failed to list snapshots: %w", err)
			}

			if len(snapshots) == 0 {
				fmt.Println("No snapshots found.")
				return nil
			}

			// Print as a table
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTASK\tSERVICE\tNODE\tCREATED\tSIZE\tCHECKSUM")
			for _, s := range snapshots {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					s.ID,
					truncate(s.TaskID, 16),
					truncate(s.ServiceID, 12),
					truncate(s.NodeID, 12),
					s.CreatedAt.Format("2006-01-02 15:04"),
					snapshotFormatBytes(s.SizeBytes),
					truncate(s.Checksum, 16),
				)
			}
			tw.Flush()

			fmt.Printf("\n%d snapshot(s)\n", len(snapshots))

			return nil
		},
	}

	cmd.Flags().StringVar(&serviceID, "service", "", "Filter by service ID")
	cmd.Flags().StringVar(&taskID, "task", "", "Filter by task ID")
	cmd.Flags().StringVar(&nodeID, "node", "", "Filter by node ID")

	return cmd
}

// --- delete ---

func newSnapshotDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <snapshot-id>",
		Short: "Delete a snapshot",
		Long: `Delete a VM snapshot and free disk space.

Example:
  swarmcracker vm snapshot delete snap-abc123def4567890`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshotID := args[0]
			if err := validateSnapshotIDArg(snapshotID); err != nil {
				return err
			}

			cfg, err := loadSnapshotConfig(cfgFile)
			if err != nil {
				return err
			}

			mgr, err := snapshot.NewManager(toSnapshotConfig(cfg.Snapshot))
			if err != nil {
				return fmt.Errorf("failed to create snapshot manager: %w", err)
			}

			ctx := context.Background()

			// Show snapshot info before deleting
			snapshots, err := mgr.ListSnapshots(snapshot.SnapshotFilter{})
			if err != nil {
				return fmt.Errorf("failed to list snapshots: %w", err)
			}

			for _, s := range snapshots {
				if s.ID == snapshotID {
					fmt.Printf("Deleting snapshot:\n")
					fmt.Printf("  ID:       %s\n", s.ID)
					fmt.Printf("  Task ID:  %s\n", s.TaskID)
					fmt.Printf("  Created:  %s\n", s.CreatedAt.Format(time.RFC3339))
					fmt.Printf("  Size:     %s\n", snapshotFormatBytes(s.SizeBytes))
					break
				}
			}

			if err := mgr.DeleteSnapshot(ctx, snapshotID); err != nil {
				return fmt.Errorf("failed to delete snapshot: %w", err)
			}

			fmt.Println("Snapshot deleted.")

			return nil
		},
	}

	return cmd
}

// --- cleanup ---

func newSnapshotCleanupCommand() *cobra.Command {
	var maxAge string

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove old snapshots",
		Long: `Remove snapshots older than the specified age (default: from config or 7 days).

Example:
  swarmcracker vm snapshot cleanup
  swarmcracker vm snapshot cleanup --max-age 24h
  swarmcracker vm snapshot cleanup --max-age 3d`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadSnapshotConfig(cfgFile)
			if err != nil {
				return err
			}

			mgr, err := snapshot.NewManager(toSnapshotConfig(cfg.Snapshot))
			if err != nil {
				return fmt.Errorf("failed to create snapshot manager: %w", err)
			}

			// Parse max age
			var age time.Duration
			if maxAge != "" {
				parsed, err := time.ParseDuration(maxAge)
				if err != nil {
					return fmt.Errorf("invalid max-age format: %w (use Go duration: 24h, 48h, 168h, etc.)", err)
				}
				age = parsed
			}

			ctx := context.Background()
			removed, freed, err := mgr.CleanupOldSnapshots(ctx, age)
			if err != nil {
				return fmt.Errorf("failed to cleanup snapshots: %w", err)
			}

			if removed == 0 {
				fmt.Println("No old snapshots to clean up.")
				return nil
			}

			fmt.Printf("Cleaned up %d snapshot(s), freed %s\n", removed, snapshotFormatBytes(freed))

			return nil
		},
	}

	cmd.Flags().StringVar(&maxAge, "max-age", "", "Maximum snapshot age (Go duration, e.g. 24h, 168h)")

	return cmd
}

// --- Helpers ---

// validateSnapshotIDArg rejects a snapshot ID that is not one safe path segment
// before any configuration is loaded or any directory is joined. The shape is
// snapshot.ValidSnapshotID, the same rule the package enforces, so the CLI and
// the store cannot drift. A CLI usage mistake is CodeUsage, which exits 2; the
// package refuses the same shapes with ErrInvalidSnapshotID as defence in depth.
func validateSnapshotIDArg(id string) error {
	if snapshot.ValidSnapshotID(id) {
		return nil
	}
	return output.NewErrorf(output.CodeUsage,
		"invalid snapshot ID %q: want snap- followed by 16 lowercase hex characters", id).
		WithDetails(map[string]any{"parameter": "snapshot-id"})
}

// loadSnapshotConfig loads configuration with snapshot defaults.
func loadSnapshotConfig(path string) (*config.Config, error) {
	cfg, err := loadConfigWithOverrides(path)
	if err != nil {
		// If file doesn't exist, use defaults
		if os.IsNotExist(err) {
			cfg = &config.Config{}
			cfg.SetDefaults()
		} else {
			return nil, err
		}
	}
	return cfg, nil
}

// toSnapshotConfig converts config.SnapshotConfig to snapshot.SnapshotConfig.
func toSnapshotConfig(c config.SnapshotConfig) snapshot.SnapshotConfig {
	return snapshot.SnapshotConfig{
		Enabled:      c.Enabled,
		SnapshotDir:  c.SnapshotDir,
		MaxSnapshots: c.MaxSnapshots,
		MaxAge:       c.MaxAge.ToDuration(),
		AutoSnapshot: c.AutoSnapshot,
		Compress:     c.Compress,
	}
}

func snapshotFormatBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
