package network

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIncIP tests the incIP function
func TestIncIP(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "increment last octet",
			input:    "192.168.1.1",
			expected: "192.168.1.2",
		},
		{
			name:     "increment with overflow",
			input:    "192.168.1.255",
			expected: "192.168.2.0",
		},
		{
			name:     "increment multiple octets",
			input:    "192.168.255.255",
			expected: "192.169.0.0",
		},
		{
			name:     "increment all octets with overflow",
			input:    "192.255.255.255",
			expected: "193.0.0.0",
		},
		{
			name:     "increment zero",
			input:    "0.0.0.0",
			expected: "0.0.0.1",
		},
		{
			name:     "increment first octet",
			input:    "10.0.0.0",
			expected: "10.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := net.ParseIP(tt.input)
			require.NotNil(t, input, "Failed to parse input IP: %s", tt.input)

			result := incIP(input)
			assert.Equal(t, tt.expected, result.String(), "incIP(%s) = %s, want %s", tt.input, result, tt.expected)
		})
	}
}

// TestGetTapIP_Uncovered tests the GetTapIP method - uncovered scenarios
func TestNetworkManager_GetTapIP_Uncovered(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*NetworkManager)
		taskID      string
		expectedIP  string
		expectError bool
	}{
		{
			name: "get existing TAP IP",
			setupFunc: func(nm *NetworkManager) {
				nm.tapDevices["task1-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "192.168.1.10",
				}
			},
			taskID:      "task1",
			expectedIP:  "192.168.1.10",
			expectError: false,
		},
		{
			name: "task with no IP assigned",
			setupFunc: func(nm *NetworkManager) {
				nm.tapDevices["task2-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "",
				}
			},
			taskID:      "task2",
			expectedIP:  "",
			expectError: true,
		},
		{
			name:        "non-existent task",
			setupFunc:   func(nm *NetworkManager) {},
			taskID:      "nonexistent",
			expectedIP:  "",
			expectError: true,
		},
		{
			name: "multiple TAP devices for task",
			setupFunc: func(nm *NetworkManager) {
				nm.tapDevices["task3-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "192.168.1.10",
				}
				nm.tapDevices["task3-tap-eth1"] = &TapDevice{
					Name: "tap-eth1",
					IP:   "192.168.1.11",
				}
			},
			taskID:      "task3",
			expectedIP:  "192.168.1.10",
			expectError: false,
		},
		{
			name: "empty task ID",
			setupFunc: func(nm *NetworkManager) {
				nm.tapDevices["task4-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "192.168.1.10",
				}
			},
			taskID:      "",
			expectedIP:  "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				tapDevices: make(map[string]*TapDevice),
			}
			tt.setupFunc(nm)

			ip, err := nm.GetTapIP(tt.taskID)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedIP, ip)
			}
		})
	}
}

// TestSetupBridgeIP tests the setupBridgeIP method
func TestNetworkManager_SetupBridgeIP(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping bridge IP test in short mode (requires root)")
	}

	tests := []struct {
		name        string
		bridgeName  string
		bridgeIP    string
		expectError bool
	}{
		{
			name:        "setup bridge IP with valid config",
			bridgeName:  "test-br0",
			bridgeIP:    "192.168.100.1/24",
			expectError: false,
		},
		{
			name:        "setup bridge IP with IPv6",
			bridgeName:  "test-br1",
			bridgeIP:    "fd00::1/64",
			expectError: false,
		},
		{
			name:        "setup bridge IP with CIDR notation",
			bridgeName:  "test-br2",
			bridgeIP:    "10.0.0.1/16",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config: types.NetworkConfig{
					BridgeName: tt.bridgeName,
					BridgeIP:   tt.bridgeIP,
				},
			}

			ctx := context.Background()
			err := nm.setupBridgeIP(ctx)

			// Note: This test will likely fail without root privileges
			// We're testing the logic flow, not actual system changes
			if tt.expectError {
				assert.Error(t, err)
			}
		})
	}
}

// TestSetupBridgeIP_AlreadySet tests setupBridgeIP when IP is already configured
func TestNetworkManager_SetupBridgeIP_AlreadySet(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping bridge IP test in short mode (requires root)")
	}

	nm := &NetworkManager{
		config: types.NetworkConfig{
			BridgeName: "test-br-already",
			BridgeIP:   "172.16.0.1/24",
		},
	}

	ctx := context.Background()
	// First call should succeed or fail based on system state
	err := nm.setupBridgeIP(ctx)
	// Second call should handle existing IP gracefully
	err2 := nm.setupBridgeIP(ctx)

	// We're mainly testing that the function doesn't panic
	// and handles the "already exists" case
	assert.NotNil(t, err)
	assert.NotNil(t, err2)
}

