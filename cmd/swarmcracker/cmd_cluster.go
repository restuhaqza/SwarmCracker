package main

import (
	"github.com/spf13/cobra"
)

// newClusterCommand creates the cluster command group
func newClusterCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage SwarmCracker cluster lifecycle",
		Long: `Manage SwarmCracker cluster lifecycle operations.

These commands provide cluster-level operations like initialization,
joining, leaving, and status monitoring.`,
	}

	// Add subcommands - reuse existing working commands
	cmd.AddCommand(newClusterInitCommand())
	cmd.AddCommand(newClusterJoinCommand())
	cmd.AddCommand(newClusterLeaveCommand())
	cmd.AddCommand(newClusterTokenCommand())
	cmd.AddCommand(newClusterStatusCommand())
	cmd.AddCommand(newClusterResetCommand())
	cmd.AddCommand(newClusterDeinitCommand())

	return cmd
}

// newClusterInitCommand creates the cluster init command
func newClusterInitCommand() *cobra.Command {
	// Reuse existing init command - it already works perfectly
	cmd := newInitCommand()
	return cmd
}

// newClusterJoinCommand creates the cluster join command
func newClusterJoinCommand() *cobra.Command {
	// Reuse existing join command
	cmd := newJoinCommand()
	return cmd
}

// newClusterLeaveCommand creates the cluster leave command
func newClusterLeaveCommand() *cobra.Command {
	// Reuse existing leave command
	cmd := newLeaveCommand()
	return cmd
}

// newClusterTokenCommand creates the cluster token command
func newClusterTokenCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token [worker|manager]",
		Short: "Display join tokens",
		Long: `Display join tokens for adding nodes to the cluster.

Shows the token needed to join new worker or manager nodes.`,
		Example: `  swarmcracker cluster token worker
  swarmcracker cluster token manager`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Delegate to existing get-join-token logic
			role := "worker"
			if len(args) > 0 {
				role = args[0]
			}
			return runGetJoinToken(role)
		},
	}
	return cmd
}

// newClusterStatusCommand creates the cluster status command
func newClusterStatusCommand() *cobra.Command {
	// Reuse existing status command
	cmd := newStatusCommand()
	return cmd
}

// newClusterResetCommand creates the cluster reset command
func newClusterResetCommand() *cobra.Command {
	// Reuse existing reset command
	cmd := newResetCommand()
	return cmd
}

// newClusterDeinitCommand creates the cluster deinit command
func newClusterDeinitCommand() *cobra.Command {
	// Reuse existing deinit command
	cmd := newDeinitCommand()
	return cmd
}
