// swarmctl - Simple SwarmKit control client for testing
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
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

	// Load TLS certificates for mTLS to the control socket
	certDir := filepath.Join(stateDir, "certificates")
	certFile := filepath.Join(certDir, "swarm-node.crt")
	keyFile := filepath.Join(certDir, "swarm-node.key")
	caFile := filepath.Join(certDir, "swarm-root-ca.crt")

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load node TLS certificate: %v\n", err)
		os.Exit(1)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read root CA certificate: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "Failed to connect: %v\n", err)
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
			fmt.Println("Usage: swarmctl create-service <image>")
			os.Exit(1)
		}
		createService(ctx, client, os.Args[2])
	case "remove-service", "rm-service":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl rm-service <service-id>")
			os.Exit(1)
		}
		removeService(ctx, client, os.Args[2])
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
	fmt.Println(string(make([]byte, 70)))
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
	fmt.Println(string(make([]byte, 70)))
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
	fmt.Println(string(make([]byte, 80)))
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
		fmt.Printf("%-20s %-20s %-12s %-12s %s\n", task.ID[:12], svcID, status, nodeID, image)
	}
	fmt.Printf("\nTotal: %d task(s)\n", len(resp.Tasks))
}

func createService(ctx context.Context, client api.ControlClient, image string) {
	// Create a simple replicated service
	svcName := fmt.Sprintf("svc-%s", image[strings.LastIndex(image, "/")+1:])
	if strings.Contains(svcName, ":") {
		svcName = svcName[:strings.Index(svcName, ":")]
	}
	svcName = svcName + "-" + time.Now().Format("150405")

	req := &api.CreateServiceRequest{
		Spec: &api.ServiceSpec{
			Annotations: api.Annotations{
				Name: svcName,
			},
			Task: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image: image,
					},
				},
			},
			Mode: &api.ServiceSpec_Replicated{
				Replicated: &api.ReplicatedService{
					Replicas: 1,
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