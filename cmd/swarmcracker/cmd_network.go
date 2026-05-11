package main

import (
	"github.com/spf13/cobra"
)

// newNetworkCommand creates the network command group
func newNetworkCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Manage network configuration",
		Long: `Manage network configuration for SwarmCracker.

Provides commands for VXLAN overlay and bridge management.`,
	}

	// Add subcommands
	cmd.AddCommand(newNetworkVXLANCommand())
	cmd.AddCommand(newNetworkBridgeCommand())

	return cmd
}

// newNetworkVXLANCommand creates the VXLAN subcommand
func newNetworkVXLANCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vxlan",
		Short: "Manage VXLAN overlay",
		Long: `Manage VXLAN overlay networking for cross-node VM communication.`,
	}

	cmd.AddCommand(newVXLANListCommand())
	cmd.AddCommand(newVXLANStatusCommand())

	return cmd
}

// newVXLANListCommand lists VXLAN peers
func newVXLANListCommand() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"peers", "list"},
		Short:   "List VXLAN peers",
		Long: `List VXLAN overlay peers for cross-node VM communication.

Peers are discovered from:
1. Consul service registry (if enabled)
2. Static configuration from /etc/swarmcracker/config.yaml

Examples:
  swarmcracker network vxlan ls
  swarmcracker network vxlan ls --format json
  swarmcracker network vxlan peers`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listVXLANPeers(format)
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")

	return cmd
}

// listVXLANPeers lists VXLAN peers (placeholder implementation)
func listVXLANPeers(format string) error {
	// Placeholder implementation - will be implemented in future phases
	// Currently delegates to doctor network checks
	return runDoctorNetwork()
}

// newVXLANStatusCommand shows VXLAN status
func newVXLANStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show VXLAN status",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Delegate to doctor command for network checks
			return runDoctorNetwork()
		},
	}
}

// newNetworkBridgeCommand creates the bridge subcommand
func newNetworkBridgeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bridge",
		Short: "Manage bridge network",
		Long: `Manage the bridge network for local VM communication.`,
	}

	cmd.AddCommand(newBridgeStatusCommand())

	return cmd
}

// newBridgeStatusCommand shows bridge status
func newBridgeStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show bridge status",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Delegate to doctor command for bridge checks
			return runDoctorNetwork()
		},
	}
}