// Package client is the one supported way to talk to a SwarmCracker cluster.
//
// It owns the dial, TLS and SwarmKit mapping. The CLI and, later, the HTTP
// gateway use this package instead of importing api.ControlClient directly.
package client

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
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/apiversion"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Config controls how the client reaches a manager.
type Config struct {
	// SocketPath is the SwarmKit control socket. Defaults to $SWARM_SOCKET or
	// /var/run/swarmkit/swarm.sock.
	SocketPath string
	// StateDir holds the node certificates. Defaults to $SWARM_STATE_DIR or
	// /var/lib/swarmkit.
	StateDir string
	// CertDir overrides StateDir/certificates when set.
	CertDir string
	// Timeout is the dial/request default. Zero is replaced by 10s.
	Timeout time.Duration
}

// DefaultConfig reads the same environment variables as the legacy CLI.
func DefaultConfig() Config {
	stateDir := os.Getenv("SWARM_STATE_DIR")
	if stateDir == "" {
		stateDir = "/var/lib/swarmkit"
	}
	socketPath := os.Getenv("SWARM_SOCKET")
	if socketPath == "" {
		socketPath = "/var/run/swarmkit/swarm.sock"
	}
	return Config{
		SocketPath: socketPath,
		StateDir:   stateDir,
		CertDir:    filepath.Join(stateDir, "certificates"),
		Timeout:    10 * time.Second,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.SocketPath == "" {
		c.SocketPath = d.SocketPath
	}
	if c.StateDir == "" {
		c.StateDir = d.StateDir
	}
	if c.CertDir == "" {
		c.CertDir = filepath.Join(c.StateDir, "certificates")
	}
	if c.Timeout == 0 {
		c.Timeout = d.Timeout
	}
	return c
}

// controlClient is the narrow slice of api.ControlClient this package needs.
// Keeping it narrow makes the client testable with a small fake.
type controlClient interface {
	ListServices(context.Context, *api.ListServicesRequest, ...grpc.CallOption) (*api.ListServicesResponse, error)
	ListTasks(context.Context, *api.ListTasksRequest, ...grpc.CallOption) (*api.ListTasksResponse, error)
}

// Client is a typed SwarmCracker client. It is safe for concurrent use.
type Client struct {
	control controlClient
	conn    *grpc.ClientConn
}

// New dials the manager's local control socket. Remote HTTP and bearer-token
// transports are added in phase 1 behind this same API.
func New(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()

	cert, err := tls.LoadX509KeyPair(
		filepath.Join(cfg.CertDir, "swarm-node.crt"),
		filepath.Join(cfg.CertDir, "swarm-node.key"),
	)
	if err != nil {
		return nil, fmt.Errorf("load node certificate: %w", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(cfg.CertDir, "swarm-root-ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse cluster CA: no certificates found")
	}

	// The control socket has no DNS name to verify; the CA and client
	// certificate still authenticate both ends. This exception is local to the
	// Unix-socket transport and must never be used for TCP.
	tlsConfig := &tls.Config{ //nolint:gosec // Unix socket has no hostname
		Certificates:       []tls.Certificate{cert},
		RootCAs:            pool,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true,
	}

	opts := []grpc.DialOption{
		apiversion.WithVersion(),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return net.Dial("unix", cfg.SocketPath)
		}),
	}

	conn, err := grpc.NewClient("unix://"+cfg.SocketPath, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial control socket: %w", err)
	}

	return &Client{control: api.NewControlClient(conn), conn: conn}, nil
}

// Close releases the gRPC connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// ListServices returns services with their desired/running task counts.
func (c *Client) ListServices(ctx context.Context) ([]v1.Service, error) {
	resp, err := c.control.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		return nil, err
	}
	if len(resp.Services) == 0 {
		return []v1.Service{}, nil
	}

	tasks, err := c.control.ListTasks(ctx, &api.ListTasksRequest{})
	if err != nil {
		return nil, err
	}

	counts := countTasks(tasks.Tasks)
	items := make([]v1.Service, 0, len(resp.Services))
	for _, svc := range resp.Services {
		if svc == nil {
			continue
		}
		items = append(items, serviceFromSwarmkit(svc, counts[svc.ID]))
	}
	return items, nil
}
