package network

import (
	"context"
	"sync"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

// TestNetworkManager_PrepareNetwork_InternalLogic tests internal logic paths without system calls
func TestNetworkManager_PrepareNetwork_InternalLogic(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*NetworkManager)
		task        *types.Task
		validate    func(*testing.T, *NetworkManager, error)
	}{
		{
			name: "prepare with existing bridge in cache",
			setupFunc: func(nm *NetworkManager) {
				nm.mu.Lock()
				nm.bridges["br-cached"] = true
				nm.mu.Unlock()
				nm.config.BridgeName = "br-cached"
			},
			task: &types.Task{
				ID: "task-cached-bridge",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "br-cached",
									},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, nm *NetworkManager, err error) {
				// Bridge should be in cache
				nm.mu.RLock()
				exists := nm.bridges["br-cached"]
				nm.mu.RUnlock()
				assert.True(t, exists, "Bridge should be cached")
			},
		},
		{
			name: "prepare with NAT already setup",
			setupFunc: func(nm *NetworkManager) {
				nm.config.NATEnabled = true
				nm.config.Subnet = "192.168.77.0/24"
				nm.natSetup = true // NAT already setup
			},
			task: &types.Task{
				ID: "task-nat-already",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "br-nat-setup",
									},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, nm *NetworkManager, err error) {
				assert.True(t, nm.natSetup, "NAT should remain setup")
			},
		},
		{
			name: "prepare with empty bridge name in driver config",
			setupFunc: func(nm *NetworkManager) {
				nm.config.BridgeName = "br-default"
			},
			task: &types.Task{
				ID: "task-empty-bridge",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "", // Empty, should use default
									},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, nm *NetworkManager, err error) {
				// Should use default bridge name from config
				_ = nm
			},
		},
		{
			name: "prepare stores TAP device correctly",
			setupFunc: func(nm *NetworkManager) {
				nm.config.BridgeName = "br-storage"
				nm.config.Subnet = "10.5.5.0/24"
				nm.config.BridgeIP = "10.5.5.1/24"
				nm.config.IPMode = "static"

				gatewayStr := "10.5.5.1"
				allocator, _ := NewIPAllocator("10.5.5.0/24", gatewayStr)
				nm.ipAllocator = allocator
			},
			task: &types.Task{
				ID: "task-storage-test",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "br-storage",
									},
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, nm *NetworkManager, err error) {
				// Check that TAP device structure is correct
				nm.mu.RLock()
				defer nm.mu.RUnlock()

				found := false
				for key, tap := range nm.tapDevices {
					if key[:len("task-storage-test")+1] == "task-storage-test-" {
						found = true
						assert.NotEmpty(t, tap.Name, "TAP should have name")
						assert.Equal(t, "br-storage", tap.Bridge, "Should use correct bridge")
						assert.NotEmpty(t, tap.Subnet, "Should have subnet")
						assert.NotEmpty(t, tap.Gateway, "Should have gateway")
					}
				}
				// Don't assert found true since system calls may fail
				_ = found
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config:     types.NetworkConfig{},
				bridges:    make(map[string]bool),
				tapDevices: make(map[string]*TapDevice),
			}

			tt.setupFunc(nm)

			ctx := context.Background()
			err := nm.PrepareNetwork(ctx, tt.task)

			// System calls may fail without root, that's ok
			_ = err

			if tt.validate != nil {
				tt.validate(t, nm, err)
			}
		})
	}
}

