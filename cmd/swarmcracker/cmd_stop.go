package main

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	stopForce   bool
	stopTimeout int
)

// newStopCommand creates the stop command
func newStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <vm-id>",
		Short: "Stop a running microVM",
		Long: `Stop a running SwarmCracker microVM.

This command gracefully stops a running microVM by sending a shutdown signal.
If the VM doesn't stop within the timeout period, it will be forcibly terminated.

Example:
  swarmcracker stop nginx-1
  swarmcracker stop --force nginx-1
  swarmcracker stop --timeout 30 nginx-1`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStop(args[0])
		},
	}

	cmd.Flags().BoolVarP(&stopForce, "force", "f", false, "Force kill the VM (SIGKILL)")
	cmd.Flags().IntVar(&stopTimeout, "timeout", 10, "Graceful shutdown timeout in seconds")

	return cmd
}

// runStop executes the stop command
func runStop(vmID string) error {
	// Create state manager
	stateMgr, err := runtime.NewStateManager("")
	if err != nil {
		return fmt.Errorf("failed to create state manager: %w", err)
	}

	// Get VM state
	vmState, err := stateMgr.Get(vmID)
	if err != nil {
		return fmt.Errorf("VM not found: %s", vmID)
	}

	// Check if VM is already stopped
	if vmState.Status == "stopped" {
		fmt.Printf("VM %s is already stopped\n", vmID)
		return nil
	}

	// Check if VM is in error state
	if vmState.Status == "error" {
		fmt.Printf("VM %s is in error state, cleaning up...\n", vmID)
		return cleanupVM(stateMgr, vmState)
	}

	log.Info().
		Str("vm_id", vmID).
		Str("pid", fmt.Sprintf("%d", vmState.PID)).
		Bool("force", stopForce).
		Int("timeout", stopTimeout).
		Msg("Stopping VM")

	// Stop the VM
	if err := stopVM(vmState); err != nil {
		// Update state with error
		stateMgr.UpdateError(vmID, err.Error())
		return fmt.Errorf("failed to stop VM: %w", err)
	}

	// Update state to stopped
	if err := stateMgr.UpdateStatus(vmID, "stopped"); err != nil {
		log.Warn().Err(err).Msg("Failed to update VM status")
	}

	fmt.Printf("VM %s stopped successfully\n", vmID)

	return nil
}

// stopVM stops the VM process
func stopVM(vm *runtime.VMState) error {
	// Find the process
	process, err := os.FindProcess(vm.PID)
	if err != nil {
		return fmt.Errorf("process not found: %w", err)
	}

	// Check if process is still running
	if err := process.Signal(syscall.Signal(0)); err != nil {
		// Process already dead
		log.Info().Int("pid", vm.PID).Msg("Process already terminated")
		return nil
	}

	if stopForce {
		// Force kill immediately
		log.Info().Int("pid", vm.PID).Msg("Sending SIGKILL")
		if err := process.Kill(); err != nil {
			return fmt.Errorf("failed to kill process: %w", err)
		}

		// Wait for process to exit
		_, err := process.Wait()
		if err != nil {
			log.Warn().Err(err).Msg("Process wait returned error")
		}

		return nil
	}

	// Graceful shutdown
	log.Info().Int("pid", vm.PID).Msg("Sending SIGTERM")

	// Try SIGTERM first
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to send SIGTERM: %w", err)
	}

	// Wait for graceful shutdown with timeout
	timeout := time.Duration(stopTimeout) * time.Second
	done := make(chan error, 1)

	go func() {
		_, err := process.Wait()
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			log.Warn().Err(err).Msg("Process wait returned error")
		}
		log.Info().Int("pid", vm.PID).Msg("Process terminated gracefully")
		return nil

	case <-time.After(timeout):
		log.Warn().Int("pid", vm.PID).Msg("Graceful shutdown timeout, forcing kill")
		if err := process.Kill(); err != nil {
			return fmt.Errorf("failed to kill process after timeout: %w", err)
		}
		_, _ = process.Wait()
		return nil
	}
}

// cleanupVM cleans up VM resources
func cleanupVM(stateMgr *runtime.StateManager, vm *runtime.VMState) error {
	log.Info().Str("vm_id", vm.ID).Msg("Cleaning up VM resources")

	// Remove socket file if it exists
	if vm.SocketPath != "" {
		if _, err := os.Stat(vm.SocketPath); err == nil {
			if err := os.Remove(vm.SocketPath); err != nil {
				log.Warn().Err(err).Str("socket", vm.SocketPath).Msg("Failed to remove socket")
			}
		}
	}

	// Remove VM from state
	if err := stateMgr.Remove(vm.ID); err != nil {
		log.Warn().Err(err).Msg("Failed to remove VM from state")
	}

	return nil
}
