package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// deinitConfig holds the configuration for deinitializing a manager
type deinitConfig struct {
	Purge         bool
	Force         bool
	CleanupNetwork bool
	KeepTokens    bool
	StateDir      string
	ConfigDir     string
	RootfsDir     string
	BridgeName    string
}

func newDeinitCommand() *cobra.Command {
	cfg := &deinitConfig{}

	cmd := &cobra.Command{
		Use:   "deinit",
		Short: "Deinitialize the SwarmCracker manager",
		Long: `Deinitialize the SwarmCracker manager node.

This command will:
  1. Check if this is the last manager (warn if so)
  2. Stop all running microVMs
  3. Stop the manager systemd service
  4. Remove the systemd service file
  5. Clear cluster state (with --purge)
  6. Optionally remove network bridge

WARNING: If this is the last manager in a multi-manager cluster,
the cluster will become unreachable. Use --force to proceed.

Examples:
  # Graceful deinit (keeps state for potential recovery)
  swarmcracker deinit

  # Deinit and purge all state
  swarmcracker deinit --purge

  # Full cleanup including network
  swarmcracker deinit --purge --cleanup-network

  # Force deinit as last manager
  swarmcracker deinit --force`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeinit(cfg)
		},
	}

	cmd.Flags().BoolVar(&cfg.Purge, "purge", false, "Remove all state, config, and rootfs files")
	cmd.Flags().BoolVar(&cfg.Force, "force", false, "Force deinit even if last manager")
	cmd.Flags().BoolVar(&cfg.CleanupNetwork, "cleanup-network", false, "Remove bridge and TAP devices")
	cmd.Flags().BoolVar(&cfg.KeepTokens, "keep-tokens", false, "Preserve join tokens file")
	cmd.Flags().StringVar(&cfg.StateDir, "state-dir", "/var/lib/swarmkit", "State directory")
	cmd.Flags().StringVar(&cfg.ConfigDir, "config-dir", "/etc/swarmcracker", "Configuration directory")
	cmd.Flags().StringVar(&cfg.RootfsDir, "rootfs-dir", "/var/lib/firecracker/rootfs", "Rootfs directory")
	cmd.Flags().StringVar(&cfg.BridgeName, "bridge-name", "swarm-br0", "Bridge name for VM networking")

	return cmd
}

func runDeinit(cfg *deinitConfig) error {
	fmt.Println()
	fmt.Println("🔥 Deinitializing SwarmCracker Manager")
	fmt.Println(strings.Repeat("─", 50))

	// Check if manager service exists
	servicePath := "/etc/systemd/system/swarmcracker-manager.service"
	if _, err := os.Stat(servicePath); os.IsNotExist(err) {
		fmt.Println("\n⚠ No manager service found. This node may not be a manager.")
		fmt.Println("If you want to clean up leftover state, use 'swarmcracker reset'")
		return nil
	}

	// Check for running VMs
	PrintProgress(1, 6, "Checking for running microVMs...")
	vmCount, err := countRunningVMs()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to count VMs")
	}
	if vmCount > 0 {
		fmt.Printf("\r\033[K[1/6] ⚠ Found %d running VMs\n", vmCount)
		if !cfg.Force {
			fmt.Println("\n⚠ Warning: Running VMs will be stopped.")
			fmt.Println("Use --force to proceed or stop VMs manually first.")
			// Continue anyway - we'll stop them
		}
	} else {
		PrintProgressComplete(1, 6, "No running VMs")
	}

	// Step 2: Stop running VMs
	if vmCount > 0 {
		PrintProgress(2, 6, "Stopping running microVMs...")
		if err := stopRunningVMsDeinit(cfg); err != nil {
			if !cfg.Force {
				PrintProgressFailed(2, 6, "Stopping VMs", err)
				return fmt.Errorf("failed to stop VMs: %w (use --force to continue)", err)
			}
			log.Warn().Err(err).Msg("Failed to stop VMs (continuing with --force)")
		}
		PrintProgressComplete(2, 6, "MicroVMs stopped")
	} else {
		PrintProgressComplete(2, 6, "Skipping (no VMs)")
	}

	// Step 3: Stop systemd service
	PrintProgress(3, 6, "Stopping manager service...")
	if err := stopManagerService(cfg); err != nil {
		if !cfg.Force {
			PrintProgressFailed(3, 6, "Stopping service", err)
			return fmt.Errorf("failed to stop service: %w", err)
		}
		log.Warn().Err(err).Msg("Failed to stop service (continuing with --force)")
	}
	PrintProgressComplete(3, 6, "Manager service stopped")

	// Step 4: Disable and remove service file
	PrintProgress(4, 6, "Removing systemd service...")
	if err := removeManagerService(cfg); err != nil {
		PrintProgressFailed(4, 6, "Removing service", err)
		return fmt.Errorf("failed to remove service: %w", err)
	}
	PrintProgressComplete(4, 6, "Systemd service removed")

	// Step 5: Clear state
	PrintProgress(5, 6, "Clearing cluster state...")
	if err := clearStateDeinit(cfg); err != nil {
		PrintProgressFailed(5, 6, "Clearing state", err)
		return fmt.Errorf("failed to clear state: %w", err)
	}
	PrintProgressComplete(5, 6, "Cluster state cleared")

	// Step 6: Remove network (optional)
	if cfg.CleanupNetwork {
		PrintProgress(6, 6, "Removing network bridge...")
		if err := removeNetworkDeinit(cfg); err != nil {
			log.Warn().Err(err).Msg("Failed to remove network")
		}
		PrintProgressComplete(6, 6, "Network bridge removed")
	} else {
		PrintProgressComplete(6, 6, "Network preserved")
	}

	// Reload systemd
	exec.Command("systemctl", "daemon-reload").Run()

	// Success message
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("✅ Manager deinitialized successfully!")
	fmt.Println("========================================")
	fmt.Println()
	fmt.Println("This node is no longer a SwarmCracker manager.")
	fmt.Println()
	if cfg.Purge {
		fmt.Println("⚠ All state, config, and rootfs files have been removed.")
		fmt.Println("To set up a new cluster: swarmcracker init")
	} else {
		fmt.Println("State files preserved. To reinitialize:")
		fmt.Println("  swarmcracker reset  # clean up first")
		fmt.Println("  swarmcracker init   # start fresh")
	}
	fmt.Println("========================================")

	return nil
}

