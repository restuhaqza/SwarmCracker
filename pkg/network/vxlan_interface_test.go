//go:build linux

package network

import (
	"context"
	"errors"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestVXLANManager_CreateVXLANInterface_WithMock(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkByNameFunc: func(name string) (netlink.Link, error) {
			if name == "eth0" {
				return &netlink.Dummy{
					LinkAttrs: netlink.LinkAttrs{
						Name:  name,
						Index: 1,
					},
				}, nil
			}
			return nil, errors.New("link not found")
		},
		LinkDelFunc: func(link netlink.Link) error {
			return nil
		},
		LinkAddFunc: func(link netlink.Link) error {
			return nil
		},
		LinkSetUpFunc: func(link netlink.Link) error {
			return nil
		},
	}

	v := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", nil, mock)

	err := v.createVXLANInterface("br0-vxlan", "eth0", "192.168.1.1")
	if err != nil {
		t.Errorf("createVXLANInterface failed: %v", err)
	}
}

func TestVXLANManager_AttachVXLANToBridge_WithMock(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkByNameFunc: func(name string) (netlink.Link, error) {
			return &netlink.Dummy{
				LinkAttrs: netlink.LinkAttrs{
					Name:  name,
					Index: 1,
				},
			}, nil
		},
		LinkSetMasterFunc: func(link netlink.Link, master netlink.Link) error {
			return nil
		},
	}

	v := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", nil, mock)

	err := v.attachVXLANToBridge("br0-vxlan")
	if err != nil {
		t.Errorf("attachVXLANToBridge failed: %v", err)
	}
}

func TestVXLANManager_AddOverlayIP_WithMock(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkByNameFunc: func(name string) (netlink.Link, error) {
			return &netlink.Dummy{
				LinkAttrs: netlink.LinkAttrs{
					Name:  name,
					Index: 1,
				},
			}, nil
		},
		AddrAddFunc: func(link netlink.Link, addr *netlink.Addr) error {
			return nil
		},
	}

	v := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", nil, mock)

	err := v.addOverlayIP()
	if err != nil {
		t.Errorf("addOverlayIP failed: %v", err)
	}
}

func TestVXLANManager_AddPeerForwarding_WithMock(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkByNameFunc: func(name string) (netlink.Link, error) {
			return &netlink.Dummy{
				LinkAttrs: netlink.LinkAttrs{
					Name:  name,
					Index: 1,
				},
			}, nil
		},
		NeighAddFunc: func(neigh *netlink.Neigh) error {
			return nil
		},
	}

	v := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", nil, mock)

	err := v.addPeerForwarding("br0-vxlan", "192.168.1.2")
	if err != nil {
		t.Errorf("addPeerForwarding failed: %v", err)
	}
}

func TestVXLANManager_AddRouteToSubnet_WithMock(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkByNameFunc: func(name string) (netlink.Link, error) {
			return &netlink.Dummy{
				LinkAttrs: netlink.LinkAttrs{
					Name:  name,
					Index: 1,
				},
			}, nil
		},
		RouteAddFunc: func(route *netlink.Route) error {
			return nil
		},
	}

	v := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", nil, mock)

	err := v.AddRouteToSubnet("172.16.0.0/24", "10.0.0.2")
	if err != nil {
		t.Errorf("AddRouteToSubnet failed: %v", err)
	}
}

func TestVXLANManager_UpdatePeers_WithMock(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkByNameFunc: func(name string) (netlink.Link, error) {
			return &netlink.Dummy{
				LinkAttrs: netlink.LinkAttrs{
					Name:  name,
					Index: 1,
				},
			}, nil
		},
		NeighAddFunc: func(neigh *netlink.Neigh) error {
			return nil
		},
	}

	peerStore := NewStaticPeerStore([]string{"192.168.1.2"})
	v := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", peerStore, mock)

	// First, simulate VXLAN setup by setting context
	v.ctx, v.cancel = context.WithCancel(context.Background())

	err := v.UpdatePeers([]string{"192.168.1.3", "192.168.1.4"})
	if err != nil {
		t.Errorf("UpdatePeers failed: %v", err)
	}

	peers := peerStore.GetPeers()
	if len(peers) < 2 {
		t.Errorf("Expected at least 2 peers, got %d", len(peers))
	}
}

func TestVXLANManager_CreateVXLANInterface_InvalidIP(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	v := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", nil, mock)

	err := v.createVXLANInterface("br0-vxlan", "eth0", "invalid-ip")
	if err == nil {
		t.Error("Expected error for invalid IP")
	}
}

func TestVXLANManager_AttachVXLANToBridge_VXLANNotFound_Interface(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkByNameFunc: func(name string) (netlink.Link, error) {
			if name == "br0-vxlan" {
				return nil, errors.New("link not found")
			}
			return &netlink.Dummy{}, nil
		},
	}

	v := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", nil, mock)

	err := v.attachVXLANToBridge("br0-vxlan")
	if err == nil {
		t.Error("Expected error when VXLAN interface not found")
	}
}

func TestVXLANManager_AddRouteToSubnet_InvalidSubnet(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	v := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", nil, mock)

	err := v.AddRouteToSubnet("invalid-subnet", "10.0.0.2")
	if err == nil {
		t.Error("Expected error for invalid subnet")
	}
}

func TestVXLANManager_AddRouteToSubnet_InvalidGateway_Interface(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	v := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", nil, mock)

	err := v.AddRouteToSubnet("172.16.0.0/24", "invalid-gateway")
	if err == nil {
		t.Error("Expected error for invalid gateway IP")
	}
}

func TestVXLANManager_AddPeerForwarding_InvalidPeerIP_Interface(t *testing.T) {
	mock := &MockNetlinkExecutor{}

	v := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", nil, mock)

	err := v.addPeerForwarding("br0-vxlan", "invalid-ip")
	if err == nil {
		t.Error("Expected error for invalid peer IP")
	}
}

func TestVXLANManager_AddOverlayIP_InvalidCIDR_Interface(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkByNameFunc: func(name string) (netlink.Link, error) {
			return &netlink.Dummy{}, nil
		},
	}

	v := NewVXLANManagerWithExecutor("br0", 100, "invalid-cidr", nil, mock)

	err := v.addOverlayIP()
	if err == nil {
		t.Error("Expected error for invalid CIDR")
	}
}

func TestVXLANManager_AddOverlayIP_BridgeNotFound_Interface(t *testing.T) {
	mock := &MockNetlinkExecutor{
		LinkByNameFunc: func(name string) (netlink.Link, error) {
			return nil, errors.New("link not found")
		},
	}

	v := NewVXLANManagerWithExecutor("br0", 100, "10.0.0.1/24", nil, mock)

	err := v.addOverlayIP()
	if err == nil {
		t.Error("Expected error when bridge not found")
	}
}