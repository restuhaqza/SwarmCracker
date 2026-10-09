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
	"github.com/moby/swarmkit/v2/node"
	"github.com/restuhaqza/swarmcracker/pkg/ingress"
	"github.com/restuhaqza/swarmcracker/pkg/swarmkit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// startIngressController runs the manager-side routing mesh reconciler: it
// reads ingress services and their replicas from the control API and programs
// L4 load balancing on this node. Only managers have the global view required.
func startIngressController(ctx context.Context, config *node.Config, executor *swarmkit.Executor) {
	nm := executor.NetworkManager()
	if nm == nil {
		return
	}
	lb, ok := nm.(ingress.LoadBalancer)
	if !ok {
		return
	}

	client, conn, err := newControlClient(config.StateDir, config.ListenControlAPI)
	if err != nil {
		log.G(ctx).WithError(err).Warn("Ingress routing mesh disabled: cannot create control client")
		return
	}
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	ctrl := ingress.NewController(ingress.NewControlClientSource(client), lb, 3*time.Second)
	go ctrl.Run(ctx)
	log.G(ctx).Info("Ingress routing mesh reconciler started")
}

// newControlClient dials the local control API socket using the node's TLS
// material, mirroring the CLI's connection.
func newControlClient(stateDir, socketPath string) (api.ControlClient, *grpc.ClientConn, error) {
	certDir := filepath.Join(stateDir, "certificates")
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certDir, "swarm-node.crt"),
		filepath.Join(certDir, "swarm-node.key"),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load TLS certificate: %w", err)
	}

	caCert, err := os.ReadFile(filepath.Join(certDir, "swarm-root-ca.crt"))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read CA certificate: %w", err)
	}
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCert)

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caPool,
		InsecureSkipVerify: true, //nolint:gosec // self-signed cluster CA, verified via RootCAs
	}

	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to swarm: %w", err)
	}
	return api.NewControlClient(conn), conn, nil
}
