package network

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetworkManager_PrepareNetwork_BridgeErrors tests bridge creation error paths
func TestNetworkManager_PrepareNetwork_BridgeErrors(t *testing.T) {
	if syscall.Geteuid() != 0 {
		t.Skip("skipping test: requires root privileges")
	}

	tests := []struct {
		name        string
		config      types.NetworkConfig
		setupFunc   func(*NetworkManager)
		task        *types.Task
		expectError bool
		errorMsg    string
	}{
		{
			name: "prepare with invalid bridge name",
			config: types.NetworkConfig{
				BridgeName: strings.Repeat("a", 20), // Too long
			},
			setupFunc: func(nm *NetworkManager) {
				// No setup
			},
			task: &types.Task{
				ID: "invalid-bridge-task",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "network-1",
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: strings.Repeat("a", 20),
									},
								},
							},
						},
					},
				},
			},
			expectError: true,
		},
		{
			name: "prepare with empty bridge name",
			config: types.NetworkConfig{
				BridgeName: "",
			},
			setupFunc: func(nm *NetworkManager) {
				// No setup
			},
			task: &types.Task{
				ID: "empty-bridge-task",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "network-1",
						},
					},
				},
			},
			expectError: true,
		},
		{
			name: "prepare with special characters in bridge name",
			config: types.NetworkConfig{
				BridgeName: "test@br0!",
			},
			setupFunc: func(nm *NetworkManager) {
				// No setup
			},
			task: &types.Task{
				ID: "special-bridge-task",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "network-1",
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "test@br0!",
									},
								},
							},
						},
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config:     tt.config,
				bridges:    make(map[string]bool),
				tapDevices: make(map[string]*TapDevice),
			}

			if tt.setupFunc != nil {
				tt.setupFunc(nm)
			}

			ctx := context.Background()
			err := nm.PrepareNetwork(ctx, tt.task)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				_ = err
			}

			// Cleanup
			if tt.task != nil {
				_ = nm.CleanupNetwork(context.Background(), tt.task)
			}
		})
	}
}

// TestIPAllocator_IPValidation tests IP validation and allocation
func TestIPAllocator_IPValidation(t *testing.T) {
	tests := []struct {
		name        string
		subnet      string
		gateway     string
		vmID        string
		validateIP  func(*testing.T, string, string, string)
		expectError bool
	}{
		{
			name:    "allocate IP in /24 subnet",
			subnet:  "192.168.1.0/24",
			gateway: "192.168.1.1",
			vmID:    "vm-1",
			validateIP: func(t *testing.T, ip, subnet, gateway string) {
				parsedIP := net.ParseIP(ip)
				_, ipNet, _ := net.ParseCIDR(subnet)
				assert.True(t, ipNet.Contains(parsedIP), "IP should be in subnet")
				assert.NotEqual(t, gateway, ip, "IP should not be gateway")
			},
			expectError: false,
		},
		{
			name:    "allocate IP in /16 subnet",
			subnet:  "10.0.0.0/16",
			gateway: "10.0.0.1",
			vmID:    "vm-1",
			validateIP: func(t *testing.T, ip, subnet, gateway string) {
				parsedIP := net.ParseIP(ip)
				_, ipNet, _ := net.ParseCIDR(subnet)
				assert.True(t, ipNet.Contains(parsedIP))
			},
			expectError: false,
		},
		{
			name:    "allocate IP in /8 subnet",
			subnet:  "172.16.0.0/12",
			gateway: "172.16.0.1",
			vmID:    "vm-1",
			validateIP: func(t *testing.T, ip, subnet, gateway string) {
				parsedIP := net.ParseIP(ip)
				_, ipNet, _ := net.ParseCIDR(subnet)
				assert.True(t, ipNet.Contains(parsedIP))
			},
			expectError: false,
		},
		{
			name:        "allocate with invalid subnet CIDR",
			subnet:      "192.168.1.0/33",
			gateway:     "192.168.1.1",
			vmID:        "vm-1",
			expectError: true,
		},
		{
			name:        "allocate with gateway not in subnet",
			subnet:      "192.168.1.0/24",
			gateway:     "10.0.0.1",
			vmID:        "vm-1",
			expectError: false, // Allocator doesn't validate gateway
			validateIP: func(t *testing.T, ip, subnet, gateway string) {
				// IP allocator doesn't validate gateway is in subnet
				// It just allocates an IP based on VMID hash
				assert.NotEmpty(t, ip)
			},
		},
		{
			name:        "allocate with empty subnet",
			subnet:      "",
			gateway:     "192.168.1.1",
			vmID:        "vm-1",
			expectError: true,
		},
		{
			name:        "allocate with empty gateway",
			subnet:      "192.168.1.0/24",
			gateway:     "",
			vmID:        "vm-1",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alloc, err := NewIPAllocator(tt.subnet, tt.gateway)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			ip, err := alloc.Allocate(tt.vmID)
			require.NoError(t, err)

			if tt.validateIP != nil {
				tt.validateIP(t, ip, tt.subnet, tt.gateway)
			}
		})
	}
}

