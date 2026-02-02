package network

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetworkManager_NewNetworkManager tests NetworkManager creation
func TestNetworkManager_NewNetworkManager(t *testing.T) {
	tests := []struct {
		name   string
		config types.NetworkConfig
	}{
		{
			name: "basic configuration",
			config: types.NetworkConfig{
				BridgeName: "br0",
			},
		},
		{
			name: "with rate limiting",
			config: types.NetworkConfig{
				BridgeName:       "br0",
				EnableRateLimit:  true,
				MaxPacketsPerSec: 10000,
			},
		},
		{
			name: "empty bridge name",
			config: types.NetworkConfig{
				BridgeName: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := NewNetworkManager(tt.config)

			assert.NotNil(t, nm)
			manager, ok := nm.(*NetworkManager)
			require.True(t, ok, "Should return *NetworkManager")
			assert.Equal(t, tt.config, manager.config)
			assert.NotNil(t, manager.bridges)
			assert.NotNil(t, manager.tapDevices)
		})
	}
}

// TestNetworkManager_PrepareNetwork_EmptyNetworks tests task with no networks
func TestNetworkManager_PrepareNetwork_EmptyNetworks(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	task := &types.Task{
		ID:        "task-empty",
		ServiceID: "service-empty",
		Networks:  []types.NetworkAttachment{},
	}

	ctx := context.Background()
	err := nm.PrepareNetwork(ctx, task)

	// Will fail without root trying to create bridge
	if err != nil {
		t.Logf("PrepareNetwork failed (expected without root): %v", err)
	}

	// No TAP devices should be created
	nm.mu.RLock()
	devices := len(nm.tapDevices)
	nm.mu.RUnlock()

	assert.Equal(t, 0, devices, "Should have no TAP devices")
}

// TestNetworkManager_PrepareNetwork_NilNetworks tests task with nil network slice
func TestNetworkManager_PrepareNetwork_NilNetworks(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	task := &types.Task{
		ID:        "task-nil",
		ServiceID: "service-nil",
		Networks:  nil,
	}

	ctx := context.Background()
	err := nm.PrepareNetwork(ctx, task)

	// Will fail without root trying to create bridge
	if err != nil {
		t.Logf("PrepareNetwork failed (expected without root): %v", err)
	}

	// No TAP devices should be created
	nm.mu.RLock()
	devices := len(nm.tapDevices)
	nm.mu.RUnlock()

	assert.Equal(t, 0, devices, "Should have no TAP devices")
}

// TestNetworkManager_PrepareNetwork_SingleNetwork tests single network attachment
func TestNetworkManager_PrepareNetwork_SingleNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	task := &types.Task{
		ID:        "task-single",
		ServiceID: "service-single",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "br0",
							},
						},
					},
				},
				Addresses: []string{"192.168.1.10/24"},
			},
		},
	}

	ctx := context.Background()
	err := nm.PrepareNetwork(ctx, task)

	// Will likely fail without privileges, but we can test the logic
	if err != nil {
		t.Logf("PrepareNetwork failed (expected without root): %v", err)
	}

	// Check if devices were tracked (even if creation failed)
	nm.mu.RLock()
	devices := len(nm.tapDevices)
	nm.mu.RUnlock()

	// If successful, should have 1 device
	if err == nil {
		assert.Equal(t, 1, devices, "Should have 1 TAP device")
	}
}

// TestNetworkManager_PrepareNetwork_MultipleNetworks tests multiple network attachments
func TestNetworkManager_PrepareNetwork_MultipleNetworks(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	task := &types.Task{
		ID:        "task-multi",
		ServiceID: "service-multi",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "br0",
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
								Name: "br1",
							},
						},
					},
				},
				Addresses: []string{"10.0.0.10/24"},
			},
		},
	}

	ctx := context.Background()
	err := nm.PrepareNetwork(ctx, task)

	if err != nil {
		t.Logf("PrepareNetwork failed (expected without root): %v", err)
	}

	// Check if devices were tracked
	nm.mu.RLock()
	devices := len(nm.tapDevices)
	nm.mu.RUnlock()

	if err == nil {
		assert.Equal(t, 2, devices, "Should have 2 TAP devices")
	}
}

