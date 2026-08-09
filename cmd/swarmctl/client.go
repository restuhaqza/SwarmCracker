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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// swarmctlClient bundles the gRPC connection and control client.
type swarmctlClient struct {
	conn   *grpc.ClientConn
	client api.ControlClient
}

// connectControlAPI performs pre-flight checks, loads TLS certificates,
// and establishes a connection to the SwarmKit control socket.
func connectControlAPI() (*swarmctlClient, context.Context, context.CancelFunc) {
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

	client := api.NewControlClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	return &swarmctlClient{conn: conn, client: client}, ctx, cancel
}
