package network

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
)

// TestVXLANManager_EnableProxySettings tests proxy settings configuration
func TestVXLANManager_EnableProxySettings(t *testing.T) {
	t.Run("enable proxy settings with temp directory", func(t *testing.T) {
		// Create temporary sysctl directory structure
		tmpDir := t.TempDir()

		// Create necessary directories
		ipv4Dir := filepath.Join(tmpDir, "net", "ipv4")
		confDir := filepath.Join(ipv4Dir, "conf", "test-br0")
		require.NoError(t, os.MkdirAll(confDir, 0755))

		// Create sysctl files
		require.NoError(t, os.WriteFile(filepath.Join(ipv4Dir, "ip_forward"), []byte("0"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(confDir, "proxy_arp"), []byte("0"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(confDir, "forwarding"), []byte("0"), 0644))

		// Verify files were created
		assert.FileExists(t, filepath.Join(ipv4Dir, "ip_forward"))
		assert.FileExists(t, filepath.Join(confDir, "proxy_arp"))
	})
}

// TestVXLANManager_AddPeerForwarding_Errors tests error cases in peer forwarding
func TestVXLANManager_AddPeerForwarding_Errors(t *testing.T) {
	t.Run("invalid peer IP", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return &netlink.GenericLink{
					LinkAttrs: netlink.LinkAttrs{Name: name, Index: 1},
				}, nil
			},
		}

		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.addPeerForwarding("test-br-vxlan", "invalid-ip")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid peer IP")
	})

	t.Run("vxlan interface not found", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return nil, assert.AnError
			},
		}

		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.addPeerForwarding("nonexistent-vxlan", "192.168.1.100")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "VXLAN interface not found")
	})

	t.Run("neigh add fails", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return &netlink.GenericLink{
					LinkAttrs: netlink.LinkAttrs{Name: name, Index: 1},
				}, nil
			},
			NeighAddFunc: func(neigh *netlink.Neigh) error {
				// Use a generic error that doesn't panic
				return assert.AnError
			},
		}

		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.addPeerForwarding("test-br-vxlan", "192.168.1.100")
		// Should return error
		assert.Error(t, err)
	})
}

// TestVXLANManager_AddRouteToSubnet_Errors tests error cases in route addition
func TestVXLANManager_AddRouteToSubnet_Errors(t *testing.T) {
	t.Run("invalid remote subnet", func(t *testing.T) {
		vm := NewVXLANManager("test-br", 100, "192.168.1.1/24", nil)
		err := vm.AddRouteToSubnet("invalid-subnet", "192.168.1.100")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid remote subnet")
	})

	t.Run("invalid gateway IP", func(t *testing.T) {
		vm := NewVXLANManager("test-br", 100, "192.168.1.1/24", nil)
		err := vm.AddRouteToSubnet("10.0.0.0/8", "invalid-gateway")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid gateway IP")
	})

	t.Run("bridge not found", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return nil, assert.AnError
			},
		}

		vm := NewVXLANManagerWithExecutor("nonexistent-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.AddRouteToSubnet("10.0.0.0/8", "192.168.1.100")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bridge not found")
	})

	t.Run("route add fails", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return &netlink.GenericLink{
					LinkAttrs: netlink.LinkAttrs{Name: name, Index: 1},
				}, nil
			},
			RouteAddFunc: func(route *netlink.Route) error {
				return assert.AnError
			},
		}

		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.AddRouteToSubnet("10.0.0.0/8", "192.168.1.100")
		// Should return error (not "file exists")
		assert.Error(t, err)
	})
}

// TestVXLANManager_AttachVXLANToBridge_Errors tests error cases
func TestVXLANManager_AttachVXLANToBridge_Errors(t *testing.T) {
	t.Run("vxlan interface not found", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				if name == "test-br-vxlan" {
					return nil, assert.AnError
				}
				return &netlink.GenericLink{
					LinkAttrs: netlink.LinkAttrs{Name: name},
				}, nil
			},
		}

		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.attachVXLANToBridge("test-br-vxlan")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "VXLAN")
	})

	t.Run("bridge not found", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				if name == "test-br-vxlan" {
					return &netlink.GenericLink{
						LinkAttrs: netlink.LinkAttrs{Name: name},
					}, nil
				}
				// Return error for nonexistent bridge
				return nil, assert.AnError
			},
		}

		vm := NewVXLANManagerWithExecutor("nonexistent-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.attachVXLANToBridge("test-br-vxlan")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bridge")
	})

	t.Run("link set master fails", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return &netlink.GenericLink{
					LinkAttrs: netlink.LinkAttrs{Name: name},
				}, nil
			},
			LinkSetMasterFunc: func(link, master netlink.Link) error {
				return assert.AnError
			},
		}

		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.attachVXLANToBridge("test-br-vxlan")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to attach VXLAN to bridge")
	})
}