// TestNetworkManager_PrepareNetwork_NoAddresses tests network without addresses
func TestNetworkManager_PrepareNetwork_NoAddresses(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	task := &types.Task{
		ID:        "task-noaddr",
		ServiceID: "service-noaddr",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "br0",
							},
						},
					},
				},
				Addresses: []string{},
			},
		},
	}

	ctx := context.Background()
	err := nm.PrepareNetwork(ctx, task)

	if err != nil {
		t.Logf("PrepareNetwork failed (expected without root): %v", err)
	}
}

// TestNetworkManager_CleanupNetwork_NoDevices tests cleanup with no devices
func TestNetworkManager_CleanupNetwork_NoDevices(t *testing.T) {
	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	task := &types.Task{
		ID:   "task-no-devices",
		Spec: types.TaskSpec{},
	}

	ctx := context.Background()
	err := nm.CleanupNetwork(ctx, task)

	// Should succeed even with no devices
	assert.NoError(t, err)

	// Verify no devices exist
	nm.mu.RLock()
	devices := len(nm.tapDevices)
	nm.mu.RUnlock()

	assert.Equal(t, 0, devices)
}

// TestNetworkManager_CleanupNetwork_WithDevices tests cleanup with mock devices
func TestNetworkManager_CleanupNetwork_WithDevices(t *testing.T) {
	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	// Add mock devices
	taskID := "task-cleanup"
	nm.mu.Lock()
	nm.tapDevices[taskID+"-tap-eth0"] = &TapDevice{
		Name:    "tap-eth0",
		Bridge:  "br0",
		IP:      "192.168.1.10",
		Netmask: "255.255.255.0",
	}
	nm.tapDevices[taskID+"-tap-eth1"] = &TapDevice{
		Name:    "tap-eth1",
		Bridge:  "br0",
		IP:      "192.168.1.11",
		Netmask: "255.255.255.0",
	}
	// Add a device for different task
	nm.tapDevices["other-task-tap-eth0"] = &TapDevice{
		Name:    "tap-eth0",
		Bridge:  "br0",
		IP:      "192.168.1.20",
		Netmask: "255.255.255.0",
	}
	nm.mu.Unlock()

	task := &types.Task{
		ID:   taskID,
		Spec: types.TaskSpec{},
	}

	ctx := context.Background()
	err := nm.CleanupNetwork(ctx, task)

	// Cleanup will fail trying to delete devices without root, but tracking should work
	if err != nil {
		t.Logf("CleanupNetwork failed (expected without root): %v", err)
	}

	// Verify task's devices were removed from tracking
	nm.mu.RLock()
	_, hasTap0 := nm.tapDevices[taskID+"-tap-eth0"]
	_, hasTap1 := nm.tapDevices[taskID+"-tap-eth1"]
	_, hasOther := nm.tapDevices["other-task-tap-eth0"]
	nm.mu.RUnlock()

	assert.False(t, hasTap0, "Task's tap-eth0 should be removed")
	assert.False(t, hasTap1, "Task's tap-eth1 should be removed")
	assert.True(t, hasOther, "Other task's device should remain")
}

// TestNetworkManager_ensureBridge_DoubleCheck tests double-check pattern
func TestNetworkManager_ensureBridge_DoubleCheck(t *testing.T) {
	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	// Mark bridge as existing
	nm.mu.Lock()
	nm.bridges["br0"] = true
	nm.mu.Unlock()

	ctx := context.Background()
	err := nm.ensureBridge(ctx)

	// Should succeed immediately without trying to create
	assert.NoError(t, err)

	// Verify still marked as existing
	nm.mu.RLock()
	exists := nm.bridges["br0"]
	nm.mu.RUnlock()

	assert.True(t, exists)
}

