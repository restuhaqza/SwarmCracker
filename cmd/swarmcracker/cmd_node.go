package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/client"
	"github.com/restuhaqza/swarmcracker/pkg/output"
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
	cmd.AddCommand(newNodeUpdateCommand())
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
		Long: `List all nodes in the SwarmCracker cluster with their status.

Use -o json or -o name for the v1 envelope; --format json keeps the legacy
node array for scripts that already parse it.`,
		Aliases: []string{"list"},
		Example: `  swarmcracker node ls
  swarmcracker node ls -o json
  swarmcracker node ls -o name
  swarmcracker node ls --format json          # legacy shape
  swarmcracker node ls --filter "role=manager"`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputFormat != "" && cmd.Flags().Changed("format") {
				return output.NewError(output.CodeUsage, "use either --output or --format, not both")
			}
			if outputFormat == "" || output.Format(outputFormat) == output.FormatTable {
				return listNodes(format, filter, quiet)
			}
			switch output.Format(outputFormat) {
			case output.FormatJSON, output.FormatName:
				if quiet {
					if output.Format(outputFormat) == output.FormatName {
						return output.NewError(output.CodeUsage, "do not combine -o name with --quiet")
					}
					return output.NewError(output.CodeUsage, "use -o name instead of --quiet")
				}
				return listNodesContract(outputFormat, filter)
			default:
				return output.NewErrorf(output.CodeUsage, "unsupported output format %q for node ls", outputFormat)
			}
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Legacy output format (table, json)")
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
		Use:     "inspect <node-id>",
		Short:   "Inspect a node",
		Long:    `Display detailed information about a node.`,
		Example: `  swarmcracker node inspect 123abc...`,
		Args:    cobra.ExactArgs(1),
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
		Args:    cobra.ExactArgs(1),
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
		Args:    cobra.ExactArgs(1),
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
		Args:    cobra.ExactArgs(1),
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
		Args: cobra.ExactArgs(1),
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

// newNodeUpdateCommand creates the node update command
func newNodeUpdateCommand() *cobra.Command {
	var (
		labelAdd []string
		labelRm  []string
	)

	cmd := &cobra.Command{
		Use:   "update <node-id>",
		Short: "Update a node's placement labels",
		Long: `Update the placement labels of a node.

Labels are matched by the scheduler's node.labels.* placement constraints, so
labelling a node is how a "node.labels.<key>==<value>" constraint becomes
satisfiable.

--label-add KEY=VALUE may be repeated; an existing key is overwritten.
--label-rm KEY may be repeated; removing a key the node does not have is a
no-op, not an error. At least one of the two flags is required.`,
		Example: `  swarmcracker node update 123abc... --label-add disk=ssd
  swarmcracker node update 123abc... --label-add disk=ssd --label-add zone=a
  swarmcracker node update 123abc... --label-rm disk`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return updateNodeLabels(args[0], labelAdd, labelRm)
		},
	}

	cmd.Flags().StringArrayVar(&labelAdd, "label-add", nil, "Add or overwrite a node label as KEY=VALUE (repeatable)")
	cmd.Flags().StringArrayVar(&labelRm, "label-rm", nil, "Remove a node label by KEY (repeatable; a key the node does not have is a no-op)")

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

	conn, err := grpc.NewClient(
		"unix://"+socketPath,
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

	return listNodesWithClient(ctx, client, os.Stdout, format, filter, quiet)
}

// listNodesWithClient is the body of the legacy `--format` path with the control
// client and output writer injected, so the table and JSON rendering is
// testable without a cluster.
func listNodesWithClient(ctx context.Context, client api.ControlClient, w io.Writer, format, filter string, quiet bool) error {
	resp, err := client.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(resp.Nodes) == 0 {
		// JSON mode must stay parseable on a healthy empty cluster. Table mode
		// keeps the human-friendly message.
		if format == "json" {
			fmt.Fprintln(w, "[]")
			return nil
		}
		fmt.Fprintln(w, "No nodes found")
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
		// A filter that matches nothing must not marshal a nil slice as null.
		if len(nodes) == 0 {
			fmt.Fprintln(w, "[]")
			return nil
		}
		data, err := json.MarshalIndent(nodes, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal nodes: %w", err)
		}
		fmt.Fprintln(w, string(data))
		return nil
	}

	// Table format
	if quiet {
		for _, node := range nodes {
			fmt.Fprintln(w, node.ID)
		}
		return nil
	}

	fmt.Fprintf(w, "%-20s %-12s %-20s %s\n", "ID", "STATUS", "HOSTNAME", "AVAILABILITY")
	fmt.Fprintln(w, strings.Repeat("-", 70))
	for _, node := range nodes {
		status := node.Status.State.String()
		avail := node.Spec.Availability.String()
		hostname := ""
		if node.Description != nil {
			hostname = node.Description.Hostname
		}
		id := node.ID
		if len(id) > 12 {
			id = id[:12]
		}
		fmt.Fprintf(w, "%-20s %-12s %-20s %s\n", id, status, hostname, avail)
	}
	fmt.Fprintf(w, "\nTotal: %d node(s)\n", len(nodes))

	return nil
}

// nodeLister is the narrow client surface the contract path needs. Keeping it
// small lets the command be tested without certificates or a live manager.
type nodeLister interface {
	ListNodes(context.Context) ([]v1.Node, error)
}

// listNodesContract is the `-o json` / `-o name` path. Unlike the legacy
// --format json path, it emits the v1 envelope.
func listNodesContract(format, filter string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := client.New(client.DefaultConfig())
	if err != nil {
		return output.FromGRPC(err)
	}
	defer c.Close()

	return listNodesContractWithClient(ctx, c, os.Stdout, format, filter)
}

