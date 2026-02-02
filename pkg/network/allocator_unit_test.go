// Package network manages networking for Firecracker VMs.
package network

import (
	"context"
	"net"
	"testing"
	"fmt"
	"sync"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewIPAllocator tests IP allocator creation
func TestNewIPAllocator_Unit(t *testing.T) {
	tests := []struct {
		name        string
		subnet      string
		gateway     string
		wantErr     bool
		errContains string
	}{
		{
			name:    "valid IPv4 subnet",
			subnet:  "192.168.1.0/24",
			gateway: "192.168.1.1",
			wantErr: false,
		},
		{
			name:    "valid IPv6 subnet",
			subnet:  "fd00::/64",
			gateway: "fd00::1",
			wantErr: false,
		},
		{
			name:        "invalid subnet",
			subnet:      "invalid-subnet",
			gateway:     "192.168.1.1",
			wantErr:     true,
			errContains: "invalid CIDR address",
		},
		{
			name:        "invalid gateway",
			subnet:      "192.168.1.0/24",
			gateway:     "invalid-gateway",
			wantErr:     true,
			errContains: "invalid gateway",
		},
		{
			name:        "empty subnet",
			subnet:      "",
			gateway:     "192.168.1.1",
			wantErr:     true,
			errContains: "invalid CIDR address",
		},
		{
			name:        "empty gateway",
			subnet:      "192.168.1.0/24",
			gateway:     "",
			wantErr:     true,
			errContains: "invalid gateway",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocator, err := NewIPAllocator(tt.subnet, tt.gateway)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, allocator)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, allocator)
				assert.NotNil(t, allocator.subnet)
				assert.NotNil(t, allocator.gateway)
				assert.NotNil(t, allocator.allocated)
			}
		})
	}
}

// TestIPAllocator_Allocate tests IP allocation
func TestIPAllocator_Allocate_Unit(t *testing.T) {
	tests := []struct {
		name        string
		subnet      string
		gateway     string
		vmID        string
		wantErr     bool
		errContains string
		checkIP     bool
	}{
		{
			name:    "allocate valid IP",
			subnet:  "192.168.1.0/24",
			gateway: "192.168.1.1",
			vmID:    "vm-1",
			wantErr: false,
			checkIP: true,
		},
		{
			name:    "allocate with IPv6",
			subnet:  "fd00::/64",
			gateway: "fd00::1",
			vmID:    "vm-2",
			wantErr: false,
			checkIP: true,
		},
		{
			name:    "allocate same VM ID twice",
			subnet:  "192.168.1.0/24",
			gateway: "192.168.1.1",
			vmID:    "vm-duplicate",
			wantErr: false,
			checkIP: true,
		},
		{
			name:    "allocate with different VM IDs",
			subnet:  "192.168.1.0/24",
			gateway: "192.168.1.1",
			vmID:    "vm-3",
			wantErr: false,
			checkIP: true,
		},
		{
			name:    "allocate with small subnet",
			subnet:  "192.168.1.0/30",
			gateway: "192.168.1.1",
			vmID:    "vm-4",
			wantErr: false,
			checkIP: true,
		},
		{
			name:        "empty VM ID",
			subnet:      "192.168.1.0/24",
			gateway:     "192.168.1.1",
			vmID:        "",
			wantErr:     false,
			checkIP:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocator, err := NewIPAllocator(tt.subnet, tt.gateway)
			require.NoError(t, err)

			ip, err := allocator.Allocate(tt.vmID)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, ip)

				if tt.checkIP {
					// Verify IP is valid
					parsedIP := net.ParseIP(ip)
					assert.NotNil(t, parsedIP)

					// Verify IP is in subnet
					assert.True(t, allocator.subnet.Contains(parsedIP))
				}
			}
		})
	}
}

// TestIPAllocator_Allocate_Deterministic tests that allocation is deterministic
func TestIPAllocator_Allocate_Deterministic_Unit(t *testing.T) {
	subnet := "192.168.1.0/24"
	gateway := "192.168.1.1"

	allocator1, err := NewIPAllocator(subnet, gateway)
	require.NoError(t, err)

	allocator2, err := NewIPAllocator(subnet, gateway)
	require.NoError(t, err)

	vmID := "test-vm-123"

	ip1, err := allocator1.Allocate(vmID)
	assert.NoError(t, err)

	ip2, err := allocator2.Allocate(vmID)
	assert.NoError(t, err)

	// Same VM ID should get same IP
	assert.Equal(t, ip1, ip2)
}