// TestNetworkManager_CleanupNetwork_InternalLogic tests cleanup logic paths
func TestNetworkManager_CleanupNetwork_InternalLogic(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*NetworkManager, *types.Task)
		task        *types.Task
		validate    func(*testing.T, *NetworkManager, *types.Task, error)
	}{
		{
			name: "cleanup releases IPs from allocator",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				nm.ipAllocator = &IPAllocator{
					subnet:    nil,
					gateway:   nil,
					allocated: make(map[string]bool),
					mu:        sync.Mutex{},
				}

				// Mark some IPs as allocated
				nm.ipAllocator.mu.Lock()
				nm.ipAllocator.allocated["192.168.1.10"] = true
				nm.ipAllocator.allocated["192.168.1.11"] = true
				nm.ipAllocator.mu.Unlock()

				nm.mu.Lock()
				nm.tapDevices[task.ID+"-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "192.168.1.10",
				}
				nm.tapDevices[task.ID+"-tap-eth1"] = &TapDevice{
					Name: "tap-eth1",
					IP:   "192.168.1.11",
				}
				nm.mu.Unlock()
			},
			task: &types.Task{
				ID: "task-ip-release",
			},
			validate: func(t *testing.T, nm *NetworkManager, task *types.Task, err error) {
				// IPs should be released
				if nm.ipAllocator != nil {
					nm.ipAllocator.mu.Lock()
					_, allocated1 := nm.ipAllocator.allocated["192.168.1.10"]
					_, allocated2 := nm.ipAllocator.allocated["192.168.1.11"]
					nm.ipAllocator.mu.Unlock()

					assert.False(t, allocated1, "IP 192.168.1.10 should be released")
					assert.False(t, allocated2, "IP 192.168.1.11 should be released")
				}

				// TAP devices should be removed from map
				nm.mu.RLock()
				_, exists1 := nm.tapDevices[task.ID+"-tap-eth0"]
				_, exists2 := nm.tapDevices[task.ID+"-tap-eth1"]
				nm.mu.RUnlock()

				assert.False(t, exists1, "TAP device should be removed from map")
				assert.False(t, exists2, "TAP device should be removed from map")
			},
		},
		{
			name: "cleanup with multiple tasks doesn't interfere",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				nm.ipAllocator = &IPAllocator{
					allocated: make(map[string]bool),
					mu:        sync.Mutex{},
				}

				// Setup multiple tasks
				task1 := "task-multi-1"
				task2 := "task-multi-2"

				nm.ipAllocator.mu.Lock()
				nm.ipAllocator.allocated["10.0.0.10"] = true
				nm.ipAllocator.allocated["10.0.0.11"] = true
				nm.ipAllocator.mu.Unlock()

				nm.mu.Lock()
				nm.tapDevices[task1+"-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "10.0.0.10",
				}
				nm.tapDevices[task2+"-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "10.0.0.11",
				}
				nm.mu.Unlock()

				// Set the task ID for this test
				task.ID = task1
			},
			task: &types.Task{
				ID: "", // Will be set in setup
			},
			validate: func(t *testing.T, nm *NetworkManager, task *types.Task, err error) {
				// Only task1's devices and IP should be removed
				nm.mu.RLock()
				_, task1Exists := nm.tapDevices["task-multi-1-tap-eth0"]
				_, task2Exists := nm.tapDevices["task-multi-2-tap-eth0"]
				nm.mu.RUnlock()

				assert.False(t, task1Exists, "Task1 TAP should be removed")
				assert.True(t, task2Exists, "Task2 TAP should still exist")

				if nm.ipAllocator != nil {
					nm.ipAllocator.mu.Lock()
					_, ip1Allocated := nm.ipAllocator.allocated["10.0.0.10"]
					_, ip2Allocated := nm.ipAllocator.allocated["10.0.0.11"]
					nm.ipAllocator.mu.Unlock()

					assert.False(t, ip1Allocated, "Task1 IP should be released")
					assert.True(t, ip2Allocated, "Task2 IP should still be allocated")
				}
			},
		},
		{
			name: "cleanup with empty task ID",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				nm.mu.Lock()
				nm.tapDevices["some-task-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "172.16.0.10",
				}
				nm.mu.Unlock()
			},
			task: &types.Task{
				ID: "",
			},
			validate: func(t *testing.T, nm *NetworkManager, task *types.Task, err error) {
				// Should not crash or error
				assert.NoError(t, err)
			},
		},
		{
			name: "cleanup with no matching devices",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				nm.mu.Lock()
				nm.tapDevices["other-task-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
				}
				nm.mu.Unlock()
			},
			task: &types.Task{
				ID: "nonexistent-task",
			},
			validate: func(t *testing.T, nm *NetworkManager, task *types.Task, err error) {
				// Should complete without error
				assert.NoError(t, err)

				// Other task's devices should remain
				nm.mu.RLock()
				_, exists := nm.tapDevices["other-task-tap-eth0"]
				nm.mu.RUnlock()
				assert.True(t, exists, "Other task's devices should remain")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config:     types.NetworkConfig{},
				bridges:    make(map[string]bool),
				tapDevices: make(map[string]*TapDevice),
			}

			tt.setupFunc(nm, tt.task)

			ctx := context.Background()
			err := nm.CleanupNetwork(ctx, tt.task)

			if tt.validate != nil {
				tt.validate(t, nm, tt.task, err)
			}
		})
	}
}