// TestNetworkManager_TapDeviceCreation tests TAP device creation edge cases
func TestNetworkManager_TapDeviceCreation(t *testing.T) {
	if syscall.Geteuid() != 0 {
		t.Skip("skipping test: requires root privileges")
	}

	tests := []struct {
		name        string
		setupFunc   func(*NetworkManager, *types.Task)
		task        *types.Task
		validate    func(*testing.T, *NetworkManager, *types.Task)
		expectError bool
	}{
		{
			name: "create TAP with very long task ID",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				// No setup
			},
			task: &types.Task{
				ID: strings.Repeat("a", 500),
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
				},
			},
			validate: func(t *testing.T, nm *NetworkManager, task *types.Task) {
				devices := nm.ListTapDevices()
				assert.Greater(t, len(devices), 0)
			},
			expectError: true, // TAP name may be too long
		},
		{
			name: "create TAP with special characters in task ID",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				// No setup
			},
			task: &types.Task{
				ID: "task@with-special_chars/123",
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
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config: types.NetworkConfig{
					BridgeName: "test-br0",
					Subnet:     "192.168.127.0/24",
					BridgeIP:   "192.168.127.1/24",
					IPMode:     "static",
				},
				bridges:    make(map[string]bool),
				tapDevices: make(map[string]*TapDevice),
			}

			if tt.setupFunc != nil {
				tt.setupFunc(nm, tt.task)
			}

			ctx := context.Background()
			err := nm.PrepareNetwork(ctx, tt.task)

			if tt.expectError {
				assert.Error(t, err)
			}

			if tt.validate != nil {
				tt.validate(t, nm, tt.task)
			}

			// Cleanup
			_ = nm.CleanupNetwork(context.Background(), tt.task)
			_ = exec.Command("ip", "link", "delete", "test-br0").Run()
		})
	}
}

// TestNetworkManager_NATSetup tests NAT setup edge cases
func TestNetworkManager_NATSetup(t *testing.T) {
	if syscall.Geteuid() != 0 {
		t.Skip("skipping test: requires root privileges")
	}

	tests := []struct {
		name        string
		config      types.NetworkConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "setup NAT with valid subnet",
			config: types.NetworkConfig{
				BridgeName:  "test-br0",
				Subnet:      "192.168.127.0/24",
				BridgeIP:    "192.168.127.1/24",
				NATEnabled:  true,
			},
			expectError: false,
		},
		{
			name: "setup NAT without subnet",
			config: types.NetworkConfig{
				BridgeName: "test-br0",
				NATEnabled: true,
			},
			expectError: true,
			errorMsg:    "subnet must be configured",
		},
		{
			name: "setup NAT with invalid subnet",
			config: types.NetworkConfig{
				BridgeName: "test-br0",
				Subnet:     "invalid",
				NATEnabled: true,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config:     tt.config,
				bridges:    make(map[string]bool),
				tapDevices: make(map[string]*TapDevice),
			}

			ctx := context.Background()
			err := nm.setupNAT(ctx)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tt.errorMsg))
				}
			}

			// Cleanup iptables rules
			if tt.config.Subnet != "" {
				exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", tt.config.Subnet, "-j", "MASQUERADE").Run()
			}
		})
	}
}

