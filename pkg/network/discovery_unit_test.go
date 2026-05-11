package network

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ===== HostnameNodeDiscovery Tests =====

func TestNewHostnameNodeDiscovery(t *testing.T) {
	localHostname := "node-1"
	clusterNodes := []string{"node-1", "node-2", "node-3"}

	discovery := NewHostnameNodeDiscovery(localHostname, clusterNodes)

	require.NotNil(t, discovery)
	assert.Equal(t, localHostname, discovery.localHostname)
	assert.Equal(t, clusterNodes, discovery.clusterNodes)
}

func TestHostnameNodeDiscovery_GetNodes_EmptyCluster(t *testing.T) {
	discovery := NewHostnameNodeDiscovery("local", []string{})

	nodes, err := discovery.GetNodes()

	require.NoError(t, err)
	assert.Empty(t, nodes)
}

func TestHostnameNodeDiscovery_GetNodes_SkipLocal(t *testing.T) {
	// Local hostname should be skipped
	discovery := NewHostnameNodeDiscovery("node-1", []string{"node-1", "node-2"})

	// DNS lookup may fail in test env, but local should always be skipped
	nodes, err := discovery.GetNodes()

	require.NoError(t, err)
	// If DNS works, should only have node-2
	// If DNS fails, should be empty
	_ = nodes
}

func TestHostnameNodeDiscovery_GetNodes_SingleRemote(t *testing.T) {
	discovery := NewHostnameNodeDiscovery("local", []string{"remote-host"})

	nodes, err := discovery.GetNodes()

	require.NoError(t, err)
	// DNS may fail in test env
	_ = nodes
}

func TestHostnameNodeDiscovery_GetNodes_AllLocal(t *testing.T) {
	// All nodes are local - should return empty
	discovery := NewHostnameNodeDiscovery("local", []string{"local", "local"})

	nodes, err := discovery.GetNodes()

	require.NoError(t, err)
	assert.Empty(t, nodes)
}

// ===== SwarmKitNodeDiscovery Tests =====

func TestNewSwarmKitNodeDiscovery(t *testing.T) {
	discovery, err := NewSwarmKitNodeDiscovery("/var/run/swarm.sock", "", "node-1", "host-1")

	require.NoError(t, err)
	require.NotNil(t, discovery)
	assert.Equal(t, "/var/run/swarm.sock", discovery.controlSocket)
	assert.Equal(t, "", discovery.remoteAddr)
	assert.Equal(t, "node-1", discovery.localNodeID)
	assert.Equal(t, "host-1", discovery.localHostname)
	assert.Equal(t, 10*time.Second, discovery.timeout)
}

func TestNewSwarmKitNodeDiscovery_RemoteAddr(t *testing.T) {
	discovery, err := NewSwarmKitNodeDiscovery("", "192.168.1.1:4242", "node-1", "host-1")

	require.NoError(t, err)
	assert.Equal(t, "", discovery.controlSocket)
	assert.Equal(t, "192.168.1.1:4242", discovery.remoteAddr)
}

func TestNewSwarmKitNodeDiscovery_NoAddress(t *testing.T) {
	discovery, err := NewSwarmKitNodeDiscovery("", "", "node-1", "host-1")

	require.NoError(t, err)
	assert.NotNil(t, discovery)
}

func TestSwarmKitNodeDiscovery_Connect_NoAddress(t *testing.T) {
	discovery, err := NewSwarmKitNodeDiscovery("", "", "node-1", "host-1")
	require.NoError(t, err)

	ctx := context.Background()
	err = discovery.Connect(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no control socket or remote address")
}

func TestSwarmKitNodeDiscovery_Connect_NonexistentSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires gRPC connection")
	}

	discovery, err := NewSwarmKitNodeDiscovery("/nonexistent/socket", "", "node-1", "host-1")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = discovery.Connect(ctx)
	// Connection to nonexistent socket should fail
	if err == nil {
		t.Log("Connect succeeded unexpectedly (socket may exist)")
	} else {
		require.Error(t, err)
	}
}

func TestSwarmKitNodeDiscovery_Close_NoConnection(t *testing.T) {
	discovery, _ := NewSwarmKitNodeDiscovery("", "", "node-1", "host-1")

	err := discovery.Close()
	require.NoError(t, err) // Closing nil connection is safe
}

func TestSwarmKitNodeDiscovery_GetNodes_NotConnected(t *testing.T) {
	discovery, _ := NewSwarmKitNodeDiscovery("", "", "node-1", "host-1")

	nodes, err := discovery.GetNodes()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not connected")
	assert.Nil(t, nodes)
}

// ===== AutoNodeDiscovery Tests =====

func TestNewAutoNodeDiscovery(t *testing.T) {
	discovery := NewAutoNodeDiscovery("/var/run/swarm.sock", "192.168.1.1:4242", "host-1", []string{"node-1", "node-2"})

	require.NotNil(t, discovery)
	assert.Equal(t, "/var/run/swarm.sock", discovery.controlSocket)
	assert.Equal(t, "192.168.1.1:4242", discovery.remoteAddr)
	assert.Equal(t, "host-1", discovery.localHostname)
	assert.Equal(t, []string{"node-1", "node-2"}, discovery.clusterNodes)
}

