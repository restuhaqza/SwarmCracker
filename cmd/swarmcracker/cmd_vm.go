package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

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

	return cmd
}

// newVMCreateCommand creates the VM create command
func newVMCreateCommand() *cobra.Command {
	var (
		name    string
		vcpus   int
		memory  int
		network string
		detach  bool
		env     []string
	)

	cmd := &cobra.Command{
		Use:   "create <image>",
		Short: "Create a Firecracker microVM from an OCI image",
		Long: `Create a Firecracker microVM from an OCI container image.

This command pulls the specified container image, converts it to a rootfs,
and launches it as an isolated microVM using Firecracker.

Example:
  swarmcracker vm create alpine:latest
  swarmcracker vm create --name my-vm --cpu 2 --memory 1024 nginx:latest
  swarmcracker vm create -d --env FOO=bar alpine:latest`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			imageRef := args[0]

			// Load configuration
			cfg, err := loadConfigWithOverrides(cfgFile)
			if err != nil {
				return fmt.Errorf("failed to load configuration: %w", err)
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
				exec.Remove(context.Background(), task)
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
					container := task.Spec.Runtime.(*types.Container)

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