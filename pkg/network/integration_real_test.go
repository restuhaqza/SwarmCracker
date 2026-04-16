//go:build linux

package network

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/network/testhelpers"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
)

// =============================================================================
// Tier 2: DefaultNetlinkExecutor — Real netlink operations (requires root)
// =============================================================================

// TestIntegration_Netlink_BasicOperations tests real netlink operations
// with dummy interfaces (no namespace needed, just root).
func TestIntegration_Netlink_BasicOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	testhelpers.RequireRoot(t)

	dummyName := testhelpers.RandomName("test-dummy")
	bridgeName := testhelpers.RandomName("test-br")

	exec := NewDefaultNetlinkExecutor()

	// --- Cleanup on exit ---
	t.Cleanup(func() {
		testhelpers.RunCombined(t, "ip", "link", "delete", dummyName)
		testhelpers.RunCombined(t, "ip", "link", "delete", bridgeName)
	})

	// --- LinkAdd + LinkByName ---
	t.Run("LinkAdd_LinkByName", func(t *testing.T) {
		dummy := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: dummyName}}
		err := exec.LinkAdd(dummy)
		require.NoError(t, err)

		link, err := exec.LinkByName(dummyName)
		require.NoError(t, err)
		assert.Equal(t, dummyName, link.Attrs().Name)
	})

	// --- LinkSetUp + LinkSetDown ---
	t.Run("LinkSetUp_LinkSetDown", func(t *testing.T) {
		link, err := exec.LinkByName(dummyName)
		require.NoError(t, err)

		err = exec.LinkSetUp(link)
		require.NoError(t, err)

		err = exec.LinkSetDown(link)
		require.NoError(t, err)
	})

	// --- LinkSetMTU ---
	t.Run("LinkSetMTU", func(t *testing.T) {
		link, err := exec.LinkByName(dummyName)
		require.NoError(t, err)

		err = exec.LinkSetMTU(link, 1400)
		require.NoError(t, err)

		// Verify
		link, err = exec.LinkByName(dummyName)
		require.NoError(t, err)
		assert.Equal(t, 1400, link.Attrs().MTU)
	})

	// --- AddrAdd + AddrList + AddrDel ---
	t.Run("AddrAdd_AddrList_AddrDel", func(t *testing.T) {
		link, err := exec.LinkByName(dummyName)
		require.NoError(t, err)

		// Bring up first
		require.NoError(t, exec.LinkSetUp(link))

		ipNet := &net.IPNet{IP: net.ParseIP("10.99.0.2"), Mask: net.CIDRMask(24, 32)}
		addr := &netlink.Addr{IPNet: ipNet}

		// Add address
		err = exec.AddrAdd(link, addr)
		require.NoError(t, err)

		// List addresses
		addrs, err := exec.AddrList(link, netlink.FAMILY_V4)
		require.NoError(t, err)
		assert.True(t, len(addrs) > 0, "expected at least one address")

		// Delete address
		err = exec.AddrDel(link, addr)
		require.NoError(t, err)
	})

	// --- LinkSetMaster (bridge) ---
	t.Run("LinkSetMaster", func(t *testing.T) {
		// Create bridge
		bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: bridgeName}}
		require.NoError(t, exec.LinkAdd(bridge))
		require.NoError(t, exec.LinkSetUp(bridge))

		link, err := exec.LinkByName(dummyName)
		require.NoError(t, err)
		require.NoError(t, exec.LinkSetUp(link))

		bridgeLink, err := exec.LinkByName(bridgeName)
		require.NoError(t, err)

		err = exec.LinkSetMaster(link, bridgeLink)
		require.NoError(t, err)
	})

	// --- RouteAdd + RouteList + RouteDel ---
	t.Run("RouteAdd_RouteList_RouteDel", func(t *testing.T) {
		link, err := exec.LinkByName(dummyName)
		require.NoError(t, err)

		// Add IP to dummy so route gateway is reachable
		require.NoError(t, exec.AddrAdd(link, &netlink.Addr{IPNet: &net.IPNet{IP: net.ParseIP("10.99.0.2"), Mask: net.CIDRMask(24, 32)}}))

		_, dst, _ := net.ParseCIDR("10.99.1.0/24")
		route := &netlink.Route{
			Dst:       dst,
			Gw:        net.ParseIP("10.99.0.1"),
			LinkIndex: link.Attrs().Index,
		}

		err = exec.RouteAdd(route)
		require.NoError(t, err)

		// Verify via RouteList
		routes, err := exec.RouteList(link, netlink.FAMILY_V4)
		require.NoError(t, err)
		found := false
		for _, r := range routes {
			if r.Dst != nil && r.Dst.String() == "10.99.1.0/24" {
				found = true
				break
			}
		}
		assert.True(t, found, "route should be in list")

		// Clean up route
		err = exec.RouteDel(route)
		require.NoError(t, err)
	})

	// --- NeighAdd + NeighList + NeighDel (FDB) ---
	t.Run("NeighAdd_NeighList_NeighDel", func(t *testing.T) {
		link, err := exec.LinkByName(dummyName)
		require.NoError(t, err)

		neigh := &netlink.Neigh{
			LinkIndex:    link.Attrs().Index,
			Family:       netlink.FAMILY_V4,
			IP:           net.ParseIP("10.99.0.5"),
			State:        netlink.NUD_PERMANENT,
			HardwareAddr: []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55},
		}

		err = exec.NeighAdd(neigh)
		require.NoError(t, err)

		// Verify via NeighList
		neighs, err := exec.NeighList(link.Attrs().Index, netlink.FAMILY_V4)
		require.NoError(t, err)
		found := false
		for _, n := range neighs {
			if n.IP.Equal(net.ParseIP("10.99.0.5")) {
				found = true
				break
			}
		}
		assert.True(t, found, "neighbor should be in list")

		// Clean up
		err = exec.NeighDel(neigh)
		require.NoError(t, err)
	})

	// --- BridgeVlanAdd + BridgeVlanDel ---
	t.Run("BridgeVlanAdd_BridgeVlanDel", func(t *testing.T) {
		link, err := exec.LinkByName(dummyName)
		require.NoError(t, err)

		err = exec.BridgeVlanAdd(link, 100)
		require.NoError(t, err)

		err = exec.BridgeVlanDel(link, 100)
		require.NoError(t, err)
	})

	// --- LinkDel ---
	t.Run("LinkDel", func(t *testing.T) {
		// Create a temporary dummy to delete
		tmpName := testhelpers.RandomName("tmp-del")
		tmp := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: tmpName}}
		require.NoError(t, exec.LinkAdd(tmp))

		link, err := exec.LinkByName(tmpName)
		require.NoError(t, err)

		err = exec.LinkDel(link)
		require.NoError(t, err)

		// Verify it's gone
		_, err = exec.LinkByName(tmpName)
		assert.Error(t, err, "link should be deleted")
	})
}