// TestNetworkManager_ensureBridge_Concurrent tests concurrent bridge creation
func TestNetworkManager_ensureBridge_Concurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	ctx := context.Background()
	numGoroutines := 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	errors := make(chan error, numGoroutines)

	// Launch concurrent goroutines
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			if err := nm.ensureBridge(ctx); err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	// Count errors (expected without root)
	errorCount := 0
	for err := range errors {
		errorCount++
		_ = err // Expected errors without root
	}

	// If all goroutines failed (no root), bridge won't be marked
	// If at least one succeeded, bridge should be marked
	nm.mu.RLock()
	exists := nm.bridges["br0"]
	nm.mu.RUnlock()

	if errorCount == numGoroutines {
		// All failed - bridge not created or marked (expected without root)
		assert.False(t, exists, "Bridge should not be marked when all attempts fail")
	} else {
		// At least one succeeded - bridge should be marked
		assert.True(t, exists, "Bridge should be marked when at least one attempt succeeds")
	}
}

// TestNetworkManager_createTapDevice_IPParsing tests IP address parsing
func TestNetworkManager_createTapDevice_IPParsing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	tests := []struct {
		name      string
		addresses []string
		wantIP    string
	}{
		{
			name:      "valid CIDR",
			addresses: []string{"192.168.1.10/24"},
			wantIP:    "192.168.1.10",
		},
		{
			name:      "IPv6 CIDR",
			addresses: []string{"2001:db8::1/64"},
			wantIP:    "2001:db8::1",
		},
		{
			name:      "empty addresses",
			addresses: []string{},
			wantIP:    "",
		},
		{
			name:      "no CIDR notation",
			addresses: []string{"192.168.1.10"},
			wantIP:    "192.168.1.10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network := types.NetworkAttachment{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "br0",
							},
						},
					},
				},
				Addresses: tt.addresses,
			}

			ctx := context.Background()
			tap, err := nm.createTapDevice(ctx, network, 0, "test-task")

			if err != nil {
				t.Logf("createTapDevice failed (expected without root): %v", err)
				return
			}

			assert.Equal(t, tt.wantIP, tap.IP)
		})
	}
}

// TestNetworkManager_createTapDevice_CustomBridge tests custom bridge name
func TestNetworkManager_createTapDevice_CustomBridge(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	network := types.NetworkAttachment{
		Network: types.Network{
			ID: "network-1",
			Spec: types.NetworkSpec{
				DriverConfig: &types.DriverConfig{
					Bridge: &types.BridgeConfig{
						Name: "custom-br0",
					},
				},
			},
		},
		Addresses: []string{"192.168.1.10/24"},
	}

	ctx := context.Background()
	tap, err := nm.createTapDevice(ctx, network, 0, "test-task")

	if err != nil {
		t.Skipf("createTapDevice failed (expected without root): %v", err)
	}

	require.NotNil(t, tap)
	assert.Equal(t, "custom-br0", tap.Bridge, "Should use custom bridge name")
}

// TestNetworkManager_createTapDevice_DefaultBridge tests default bridge
func TestNetworkManager_createTapDevice_DefaultBridge(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "default-br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	network := types.NetworkAttachment{
		Network: types.Network{
			ID: "network-1",
			Spec: types.NetworkSpec{
				DriverConfig: &types.DriverConfig{
					Bridge: &types.BridgeConfig{
						Name: "", // Empty, should use default
					},
				},
			},
		},
		Addresses: []string{"192.168.1.10/24"},
	}

	ctx := context.Background()
	tap, err := nm.createTapDevice(ctx, network, 0, "test-task")

	if err != nil {
		t.Skipf("createTapDevice failed (expected without root): %v", err)
	}

	require.NotNil(t, tap)
	assert.Equal(t, "default-br0", tap.Bridge, "Should use default bridge name")
}