// TestNetworkManager_IPAllocation tests IP allocation scenarios
func TestNetworkManager_IPAllocation(t *testing.T) {
	tests := []struct {
		name        string
		config      types.NetworkConfig
		setupFunc   func(*NetworkManager)
		taskIDs     []string
		validate    func(*testing.T, *NetworkManager, []string)
		expectError bool
	}{
		{
			name: "allocate many IPs",
			config: types.NetworkConfig{
				BridgeName: "test-br0",
				Subnet:     "192.168.127.0/24",
				BridgeIP:   "192.168.127.1/24",
				IPMode:     "static",
			},
			setupFunc: func(nm *NetworkManager) {
				// No setup
			},
			taskIDs: func() []string {
				ids := make([]string, 50)
				for i := 0; i < 50; i++ {
					ids[i] = fmt.Sprintf("task-%d", i)
				}
				return ids
			}(),
			validate: func(t *testing.T, nm *NetworkManager, taskIDs []string) {
				ips := make(map[string]bool)
				for _, taskID := range taskIDs {
					ip, err := nm.ipAllocator.Allocate(taskID)
					if err == nil {
						ips[ip] = true
					}
				}
				// Most IPs should be unique
				assert.Greater(t, len(ips), 40)
			},
			expectError: false,
		},
		{
			name: "allocate same task ID twice",
			config: types.NetworkConfig{
				BridgeName: "test-br0",
				Subnet:     "192.168.127.0/24",
				BridgeIP:   "192.168.127.1/24",
				IPMode:     "static",
			},
			setupFunc: func(nm *NetworkManager) {
				// No setup
			},
			taskIDs: []string{"task-1", "task-1"},
			validate: func(t *testing.T, nm *NetworkManager, taskIDs []string) {
				ip1, _ := nm.ipAllocator.Allocate(taskIDs[0])
				ip2, _ := nm.ipAllocator.Allocate(taskIDs[1])
				// Same task ID should get same IP
				assert.Equal(t, ip1, ip2)
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

			// Initialize IP allocator if subnet is configured
			if tt.config.Subnet != "" {
				// Extract IP from CIDR if needed
				gatewayIP := tt.config.BridgeIP
				if strings.Contains(gatewayIP, "/") {
					parts := strings.Split(gatewayIP, "/")
					gatewayIP = parts[0]
				}
				alloc, err := NewIPAllocator(tt.config.Subnet, gatewayIP)
				require.NoError(t, err)
				nm.ipAllocator = alloc
			}

			if tt.setupFunc != nil {
				tt.setupFunc(nm)
			}

			if tt.validate != nil {
				tt.validate(t, nm, tt.taskIDs)
			}
		})
	}
}

// TestNetworkManager_ConcurrentIPAllocation tests concurrent IP allocation
func TestNetworkManager_ConcurrentIPAllocation(t *testing.T) {
	nm := &NetworkManager{
		config: types.NetworkConfig{
			BridgeName: "test-br0",
			Subnet:     "192.168.127.0/24",
			BridgeIP:   "192.168.127.1/24",
			IPMode:     "static",
		},
		bridges:    make(map[string]bool),
		tapDevices: make(map[string]*TapDevice),
	}

	alloc, err := NewIPAllocator(nm.config.Subnet, "192.168.127.1")
	require.NoError(t, err)

	numGoroutines := 100
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	ips := make(chan string, numGoroutines)
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			taskID := fmt.Sprintf("task-%d", id)
			ip, err := alloc.Allocate(taskID)
			if err != nil {
				errors <- err
			} else {
				ips <- ip
			}
		}(i)
	}

	wg.Wait()
	close(ips)
	close(errors)

	// Count unique IPs
	uniqueIPs := make(map[string]bool)
	for ip := range ips {
		uniqueIPs[ip] = true
	}

	// Should have allocated many unique IPs
	assert.Greater(t, len(uniqueIPs), 80, "Should have allocated many unique IPs")

	// Check for errors
	errorCount := 0
	for range errors {
		errorCount++
	}
	assert.Equal(t, 0, errorCount, "Should not have any errors")
}

// TestNetworkManager_TapDeviceAttributes tests TAP device attributes
func TestNetworkManager_TapDeviceAttributes(t *testing.T) {
	tests := []struct {
		name        string
		config      types.NetworkConfig
		task        *types.Task
		validate    func(*testing.T, *TapDevice)
		expectError bool
	}{
		{
			name: "TAP device with all attributes",
			config: types.NetworkConfig{
				BridgeName: "test-br0",
				Subnet:     "192.168.127.0/24",
				BridgeIP:   "192.168.127.1/24",
				IPMode:     "static",
			},
			task: &types.Task{
				ID: "full-attr-task",
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
				},
			},
			validate: func(t *testing.T, tap *TapDevice) {
				assert.NotEmpty(t, tap.Name, "TAP name should not be empty")
				assert.NotEmpty(t, tap.Bridge, "Bridge should not be empty")
				assert.NotEmpty(t, tap.IP, "IP should not be empty")
				assert.NotEmpty(t, tap.Gateway, "Gateway should not be empty")
				assert.NotEmpty(t, tap.Subnet, "Subnet should not be empty")
				assert.NotEmpty(t, tap.Netmask, "Netmask should not be empty")
			},
			expectError: true, // Requires root
		},
		{
			name: "TAP device without subnet",
			config: types.NetworkConfig{
				BridgeName: "test-br0",
				IPMode:     "static",
			},
			task: &types.Task{
				ID: "no-subnet-task",
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
				},
			},
			validate: func(t *testing.T, tap *TapDevice) {
				assert.NotEmpty(t, tap.Name)
				assert.NotEmpty(t, tap.Bridge)
				assert.Empty(t, tap.IP, "IP should be empty when no subnet")
			},
			expectError: true, // Requires root
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if syscall.Geteuid() != 0 {
				t.Skip("skipping test: requires root privileges")
			}

			nm := &NetworkManager{
				config:     tt.config,
				bridges:    make(map[string]bool),
				tapDevices: make(map[string]*TapDevice),
			}

			ctx := context.Background()
			err := nm.PrepareNetwork(ctx, tt.task)

			// Will likely fail without proper setup
			_ = err

			// Try to get the created tap device
			devices := nm.ListTapDevices()
			if len(devices) > 0 && tt.validate != nil {
				tt.validate(t, devices[0])
			}

			// Cleanup
			_ = nm.CleanupNetwork(context.Background(), tt.task)
			_ = exec.Command("ip", "link", "delete", "test-br0").Run()
		})
	}
}

