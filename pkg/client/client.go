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
	// Timeout is the dial/request default. Zero is replaced by 10s locally and
	// 30s for a remote endpoint.
	Timeout time.Duration

	// Endpoint is the manager's HTTPS API base URL, e.g.
	// https://manager:4244. Empty means the local Unix socket.
	Endpoint string
	// CAFile is the PEM CA bundle used to verify Endpoint. It is required
	// whenever Endpoint is set; there is no unverified fallback.
	CAFile string
	// TokenFile holds a bearer token. Surrounding whitespace is trimmed.
	TokenFile string
	// Token is an explicit bearer token. It wins over TokenFile.
	Token string
	// ServerName overrides the TLS server name used to verify Endpoint.
	ServerName string
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
	GetTask(context.Context, *api.GetTaskRequest, ...grpc.CallOption) (*api.GetTaskResponse, error)
	ListNodes(context.Context, *api.ListNodesRequest, ...grpc.CallOption) (*api.ListNodesResponse, error)
	GetNode(context.Context, *api.GetNodeRequest, ...grpc.CallOption) (*api.GetNodeResponse, error)
	ListNetworks(context.Context, *api.ListNetworksRequest, ...grpc.CallOption) (*api.ListNetworksResponse, error)
	GetNetwork(context.Context, *api.GetNetworkRequest, ...grpc.CallOption) (*api.GetNetworkResponse, error)
	ListConfigs(context.Context, *api.ListConfigsRequest, ...grpc.CallOption) (*api.ListConfigsResponse, error)
	GetConfig(context.Context, *api.GetConfigRequest, ...grpc.CallOption) (*api.GetConfigResponse, error)
	ListSecrets(context.Context, *api.ListSecretsRequest, ...grpc.CallOption) (*api.ListSecretsResponse, error)
	GetSecret(context.Context, *api.GetSecretRequest, ...grpc.CallOption) (*api.GetSecretResponse, error)
	ListClusters(context.Context, *api.ListClustersRequest, ...grpc.CallOption) (*api.ListClustersResponse, error)
}

// Client is a typed SwarmCracker client. It is safe for concurrent use.
type Client struct {
	control controlClient
	conn    *grpc.ClientConn
	remote  *remoteClient
}