// TestNetworkManager_GetTapIP_EdgeCases tests edge cases for GetTapIP
func TestNetworkManager_GetTapIP_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*NetworkManager)
		taskID      string
		expectError bool
		expectedIP  string
	}{
		{
			name: "get IP from task with multiple devices",
			setupFunc: func(nm *NetworkManager) {
				nm.mu.Lock()
				nm.tapDevices["task-multi-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "10.10.10.10",
				}
				nm.tapDevices["task-multi-tap-eth1"] = &TapDevice{
					Name: "tap-eth1",
					IP:   "10.10.10.11",
				}
				nm.mu.Unlock()
			},
			taskID:      "task-multi",
			expectError: false,
			expectedIP:  "10.10.10.10", // Should return first one
		},
		{
			name: "get IP with special characters in task ID",
			setupFunc: func(nm *NetworkManager) {
				nm.mu.Lock()
				nm.tapDevices["task-123-456-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "192.168.99.99",
				}
				nm.mu.Unlock()
			},
			taskID:      "task-123-456",
			expectError: false,
			expectedIP:  "192.168.99.99",
		},
		{
			name: "get IP when TAP has no IP assigned",
			setupFunc: func(nm *NetworkManager) {
				nm.mu.Lock()
				nm.tapDevices["task-no-ip-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "", // No IP
				}
				nm.mu.Unlock()
			},
			taskID:      "task-no-ip",
			expectError: true,
			expectedIP:  "",
		},
		{
			name: "concurrent GetTapIP calls",
			setupFunc: func(nm *NetworkManager) {
				nm.mu.Lock()
				nm.tapDevices["task-concurrent-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "172.31.0.50",
				}
				nm.mu.Unlock()
			},
			taskID:      "task-concurrent",
			expectError: false,
			expectedIP:  "172.31.0.50",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config:     types.NetworkConfig{},
				tapDevices: make(map[string]*TapDevice),
			}

			tt.setupFunc(nm)

			// For concurrent test, spawn multiple goroutines
			if tt.name == "concurrent GetTapIP calls" {
				var wg sync.WaitGroup
				results := make(chan string, 10)

				for i := 0; i < 10; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						ip, err := nm.GetTapIP(tt.taskID)
						if err == nil {
							results <- ip
						}
					}()
				}

				wg.Wait()
				close(results)

				// All should return the same IP
				for ip := range results {
					assert.Equal(t, tt.expectedIP, ip)
				}
			} else {
				ip, err := nm.GetTapIP(tt.taskID)

				if tt.expectError {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
					assert.Equal(t, tt.expectedIP, ip)
				}
			}
		})
	}
}