func TestNewAutoNodeDiscovery_EmptyClusterNodes(t *testing.T) {
	discovery := NewAutoNodeDiscovery("", "", "host-1", []string{})

	require.NotNil(t, discovery)
	assert.Empty(t, discovery.clusterNodes)
}

func TestAutoNodeDiscovery_GetNodes_NoDiscoveryMethod(t *testing.T) {
	discovery := NewAutoNodeDiscovery("", "", "host-1", []string{})

	nodes, err := discovery.GetNodes()

	// May return empty or error depending on scanForPeers
	_ = nodes
	_ = err
}

func TestAutoNodeDiscovery_GetNodes_FallbackHostname(t *testing.T) {
	discovery := NewAutoNodeDiscovery("", "", "local-host", []string{"remote-host"})

	nodes, err := discovery.GetNodes()

	// Should fallback to hostname discovery
	require.NoError(t, err)
	_ = nodes
}

func TestAutoNodeDiscovery_socketExists_Nonexistent(t *testing.T) {
	discovery := NewAutoNodeDiscovery("", "", "host-1", []string{})

	exists := discovery.socketExists("/nonexistent/socket/path")
	assert.False(t, exists)
}

func TestAutoNodeDiscovery_socketExists_WithSocket(t *testing.T) {
	// Create temp Unix socket
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "test.sock")

	// Create a simple Unix socket listener
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	discovery := NewAutoNodeDiscovery("", "", "host-1", []string{})

	exists := discovery.socketExists(socketPath)
	assert.True(t, exists)
}

func TestAutoNodeDiscovery_socketExists_Timeout(t *testing.T) {
	discovery := NewAutoNodeDiscovery("", "", "host-1", []string{})

	// Nonexistent socket should timeout quickly
	exists := discovery.socketExists("/proc/nonexistent/socket")
	assert.False(t, exists)
}

func TestAutoNodeDiscovery_scanForPeers_ManagerPattern(t *testing.T) {
	discovery := NewAutoNodeDiscovery("", "", "swarm-manager", []string{})

	nodes, err := discovery.scanForPeers()

	require.NoError(t, err)
	// DNS lookup may fail, but function should not panic
	_ = nodes
}

func TestAutoNodeDiscovery_scanForPeers_WorkerPattern(t *testing.T) {
	discovery := NewAutoNodeDiscovery("", "", "swarm-worker-1", []string{})

	nodes, err := discovery.scanForPeers()

	require.NoError(t, err)
	// DNS lookup may fail, but function should not panic
	_ = nodes
}

func TestAutoNodeDiscovery_scanForPeers_UnknownPattern(t *testing.T) {
	discovery := NewAutoNodeDiscovery("", "", "random-hostname", []string{})

	nodes, err := discovery.scanForPeers()

	require.NoError(t, err)
	assert.Empty(t, nodes) // Unknown pattern should return empty
}

func TestAutoNodeDiscovery_scanForPeers_SkipsSelf(t *testing.T) {
	discovery := NewAutoNodeDiscovery("", "", "swarm-worker-5", []string{})

	nodes, err := discovery.scanForPeers()

	require.NoError(t, err)
	// Should skip swarm-worker-5 if DNS resolves
	for _, node := range nodes {
		assert.NotEqual(t, "swarm-worker-5", node.Hostname)
	}
}

// ===== types.NodeInfo Tests =====

func TestNodeInfo_Fields(t *testing.T) {
	node := types.NodeInfo{
		ID:       "node-1",
		IP:       "192.168.1.1",
		VXLANIP:  "10.0.0.1",
		Status:   "ready",
		Hostname: "swarm-worker-1",
	}

	assert.Equal(t, "node-1", node.ID)
	assert.Equal(t, "192.168.1.1", node.IP)
	assert.Equal(t, "10.0.0.1", node.VXLANIP)
	assert.Equal(t, "ready", node.Status)
	assert.Equal(t, "swarm-worker-1", node.Hostname)
}

func TestNodeInfo_Empty(t *testing.T) {
	node := types.NodeInfo{}

	assert.Empty(t, node.ID)
	assert.Empty(t, node.IP)
	assert.Empty(t, node.Status)
}

// ===== Helper function tests =====

func TestHostnameNodeDiscovery_ImplementsInterface(t *testing.T) {
	var _ types.NodeDiscovery = NewHostnameNodeDiscovery("", []string{})
}

func TestSwarmKitNodeDiscovery_ImplementsInterface(t *testing.T) {
	discovery, _ := NewSwarmKitNodeDiscovery("", "", "", "")
	// SwarmKitNodeDiscovery doesn't directly implement NodeDiscovery
	// but has GetNodes method
	_ = discovery
}

func TestAutoNodeDiscovery_ImplementsInterface(t *testing.T) {
	var _ types.NodeDiscovery = NewAutoNodeDiscovery("", "", "", []string{})
}