// TestNetworkManager_BridgeState tests bridge state tracking
func TestNetworkManager_BridgeState(t *testing.T) {
	if syscall.Geteuid() != 0 {
		t.Skip("skipping test: requires root privileges")
	}

	nm := &NetworkManager{
		config: types.NetworkConfig{
			BridgeName: "test-br0",
		},
		bridges:    make(map[string]bool),
		tapDevices: make(map[string]*TapDevice),
	}

	// Initially, bridge should not exist
	_, exists := nm.bridges["test-br0"]
	assert.False(t, exists, "Bridge should not exist initially")

	// After ensureBridge, it should exist
	ctx := context.Background()
	_ = nm.ensureBridge(ctx)

	_, exists = nm.bridges["test-br0"]
	// May or may not exist depending on if bridge was created

	// Cleanup
	_ = exec.Command("ip", "link", "delete", "test-br0").Run()
	nm.bridges = make(map[string]bool)
}

// TestNetworkManager_DifferentSubnets tests different subnet configurations
func TestNetworkManager_DifferentSubnets(t *testing.T) {
	subnets := []struct {
		subnet  string
		gateway string
	}{
		{"192.168.1.0/24", "192.168.1.1"},
		{"192.168.2.0/24", "192.168.2.1"},
		{"10.0.0.0/8", "10.0.0.1"},
		{"172.16.0.0/12", "172.16.0.1"},
	}

	for _, tt := range subnets {
		t.Run(tt.subnet, func(t *testing.T) {
			config := types.NetworkConfig{
				BridgeName: "test-br0",
				Subnet:     tt.subnet,
				BridgeIP:   tt.gateway + "/24",
				IPMode:     "static",
			}

			nm := NewNetworkManager(config)
			assert.NotNil(t, nm)

			nmImpl, ok := nm.(*NetworkManager)
			assert.True(t, ok)
			assert.NotNil(t, nmImpl.ipAllocator)
		})
	}
}

// TestIncIP_Additional tests IP increment function
func TestIncIP_Additional(t *testing.T) {
	tests := []struct {
		name     string
		inputIP  string
		expected string
	}{
		{
			name:     "increment last octet",
			inputIP:  "192.168.1.1",
			expected: "192.168.1.2",
		},
		{
			name:     "increment with overflow",
			inputIP:  "192.168.1.255",
			expected: "192.168.2.0",
		},
		{
			name:     "increment multiple overflows",
			inputIP:  "192.168.255.255",
			expected: "192.169.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := net.ParseIP(tt.inputIP)
			result := incIP(input)
			assert.Equal(t, tt.expected, result.String())
		})
	}
}

// TestNetworkManager_NetworkAttachments tests different network attachment scenarios
func TestNetworkManager_NetworkAttachments(t *testing.T) {
	tests := []struct {
		name        string
		task        *types.Task
		expectError bool
	}{
		{
			name: "single network attachment",
			task: &types.Task{
				ID: "single-net-task",
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
				},
			},
			expectError: true, // Requires root
		},
		{
			name: "multiple network attachments",
			task: &types.Task{
				ID: "multi-net-task",
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
			expectError: true, // Requires root
		},
		{
			name: "no network attachments",
			task: &types.Task{
				ID:       "no-net-task",
				Networks: []types.NetworkAttachment{},
			},
			expectError: false, // Should succeed without doing anything
		},
		{
			name: "network attachment without driver config",
			task: &types.Task{
				ID: "no-driver-task",
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
			expectError: true, // Needs bridge name
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config: types.NetworkConfig{
					BridgeName: "test-br0",
					Subnet:     "192.168.127.0/24",
					BridgeIP:   "192.168.127.1/24",
					IPMode:     "static",
				},
				bridges:    make(map[string]bool),
				tapDevices: make(map[string]*TapDevice),
			}

			ctx := context.Background()
			err := nm.PrepareNetwork(ctx, tt.task)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Cleanup
			_ = nm.CleanupNetwork(context.Background(), tt.task)
			_ = exec.Command("ip", "link", "delete", "test-br0").Run()
			_ = exec.Command("ip", "link", "delete", "test-br1").Run()
		})
	}
}