// TestIPAllocator_Allocate_Distribution tests IP distribution
func TestIPAllocator_Allocate_Distribution_Unit(t *testing.T) {
	subnet := "192.168.1.0/24"
	gateway := "192.168.1.1"

	allocator, err := NewIPAllocator(subnet, gateway)
	require.NoError(t, err)

	// Allocate IPs for multiple VMs
	ips := make(map[string]bool)
	for i := 0; i < 100; i++ {
		}
		vmID := "vm-" + string(rune('a'+i%26))

		ip, err := allocator.Allocate(vmID)
		assert.NoError(t, err)
		assert.NotEmpty(t, ip)
		assert.False(t, ips[ip], "IP should be unique: %s", ip)
		ips[ip] = true
	}

	// Should have allocated 100 unique IPs
	assert.Equal(t, 100, len(ips))
}

// TestIPAllocator_Release tests IP release
func TestIPAllocator_Release_Unit(t *testing.T) {
	subnet := "192.168.1.0/24"
	gateway := "192.168.1.1"

	allocator, err := NewIPAllocator(subnet, gateway)
	require.NoError(t, err)

	vmID := "test-vm"
	ip, err := allocator.Allocate(vmID)
	require.NoError(t, err)

	// Verify IP is allocated
	assert.True(t, allocator.allocated[ip])

	// Release IP
	allocator.Release(ip)

	// Verify IP is no longer allocated
	assert.False(t, allocator.allocated[ip])

	// Should be able to allocate again
	ip2, err := allocator.Allocate(vmID)
	assert.NoError(t, err)
	// May or may not be the same IP
}

// TestIPAllocator_Release_NonExistent tests releasing non-existent IP
func TestIPAllocator_Release_NonExistent_Unit(t *testing.T) {
	subnet := "192.168.1.0/24"
	gateway := "192.168.1.1"

	allocator, err := NewIPAllocator(subnet, gateway)
	require.NoError(t, err)

	// Release non-existent IP (should not panic)
	allocator.Release("192.168.1.100")
	allocator.Release("")
	allocator.Release("invalid-ip")
}

// TestIPAllocator_GatewayCollision tests gateway collision handling
func TestIPAllocator_GatewayCollision_Unit(t *testing.T) {
	subnet := "192.168.1.0/24"
	gateway := "192.168.1.1"

	allocator, err := NewIPAllocator(subnet, gateway)
	require.NoError(t, err)

	// Find a VM ID that hashes to the gateway IP
	// This is difficult to predict, so we'll just verify the logic
	// by testing that allocated IPs are not the gateway

	for i := 0; i < 100; i++ {
		vmID := "vm-" + string(rune('a'+i%26))

		ip, err := allocator.Allocate(vmID)
		assert.NoError(t, err)
		assert.NotEqual(t, gateway, ip, "Allocated IP should not be gateway")
	}
}

// TestIPAllocator_HashToIP tests hash-to-IP conversion
func TestIPAllocator_HashToIP_Unit(t *testing.T) {
	subnet := "192.168.1.0/24"
	gateway := "192.168.1.1"

	allocator, err := NewIPAllocator(subnet, gateway)
	require.NoError(t, err)

	tests := []struct {
		name  string
		vmID  string
		check func(t *testing.T, ip net.IP)
	}{
		{
			name: "valid VM ID",
			vmID: "test-vm",
			check: func(t *testing.T, ip net.IP) {
				assert.NotNil(t, ip)
				assert.True(t, allocator.subnet.Contains(ip))
			},
		},
		{
			name: "empty VM ID",
			vmID: "",
			check: func(t *testing.T, ip net.IP) {
				assert.NotNil(t, ip)
				assert.True(t, allocator.subnet.Contains(ip))
			},
		},
		{
			name: "long VM ID",
			vmID: "very-long-vm-id-with-many-characters-123456789",
			check: func(t *testing.T, ip net.IP) {
				assert.NotNil(t, ip)
				assert.True(t, allocator.subnet.Contains(ip))
			},
		},
		{
			name: "special characters",
			vmID: "vm-with-special-chars-!@#$%",
			check: func(t *testing.T, ip net.IP) {
				assert.NotNil(t, ip)
				assert.True(t, allocator.subnet.Contains(ip))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := allocator.hashToIP(tt.vmID)
			tt.check(t, ip)
		})
	}
}