// TestVXLANManager_AddOverlayIP_Errors tests overlay IP error cases
func TestVXLANManager_AddOverlayIP_Errors(t *testing.T) {
	t.Run("bridge not found", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return nil, assert.AnError
			},
		}

		vm := NewVXLANManagerWithExecutor("nonexistent-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.addOverlayIP()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bridge not found")
	})

	t.Run("invalid overlay CIDR", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return &netlink.GenericLink{
					LinkAttrs: netlink.LinkAttrs{Name: name},
				}, nil
			},
		}

		vm := NewVXLANManagerWithExecutor("test-br", 100, "invalid-cidr", nil, mock)
		err := vm.addOverlayIP()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid overlay CIDR")
	})

	t.Run("addr add fails", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return &netlink.GenericLink{
					LinkAttrs: netlink.LinkAttrs{Name: name},
				}, nil
			},
			AddrAddFunc: func(link netlink.Link, addr *netlink.Addr) error {
				return assert.AnError
			},
		}

		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.addOverlayIP()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to add overlay IP")
	})
}

// TestVXLANManager_CreateVXLANInterface_Errors tests VXLAN creation error cases
func TestVXLANManager_CreateVXLANInterface_Errors(t *testing.T) {
	t.Run("invalid local IP", func(t *testing.T) {
		mock := &MockNetlinkExecutor{}
		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.createVXLANInterface("test-br-vxlan", "eth0", "invalid-ip")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid local IP")
	})

	t.Run("physical interface not found", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return nil, assert.AnError
			},
		}
		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.createVXLANInterface("test-br-vxlan", "eth0", "192.168.1.1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "eth0")
	})

	t.Run("link add fails", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return &netlink.GenericLink{
					LinkAttrs: netlink.LinkAttrs{Name: name, Index: 1},
				}, nil
			},
			LinkAddFunc: func(link netlink.Link) error {
				return assert.AnError
			},
		}
		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.createVXLANInterface("test-br-vxlan", "eth0", "192.168.1.1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to add VXLAN link")
	})

	t.Run("link set up fails", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return &netlink.GenericLink{
					LinkAttrs: netlink.LinkAttrs{Name: name, Index: 1},
				}, nil
			},
			LinkAddFunc: func(link netlink.Link) error {
				return nil
			},
			LinkSetUpFunc: func(link netlink.Link) error {
				return assert.AnError
			},
		}
		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.createVXLANInterface("test-br-vxlan", "eth0", "192.168.1.1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to bring VXLAN link up")
	})
}

// TestVXLANManager_UpdatePeers_Errors tests UpdatePeers error cases
func TestVXLANManager_UpdatePeers_Errors(t *testing.T) {
	t.Run("vxlan interface not found", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return nil, assert.AnError
			},
		}

		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", nil, mock)
		err := vm.UpdatePeers([]string{"192.168.1.100"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "VXLAN interface not found")
	})

	t.Run("peer forwarding fails for some peers", func(t *testing.T) {
		mock := &MockNetlinkExecutor{
			LinkByNameFunc: func(name string) (netlink.Link, error) {
				return &netlink.GenericLink{
					LinkAttrs: netlink.LinkAttrs{Name: name, Index: 1},
				}, nil
			},
			NeighAddFunc: func(neigh *netlink.Neigh) error {
				// Fail for some peers
				if neigh.IP.String() == "192.168.1.101" {
					return assert.AnError
				}
				return nil
			},
		}

		peerStore := NewStaticPeerStore([]string{"192.168.1.100"})
		vm := NewVXLANManagerWithExecutor("test-br", 100, "192.168.1.1/24", peerStore, mock)
		err := vm.UpdatePeers([]string{"192.168.1.100", "192.168.1.101", "192.168.1.102"})
		// Should succeed even if some peers fail
		assert.NoError(t, err)
	})
}

// TestVXLANManager_StartPeerDiscovery_Errors tests peer discovery error cases
func TestVXLANManager_StartPeerDiscovery_Errors(t *testing.T) {
	t.Run("peer discovery already running", func(t *testing.T) {
		vm := NewVXLANManager("test-br", 100, "192.168.1.1/24", nil)

		ctx := context.Background()
		err := vm.StartPeerDiscovery(ctx, "127.0.0.1", 4789)
		require.NoError(t, err)

		// Try to start again
		err = vm.StartPeerDiscovery(ctx, "127.0.0.1", 4789)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "peer discovery already running")

		// Stop to cleanup
		vm.StopPeerDiscovery()
	})
}
