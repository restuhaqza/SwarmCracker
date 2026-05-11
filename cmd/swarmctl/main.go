// swarmctl - Simple SwarmKit control client for testing
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	socketPath := "/var/run/swarmkit/swarm.sock"
	if envSocket := os.Getenv("SWARM_SOCKET"); envSocket != "" {
		socketPath = envSocket
	}

	stateDir := "/var/lib/swarmkit"
	if envState := os.Getenv("SWARM_STATE_DIR"); envState != "" {
		stateDir = envState
	}

	// === PRE-FLIGHT CHECKS: Validate environment before connecting ===

	certDir := filepath.Join(stateDir, "certificates")
	certFile := filepath.Join(certDir, "swarm-node.crt")
	keyFile := filepath.Join(certDir, "swarm-node.key")
	caFile := filepath.Join(certDir, "swarm-root-ca.crt")

	// 1. Check socket exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: SwarmKit control socket not found at %s\n", socketPath)
		fmt.Fprintf(os.Stderr, "\nHints:\n")
		fmt.Fprintf(os.Stderr, "  1. Is the swarmd daemon running? Check: sudo systemctl status swarmd-manager\n")
		fmt.Fprintf(os.Stderr, "  2. Are you on a manager node? Workers don't have control sockets.\n")
		fmt.Fprintf(os.Stderr, "  3. Has the cluster been initialized? Run: sudo swarmcracker init\n")
		os.Exit(1)
	}

	// 2. Check certificates directory exists
	if _, err := os.Stat(certDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: Certificate directory not found at %s\n", certDir)
		fmt.Fprintf(os.Stderr, "\nHints:\n")
		fmt.Fprintf(os.Stderr, "  1. Cluster not initialized. Run: sudo swarmcracker init\n")
		fmt.Fprintf(os.Stderr, "  2. Wrong state directory. Set SWARM_STATE_DIR=/path/to/state\n")
		os.Exit(1)
	}

	// 3. Check node certificate exists
	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: TLS certificate not found at %s\n", certFile)
		fmt.Fprintf(os.Stderr, "\nHints:\n")
		fmt.Fprintf(os.Stderr, "  1. Cluster not initialized. Run: sudo swarmcracker init\n")
		fmt.Fprintf(os.Stderr, "  2. Node certificates not generated. Check daemon logs.\n")
		os.Exit(1)
	}

	// 4. Hint for sudo if not root (warn but continue)
	if os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr, "Warning: swarmctl typically requires root access.\n")
		fmt.Fprintf(os.Stderr, "Hint: Try running with sudo: sudo swarmctl %s\n\n", os.Args[1])
	}

	// === LOAD TLS CERTIFICATES AND CONNECT ===

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load node TLS certificate: %v\n", err)
		fmt.Fprintf(os.Stderr, "Hint: Run with sudo to access certificates in %s\n", certDir)
		os.Exit(1)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read root CA certificate: %v\n", err)
		fmt.Fprintf(os.Stderr, "Hint: Run with sudo to access certificates in %s\n", certDir)
		os.Exit(1)
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: true, // Unix socket, no hostname to verify
	}

	conn, err := grpc.Dial(
		socketPath,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return net.Dial("unix", addr)
		}),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to control socket: %v\n", err)
		fmt.Fprintf(os.Stderr, "Hint: Check if swarmd-manager is running: sudo systemctl status swarmd-manager\n")
		os.Exit(1)
	}
	defer conn.Close()

	client := api.NewControlClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch os.Args[1] {
	case "list-nodes", "ls-nodes":
		listNodes(ctx, client)
	case "list-services", "ls-services", "ls":
		listServices(ctx, client)
	case "list-tasks", "ls-tasks":
		listTasks(ctx, client)
	case "create-service":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl create-service <image> [--network <network-id>] [--name <name>] [--replicas <n>]")
			os.Exit(1)
		}
		createService(ctx, client, os.Args[2:])
	case "create-network":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl create-network <name> [--subnet <subnet>] [--driver <driver>]")
			os.Exit(1)
		}
		createNetwork(ctx, client, os.Args[2:])
	case "list-networks", "ls-networks":
		listNetworks(ctx, client)
	case "remove-service", "rm-service":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl rm-service <service-id>")
			os.Exit(1)
		}
		removeService(ctx, client, os.Args[2])
	case "stop-task":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl stop-task <task-id>")
			os.Exit(1)
		}
		stopTask(ctx, client, os.Args[2])
	case "snapshot":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl snapshot <create|list|restore> ...")
			os.Exit(1)
		}
		handleSnapshotCommand(os.Args[2:])
	case "inspect":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl inspect <service-id|task-id>")
			os.Exit(1)
		}
		inspectService(ctx, client, os.Args[2])
	case "scale":
		if len(os.Args) < 4 {
			fmt.Println("Usage: swarmctl scale <service-id> <replicas>")
			os.Exit(1)
		}
		replicas, err := parseInt(os.Args[3])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid replicas: %v\n", err)
			os.Exit(1)
		}
		scaleService(ctx, client, os.Args[2], replicas)
	case "update":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl update <service-id> [flags]")
			os.Exit(1)
		}
		updateService(ctx, client, os.Args[2:], os.Args[2])
	case "drain":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl drain <node-id>")
			os.Exit(1)
		}
		setNodeAvailability(ctx, client, os.Args[2], api.NodeAvailabilityDrain)
	case "activate":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl activate <node-id>")
			os.Exit(1)
		}
		setNodeAvailability(ctx, client, os.Args[2], api.NodeAvailabilityActive)
	case "pause-node":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl pause-node <node-id>")
			os.Exit(1)
		}
		setNodeAvailability(ctx, client, os.Args[2], api.NodeAvailabilityPause)
	case "promote":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl promote <node-id>")
			os.Exit(1)
		}
		promoteNode(ctx, client, os.Args[2])
	case "demote":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl demote <node-id>")
			os.Exit(1)
		}
		demoteNode(ctx, client, os.Args[2])
	case "logs":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl logs <task-id> [--lines N]")
			os.Exit(1)
		}
		lines := 100
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "--lines" && i+1 < len(os.Args) {
				if n, err := parseInt(os.Args[i+1]); err == nil {
					lines = int(n)
				}
			}
		}
		getTaskLogs(ctx, client, os.Args[2], lines)
	case "metrics":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl metrics <task-id>")
			os.Exit(1)
		}
		getTaskMetrics(ctx, client, os.Args[2])
	case "volume":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl volume <create|list|inspect|rm> ...")
			os.Exit(1)
		}
		handleVolumeCommand(os.Args[2:])
	default:
		printUsage()
	}
}
func printUsage() {
	fmt.Println("SwarmKit Control Client")
	fmt.Println()
	fmt.Println("Services:")
	fmt.Println("  ls-services, ls   List services")
	fmt.Println("  create-service    Create a service from an image")
	fmt.Println("  rm-service        Remove a service")
	fmt.Println("  inspect           Inspect a service or task")
	fmt.Println("  scale             Scale a service to N replicas")
	fmt.Println("  update            Update service (image, replicas, env)")
	fmt.Println()
	fmt.Println("Nodes:")
	fmt.Println("  ls-nodes          List nodes in the cluster")
	fmt.Println("  drain             Drain a node (reschedule tasks)")
	fmt.Println("  activate          Activate a drained/paused node")
	fmt.Println("  pause-node        Pause a node (no new tasks)")
	fmt.Println("  promote           Promote worker to manager")
	fmt.Println("  demote            Demote manager to worker")
	fmt.Println()
	fmt.Println("Tasks:")
	fmt.Println("  ls-tasks          List tasks")
	fmt.Println("  logs              Get VM logs for a task")
	fmt.Println("  metrics           Get VM resource metrics for a task")
	fmt.Println("  stop-task         Stop a running task/VM")
	fmt.Println()
	fmt.Println("Volumes:")
	fmt.Println("  volume create     Create a persistent volume")
	fmt.Println("  volume list       List all volumes")
	fmt.Println("  volume inspect    Inspect a volume")
	fmt.Println("  volume rm         Remove a volume")
	fmt.Println()
	fmt.Println("Snapshots:")
	fmt.Println("  snapshot create   Create a VM snapshot")
	fmt.Println("  snapshot list     List snapshots")
	fmt.Println("  snapshot restore  Restore from snapshot")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Println("  SWARM_SOCKET      Path to swarm socket (default: /var/run/swarmkit/swarm.sock)")
	fmt.Println("  SWARM_STATE_DIR   State directory (default: /var/lib/swarmkit)")
}