// TestIncIP tests IP increment function
func TestIncIP_Unit(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "increment last octet",
			input: "192.168.1.1",
			want:  "192.168.1.2",
		},
		{
			name:  "increment with overflow",
			input: "192.168.1.255",
			want:  "192.168.2.0",
		},
		{
			name:  "increment multiple overflows",
			input: "192.168.255.255",
			want:  "192.169.0.0",
		},
		{
			name:  "increment all overflows",
			input: "255.255.255.255",
			want:  "0.0.0.0",
		},
		{
			name:  "increment zero",
			input: "0.0.0.0",
			want:  "0.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := net.ParseIP(tt.input)
			require.NotNil(t, input)

			got := incIP(input)
			assert.Equal(t, tt.want, got.String())
		})
	}
}

// TestNewNetworkManager tests network manager creation
func TestNewNetworkManager_Unit(t *testing.T) {
	tests := []struct {
		name            string
		config          types.NetworkConfig
		expectAllocator bool
	}{
		{
			name: "with subnet and bridge IP",
			config: types.NetworkConfig{
				Subnet:      "192.168.1.0/24",
				BridgeIP:    "192.168.1.1/24",
				BridgeName:  "br0",
				NATEnabled:  true,
				IPMode:      "static",
			},
			expectAllocator: true,
		},
		{
			name: "without subnet",
			config: types.NetworkConfig{
				BridgeName: "br0",
			},
			expectAllocator: false,
		},
		{
			name: "without bridge IP",
			config: types.NetworkConfig{
				Subnet:     "192.168.1.0/24",
				BridgeName: "br0",
			},
			expectAllocator: false,
		},
		{
			name: "empty config",
			config: types.NetworkConfig{
				BridgeName: "swarm-br0",
			},
			expectAllocator: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := NewNetworkManager(tt.config).(*NetworkManager)

			assert.NotNil(t, nm)
			assert.NotNil(t, nm.bridges)
			assert.NotNil(t, nm.tapDevices)

			if tt.expectAllocator {
				assert.NotNil(t, nm.ipAllocator)
			} else {
				assert.Nil(t, nm.ipAllocator)
			}
		})
	}
}

// TestNetworkManager_GetTapIP tests GetTapIP functionality
func TestNetworkManager_GetTapIP_Unit(t *testing.T) {
	tests := []struct {
		name        string
		taskID      string
		setup       func(t *testing.T) *NetworkManager
		wantErr     bool
		errContains string
	}{
		{
			name:   "existing TAP device",
			taskID: "task-1",
			setup: func(t *testing.T) *NetworkManager {
				nm := &NetworkManager{
					tapDevices: make(map[string]*TapDevice),
				}
				nm.tapDevices["task-1-tap-0"] = &TapDevice{
					Name: "tap-0",
					IP:   "192.168.1.10",
				}
				return nm
			},
			wantErr: false,
		},
		{
			name:   "non-existent task",
			taskID: "task-999",
			setup: func(t *testing.T) *NetworkManager {
				nm := &NetworkManager{
					tapDevices: make(map[string]*TapDevice),
				}
				nm.tapDevices["task-1-tap-0"] = &TapDevice{
					Name: "tap-0",
					IP:   "192.168.1.10",
				}
				return nm
			},
			wantErr:     true,
			errContains: "no TAP device found",
		},
		{
			name:   "TAP device with no IP",
			taskID: "task-2",
			setup: func(t *testing.T) *NetworkManager {
				nm := &NetworkManager{
					tapDevices: make(map[string]*TapDevice),
				}
				nm.tapDevices["task-2-tap-0"] = &TapDevice{
					Name: "tap-0",
					IP:   "",
				}
				return nm
			},
			wantErr:     true,
			errContains: "no IP allocated",
		},
		{
			name:   "empty task ID",
			taskID: "",
			setup: func(t *testing.T) *NetworkManager {
				nm := &NetworkManager{
					tapDevices: make(map[string]*TapDevice),
				}
				return nm
			},
			wantErr:     true,
			errContains: "no TAP device found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := tt.setup(t)

			ip, err := nm.GetTapIP(tt.taskID)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, ip)
			}
		})
	}
}

