// Package network provides network management for SwarmCracker.
package network

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"google.golang.org/grpc"
)

// HostnameNodeDiscovery implements types.NodeDiscovery using hostname DNS resolution.
type HostnameNodeDiscovery struct {
	localHostname string
	clusterNodes  []string
}

// NewHostnameNodeDiscovery creates hostname-based node discovery.
func NewHostnameNodeDiscovery(localHostname string, clusterNodes []string) *HostnameNodeDiscovery {
	return &HostnameNodeDiscovery{
		localHostname: localHostname,
		clusterNodes:  clusterNodes,
	}
}

// GetNodes returns peer nodes by resolving hostnames via DNS.
func (d *HostnameNodeDiscovery) GetNodes() ([]types.NodeInfo, error) {
	nodes := []types.NodeInfo{}
	for _, hostname := range d.clusterNodes {
		if hostname == d.localHostname {
			continue
		}
		ips, err := net.LookupHost(hostname)
		if err != nil {
			continue
		}
		if len(ips) > 0 {
			nodes = append(nodes, types.NodeInfo{
				ID:       hostname,
				IP:       ips[0],
				VXLANIP:  ips[0],
				Status:   "ready",
				Hostname: hostname,
			})
		}
	}
	return nodes, nil
}

// SwarmKitNodeDiscovery implements types.NodeDiscovery using SwarmKit API.
type SwarmKitNodeDiscovery struct {
	controlSocket string
	remoteAddr    string
	client        api.ControlClient
	conn          *grpc.ClientConn
	localNodeID   string
	localHostname string
	timeout       time.Duration
}

// NewSwarmKitNodeDiscovery creates SwarmKit API-based node discovery.
func NewSwarmKitNodeDiscovery(controlSocket, remoteAddr, localNodeID, localHostname string) (*SwarmKitNodeDiscovery, error) {
	return &SwarmKitNodeDiscovery{
		controlSocket: controlSocket,
		remoteAddr:    remoteAddr,
		localNodeID:   localNodeID,
		localHostname: localHostname,
		timeout:       10 * time.Second,
	}, nil
}

// Connect establishes connection to SwarmKit API.
func (d *SwarmKitNodeDiscovery) Connect(ctx context.Context) error {
	var conn *grpc.ClientConn
	var err error

	if d.controlSocket != "" {
		dialer := func(addr string, t time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, t)
		}
		conn, err = grpc.Dial(d.controlSocket,
			grpc.WithInsecure(),
			grpc.WithTimeout(d.timeout),
			grpc.WithDialer(dialer))
	} else if d.remoteAddr != "" {
		conn, err = grpc.Dial(d.remoteAddr,
			grpc.WithInsecure(),
			grpc.WithTimeout(d.timeout))
	} else {
		return fmt.Errorf("no control socket or remote address")
	}

	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	d.conn = conn
	d.client = api.NewControlClient(conn)
	return nil
}

// Close closes the connection.
func (d *SwarmKitNodeDiscovery) Close() error {
	if d.conn != nil {
		return d.conn.Close()
	}
	return nil
}

// GetNodes returns peer nodes from SwarmKit cluster.
func (d *SwarmKitNodeDiscovery) GetNodes() ([]types.NodeInfo, error) {
	if d.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()

	resp, err := d.client.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	nodes := []types.NodeInfo{}
	for _, node := range resp.Nodes {
		if node.ID == d.localNodeID {
			continue
		}
		if node.Description != nil && node.Description.Hostname == d.localHostname {
			continue
		}
		if node.Status.State != api.NodeStatus_READY {
			continue
		}

		nodeIP := ""
		// Manager: extract from ManagerStatus.Addr
		if node.ManagerStatus != nil {
			host, _, _ := net.SplitHostPort(node.ManagerStatus.Addr)
			nodeIP = host
		}
		// Worker: resolve hostname
		if nodeIP == "" && node.Description != nil {
			ips, _ := net.LookupHost(node.Description.Hostname)
			if len(ips) > 0 {
				nodeIP = ips[0]
			}
		}

		if nodeIP != "" {
			nodes = append(nodes, types.NodeInfo{
				ID:       node.ID,
				IP:       nodeIP,
				VXLANIP:  nodeIP,
				Status:   node.Status.State.String(),
				Hostname: node.Description.Hostname,
			})
		}
	}
	return nodes, nil
}

// AutoNodeDiscovery automatically chooses best discovery method.
type AutoNodeDiscovery struct {
	controlSocket string
	remoteAddr    string
	localHostname string
	clusterNodes  []string
}

// NewAutoNodeDiscovery creates automatic node discovery.
func NewAutoNodeDiscovery(controlSocket, remoteAddr, localHostname string, clusterNodes []string) *AutoNodeDiscovery {
	return &AutoNodeDiscovery{
		controlSocket: controlSocket,
		remoteAddr:    remoteAddr,
		localHostname: localHostname,
		clusterNodes:  clusterNodes,
	}
}

// GetNodes discovers peers using best available method.
func (d *AutoNodeDiscovery) GetNodes() ([]types.NodeInfo, error) {
	// Workers use remoteAddr, managers use controlSocket
	socket := ""
	if d.controlSocket != "" && d.socketExists(d.controlSocket) {
		socket = d.controlSocket
	}

	// Try SwarmKit API (manager socket or remote API)
	if socket != "" || d.remoteAddr != "" {
		swarmDiscovery, _ := NewSwarmKitNodeDiscovery(socket, d.remoteAddr, "", d.localHostname)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if swarmDiscovery.Connect(ctx) == nil {
			nodes, err := swarmDiscovery.GetNodes()
			swarmDiscovery.Close()
			if err == nil && len(nodes) > 0 {
				return nodes, nil
			}
		}
	}

	// Fallback: hostname DNS resolution
	if len(d.clusterNodes) > 0 {
		return NewHostnameNodeDiscovery(d.localHostname, d.clusterNodes).GetNodes()
	}

	// Fallback: scan hostname patterns
	return d.scanForPeers()
}

// socketExists checks if Unix socket exists.
func (d *AutoNodeDiscovery) socketExists(socket string) bool {
	conn, err := net.DialTimeout("unix", socket, 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// scanForPeers scans for peers using hostname patterns.
func (d *AutoNodeDiscovery) scanForPeers() ([]types.NodeInfo, error) {
	nodes := []types.NodeInfo{}
	hostname := d.localHostname

	// Pattern: swarm-worker-N → scan other workers
	parts := strings.Split(hostname, "-")
	if len(parts) >= 3 && parts[len(parts)-2] == "worker" {
		for i := 1; i <= 10; i++ {
			peerHostname := fmt.Sprintf("%s-worker-%d", parts[0], i)
			if peerHostname == hostname {
				continue
			}
			ips, err := net.LookupHost(peerHostname)
			if err == nil && len(ips) > 0 {
				nodes = append(nodes, types.NodeInfo{
					ID:       peerHostname,
					IP:       ips[0],
					VXLANIP:  ips[0],
					Status:   "ready",
					Hostname: peerHostname,
				})
			}
		}
	}

	// Pattern: swarm-manager → scan workers
	if strings.Contains(hostname, "manager") {
		for i := 1; i <= 10; i++ {
			peerHostname := fmt.Sprintf("swarm-worker-%d", i)
			ips, err := net.LookupHost(peerHostname)
			if err == nil && len(ips) > 0 {
				nodes = append(nodes, types.NodeInfo{
					ID:       peerHostname,
					IP:       ips[0],
					VXLANIP:  ips[0],
					Status:   "ready",
					Hostname: peerHostname,
				})
			}
		}
	}

	return nodes, nil
}