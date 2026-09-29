package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/golden"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// newVMCommand creates the VM command group
func newVMCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vm",
		Short: "Manage Firecracker microVMs",
		Long: `Manage Firecracker microVMs in the SwarmCracker cluster.

These commands provide VM-level operations like creating, listing, stopping, and viewing logs.`,
	}

	// Add subcommands
	cmd.AddCommand(newVMCreateCommand())
	cmd.AddCommand(newVMListCommand())
	cmd.AddCommand(newVMStopCommand())
	cmd.AddCommand(newVMLogsCommand())
	cmd.AddCommand(newVMSnapshotCommand())
	cmd.AddCommand(newVMAttachCommand())
	// status is also available at the top level (`swarmcracker status`); expose
	// it under `vm` too for consistency.
	cmd.AddCommand(newStatusCommand())

	return cmd
}

// newVMCreateCommand creates the VM create command
func newVMCreateCommand() *cobra.Command {
	var (
		name      string
		vcpus     int
		memory    int
		network   string
		detach    bool
		env       []string
		goldenRef string
		goldenDir string
	)

	cmd := &cobra.Command{
		Use:   "create [image]",
		Short: "Create a Firecracker microVM from an OCI image or golden image",
		Long: `Create a Firecracker microVM from an OCI container image or a prebuilt golden image.

With an image argument, SwarmCracker pulls the container image, converts it to a
rootfs, and launches it as an isolated microVM using Firecracker.

With --golden, it boots a prebuilt golden image (see 'swarmcracker image build')
with its own init (systemd/OpenRC), so container runtimes such as Docker can run
inside the microVM.

Example:
  swarmcracker vm create alpine:latest
  swarmcracker vm create --name my-vm --cpu 2 --memory 1024 nginx:latest
  swarmcracker vm create --golden ubuntu-24.04-docker
  swarmcracker vm create --golden ubuntu-24.04-docker@1.0.0 --memory 4096 -d`,
		Args: cobra.MaximumNArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			imageRef := ""
			if len(args) > 0 {
				imageRef = args[0]
			}
			if goldenRef == "" && imageRef == "" {
				return fmt.Errorf("provide an image argument or --golden <name[@version]>")
			}
			if goldenRef != "" && imageRef != "" {
				return fmt.Errorf("--golden and an image argument are mutually exclusive")
			}

			// Load configuration
			cfg, err := loadConfigWithOverrides(cfgFile)
			if err != nil {
				return fmt.Errorf("failed to load configuration: %w", err)
			}

			// Resolve a golden image, if requested, into task annotations the
			// executor and translator understand.
			var goldenAnnotations map[string]string
			if goldenRef != "" {
				if goldenDir == "" {
					goldenDir = defaultGoldenDir()
				}
				art, err := golden.Resolve(goldenDir, goldenRef)
				if err != nil {
					return err
				}
				imageRef = "golden:" + art.Ref
				goldenAnnotations = map[string]string{
					types.AnnotationRootfs:         art.Path,
					types.AnnotationPrebuiltRootfs: "true",
					types.AnnotationGolden:         art.Ref,
				}
				if len(art.Metadata.Init.BootArgs) > 0 {
					goldenAnnotations[types.AnnotationBootArgs] = strings.Join(art.Metadata.Init.BootArgs, " ")
				}
				// A full-OS guest needs more headroom than the single-workload default.
				if !cmd.Flags().Changed("cpu") && vcpus < 2 {
					vcpus = 2
				}
				if !cmd.Flags().Changed("memory") && memory < 2048 {
					memory = 2048
				}
				fmt.Printf("Using golden image %s (%s)\n", art.Ref, art.Path)
			}

			// Create executor
			exec, err := createExecutor(cfg)
			if err != nil {
				return fmt.Errorf("failed to create executor: %w", err)
			}
			defer exec.Close()

			// Create state manager for tracking VMs
			stateMgr, err := runtime.NewStateManager("")
			if err != nil {
				log.Warn().Err(err).Msg("Failed to create state manager, VM tracking disabled")
			}

			// Create a mock task
			task := createMockTask(imageRef, vcpus, memory, env)
			if goldenAnnotations != nil {
				task.Annotations = goldenAnnotations
			}

			// Override task ID with name if provided
			if name != "" {
				task.ID = name
			}

			// Prepare context with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			// Setup signal handling for cleanup
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				sig := <-sigCh
				log.Info().Str("signal", sig.String()).Msg("Received interrupt signal, cleaning up...")
				cancel()
				if err := exec.Remove(context.Background(), task); err != nil {
					log.Warn().Err(err).Msg("Failed to remove task during interrupt cleanup")
				}
				os.Exit(1)
			}()

			// Prepare the task
			log.Info().Str("task_id", task.ID).Msg("Preparing task...")
			if err := exec.Prepare(ctx, task); err != nil {
				return fmt.Errorf("failed to prepare task: %w", err)
			}

			// Start the task
			log.Info().Str("task_id", task.ID).Msg("Starting task...")
			if err := exec.Start(ctx, task); err != nil {
				return fmt.Errorf("failed to start task: %w", err)
			}

			if detach {
				log.Info().Str("task_id", task.ID).Msg("Task started in detached mode")

				// Save VM state for tracking
				if stateMgr != nil {
					// Get task details for state
					container, ok := task.Spec.Runtime.(*types.Container)
					if !ok {
						log.Warn().Str("task_id", task.ID).Msg("Task runtime is not a container, skipping state save")
						return nil
					}

					vmState := &runtime.VMState{
						ID:         task.ID,
						Image:      container.Image,
						Command:    append(container.Command, container.Args...),
						Status:     "running",
						VCPUs:      vcpus,
						MemoryMB:   memory,
						KernelPath: cfg.Executor.KernelPath,
						LogPath:    filepath.Join(stateMgr.GetLogDir(), task.ID+".log"),
					}

					// Get network info if available
					if len(task.Networks) > 0 {
						vmState.NetworkID = task.Networks[0].Network.ID
						vmState.IPAddresses = task.Networks[0].Addresses
					}

					// Add to state manager
					if err := stateMgr.Add(vmState); err != nil {
						log.Warn().Err(err).Msg("Failed to save VM state")
					} else {
						log.Info().Str("vm_id", task.ID).Msg("VM state saved")
					}
				}

				// Output the VM ID
				fmt.Println(task.ID)
				return nil
			}

			// Wait for completion
			log.Info().Str("task_id", task.ID).Msg("Waiting for task to complete...")
			status, err := exec.Wait(ctx, task)
			if err != nil {
				log.Error().Err(err).Msg("Task failed")
				return fmt.Errorf("task execution failed: %w", err)
			}

			log.Info().
				Str("task_id", task.ID).
				Str("state", taskStateString(status.State)).
				Msg("Task completed")

			// Output the VM ID
			fmt.Println(task.ID)

			// Cleanup
			log.Info().Str("task_id", task.ID).Msg("Cleaning up...")
			if err := exec.Remove(ctx, task); err != nil {
				log.Warn().Err(err).Msg("Cleanup failed (task may still be running)")
			}

			return nil
		},
	}

	// Flags
	cmd.Flags().StringVarP(&name, "name", "n", "", "Name for the VM (auto-generated if not specified)")
	cmd.Flags().IntVar(&vcpus, "cpu", 1, "Number of vCPUs to allocate")
	cmd.Flags().IntVarP(&memory, "memory", "m", 512, "Memory in MB to allocate")
	cmd.Flags().StringVar(&network, "network", "", "Network to attach the VM to")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "Run in detached mode (don't wait for completion)")
	cmd.Flags().StringArrayVarP(&env, "env", "e", []string{}, "Environment variables (e.g., -e KEY=value)")
	cmd.Flags().StringVar(&goldenRef, "golden", "", "Boot a prebuilt golden image (name or name@version) instead of an OCI image")
	cmd.Flags().StringVar(&goldenDir, "golden-dir", "", "Directory containing golden image artifacts (default: <rootfs-dir>/golden)")

	return cmd
}

// newVMListCommand creates the VM list command
func newVMListCommand() *cobra.Command {
	// Reuse existing list command
	cmd := newListCommand()
	return cmd
}

// newVMStopCommand creates the VM stop command
func newVMStopCommand() *cobra.Command {
	// Reuse existing stop command
	cmd := newStopCommand()
	return cmd
}

// newVMLogsCommand creates the VM logs command
func newVMLogsCommand() *cobra.Command {
	// Reuse existing logs command
	cmd := newLogsCommand()
	return cmd
}

// newVMSnapshotCommand creates the VM snapshot command
func newVMSnapshotCommand() *cobra.Command {
	// Reuse existing snapshot command
	cmd := newSnapshotCommand()
	return cmd
}
