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
	"strings"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/restuhaqza/swarmcracker/pkg/redact"
)

// newTaskCommand creates the task command group
func newTaskCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manage SwarmCracker tasks",
		Long: `Manage and inspect tasks in the SwarmCracker cluster.

These commands provide task-level operations like listing, inspecting,
and viewing task logs.`,
	}

	// Add subcommands
	cmd.AddCommand(newTaskListCommand())
	cmd.AddCommand(newTaskInspectCommand())

	return cmd
}

// newTaskListCommand creates the task list command
func newTaskListCommand() *cobra.Command {
	var (
		format     string
		filter     string
		quiet      bool
		all        bool
		noTrunc    bool
		node       string
		serviceID  string
		healthPort int
		noHealth   bool
	)

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List tasks",
		Long: `List all tasks in the SwarmCracker cluster.

Tasks are the individual units of work that run on nodes. The HEALTH column
shows the daemon's live healthcheck state, read from the node owning each task;
a task the owning node has not published health for falls back to the
health-gated task state, and a node that cannot be reached shows "-".`,
		Aliases: []string{"list"},
		Example: `  swarmcracker task ls
  swarmcracker task ls --format json
  swarmcracker task ls --filter "node=123abc..."
  swarmcracker task ls --all`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listTasks(format, filter, quiet, all, noTrunc, node, serviceID, clusterHealthOptions{Port: healthPort, Disabled: noHealth})
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")
	cmd.Flags().StringVar(&filter, "filter", "", "Filter output (e.g., 'node=xxx', 'state=running')")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only display IDs")
	cmd.Flags().BoolVar(&all, "all", false, "Show all tasks (including stopped)")
	cmd.Flags().BoolVar(&noTrunc, "no-trunc", false, "Don't truncate output")
	cmd.Flags().StringVar(&node, "node", "", "Filter by node ID")
	cmd.Flags().StringVar(&serviceID, "service", "", "Filter by service ID")
	cmd.Flags().IntVar(&healthPort, "health-port", defaultHealthPort, "Port of each node's health server, used to fetch live health")
	cmd.Flags().BoolVar(&noHealth, "no-health", false, "Skip the live-health lookup; derive HEALTH from the task state only")

	return cmd
}

// newTaskInspectCommand creates the task inspect command
func newTaskInspectCommand() *cobra.Command {
	var (
		format string
		pretty bool
	)

	cmd := &cobra.Command{
		Use:     "inspect <task-id>",
		Short:   "Inspect a task",
		Long:    `Display detailed information about a task.`,
		Example: `  swarmcracker task inspect 123abc...`,
		Args:    cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return inspectTask(args[0], format, pretty)
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "Output format (json)")
	cmd.Flags().BoolVar(&pretty, "pretty", true, "Pretty print output")

	return cmd
}

// Task operations

func listTasks(format, filter string, quiet, all, noTrunc bool, node, serviceID string, health clusterHealthOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForTask()
	if err != nil {
		return err
	}
	defer conn.Close()

	return listTasksWithClient(ctx, client, os.Stdout, format, filter, quiet, all, noTrunc, node, serviceID, health)
}

// listTasksWithClient is the body of `task ls` with the control client and
// output writer injected, so the table/JSON rendering is unit-testable without
// a cluster.
func listTasksWithClient(ctx context.Context, client api.ControlClient, w io.Writer, format, filter string, quiet, all, noTrunc bool, node, serviceID string, health clusterHealthOptions) error {
	if err := health.validate(); err != nil {
		return err
	}

	resp, err := client.ListTasks(ctx, &api.ListTasksRequest{})
	if err != nil {
		return fmt.Errorf("failed to list tasks: %w", err)
	}

	if len(resp.Tasks) == 0 {
		fmt.Fprintln(w, "No tasks found")
		return nil
	}

	// Apply filters
	tasks := resp.Tasks
	tasks = filterTasks(tasks, filter, node, serviceID, all)

	// Table format: --quiet prints only IDs, so skip the health fan-out.
	if format != "json" && quiet {
		for _, task := range tasks {
			fmt.Fprintln(w, task.ID)
		}
		return nil
	}

	lh := fetchLiveHealth(ctx, client, tasks, health)

	// Output based on format
	if format == "json" {
		return writeTasksJSON(w, tasks, lh)
	}

	// ERROR is appended last: every other column keeps its position, because
	// scripts parse STATUS (field 3) and HEALTH (field 4) positionally. The rule
	// spans the header it follows, so it cannot go stale when a column changes.
	header := fmt.Sprintf("%-20s %-20s %-12s %-12s %-12s %s %s", "ID", "SERVICE", "STATUS", "HEALTH", "NODE", "IMAGE", "ERROR")
	fmt.Fprintln(w, header)
	fmt.Fprintln(w, strings.Repeat("-", len(header)))
	for _, task := range tasks {
		status := task.Status.State.String()
		nodeID := task.NodeID
		if !noTrunc && len(nodeID) > 12 {
			nodeID = nodeID[:12]
		}
		svcID := task.ServiceID
		if !noTrunc && len(svcID) > 12 {
			svcID = svcID[:12]
		}
		image := ""
		if task.Spec.GetContainer() != nil {
			image = task.Spec.GetContainer().Image
		}
		row := fmt.Sprintf("%-20s %-20s %-12s %-12s %-12s %s", task.ID, svcID, status, taskHealthDisplay(task, lh), nodeID, image)
		// A task with no reason prints no trailing column at all, so a row keeps
		// exactly the shape it had before ERROR existed.
		if reason := taskErrorColumn(task); reason != "" {
			row += " " + reason
		}
		fmt.Fprintln(w, row)
	}
	fmt.Fprintf(w, "\nTotal: %d task(s)\n", len(tasks))

	return nil
}