// New returns a client for cfg. A non-empty Config.Endpoint selects the remote
// HTTPS API; otherwise the manager's local control socket is dialed.
func New(cfg Config) (*Client, error) {
	if cfg.Endpoint != "" {
		return newRemote(cfg)
	}
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

// Close releases the transport. A local client closes its gRPC connection; a
// remote client closes idle HTTP keep-alive connections.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	if c.remote != nil {
		c.remote.close()
		return nil
	}
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// ListServices returns services with their desired/running task counts.
func (c *Client) ListServices(ctx context.Context) ([]v1.Service, error) {
	if c.remote != nil {
		return c.remote.listServices(ctx)
	}
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
	if c.remote != nil {
		return c.remote.getService(ctx, id)
	}
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

// GetTask returns one task with its node name resolved.
//
// The node name comes from one ListNodes call and only when the task has a
// node; a task that has not been scheduled needs no node lookup.
func (c *Client) GetTask(ctx context.Context, id string) (v1.Task, error) {
	if c.remote != nil {
		return c.remote.getTask(ctx, id)
	}
	resp, err := c.control.GetTask(ctx, &api.GetTaskRequest{TaskID: id})
	if err != nil {
		return v1.Task{}, err
	}
	if resp == nil || resp.Task == nil {
		return v1.Task{}, status.Error(codes.NotFound, "task not found")
	}

	var names map[string]string
	if resp.Task.NodeID != "" {
		nodes, err := c.control.ListNodes(ctx, &api.ListNodesRequest{})
		if err != nil {
			return v1.Task{}, err
		}
		if nodes != nil {
			names = nodeNameIndex(nodes.Nodes)
		}
	}

	return taskFromSwarmkit(resp.Task, names), nil
}

// GetNode returns one cluster node.
func (c *Client) GetNode(ctx context.Context, id string) (v1.Node, error) {
	if c.remote != nil {
		return c.remote.getNode(ctx, id)
	}
	resp, err := c.control.GetNode(ctx, &api.GetNodeRequest{NodeID: id})
	if err != nil {
		return v1.Node{}, err
	}
	if resp == nil || resp.Node == nil {
		return v1.Node{}, status.Error(codes.NotFound, "node not found")
	}
	return nodeFromSwarmkit(resp.Node), nil
}

// GetNetwork returns one network by ID.
//
// Unlike ListNetworks, a direct read does not filter built-in networks, so
// ingress and docker_gwbridge are returned; only the list hides them.
func (c *Client) GetNetwork(ctx context.Context, id string) (v1.Network, error) {
	if c.remote != nil {
		return c.remote.getNetwork(ctx, id)
	}
	resp, err := c.control.GetNetwork(ctx, &api.GetNetworkRequest{NetworkID: id})
	if err != nil {
		return v1.Network{}, err
	}
	if resp == nil || resp.Network == nil {
		return v1.Network{}, status.Error(codes.NotFound, "network not found")
	}
	return networkFromSwarmkit(resp.Network), nil
}

// GetConfig returns one config's metadata. The payload is never read.
func (c *Client) GetConfig(ctx context.Context, id string) (v1.Config, error) {
	if c.remote != nil {
		return c.remote.getConfig(ctx, id)
	}
	resp, err := c.control.GetConfig(ctx, &api.GetConfigRequest{ConfigID: id})
	if err != nil {
		return v1.Config{}, err
	}
	if resp == nil || resp.Config == nil {
		return v1.Config{}, status.Error(codes.NotFound, "config not found")
	}
	return configFromSwarmkit(resp.Config), nil
}

// GetSecret returns one secret's metadata. The payload is never read.
func (c *Client) GetSecret(ctx context.Context, id string) (v1.Secret, error) {
	if c.remote != nil {
		return c.remote.getSecret(ctx, id)
	}
	resp, err := c.control.GetSecret(ctx, &api.GetSecretRequest{SecretID: id})
	if err != nil {
		return v1.Secret{}, err
	}
	if resp == nil || resp.Secret == nil {
		return v1.Secret{}, status.Error(codes.NotFound, "secret not found")
	}
	return secretFromSwarmkit(resp.Secret), nil
}

// ClusterID returns the cluster's ID, which a local manager answers because a
// node belongs to exactly one cluster. An empty cluster list is NotFound; a
// remote version document with an empty clusterId is NotFound too.
func (c *Client) ClusterID(ctx context.Context) (string, error) {
	if c.remote != nil {
		return c.remote.clusterID(ctx)
	}
	resp, err := c.control.ListClusters(ctx, &api.ListClustersRequest{})
	if err != nil {
		return "", err
	}
	if resp == nil || len(resp.Clusters) == 0 || resp.Clusters[0] == nil {
		return "", status.Error(codes.NotFound, "cluster not found")
	}
	return resp.Clusters[0].ID, nil
}

// ListNetworks returns user-defined networks, ordered by creation time then
// ID. The built-in ingress and gateway networks are excluded, matching the
// legacy table.
func (c *Client) ListNetworks(ctx context.Context) ([]v1.Network, error) {
	if c.remote != nil {
		return c.remote.listNetworks(ctx)
	}
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
	if c.remote != nil {
		return c.remote.listConfigs(ctx)
	}
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
	if c.remote != nil {
		return c.remote.listSecrets(ctx)
	}
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
	if c.remote != nil {
		return c.remote.listNodes(ctx)
	}
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
	if c.remote != nil {
		return c.remote.listTasks(ctx)
	}
	return c.listTasks(ctx, &api.ListTasksRequest{})
}

// ListServiceTasks returns one service's tasks, ordered by creation time then
// ID. The manager filters the task list; the node lookup is shared with
// ListTasks so nodeName stays populated.
func (c *Client) ListServiceTasks(ctx context.Context, serviceID string) ([]v1.Task, error) {
	if c.remote != nil {
		return c.remote.listServiceTasks(ctx, serviceID)
	}
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
