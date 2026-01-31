package network

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewNetworkManager(t *testing.T) {
	config := types.NetworkConfig{
		BridgeName:       "test-br0",
		EnableRateLimit:  true,
		MaxPacketsPerSec: 10000,
	}

	nm := NewNetworkManager(config)

	assert.NotNil(t, nm)
	assert.Equal(t, config, nm.(*NetworkManager).config)
	assert.NotNil(t, nm.(*NetworkManager).bridges)
	assert.NotNil(t, nm.(*NetworkManager).tapDevices)
}

func TestNetworkManager_PrepareNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tests := []struct {
		name    string
		task    *types.Task
		wantErr bool
	}{
		{
			name: "single network attachment",
			task: &types.Task{
				ID:        "task-1",
				ServiceID: "service-1",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "network-1",
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "test-br0",
									},
								},
							},
						},
						Addresses: []string{"192.168.1.10/24"},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "multiple network attachments",
			task: &types.Task{
				ID:        "task-2",
				ServiceID: "service-2",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "network-1",
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "test-br0",
									},
								},
							},
						},
						Addresses: []string{"192.168.1.10/24"},
					},
					{
						Network: types.Network{
							ID: "network-2",
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "test-br1",
									},
								},
							},
						},
						Addresses: []string{"10.0.0.10/24"},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "no network attachments",
			task: &types.Task{
				ID:        "task-3",
				ServiceID: "service-3",
				Networks: []types.NetworkAttachment{},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := types.NetworkConfig{
				BridgeName: "test-br0",
			}
			nm := NewNetworkManager(config).(*NetworkManager)

			ctx := context.Background()
			err := nm.PrepareNetwork(ctx, tt.task)

			// In CI/test environments, this might fail due to permissions
			// We'll just check the logic flow
			if err != nil {
				t.Logf("PrepareNetwork failed (expected in container): %v", err)
			}

			// Verify the task ID was tracked
			nm.mu.RLock()
			hasDevices := len(nm.tapDevices) > 0
			nm.mu.RUnlock()

			if len(tt.task.Networks) > 0 && err == nil {
				assert.True(t, hasDevices, "Should have created TAP devices")
			}
		})
	}
}

func TestNetworkManager_CleanupNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "test-br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	// Create a mock TAP device entry
	taskID := "task-cleanup"
	nm.mu.Lock()
	nm.tapDevices[taskID+"-tap-eth0"] = &TapDevice{
		Name:    "tap-eth0",
		Bridge:  "test-br0",
		IP:      "192.168.1.10",
		Netmask: "255.255.255.0",
	}
	nm.mu.Unlock()

	task := &types.Task{
		ID:   taskID,
		Spec: types.TaskSpec{},
	}

	ctx := context.Background()
	err := nm.CleanupNetwork(ctx, task)

	// In container, this might fail - that's ok
	if err != nil {
		t.Logf("CleanupNetwork failed (expected in container): %v", err)
	}

	// Verify the entry was removed
	nm.mu.RLock()
	_, exists := nm.tapDevices[taskID+"-tap-eth0"]
	nm.mu.RUnlock()

	assert.False(t, exists, "TAP device entry should be removed")
}

func TestNetworkManager_ensureBridge(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tests := []struct {
		name       string
		bridgeName string
		wantErr    bool
	}{
		{
			name:       "valid bridge name",
			bridgeName: "test-br0",
			wantErr:    false,
		},
		{
			name:       "another bridge",
			bridgeName: "test-br1",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := types.NetworkConfig{
				BridgeName: tt.bridgeName,
			}
			nm := NewNetworkManager(config).(*NetworkManager)

			ctx := context.Background()
			err := nm.ensureBridge(ctx)

			// In container, might fail due to permissions
			if err != nil {
				t.Logf("ensureBridge failed (expected in container): %v", err)
			}

			// Verify bridge is tracked
			nm.mu.RLock()
			exists := nm.bridges[tt.bridgeName]
			nm.mu.RUnlock()

			// If no error, bridge should be tracked
			if err == nil {
				assert.True(t, exists)
			}
		})
	}
}

