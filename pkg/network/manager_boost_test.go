package network

import (
	"context"
	"sync"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

// Additional unique boost tests for network package

func TestNetworkManager_PrepareNetwork_InvalidBridgeName(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{
		BridgeName: "", // Empty bridge name
	}).(*NetworkManager)

	task := &types.Task{
		ID:        "task-invalid-bridge",
		ServiceID: "service-1",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "net-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "", // Empty bridge name
							},
						},
					},
				},
			},
		},
	}

	err := nm.PrepareNetwork(context.Background(), task)
	// Will fail without real network commands
	_ = err
}

func TestNetworkManager_PrepareNetwork_WithIPv4MappedIPv6(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{
		BridgeName: "test-br-ipv6-mapped",
	}).(*NetworkManager)

	task := &types.Task{
		ID:        "task-ipv6-mapped",
		ServiceID: "service-1",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "net-ipv6",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "test-br-ipv6-mapped",
							},
						},
					},
				},
				Addresses: []string{"::ffff:192.0.2.1/128"},
			},
		},
	}

	err := nm.PrepareNetwork(context.Background(), task)
	_ = err
}

func TestNetworkManager_ensureBridge_MultipleBridges(t *testing.T) {
	bridges := []string{"test-br-multi-1", "test-br-multi-2", "test-br-multi-3"}

	for _, bridge := range bridges {
		nm := NewNetworkManager(types.NetworkConfig{
			BridgeName: bridge,
		}).(*NetworkManager)

		err := nm.ensureBridge(context.Background())
		// May fail due to permissions, but tests the function
		_ = err
	}
}

func TestNetworkManager_createTapDevice_WithIPv4CIDRVariations(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{
		BridgeName: "test-br-cidr",
	}).(*NetworkManager)

	cidrs := []string{
		"192.168.1.1/32",
		"10.0.0.1/8",
		"172.16.0.1/12",
		"192.168.0.1/16",
	}

	for i, cidr := range cidrs {
		network := types.NetworkAttachment{
			Network: types.Network{
				ID: "net-cidr",
				Spec: types.NetworkSpec{
					DriverConfig: &types.DriverConfig{
						Bridge: &types.BridgeConfig{
							Name: "test-br-cidr",
						},
					},
				},
			},
			Addresses: []string{cidr},
		}

		_, err := nm.createTapDevice(context.Background(), network, i, "test-task")
		_ = err
	}
}

func TestNetworkManager_createTapDevice_LargeInterfaceIndex(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{
		BridgeName: "test-br-large-index",
	}).(*NetworkManager)

	network := types.NetworkAttachment{
		Network: types.Network{
			ID: "net-large",
			Spec: types.NetworkSpec{
				DriverConfig: &types.DriverConfig{
					Bridge: &types.BridgeConfig{
						Name: "test-br-large-index",
					},
				},
			},
		},
	}

	// Test with large interface index
	_, err := nm.createTapDevice(context.Background(), network, 999, "test-task")
	_ = err
}

func TestNetworkManager_removeTapDevice_Concurrent(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{
		BridgeName: "test-br-concurrent-remove",
	}).(*NetworkManager)

	// Add some TAP devices
	nm.mu.Lock()
	nm.tapDevices["task-1-tap-test"] = &TapDevice{
		Name:   "tap-test",
		Bridge: "test-br-concurrent-remove",
	}
	nm.mu.Unlock()

	var wg sync.WaitGroup
	// Concurrently try to remove the same device
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task := &types.Task{ID: "task-1"}
			_ = nm.CleanupNetwork(context.Background(), task)
		}()
	}
	wg.Wait()
}

// DEPRECATED: Bridge IP setup is now handled automatically via ensureBridge
/*
func TestNetworkManager_SetupBridgeIP_MultipleIPs(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{
		BridgeName: "test-br-multi-ip",
	}).(*NetworkManager)

	ips := []struct {
		ip      string
		netmask string
	}{
		{"192.168.1.1", "/24"},
		{"10.0.0.1", "/8"},
		{"172.16.0.1", "/12"},
	}

	for _, ip := range ips {
		err := nm.SetupBridgeIP(context.Background(), ip.ip, ip.netmask)
		_ = err
	}
}
*/

func TestNetworkManager_ListTapDevices_ConcurrentAccess(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{
		BridgeName: "test-br-concurrent-list",
	}).(*NetworkManager)

	// Add some devices
	nm.mu.Lock()
	for i := 0; i < 10; i++ {
		nm.tapDevices["task-list-"+string(rune('0'+i))] = &TapDevice{
			Name:   "tap-test",
			Bridge: "test-br-concurrent-list",
		}
	}
	nm.mu.Unlock()

	var wg sync.WaitGroup
	// Concurrently read devices
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			devices := nm.ListTapDevices()
			assert.NotNil(t, devices)
		}()
	}
	wg.Wait()
}

func TestNetworkManager_CleanupNetwork_WithPartialCleanup(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{
		BridgeName: "test-br-partial-cleanup",
	}).(*NetworkManager)

	// Add multiple TAP devices for the same task
	nm.mu.Lock()
	nm.tapDevices["task-partial-tap-eth0"] = &TapDevice{
		Name:   "tap-eth0",
		Bridge: "test-br-partial-cleanup",
	}
	nm.tapDevices["task-partial-tap-eth1"] = &TapDevice{
		Name:   "tap-eth1",
		Bridge: "test-br-partial-cleanup",
	}
	// Add a device for a different task
	nm.tapDevices["task-other-tap-eth0"] = &TapDevice{
		Name:   "tap-eth0",
		Bridge: "test-br-partial-cleanup",
	}
	nm.mu.Unlock()

	// Cleanup only task-partial
	task := &types.Task{
		ID: "task-partial",
	}
	err := nm.CleanupNetwork(context.Background(), task)
	_ = err

	// Verify task-other devices still exist
	nm.mu.RLock()
	_, exists := nm.tapDevices["task-other-tap-eth0"]
	nm.mu.RUnlock()
	assert.True(t, exists, "Other task devices should still exist")
}

func TestTapDevice_ZeroValue(t *testing.T) {
	var tap TapDevice
	assert.Empty(t, tap.Name)
	assert.Empty(t, tap.Bridge)
	assert.Empty(t, tap.IP)
	assert.Empty(t, tap.Netmask)
}
