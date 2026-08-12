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
	"github.com/moby/swarmkit/v2/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// printJoinTokens reads the actual join tokens from the cluster's Raft store
// via the local control API socket using the node's own TLS certificates.
func printJoinTokens(ctx context.Context, stateDir string) {
	socketPath := "/var/run/swarmkit/swarm.sock"

	// Load the node's TLS certificates for mTLS to the control socket
	certDir := filepath.Join(stateDir, "certificates")
	certFile := filepath.Join(certDir, "swarm-node.crt")
	keyFile := filepath.Join(certDir, "swarm-node.key")
	caFile := filepath.Join(certDir, "swarm-root-ca.crt")

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.G(ctx).WithError(err).Warn("Failed to load node TLS certificate for token retrieval")
		return
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		log.G(ctx).WithError(err).Warn("Failed to read root CA certificate")
		return
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: true, // Unix socket, no hostname to verify
	}

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithContextDialer(func(dialCtx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(dialCtx, "unix", socketPath)
		}),
		grpc.WithBlock(),
	}

	conn, err := grpc.Dial("unix://"+socketPath, dialOpts...)
	if err != nil {
		log.G(ctx).WithError(err).Warn("Failed to connect to control API for token retrieval")
		return
	}
	defer conn.Close()

	client := api.NewControlClient(conn)
	listCtx, listCancel := context.WithTimeout(ctx, 10*time.Second)
	defer listCancel()

	resp, err := client.ListClusters(listCtx, &api.ListClustersRequest{})
	if err != nil {
		log.G(ctx).WithError(err).Warn("Failed to list clusters for token retrieval")
		return
	}

	for _, cluster := range resp.Clusters {
		workerToken := cluster.RootCA.JoinTokens.Worker
		managerToken := cluster.RootCA.JoinTokens.Manager

		log.G(ctx).Infof("========================================")
		log.G(ctx).Infof("CLUSTER JOIN TOKENS")
		log.G(ctx).Infof("========================================")
		log.G(ctx).Debugf("Worker token: %s", workerToken)   // DEBUG level to avoid exposing token in logs
		log.G(ctx).Debugf("Manager token: %s", managerToken) // DEBUG level to avoid exposing token in logs
		log.G(ctx).Infof("========================================")

		// Write tokens to a file
		tokenFile := filepath.Join(stateDir, "join-tokens.txt")
		tokenContent := fmt.Sprintf("WORKER_TOKEN=%s\nMANAGER_TOKEN=%s\n", workerToken, managerToken)
		if err := os.WriteFile(tokenFile, []byte(tokenContent), 0600); err != nil {
			log.G(ctx).WithError(err).Warnf("Failed to write join tokens to %s", tokenFile)
		} else {
			log.G(ctx).Infof("Join tokens saved to %s", tokenFile)
		}
	}
}
