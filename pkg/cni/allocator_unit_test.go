package cni

import (
	"net"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCNINetworkAllocator_IsAllocated_Nil tests IsAllocated with nil network
func TestCNINetworkAllocator_IsAllocated_NilNetwork(t *testing.T) {
	allocator := &CNINetworkAllocator{
		allocatedNets: make(map[string]*AllocatedNetwork),
	}

	assert.False(t, allocator.IsAllocated(nil))
}

// TestCNINetworkAllocator_IsAllocated_NotAllocated tests unallocated network
func TestCNINetworkAllocator_IsAllocated_NotFound(t *testing.T) {
	allocator := &CNINetworkAllocator{
		allocatedNets: make(map[string]*AllocatedNetwork),
	}

	network := &api.Network{ID: "net-1"}
	assert.False(t, allocator.IsAllocated(network))
}

// TestCNINetworkAllocator_IsAllocated_Allocated tests allocated network
func TestCNINetworkAllocator_IsAllocated_Found(t *testing.T) {
	allocator := &CNINetworkAllocator{
		allocatedNets: map[string]*AllocatedNetwork{
			"net-1": &AllocatedNetwork{ID: "net-1"},
		},
	}

	network := &api.Network{ID: "net-1"}
	assert.True(t, allocator.IsAllocated(network))
}

// TestAllocatedNetwork_DriverState_Bridge tests bridge driver state
func TestAllocatedNetwork_DriverState_BridgeNetwork(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("10.0.1.0/24")
	net := &AllocatedNetwork{
		Name:       "test-bridge",
		Driver:     "bridge",
		Subnet:     subnet,
		Gateway:    net.ParseIP("10.0.1.1"),
		BridgeName: "br-test",
	}

	state := net.DriverState()
	require.NotNil(t, state)
}

// TestAllocatedNetwork_DriverState_VXLAN tests VXLAN driver state
func TestAllocatedNetwork_DriverState_VXLANNetwork(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("10.0.2.0/24")
	net := &AllocatedNetwork{
		Name:    "test-vxlan",
		Driver:  "vxlan",
		Subnet:  subnet,
		Gateway: net.ParseIP("10.0.2.1"),
		VXLANID: 100,
	}

	state := net.DriverState()
	require.NotNil(t, state)
}

// TestAllocatedNetwork_IPAMState tests IPAM state generation
func TestAllocatedNetwork_IPAMState_Basic(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("10.0.1.0/24")
	net := &AllocatedNetwork{
		Name:    "test-ipam",
		Subnet:  subnet,
		Gateway: net.ParseIP("10.0.1.1"),
	}

	state := net.IPAMState()
	require.NotNil(t, state)
	assert.Len(t, state.Configs, 1)
}

// TestIPPool tests IP pool struct
func TestIPPool_Basic(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("10.0.0.0/24")
	pool := &IPPool{
		Subnet:  subnet,
		Gateway: net.ParseIP("10.0.0.1"),
		UsedIPs: make(map[string]string),
	}

	assert.NotNil(t, pool.Subnet)
	assert.NotNil(t, pool.Gateway)
}

// TestCNINetworkConfig tests CNI config struct
func TestCNINetworkConfig_Basic(t *testing.T) {
	config := &CNINetworkConfig{
		CNIVersion: "1.0.0",
		Name:       "test-network",
		Type:       "bridge",
	}

	assert.Equal(t, "1.0.0", config.CNIVersion)
	assert.Equal(t, "test-network", config.Name)
	assert.Equal(t, "bridge", config.Type)
}

// TestCNIExecResult tests CNI exec result
func TestCNIExecResult_Basic(t *testing.T) {
	result := &CNIExecResult{
		Interfaces: []CNIInterface{
			{Name: "eth0", Sandbox: "containerID"},
		},
		IPs: []CNIIPConfig{
			{Address: "10.0.0.2/24", Gateway: "10.0.0.1"},
		},
	}

	assert.Len(t, result.Interfaces, 1)
	assert.Len(t, result.IPs, 1)
}

// TestCNIInterface tests interface struct
func TestCNIInterface_Basic(t *testing.T) {
	iface := CNIInterface{
		Name:    "eth0",
		MAC:     "00:11:22:33:44:55",
		Sandbox: "/var/run/netns/container",
	}

	assert.Equal(t, "eth0", iface.Name)
	assert.Equal(t, "00:11:22:33:44:55", iface.MAC)
}

// TestCNIIPConfig tests IP config struct
func TestCNIIPConfig_Basic(t *testing.T) {
	ip := CNIIPConfig{
		Address: "10.0.0.2/24",
		Gateway: "10.0.0.1",
	}

	assert.Equal(t, "10.0.0.2/24", ip.Address)
	assert.Equal(t, "10.0.0.1", ip.Gateway)
}

// TestCNIRoute tests route struct
func TestCNIRoute_Basic(t *testing.T) {
	route := CNIRoute{
		Destination: "0.0.0.0/0",
		Gateway:     "10.0.0.1",
	}

	assert.Equal(t, "0.0.0.0/0", route.Destination)
	assert.Equal(t, "10.0.0.1", route.Gateway)
}

// TestCNIDNS tests DNS struct
func TestCNIDNS_Basic(t *testing.T) {
	dns := CNIDNS{
		Nameservers: []string{"8.8.8.8"},
		Domain:      "example.com",
	}

	assert.Len(t, dns.Nameservers, 1)
	assert.Equal(t, "example.com", dns.Domain)
}

// TestCNIConfig_Defaults tests default config
func TestCNIConfig_DefaultValues(t *testing.T) {
	cfg := DefaultCNIConfig()

	assert.Equal(t, DefaultBridgeName, cfg.BridgeName)
	assert.Equal(t, DefaultSubnetPool, cfg.SubnetPool)
	assert.Equal(t, DefaultSubnetSize, cfg.SubnetSize)
	assert.Equal(t, "host-local", cfg.IPAMType)
	assert.True(t, cfg.EnableIPMasq)
}

// TestCNIConfig_Custom tests custom config
func TestCNIConfig_CustomValues(t *testing.T) {
	cfg := &CNIConfig{
		BridgeName:   "br-custom",
		SubnetPool:   "172.16.0.0/12",
		SubnetSize:   16,
		VXLANPort:    8472,
		IPAMType:     "dhcp",
		EnableIPMasq: false,
	}

	assert.Equal(t, "br-custom", cfg.BridgeName)
	assert.Equal(t, "172.16.0.0/12", cfg.SubnetPool)
	assert.Equal(t, 16, cfg.SubnetSize)
	assert.Equal(t, uint32(8472), cfg.VXLANPort)
	assert.Equal(t, "dhcp", cfg.IPAMType)
	assert.False(t, cfg.EnableIPMasq)
}

// TestNodeAttachment tests node attachment struct
func TestNodeAttachment_Basic(t *testing.T) {
	attach := &NodeAttachment{
		NodeID:     "node-1",
		NetworkID:  "net-1",
		IPAddress:  net.ParseIP("10.0.0.2"),
		MACAddress: "00:11:22:33:44:55",
	}

	assert.Equal(t, "node-1", attach.NodeID)
	assert.Equal(t, "net-1", attach.NetworkID)
	assert.NotNil(t, attach.IPAddress)
}

// TestServiceVIP tests service VIP struct
func TestServiceVIP_Basic(t *testing.T) {
	vip := &ServiceVIP{
		ServiceID: "service-1",
		NetworkID: "net-1",
		VIP:       net.ParseIP("10.0.0.100"),
	}

	assert.Equal(t, "service-1", vip.ServiceID)
	assert.Equal(t, "net-1", vip.NetworkID)
	assert.NotNil(t, vip.VIP)
}

// TestPublishedPort tests published port struct
func TestPublishedPort_Basic(t *testing.T) {
	port := PublishedPort{
		Port:          80,
		PublishedPort: 8080,
		Protocol:      "tcp",
		PublishMode:   "ingress",
	}

	assert.Equal(t, uint32(80), port.Port)
	assert.Equal(t, uint32(8080), port.PublishedPort)
	assert.Equal(t, "tcp", port.Protocol)
}

// TestConstants tests package constants
func TestConstants_Values(t *testing.T) {
	assert.Equal(t, "1.0.0", DefaultCNIVersion)
	assert.Equal(t, "cni0", DefaultBridgeName)
	assert.EqualValues(t, 4789, DefaultVXLANPort) // use EqualValues for type flexibility
	assert.Equal(t, "10.0.0.0/8", DefaultSubnetPool)
	assert.Equal(t, 24, DefaultSubnetSize)
	assert.Equal(t, "ingress", IngressNetworkName)
	assert.Equal(t, "/opt/cni/bin", DefaultPluginDir)
	assert.Equal(t, "/etc/cni/net.d", DefaultConfigDir)
}