func listNodes(ctx context.Context, client api.ControlClient) {
	resp, err := client.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list nodes: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Nodes) == 0 {
		fmt.Println("No nodes found")
		return
	}

	fmt.Printf("%-20s %-12s %-20s %s\n", "ID", "STATUS", "HOSTNAME", "AVAILABILITY")
	fmt.Println(strings.Repeat("-", 70))
	for _, node := range resp.Nodes {
		status := node.Status.State.String()
		avail := node.Spec.Availability.String()
		hostname := node.Description.Hostname
		fmt.Printf("%-20s %-12s %-20s %s\n", node.ID[:12], status, hostname, avail)
	}
	fmt.Printf("\nTotal: %d node(s)\n", len(resp.Nodes))
}

func listServices(ctx context.Context, client api.ControlClient) {
	resp, err := client.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list services: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Services) == 0 {
		fmt.Println("No services found")
		return
	}

	fmt.Printf("%-20s %-30s %-8s %s\n", "ID", "NAME", "MODE", "REPLICAS")
	fmt.Println(strings.Repeat("-", 70))
	for _, svc := range resp.Services {
		name := svc.Spec.Annotations.Name
		if name == "" {
			name = "<unnamed>"
		}
		mode := "replicated"
		replicas := ""
		if replicated := svc.Spec.GetReplicated(); replicated != nil {
			replicas = fmt.Sprintf("%d", replicated.Replicas)
		}
		fmt.Printf("%-24s %-30s %-8s %s\n", svc.ID, name, mode, replicas)
	}
	fmt.Printf("\nTotal: %d service(s)\n", len(resp.Services))
}

