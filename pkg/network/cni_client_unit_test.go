package network

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ===== CNIConfig Tests =====

func TestCNIConfig_Defaults(t *testing.T) {
	cfg := CNIConfig{}
	client := NewCNIClient(cfg)

	require.NotNil(t, client)
	assert.Equal(t, "/opt/cni/bin", client.config.BinDir)
	assert.Equal(t, "/etc/cni/net.d", client.config.ConfDir)
	assert.Equal(t, "/var/lib/cni", client.config.CacheDir)
	assert.Equal(t, "swarmcracker", client.config.NetworkName)
}

func TestCNIConfig_Custom(t *testing.T) {
	cfg := CNIConfig{
		BinDir:      "/custom/bin",
		ConfDir:     "/custom/conf",
		CacheDir:    "/custom/cache",
		NetworkName: "custom-net",
	}

	client := NewCNIClient(cfg)

	require.NotNil(t, client)
	assert.Equal(t, "/custom/bin", client.config.BinDir)
	assert.Equal(t, "/custom/conf", client.config.ConfDir)
	assert.Equal(t, "/custom/cache", client.config.CacheDir)
	assert.Equal(t, "custom-net", client.config.NetworkName)
}

func TestCNIConfig_PartialDefaults(t *testing.T) {
	cfg := CNIConfig{
		BinDir: "/custom/bin",
		// Other fields empty - should use defaults
	}

	client := NewCNIClient(cfg)

	assert.Equal(t, "/custom/bin", client.config.BinDir)
	assert.Equal(t, "/etc/cni/net.d", client.config.ConfDir)   // Default
	assert.Equal(t, "/var/lib/cni", client.config.CacheDir)    // Default
	assert.Equal(t, "swarmcracker", client.config.NetworkName) // Default
}

// ===== CNIResult Tests =====

func TestCNIResult_Empty(t *testing.T) {
	result := &CNIResult{}

	assert.Empty(t, result.CNIVersion)
	assert.Empty(t, result.Interfaces)
	assert.Empty(t, result.IPs)
	assert.Empty(t, result.Routes)
}

func TestCNIResult_Full(t *testing.T) {
	ipNet := net.IPNet{
		IP:   net.ParseIP("10.0.0.2"),
		Mask: net.IPMask{255, 255, 255, 0},
	}

	destNet := net.IPNet{
		IP:   net.ParseIP("0.0.0.0"),
		Mask: net.IPMask{0, 0, 0, 0},
	}

	result := &CNIResult{
		CNIVersion: "1.0.0",
		Interfaces: []CNIInterface{
			{Name: "eth0", Mac: "00:11:22:33:44:55", Sandbox: "/proc/123/ns/net"},
		},
		IPs: []CNIIP{
			{Address: ipNet, Interface: 0},
		},
		Routes: []CNIRoute{
			{Dest: destNet, GW: net.ParseIP("10.0.0.1")},
		},
	}

	assert.Equal(t, "1.0.0", result.CNIVersion)
	assert.Len(t, result.Interfaces, 1)
	assert.Len(t, result.IPs, 1)
	assert.Len(t, result.Routes, 1)
	assert.Equal(t, "eth0", result.Interfaces[0].Name)
	assert.Equal(t, "00:11:22:33:44:55", result.Interfaces[0].Mac)
}

// ===== CNIInterface Tests =====

func TestCNIInterface_Fields(t *testing.T) {
	iface := CNIInterface{
		Name:    "tap-vm-1",
		Mac:     "aa:bb:cc:dd:ee:ff",
		Sandbox: "/var/run/netns/ns-123",
	}

	assert.Equal(t, "tap-vm-1", iface.Name)
	assert.Equal(t, "aa:bb:cc:dd:ee:ff", iface.Mac)
	assert.Equal(t, "/var/run/netns/ns-123", iface.Sandbox)
}

// ===== CNIIP Tests =====

func TestCNIIP_AddressParsing(t *testing.T) {
	ip, ipNet, err := net.ParseCIDR("10.0.0.2/24")
	require.NoError(t, err)

	cniIP := CNIIP{
		Address:   *ipNet,
		Interface: 0,
	}

	// ipNet.IP is the network address (10.0.0.0), original IP is 'ip'
	assert.Equal(t, "10.0.0.0", cniIP.Address.IP.String())
	assert.Equal(t, "10.0.0.2", ip.String()) // Original parsed IP
	prefix := maskToPrefix(cniIP.Address.Mask)
	assert.Equal(t, 24, prefix)
}

func TestCNIIP_InterfaceIndex(t *testing.T) {
	cniIP := CNIIP{Interface: 5}
	assert.Equal(t, 5, cniIP.Interface)
}