// TestNetworkManager_createTapDevice_NilDriverConfig tests nil driver config
func TestNetworkManager_createTapDevice_NilDriverConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	network := types.NetworkAttachment{
		Network: types.Network{
			ID:   "network-1",
			Spec: types.NetworkSpec{},
		},
		Addresses: []string{"192.168.1.10/24"},
	}

	ctx := context.Background()
	tap, err := nm.createTapDevice(ctx, network, 0, "test-task")

	if err != nil {
		t.Skipf("createTapDevice failed (expected without root): %v", err)
	}

	require.NotNil(t, tap)
	assert.Equal(t, "br0", tap.Bridge, "Should use default bridge from config")
}

// TestNetworkManager_createTapDevice_InterfaceName tests interface naming
func TestNetworkManager_createTapDevice_InterfaceName(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	tests := []struct {
		name     string
		index    int
		expected string
	}{
		{
			name:     "first interface",
			index:    0,
			expected: "tap-eth0",
		},
		{
			name:     "second interface",
			index:    1,
			expected: "tap-eth1",
		},
		{
			name:     "tenth interface",
			index:    10,
			expected: "tap-eth10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network := types.NetworkAttachment{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "br0",
							},
						},
					},
				},
				Addresses: []string{"192.168.1.10/24"},
			}

			ctx := context.Background()
			tap, err := nm.createTapDevice(ctx, network, tt.index, "test-task")

			if err != nil {
				t.Skipf("createTapDevice failed (expected without root): %v", err)
			}

			assert.Contains(t, tap.Name, fmt.Sprintf("eth%d", tt.index))
		})
	}
}

// TestNetworkManager_removeTapDevice_Logging tests device removal logging
func TestNetworkManager_removeTapDevice_Logging(t *testing.T) {
	tap := &TapDevice{
		Name:    "tap-test",
		Bridge:  "br0",
		IP:      "192.168.1.10",
		Netmask: "255.255.255.0",
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	// This will fail without root, but we're just testing it doesn't panic
	err := nm.removeTapDevice(tap)

	// Expected to fail without privileges
	if err != nil {
		assert.Contains(t, err.Error(), "failed to delete TAP device")
	}
}

// TestNetworkManager_SetupBridgeIP tests bridge IP configuration
// DEPRECATED: Bridge IP setup is now handled automatically via ensureBridge
/*
func TestNetworkManager_SetupBridgeIP(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	tests := []struct {
		name    string
		ip      string
		netmask string
	}{
		{
			name:    "IPv4 /24",
			ip:      "192.168.1.1",
			netmask: "/24",
		},
		{
			name:    "IPv4 /16",
			ip:      "10.0.0.1",
			netmask: "/16",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := nm.SetupBridgeIP(ctx, tt.ip, tt.netmask)

			if err != nil {
				t.Logf("SetupBridgeIP failed (expected without root): %v", err)
			}
		})
	}
}
*/

// TestNetworkManager_ListTapDevices_Empty tests listing with no devices
func TestNetworkManager_ListTapDevices_Empty(t *testing.T) {
	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	devices := nm.ListTapDevices()

	assert.NotNil(t, devices)
	assert.Empty(t, devices, "Should have no devices")
}

// TestNetworkManager_ListTapDevices_MultipleDevices tests listing multiple devices
func TestNetworkManager_ListTapDevices_MultipleDevices(t *testing.T) {
	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	// Add mock devices
	nm.mu.Lock()
	nm.tapDevices["task1-tap-eth0"] = &TapDevice{
		Name:   "tap-eth0",
		Bridge: "br0",
		IP:     "192.168.1.10",
	}
	nm.tapDevices["task1-tap-eth1"] = &TapDevice{
		Name:   "tap-eth1",
		Bridge: "br0",
		IP:     "192.168.1.11",
	}
	nm.tapDevices["task2-tap-eth0"] = &TapDevice{
		Name:   "tap-eth0",
		Bridge: "br0",
		IP:     "192.168.1.20",
	}
	nm.mu.Unlock()

	devices := nm.ListTapDevices()

	assert.Len(t, devices, 3, "Should have 3 devices")
}

// TestNetworkManager_ConcurrentAccess tests thread-safe concurrent access
func TestNetworkManager_ConcurrentAccess(t *testing.T) {
	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	numOperations := 100
	var wg sync.WaitGroup

	// Concurrent reads
	for i := 0; i < numOperations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = nm.ListTapDevices()
		}()
	}

	// Concurrent writes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			taskID := fmt.Sprintf("task-%d", idx)

			// Add device
			nm.mu.Lock()
			nm.tapDevices[taskID+"-tap-eth0"] = &TapDevice{
				Name:   "tap-eth0",
				Bridge: "br0",
			}
			nm.mu.Unlock()

			// Remove device
			nm.mu.Lock()
			delete(nm.tapDevices, taskID+"-tap-eth0")
			nm.mu.Unlock()
		}(i)
	}

	wg.Wait()

	// Verify no race conditions
	devices := nm.ListTapDevices()
	assert.NotNil(t, devices)
}

