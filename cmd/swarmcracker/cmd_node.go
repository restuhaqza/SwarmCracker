package main

import (
	"strings"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"github.com/spf13/cobra"
)

// newNodeCommand creates the node command group
func newNodeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Manage SwarmCracker nodes",
		Long: `Manage and inspect nodes in the SwarmCracker cluster.

These commands provide node-level operations like listing, inspecting,
draining, and removing nodes.`,
	}

	// Add subcommands
	cmd.AddCommand(newNodeListCommand())
	cmd.AddCommand(newNodeInspectCommand())
	cmd.AddCommand(newNodeDrainCommand())
	cmd.AddCommand(newNodeActivateCommand())
	cmd.AddCommand(newNodePromoteCommand())
	cmd.AddCommand(newNodeRemoveCommand())

	return cmd
}

// newNodeListCommand creates the node list command
func newNodeListCommand() *cobra.Command {
	var (
		format string
		filter string
		quiet  bool
	)

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List nodes in the cluster",
		Long: `List all nodes in the SwarmCracker cluster with their status.`,
		Aliases: []string{"list"},
		Example: `  swarmcracker node ls
  swarmcracker node ls --format json
  swarmcracker node ls --filter "role=manager"`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listNodes(format, filter, quiet)
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")
	cmd.Flags().StringVar(&filter, "filter", "", "Filter output (e.g., 'role=manager')")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only display IDs")

	return cmd
}

// newNodeInspectCommand creates the node inspect command
func newNodeInspectCommand() *cobra.Command {
	var (
		format string
		pretty bool
	)

	cmd := &cobra.Command{
		Use:   "inspect <node-id>",
		Short: "Inspect a node",
		Long: `Display detailed information about a node.`,
		Example: `  swarmcracker node inspect 123abc...`,
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return inspectNode(args[0], format, pretty)
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "Output format (json)")
	cmd.Flags().BoolVar(&pretty, "pretty", true, "Pretty print output")

	return cmd
}

// newNodeDrainCommand creates the node drain command
func newNodeDrainCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "drain <node-id>",
		Short: "Drain a node",
		Long: `Drain a node to reschedule tasks away from it.

A drained node will not receive any new tasks. Existing tasks
will be rescheduled on other active nodes.`,
		Example: `  swarmcracker node drain 123abc...`,
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return setNodeAvailability(args[0], api.NodeAvailabilityDrain)
		},
	}

	return cmd
}

// newNodeActivateCommand creates the node activate command
func newNodeActivateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "activate <node-id>",
		Short: "Activate a node",
		Long: `Activate a drained or paused node to make it available for tasks.

An activated node can receive new tasks.`,
		Example: `  swarmcracker node activate 123abc...`,
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return setNodeAvailability(args[0], api.NodeAvailabilityActive)
		},
	}

	return cmd
}

// newNodePromoteCommand creates the node promote command
func newNodePromoteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "promote <node-id>",
		Short: "Promote a worker to manager",
		Long: `Promote a worker node to manager role.

Manager nodes participate in the SwarmKit Raft consensus and can
perform management operations.`,
		Example: `  swarmcracker node promote 123abc...`,
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return promoteNode(args[0])
		},
	}

	return cmd
}

// newNodeRemoveCommand creates the node remove command
func newNodeRemoveCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "rm <node-id>",
		Short: "Remove a node",
		Long: `Remove a node from the cluster.

For active nodes, use --force to remove without confirmation.`,
		Aliases: []string{"remove"},
		Example: `  swarmcracker node rm 123abc...
  swarmcracker node rm --force 123abc...`,
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return removeNode(args[0], force)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force removal without confirmation")

	return cmd
}

// SwarmKit client helpers

func getSwarmClient() (api.ControlClient, *grpc.ClientConn, error) {
	socketPath := "/var/run/swarmkit/swarm.sock"
	if envSocket := os.Getenv("SWARM_SOCKET"); envSocket != "" {
		socketPath = envSocket
	}

	stateDir := "/var/lib/swarmkit"
	if envState := os.Getenv("SWARM_STATE_DIR"); envState != "" {
		stateDir = envState
	}

	// Load TLS certificates
	certDir := filepath.Join(stateDir, "certificates")
	certFile := filepath.Join(certDir, "swarm-node.crt")
	keyFile := filepath.Join(certDir, "swarm-node.key")
	caFile := filepath.Join(certDir, "swarm-root-ca.crt")

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load TLS certificate: %w", err)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read CA certificate: %w", err)
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: true,
	}

	conn, err := grpc.Dial(
		socketPath,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to swarm: %w", err)
	}

	client := api.NewControlClient(conn)
	return client, conn, nil
}

