package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/storage"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newVolumeCommand() *cobra.Command {
	var volumesDir string

	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Manage persistent volumes",
		Long: `Manage persistent volumes for SwarmCracker microVMs.

Volumes persist data across VM restarts and can be mounted into microVMs
at any path. Supports directory-based and block device (ext4) backends.`,
	}

	cmd.PersistentFlags().StringVarP(&volumesDir, "volumes-dir", "d", "", "Volumes storage directory (default: /var/lib/swarmcracker/volumes)")

	cmd.AddCommand(newVolumeCreateCommand(&volumesDir))
	cmd.AddCommand(newVolumeListCommand(&volumesDir))
	cmd.AddCommand(newVolumeInspectCommand(&volumesDir))
	cmd.AddCommand(newVolumeDeleteCommand(&volumesDir))
	cmd.AddCommand(newVolumeSnapshotCommand(&volumesDir))
	cmd.AddCommand(newVolumeRestoreCommand(&volumesDir))
	cmd.AddCommand(newVolumeExportCommand(&volumesDir))
	cmd.AddCommand(newVolumeImportCommand(&volumesDir))

	return cmd
}

// newVolumeManager creates a VolumeManager using the given directory pointer.
// If *dir is empty, uses the default path.
func newVolumeManager(dir *string) (*storage.VolumeManager, error) {
	d := ""
	if dir != nil && *dir != "" {
		d = *dir
	}
	return storage.NewVolumeManager(d)
}

// --- volume create ---

func newVolumeCreateCommand(volumesDir *string) *cobra.Command {
	var (
		sizeMB     int
		volType    string
		driverOpts []string
	)

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new volume",
		Long: `Create a new persistent volume.

Examples:
  swarmcracker volume create my-data --size 1024
  swarmcracker volume create db-vol --size 5120 --type block
  swarmcracker volume create cache --size 100`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			vmm, err := newVolumeManager(volumesDir)
			if err != nil {
				return fmt.Errorf("failed to initialize volume manager: %w", err)
			}

			t := storage.VolumeType(volType)
			if t == "" {
				t = storage.VolumeTypeDir
			}

			opts := storage.CreateOptions{
				Type:   t,
				SizeMB: sizeMB,
			}

			if len(driverOpts) > 0 {
				opts.DriverOpts = make(map[string]string)
				for _, opt := range driverOpts {
					parts := strings.SplitN(opt, "=", 2)
					if len(parts) == 2 {
						opts.DriverOpts[parts[0]] = parts[1]
					}
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			vol, err := vmm.CreateVolumeWithOptions(ctx, name, "", opts)
			if err != nil {
				return fmt.Errorf("failed to create volume: %w", err)
			}

			fmt.Printf("Volume %q created (type=%s, size=%dMB)\n", vol.Name, t, sizeMB)
			return nil
		},
	}

	cmd.Flags().IntVarP(&sizeMB, "size", "s", 0, "Volume size in MB (0=unlimited for dir, 1024 default for block)")
	cmd.Flags().StringVarP(&volType, "type", "t", "dir", "Volume type: dir or block")
	cmd.Flags().StringArrayVar(&driverOpts, "opt", nil, "Driver-specific options (key=value)")

	return cmd
}

// --- volume ls ---