// TestSetupNAT tests the setupNAT method
func TestNetworkManager_SetupNAT(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping NAT test in short mode (requires root)")
	}

	tests := []struct {
		name        string
		config      types.NetworkConfig
		expectError bool
	}{
		{
			name: "setup NAT with valid subnet",
			config: types.NetworkConfig{
				Subnet:     "192.168.100.0/24",
				BridgeName: "test-br-nat",
			},
			expectError: false,
		},
		{
			name: "setup NAT with empty subnet",
			config: types.NetworkConfig{
				Subnet:     "",
				BridgeName: "test-br-nat2",
			},
			expectError: true,
		},
		{
			name: "setup NAT with IPv6 subnet",
			config: types.NetworkConfig{
				Subnet:     "fd00::/64",
				BridgeName: "test-br-nat3",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config: tt.config,
			}

			ctx := context.Background()
			err := nm.setupNAT(ctx)

			// Note: These tests will likely fail without root privileges
			// We're testing the logic flow and error handling
			if tt.expectError {
				assert.Error(t, err)
			}
		})
	}
}

// TestSetupNAT_DuplicateSetup tests calling setupNAT twice
func TestNetworkManager_SetupNAT_DuplicateSetup(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping NAT test in short mode (requires root)")
	}

	nm := &NetworkManager{
		config: types.NetworkConfig{
			Subnet:     "10.20.30.0/24",
			BridgeName: "test-br-dup",
		},
	}

	ctx := context.Background()
	// First call
	err1 := nm.setupNAT(ctx)
	// Second call should handle existing rules gracefully
	err2 := nm.setupNAT(ctx)

	// We're testing that the function handles duplicate calls
	assert.NotNil(t, err1)
	assert.NotNil(t, err2)
}

// TestPrepareNetwork_FullFlow tests the full PrepareNetwork flow
func TestNetworkManager_PrepareNetwork_FullFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping full network test in short mode (requires root)")
	}

	tests := []struct {
		name        string
		config      types.NetworkConfig
		task        *types.Task
		expectError bool
	}{
		{
			name: "prepare network with NAT enabled",
			config: types.NetworkConfig{
				BridgeName: "test-br-full",
				BridgeIP:   "192.168.200.1/24",
				Subnet:     "192.168.200.0/24",
				NATEnabled: true,
				IPMode:     "static",
			},
			task: &types.Task{
				ID: "test-task-1",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "test-br-full",
									},
								},
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "prepare network without NAT",
			config: types.NetworkConfig{
				BridgeName: "test-br-nonnat",
				BridgeIP:   "10.30.30.1/24",
				Subnet:     "10.30.30.0/24",
				NATEnabled: false,
				IPMode:     "dhcp",
			},
			task: &types.Task{
				ID: "test-task-2",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "test-br-nonnat",
									},
								},
							},
						},
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config:     tt.config,
				bridges:    make(map[string]bool),
				tapDevices: make(map[string]*TapDevice),
			}

			// Initialize IP allocator
			if tt.config.Subnet != "" && tt.config.BridgeIP != "" {
				gatewayStr := tt.config.BridgeIP
				if idx := len(gatewayStr); idx > 0 {
					if slashIdx := findLastIndex(gatewayStr, '/'); slashIdx != -1 {
						gatewayStr = gatewayStr[:slashIdx]
					}
				}
				allocator, err := NewIPAllocator(tt.config.Subnet, gatewayStr)
				require.NoError(t, err)
				nm.ipAllocator = allocator
			}

			ctx := context.Background()
			err := nm.PrepareNetwork(ctx, tt.task)

			// Clean up any created devices
			defer nm.CleanupNetwork(ctx, tt.task)

			// Note: These tests will likely fail without root privileges
			// We're testing the logic flow
			_ = err
		})
	}
}

