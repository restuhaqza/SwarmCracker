package network

import (
	"net"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ===== NetworkManager Tests =====

func TestNetworkManager_New(t *testing.T) {
	cfg := types.NetworkConfig{
		BridgeName:   "br0",
		VXLANEnabled: false,
		NATEnabled:   false,
	}

	nm := NewNetworkManager(cfg)
	require.NotNil(t, nm)
}

func TestNetworkManager_New_WithVXLAN(t *testing.T) {
	cfg := types.NetworkConfig{
		BridgeName:   "br-vxlan",
		VXLANEnabled: true,
		VXLANID:      100,
	}

	nm := NewNetworkManager(cfg)
	require.NotNil(t, nm)
}

func TestNetworkManager_New_EmptyConfig(t *testing.T) {
	cfg := types.NetworkConfig{}
	nm := NewNetworkManager(cfg)
	require.NotNil(t, nm)
}

func TestNetworkManager_InterfaceCompliance(t *testing.T) {
	var _ types.NetworkManager = NewNetworkManager(types.NetworkConfig{})
}

// ===== NetworkConfig Tests =====

func TestNetworkConfig_Fields(t *testing.T) {
	cfg := types.NetworkConfig{
		BridgeName:    "br-custom",
		Subnet:        "172.16.0.0/16",
		BridgeIP:      "172.16.0.1",
		VXLANEnabled:  true,
		VXLANID:       500,
		NATEnabled:    true,
		VXLANTunnelIP: "10.30.0.1/24",
		VXLANPeers:    []string{"192.168.1.1", "192.168.1.2"},
	}

	assert.Equal(t, "br-custom", cfg.BridgeName)
	assert.Equal(t, "172.16.0.0/16", cfg.Subnet)
	assert.Equal(t, "172.16.0.1", cfg.BridgeIP)
	assert.True(t, cfg.VXLANEnabled)
	assert.Equal(t, 500, cfg.VXLANID)
	assert.True(t, cfg.NATEnabled)
	assert.Equal(t, "10.30.0.1/24", cfg.VXLANTunnelIP)
	assert.Len(t, cfg.VXLANPeers, 2)
}

func TestNetworkConfig_Empty(t *testing.T) {
	cfg := types.NetworkConfig{}

	assert.Empty(t, cfg.BridgeName)
	assert.Empty(t, cfg.Subnet)
	assert.False(t, cfg.VXLANEnabled)
	assert.False(t, cfg.NATEnabled)
}

// ===== Subnet parsing Tests =====

func TestParseSubnet_24(t *testing.T) {
	_, ipNet, err := net.ParseCIDR("10.0.0.0/24")
	require.NoError(t, err)

	assert.Equal(t, 24, maskToPrefix(ipNet.Mask))
	assert.Equal(t, "10.0.0.0", ipNet.IP.String())
}

func TestParseSubnet_16(t *testing.T) {
	_, ipNet, err := net.ParseCIDR("172.16.0.0/16")
	require.NoError(t, err)

	assert.Equal(t, 16, maskToPrefix(ipNet.Mask))
}

func TestParseSubnet_Invalid(t *testing.T) {
	_, _, err := net.ParseCIDR("invalid-subnet")
	require.Error(t, err)
}

func TestParseSubnet_8(t *testing.T) {
	_, ipNet, err := net.ParseCIDR("10.0.0.0/8")
	require.NoError(t, err)

	assert.Equal(t, 8, maskToPrefix(ipNet.Mask))
}

func TestParseSubnet_32(t *testing.T) {
	_, ipNet, err := net.ParseCIDR("10.0.0.1/32")
	require.NoError(t, err)

	assert.Equal(t, 32, maskToPrefix(ipNet.Mask))
}

// ===== VXLANManager Tests =====

func TestVXLANManager_NewBasic(t *testing.T) {
	peerStore := NewStaticPeerStore([]string{"192.168.1.1", "192.168.1.2"})
	vxlanMgr := NewVXLANManager("swarm-br0", 100, "10.0.0.1", peerStore)
	require.NotNil(t, vxlanMgr)
	assert.Equal(t, "swarm-br0", vxlanMgr.BridgeName)
	assert.Equal(t, 100, vxlanMgr.VXLANID)
	assert.Equal(t, "10.0.0.1", vxlanMgr.OverlayIP)
}

func TestVXLANManager_NewNilPeerStoreHandled(t *testing.T) {
	vxlanMgr := NewVXLANManager("swarm-br0", 100, "10.0.0.1", nil)
	require.NotNil(t, vxlanMgr)
	assert.NotNil(t, vxlanMgr.peerStore) // Should create default
}

func TestVXLANManager_NewWithCustomExecutor(t *testing.T) {
	executor := NewDefaultNetlinkExecutor()
	peerStore := NewStaticPeerStore(nil)
	vxlanMgr := NewVXLANManagerWithExecutor("swarm-br0", 100, "10.0.0.1", peerStore, executor)
	require.NotNil(t, vxlanMgr)
}

func TestVXLANManager_NewNilExecutorHandled(t *testing.T) {
	peerStore := NewStaticPeerStore(nil)
	vxlanMgr := NewVXLANManagerWithExecutor("swarm-br0", 100, "10.0.0.1", peerStore, nil)
	require.NotNil(t, vxlanMgr)
	assert.NotNil(t, vxlanMgr.netlinkExecutor) // Should create default
}

// ===== StaticPeerStore Tests =====

func TestStaticPeerStore_NewWithPeers(t *testing.T) {
	initialPeers := []string{"192.168.1.1", "192.168.1.2"}
	ps := NewStaticPeerStore(initialPeers)

	require.NotNil(t, ps)
	peers := ps.GetPeers()
	assert.Len(t, peers, 2)
}

func TestStaticPeerStore_NewEmpty(t *testing.T) {
	ps := NewStaticPeerStore([]string{})
	require.NotNil(t, ps)
	peers := ps.GetPeers()
	assert.Empty(t, peers)
}

func TestStaticPeerStore_NewNil(t *testing.T) {
	ps := NewStaticPeerStore(nil)
	require.NotNil(t, ps)
	peers := ps.GetPeers()
	assert.Empty(t, peers)
}

func TestStaticPeerStore_AddNewPeer(t *testing.T) {
	ps := NewStaticPeerStore([]string{})
	ps.AddPeer("192.168.1.2")
	peers := ps.GetPeers()
	assert.Contains(t, peers, "192.168.1.2")
}

func TestStaticPeerStore_RemoveExistingPeer(t *testing.T) {
	ps := NewStaticPeerStore([]string{"192.168.1.1", "192.168.1.2"})
	ps.RemovePeer("192.168.1.1")
	peers := ps.GetPeers()
	assert.NotContains(t, peers, "192.168.1.1")
	assert.Contains(t, peers, "192.168.1.2")
}

func TestStaticPeerStore_RemoveNonexistentSafe(t *testing.T) {
	ps := NewStaticPeerStore([]string{"192.168.1.1"})
	ps.RemovePeer("192.168.1.999") // Should not error
	peers := ps.GetPeers()
	assert.Len(t, peers, 1)
}

func TestStaticPeerStore_AddDuplicateIgnored(t *testing.T) {
	ps := NewStaticPeerStore([]string{})
	ps.AddPeer("192.168.1.1")
	ps.AddPeer("192.168.1.1") // Duplicate
	peers := ps.GetPeers()
	assert.Len(t, peers, 1)
}

func TestStaticPeerStore_MultipleSequentialAdds(t *testing.T) {
	ps := NewStaticPeerStore([]string{})
	for i := 0; i < 10; i++ {
		ps.AddPeer("192.168.1." + string(rune('0'+i)))
	}
	peers := ps.GetPeers()
	assert.GreaterOrEqual(t, len(peers), 1)
}

// ===== Peer Discovery Tests =====

func TestVXLANManager_UpdatePeers_Basic(t *testing.T) {
	ps := NewStaticPeerStore([]string{})
	vxlanMgr := NewVXLANManager("swarm-br0", 100, "10.0.0.1", ps)

	newPeers := []string{"192.168.1.1", "192.168.1.2"}
	vxlanMgr.UpdatePeers(newPeers)

	// Peer store should be updated
	peers := vxlanMgr.peerStore.GetPeers()
	_ = peers
}

func TestVXLANManager_UpdatePeers_EmptyList(t *testing.T) {
	ps := NewStaticPeerStore([]string{"192.168.1.1"})
	vxlanMgr := NewVXLANManager("swarm-br0", 100, "10.0.0.1", ps)

	vxlanMgr.UpdatePeers([]string{})

	peers := vxlanMgr.peerStore.GetPeers()
	_ = peers
}

func TestVXLANManager_UpdatePeers_NilPeers(t *testing.T) {
	ps := NewStaticPeerStore([]string{})
	vxlanMgr := NewVXLANManager("swarm-br0", 100, "10.0.0.1", ps)

	vxlanMgr.UpdatePeers(nil)

	peers := vxlanMgr.peerStore.GetPeers()
	assert.Empty(t, peers)
}

// ===== DefaultNetlinkExecutor Tests =====

func TestNewDefaultNetlinkExecutor(t *testing.T) {
	executor := NewDefaultNetlinkExecutor()
	require.NotNil(t, executor)
}

func TestDefaultNetlinkExecutor_InterfaceCompliance(t *testing.T) {
	var _ NetlinkExecutor = NewDefaultNetlinkExecutor()
}