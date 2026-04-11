package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// resetConfig holds the configuration for full reset
type resetConfig struct {
	Hard      bool
	KeepConfig bool
	KeepRootfs bool
	StateDir   string
	ConfigDir  string
	RootfsDir  string
	BridgeName string
}

func newResetCommand() *cobra.Command {
	cfg := &resetConfig{}

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset SwarmCracker completely",
		Long: `Reset SwarmCracker completely (nuclear option).

This command will:
  1. Kill all Firecracker processes
  2. Remove all TAP devices
  3. Remove bridge network
  4. Stop both manager and worker services
  5. Remove all systemd service files
  6. Clear all state directories
  7. Optionally remove config and rootfs (--hard)

WARNING: This is destructive. All VMs, state, and configuration
will be permanently removed.

Examples:
  # Standard reset (keeps config and rootfs)
  swarmcracker reset

  # Keep config files
  swarmcracker reset --keep-config

  # Full reset including binaries
  swarmcracker reset --hard`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReset(cfg)
		},
	}

	cmd.Flags().BoolVar(&cfg.Hard, "hard", false, "Remove everything including binaries")
	cmd.Flags().BoolVar(&cfg.KeepConfig, "keep-config", false, "Preserve config files in /etc/swarmcracker")
	cmd.Flags().BoolVar(&cfg.KeepRootfs, "keep-rootfs", false, "Preserve rootfs images")
	cmd.Flags().StringVar(&cfg.StateDir, "state-dir", "/var/lib/swarmkit", "State directory")
	cmd.Flags().StringVar(&cfg.ConfigDir, "config-dir", "/etc/swarmcracker", "Configuration directory")
	cmd.Flags().StringVar(&cfg.RootfsDir, "rootfs-dir", "/var/lib/firecracker/rootfs", "Rootfs directory")
	cmd.Flags().StringVar(&cfg.BridgeName, "bridge-name", "swarm-br0", "Bridge name")

	return cmd
}

func runReset(cfg *resetConfig) error {
	fmt.Println()
	fmt.Println("☢️  SwarmCracker Full Reset")
	fmt.Println(strings.Repeat("─", 50))
	fmt.Println()
	fmt.Println("⚠ WARNING: This will remove ALL SwarmCracker state and services!")
	fmt.Println()

	if !cfg.Hard && !cfg.KeepConfig && !cfg.KeepRootfs {
		fmt.Println("This will remove:")
		fmt.Println("  • All running microVMs")
		fmt.Println("  • All systemd services")
		fmt.Println("  • All state directories")
		fmt.Println("  • All TAP devices and bridge network")
		fmt.Println("  • All rootfs images")
		fmt.Println("  • All config files")
		fmt.Println()
		if cfg.Hard {
			fmt.Println("  • Installed binaries (--hard)")
		}
	}

	// Step 1: Kill all Firecracker processes
	PrintProgress(1, 7, "Killing all Firecracker processes...")
	if err := killAllFirecracker(); err != nil {
		log.Warn().Err(err).Msg("Failed to kill some processes")
	}
	PrintProgressComplete(1, 7, "All Firecracker processes killed")

	// Step 2: Remove TAP devices
	PrintProgress(2, 7, "Removing all TAP devices...")
	if err := removeAllTapDevices(); err != nil {
		log.Warn().Err(err).Msg("Failed to remove some TAP devices")
	}
	PrintProgressComplete(2, 7, "All TAP devices removed")

	// Step 3: Remove bridge network
	PrintProgress(3, 7, "Removing bridge network...")
	if err := removeBridge(cfg); err != nil {
		log.Warn().Err(err).Msg("Bridge removal failed (may not exist)")
	}
	PrintProgressComplete(3, 7, "Bridge network removed")

	// Step 4: Stop all services
	PrintProgress(4, 7, "Stopping all systemd services...")
	stopAllServices()
	PrintProgressComplete(4, 7, "All services stopped")

	// Step 5: Remove service files
	PrintProgress(5, 7, "Removing systemd service files...")
	removeAllServiceFiles()
	PrintProgressComplete(5, 7, "Service files removed")

	// Step 6: Clear state directories
	PrintProgress(6, 7, "Clearing state directories...")
	clearAllState(cfg)
	PrintProgressComplete(6, 7, "State directories cleared")

	// Step 7: Remove config/rootfs (optional)
	PrintProgress(7, 7, "Removing config and rootfs...")
	if !cfg.KeepConfig {
		os.RemoveAll(cfg.ConfigDir)
	}
	if !cfg.KeepRootfs {
		os.RemoveAll(cfg.RootfsDir)
	}
	if cfg.Hard {
		removeBinaries()
	}
	PrintProgressComplete(7, 7, "Cleanup complete")

	// Reload systemd
	exec.Command("systemctl", "daemon-reload").Run()

	// Success message
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("✅ SwarmCracker reset complete!")
	fmt.Println("========================================")
	fmt.Println()
	fmt.Println("All state has been removed. To set up a new cluster:")
	fmt.Println("  swarmcracker init")
	fmt.Println()
	if cfg.Hard {
		fmt.Println("⚠ Binaries removed. Reinstall SwarmCracker:")
		fmt.Println("  curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash")
	}
	fmt.Println("========================================")

	return nil
}