// ===== CNIRoute Tests =====

func TestCNIRoute_DefaultRoute(t *testing.T) {
	_, dest, _ := net.ParseCIDR("0.0.0.0/0")

	route := CNIRoute{
		Dest: *dest,
		GW:   net.ParseIP("10.0.0.1"),
	}

	assert.Equal(t, "0.0.0.0/0", route.Dest.String())
	assert.Equal(t, "10.0.0.1", route.GW.String())
}

func TestCNIRoute_SubnetRoute(t *testing.T) {
	_, dest, _ := net.ParseCIDR("172.16.0.0/16")

	route := CNIRoute{
		Dest: *dest,
		GW:   net.ParseIP("172.16.0.1"),
	}

	assert.Equal(t, "172.16.0.0/16", route.Dest.String())
}

// ===== CNIClient getNetworkConfig Tests (Integration - needs filesystem) =====

func TestCNIClient_getNetworkConfig_NonexistentDir(t *testing.T) {
	cfg := CNIConfig{
		ConfDir: "/nonexistent/dir",
	}

	client := NewCNIClient(cfg)
	_, err := client.getNetworkConfig("test-net")

	// Should return error when directory doesn't exist
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read CNI config dir")
}

func TestCNIClient_getNetworkConfig_DefaultConfigReturn(t *testing.T) {
	// When no matching config found, returns default
	cfg := CNIConfig{
		ConfDir: "/tmp",
	}
	client := NewCNIClient(cfg)

	// Use a unique network name unlikely to exist in /tmp
	config, err := client.getNetworkConfig("nonexistent-network-xyz")

	// Should return default config (no error)
	require.NoError(t, err)
	assert.Contains(t, config, "nonexistent-network-xyz")
	assert.Contains(t, config, "swarmcracker-cni")
}

func TestCNIClient_getNetworkConfig_EmptyNetworkName(t *testing.T) {
	cfg := CNIConfig{
		ConfDir: "/tmp",
	}
	client := NewCNIClient(cfg)

	config, err := client.getNetworkConfig("")

	require.NoError(t, err)
	assert.Contains(t, config, "swarmcracker-cni")
}

// ===== CNIClient AddNetwork/DelNetwork Tests (Skip in short mode) =====

func TestCNIClient_AddNetwork_NeedsCNI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires CNI plugin binary")
	}

	cfg := CNIConfig{}
	client := NewCNIClient(cfg)

	ctx := context.Background()
	_, err := client.AddNetwork(ctx, "container-123", "/proc/1/ns/net", "10.0.0.2/24", "swarmcracker")

	// Will fail without actual CNI plugin
	_ = err
}

func TestCNIClient_DelNetwork_NeedsCNI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires CNI plugin binary")
	}

	cfg := CNIConfig{}
	client := NewCNIClient(cfg)

	ctx := context.Background()
	err := client.DelNetwork(ctx, "container-123", "/proc/1/ns/net", "swarmcracker")

	// DEL should be tolerant of errors
	_ = err
}

func TestCNIClient_DelNetwork_MissingConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires exec")
	}

	cfg := CNIConfig{
		ConfDir: "/nonexistent",
		BinDir:  "/nonexistent",
	}
	client := NewCNIClient(cfg)

	ctx := context.Background()
	// Should use default config when config dir missing
	err := client.DelNetwork(ctx, "container-123", "/proc/1/ns/net", "missing-net")

	// DEL is tolerant, shouldn't return error
	_ = err
}

// ===== Net.IPNet Parsing Tests =====

func TestParseCIDR_Valid(t *testing.T) {
	ip, ipNet, err := net.ParseCIDR("192.168.1.2/24")
	require.NoError(t, err)

	assert.Equal(t, "192.168.1.2", ip.String())
	assert.Equal(t, "192.168.1.0", ipNet.IP.String())
	assert.Equal(t, 24, maskToPrefix(ipNet.Mask))
}

func TestParseCIDR_Valid32(t *testing.T) {
	ip, ipNet, err := net.ParseCIDR("10.0.0.1/32")
	require.NoError(t, err)

	assert.Equal(t, "10.0.0.1", ip.String())
	assert.Equal(t, 32, maskToPrefix(ipNet.Mask))
}

func TestParseCIDR_Invalid(t *testing.T) {
	_, _, err := net.ParseCIDR("invalid-cidr")
	require.Error(t, err)
}

func TestParseCIDR_IPv6(t *testing.T) {
	ip, ipNet, err := net.ParseCIDR("fd00::1/64")
	require.NoError(t, err)

	assert.NotNil(t, ip)
	prefix, _ := ipNet.Mask.Size()
	assert.Equal(t, 64, prefix)
}
