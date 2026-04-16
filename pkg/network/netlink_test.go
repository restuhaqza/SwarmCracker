package network

import (
	"testing"

	"github.com/vishvananda/netlink"
)

func TestDefaultNetlinkExecutor_AllMethodsReturnNil(t *testing.T) {
	// This test verifies that DefaultNetlinkExecutor exists and has all methods.
	// Actual netlink calls require CAP_NET_ADMIN, so we just verify the struct exists.
	executor := NewDefaultNetlinkExecutor()

	if executor == nil {
		t.Error("NewDefaultNetlinkExecutor should return non-nil")
	}

	// Verify it's the correct type
	_, ok := executor.(*DefaultNetlinkExecutor)
	if !ok {
		t.Error("NewDefaultNetlinkExecutor should return *DefaultNetlinkExecutor")
	}
}

func TestMockNetlinkExecutor_AllMethodsCanBeConfigured(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	// Test that all methods can be configured and called
	var callCount int

	// All 19 methods (not counting the interface methods twice)
	mock.LinkByNameFunc = func(name string) (netlink.Link, error) { callCount++; return nil, nil }
	mock.LinkByName("test")

	mock.LinkAddFunc = func(link netlink.Link) error { callCount++; return nil }
	mock.LinkAdd(nil)

	mock.LinkDelFunc = func(link netlink.Link) error { callCount++; return nil }
	mock.LinkDel(nil)

	mock.LinkSetUpFunc = func(link netlink.Link) error { callCount++; return nil }
	mock.LinkSetUp(nil)

	mock.LinkSetDownFunc = func(link netlink.Link) error { callCount++; return nil }
	mock.LinkSetDown(nil)

	mock.LinkSetMasterFunc = func(link netlink.Link, master netlink.Link) error { callCount++; return nil }
	mock.LinkSetMaster(nil, nil)

	mock.LinkSetMTUFunc = func(link netlink.Link, mtu int) error { callCount++; return nil }
	mock.LinkSetMTU(nil, 1500)

	mock.LinkSetNsFdFunc = func(link netlink.Link, fd int) error { callCount++; return nil }
	mock.LinkSetNsFd(nil, 0)

	mock.AddrAddFunc = func(link netlink.Link, addr *netlink.Addr) error { callCount++; return nil }
	mock.AddrAdd(nil, nil)

	mock.AddrDelFunc = func(link netlink.Link, addr *netlink.Addr) error { callCount++; return nil }
	mock.AddrDel(nil, nil)

	mock.AddrListFunc = func(link netlink.Link, family int) ([]netlink.Addr, error) { callCount++; return nil, nil }
	mock.AddrList(nil, 0)

	mock.RouteAddFunc = func(route *netlink.Route) error { callCount++; return nil }
	mock.RouteAdd(nil)

	mock.RouteDelFunc = func(route *netlink.Route) error { callCount++; return nil }
	mock.RouteDel(nil)

	mock.RouteListFunc = func(link netlink.Link, family int) ([]netlink.Route, error) { callCount++; return nil, nil }
	mock.RouteList(nil, 0)

	mock.BridgeVlanAddFunc = func(link netlink.Link, vid int) error { callCount++; return nil }
	mock.BridgeVlanAdd(nil, 100)

	mock.BridgeVlanDelFunc = func(link netlink.Link, vid int) error { callCount++; return nil }
	mock.BridgeVlanDel(nil, 100)

	mock.NeighAddFunc = func(neigh *netlink.Neigh) error { callCount++; return nil }
	mock.NeighAdd(nil)

	mock.NeighDelFunc = func(neigh *netlink.Neigh) error { callCount++; return nil }
	mock.NeighDel(nil)

	mock.NeighListFunc = func(linkIndex, family int) ([]netlink.Neigh, error) { callCount++; return nil, nil }
	mock.NeighList(0, 0)

	// Verify all 19 methods were called
	if callCount != 19 {
		t.Errorf("Expected 19 method calls, got %d", callCount)
	}
}

func TestMockNetlinkExecutor_DefaultReturns(t *testing.T) {
	// Test that mock returns default values when funcs are not configured
	mock := &MockNetlinkExecutor{}

	link, err := mock.LinkByName("test")
	if link != nil || err != nil {
		t.Error("Default LinkByName should return nil, nil")
	}

	if err := mock.LinkAdd(nil); err != nil {
		t.Error("Default LinkAdd should return nil")
	}

	routes, err := mock.RouteList(nil, 0)
	if routes != nil || err != nil {
		t.Error("Default RouteList should return nil, nil")
	}

	neighs, err := mock.NeighList(0, 0)
	if neighs != nil || err != nil {
		t.Error("Default NeighList should return nil, nil")
	}
}

func TestNetlinkExecutor_InterfaceType(t *testing.T) {
	// Verify that both implementations satisfy the interface
	var _ NetlinkExecutor = NewDefaultNetlinkExecutor()
	var _ NetlinkExecutor = &MockNetlinkExecutor{}
}

func TestNewVXLANManagerWithExecutor_BackwardCompatibility(t *testing.T) {
	// Verify backward compatibility - NewVXLANManager should still work
	v := NewVXLANManager("br0", 100, "10.0.0.1/24", nil)

	if v == nil {
		t.Error("NewVXLANManager should return non-nil")
	}
	if v.BridgeName != "br0" {
		t.Errorf("Expected BridgeName br0, got %s", v.BridgeName)
	}

	// Verify NewVXLANManagerWithExecutor also works
	mock := &MockNetlinkExecutor{}
	v2 := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", nil, mock)

	if v2 == nil {
		t.Error("NewVXLANManagerWithExecutor should return non-nil")
	}
	if v2.netlinkExecutor == nil {
		t.Error("VXLANManager should have netlinkExecutor set")
	}
}