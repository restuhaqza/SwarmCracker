//go:build !integration

package network

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
)

// =============================================================================
// MockNetlinkExecutor comprehensive tests
// =============================================================================

func TestMockNetlinkExecutor_LinkByName(t *testing.T) {
	expectedLink := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "eth0", Index: 1}}
	mock := &MockNetlinkExecutor{
		LinkByNameFunc: func(name string) (netlink.Link, error) {
			if name == "eth0" {
				return expectedLink, nil
			}
			return nil, errors.New("not found")
		},
	}

	link, err := mock.LinkByName("eth0")
	require.NoError(t, err)
	assert.Equal(t, expectedLink, link)

	link, err = mock.LinkByName("nonexistent")
	require.Error(t, err)
	assert.Nil(t, link)
}

func TestMockNetlinkExecutor_LinkByName_Default(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	link, err := mock.LinkByName("test")
	assert.Nil(t, link)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_LinkAdd(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkAddFunc: func(link netlink.Link) error {
			return errors.New("permission denied")
		},
	}

	err := mock.LinkAdd(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestMockNetlinkExecutor_LinkAdd_Default(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	err := mock.LinkAdd(nil)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_LinkDel(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkDelFunc: func(link netlink.Link) error {
			return errors.New("device not found")
		},
	}

	err := mock.LinkDel(nil)
	require.Error(t, err)
}

func TestMockNetlinkExecutor_LinkDel_Default(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	err := mock.LinkDel(nil)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_LinkSetUp(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkSetUpFunc: func(link netlink.Link) error {
			return errors.New("link not found")
		},
	}

	err := mock.LinkSetUp(nil)
	require.Error(t, err)
}

func TestMockNetlinkExecutor_LinkSetUp_Default(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	err := mock.LinkSetUp(nil)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_LinkSetDown(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkSetDownFunc: func(link netlink.Link) error {
			return nil
		},
	}

	err := mock.LinkSetDown(nil)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_LinkSetMaster(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkSetMasterFunc: func(link, master netlink.Link) error {
			return errors.New("master not found")
		},
	}

	err := mock.LinkSetMaster(nil, nil)
	require.Error(t, err)
}

func TestMockNetlinkExecutor_LinkSetMaster_Default(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	err := mock.LinkSetMaster(nil, nil)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_LinkSetMTU(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkSetMTUFunc: func(link netlink.Link, mtu int) error {
			if mtu > 1500 {
				return errors.New("MTU too large")
			}
			return nil
		},
	}

	err := mock.LinkSetMTU(nil, 9000)
	require.Error(t, err)

	err = mock.LinkSetMTU(nil, 1500)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_LinkSetNsFd(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkSetNsFdFunc: func(link netlink.Link, fd int) error {
			return nil
		},
	}

	err := mock.LinkSetNsFd(nil, 0)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_AddrAdd(t *testing.T) {
	mock := &MockNetlinkExecutor{
		AddrAddFunc: func(link netlink.Link, addr *netlink.Addr) error {
			return errors.New("address already exists")
		},
	}

	err := mock.AddrAdd(nil, nil)
	require.Error(t, err)
}

func TestMockNetlinkExecutor_AddrAdd_Default(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	err := mock.AddrAdd(nil, nil)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_AddrDel(t *testing.T) {
	mock := &MockNetlinkExecutor{
		AddrDelFunc: func(link netlink.Link, addr *netlink.Addr) error {
			return nil
		},
	}

	err := mock.AddrDel(nil, nil)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_AddrList(t *testing.T) {
	expectedAddrs := []netlink.Addr{
		{IPNet: &net.IPNet{IP: net.ParseIP("10.0.0.1"), Mask: net.IPMask{255, 255, 255, 0}}},
		{IPNet: &net.IPNet{IP: net.ParseIP("10.0.0.2"), Mask: net.IPMask{255, 255, 255, 0}}},
	}

	mock := &MockNetlinkExecutor{
		AddrListFunc: func(link netlink.Link, family int) ([]netlink.Addr, error) {
			if family == netlink.FAMILY_V4 {
				return expectedAddrs, nil
			}
			return nil, errors.New("unsupported family")
		},
	}

	addrs, err := mock.AddrList(nil, netlink.FAMILY_V4)
	require.NoError(t, err)
	assert.Len(t, addrs, 2)

	addrs, err = mock.AddrList(nil, netlink.FAMILY_V6)
	require.Error(t, err)
	assert.Nil(t, addrs)
}

func TestMockNetlinkExecutor_AddrList_Default(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	addrs, err := mock.AddrList(nil, 0)
	assert.Nil(t, addrs)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_RouteAdd(t *testing.T) {
	mock := &MockNetlinkExecutor{
		RouteAddFunc: func(route *netlink.Route) error {
			return errors.New("route add failed")
		},
	}

	err := mock.RouteAdd(nil)
	require.Error(t, err)
}

func TestMockNetlinkExecutor_RouteAdd_Default(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	err := mock.RouteAdd(nil)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_RouteDel(t *testing.T) {
	mock := &MockNetlinkExecutor{
		RouteDelFunc: func(route *netlink.Route) error {
			return nil
		},
	}

	err := mock.RouteDel(nil)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_RouteList(t *testing.T) {
	expectedRoutes := []netlink.Route{
		{Dst: &net.IPNet{IP: net.ParseIP("10.0.0.0"), Mask: net.IPMask{255, 255, 255, 0}}},
	}

	mock := &MockNetlinkExecutor{
		RouteListFunc: func(link netlink.Link, family int) ([]netlink.Route, error) {
			return expectedRoutes, nil
		},
	}

	routes, err := mock.RouteList(nil, netlink.FAMILY_V4)
	require.NoError(t, err)
	assert.Len(t, routes, 1)
}

func TestMockNetlinkExecutor_RouteList_Default(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	routes, err := mock.RouteList(nil, 0)
	assert.Nil(t, routes)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_BridgeVlanAdd(t *testing.T) {
	mock := &MockNetlinkExecutor{
		BridgeVlanAddFunc: func(link netlink.Link, vid int) error {
			if vid < 1 || vid > 4094 {
				return errors.New("invalid VLAN ID")
			}
			return nil
		},
	}

	err := mock.BridgeVlanAdd(nil, 100)
	assert.NoError(t, err)

	err = mock.BridgeVlanAdd(nil, 0)
	require.Error(t, err)
}

func TestMockNetlinkExecutor_BridgeVlanDel(t *testing.T) {
	mock := &MockNetlinkExecutor{
		BridgeVlanDelFunc: func(link netlink.Link, vid int) error {
			return nil
		},
	}

	err := mock.BridgeVlanDel(nil, 100)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_NeighAdd(t *testing.T) {
	mock := &MockNetlinkExecutor{
		NeighAddFunc: func(neigh *netlink.Neigh) error {
			return errors.New("neighbor add failed")
		},
	}

	err := mock.NeighAdd(nil)
	require.Error(t, err)
}

func TestMockNetlinkExecutor_NeighAdd_Default(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	err := mock.NeighAdd(nil)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_NeighDel(t *testing.T) {
	mock := &MockNetlinkExecutor{
		NeighDelFunc: func(neigh *netlink.Neigh) error {
			return nil
		},
	}

	err := mock.NeighDel(nil)
	assert.NoError(t, err)
}

func TestMockNetlinkExecutor_NeighList(t *testing.T) {
	expectedNeighs := []netlink.Neigh{
		{IP: net.ParseIP("10.0.0.2")},
	}

	mock := &MockNetlinkExecutor{
		NeighListFunc: func(linkIndex, family int) ([]netlink.Neigh, error) {
			return expectedNeighs, nil
		},
	}

	neighs, err := mock.NeighList(1, netlink.FAMILY_V4)
	require.NoError(t, err)
	assert.Len(t, neighs, 1)
}

func TestMockNetlinkExecutor_NeighList_Default(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	neighs, err := mock.NeighList(0, 0)
	assert.Nil(t, neighs)
	assert.NoError(t, err)
}

// =============================================================================
// NetlinkExecutor interface compliance
// =============================================================================

func TestNetlinkExecutor_Interface(t *testing.T) {
	// Verify both implementations satisfy the interface
	var _ NetlinkExecutor = NewDefaultNetlinkExecutor()
	var _ NetlinkExecutor = &MockNetlinkExecutor{}
}

// =============================================================================
// DefaultNetlinkExecutor type verification
// =============================================================================

func TestNewDefaultNetlinkExecutor_Type(t *testing.T) {
	executor := NewDefaultNetlinkExecutor()

	require.NotNil(t, executor)
	assert.IsType(t, &DefaultNetlinkExecutor{}, executor)
}

// =============================================================================
// Addr parsing and IP tests
// =============================================================================

func TestNetlinkAddr_IPNet(t *testing.T) {
	ip, ipNet, err := net.ParseCIDR("10.0.0.1/24")
	require.NoError(t, err)

	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   ip,
			Mask: ipNet.Mask,
		},
	}

	assert.Equal(t, "10.0.0.1", addr.IPNet.IP.String())
	ones, _ := addr.IPNet.Mask.Size()
	assert.Equal(t, 24, ones)
}

func TestNetlinkAddr_MaskSize(t *testing.T) {
	tests := []struct {
		cidr   string
		prefix int
	}{
		{"192.168.1.1/24", 24},
		{"10.0.0.1/16", 16},
		{"172.16.0.1/12", 12},
		{"192.168.255.255/32", 32},
	}

	for _, tt := range tests {
		_, ipNet, err := net.ParseCIDR(tt.cidr)
		require.NoError(t, err)

		addr := &netlink.Addr{IPNet: ipNet}
		ones, _ := addr.IPNet.Mask.Size()
		assert.Equal(t, tt.prefix, ones)
	}
}

// =============================================================================
// Link types test
// =============================================================================

func TestNetlinkLinkTypes(t *testing.T) {
	// Verify various link types can be created
	dummy := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "dummy0"}}
	assert.Equal(t, "dummy0", dummy.Name)

	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0"}}
	assert.Equal(t, "br0", bridge.Name)

	vxlan := &netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{Name: "vxlan0"},
		VxlanId:   100,
	}
	assert.Equal(t, "vxlan0", vxlan.Name)
	assert.Equal(t, 100, vxlan.VxlanId)
}

// =============================================================================
// Route tests
// =============================================================================

func TestNetlinkRoute_Dst(t *testing.T) {
	_, dst, err := net.ParseCIDR("10.1.0.0/24")
	require.NoError(t, err)

	route := &netlink.Route{
		Dst: dst,
		Gw:  net.ParseIP("10.0.0.1"),
	}

	assert.Equal(t, "10.1.0.0/24", route.Dst.String())
	assert.Equal(t, "10.0.0.1", route.Gw.String())
}

func TestNetlinkRoute_DefaultRoute(t *testing.T) {
	_, dst, _ := net.ParseCIDR("0.0.0.0/0")

	route := &netlink.Route{
		Dst: dst,
	}

	assert.Equal(t, "0.0.0.0/0", route.Dst.String())
}

// =============================================================================
// Neighbor/FDB tests
// =============================================================================

func TestNetlinkNeigh_FDBEntry(t *testing.T) {
	neigh := &netlink.Neigh{
		LinkIndex:    1,
		IP:           net.ParseIP("10.0.0.2"),
		HardwareAddr: net.HardwareAddr{0x00, 0x11, 0x22, 0x33, 0x44, 0x55},
	}

	assert.Equal(t, 1, neigh.LinkIndex)
	assert.Equal(t, "10.0.0.2", neigh.IP.String())
	assert.Equal(t, "00:11:22:33:44:55", neigh.HardwareAddr.String())
}

func TestNetlinkNeigh_VxlanFDB(t *testing.T) {
	// VXLAN FDB uses all-zeros MAC for broadcast
	neigh := &netlink.Neigh{
		LinkIndex:    1,
		IP:           nil, // VXLAN FDB may not have IP
		HardwareAddr: net.HardwareAddr{0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
	}

	assert.Equal(t, "00:00:00:00:00:00", neigh.HardwareAddr.String())
}

// =============================================================================
// Link attributes tests
// =============================================================================

func TestLinkAttrs_NameAndIndex(t *testing.T) {
	attrs := netlink.LinkAttrs{
		Name:  "eth0",
		Index: 2,
		MTU:   1500,
	}

	assert.Equal(t, "eth0", attrs.Name)
	assert.Equal(t, 2, attrs.Index)
	assert.Equal(t, 1500, attrs.MTU)
}

func TestLinkAttrs_HardwareAddr(t *testing.T) {
	mac := net.HardwareAddr{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}
	attrs := netlink.LinkAttrs{
		Name:         "eth0",
		HardwareAddr: mac,
	}

	assert.Equal(t, "00:11:22:33:44:55", attrs.HardwareAddr.String())
}

// =============================================================================
// Family constants tests
// =============================================================================

func TestNetlink_FamilyConstants(t *testing.T) {
	// Verify family constants are as expected
	assert.Equal(t, 0, netlink.FAMILY_ALL)
	assert.Equal(t, 2, netlink.FAMILY_V4)
	assert.Equal(t, 10, netlink.FAMILY_V6)
}

// =============================================================================
// Bridge VLAN tests
// =============================================================================

func TestBridgeVlan_Range(t *testing.T) {
	// Valid VLAN IDs: 1-4094
	validVLANs := []int{1, 100, 4094}
	invalidVLANs := []int{0, 4095, 5000}

	for _, vid := range validVLANs {
		assert.True(t, vid >= 1 && vid <= 4094, "VLAN %d should be valid", vid)
	}

	for _, vid := range invalidVLANs {
		assert.False(t, vid >= 1 && vid <= 4094, "VLAN %d should be invalid", vid)
	}
}
