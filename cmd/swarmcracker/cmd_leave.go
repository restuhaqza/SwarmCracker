package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// leaveConfig holds the configuration for leaving a cluster
type leaveConfig struct {
	Purge       bool
	Force       bool
	KeepNetwork bool
	StateDir    string
	ConfigDir   string
	BridgeName  string
}

func newLeaveCommand() *cobra.Command {
	cfg := &leaveConfig{}

	cmd := &cobra.Command{
		Use:   "leave",
		Short: "Leave the SwarmCracker cluster",
		Long: `Leave the SwarmCracker cluster gracefully.

This command will:
  1. Stop all running microVMs on this node
  2. Stop the worker systemd service
  3. Remove the systemd service file
  4. Clear cluster state (with --purge)
  5. Optionally remove network bridge (with --cleanup-network)

Examples:
  # Graceful leave (keeps state for rejoin)
  swarmcracker leave

  # Leave and purge all state
  swarmcracker leave --purge

  # Force leave when manager unreachable
  swarmcracker leave --force

  # Full cleanup including network
  swarmcracker leave --purge --cleanup-network`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLeave(cfg)
		},
	}

	cmd.Flags().BoolVar(&cfg.Purge, "purge", false, "Remove all state and config files")
	cmd.Flags().BoolVar(&cfg.Force, "force", false, "Force leave even if manager unreachable")
	cmd.Flags().BoolVar(&cfg.KeepNetwork, "keep-network", false, "Keep bridge and TAP devices (default: remove)")
	cmd.Flags().StringVar(&cfg.StateDir, "state-dir", "/var/lib/swarmkit", "State directory")
	cmd.Flags().StringVar(&cfg.ConfigDir, "config-dir", "/etc/swarmcracker", "Configuration directory")
	cmd.Flags().StringVar(&cfg.BridgeName, "bridge-name", "swarm-br0", "Bridge name for VM networking")

	return cmd
}

func runLeave(cfg *leaveConfig) error {
	fmt.Println()
	fmt.Println("🚪 Leaving SwarmCracker Cluster")
	fmt.Println(strings.Repeat("─", 50))

	// Check if worker service exists
	servicePath := "/etc/systemd/system/swarmcracker-worker.service"
	if _, err := os.Stat(servicePath); os.IsNotExist(err) {
		fmt.Println("\n⚠ No worker service found. This node may not be part of a cluster.")
		fmt.Println("If you want to clean up leftover state, use 'swarmcracker reset'")
		return nil
	}

	// Step 1: Stop running VMs
	PrintProgress(1, 5, "Stopping running microVMs...")
	if err := stopRunningVMs(cfg); err != nil {
		if !cfg.Force {
			PrintProgressFailed(1, 5, "Stopping VMs", err)
			return fmt.Errorf("failed to stop VMs: %w (use --force to continue)", err)
		}
		log.Warn().Err(err).Msg("Failed to stop VMs (continuing with --force)")
	}
	PrintProgressComplete(1, 5, "MicroVMs stopped")

	// Step 2: Stop systemd service
	PrintProgress(2, 5, "Stopping worker service...")
	if err := stopWorkerService(cfg); err != nil {
		if !cfg.Force {
			PrintProgressFailed(2, 5, "Stopping service", err)
			return fmt.Errorf("failed to stop service: %w", err)
		}
		log.Warn().Err(err).Msg("Failed to stop service (continuing with --force)")
	}
	PrintProgressComplete(2, 5, "Worker service stopped")

	// Step 3: Disable and remove service file
	PrintProgress(3, 5, "Removing systemd service...")
	if err := removeWorkerService(cfg); err != nil {
		PrintProgressFailed(3, 5, "Removing service", err)
		return fmt.Errorf("failed to remove service: %w", err)
	}
	PrintProgressComplete(3, 5, "Systemd service removed")

	// Step 4: Clear state
	PrintProgress(4, 5, "Clearing cluster state...")
	if err := clearState(cfg); err != nil {
		PrintProgressFailed(4, 5, "Clearing state", err)
		return fmt.Errorf("failed to clear state: %w", err)
	}
	PrintProgressComplete(4, 5, "Cluster state cleared")

	// Step 5: Remove network (optional)
	if !cfg.KeepNetwork {
		PrintProgress(5, 5, "Removing network bridge...")
		if err := removeNetwork(cfg); err != nil {
			log.Warn().Err(err).Msg("Failed to remove network (may have active TAPs)")
		}
		PrintProgressComplete(5, 5, "Network bridge removed")
	} else {
		PrintProgressComplete(5, 5, "Network preserved (--keep-network)")
	}

	// Reload systemd
	exec.Command("systemctl", "daemon-reload").Run()

	// Success message
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("✅ Successfully left the cluster!")
	fmt.Println("========================================")
	fmt.Println()
	fmt.Println("To rejoin a cluster:")
	fmt.Println("  swarmcracker join <manager-addr> --token <token>")
	fmt.Println()
	if cfg.Purge {
		fmt.Println("⚠ State and config files have been removed.")
		fmt.Println("You will need to reconfigure before joining.")
	}
	fmt.Println("========================================")

	return nil
}