func inspectTask(taskID, format string, pretty bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForTask()
	if err != nil {
		return err
	}
	defer conn.Close()

	return inspectTaskWithClient(ctx, client, os.Stdout, taskID, format, pretty)
}

// inspectTaskWithClient is the body of `task inspect` with the control client
// and output writer injected, so the rendering is unit-testable without a
// cluster.
func inspectTaskWithClient(ctx context.Context, client api.ControlClient, w io.Writer, taskID, format string, pretty bool) error {
	resolvedID, err := resolveTaskRef(ctx, client, taskID)
	if err != nil {
		return err
	}

	resp, err := client.GetTask(ctx, &api.GetTaskRequest{TaskID: resolvedID})
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	if format == "json" {
		// redact.Task masks the registry credential the task carries, so it can
		// never reach stdout.
		data, err := json.MarshalIndent(redact.Task(resp.Task), "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal task: %w", err)
		}
		fmt.Fprintln(w, string(data))
		return nil
	}

	task := resp.Task
	fmt.Fprintf(w, "ID: %s\n", task.ID)
	fmt.Fprintf(w, "Service ID: %s\n", task.ServiceID)
	fmt.Fprintf(w, "Node ID: %s\n", task.NodeID)
	fmt.Fprintf(w, "Status: %s\n", task.Status.State.String())
	if container := task.Spec.GetContainer(); container != nil {
		fmt.Fprintf(w, "Image: %s\n", container.Image)
		if hc := container.Healthcheck; hc != nil {
			fmt.Fprintf(w, "Healthcheck:\n")
			fmt.Fprintf(w, "  Test: %s\n", strings.Join(hc.Test, " "))
			fmt.Fprintf(w, "  Interval: %s\n", healthDurationString(hc.Interval))
			fmt.Fprintf(w, "  Timeout: %s\n", healthDurationString(hc.Timeout))
			fmt.Fprintf(w, "  Retries: %d\n", hc.Retries)
			fmt.Fprintf(w, "  Start Period: %s\n", healthDurationString(hc.StartPeriod))
		}
	}
	// Status.Err is where the executor records the health failure reason, e.g.
	// "healthcheck failed: <cmd>: <output>".
	if task.Status.Err != "" {
		fmt.Fprintf(w, "Error: %s\n", task.Status.Err)
	}

	return nil
}

// healthDurationString renders a protobuf duration as a human-readable string,
// falling back to the raw protobuf form if it cannot be decoded. A nil duration
// is rendered as "0s" so the inspect output always shows a value.
func healthDurationString(d *gogotypes.Duration) string {
	if d == nil {
		return "0s"
	}
	v, err := gogotypes.DurationFromProto(d)
	if err != nil {
		return d.String()
	}
	return v.String()
}

// getSwarmClientForTask creates a SwarmKit client connection
// Note: This is a duplicate of getSwarmClient to avoid import conflicts
func getSwarmClientForTask() (api.ControlClient, *grpc.ClientConn, error) {
	socketPath := "/var/run/swarmkit/swarm.sock"
	if envSocket := os.Getenv("SWARM_SOCKET"); envSocket != "" {
		socketPath = envSocket
	}

	stateDir := "/var/lib/swarmkit"
	if envState := os.Getenv("SWARM_STATE_DIR"); envState != "" {
		stateDir = envState
	}

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
