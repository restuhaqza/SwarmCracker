package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/apiversion"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// runGetJoinToken retrieves join tokens from SwarmKit via the manager's local
// control socket. The control socket serves mutual TLS, so the node's own
// certificates are used (matching cmd/swarmd-firecracker/tokens.go).
func runGetJoinToken(role string) error {
	socketPath := "/var/run/swarmkit/swarm.sock"
	if envSocket := os.Getenv("SWARM_SOCKET"); envSocket != "" {
		socketPath = envSocket
	}

	stateDir := "/var/lib/swarmkit"
	if envState := os.Getenv("SWARM_STATE_DIR"); envState != "" {
		stateDir = envState
	}

	certDir := filepath.Join(stateDir, "certificates")
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certDir, "swarm-node.crt"),
		filepath.Join(certDir, "swarm-node.key"),
	)
	if err != nil {
		return fmt.Errorf("failed to load TLS certificate: %w", err)
	}

	caCert, err := os.ReadFile(filepath.Join(certDir, "swarm-root-ca.crt"))
	if err != nil {
		return fmt.Errorf("failed to read CA certificate: %w", err)
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: true, // Unix socket, no hostname to verify
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dialOpts := []grpc.DialOption{
		apiversion.WithVersion(),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithContextDialer(func(_ context.Context, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		}),
	}

	conn, err := grpc.NewClient("unix://"+socketPath, dialOpts...)
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