func newVolumeListCommand(volumesDir *string) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List volumes",
		Long: `List all persistent volumes.

Examples:
  swarmcracker volume ls
  swarmcracker volume ls --json`,
		Args: cobra.NoArgs,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			vmm, err := newVolumeManager(volumesDir)
			if err != nil {
				return fmt.Errorf("failed to initialize volume manager: %w", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			infos, err := vmm.ListVolumeInfos(ctx)
			if err != nil {
				return fmt.Errorf("failed to list volumes: %w", err)
			}

			if len(infos) == 0 {
				fmt.Println("No volumes found.")
				return nil
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(infos)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tSIZE\tUSED\tCREATED\tTASK")
			for _, info := range infos {
				fmt.Fprintf(w, "%s\t%s\t%dMB\t%dMB\t%s\t%s\n",
					info.Name,
					info.Type,
					info.SizeMB,
					info.UsedMB,
					info.CreatedAt.Format("2006-01-02 15:04"),
					info.TaskID,
				)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

// --- volume inspect ---

func newVolumeInspectCommand(volumesDir *string) *cobra.Command {
	var volType string

	cmd := &cobra.Command{
		Use:   "inspect <name>",
		Short: "Show detailed volume information",
		Long: `Show detailed information about a volume including usage, metadata, and capacity.

Examples:
  swarmcracker volume inspect my-data`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			vmm, err := newVolumeManager(volumesDir)
			if err != nil {
				return fmt.Errorf("failed to initialize volume manager: %w", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			t := storage.VolumeType(volType)
			info, err := vmm.GetVolumeInfo(ctx, name, t)
			if err != nil {
				if t == storage.VolumeTypeDir {
					info, err = vmm.GetVolumeInfo(ctx, name, storage.VolumeTypeBlock)
				} else {
					info, err = vmm.GetVolumeInfo(ctx, name, storage.VolumeTypeDir)
				}
				if err != nil {
					return fmt.Errorf("volume not found: %s", name)
				}
			}

			drv, drvErr := vmm.GetDriver(info.Type)
			if drvErr == nil {
				usedBytes, limitBytes, capErr := drv.Capacity(ctx, name)
				fmt.Printf("Capacity: %d / %d bytes\n", usedBytes, limitBytes)
				if capErr != nil {
					log.Warn().Err(capErr).Msg("Failed to get capacity")
				}
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(info)
		},
	}

	cmd.Flags().StringVarP(&volType, "type", "t", "", "Volume type (auto-detected if omitted)")
	return cmd
}

// --- volume rm ---

func newVolumeDeleteCommand(volumesDir *string) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"delete"},
		Short:   "Delete a volume",
		Long: `Delete a volume and all its data. This action cannot be undone.

Examples:
  swarmcracker volume rm old-vol
  swarmcracker volume rm old-vol --force`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if !force {
				fmt.Fprintf(os.Stderr, "WARNING: This will delete volume %q and all its data.\n", name)
				fmt.Fprintf(os.Stderr, "Use --force to skip confirmation.\n")
				return fmt.Errorf("cancelled")
			}

			vmm, err := newVolumeManager(volumesDir)
			if err != nil {
				return fmt.Errorf("failed to initialize volume manager: %w", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := vmm.DeleteVolume(ctx, name); err != nil {
				return fmt.Errorf("failed to delete volume: %w", err)
			}

			fmt.Printf("Volume %q deleted\n", name)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	return cmd
}

// --- volume snapshot ---

func newVolumeSnapshotCommand(volumesDir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot <name>",
		Short: "Create a snapshot of a volume",
		Long: `Create a point-in-time snapshot of a volume.

For directory volumes, creates a tar.gz archive.
For block volumes, creates a raw copy of the ext4 image.

Examples:
  swarmcracker volume snapshot my-data`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			vmm, err := newVolumeManager(volumesDir)
			if err != nil {
				return fmt.Errorf("failed to initialize volume manager: %w", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			snap, err := vmm.SnapshotVolume(ctx, name)
			if err != nil {
				return fmt.Errorf("failed to create snapshot: %w", err)
			}

			fmt.Printf("Snapshot created: %s\n", snap.ID)
			fmt.Printf("  Volume:  %s\n", snap.Volume)
			fmt.Printf("  Path:    %s\n", snap.Path)
			fmt.Printf("  Size:    %d MB\n", snap.SizeMB)
			fmt.Printf("  Created: %s\n", snap.CreatedAt.Format(time.RFC3339))
			return nil
		},
	}

	return cmd
}

// --- volume restore ---

func newVolumeRestoreCommand(volumesDir *string) *cobra.Command {
	var snapPath string

	cmd := &cobra.Command{
		Use:   "restore <name>",
		Short: "Restore a volume from a snapshot",
		Long: `Restore a volume from a previously created snapshot.

This replaces all current volume data with the snapshot contents.

Examples:
  swarmcracker volume restore my-data --snapshot .snapshots/my-data-1775544834416.tar.gz`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if snapPath == "" {
				return fmt.Errorf("--snapshot is required")
			}

			vmm, err := newVolumeManager(volumesDir)
			if err != nil {
				return fmt.Errorf("failed to initialize volume manager: %w", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			if _, err := os.Stat(snapPath); err != nil {
				return fmt.Errorf("snapshot file not found: %w", err)
			}

			snap := &storage.Snapshot{
				ID:     filepathBase(snapPath),
				Volume: name,
				Path:   snapPath,
			}

			if err := vmm.RestoreVolume(ctx, name, snap); err != nil {
				return fmt.Errorf("failed to restore volume: %w", err)
			}

			fmt.Printf("Volume %q restored from %s\n", name, snapPath)
			return nil
		},
	}

	cmd.Flags().StringVarP(&snapPath, "snapshot", "s", "", "Path to snapshot file (required)")
	return cmd
}

// --- volume export ---

func newVolumeExportCommand(volumesDir *string) *cobra.Command {
	var outputFile string

	cmd := &cobra.Command{
		Use:   "export <name>",
		Short: "Export volume data to a file",
		Long: `Export volume data to a tar.gz file for backup or transfer.

Examples:
  swarmcracker volume export my-data -o backup.tar.gz`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if outputFile == "" {
				outputFile = name + ".tar.gz"
			}

			vmm, err := newVolumeManager(volumesDir)
			if err != nil {
				return fmt.Errorf("failed to initialize volume manager: %w", err)
			}

			d, err := vmm.GetDriver(storage.VolumeTypeDir)
			if err != nil {
				return fmt.Errorf("directory driver not available: %w", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			f, err := os.Create(outputFile)
			if err != nil {
				return fmt.Errorf("create output file: %w", err)
			}
			defer f.Close()

			if err := d.Export(ctx, name, f); err != nil {
				os.Remove(outputFile)
				return fmt.Errorf("export failed: %w", err)
			}

			info, _ := f.Stat()
			fmt.Printf("Exported volume %q to %s (%d bytes)\n", name, outputFile, info.Size())
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output file path (default: <name>.tar.gz)")
	return cmd
}

// --- volume import ---

func newVolumeImportCommand(volumesDir *string) *cobra.Command {
	var (
		sizeMB  int
		volType string
	)

	cmd := &cobra.Command{
		Use:   "import <name> <archive>",
		Short: "Import volume data from a file",
		Long: `Import volume data from a tar.gz archive created with 'volume export'.

Examples:
  swarmcracker volume import my-data backup.tar.gz
  swarmcracker volume import db-vol backup.tar.gz --size 5120`,
		Args: cobra.ExactArgs(2),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			archive := args[1]

			if _, err := os.Stat(archive); err != nil {
				return fmt.Errorf("archive file not found: %w", err)
			}

			vmm, err := newVolumeManager(volumesDir)
			if err != nil {
				return fmt.Errorf("failed to initialize volume manager: %w", err)
			}

			d, err := vmm.GetDriver(storage.VolumeType(volType))
			if err != nil {
				return fmt.Errorf("driver not available: %w", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			f, err := os.Open(archive)
			if err != nil {
				return fmt.Errorf("open archive: %w", err)
			}
			defer f.Close()

			if err := d.Import(ctx, name, f, sizeMB); err != nil {
				return fmt.Errorf("import failed: %w", err)
			}

			fmt.Printf("Volume %q imported from %s\n", name, archive)
			return nil
		},
	}

	cmd.Flags().IntVarP(&sizeMB, "size", "s", 0, "Volume size in MB")
	cmd.Flags().StringVarP(&volType, "type", "t", "dir", "Volume type: dir or block")
	return cmd
}

// filepathBase returns the last element of a path.
func filepathBase(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
