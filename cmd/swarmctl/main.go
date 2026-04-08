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
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Println("SwarmKit Control Client")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  ls-nodes          List nodes in the cluster")
	fmt.Println("  ls-services, ls   List services")
	fmt.Println("  ls-tasks          List tasks")
	fmt.Println("  create-service    Create a service from an image")
	fmt.Println("  rm-service        Remove a service")
	fmt.Println("  inspect           Inspect a service or task")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Println("  SWARM_SOCKET      Path to swarm socket (default: /var/run/swarmkit/swarm.sock)")
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
		fmt.Printf("%-20s %-30s %-8s %s\n", svc.ID[:12], name, mode, replicas)
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