func listNodes(format, filter string, quiet bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := client.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(resp.Nodes) == 0 {
		fmt.Println("No nodes found")
		return nil
	}

	// Apply filter if specified
	nodes := resp.Nodes
	if filter != "" {
		// Simple filter implementation
		nodes = filterNodes(nodes, filter)
	}

	// Output based on format
	if format == "json" {
		data, _ := json.MarshalIndent(nodes, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	// Table format
	if quiet {
		for _, node := range nodes {
			fmt.Println(node.ID)
		}
		return nil
	}

	fmt.Printf("%-20s %-12s %-20s %s\n", "ID", "STATUS", "HOSTNAME", "AVAILABILITY")
	fmt.Println(strings.Repeat("-", 70))
	for _, node := range nodes {
		status := node.Status.State.String()
		avail := node.Spec.Availability.String()
		hostname := node.Description.Hostname
		fmt.Printf("%-20s %-12s %-20s %s\n", node.ID[:12], status, hostname, avail)
	}
	fmt.Printf("\nTotal: %d node(s)\n", len(nodes))

	return nil
}

func inspectNode(nodeID, format string, pretty bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	if format == "json" {
		data, _ := json.MarshalIndent(resp.Node, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("ID: %s\n", resp.Node.ID)
		fmt.Printf("Hostname: %s\n", resp.Node.Description.Hostname)
		fmt.Printf("Role: %s\n", resp.Node.Spec.DesiredRole.String())
		fmt.Printf("Status: %s\n", resp.Node.Status.State.String())
		fmt.Printf("Availability: %s\n", resp.Node.Spec.Availability.String())
	}

	return nil
}

func setNodeAvailability(nodeID string, availability api.NodeSpec_Availability) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	node := resp.Node
	node.Spec.Availability = availability

	_, err = client.UpdateNode(ctx, &api.UpdateNodeRequest{
		NodeID:      nodeID,
		NodeVersion: &node.Meta.Version,
		Spec:        &node.Spec,
	})
	if err != nil {
		return fmt.Errorf("failed to update node: %w", err)
	}

	fmt.Printf("Node %s availability set to %s\n", nodeID, availability.String())
	return nil
}

func promoteNode(nodeID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	node := resp.Node
	node.Spec.DesiredRole = api.NodeRoleManager

	_, err = client.UpdateNode(ctx, &api.UpdateNodeRequest{
		NodeID:      nodeID,
		NodeVersion: &node.Meta.Version,
		Spec:        &node.Spec,
	})
	if err != nil {
		return fmt.Errorf("failed to promote node: %w", err)
	}

	fmt.Printf("Node %s promoted to manager\n", nodeID)
	return nil
}

func filterNodes(nodes []*api.Node, filter string) []*api.Node {
	// Simple filter implementation
	// Supports: role=manager, role=worker, availability=active, etc.
	var result []*api.Node
	for _, node := range nodes {
		match := true
		if filter == "role=manager" && node.Spec.DesiredRole != api.NodeRoleManager {
			match = false
		}
		if filter == "role=worker" && node.Spec.DesiredRole != api.NodeRoleWorker {
			match = false
		}
		if match {
			result = append(result, node)
		}
	}
	return result
}

func removeNode(nodeID string, force bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	// Verify node exists first
	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	// Check node state - warn if not drained
	if resp.Node.Status.State == api.NodeStatus_READY && !force {
		return fmt.Errorf("node %s is still active - drain first or use --force", nodeID)
	}

	// Remove the node
	_, err = client.RemoveNode(ctx, &api.RemoveNodeRequest{
		NodeID: nodeID,
		Force:  force,
	})
	if err != nil {
		return fmt.Errorf("failed to remove node: %w", err)
	}

	fmt.Printf("Node %s removed from cluster\n", nodeID)
	return nil
}
