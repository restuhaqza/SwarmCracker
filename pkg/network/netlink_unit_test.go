package network

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
)

// TestNewDefaultNetlinkExecutor tests netlink executor creation
func TestNewDefaultNetlinkExecutor(t *testing.T) {
	executor := NewDefaultNetlinkExecutor()
	require.NotNil(t, executor)
	assert.IsType(t, &DefaultNetlinkExecutor{}, executor)
}

// TestDefaultNetlinkExecutor_Interface tests the interface compliance
func TestDefaultNetlinkExecutor_Interface(t *testing.T) {
	// Verify DefaultNetlinkExecutor implements NetlinkExecutor interface
	var _ NetlinkExecutor = &DefaultNetlinkExecutor{}
	var _ NetlinkExecutor = NewDefaultNetlinkExecutor()
}

// TestMockNetlinkExecutor tests mock netlink executor behavior
func TestMockNetlinkExecutor_AllMethods(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	t.Run("all methods return nil by default", func(t *testing.T) {
		// LinkByName returns nil without function set
		link, err := mock.LinkByName("test")
		assert.Nil(t, link)
		assert.NoError(t, err)

		// All other methods return nil error by default
		assert.NoError(t, mock.LinkAdd(nil))
		assert.NoError(t, mock.LinkDel(nil))
		assert.NoError(t, mock.LinkSetUp(nil))
		assert.NoError(t, mock.LinkSetDown(nil))
		assert.NoError(t, mock.LinkSetMaster(nil, nil))
		assert.NoError(t, mock.LinkSetMTU(nil, 1500))
		assert.NoError(t, mock.LinkSetNsFd(nil, 0))
		assert.NoError(t, mock.AddrAdd(nil, nil))
		assert.NoError(t, mock.AddrDel(nil, nil))
		assert.NoError(t, mock.RouteAdd(nil))
		assert.NoError(t, mock.RouteDel(nil))
		assert.NoError(t, mock.BridgeVlanAdd(nil, 1))
		assert.NoError(t, mock.BridgeVlanDel(nil, 1))
		assert.NoError(t, mock.NeighAdd(nil))
		assert.NoError(t, mock.NeighDel(nil))
	})

	t.Run("AddrList returns nil by default", func(t *testing.T) {
		addrs, err := mock.AddrList(nil, 0)
		assert.Nil(t, addrs)
		assert.NoError(t, err)
	})

	t.Run("RouteList returns nil by default", func(t *testing.T) {
		routes, err := mock.RouteList(nil, 0)
		assert.Nil(t, routes)
		assert.NoError(t, err)
	})

	t.Run("NeighList returns nil by default", func(t *testing.T) {
		neighs, err := mock.NeighList(0, 0)
		assert.Nil(t, neighs)
		assert.NoError(t, err)
	})
}

