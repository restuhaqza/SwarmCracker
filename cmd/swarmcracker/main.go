package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	Version   = "v0.1.0-alpha"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

// Global flags
var (
	cfgFile        string
	logLevel       string
	kernelPath     string
	rootfsDir      string
	sshKeyPath     string
	knownHostsPath string
	insecureSSH    bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "swarmcracker",
		Short:   "SwarmCracker CLI - Run containers as Firecracker microVMs",
		Long:    `SwarmCracker is a CLI tool for running containers as isolated Firecracker microVMs.`,
		Version: Version,
	}

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "Path to configuration file (default: /etc/swarmcracker/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().StringVar(&kernelPath, "kernel", "", "Override kernel path")
	rootCmd.PersistentFlags().StringVar(&rootfsDir, "rootfs-dir", "", "Override rootfs directory")
	rootCmd.PersistentFlags().StringVar(&sshKeyPath, "ssh-key", "", "SSH private key path for remote deployment")
	rootCmd.PersistentFlags().StringVar(&knownHostsPath, "known-hosts", "", "Path to SSH known_hosts file")
	rootCmd.PersistentFlags().BoolVar(&insecureSSH, "insecure-ssh", false, "Skip SSH host key verification (WARNING: allows MITM attacks)")

	// New command structure
	rootCmd.AddCommand(newClusterCommand())
	rootCmd.AddCommand(newNodeCommand())
	rootCmd.AddCommand(newServiceCommand())
	rootCmd.AddCommand(newTaskCommand())
	rootCmd.AddCommand(newVMCommand())
	rootCmd.AddCommand(newNetworkCommand())
	rootCmd.AddCommand(newVolumeCommand())
	rootCmd.AddCommand(newAssetCommand())
	rootCmd.AddCommand(newConfigCommand())
	rootCmd.AddCommand(newSetupCommand())

	// Backward compatibility: legacy commands with deprecation warnings
	rootCmd.AddCommand(newDeprecatedInitCommand())
	rootCmd.AddCommand(newDeprecatedJoinCommand())
	rootCmd.AddCommand(newDeprecatedLeaveCommand())
	rootCmd.AddCommand(newDeprecatedDeinitCommand())
	rootCmd.AddCommand(newDeprecatedResetCommand())
	rootCmd.AddCommand(newDeprecatedRunCommand())
	rootCmd.AddCommand(newDeprecatedDeployCommand())
	rootCmd.AddCommand(newDeprecatedValidateCommand())
	rootCmd.AddCommand(newDeprecatedListCommand())
	rootCmd.AddCommand(newDeprecatedStatusCommand())
	rootCmd.AddCommand(newDeprecatedLogsCommand())
	rootCmd.AddCommand(newDeprecatedStopCommand())
	rootCmd.AddCommand(newDeprecatedMetricsCommand())
	rootCmd.AddCommand(newDeprecatedSnapshotCommand())

	// Utility
	rootCmd.AddCommand(newDoctorCommand())
	rootCmd.AddCommand(newVersionCommand())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