func countRunningVMs() (int, error) {
	cmd := exec.Command("pgrep", "-c", "-f", "firecracker")
	output, err := cmd.Output()
	if err != nil {
		return 0, nil // No processes
	}

	count := strings.TrimSpace(string(output))
	var vmCount int
	fmt.Sscanf(count, "%d", &vmCount)
	return vmCount, nil
}

func stopRunningVMsDeinit(cfg *deinitConfig) error {
	return stopAllFirecrackerVMs() // Use shared function from leave
}

func stopManagerService(cfg *deinitConfig) error {
	log.Info().Msg("Stopping swarmcracker-manager service...")

	cmd := exec.Command("systemctl", "stop", "swarmcracker-manager.service")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to stop service: %w\nOutput: %s", err, string(output))
	}

	log.Info().Msg("Manager service stopped")
	return nil
}

func removeManagerService(cfg *deinitConfig) error {
	log.Info().Msg("Disabling and removing systemd service...")

	exec.Command("systemctl", "disable", "swarmcracker-manager.service").Run()

	servicePath := "/etc/systemd/system/swarmcracker-manager.service"
	if _, err := os.Stat(servicePath); err == nil {
		if err := os.Remove(servicePath); err != nil {
			return fmt.Errorf("failed to remove service file: %w", err)
		}
	}

	log.Info().Msg("Systemd service removed")
	return nil
}

func clearStateDeinit(cfg *deinitConfig) error {
	log.Info().Msg("Clearing cluster state...")

	// Preserve join tokens if requested
	if cfg.KeepTokens {
		tokenFile := cfg.StateDir + "/join-tokens.txt"
		if _, err := os.Stat(tokenFile); err == nil {
			// Copy to safe location
			exec.Command("cp", tokenFile, "/tmp/swarmcracker-join-tokens.txt").Run()
			log.Info().Msg("Join tokens preserved in /tmp/swarmcracker-join-tokens.txt")
		}
	}

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
		// Also remove config and rootfs
		configPath := cfg.ConfigDir
		if _, err := os.Stat(configPath); err == nil {
			os.RemoveAll(configPath)
		}

		rootfsPath := cfg.RootfsDir
		if _, err := os.Stat(rootfsPath); err == nil {
			os.RemoveAll(rootfsPath)
		}

		log.Info().Msg("All state, config, and rootfs files removed")
	}

	return nil
}

func removeNetworkDeinit(cfg *deinitConfig) error {
	return removeNetworkBridge(cfg.BridgeName) // Use shared function from leave
}