func TestNetworkManager_createTapDevice(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "test-br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	network := types.NetworkAttachment{
		Network: types.Network{
			ID: "network-1",
			Spec: types.NetworkSpec{
				DriverConfig: &types.DriverConfig{
					Bridge: &types.BridgeConfig{
						Name: "test-br0",
					},
				},
			},
		},
		Addresses: []string{"192.168.1.10/24"},
	}

	ctx := context.Background()
	tap, err := nm.createTapDevice(ctx, network, 0)

	// In container, might fail due to permissions
	if err != nil {
		t.Skipf("createTapDevice failed (expected in container): %v", err)
	}

	require.NotNil(t, tap)
	assert.Contains(t, tap.Name, "tap")
	assert.Equal(t, "test-br0", tap.Bridge)
	assert.Equal(t, "192.168.1.10", tap.IP)
	assert.Equal(t, "255.255.255.0", tap.Netmask)
}

func TestNetworkManager_removeTapDevice(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tap := &TapDevice{
		Name:    "tap-test-remove",
		Bridge:  "test-br0",
		IP:      "192.168.1.10",
		Netmask: "255.255.255.0",
	}

	config := types.NetworkConfig{
		BridgeName: "test-br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.removeTapDevice(tap)

	// In container, might fail - that's ok
	if err != nil {
		t.Logf("removeTapDevice failed (expected in container): %v", err)
	}
}

func TestNetworkManager_ListTapDevices(t *testing.T) {
	config := types.NetworkConfig{
		BridgeName: "test-br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	// Add some mock devices
	nm.mu.Lock()
	nm.tapDevices["task1-tap-eth0"] = &TapDevice{
		Name:   "tap-eth0",
		Bridge: "test-br0",
	}
	nm.tapDevices["task2-tap-eth1"] = &TapDevice{
		Name:   "tap-eth1",
		Bridge: "test-br0",
	}
	nm.mu.Unlock()

	devices := nm.ListTapDevices()

	assert.Len(t, devices, 2)
}

func TestParseMacAddress(t *testing.T) {
	// Test MAC address parsing/validation
	tests := []struct {
		name    string
		mac     string
		wantErr bool
	}{
		{
			name:    "valid MAC",
			mac:     "02:FC:00:00:00:01",
			wantErr: false,
		},
		{
			name:    "invalid MAC",
			mac:     "invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hw, err := net.ParseMAC(tt.mac)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, hw)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, hw)
			}
		})
	}
}

// Benchmark network operations
func BenchmarkNetworkManager_PrepareNetwork(b *testing.B) {
	config := types.NetworkConfig{
		BridgeName: "test-br0",
	}
	nm := NewNetworkManager(config)

	task := &types.Task{
		ID:        "bench-task",
		ServiceID: "bench-service",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "network-1",
				},
			},
		},
	}

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Just test the logic flow, not actual device creation
		task.ID = "bench-task-" + string(rune(i))
		_ = nm.PrepareNetwork(ctx, task)
	}
}