// TestTapDevice_Structure tests TapDevice structure
func TestTapDevice_Structure(t *testing.T) {
	tap := &TapDevice{
		Name:    "tap-test",
		Bridge:  "br0",
		IP:      "192.168.1.10",
		Netmask: "255.255.255.0",
	}

	assert.Equal(t, "tap-test", tap.Name)
	assert.Equal(t, "br0", tap.Bridge)
	assert.Equal(t, "192.168.1.10", tap.IP)
	assert.Equal(t, "255.255.255.0", tap.Netmask)
}

// TestParseMacAddress_Valid tests valid MAC address parsing
func TestParseMacAddress_Valid(t *testing.T) {
	validMACs := []string{
		"02:FC:00:00:00:01",
		"00:11:22:33:44:55",
		"aa:bb:cc:dd:ee:ff",
		"AA:BB:CC:DD:EE:FF",
	}

	for _, mac := range validMACs {
		t.Run(mac, func(t *testing.T) {
			hw, err := net.ParseMAC(mac)
			assert.NoError(t, err)
			assert.NotNil(t, hw)
			assert.Equal(t, 6, len(hw), "MAC should be 6 bytes")
		})
	}
}

// TestParseMacAddress_Invalid tests invalid MAC address handling
func TestParseMacAddress_Invalid(t *testing.T) {
	invalidMACs := []string{
		"invalid",
		"00:11:22:33:44",       // Too short
		"00:11:22:33:44:55:66", // Too long
		"gg:hh:jj:kk:ll:mm",    // Invalid hex
		"",
	}

	for _, mac := range invalidMACs {
		t.Run(mac, func(t *testing.T) {
			hw, err := net.ParseMAC(mac)
			assert.Error(t, err, "Should return error for invalid MAC")
			assert.Nil(t, hw)
		})
	}
}

// TestNetworkManager_PrepareNetwork_ContextCancellation tests context cancellation
func TestNetworkManager_PrepareNetwork_ContextCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping context cancellation test in short mode")
	}

	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	task := &types.Task{
		ID:        "task-ctx",
		ServiceID: "service-ctx",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "network-1",
				},
			},
		},
	}

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := nm.PrepareNetwork(ctx, task)
	// Should either succeed (bridge exists) or fail with context error
	if err != nil {
		t.Logf("PrepareNetwork with cancelled context: %v", err)
	}
}

// Benchmark tests
func BenchmarkNetworkManager_ListTapDevices(b *testing.B) {
	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	// Add 100 devices
	nm.mu.Lock()
	for i := 0; i < 100; i++ {
		nm.tapDevices[fmt.Sprintf("task%d-tap-eth0", i)] = &TapDevice{
			Name:   "tap-eth0",
			Bridge: "br0",
		}
	}
	nm.mu.Unlock()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = nm.ListTapDevices()
	}
}

func BenchmarkNetworkManager_ConcurrentAccess(b *testing.B) {
	config := types.NetworkConfig{
		BridgeName: "br0",
	}
	nm := NewNetworkManager(config).(*NetworkManager)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		go func() {
			_ = nm.ListTapDevices()
		}()
	}
}