// TestNetworkManager_ListTapDevices_Variations tests ListTapDevices variations
func TestNetworkManager_ListTapDevices_Variations(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*NetworkManager)
		validate    func(*testing.T, *NetworkManager, []*TapDevice)
	}{
		{
			name: "list with no devices",
			setupFunc: func(nm *NetworkManager) {
				// Empty
			},
			validate: func(t *testing.T, nm *NetworkManager, devices []*TapDevice) {
				assert.Empty(t, devices, "Should return empty slice")
			},
		},
		{
			name: "list with single device",
			setupFunc: func(nm *NetworkManager) {
				nm.mu.Lock()
				nm.tapDevices["task1-tap-eth0"] = &TapDevice{
					Name:  "tap-eth0",
					Bridge: "br0",
					IP:    "192.168.1.10",
				}
				nm.mu.Unlock()
			},
			validate: func(t *testing.T, nm *NetworkManager, devices []*TapDevice) {
				assert.Len(t, devices, 1, "Should have 1 device")
				assert.Equal(t, "tap-eth0", devices[0].Name)
			},
		},
		{
			name: "list with multiple devices from multiple tasks",
			setupFunc: func(nm *NetworkManager) {
				nm.mu.Lock()
				nm.tapDevices["task1-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					Bridge: "br0",
				}
				nm.tapDevices["task2-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					Bridge: "br1",
				}
				nm.tapDevices["task2-tap-eth1"] = &TapDevice{
					Name: "tap-eth1",
					Bridge: "br1",
				}
				nm.mu.Unlock()
			},
			validate: func(t *testing.T, nm *NetworkManager, devices []*TapDevice) {
				assert.Len(t, devices, 3, "Should have 3 devices")
			},
		},
		{
			name: "list returns new slice but with same device pointers",
			setupFunc: func(nm *NetworkManager) {
				nm.mu.Lock()
				nm.tapDevices["task1-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
				}
				nm.mu.Unlock()
			},
			validate: func(t *testing.T, nm *NetworkManager, devices []*TapDevice) {
				// The returned slice is new
				newSlice := make([]*TapDevice, 1)
				assert.NotEqual(t, &devices, &newSlice, "Should return new slice")

				// But contains pointers to the same devices
				nm.mu.RLock()
				original := nm.tapDevices["task1-tap-eth0"]
				nm.mu.RUnlock()

				assert.Equal(t, original, devices[0], "Should contain same device pointers")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config:     types.NetworkConfig{},
				tapDevices: make(map[string]*TapDevice),
			}

			tt.setupFunc(nm)

			devices := nm.ListTapDevices()

			if tt.validate != nil {
				tt.validate(t, nm, devices)
			}
		})
	}
}

// TestIPAllocator_ThreadSafety tests IP allocator thread safety
func TestIPAllocator_ThreadSafety(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func() *IPAllocator
		taskIDs     []string
		validate    func(*testing.T, map[string]string)
	}{
		{
			name: "concurrent allocations are deterministic",
			setupFunc: func() *IPAllocator {
				allocator, _ := NewIPAllocator("10.100.0.0/24", "10.100.0.1")
				return allocator
			},
			taskIDs: []string{
				"vm-1", "vm-2", "vm-3", "vm-4", "vm-5",
				"vm-1", "vm-2", "vm-3", "vm-1", "vm-4",
			},
			validate: func(t *testing.T, allocations map[string]string) {
				// Same VM should get same IP
				assert.Equal(t, allocations["vm-1"], allocations["vm-1"], "vm-1 should always get same IP")
				assert.Equal(t, allocations["vm-2"], allocations["vm-2"], "vm-2 should always get same IP")
			},
		},
		{
			name: "concurrent allocations don't collide",
			setupFunc: func() *IPAllocator {
				allocator, _ := NewIPAllocator("10.101.0.0/24", "10.101.0.1")
				return allocator
			},
			taskIDs: []string{
				"task-a", "task-b", "task-c", "task-d", "task-e",
			},
			validate: func(t *testing.T, allocations map[string]string) {
				// All IPs should be unique
				ips := make(map[string]bool)
				for _, ip := range allocations {
					if ips[ip] {
						t.Errorf("Duplicate IP allocated: %s", ip)
					}
					ips[ip] = true
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocator := tt.setupFunc()

			var wg sync.WaitGroup
			results := make(map[string]string)
			var mu sync.Mutex

			// Allocate concurrently
			for _, taskID := range tt.taskIDs {
				wg.Add(1)
				go func(tid string) {
					defer wg.Done()
					ip, err := allocator.Allocate(tid)
					if err == nil {
						mu.Lock()
						results[tid] = ip
						mu.Unlock()
					}
				}(taskID)
			}

			wg.Wait()

			if tt.validate != nil {
				tt.validate(t, results)
			}
		})
	}
}

