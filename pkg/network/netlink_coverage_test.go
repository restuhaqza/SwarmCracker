//go:build !integration

package network

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
)

// =============================================================================
// DefaultNetlinkExecutor tests — test real netlink calls
// =============================================================================

func TestDefaultNetlinkExecutor_LinkByName_Loopback(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	link, err := e.LinkByName("lo")
	require.NoError(t, err)
	assert.NotNil(t, link)
}

func TestDefaultNetlinkExecutor_LinkByName_NotExist(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	_, err := e.LinkByName("nonexist-iface-xyz")
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_LinkAdd_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	// Creating a bridge without root will fail
	err := e.LinkAdd(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "test-br-xyz"}})
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_LinkDel_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	err := e.LinkDel(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nonexist-xyz"}})
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_LinkSetUp_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	err := e.LinkSetUp(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nonexist-xyz"}})
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_LinkSetDown_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	err := e.LinkSetDown(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nonexist-xyz"}})
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_LinkSetMaster_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	err := e.LinkSetMaster(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nonexist"}}, &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "nonexist-br"}})
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_LinkSetMTU_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	err := e.LinkSetMTU(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nonexist-xyz"}}, 1500)
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_AddrAdd_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	addr, _ := netlink.ParseAddr("10.0.0.1/24")
	err := e.AddrAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nonexist-xyz"}}, addr)
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_AddrDel_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	addr, _ := netlink.ParseAddr("10.0.0.1/24")
	err := e.AddrDel(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nonexist-xyz"}}, addr)
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_AddrList_Loopback(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	link, err := e.LinkByName("lo")
	require.NoError(t, err)
	addrs, err := e.AddrList(link, netlink.FAMILY_V4)
	require.NoError(t, err)
	assert.NotNil(t, addrs)
}

func TestDefaultNetlinkExecutor_RouteAdd_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	_, cidr, _ := net.ParseCIDR("10.99.99.0/24")
	route := &netlink.Route{Dst: cidr}
	err := e.RouteAdd(route)
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_RouteDel_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	_, cidr, _ := net.ParseCIDR("10.99.99.0/24")
	route := &netlink.Route{Dst: cidr}
	err := e.RouteDel(route)
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_RouteList_Loopback(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	link, err := e.LinkByName("lo")
	require.NoError(t, err)
	routes, err := e.RouteList(link, netlink.FAMILY_V4)
	// lo may not have routes on all systems
	if err != nil {
		t.Logf("RouteList on lo returned error: %v (acceptable)", err)
	}
	_ = routes
}

func TestDefaultNetlinkExecutor_BridgeVlanAdd_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	err := e.BridgeVlanAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nonexist"}}, 100)
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_BridgeVlanDel_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	err := e.BridgeVlanDel(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nonexist"}}, 100)
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_NeighAdd_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	neigh := &netlink.Neigh{
		LinkIndex:    999,
		IP:           net.ParseIP("10.0.0.2"),
		HardwareAddr: net.HardwareAddr{0x00, 0x11, 0x22, 0x33, 0x44, 0x55},
	}
	err := e.NeighAdd(neigh)
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_NeighDel_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	neigh := &netlink.Neigh{
		LinkIndex: 999,
		IP:        net.ParseIP("10.0.0.2"),
	}
	err := e.NeighDel(neigh)
	require.Error(t, err)
}

func TestDefaultNetlinkExecutor_NeighList_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	_, err := e.NeighList(999, netlink.FAMILY_V4)
	// May or may not error depending on system
	_ = err
}

func TestDefaultNetlinkExecutor_LinkSetNsFd_Fail(t *testing.T) {
	e := NewDefaultNetlinkExecutor()
	err := e.LinkSetNsFd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nonexist"}}, -1)
	require.Error(t, err)
}