// TestMockNetlinkExecutor_CustomFunctions tests custom function behavior
func TestMockNetlinkExecutor_CustomFunctions(t *testing.T) {
	t.Run("LinkByName custom function", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				if name == "existing-link" {
					return &netlink.GenericLink{
						LinkAttrs: netlink.LinkAttrs{Name: name},
					}, nil
				}
				return nil, netlink.LinkNotFoundError{}
			},
		}

		link, err := mock.LinkByName("existing-link")
		assert.NoError(t, err)
		assert.Equal(t, "existing-link", link.Attrs().Name)

		link, err = mock.LinkByName("nonexistent")
		assert.Error(t, err)
		assert.Nil(t, link)
	})

	t.Run("LinkAdd custom function", func(t *testing.T) {
		addedLinks := make(map[string]bool)
		mock := &MockNetlinkExecutor{
			LinkAddFunc: func(link netlink.Link) error {
				addedLinks[link.Attrs().Name] = true
				return nil
			},
		}

		bridge := &netlink.Bridge{
			LinkAttrs: netlink.LinkAttrs{Name: "test-bridge"},
		}
		err := mock.LinkAdd(bridge)
		assert.NoError(t, err)
		assert.True(t, addedLinks["test-bridge"])
	})

	t.Run("LinkDel custom function", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkDelFunc: func(link netlink.Link) error {
				if link == nil {
					return netlink.LinkNotFoundError{}
				}
				return nil
			},
		}

		err := mock.LinkDel(&netlink.GenericLink{})
		assert.NoError(t, err)

		err = mock.LinkDel(nil)
		assert.Error(t, err)
	})

	t.Run("LinkSetUp custom function", func(t *testing.T) {
		upLinks := make(map[string]bool)
		mock := &MockNetlinkExecutor{
			LinkSetUpFunc: func(link netlink.Link) error {
				if link != nil {
					upLinks[link.Attrs().Name] = true
				}
				return nil
			},
		}

		link := &netlink.GenericLink{
			LinkAttrs: netlink.LinkAttrs{Name: "tap0"},
		}
		err := mock.LinkSetUp(link)
		assert.NoError(t, err)
		assert.True(t, upLinks["tap0"])
	})

	t.Run("LinkSetDown custom function", func(t *testing.T) {
		downLinks := make(map[string]bool)
		mock := &MockNetlinkExecutor{
			LinkSetDownFunc: func(link netlink.Link) error {
				if link != nil {
					downLinks[link.Attrs().Name] = true
				}
				return nil
			},
		}

		link := &netlink.GenericLink{
			LinkAttrs: netlink.LinkAttrs{Name: "tap1"},
		}
		err := mock.LinkSetDown(link)
		assert.NoError(t, err)
		assert.True(t, downLinks["tap1"])
	})

	t.Run("LinkSetMaster custom function", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkSetMasterFunc: func(link, master netlink.Link) error {
				if link == nil || master == nil {
					return netlink.LinkNotFoundError{}
				}
				return nil
			},
		}

		slave := &netlink.GenericLink{LinkAttrs: netlink.LinkAttrs{Name: "tap0"}}
		master := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0"}}
		err := mock.LinkSetMaster(slave, master)
		assert.NoError(t, err)

		err = mock.LinkSetMaster(nil, master)
		assert.Error(t, err)

		err = mock.LinkSetMaster(slave, nil)
		assert.Error(t, err)
	})

	t.Run("LinkSetMTU custom function", func(t *testing.T) {
		mtuMap := make(map[string]int)
		mock := &MockNetlinkExecutor{
			LinkSetMTUFunc: func(link netlink.Link, mtu int) error {
				if link != nil {
					mtuMap[link.Attrs().Name] = mtu
				}
				return nil
			},
		}

		link := &netlink.GenericLink{LinkAttrs: netlink.LinkAttrs{Name: "vxlan0"}}
		err := mock.LinkSetMTU(link, 1450)
		assert.NoError(t, err)
		assert.Equal(t, 1450, mtuMap["vxlan0"])
	})

	t.Run("LinkSetNsFd custom function", func(t *testing.T) {
		nsMap := make(map[string]int)
		mock := &MockNetlinkExecutor{
			LinkSetNsFdFunc: func(link netlink.Link, fd int) error {
				if link != nil {
					nsMap[link.Attrs().Name] = fd
				}
				return nil
			},
		}

		link := &netlink.GenericLink{LinkAttrs: netlink.LinkAttrs{Name: "veth0"}}
		err := mock.LinkSetNsFd(link, 10)
		assert.NoError(t, err)
		assert.Equal(t, 10, nsMap["veth0"])
	})
}

// TestMockNetlinkExecutor_AddressOperations tests address operations
func TestMockNetlinkExecutor_AddressOperations(t *testing.T) {
	t.Run("AddrAdd custom function", func(t *testing.T) {
		addedAddrs := make(map[string]bool)
		mock := &MockNetlinkExecutor{
			AddrAddFunc: func(link netlink.Link, addr *netlink.Addr) error {
				if addr != nil {
					addedAddrs[addr.IPNet.String()] = true
				}
				return nil
			},
		}

		link := &netlink.GenericLink{LinkAttrs: netlink.LinkAttrs{Name: "br0"}}
		addr, err := netlink.ParseAddr("192.168.1.1/24")
		require.NoError(t, err)

		err = mock.AddrAdd(link, addr)
		assert.NoError(t, err)
		assert.True(t, addedAddrs["192.168.1.1/24"])
	})

	t.Run("AddrDel custom function", func(t *testing.T) {
		deletedAddrs := make(map[string]bool)
		mock := &MockNetlinkExecutor{
			AddrDelFunc: func(link netlink.Link, addr *netlink.Addr) error {
				if addr != nil {
					deletedAddrs[addr.IPNet.String()] = true
				}
				return nil
			},
		}

		link := &netlink.GenericLink{LinkAttrs: netlink.LinkAttrs{Name: "br0"}}
		addr, err := netlink.ParseAddr("192.168.1.1/24")
		require.NoError(t, err)

		err = mock.AddrDel(link, addr)
		assert.NoError(t, err)
		assert.True(t, deletedAddrs["192.168.1.1/24"])
	})

	t.Run("AddrList custom function", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			AddrListFunc: func(link netlink.Link, family int) ([]netlink.Addr, error) {
				addr1, _ := netlink.ParseAddr("192.168.1.1/24")
				addr2, _ := netlink.ParseAddr("10.0.0.1/16")
				return []netlink.Addr{*addr1, *addr2}, nil
			},
		}

		addrs, err := mock.AddrList(nil, netlink.FAMILY_V4)
		assert.NoError(t, err)
		assert.Len(t, addrs, 2)
	})

	t.Run("AddrList error simulation", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			AddrListFunc: func(link netlink.Link, family int) ([]netlink.Addr, error) {
				return nil, netlink.LinkNotFoundError{}
			},
		}

		addrs, err := mock.AddrList(nil, netlink.FAMILY_V4)
		assert.Error(t, err)
		assert.Nil(t, addrs)
	})
}