// =============================================================================
// Tier 1: NetworkManager — Bridge, NAT, TAP (requires root)
// =============================================================================

// TestIntegration_NetworkManager_EnsureBridge tests bridge creation with real system.
func TestIntegration_NetworkManager_EnsureBridge(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	testhelpers.RequireRoot(t)

	bridgeName := testhelpers.RandomName("test-br")
	t.Cleanup(func() {
		testhelpers.CleanupLink(t, bridgeName)
	})

	config := defaultNetworkConfig()
	config.BridgeName = bridgeName

	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.ensureBridge(t.Context())
	require.NoError(t, err)

	// Verify bridge exists
	assert.True(t, testhelpers.BridgeExists(t, bridgeName))
	assert.True(t, nm.bridges[bridgeName])
}

// TestIntegration_NetworkManager_SetupBridgeIP tests bridge IP assignment.
func TestIntegration_NetworkManager_SetupBridgeIP(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	testhelpers.RequireRoot(t)

	bridgeName := testhelpers.RandomName("test-br")
	t.Cleanup(func() {
		testhelpers.CleanupLink(t, bridgeName)
	})

	// Create bridge first
	output, err := testhelpers.RunCombined(t, "ip", "link", "add", bridgeName, "type", "bridge")
	require.NoError(t, err, "bridge create: %s", string(output))
	testhelpers.RunCombined(t, "ip", "link", "set", bridgeName, "up")

	config := defaultNetworkConfig()
	config.BridgeName = bridgeName
	config.BridgeIP = "10.99.0.1/24"

	nm := NewNetworkManager(config).(*NetworkManager)

	err = nm.setupBridgeIP(t.Context())
	assert.NoError(t, err)

	// Verify IP is assigned
	assert.True(t, testhelpers.LinkHasIP(t, bridgeName, "10.99.0.1"))
}

