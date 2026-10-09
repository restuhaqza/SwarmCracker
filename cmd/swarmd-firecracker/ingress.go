package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
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

// ingressRuntime holds the daemon's ingress mesh settings.
type ingressRuntime struct {
	enabled bool
	port    int
	// managerAddr is the host:port of a manager's ingress table endpoint. It is
	// empty on managers, which serve the table themselves.
	managerAddr string
}

func (ing ingressRuntime) tableURL() string {
	return fmt.Sprintf("https://%s/v1/ingress", ing.managerAddr)
}

// startIngress wires the ingress routing mesh. Managers compute the routing
// table from the control API, program their own load balancer, and serve the
// table over mutual TLS; workers fetch the table and program their own load
// balancer, so a published port is reachable on every node.
//
// It returns a cleanup function that clears this node's ingress rules, to be
// called on shutdown (nil when the mesh is disabled).
func startIngress(ctx context.Context, config *node.Config, executor *swarmkit.Executor, ing ingressRuntime) func() {
	if !ing.enabled {
		return nil
	}
	nm := executor.NetworkManager()
	if nm == nil {
		return nil
	}
	lb, ok := nm.(ingress.LoadBalancer)
	if !ok {
		return nil
	}

	cert, caPool, err := loadIngressTLS(config.StateDir)
	if err != nil {
		log.G(ctx).WithError(err).Warn("Ingress mesh disabled: TLS material unavailable")
		return nil
	}

	if ing.managerAddr == "" {
		startIngressManager(ctx, config, lb, ing, cert, caPool)
	} else {
		startIngressWorker(ctx, lb, ing, cert, caPool)
	}

	// Clear local ingress rules on shutdown so a node that leaves the cluster
	// does not leave stale forwarding behind.
	return func() {
		if err := lb.ClearIngress(); err != nil {
			log.G(ctx).WithError(err).Warn("Failed to clear ingress rules on shutdown")
		}
	}
}

// startIngressManager runs the reconciler (control API -> local LB) and serves
// the routing table to the other nodes.
func startIngressManager(ctx context.Context, config *node.Config, lb ingress.LoadBalancer, ing ingressRuntime, cert tls.Certificate, caPool *x509.CertPool) {
	client, conn, err := newControlClient(config.StateDir, config.ListenControlAPI)
	if err != nil {
		log.G(ctx).WithError(err).Warn("Ingress mesh disabled: cannot create control client")
		return
	}
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	ctrl := ingress.NewController(ingress.NewControlClientSource(client), lb, 3*time.Second)
	go ctrl.Run(ctx)

	srv := ingress.NewTableServer(ctrl, serverTLSConfig(cert, caPool), fmt.Sprintf(":%d", ing.port))
	go func() {
		if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.G(ctx).WithError(err).Warn("Ingress table server stopped")
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.G(ctx).Infof("Ingress mesh: manager balancing locally and serving the table on :%d", ing.port)
}

// startIngressWorker fetches the routing table from a manager and programs the
// local load balancer from it.
func startIngressWorker(ctx context.Context, lb ingress.LoadBalancer, ing ingressRuntime, cert tls.Certificate, caPool *x509.CertPool) {
	tc := ingress.NewTableClient(ing.tableURL(), clientTLSConfig(cert, caPool), 3*time.Second)
	go tc.Run(ctx, lb)
	log.G(ctx).Infof("Ingress mesh: worker fetching the table from %s", ing.managerAddr)
}

// loadIngressTLS loads the node's cluster certificate and CA for the ingress
// mesh's mutual TLS.
func loadIngressTLS(stateDir string) (tls.Certificate, *x509.CertPool, error) {
	certDir := filepath.Join(stateDir, "certificates")
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certDir, "swarm-node.crt"),
		filepath.Join(certDir, "swarm-node.key"),
	)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load node certificate: %w", err)
	}
	caCert, err := os.ReadFile(filepath.Join(certDir, "swarm-root-ca.crt"))
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return tls.Certificate{}, nil, fmt.Errorf("parse cluster CA")
	}
	return cert, pool, nil
}

func serverTLSConfig(cert tls.Certificate, caPool *x509.CertPool) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
}

func clientTLSConfig(cert tls.Certificate, caPool *x509.CertPool) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		// Cluster certificates are identified by node ID, not by the address the
		// client dials, so hostname verification is skipped. The server chain is
		// still verified against the cluster CA below.
		InsecureSkipVerify: true, //nolint:gosec // chain verified in VerifyPeerCertificate
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("server presented no certificate")
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			intermediates := x509.NewCertPool()
			for _, raw := range rawCerts[1:] {
				if c, err := x509.ParseCertificate(raw); err == nil {
					intermediates.AddCert(c)
				}
			}
			_, err = leaf.Verify(x509.VerifyOptions{
				Roots:         caPool,
				Intermediates: intermediates,
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
			})
			return err
		},
	}
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
