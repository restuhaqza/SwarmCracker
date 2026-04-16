// Package network provides mock implementations for testing.

package network

import (
	"github.com/vishvananda/netlink"
)

// MockNetlinkExecutor is a mock implementation for testing.
type MockNetlinkExecutor struct {
	LinkByNameFunc    func(name string) (netlink.Link, error)
	LinkAddFunc       func(link netlink.Link) error
	LinkDelFunc       func(link netlink.Link) error
	LinkSetUpFunc     func(link netlink.Link) error
	LinkSetDownFunc   func(link netlink.Link) error
	LinkSetMasterFunc func(link netlink.Link, master netlink.Link) error
	LinkSetMTUFunc    func(link netlink.Link, mtu int) error
	LinkSetNsFdFunc   func(link netlink.Link, fd int) error
	AddrAddFunc       func(link netlink.Link, addr *netlink.Addr) error
	AddrDelFunc       func(link netlink.Link, addr *netlink.Addr) error
	AddrListFunc      func(link netlink.Link, family int) ([]netlink.Addr, error)
	RouteAddFunc      func(route *netlink.Route) error
	RouteDelFunc      func(route *netlink.Route) error
	RouteListFunc     func(link netlink.Link, family int) ([]netlink.Route, error)
	BridgeVlanAddFunc func(link netlink.Link, vid int) error
	BridgeVlanDelFunc func(link netlink.Link, vid int) error
	NeighAddFunc      func(neigh *netlink.Neigh) error
	NeighDelFunc      func(neigh *netlink.Neigh) error
	NeighListFunc     func(linkIndex, family int) ([]netlink.Neigh, error)
}

func (m *MockNetlinkExecutor) LinkByName(name string) (netlink.Link, error) {
	if m.LinkByNameFunc != nil {
		return m.LinkByNameFunc(name)
	}
	return nil, nil
}

func (m *MockNetlinkExecutor) LinkAdd(link netlink.Link) error {
	if m.LinkAddFunc != nil {
		return m.LinkAddFunc(link)
	}
	return nil
}

func (m *MockNetlinkExecutor) LinkDel(link netlink.Link) error {
	if m.LinkDelFunc != nil {
		return m.LinkDelFunc(link)
	}
	return nil
}

func (m *MockNetlinkExecutor) LinkSetUp(link netlink.Link) error {
	if m.LinkSetUpFunc != nil {
		return m.LinkSetUpFunc(link)
	}
	return nil
}

func (m *MockNetlinkExecutor) LinkSetDown(link netlink.Link) error {
	if m.LinkSetDownFunc != nil {
		return m.LinkSetDownFunc(link)
	}
	return nil
}

func (m *MockNetlinkExecutor) LinkSetMaster(link netlink.Link, master netlink.Link) error {
	if m.LinkSetMasterFunc != nil {
		return m.LinkSetMasterFunc(link, master)
	}
	return nil
}

func (m *MockNetlinkExecutor) LinkSetMTU(link netlink.Link, mtu int) error {
	if m.LinkSetMTUFunc != nil {
		return m.LinkSetMTUFunc(link, mtu)
	}
	return nil
}

func (m *MockNetlinkExecutor) LinkSetNsFd(link netlink.Link, fd int) error {
	if m.LinkSetNsFdFunc != nil {
		return m.LinkSetNsFdFunc(link, fd)
	}
	return nil
}

func (m *MockNetlinkExecutor) AddrAdd(link netlink.Link, addr *netlink.Addr) error {
	if m.AddrAddFunc != nil {
		return m.AddrAddFunc(link, addr)
	}
	return nil
}

func (m *MockNetlinkExecutor) AddrDel(link netlink.Link, addr *netlink.Addr) error {
	if m.AddrDelFunc != nil {
		return m.AddrDelFunc(link, addr)
	}
	return nil
}

func (m *MockNetlinkExecutor) AddrList(link netlink.Link, family int) ([]netlink.Addr, error) {
	if m.AddrListFunc != nil {
		return m.AddrListFunc(link, family)
	}
	return nil, nil
}

func (m *MockNetlinkExecutor) RouteAdd(route *netlink.Route) error {
	if m.RouteAddFunc != nil {
		return m.RouteAddFunc(route)
	}
	return nil
}

func (m *MockNetlinkExecutor) RouteDel(route *netlink.Route) error {
	if m.RouteDelFunc != nil {
		return m.RouteDelFunc(route)
	}
	return nil
}

func (m *MockNetlinkExecutor) RouteList(link netlink.Link, family int) ([]netlink.Route, error) {
	if m.RouteListFunc != nil {
		return m.RouteListFunc(link, family)
	}
	return nil, nil
}

func (m *MockNetlinkExecutor) BridgeVlanAdd(link netlink.Link, vid int) error {
	if m.BridgeVlanAddFunc != nil {
		return m.BridgeVlanAddFunc(link, vid)
	}
	return nil
}

func (m *MockNetlinkExecutor) BridgeVlanDel(link netlink.Link, vid int) error {
	if m.BridgeVlanDelFunc != nil {
		return m.BridgeVlanDelFunc(link, vid)
	}
	return nil
}

func (m *MockNetlinkExecutor) NeighAdd(neigh *netlink.Neigh) error {
	if m.NeighAddFunc != nil {
		return m.NeighAddFunc(neigh)
	}
	return nil
}

func (m *MockNetlinkExecutor) NeighDel(neigh *netlink.Neigh) error {
	if m.NeighDelFunc != nil {
		return m.NeighDelFunc(neigh)
	}
	return nil
}

func (m *MockNetlinkExecutor) NeighList(linkIndex, family int) ([]netlink.Neigh, error) {
	if m.NeighListFunc != nil {
		return m.NeighListFunc(linkIndex, family)
	}
	return nil, nil
}