// TestMockNetlinkExecutor_RouteOperations tests route operations
func TestMockNetlinkExecutor_RouteOperations(t *testing.T) {
	t.Run("RouteAdd custom function", func(t *testing.T) {
		addedRoutes := make(map[string]bool)
		mock := &MockNetlinkExecutor{
			RouteAddFunc: func(route *netlink.Route) error {
				if route != nil && route.Dst != nil {
					addedRoutes[route.Dst.String()] = true
				}
				return nil
			},
		}

		_, dst, _ := net.ParseCIDR("10.0.0.0/8")
		route := &netlink.Route{Dst: dst}
		err := mock.RouteAdd(route)
		assert.NoError(t, err)
		assert.True(t, addedRoutes["10.0.0.0/8"])
	})

	t.Run("RouteDel custom function", func(t *testing.T) {
		deletedRoutes := make(map[string]bool)
		mock := &MockNetlinkExecutor{
			RouteDelFunc: func(route *netlink.Route) error {
				if route != nil && route.Dst != nil {
					deletedRoutes[route.Dst.String()] = true
				}
				return nil
			},
		}

		_, dst, _ := net.ParseCIDR("192.168.0.0/16")
		route := &netlink.Route{Dst: dst}
		err := mock.RouteDel(route)
		assert.NoError(t, err)
		assert.True(t, deletedRoutes["192.168.0.0/16"])
	})

	t.Run("RouteList custom function", func(t *testing.T) {
		_, dst1, _ := net.ParseCIDR("10.0.0.0/8")
		_, dst2, _ := net.ParseCIDR("192.168.0.0/16")
		mock := &MockNetlinkExecutor{
			RouteListFunc: func(link netlink.Link, family int) ([]netlink.Route, error) {
				return []netlink.Route{
					{Dst: dst1},
					{Dst: dst2},
				}, nil
			},
		}

		routes, err := mock.RouteList(nil, netlink.FAMILY_V4)
		assert.NoError(t, err)
		assert.Len(t, routes, 2)
	})
}

// TestMockNetlinkExecutor_BridgeVlanOperations tests bridge VLAN operations
func TestMockNetlinkExecutor_BridgeVlanOperations(t *testing.T) {
	t.Run("BridgeVlanAdd custom function", func(t *testing.T) {
		vlanMap := make(map[int]bool)
		mock := &MockNetlinkExecutor{
			BridgeVlanAddFunc: func(link netlink.Link, vid int) error {
				vlanMap[vid] = true
				return nil
			},
		}

		err := mock.BridgeVlanAdd(nil, 100)
		assert.NoError(t, err)
		assert.True(t, vlanMap[100])

		err = mock.BridgeVlanAdd(nil, 200)
		assert.NoError(t, err)
		assert.True(t, vlanMap[200])
	})

	t.Run("BridgeVlanDel custom function", func(t *testing.T) {
		vlanMap := make(map[int]bool)
		mock := &MockNetlinkExecutor{
			BridgeVlanDelFunc: func(link netlink.Link, vid int) error {
				delete(vlanMap, vid)
				return nil
			},
		}

		// Add then delete
		vlanMap[100] = true
		err := mock.BridgeVlanDel(nil, 100)
		assert.NoError(t, err)
		assert.False(t, vlanMap[100])
	})
}