func killAllFirecracker() error {
	log.Info().Msg("Killing all Firecracker processes...")

	// SIGTERM first
	exec.Command("pkill", "-TERM", "-f", "firecracker").Run()

	// Wait a moment
	exec.Command("sleep", "2").Run()

	// SIGKILL for remaining
	exec.Command("pkill", "-9", "-f", "firecracker").Run()

	// Also kill swarmd-firecracker
	exec.Command("pkill", "-9", "-f", "swarmd-firecracker").Run()

	return nil
}

func removeAllTapDevices() error {
	log.Info().Msg("Removing all TAP devices...")

	cmd := exec.Command("ip", "link", "show", "type", "tap")
	output, err := cmd.Output()
	if err != nil {
		return nil // No TAP devices
	}

	taps := parseTapDevices(string(output))
	for _, tap := range taps {
		log.Debug().Str("tap", tap).Msg("Removing TAP device")
		exec.Command("ip", "link", "delete", tap).Run()
	}

	log.Info().Int("count", len(taps)).Msg("TAP devices removed")
	return nil
}

func removeBridge(cfg *resetConfig) error {
	log.Info().Str("bridge", cfg.BridgeName).Msg("Removing bridge...")

	// Check if exists
	cmd := exec.Command("ip", "link", "show", cfg.BridgeName)
	if err := cmd.Run(); err != nil {
		return nil // Doesn't exist
	}

	exec.Command("ip", "link", "delete", cfg.BridgeName).Run()
	return nil
}

func stopAllServices() {
	log.Info().Msg("Stopping all SwarmCracker services...")

	services := []string{
		"swarmcracker-manager.service",
		"swarmcracker-worker.service",
		"swarmd-firecracker.service",
	}

	for _, svc := range services {
		exec.Command("systemctl", "stop", svc).Run()
		exec.Command("systemctl", "disable", svc).Run()
	}
}

func removeAllServiceFiles() {
	log.Info().Msg("Removing systemd service files...")

	serviceFiles := []string{
		"/etc/systemd/system/swarmcracker-manager.service",
		"/etc/systemd/system/swarmcracker-worker.service",
		"/etc/systemd/system/swarmd-firecracker.service",
	}

	for _, file := range serviceFiles {
		if _, err := os.Stat(file); err == nil {
			os.Remove(file)
		}
	}
}

func clearAllState(cfg *resetConfig) {
	log.Info().Msg("Clearing state directories...")

	statePaths := []string{
		cfg.StateDir,
		"/var/lib/swarmkit",
		"/var/run/swarmkit",
		"/var/run/firecracker",
		"/var/lib/firecracker",
	}

	for _, path := range statePaths {
		if _, err := os.Stat(path); err == nil {
			os.RemoveAll(path)
		}
	}
}

func removeBinaries() {
	log.Info().Msg("Removing installed binaries...")

	binaries := []string{
		"/usr/local/bin/swarmcracker",
		"/usr/local/bin/swarmd-firecracker",
		"/usr/local/bin/swarmctl",
		"/usr/bin/swarmcracker",
		"/usr/bin/swarmd-firecracker",
	}

	for _, bin := range binaries {
		if _, err := os.Stat(bin); err == nil {
			os.Remove(bin)
		}
	}
}