func listNodesContractWithClient(ctx context.Context, l nodeLister, w io.Writer, format, filter string) error {
	nodes, err := l.ListNodes(ctx)
	if err != nil {
		return output.FromGRPC(err)
	}
	nodes = filterV1Nodes(nodes, filter)

	switch output.Format(format) {
	case output.FormatJSON:
		return output.WriteJSON(w, v1.NewNodeList(nodes))
	case output.FormatName:
		for _, node := range nodes {
			fmt.Fprintln(w, node.ID)
		}
		return nil
	default:
		return output.NewErrorf(output.CodeUsage, "unsupported output format %q for node ls", format)
	}
}

// filterV1Nodes applies one key=value filter to the v1 read model.
//
// role, membership, availability, state and hostname compare case-insensitively
// against the lowercase v1 values; id is exact. An unknown key (or a filter with
// no '=') matches everything, preserving the legacy filterNodes behavior.
//
// label is a selector rather than a value: `label=key` matches key presence and
// `label=key=value` matches both. Label values stay exact because labels are
// case-sensitive, unlike the enum fields above.
func filterV1Nodes(nodes []v1.Node, filter string) []v1.Node {
	out := make([]v1.Node, 0, len(nodes))

	key, value, ok := strings.Cut(filter, "=")
	if !ok {
		return append(out, nodes...)
	}

	if key == "label" {
		labelKey, labelValue, hasValue := strings.Cut(value, "=")
		for _, node := range nodes {
			got, present := node.Labels[labelKey]
			if present && (!hasValue || got == labelValue) {
				out = append(out, node)
			}
		}
		return out
	}

	for _, node := range nodes {
		var matched bool
		switch key {
		case "role":
			matched = strings.EqualFold(node.Role, value)
		case "membership":
			matched = strings.EqualFold(node.Membership, value)
		case "availability":
			matched = strings.EqualFold(node.Availability, value)
		case "state":
			matched = strings.EqualFold(node.State, value)
		case "id":
			matched = node.ID == value
		case "hostname":
			matched = strings.EqualFold(node.Hostname, value)
		default:
			matched = true
		}
		if matched {
			out = append(out, node)
		}
	}
	return out
}

func inspectNode(nodeID, format string, pretty bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	nodeID, err = resolveNodeRef(ctx, client, nodeID)
	if err != nil {
		return err
	}

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

	nodeID, err = resolveNodeRef(ctx, client, nodeID)
	if err != nil {
		return err
	}

	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	node := resp.Node
	if availability == api.NodeAvailabilityDrain {
		fmt.Printf("Warning: draining node %s stops its running tasks; they are not rescheduled until the node is activated again.\n", nodeID)
	}
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

// updateNodeLabels applies --label-add/--label-rm to a node's placement labels.
//
// Every argument is validated before the control client is dialled, so a
// mistyped flag fails with a clear error instead of after a round-trip (and
// without depending on the swarm socket at all).
func updateNodeLabels(nodeID string, add, remove []string) error {
	if err := validateLabelUpdate(add, remove); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	return updateNodeLabelsWithClient(ctx, client, os.Stdout, nodeID, add, remove)
}

// updateNodeLabelsWithClient is updateNodeLabels' update path. It takes the
// control client and output writer so tests can drive it with a fake (the same
// ...WithClient seam as updateServiceWithClient) and assert that a rejected
// argument sends no RPC.
//
// It revalidates the arguments rather than trusting a caller of the seam, so no
// entry point can accept a flag it then ignores.
func updateNodeLabelsWithClient(ctx context.Context, client api.ControlClient, w io.Writer, nodeID string, add, remove []string) error {
	if err := validateLabelUpdate(add, remove); err != nil {
		return err
	}

	nodeID, err := resolveNodeRef(ctx, client, nodeID)
	if err != nil {
		return err
	}

	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	node := resp.Node
	// Labels are applied to a copy of the current map: applyLabelChanges never
	// mutates its input, so a failed update cannot leave the fetched node
	// half-changed.
	labels, err := applyLabelChanges(node.Spec.Annotations.Labels, add, remove)
	if err != nil {
		return err
	}
	node.Spec.Annotations.Labels = labels

	_, err = client.UpdateNode(ctx, &api.UpdateNodeRequest{
		NodeID:      nodeID,
		NodeVersion: &node.Meta.Version,
		Spec:        &node.Spec,
	})
	if err != nil {
		return fmt.Errorf("failed to update node: %w", err)
	}

	writeNodeLabels(w, nodeID, labels)
	return nil
}

// writeNodeLabels prints the node's labels in a stable (sorted) order so the
// result of an update is visible to the operator.
func writeNodeLabels(w io.Writer, nodeID string, labels map[string]string) {
	fmt.Fprintf(w, "Node %s labels updated\n", nodeID)
	if len(labels) == 0 {
		fmt.Fprintln(w, "  (no labels)")
		return
	}

	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  %s=%s\n", k, labels[k])
	}
}

func promoteNode(nodeID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	nodeID, err = resolveNodeRef(ctx, client, nodeID)
	if err != nil {
		return err
	}

	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	node := resp.Node
	if node.Spec.DesiredRole == api.NodeRoleManager {
		fmt.Printf("Node %s is already a manager\n", nodeID)
		return nil
	}
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

	nodeID, err = resolveNodeRef(ctx, client, nodeID)
	if err != nil {
		return err
	}

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