func stopRunningVMs(cfg *leaveConfig) error {
	return stopAllFirecrackerVMs()
}

// stopAllFirecrackerVMs kills all Firecracker processes (shared by leave/deinit)
func stopAllFirecrackerVMs() error {
	log.Info().Msg("Stopping all running Firecracker VMs...")

	// Find all Firecracker processes
	cmd := exec.Command("pgrep", "-f", "firecracker")
	output, err := cmd.Output()
	if err != nil {
		// No processes found - that's OK
		log.Debug().Msg("No Firecracker processes found")
		return nil
	}

	pids := strings.Fields(strings.TrimSpace(string(output)))
	if len(pids) == 0 {
		return nil
	}

	log.Info().Int("count", len(pids)).Msg("Found running VMs")

	// Kill each process gracefully
	for _, pid := range pids {
		log.Debug().Str("pid", pid).Msg("Stopping Firecracker process")

		// Try SIGTERM first
		exec.Command("kill", "-TERM", pid).Run()
	}

	// Wait for graceful shutdown
	time.Sleep(3 * time.Second)

	// Check if any still running, force kill
	cmd = exec.Command("pgrep", "-f", "firecracker")
	output, err = cmd.Output()
	if err == nil && len(strings.TrimSpace(string(output))) > 0 {
		log.Warn().Msg("Some VMs didn't stop gracefully, forcing kill")
		exec.Command("pkill", "-9", "-f", "firecracker").Run()
	}

	return nil
}

func stopWorkerService(cfg *leaveConfig) error {
	log.Info().Msg("Stopping swarmcracker-worker service...")

	// Stop the service
	cmd := exec.Command("systemctl", "stop", "swarmcracker-worker.service")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to stop service: %w\nOutput: %s", err, string(output))
	}

	log.Info().Msg("Worker service stopped")
	return nil
}

func removeWorkerService(cfg *leaveConfig) error {
	log.Info().Msg("Disabling and removing systemd service...")

	// Disable the service
	exec.Command("systemctl", "disable", "swarmcracker-worker.service").Run()

	// Remove service file
	servicePath := "/etc/systemd/system/swarmcracker-worker.service"
	if _, err := os.Stat(servicePath); err == nil {
		if err := os.Remove(servicePath); err != nil {
			return fmt.Errorf("failed to remove service file: %w", err)
		}
	}

	// Also remove old service name if exists
	oldServicePath := "/etc/systemd/system/swarmd-firecracker.service"
	if _, err := os.Stat(oldServicePath); err == nil {
		os.Remove(oldServicePath)
	}

	log.Info().Msg("Systemd service removed")
	return nil
}

func clearState(cfg *leaveConfig) error {
	log.Info().Msg("Clearing cluster state...")

	statePaths := []string{
		cfg.StateDir,
		"/var/lib/swarmkit",
		"/var/run/swarmkit",
		"/var/run/firecracker",
	}

	for _, path := range statePaths {
		if _, err := os.Stat(path); err == nil {
			if err := os.RemoveAll(path); err != nil {
				log.Warn().Err(err).Str("path", path).Msg("Failed to remove state dir")
			} else {
				log.Debug().Str("path", path).Msg("State directory cleared")
			}
		}
	}

	if cfg.Purge {
		// Also remove config files
		configPath := cfg.ConfigDir
		if _, err := os.Stat(configPath); err == nil {
			if err := os.RemoveAll(configPath); err != nil {
				log.Warn().Err(err).Str("path", configPath).Msg("Failed to remove config dir")
			} else {
				log.Debug().Str("path", configPath).Msg("Config directory cleared")
			}
		}
	}

	return nil
}

func removeNetwork(cfg *leaveConfig) error {
	return removeNetworkBridge(cfg.BridgeName)
}

// removeNetworkBridge removes the bridge and TAP devices (shared by leave/deinit/reset)
func removeNetworkBridge(bridgeName string) error {
	log.Info().Str("bridge", bridgeName).Msg("Removing network bridge...")

	// Remove all TAP devices attached to bridge first
	cmd := exec.Command("ip", "link", "show", "type", "tap")
	output, err := cmd.Output()
	if err == nil {
		taps := parseTapDevices(string(output))
		for _, tap := range taps {
			log.Debug().Str("tap", tap).Msg("Removing TAP device")
			exec.Command("ip", "link", "delete", tap).Run()
		}
	}

	// Check if bridge exists
	cmd = exec.Command("ip", "link", "show", bridgeName)
	if err := cmd.Run(); err != nil {
		log.Debug().Msg("Bridge doesn't exist or already removed")
		return nil
	}

	// Remove bridge
	cmd = exec.Command("ip", "link", "delete", bridgeName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to remove bridge: %w\nOutput: %s", err, string(output))
	}

	log.Info().Str("bridge", bridgeName).Msg("Network bridge removed")
	return nil
}

func parseTapDevices(output string) []string {
	taps := []string{}
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		if strings.Contains(line, "tap") {
			// Parse interface name from "XX: tapXX: <flags>"
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				name := strings.TrimSuffix(fields[1], ":")
				if strings.HasPrefix(name, "tap") {
					taps = append(taps, name)
				}
			}
		}
	}

	return taps
}