func listTasks(ctx context.Context, client api.ControlClient) {
	resp, err := client.ListTasks(ctx, &api.ListTasksRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list tasks: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Tasks) == 0 {
		fmt.Println("No tasks found")
		return
	}

	fmt.Printf("%-20s %-20s %-12s %-12s %s\n", "ID", "SERVICE", "STATUS", "NODE", "IMAGE")
	fmt.Println(strings.Repeat("-", 80))
	for _, task := range resp.Tasks {
		status := task.Status.State.String()
		nodeID := ""
		if task.NodeID != "" {
			nodeID = task.NodeID[:12]
		}
		svcID := ""
		if task.ServiceID != "" {
			svcID = task.ServiceID[:12]
		}
		image := ""
		if task.Spec.GetContainer() != nil {
			image = task.Spec.GetContainer().Image
		}
		// Show full ID for easier use with inspect
		fmt.Printf("%-20s %-20s %-12s %-12s %s\n", task.ID, svcID, status, nodeID, image)
	}
	fmt.Printf("\nTotal: %d task(s)\n", len(resp.Tasks))
}

func createService(ctx context.Context, client api.ControlClient, args []string) {
	// Parse image (first arg)
	image := args[0]

	// Parse flags
	var networkID string
	var svcName string
	var replicas uint64 = 1

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--network":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--network requires a value\n")
				os.Exit(1)
			}
			networkID = args[i+1]
			i++
		case "--name":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--name requires a value\n")
				os.Exit(1)
			}
			svcName = args[i+1]
			i++
		case "--replicas":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--replicas requires a value\n")
				os.Exit(1)
			}
			replicas, _ = parseInt(args[i+1])
			i++
		}
	}

	// Generate service name if not provided
	if svcName == "" {
		svcName = fmt.Sprintf("svc-%s", image[strings.LastIndex(image, "/")+1:])
		if strings.Contains(svcName, ":") {
			svcName = svcName[:strings.Index(svcName, ":")]
		}
		svcName = svcName + "-" + time.Now().Format("150405")
	}

	// Build task spec with optional network
	taskSpec := api.TaskSpec{
		Runtime: &api.TaskSpec_Container{
			Container: &api.ContainerSpec{
				Image: image,
			},
		},
	}

	if networkID != "" {
		taskSpec.Networks = []*api.NetworkAttachmentConfig{
			{
				Target: networkID,
			},
		}
	}

	req := &api.CreateServiceRequest{
		Spec: &api.ServiceSpec{
			Annotations: api.Annotations{
				Name: svcName,
			},
			Task: taskSpec,
			Mode: &api.ServiceSpec_Replicated{
				Replicated: &api.ReplicatedService{
					Replicas: replicas,
				},
			},
		},
	}

	resp, err := client.CreateService(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create service: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Service created: %s\n", resp.Service.ID)
	fmt.Printf("Name: %s\n", svcName)
	fmt.Printf("Image: %s\n", image)
	if networkID != "" {
		fmt.Printf("Network: %s\n", networkID)
	}
	fmt.Printf("Replicas: %d\n", replicas)
}