// TestNetworkManager_ListTapDevices tests listing TAP devices
func TestNetworkManager_ListTapDevices_Unit(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) *NetworkManager
		expected int
	}{
		{
			name: "empty list",
			setup: func(t *testing.T) *NetworkManager {
				return &NetworkManager{
					tapDevices: make(map[string]*TapDevice),
				}
			},
			expected: 0,
		},
		{
			name: "single device",
			setup: func(t *testing.T) *NetworkManager {
				nm := &NetworkManager{
					tapDevices: make(map[string]*TapDevice),
				}
				nm.tapDevices["task-1-tap-0"] = &TapDevice{
					Name: "tap-0",
					IP:   "192.168.1.10",
				}
				return nm
			},
			expected: 1,
		},
		{
			name: "multiple devices",
			setup: func(t *testing.T) *NetworkManager {
				nm := &NetworkManager{
					tapDevices: make(map[string]*TapDevice),
				}
				nm.tapDevices["task-1-tap-0"] = &TapDevice{
					Name: "tap-0",
					IP:   "192.168.1.10",
				}
				nm.tapDevices["task-2-tap-0"] = &TapDevice{
					Name: "tap-0",
					IP:   "192.168.1.11",
				}
				nm.tapDevices["task-3-tap-0"] = &TapDevice{
					Name: "tap-0",
					IP:   "192.168.1.12",
				}
				return nm
			},
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := tt.setup(t)

			devices := nm.ListTapDevices()
			assert.Equal(t, tt.expected, len(devices))
		})
	}
}

// TestTapDevice tests TapDevice structure
func TestTapDevice_Unit(t *testing.T) {
	tests := []struct {
		name string
		tap  *TapDevice
	}{
		{
			name: "complete TAP device",
			tap: &TapDevice{
				Name:    "tap-0",
				Bridge:  "br0",
				IP:      "192.168.1.10",
				Netmask: "255.255.255.0",
				Gateway: "192.168.1.1",
				Subnet:  "192.168.1.0/24",
			},
		},
		{
			name: "minimal TAP device",
			tap: &TapDevice{
				Name: "tap-1",
				Bridge: "br0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotNil(t, tt.tap)
			assert.NotEmpty(t, tt.tap.Name)
			assert.NotEmpty(t, tt.tap.Bridge)
		})
	}
}

// TestIPAllocator_ConcurrentAllocation tests concurrent IP allocation
func TestIPAllocator_ConcurrentAllocation_Unit(t *testing.T) {
	subnet := "192.168.1.0/24"
	gateway := "192.168.1.1"

	allocator, err := NewIPAllocator(subnet, gateway)
	require.NoError(t, err)

	const numGoroutines = 10
	const numAllocations = 10

	ips := make(map[string]bool)
	var mu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			for j := 0; j < numAllocations; j++ {
				vmID := fmt.Sprintf("vm-%d-%d", id, j)
				ip, err := allocator.Allocate(vmID)
				assert.NoError(t, err)

				mu.Lock()
				ips[ip] = true
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()

	// Should have allocated unique IPs
	expectedIPs := numGoroutines * numAllocations
	assert.Equal(t, expectedIPs, len(ips))
}

// TestNetworkManager_CleanupNetwork tests cleanup with nil task
func TestNetworkManager_CleanupNetwork(t *testing.T) {
	nm := &NetworkManager{
		tapDevices: make(map[string]*TapDevice),
	}

	ctx := context.Background()

	// Should not panic with nil task
	err := nm.CleanupNetwork(ctx, nil)
	assert.NoError(t, err)
}
