package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/apiversion"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// runGetJoinToken retrieves join tokens from SwarmKit
func runGetJoinToken(role string) error {
	socketPath := "/var/run/swarmkit/swarm.sock"
	if envSocket := os.Getenv("SWARM_SOCKET"); envSocket != "" {
		socketPath = envSocket
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dialOpts := []grpc.DialOption{
		apiversion.WithVersion(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		}),
	}

	conn, err := grpc.Dial("unix://"+socketPath, append(dialOpts, grpc.WithBlock())...)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()

	client := api.NewControlClient(conn)
	resp, err := client.ListClusters(ctx, &api.ListClustersRequest{})
	if err != nil {
		return fmt.Errorf("failed to list clusters: %w", err)
	}

	for _, cluster := range resp.Clusters {
		fmt.Printf("Cluster: %s\n", cluster.ID)
		switch role {
		case "worker":
			fmt.Printf("Worker Join Token: %s\n", cluster.RootCA.JoinTokens.Worker)
		case "manager":
			fmt.Printf("Manager Join Token: %s\n", cluster.RootCA.JoinTokens.Manager)
		default:
			fmt.Printf("Worker Join Token: %s\n", cluster.RootCA.JoinTokens.Worker)
			fmt.Printf("Manager Join Token: %s\n", cluster.RootCA.JoinTokens.Manager)
		}
	}

	return nil
}
