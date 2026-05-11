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
		format    string
		filter    string
		quiet     bool
		all       bool
		noTrunc   bool
		node      string
		serviceID string
	)

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List tasks",
		Long: `List all tasks in the SwarmCracker cluster.

Tasks are the individual units of work that run on nodes.`,
		Aliases: []string{"list"},
		Example: `  swarmcracker task ls
  swarmcracker task ls --format json
  swarmcracker task ls --filter "node=123abc..."
  swarmcracker task ls --all`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listTasks(format, filter, quiet, all, noTrunc, node, serviceID)
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")
	cmd.Flags().StringVar(&filter, "filter", "", "Filter output (e.g., 'node=xxx', 'state=running')")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only display IDs")
	cmd.Flags().BoolVar(&all, "all", false, "Show all tasks (including stopped)")
	cmd.Flags().BoolVar(&noTrunc, "no-trunc", false, "Don't truncate output")
	cmd.Flags().StringVar(&node, "node", "", "Filter by node ID")
	cmd.Flags().StringVar(&serviceID, "service", "", "Filter by service ID")

	return cmd
}

// newTaskInspectCommand creates the task inspect command
func newTaskInspectCommand() *cobra.Command {
	var (
		format string
		pretty bool
	)

	cmd := &cobra.Command{
		Use:   "inspect <task-id>",
		Short: "Inspect a task",
		Long: `Display detailed information about a task.`,
		Example: `  swarmcracker task inspect 123abc...`,
		Args:  cobra.ExactArgs(1),
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

func listTasks(format, filter string, quiet, all, noTrunc bool, node, serviceID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForTask()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := client.ListTasks(ctx, &api.ListTasksRequest{})
	if err != nil {
		return fmt.Errorf("failed to list tasks: %w", err)
	}

	if len(resp.Tasks) == 0 {
		fmt.Println("No tasks found")
		return nil
	}

	// Apply filters
	tasks := resp.Tasks
	tasks = filterTasks(tasks, filter, node, serviceID, all)

	// Output based on format
	if format == "json" {
		data, _ := json.MarshalIndent(tasks, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	// Table format
	if quiet {
		for _, task := range tasks {
			fmt.Println(task.ID)
		}
		return nil
	}

	fmt.Printf("%-20s %-20s %-12s %-12s %s\n", "ID", "SERVICE", "STATUS", "NODE", "IMAGE")
	fmt.Println(strings.Repeat("-", 80))
	for _, task := range tasks {
		status := task.Status.State.String()
		nodeID := ""
		if task.NodeID != "" {
			if noTrunc {
				nodeID = task.NodeID
			} else {
				nodeID = task.NodeID[:12]
			}
		}
		svcID := ""
		if task.ServiceID != "" {
			if noTrunc {
				svcID = task.ServiceID
			} else {
				svcID = task.ServiceID[:12]
			}
		}
		image := ""
		if task.Spec.GetContainer() != nil {
			image = task.Spec.GetContainer().Image
		}
		fmt.Printf("%-20s %-20s %-12s %-12s %s\n", task.ID, svcID, status, nodeID, image)
	}
	fmt.Printf("\nTotal: %d task(s)\n", len(tasks))

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

	resp, err := client.GetTask(ctx, &api.GetTaskRequest{TaskID: taskID})
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	if format == "json" {
		data, _ := json.MarshalIndent(resp.Task, "", "  ")
		fmt.Println(string(data))
	} else {
		task := resp.Task
		fmt.Printf("ID: %s\n", task.ID)
		fmt.Printf("Service ID: %s\n", task.ServiceID)
		fmt.Printf("Node ID: %s\n", task.NodeID)
		fmt.Printf("Status: %s\n", task.Status.State.String())
		if task.Spec.GetContainer() != nil {
			fmt.Printf("Image: %s\n", task.Spec.GetContainer().Image)
		}
		if task.Status.Err != "" {
			fmt.Printf("Error: %s\n", task.Status.Err)
		}
	}

	return nil
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