// TestIntegration_NetworkManager_SetupNAT tests NAT configuration.
func TestIntegration_NetworkManager_SetupNAT(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	testhelpers.RequireRoot(t)

	bridgeName := testhelpers.RandomName("test-br")
	subnet := "10.99.5.0/24"

	t.Cleanup(func() {
		// Clean up iptables rules
		testhelpers.CleanupIptablesRule(t, "nat", "POSTROUTING", "-s", subnet, "-j", "MASQUERADE")
		testhelpers.CleanupIptablesRule(t, "filter", "FORWARD", "-i", bridgeName, "-j", "ACCEPT")
		testhelpers.CleanupIptablesRule(t, "filter", "FORWARD", "-o", bridgeName, "-j", "ACCEPT")
		testhelpers.CleanupLink(t, bridgeName)
	})

	// Create bridge
	testhelpers.RunCombined(t, "ip", "link", "add", bridgeName, "type", "bridge")
	testhelpers.RunCombined(t, "ip", "link", "set", bridgeName, "up")

	config := defaultNetworkConfig()
	config.BridgeName = bridgeName
	config.Subnet = subnet

	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupNAT(t.Context())
	assert.NoError(t, err)

	// Verify iptables rules
	assert.True(t, testhelpers.IptablesRuleExists(t, "nat", "POSTROUTING", "-s", subnet, "-j", "MASQUERADE"))
	assert.True(t, testhelpers.IptablesRuleExists(t, "filter", "FORWARD", "-i", bridgeName, "-j", "ACCEPT"))
	assert.True(t, testhelpers.IptablesRuleExists(t, "filter", "FORWARD", "-o", bridgeName, "-j", "ACCEPT"))
}

// TestIntegration_NetworkManager_CreateTapDevice tests TAP device creation.
func TestIntegration_NetworkManager_CreateTapDevice(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	testhelpers.RequireRoot(t)

	bridgeName := testhelpers.RandomName("test-br")
	t.Cleanup(func() {
		// Clean up any tap devices
		output, _ := testhelpers.RunOutput(t, "ip", "link", "show", "type", "tap")
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 1 {
				testhelpers.CleanupLink(t, fields[1])
			}
		}
		testhelpers.CleanupLink(t, bridgeName)
	})

	// Create bridge
	testhelpers.RunCombined(t, "ip", "link", "add", bridgeName, "type", "bridge")
	testhelpers.RunCombined(t, "ip", "link", "set", bridgeName, "up")

	config := defaultNetworkConfig()
	config.BridgeName = bridgeName
	config.Subnet = "10.99.6.0/24"
	config.BridgeIP = "10.99.6.1/24"
	config.IPMode = "static"

	nm := NewNetworkManager(config).(*NetworkManager)

	network := types.NetworkAttachment{
		Network: types.Network{
			ID: "test-net-id",
			Spec: types.NetworkSpec{
				Name:   "test",
				Driver: "bridge",
			},
		},
	}

	tap, err := nm.createTapDevice(t.Context(), network, 0, "vm-test-1")
	require.NoError(t, err)

	assert.NotEmpty(t, tap.Name)
	assert.Equal(t, bridgeName, tap.Bridge)
	assert.Contains(t, tap.IP, "10.99.6.")
	assert.Equal(t, "255.255.255.0", tap.Netmask)

	// Verify TAP device exists on system
	assert.True(t, testhelpers.LinkExists(t, tap.Name))

	// Verify TAP is master of bridge
	output, err := testhelpers.RunOutput(t, "ip", "link", "show", tap.Name)
	require.NoError(t, err)
	assert.Contains(t, string(output), fmt.Sprintf("master %s", bridgeName))
}