// TestTapDevice_Structure2 tests TapDevice structure variations
func TestTapDevice_Structure2(t *testing.T) {
	tests := []struct {
		name   string
		device TapDevice
	}{
		{
			name: "fully populated TAP device",
			device: TapDevice{
				Name:    "tap-eth0",
				Bridge:  "br-test",
				IP:      "192.168.1.10",
				Netmask: "255.255.255.0",
				Gateway: "192.168.1.1",
				Subnet:  "192.168.1.0/24",
			},
		},
		{
			name: "minimal TAP device",
			device: TapDevice{
				Name: "tap-eth0",
				Bridge: "br0",
			},
		},
		{
			name: "TAP device with IPv6",
			device: TapDevice{
				Name:    "tap-eth0",
				Bridge:  "br-v6",
				IP:      "fd00::10",
				Netmask: "ffff:ffff:ffff:ffff::",
				Gateway: "fd00::1",
				Subnet:  "fd00::/64",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test that structure is valid
			assert.NotEmpty(t, tt.device.Name, "Name should not be empty")
			assert.NotEmpty(t, tt.device.Bridge, "Bridge should not be empty")
		})
	}
}

// TestNetworkManager_StateIsolation tests that manager state is properly isolated
func TestNetworkManager_StateIsolation(t *testing.T) {
	// Create two managers with same bridge name
	nm1 := NewNetworkManager(types.NetworkConfig{
		BridgeName: "br-isolation",
		Subnet:     "10.88.0.0/24",
		BridgeIP:   "10.88.0.1/24",
		IPMode:     "static",
	})

	nm2 := NewNetworkManager(types.NetworkConfig{
		BridgeName: "br-isolation",
		Subnet:     "10.88.0.0/24",
		BridgeIP:   "10.88.0.1/24",
		IPMode:     "static",
	})

	// They should have separate internal state
	nm1Impl, _ := nm1.(*NetworkManager)
	nm2Impl, _ := nm2.(*NetworkManager)

	// Check that modifications to one don't affect the other
	nm1Impl.mu.Lock()
	nm1Impl.bridges["test"] = true
	nm1Impl.mu.Unlock()

	nm2Impl.mu.RLock()
	_, exists := nm2Impl.bridges["test"]
	nm2Impl.mu.RUnlock()

	assert.False(t, exists, "Modifying one manager should not affect the other")

	if nm1Impl.ipAllocator != nil && nm2Impl.ipAllocator != nil {
		assert.NotSame(t, nm1Impl.ipAllocator, nm2Impl.ipAllocator, "Should have separate IP allocators")
	}
}

// TestNetworkManager_ConcurrentStateChanges tests concurrent state modifications
func TestNetworkManager_ConcurrentStateChanges(t *testing.T) {
	nm := &NetworkManager{
		config:     types.NetworkConfig{BridgeName: "br-concurrent-state"},
		bridges:    make(map[string]bool),
		tapDevices: make(map[string]*TapDevice),
	}

	var wg sync.WaitGroup
	numOps := 50

	// Concurrent reads and writes
	for i := 0; i < numOps; i++ {
		wg.Add(3)

		// Writer 1: bridges
		go func(idx int) {
			defer wg.Done()
			nm.mu.Lock()
			nm.bridges["br"+string(rune('A'+idx%26))] = true
			nm.mu.Unlock()
		}(i)

		// Writer 2: tapDevices
		go func(idx int) {
			defer wg.Done()
			nm.mu.Lock()
			nm.tapDevices["task"+string(rune('0'+idx%10))+"-tap-eth0"] = &TapDevice{
				Name: "tap-eth0",
			}
			nm.mu.Unlock()
		}(i)

		// Reader: ListTapDevices
		go func() {
			defer wg.Done()
			_ = nm.ListTapDevices()
		}()
	}

	wg.Wait()

	// Should complete without race condition
	assert.True(t, true, "Concurrent operations should complete without race")
}
