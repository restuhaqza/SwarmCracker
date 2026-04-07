package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	socketPath := "/var/run/swarmkit/swarm.sock"
	role := "worker"
	if len(os.Args) >= 2 {
		socketPath = os.Args[1]
	}
	if len(os.Args) >= 3 {
		role = os.Args[2]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		}),
	}

	conn, err := grpc.Dial("unix://"+socketPath, append(dialOpts, grpc.WithBlock())...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	client := api.NewControlClient(conn)
	resp, err := client.ListClusters(ctx, &api.ListClustersRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list clusters: %v\n", err)
		os.Exit(1)
	}

	for _, cluster := range resp.Clusters {
		fmt.Printf("Cluster: %s\n", cluster.ID)
		if role == "worker" {
			fmt.Printf("Worker Join Token: %s\n", cluster.RootCA.JoinTokens.Worker)
		} else {
			fmt.Printf("Manager Join Token: %s\n", cluster.RootCA.JoinTokens.Manager)
		}
	}
}