func createNetwork(ctx context.Context, client api.ControlClient, args []string) {
	// Parse name (first arg)
	name := args[0]

	// Parse flags
	var subnet string = "10.0.9.0/24"
	var driver string = "overlay"

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--subnet":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--subnet requires a value\n")
				os.Exit(1)
			}
			subnet = args[i+1]
			i++
		case "--driver":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--driver requires a value\n")
				os.Exit(1)
			}
			driver = args[i+1]
			i++
		}
	}

	req := &api.CreateNetworkRequest{
		Spec: &api.NetworkSpec{
			Annotations: api.Annotations{
				Name: name,
			},
			DriverConfig: &api.Driver{
				Name: driver,
			},
			IPAM: &api.IPAMOptions{
				Configs: []*api.IPAMConfig{
					{
						Subnet: subnet,
					},
				},
			},
		},
	}

	resp, err := client.CreateNetwork(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create network: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Network created: %s\n", resp.Network.ID)
	fmt.Printf("Name: %s\n", name)
	fmt.Printf("Driver: %s\n", driver)
	fmt.Printf("Subnet: %s\n", subnet)
}

func listNetworks(ctx context.Context, client api.ControlClient) {
	resp, err := client.ListNetworks(ctx, &api.ListNetworksRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list networks: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Networks) == 0 {
		fmt.Println("No networks found")
		return
	}

	fmt.Printf("%-20s %-20s %-15s %s\n", "ID", "NAME", "DRIVER", "SUBNET")
	fmt.Println(strings.Repeat("-", 80))
	for _, net := range resp.Networks {
		name := net.Spec.Annotations.Name
		driver := ""
		if net.Spec.DriverConfig != nil {
			driver = net.Spec.DriverConfig.Name
		}
		subnet := ""
		if net.Spec.IPAM != nil && len(net.Spec.IPAM.Configs) > 0 {
			subnet = net.Spec.IPAM.Configs[0].Subnet
		}
		fmt.Printf("%-20s %-20s %-15s %s\n", net.ID[:12], name, driver, subnet)
	}
	fmt.Printf("\nTotal: %d network(s)\n", len(resp.Networks))
}