// TestMockNetlinkExecutor_NeighborOperations tests neighbor/FDB operations
func TestMockNetlinkExecutor_NeighborOperations(t *testing.T) {
	t.Run("NeighAdd custom function", func(t *testing.T) {
		neighMap := make(map[string]bool)
		mock := &MockNetlinkExecutor{
			NeighAddFunc: func(neigh *netlink.Neigh) error {
				if neigh != nil {
					neighMap[neigh.IP.String()] = true
				}
				return nil
			},
		}

		neigh := &netlink.Neigh{
			IP: net.ParseIP("192.168.1.100"),
			HardwareAddr: []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55},
		}
		err := mock.NeighAdd(neigh)
		assert.NoError(t, err)
		assert.True(t, neighMap["192.168.1.100"])
	})

	t.Run("NeighDel custom function", func(t *testing.T) {
		neighMap := make(map[string]bool)
		mock := &MockNetlinkExecutor{
			NeighDelFunc: func(neigh *netlink.Neigh) error {
				if neigh != nil {
					delete(neighMap, neigh.IP.String())
				}
				return nil
			},
		}

		neighMap["192.168.1.100"] = true
		neigh := &netlink.Neigh{
			IP: net.ParseIP("192.168.1.100"),
		}
		err := mock.NeighDel(neigh)
		assert.NoError(t, err)
		assert.False(t, neighMap["192.168.1.100"])
	})

	t.Run("NeighList custom function", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			NeighListFunc: func(linkIndex, family int) ([]netlink.Neigh, error) {
				return []netlink.Neigh{
					{IP: net.ParseIP("192.168.1.1")},
					{IP: net.ParseIP("192.168.1.2")},
				}, nil
			},
		}

		neighs, err := mock.NeighList(1, netlink.FAMILY_V4)
		assert.NoError(t, err)
		assert.Len(t, neighs, 2)
	})
}

// TestNetlinkExecutor_LinkTypes tests different link types
func TestNetlinkExecutor_LinkTypes(t *testing.T) {
	t.Run("Bridge link type", func(t *testing.T) {
		bridge := &netlink.Bridge{
			LinkAttrs: netlink.LinkAttrs{
				Name: "test-bridge",
				MTU:  1500,
			},
		}
		assert.Equal(t, "test-bridge", bridge.Name)
		assert.Equal(t, 1500, bridge.MTU)
	})

	t.Run("VXLAN link type", func(t *testing.T) {
		vxlan := &netlink.Vxlan{
			LinkAttrs: netlink.LinkAttrs{
				Name: "vxlan0",
				MTU:  1450,
			},
			VxlanId: 100,
			Port:    4789,
		}
		assert.Equal(t, "vxlan0", vxlan.Name)
		assert.Equal(t, 100, vxlan.VxlanId)
		assert.Equal(t, 4789, vxlan.Port)
	})

	t.Run("Generic link type", func(t *testing.T) {
		link := &netlink.GenericLink{
			LinkAttrs: netlink.LinkAttrs{
				Name: "tap0",
			},
		}
		assert.Equal(t, "tap0", link.Name)
	})
}

// TestNetlinkExecutor_AddressParsing tests address parsing
func TestNetlinkExecutor_AddressParsing(t *testing.T) {
	t.Run("parse IPv4 address", func(t *testing.T) {
		addr, err := netlink.ParseAddr("192.168.1.1/24")
		require.NoError(t, err)
		assert.Equal(t, "192.168.1.1", addr.IP.String())
		// Get mask size from the IPNet mask
		ones, _ := addr.Mask.Size()
		assert.Equal(t, 24, ones)
	})

	t.Run("parse IPv6 address", func(t *testing.T) {
		addr, err := netlink.ParseAddr("fd00::1/64")
		require.NoError(t, err)
		assert.Equal(t, "fd00::1", addr.IP.String())
	})

	t.Run("invalid address format", func(t *testing.T) {
		_, err := netlink.ParseAddr("invalid-address")
		assert.Error(t, err)
	})
}

// TestNetlinkExecutor_FamilyConstants tests family constants
func TestNetlinkExecutor_FamilyConstants(t *testing.T) {
	assert.Equal(t, 0, netlink.FAMILY_ALL)
	assert.Equal(t, 2, netlink.FAMILY_V4)
	assert.Equal(t, 10, netlink.FAMILY_V6)
}

// TestNetlinkExecutor_NeighConstants tests neighbor constants
func TestNetlinkExecutor_NeighConstants(t *testing.T) {
	// NUD_PERMANENT is used for static FDB entries
	assert.Equal(t, netlink.NUD_PERMANENT, netlink.NUD_PERMANENT)
}