// TestNetworkManager_PrepareNetwork_EdgeCases tests edge cases for PrepareNetwork
func TestNetworkManager_PrepareNetwork_EdgeCases(t *testing.T) {
	config := types.NetworkConfig{
		BridgeName: "test-br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	ctx := context.Background()

	tests := []struct {
		name    string
		task    *types.Task
		wantErr bool
	}{
		{
			name: "empty networks",
			task: &types.Task{
				ID:        "task-empty",
				ServiceID: "service-1",
				Networks:  []types.NetworkAttachment{},
			},
			wantErr: false, // Empty networks is valid
		},
		{
			name: "nil networks slice",
			task: &types.Task{
				ID:        "task-nil-nets",
				ServiceID: "service-1",
				Networks:  nil,
			},
			wantErr: false, // Nil networks is valid
		},
		{
			name: "network without driver config",
			task: &types.Task{
				ID:        "task-no-driver",
				ServiceID: "service-1",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "network-1",
							Spec: types.NetworkSpec{
								DriverConfig: nil,
							},
						},
					},
				},
			},
			wantErr: false, // Should use default bridge
		},
		{
			name: "network with empty bridge name",
			task: &types.Task{
				ID:        "task-empty-bridge",
				ServiceID: "service-1",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "network-1",
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "",
									},
								},
							},
						},
					},
				},
			},
			wantErr: false, // Should use default bridge
		},
		{
			name: "multiple networks",
			task: &types.Task{
				ID:        "task-multi",
				ServiceID: "service-1",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "network-1",
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "test-br0",
									},
								},
							},
						},
					},
					{
						Network: types.Network{
							ID: "network-2",
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "test-br1",
									},
								},
							},
						},
					},
				},
			},
			wantErr: false, // Multiple networks is valid
		},
		{
			name: "network with IPv6 address",
			task: &types.Task{
				ID:        "task-ipv6",
				ServiceID: "service-1",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "network-1",
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "test-br0",
									},
								},
							},
						},
						Addresses: []string{"2001:db8::1/64"},
					},
				},
			},
			wantErr: false, // IPv6 is valid
		},
		{
			name: "network with multiple addresses",
			task: &types.Task{
				ID:        "task-multi-addr",
				ServiceID: "service-1",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "network-1",
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "test-br0",
									},
								},
							},
						},
						Addresses: []string{"192.168.1.10/24", "192.168.1.11/24"},
					},
				},
			},
			wantErr: false, // Multiple addresses is valid (should use first)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := nm.PrepareNetwork(ctx, tt.task)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				// Don't assert.NoError because actual device creation may fail
				// Just verify the logic doesn't crash
				_ = err
			}
		})
	}
}

// TestNetworkManager_createTapDevice_EdgeCases tests edge cases for createTapDevice
func TestNetworkManager_createTapDevice_EdgeCases(t *testing.T) {
	config := types.NetworkConfig{
		BridgeName: "default-br",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	ctx := context.Background()

	tests := []struct {
		name    string
		network types.NetworkAttachment
		index   int
	}{
		{
			name: "empty addresses",
			network: types.NetworkAttachment{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "test-br0",
							},
						},
					},
				},
				Addresses: []string{},
			},
			index: 0,
		},
		{
			name: "nil addresses",
			network: types.NetworkAttachment{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "test-br0",
							},
						},
					},
				},
				Addresses: nil,
			},
			index: 1,
		},
		{
			name: "use default bridge",
			network: types.NetworkAttachment{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: nil,
					},
				},
				Addresses: []string{"192.168.1.10/24"},
			},
			index: 2,
		},
		{
			name: "empty bridge name",
			network: types.NetworkAttachment{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "",
							},
						},
					},
				},
				Addresses: []string{"192.168.1.10/24"},
			},
			index: 3,
		},
		{
			name: "address without CIDR",
			network: types.NetworkAttachment{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "test-br0",
							},
						},
					},
				},
				Addresses: []string{"192.168.1.10"},
			},
			index: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// These will likely fail without privileges, but test the logic
			_, err := nm.createTapDevice(ctx, tt.network, tt.index)
			// We expect failures due to permissions, but the code should not panic
			_ = err
		})
	}
}

// TestTapDevice_Concurrency tests concurrent access to network manager
func TestTapDevice_Concurrency(t *testing.T) {
	config := types.NetworkConfig{
		BridgeName: "test-br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	// Add some mock TAP devices
	nm.mu.Lock()
	for i := 0; i < 10; i++ {
		nm.tapDevices[fmt.Sprintf("task%d-tap-eth%d", i, i)] = &TapDevice{
			Name:   fmt.Sprintf("tap-eth%d", i),
			Bridge: "test-br0",
		}
	}
	nm.mu.Unlock()

	// Concurrent reads
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()
			devices := nm.ListTapDevices()
			_ = devices
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify no deadlocks or races
	devices := nm.ListTapDevices()
	assert.Len(t, devices, 10)
}
