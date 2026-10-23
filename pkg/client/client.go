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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
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
	GetService(context.Context, *api.GetServiceRequest, ...grpc.CallOption) (*api.GetServiceResponse, error)
	ListTasks(context.Context, *api.ListTasksRequest, ...grpc.CallOption) (*api.ListTasksResponse, error)
	ListNodes(context.Context, *api.ListNodesRequest, ...grpc.CallOption) (*api.ListNodesResponse, error)
	ListNetworks(context.Context, *api.ListNetworksRequest, ...grpc.CallOption) (*api.ListNetworksResponse, error)
	ListConfigs(context.Context, *api.ListConfigsRequest, ...grpc.CallOption) (*api.ListConfigsResponse, error)
	ListSecrets(context.Context, *api.ListSecretsRequest, ...grpc.CallOption) (*api.ListSecretsResponse, error)
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
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS12,
		//nolint:gosec // G402: Unix socket has no hostname to verify.
		InsecureSkipVerify: true,
	}

	opts := []grpc.DialOption{
		apiversion.WithVersion(),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", cfg.SocketPath)
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

// GetService returns one service with its observed task counts.
func (c *Client) GetService(ctx context.Context, id string) (v1.Service, error) {
	resp, err := c.control.GetService(ctx, &api.GetServiceRequest{ServiceID: id})
	if err != nil {
		return v1.Service{}, err
	}
	if resp == nil || resp.Service == nil {
		return v1.Service{}, status.Error(codes.NotFound, "service not found")
	}

	tasks, err := c.control.ListTasks(ctx, &api.ListTasksRequest{
		Filters: &api.ListTasksRequest_Filters{ServiceIDs: []string{id}},
	})
	if err != nil {
		return v1.Service{}, err
	}

	return serviceFromSwarmkit(resp.Service, countTasks(tasks.Tasks)[id]), nil
}

// ListNetworks returns user-defined networks, ordered by creation time then
// ID. The built-in ingress and gateway networks are excluded, matching the
// legacy table.
func (c *Client) ListNetworks(ctx context.Context) ([]v1.Network, error) {
	resp, err := c.control.ListNetworks(ctx, &api.ListNetworksRequest{})
	if err != nil {
		return nil, err
	}

	networks := sortNetworks(resp.Networks)
	items := make([]v1.Network, 0, len(networks))
	for _, network := range networks {
		if isBuiltinNetwork(network) {
			continue
		}
		items = append(items, networkFromSwarmkit(network))
	}
	return items, nil
}

// ListConfigs returns config metadata without payloads, ordered by creation
// time then ID.
func (c *Client) ListConfigs(ctx context.Context) ([]v1.Config, error) {
	resp, err := c.control.ListConfigs(ctx, &api.ListConfigsRequest{})
	if err != nil {
		return nil, err
	}

	configs := sortConfigs(resp.Configs)
	items := make([]v1.Config, 0, len(configs))
	for _, cfg := range configs {
		items = append(items, configFromSwarmkit(cfg))
	}
	return items, nil
}

// ListSecrets returns secret metadata without payloads, ordered by creation
// time then ID.
func (c *Client) ListSecrets(ctx context.Context) ([]v1.Secret, error) {
	resp, err := c.control.ListSecrets(ctx, &api.ListSecretsRequest{})
	if err != nil {
		return nil, err
	}

	secrets := sortSecrets(resp.Secrets)
	items := make([]v1.Secret, 0, len(secrets))
	for _, secret := range secrets {
		items = append(items, secretFromSwarmkit(secret))
	}
	return items, nil
}

// ListNodes returns the cluster nodes, ordered by creation time then ID.
func (c *Client) ListNodes(ctx context.Context) ([]v1.Node, error) {
	resp, err := c.control.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		return nil, err
	}

	nodes := sortNodes(resp.Nodes)
	items := make([]v1.Node, 0, len(nodes))
	for _, node := range nodes {
		items = append(items, nodeFromSwarmkit(node))
	}
	return items, nil
}

// ListTasks returns tasks with their service and node names, ordered by
// creation time then ID.
//
// Node names come from one ListNodes call, so a node read failure fails the
// list: the contract includes nodeName and a caller that cannot read nodes
// cannot produce the document. An empty task list never calls ListNodes.
func (c *Client) ListTasks(ctx context.Context) ([]v1.Task, error) {
	return c.listTasks(ctx, &api.ListTasksRequest{})
}

// ListServiceTasks returns one service's tasks, ordered by creation time then
// ID. The manager filters the task list; the node lookup is shared with
// ListTasks so nodeName stays populated.
func (c *Client) ListServiceTasks(ctx context.Context, serviceID string) ([]v1.Task, error) {
	return c.listTasks(ctx, &api.ListTasksRequest{
		Filters: &api.ListTasksRequest_Filters{ServiceIDs: []string{serviceID}},
	})
}

func (c *Client) listTasks(ctx context.Context, req *api.ListTasksRequest) ([]v1.Task, error) {
	resp, err := c.control.ListTasks(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(resp.Tasks) == 0 {
		return []v1.Task{}, nil
	}

	nodes, err := c.control.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		return nil, err
	}
	names := nodeNameIndex(nodes.Nodes)

	tasks := sortTasks(resp.Tasks)
	items := make([]v1.Task, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, taskFromSwarmkit(task, names))
	}
	return items, nil
}