// TestIntegration_NetworkManager_RemoveTapDevice tests TAP device removal.
func TestIntegration_NetworkManager_RemoveTapDevice(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	testhelpers.RequireRoot(t)

	bridgeName := testhelpers.RandomName("test-br")
	t.Cleanup(func() {
		testhelpers.CleanupLink(t, bridgeName)
	})

	// Create bridge
	testhelpers.RunCombined(t, "ip", "link", "add", bridgeName, "type", "bridge")
	testhelpers.RunCombined(t, "ip", "link", "set", bridgeName, "up")

	// Create TAP device manually
	tapName := testhelpers.RandomName("tap-rm")
	testhelpers.RunCombined(t, "ip", "tuntap", "add", tapName, "mode", "tap")
	testhelpers.RunCombined(t, "ip", "link", "set", tapName, "up")

	nm := NewNetworkManager(defaultNetworkConfig()).(*NetworkManager)

	tap := &TapDevice{Name: tapName, Bridge: bridgeName}
	err := nm.removeTapDevice(tap)
	require.NoError(t, err)

	// Verify TAP is gone
	assert.False(t, testhelpers.LinkExists(t, tapName))
}

// TestIntegration_NetworkManager_GetPhysicalInterface tests interface discovery.
func TestIntegration_NetworkManager_GetPhysicalInterface(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	testhelpers.RequireRoot(t)

	// We need a real default route for this test.
	// Check if we have one (might not in CI)
	output, err := testhelpers.RunOutput(t, "ip", "route", "show", "default")
	if err != nil || len(strings.TrimSpace(string(output))) == 0 {
		t.Skip("No default route available")
	}

	nm := NewNetworkManager(defaultNetworkConfig()).(*NetworkManager)

	iface, ip, err := nm.getPhysicalInterface()
	require.NoError(t, err)
	assert.NotEmpty(t, iface)
	assert.NotEmpty(t, ip)
	t.Logf("Physical interface: %s, IP: %s", iface, ip)
}

// TestIntegration_NetworkManager_FullPrepareCleanup tests end-to-end prepare and cleanup.
func TestIntegration_NetworkManager_FullPrepareCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	testhelpers.RequireRoot(t)

	bridgeName := testhelpers.RandomName("test-br")
	t.Cleanup(func() {
		// Clean up iptables rules
		subnet := "10.99.7.0/24"
		testhelpers.CleanupIptablesRule(t, "nat", "POSTROUTING", "-s", subnet, "-j", "MASQUERADE")
		testhelpers.CleanupIptablesRule(t, "filter", "FORWARD", "-i", bridgeName, "-j", "ACCEPT")
		testhelpers.CleanupIptablesRule(t, "filter", "FORWARD", "-o", bridgeName, "-j", "ACCEPT")

		// Clean up TAP devices
		output, _ := testhelpers.RunOutput(t, "ip", "link", "show", "type", "tap")
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 1 {
				testhelpers.CleanupLink(t, fields[1])
			}
		}
		testhelpers.CleanupLink(t, bridgeName)

		// Kill dnsmasq for this bridge
		testhelpers.RunCombined(t, "pkill", "-f", fmt.Sprintf("dnsmasq.*%s", bridgeName))
	})

	config := defaultNetworkConfig()
	config.BridgeName = bridgeName
	config.Subnet = "10.99.7.0/24"
	config.BridgeIP = "10.99.7.1/24"
	config.IPMode = "static"
	config.NATEnabled = true

	nm := NewNetworkManager(config).(*NetworkManager)

	task := &types.Task{
		ID: "integration-vm-1",
		Spec: types.TaskSpec{
			Runtime: &types.Container{},
		},
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "net-1",
					Spec: types.NetworkSpec{
						Name:   "default",
						Driver: "bridge",
					},
				},
			},
		},
	}

	// Prepare
	err := nm.PrepareNetwork(t.Context(), task)
	require.NoError(t, err)

	// Verify bridge
	assert.True(t, testhelpers.BridgeExists(t, bridgeName))
	assert.True(t, testhelpers.LinkHasIP(t, bridgeName, "10.99.7.1"))

	// Verify TAP was created
	devices := nm.ListTapDevices()
	assert.NotEmpty(t, devices)

	// Verify IP was allocated
	tapIP, err := nm.GetTapIP("integration-vm-1")
	require.NoError(t, err)
	assert.Contains(t, tapIP, "10.99.7.")

	// Verify network attachment was updated
	assert.NotEmpty(t, task.Networks[0].Addresses)

	// Cleanup
	err = nm.CleanupNetwork(t.Context(), task)
	require.NoError(t, err)

	// Verify TAP was removed
	devices = nm.ListTapDevices()
	assert.Empty(t, devices)

	// Verify IP was released
	_, err = nm.GetTapIP("integration-vm-1")
	assert.Error(t, err)
}