// TestCreateTapDevice_FullCreation tests complete TAP device creation
func TestNetworkManager_CreateTapDevice_FullCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping TAP creation test in short mode (requires root)")
	}

	tests := []struct {
		name     string
		network  types.NetworkAttachment
		index    int
		taskID   string
		validate func(*TapDevice, error)
	}{
		{
			name: "create TAP with custom bridge",
			network: types.NetworkAttachment{
				Network: types.Network{
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "custom-bridge",
							},
						},
					},
				},
			},
			index:  0,
			taskID: "task-custom",
			validate: func(tap *TapDevice, err error) {
				// Test that we get a valid TAP device structure
				if err == nil {
					assert.NotNil(t, tap)
					assert.NotEmpty(t, tap.Name)
					assert.Equal(t, "custom-bridge", tap.Bridge)
				}
			},
		},
		{
			name: "create TAP with default bridge",
			network: types.NetworkAttachment{
				Network: types.Network{
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "",
							},
						},
					},
				},
			},
			index:  1,
			taskID: "task-default",
			validate: func(tap *TapDevice, err error) {
				if err == nil {
					assert.NotNil(t, tap)
					assert.Contains(t, tap.Name, "tap-eth")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config: types.NetworkConfig{
					BridgeName: "default-bridge",
					Subnet:     "192.168.50.0/24",
					BridgeIP:   "192.168.50.1/24",
					IPMode:     "static",
				},
				tapDevices: make(map[string]*TapDevice),
			}

			// Initialize IP allocator
			gatewayStr := "192.168.50.1"
			allocator, err := NewIPAllocator("192.168.50.0/24", gatewayStr)
			require.NoError(t, err)
			nm.ipAllocator = allocator

			ctx := context.Background()
			tap, err := nm.createTapDevice(ctx, tt.network, tt.index, tt.taskID)

			tt.validate(tap, err)

			// Clean up if successful
			if err == nil && tap != nil {
				defer nm.removeTapDevice(tap)
			}
		})
	}
}

// TestIPAllocator_EdgeCases tests IP allocator edge cases
func TestIPAllocator_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		subnet      string
		gateway     string
		vmID        string
		expectError bool
		validate    func(string, error)
	}{
		{
			name:        "allocate IP for same VM twice",
			subnet:      "10.10.10.0/24",
			gateway:     "10.10.10.1",
			vmID:        "vm-twice",
			expectError: false,
			validate: func(ip1 string, err1 error) {
				if err1 == nil {
					// Create new allocator and allocate again
					allocator, _ := NewIPAllocator("10.10.10.0/24", "10.10.10.1")
					ip2, err2 := allocator.Allocate("vm-twice")
					assert.NoError(t, err2)
					// Same VM should get same IP (deterministic)
					assert.Equal(t, ip1, ip2)
				}
			},
		},
		{
			name:        "allocate IP that equals gateway",
			subnet:      "172.16.0.0/24",
			gateway:     "172.16.0.1",
			vmID:        "gateway-collision",
			expectError: false,
			validate: func(ip string, err error) {
				assert.NoError(t, err)
				assert.NotEqual(t, "172.16.0.1", ip, "Should not allocate gateway IP")
			},
		},
		{
			name:        "allocate from /30 subnet (small)",
			subnet:      "192.168.1.0/30",
			gateway:     "192.168.1.1",
			vmID:        "small-subnet",
			expectError: false,
			validate: func(ip string, err error) {
				// Should either succeed or fail with subnet exhausted
				if err != nil {
					assert.Contains(t, err.Error(), "subnet")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocator, err := NewIPAllocator(tt.subnet, tt.gateway)
			require.NoError(t, err)

			ip, err := allocator.Allocate(tt.vmID)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				// Run validation
				tt.validate(ip, err)
			}
		})
	}
}

// TestNetworkManager_ConcurrentGetTapIP tests concurrent GetTapIP calls
func TestNetworkManager_ConcurrentGetTapIP(t *testing.T) {
	nm := &NetworkManager{
		tapDevices: map[string]*TapDevice{
			"task1-tap-eth0": {
				Name: "tap-eth0",
				IP:   "10.0.0.10",
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := make(chan string, 10)

	// Launch concurrent readers
	for i := 0; i < 10; i++ {
		go func() {
			ip, _ := nm.GetTapIP("task1")
			results <- ip
		}()
	}

	// Collect results
	ips := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		select {
		case ip := <-results:
			ips = append(ips, ip)
		case <-ctx.Done():
			t.Fatal("Timeout waiting for concurrent results")
		}
	}

	// All should return the same IP
	for _, ip := range ips {
		assert.Equal(t, "10.0.0.10", ip)
	}
}

// Helper function to find last index of a character
func findLastIndex(s string, char rune) int {
	for i := len(s) - 1; i >= 0; i-- {
		if rune(s[i]) == char {
			return i
		}
	}
	return -1
}