func removeService(ctx context.Context, client api.ControlClient, serviceID string) {
	req := &api.RemoveServiceRequest{
		ServiceID: serviceID,
	}

	_, err := client.RemoveService(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to remove service: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Service %s removed\n", serviceID)
}

func inspectService(ctx context.Context, client api.ControlClient, id string) {
	resp, err := client.GetService(ctx, &api.GetServiceRequest{
		ServiceID: id,
	})
	if err != nil {
		// Try as task
		taskResp, taskErr := client.GetTask(ctx, &api.GetTaskRequest{
			TaskID: id,
		})
		if taskErr != nil {
			fmt.Fprintf(os.Stderr, "Failed to find service or task: %v\n", err)
			os.Exit(1)
		}
		printJSON(taskResp.Task)
		return
	}
	printJSON(resp.Service)
}

func printJSON(v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

func parseInt(s string) (uint64, error) {
	var result uint64
	_, err := fmt.Sscanf(s, "%d", &result)
	return result, err
}

func scaleService(ctx context.Context, client api.ControlClient, serviceID string, replicas uint64) {
	// Get current service
	resp, err := client.GetService(ctx, &api.GetServiceRequest{ServiceID: serviceID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get service: %v\n", err)
		os.Exit(1)
	}

	// Update replicas
	svc := resp.Service
	if replicated := svc.Spec.GetReplicated(); replicated != nil {
		replicated.Replicas = replicas
	} else {
		fmt.Fprintf(os.Stderr, "Service is not replicated\n")
		os.Exit(1)
	}

	_, err = client.UpdateService(ctx, &api.UpdateServiceRequest{
		ServiceID:      serviceID,
		ServiceVersion: &svc.Meta.Version,
		Spec:           &svc.Spec,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to scale service: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Service %s scaled to %d replicas\n", serviceID, replicas)
}

func updateService(ctx context.Context, client api.ControlClient, args []string, serviceID string) {
	// Get current service
	resp, err := client.GetService(ctx, &api.GetServiceRequest{ServiceID: serviceID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get service: %v\n", err)
		os.Exit(1)
	}

	svc := resp.Service

	// Parse flags
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--image":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--image requires a value\n")
				os.Exit(1)
			}
			if container := svc.Spec.Task.GetContainer(); container != nil {
				container.Image = args[i+1]
			}
			i++
		case "--replicas":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--replicas requires a value\n")
				os.Exit(1)
			}
			replicas, err := parseInt(args[i+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Invalid replicas: %v\n", err)
				os.Exit(1)
			}
			if replicated := svc.Spec.GetReplicated(); replicated != nil {
				replicated.Replicas = replicas
			}
			i++
		case "--env":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--env requires a value\n")
				os.Exit(1)
			}
			if container := svc.Spec.Task.GetContainer(); container != nil {
				parts := strings.SplitN(args[i+1], "=", 2)
				if len(parts) == 2 {
					// Remove existing env with same key
					for j, env := range container.Env {
						if strings.HasPrefix(env, parts[0]+"=") {
							container.Env = append(container.Env[:j], container.Env[j+1:]...)
							break
						}
					}
					container.Env = append(container.Env, args[i+1])
				}
			}
			i++
		default:
			// Unknown flag, skip
		}
	}

	_, err = client.UpdateService(ctx, &api.UpdateServiceRequest{
		ServiceID:      serviceID,
		ServiceVersion: &svc.Meta.Version,
		Spec:           &svc.Spec,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to update service: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Service %s updated\n", serviceID)
}

func setNodeAvailability(ctx context.Context, client api.ControlClient, nodeID string, availability api.NodeSpec_Availability) {
	// Get current node
	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get node: %v\n", err)
		os.Exit(1)
	}

	node := resp.Node
	node.Spec.Availability = availability

	_, err = client.UpdateNode(ctx, &api.UpdateNodeRequest{
		NodeID:      nodeID,
		NodeVersion: &node.Meta.Version,
		Spec:        &node.Spec,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to update node: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Node %s availability set to %s\n", nodeID, availability.String())
}

func promoteNode(ctx context.Context, client api.ControlClient, nodeID string) {
	// Get current node
	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get node: %v\n", err)
		os.Exit(1)
	}

	node := resp.Node
	node.Spec.DesiredRole = api.NodeRoleManager

	_, err = client.UpdateNode(ctx, &api.UpdateNodeRequest{
		NodeID:      nodeID,
		NodeVersion: &node.Meta.Version,
		Spec:        &node.Spec,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to promote node: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Node %s promoted to manager\n", nodeID)
}

func demoteNode(ctx context.Context, client api.ControlClient, nodeID string) {
	// Get current node
	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get node: %v\n", err)
		os.Exit(1)
	}

	node := resp.Node
	node.Spec.DesiredRole = api.NodeRoleWorker

	_, err = client.UpdateNode(ctx, &api.UpdateNodeRequest{
		NodeID:      nodeID,
		NodeVersion: &node.Meta.Version,
		Spec:        &node.Spec,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to demote node: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Node %s demoted to worker\n", nodeID)
}

// getTaskLogs retrieves logs from a running Firecracker VM.
func getTaskLogs(ctx context.Context, client api.ControlClient, taskID string, lines int) {
	socketDir := "/var/run/firecracker"
	socketPath := filepath.Join(socketDir, taskID+".sock")

	// Check if socket exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "VM socket not found for task %s\n", taskID)
		os.Exit(1)
	}

	// Read from Firecracker log file (stored in rootfs dir)
	logPath := filepath.Join("/var/lib/firecracker/logs", taskID+".log")
	if _, err := os.Stat(logPath); err == nil {
		content, err := os.ReadFile(logPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read log file: %v\n", err)
			os.Exit(1)
		}
		// Print last N lines
		logLines := strings.Split(string(content), "\n")
		start := 0
		if len(logLines) > lines {
			start = len(logLines) - lines
		}
		for i := start; i < len(logLines); i++ {
			fmt.Println(logLines[i])
		}
		return
	}

	// Fallback: try journalctl for the task
	fmt.Printf("No log file found, check: journalctl -u swarmd-* | grep %s\n", taskID)
}

// getTaskMetrics retrieves resource metrics from a running Firecracker VM.
func getTaskMetrics(ctx context.Context, client api.ControlClient, taskID string) {
	socketDir := "/var/run/firecracker"
	socketPath := filepath.Join(socketDir, taskID+".sock")

	// Check if socket exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "VM socket not found for task %s\n", taskID)
		os.Exit(1)
	}

	// Query Firecracker API for machine config
	clientHTTP := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 10 * time.Second,
	}

	// Get machine config
	resp, err := clientHTTP.Get("http://localhost/machine-config")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get machine config: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read response: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== Machine Configuration ===")
	fmt.Println(string(body))

	// Get instance info
	resp2, err := clientHTTP.Get("http://localhost/")
	if err == nil {
		defer resp2.Body.Close()
		body2, _ := io.ReadAll(resp2.Body)
		fmt.Println("=== Instance Info ===")
		fmt.Println(string(body2))
	}
}

// handleVolumeCommand handles volume subcommands.
func handleVolumeCommand(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: swarmctl volume <create|list|inspect|rm> ...")
		os.Exit(1)
	}

	volumesDir := "/var/lib/swarmcracker/volumes"
	if envState := os.Getenv("SWARM_STATE_DIR"); envState != "" {
		volumesDir = filepath.Join(envState, "volumes")
	}

	switch args[0] {
	case "create":
		if len(args) < 2 {
			fmt.Println("Usage: swarmctl volume create <name> [--size MB]")
			os.Exit(1)
		}
		name := args[1]
		sizeMB := 0
		for i := 2; i < len(args); i++ {
			if args[i] == "--size" && i+1 < len(args) {
				sizeMB, _ = strconv.Atoi(args[i+1])
			}
		}
		volPath := filepath.Join(volumesDir, name)
		if err := os.MkdirAll(volPath, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create volume: %v\n", err)
			os.Exit(1)
		}
		// Write metadata
		meta := map[string]string{"name": name, "path": volPath, "size_mb": fmt.Sprintf("%d", sizeMB)}
		metaJSON, _ := json.Marshal(meta)
		os.WriteFile(filepath.Join(volPath, "meta.json"), metaJSON, 0644)
		fmt.Printf("Volume %s created at %s\n", name, volPath)

	case "list", "ls":
		if _, err := os.Stat(volumesDir); os.IsNotExist(err) {
			fmt.Println("No volumes directory")
			return
		}
		entries, err := os.ReadDir(volumesDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to list volumes: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%-20s %-40s %-10s\n", "NAME", "PATH", "SIZE")
		for _, entry := range entries {
			if entry.IsDir() {
				volPath := filepath.Join(volumesDir, entry.Name())
				metaPath := filepath.Join(volPath, "meta.json")
				size := "-"
				if meta, err := os.ReadFile(metaPath); err == nil {
					var m map[string]string
					json.Unmarshal(meta, &m)
					if s, ok := m["size_mb"]; ok && s != "0" {
						size = s + "MB"
					}
				}
				fmt.Printf("%-20s %-40s %-10s\n", entry.Name(), volPath, size)
			}
		}

	case "inspect":
		if len(args) < 2 {
			fmt.Println("Usage: swarmctl volume inspect <name>")
			os.Exit(1)
		}
		name := args[1]
		volPath := filepath.Join(volumesDir, name)
		metaPath := filepath.Join(volPath, "meta.json")
		if _, err := os.Stat(metaPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Volume %s not found\n", name)
			os.Exit(1)
		}
		meta, err := os.ReadFile(metaPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read metadata: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(meta))

	case "rm", "remove":
		if len(args) < 2 {
			fmt.Println("Usage: swarmctl volume rm <name>")
			os.Exit(1)
		}
		name := args[1]
		volPath := filepath.Join(volumesDir, name)
		if err := os.RemoveAll(volPath); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to remove volume: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Volume %s removed\n", name)

	default:
		fmt.Printf("Unknown volume command: %s\n", args[0])
		fmt.Println("Available: create, list, inspect, rm")
		os.Exit(1)
	}
}

// stopTask stops a running task by sending SIGTERM to its Firecracker process.
func stopTask(ctx context.Context, client api.ControlClient, taskID string) {
	socketPath := filepath.Join("/var/run/firecracker", taskID+".sock")

	// Check if socket exists (VM is running)
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		fmt.Printf("Task %s is not running (socket not found)\n", taskID)
		return
	}

	// Find the Firecracker process
	out, err := exec.Command("pgrep", "-f", taskID).Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to find process for task %s: %v\n", taskID, err)
		os.Exit(1)
	}

	pids := strings.Fields(string(out))
	if len(pids) == 0 {
		fmt.Fprintf(os.Stderr, "No process found for task %s\n", taskID)
		os.Exit(1)
	}

	// Send SIGTERM to stop gracefully
	for _, pid := range pids {
		pidInt, _ := strconv.Atoi(pid)
		if err := syscall.Kill(pidInt, syscall.SIGTERM); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to kill process %d: %v\n", pidInt, err)
			continue
		}
		fmt.Printf("Sent SIGTERM to process %d for task %s\n", pidInt, taskID)
	}

	// Wait for process to exit
	time.Sleep(2 * time.Second)

	// Verify stopped
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		fmt.Printf("Task %s stopped successfully\n", taskID)
	} else {
		fmt.Printf("Socket still exists, process may still be running\n")
	}
}

