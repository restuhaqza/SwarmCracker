package main

import (
	"fmt"
	"os"
	"path/filepath"
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
		// The VM is not CLI-managed. If it is a running daemon/service VM,
		// refuse to kill the process: SwarmKit tracks its desired state and
		// would immediately recreate the task.
		if _, runningErr := resolveRunningVM(vmID); runningErr == nil {
			return fmt.Errorf("VM %s is managed by SwarmKit; stop it with 'swarmcracker service scale <service>=0' or 'swarmcracker service rm <service>'", vmID)
		}
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
		if stateErr := stateMgr.UpdateError(vmID, err.Error()); stateErr != nil {
			log.Warn().Err(stateErr).Msg("Failed to record stop error in VM state")
		}
		return fmt.Errorf("failed to stop VM: %w", err)
	}

	// Update state to stopped
	if err := stateMgr.UpdateStatus(vmID, "stopped"); err != nil {
		log.Warn().Err(err).Msg("Failed to update VM status")
	}

	fmt.Printf("VM %s stopped successfully\n", vmID)

	return nil
}

// stopVM stops the VM process.
func stopVM(vm *runtime.VMState) error {
	pid := vm.PID
	if pid <= 0 {
		// State predates PID tracking (or the create raced). Re-resolve from
		// the live process table by task ID, then by socket path.
		pid = runtime.FindFirecrackerPID(vm.ID)
		if pid == 0 && vm.SocketPath != "" {
			pid = runtime.FindFirecrackerPIDBySocket(vm.SocketPath)
		}
	}

	if pid <= 0 {
		// No process found. If the API socket is gone too, the VM is already
		// stopped; clean up any leftover sockets and report success.
		if vm.SocketPath == "" || !runtime.IsVMSocketAlive(vm.SocketPath, 500*time.Millisecond) {
			log.Info().Str("vm_id", vm.ID).Msg("No running process; VM already stopped")
			removeVMSockets(vm)
			return nil
		}
		return fmt.Errorf("could not determine the PID of running VM %s", vm.ID)
	}

	// Find the process
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("process not found: %w", err)
	}

	// Check if process is still running
	if err := process.Signal(syscall.Signal(0)); err != nil {
		// Process already dead
		log.Info().Int("pid", pid).Msg("Process already terminated")
		removeVMSockets(vm)
		//nolint:nilerr
		return nil
	}

	if stopForce {
		// Force kill immediately
		log.Info().Int("pid", pid).Msg("Sending SIGKILL")
		if err := process.Kill(); err != nil {
			return fmt.Errorf("failed to kill process: %w", err)
		}

		// Wait for process to exit
		if _, err := process.Wait(); err != nil {
			log.Warn().Err(err).Msg("Process wait returned error")
		}
		removeVMSockets(vm)
		return nil
	}

	// Graceful shutdown
	log.Info().Int("pid", pid).Msg("Sending SIGTERM")

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
		log.Info().Int("pid", pid).Msg("Process terminated gracefully")
		removeVMSockets(vm)
		return nil

	case <-time.After(timeout):
		log.Warn().Int("pid", pid).Msg("Graceful shutdown timeout, forcing kill")
		if err := process.Kill(); err != nil {
			return fmt.Errorf("failed to kill process after timeout: %w", err)
		}
		_, _ = process.Wait()
		removeVMSockets(vm)
		return nil
	}
}

// removeVMSockets removes a VM's Firecracker API and console sockets, if any.
func removeVMSockets(vm *runtime.VMState) {
	if vm.SocketPath == "" {
		return
	}
	if err := os.Remove(vm.SocketPath); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Str("socket", vm.SocketPath).Msg("Failed to remove VM socket")
	}
	consoleSock := filepath.Join(filepath.Dir(vm.SocketPath), vm.ID+".console.sock")
	if err := os.Remove(consoleSock); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Str("socket", consoleSock).Msg("Failed to remove VM console socket")
	}
}

// cleanupVM cleans up VM resources
func cleanupVM(stateMgr *runtime.StateManager, vm *runtime.VMState) error {
	log.Info().Str("vm_id", vm.ID).Msg("Cleaning up VM resources")

	// Remove the VM's API and console sockets if present.
	removeVMSockets(vm)

	// Remove VM from state
	if err := stateMgr.Remove(vm.ID); err != nil {
		log.Warn().Err(err).Msg("Failed to remove VM from state")
	}

	return nil
}
