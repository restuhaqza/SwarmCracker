package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
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
	cmd.AddCommand(newClusterHealthCommand())
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

// newClusterStatusCommand creates the cluster status command. With no
// argument it shows a cluster overview; with a VM id it shows that VM's status
// (kept for backward compatibility).
func newClusterStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [vm-id]",
		Short: "Show cluster status (or a VM's status)",
		Long: `Show an overview of the cluster: node, service, and task counts.

If a VM id (or unique prefix) is given, show that VM's detailed status instead
(the same as 'swarmcracker vm status <vm-id>').`,
		Args: cobra.MaximumNArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return runStatus(args[0])
			}
			return runClusterStatusSummary()
		},
	}
	return cmd
}

// runClusterStatusSummary prints a high-level cluster overview.
func runClusterStatusSummary() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	nodes, err := client.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}
	services, err := client.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}
	tasks, err := client.ListTasks(ctx, &api.ListTasksRequest{})
	if err != nil {
		return fmt.Errorf("failed to list tasks: %w", err)
	}

	managers := 0
	for _, n := range nodes.Nodes {
		if n.Spec.DesiredRole == api.NodeRoleManager {
			managers++
		}
	}
	running := 0
	for _, t := range tasks.Tasks {
		if t.Status.State == api.TaskStateRunning {
			running++
		}
	}

	fmt.Println("Cluster status")
	fmt.Println(strings.Repeat("-", 40))
	fmt.Printf("Nodes:     %d (%d manager(s))\n", len(nodes.Nodes), managers)
	fmt.Printf("Services:  %d\n", len(services.Services))
	fmt.Printf("Tasks:     %d (%d running)\n", len(tasks.Tasks), running)
	return nil
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
