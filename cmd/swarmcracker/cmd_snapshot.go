package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
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
  swarmcracker snapshot create <task-id>
  swarmcracker snapshot restore <snapshot-id>
  swarmcracker snapshot list
  swarmcracker snapshot delete <snapshot-id>
  swarmcracker snapshot cleanup`,
	}

	cmd.AddCommand(newSnapshotCreateCommand())
	cmd.AddCommand(newSnapshotRestoreCommand())
	cmd.AddCommand(newSnapshotListCommand())
	cmd.AddCommand(newSnapshotDeleteCommand())
	cmd.AddCommand(newSnapshotCleanupCommand())

	return cmd
}

// --- create ---

func newSnapshotCreateCommand() *cobra.Command {
	var (
		socketPath string
		serviceID  string
		nodeID     string
		vcpus      int
		memoryMB   int
		rootfsPath string
	)

	cmd := &cobra.Command{
		Use:   "create <task-id>",
		Short: "Create a snapshot of a running VM",
		Long: `Snapshot a running microVM identified by its task ID.

The VM must be running and accessible via its Firecracker API socket.

Example:
  swarmcracker snapshot create task-123
  swarmcracker snapshot create task-123 --socket /var/run/firecracker/task-123.sock`,
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

			opts := snapshot.CreateOptions{
				ServiceID:  serviceID,
				NodeID:     nodeID,
				VCPUCount:  vcpus,
				MemoryMB:   memoryMB,
				RootfsPath: rootfsPath,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	cmd.Flags().StringVar(&rootfsPath, "rootfs", "", "Rootfs path (for metadata)")

	return cmd
}

// --- restore ---

func newSnapshotRestoreCommand() *cobra.Command {
	var socketPath string

	cmd := &cobra.Command{
		Use:   "restore <snapshot-id>",
		Short: "Restore a VM from a snapshot",
		Long: `Restore a microVM from a previously created snapshot.

The restored VM will start with the exact memory state and configuration
it had when the snapshot was taken.

Example:
  swarmcracker snapshot restore snap-abc123def4567890
  swarmcracker snapshot restore snap-abc123def4567890 --socket /var/run/firecracker/task-123.sock`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshotID := args[0]

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

			// Resolve socket path
			if socketPath == "" {
				socketPath = filepath.Join(cfg.Executor.SocketDir, target.TaskID+"-restored.sock")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			if err := mgr.RestoreFromSnapshot(ctx, target, socketPath); err != nil {
				return fmt.Errorf("failed to restore snapshot: %w", err)
			}

			fmt.Printf("VM restored from snapshot\n")
			fmt.Printf("  Snapshot: %s\n", target.ID)
			fmt.Printf("  Task ID:  %s\n", target.TaskID)
			fmt.Printf("  Socket:   %s\n", socketPath)

			return nil
		},
	}

	cmd.Flags().StringVar(&socketPath, "socket", "", "Path for new Firecracker API socket")

	return cmd
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
  swarmcracker snapshot list
  swarmcracker snapshot list --service nginx
  swarmcracker snapshot list --task task-123`,
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
  swarmcracker snapshot delete snap-abc123def4567890`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshotID := args[0]

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
  swarmcracker snapshot cleanup
  swarmcracker snapshot cleanup --max-age 24h
  swarmcracker snapshot cleanup --max-age 3d`,
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
		MaxAge:       c.MaxAge,
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