// handleSnapshotCommand handles snapshot subcommands.
func handleSnapshotCommand(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: swarmctl snapshot <create|list|restore> ...")
		os.Exit(1)
	}

	snapshotDir := "/var/lib/swarmcracker/snapshots"
	if envState := os.Getenv("SWARM_STATE_DIR"); envState != "" {
		snapshotDir = filepath.Join(envState, "snapshots")
	}

	switch args[0] {
	case "create":
		if len(args) < 3 {
			fmt.Println("Usage: swarmctl snapshot create <task-id> <snapshot-name>")
			os.Exit(1)
		}
		taskID := args[1]
		name := args[2]

		// Create snapshot directory
		os.MkdirAll(snapshotDir, 0755)

		// Find the rootfs for this task
		rootfsDir := "/var/lib/firecracker/rootfs"
		taskRootfs := filepath.Join(rootfsDir, taskID)

		// If task-specific rootfs doesn't exist, try to find it by service
		if _, err := os.Stat(taskRootfs); os.IsNotExist(err) {
			// Check for image-based rootfs
			entries, _ := os.ReadDir(rootfsDir)
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".ext4") {
					taskRootfs = filepath.Join(rootfsDir, entry.Name())
					break
				}
			}
		}

		// Create snapshot metadata
		snapPath := filepath.Join(snapshotDir, name)
		os.MkdirAll(snapPath, 0755)

		meta := map[string]interface{}{
			"name":    name,
			"task_id": taskID,
			"created": time.Now().Format(time.RFC3339),
			"rootfs":  taskRootfs,
		}
		metaJSON, _ := json.Marshal(meta)
		os.WriteFile(filepath.Join(snapPath, "meta.json"), metaJSON, 0644)

		// Copy rootfs if it exists
		if _, err := os.Stat(taskRootfs); err == nil {
			srcFile, err := os.Open(taskRootfs)
			if err == nil {
				defer srcFile.Close()
				dstPath := filepath.Join(snapPath, "rootfs.ext4")
				dstFile, err := os.Create(dstPath)
				if err == nil {
					defer dstFile.Close()
					io.Copy(dstFile, srcFile)
					fmt.Printf("Snapshot %s created (copied rootfs from %s)\n", name, taskRootfs)
				} else {
					fmt.Printf("Snapshot %s created (metadata only - could not copy rootfs)\n", name)
				}
			} else {
				fmt.Printf("Snapshot %s created (metadata only)\n", name)
			}
		} else {
			fmt.Printf("Snapshot %s created (metadata only - no rootfs found)\n", name)
		}

	case "list", "ls":
		if _, err := os.Stat(snapshotDir); os.IsNotExist(err) {
			fmt.Println("No snapshots directory")
			return
		}
		entries, err := os.ReadDir(snapshotDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to list snapshots: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%-20s %-30s %-20s\n", "NAME", "CREATED", "TASK_ID")
		for _, entry := range entries {
			if entry.IsDir() {
				metaPath := filepath.Join(snapshotDir, entry.Name(), "meta.json")
				if meta, err := os.ReadFile(metaPath); err == nil {
					var m map[string]interface{}
					json.Unmarshal(meta, &m)
					created := ""
					if c, ok := m["created"]; ok {
						created = c.(string)
					}
					taskID := ""
					if t, ok := m["task_id"]; ok {
						taskID = t.(string)
					}
					fmt.Printf("%-20s %-30s %-20s\n", entry.Name(), created, taskID)
				} else {
					fmt.Printf("%-20s %-30s %-20s\n", entry.Name(), "unknown", "unknown")
				}
			}
		}

	case "restore":
		if len(args) < 2 {
			fmt.Println("Usage: swarmctl snapshot restore <snapshot-name>")
			os.Exit(1)
		}
		name := args[1]
		snapPath := filepath.Join(snapshotDir, name)
		metaPath := filepath.Join(snapPath, "meta.json")

		if _, err := os.Stat(metaPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Snapshot %s not found\n", name)
			os.Exit(1)
		}

		meta, err := os.ReadFile(metaPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read snapshot metadata: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Snapshot %s metadata:\n%s\n", name, string(meta))
		fmt.Println("\nTo restore, use the rootfs path in meta.json when creating a new service")

	case "rm", "remove":
		if len(args) < 2 {
			fmt.Println("Usage: swarmctl snapshot rm <snapshot-name>")
			os.Exit(1)
		}
		name := args[1]
		snapPath := filepath.Join(snapshotDir, name)
		if err := os.RemoveAll(snapPath); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to remove snapshot: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Snapshot %s removed\n", name)

	default:
		fmt.Printf("Unknown snapshot command: %s\n", args[0])
		fmt.Println("Available: create, list, restore, rm")
		os.Exit(1)
	}
}
