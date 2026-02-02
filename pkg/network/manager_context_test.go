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
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetworkManager_ContextCancellationTests tests context cancellation handling
func TestNetworkManager_ContextCancellationTests(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*NetworkManager, *types.Task)
		task        *types.Task
		cancelFunc  func(context.Context, context.CancelFunc)
		expectError bool
		errorMsg    string
	}{
		{
			name: "prepare network with cancelled context",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				// No setup
			},
			task: &types.Task{
				ID: "cancelled-prepare",
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
			cancelFunc: func(ctx context.Context, cancel context.CancelFunc) {
				cancel() // Cancel immediately
			},
			expectError: true,
		},
		{
			name: "prepare network with timeout",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				// No setup
			},
			task: &types.Task{
				ID: "timeout-prepare",
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
			cancelFunc: func(ctx context.Context, cancel context.CancelFunc) {
				time.AfterFunc(10*time.Millisecond, cancel)
			},
			expectError: true,
		},
		{
			name: "cleanup network with cancelled context",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				// Add a mock tap device
				nm.tapDevices[task.ID+"-tap-0"] = &TapDevice{
					Name:   "tap-0",
					Bridge: "test-br0",
					IP:     "192.168.127.10",
				}
			},
			task: &types.Task{
				ID: "cancelled-cleanup",
			},
			cancelFunc: func(ctx context.Context, cancel context.CancelFunc) {
				cancel() // Cancel immediately
			},
			expectError: false, // Cleanup is local operation
		},
		{
			name: "get tap IP with cancelled context",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				nm.tapDevices[task.ID+"-tap-0"] = &TapDevice{
					Name: "tap-0",
					Bridge: "test-br0",
					IP:     "192.168.127.10",
				}
			},
			task: &types.Task{
				ID: "cancelled-getip",
			},
			cancelFunc: func(ctx context.Context, cancel context.CancelFunc) {
				cancel() // Cancel immediately
			},
			expectError: false, // GetTapIP is quick
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if syscall.Geteuid() != 0 && !strings.Contains(tt.name, "getip") && !strings.Contains(tt.name, "cleanup") {
				t.Skip("skipping test: requires root privileges")
			}

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

			ctx, cancel := context.WithCancel(context.Background())

			if tt.cancelFunc != nil {
				tt.cancelFunc(ctx, cancel)
			}

			defer cancel()

			var err error
			switch {
			case strings.Contains(tt.name, "prepare"):
				err = nm.PrepareNetwork(ctx, tt.task)
			case strings.Contains(tt.name, "cleanup"):
				err = nm.CleanupNetwork(ctx, tt.task)
			case strings.Contains(tt.name, "getip"):
				_, err = nm.GetTapIP(tt.task.ID)
			}

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				_ = err
			}
		})
	}
}

// TestNetworkManager_ConcurrentContextOperations tests concurrent network operations
func TestNetworkManager_ConcurrentContextOperations(t *testing.T) {
	t.Run("concurrent prepare different tasks", func(t *testing.T) {
		if syscall.Geteuid() != 0 {
			t.Skip("skipping test: requires root privileges")
		}

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

		numGoroutines := 10
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		errors := make(chan error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()

				task := &types.Task{
					ID: fmt.Sprintf("task-%d", id),
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
				}

				ctx := context.Background()
				err := nm.PrepareNetwork(ctx, task)
				errors <- err
			}(i)
		}

		wg.Wait()
		close(errors)

		// All operations should complete
		errorCount := 0
		for err := range errors {
			if err != nil {
				errorCount++
			}
		}
		assert.Equal(t, numGoroutines, errorCount)

		// Cleanup
		for i := 0; i < numGoroutines; i++ {
			task := &types.Task{ID: fmt.Sprintf("task-%d", i)}
			_ = nm.CleanupNetwork(context.Background(), task)
		}
	})

	t.Run("concurrent cleanup operations", func(t *testing.T) {
		nm := &NetworkManager{
			config: types.NetworkConfig{
				BridgeName: "test-br0",
			},
			bridges:    make(map[string]bool),
			tapDevices: make(map[string]*TapDevice),
		}

		// Add some tap devices
		for i := 0; i < 5; i++ {
			nm.tapDevices[fmt.Sprintf("task-%d-tap-0", i)] = &TapDevice{
				Name:   fmt.Sprintf("tap-%d", i),
				Bridge: "test-br0",
				IP:     fmt.Sprintf("192.168.127.%d", i+10),
			}
		}

		numGoroutines := 10
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()

				task := &types.Task{
					ID: fmt.Sprintf("task-%d", id%5),
				}

				ctx := context.Background()
				err := nm.CleanupNetwork(ctx, task)
				assert.NoError(t, err)
			}(i)
		}

		wg.Wait()
	})

	t.Run("concurrent get tap IP", func(t *testing.T) {
		nm := &NetworkManager{
			config: types.NetworkConfig{
				BridgeName: "test-br0",
			},
			bridges:    make(map[string]bool),
			tapDevices: make(map[string]*TapDevice),
		}

		// Add some tap devices
		for i := 0; i < 5; i++ {
			nm.tapDevices[fmt.Sprintf("task-%d-tap-0", i)] = &TapDevice{
				Name:   fmt.Sprintf("tap-%d", i),
				Bridge: "test-br0",
				IP:     fmt.Sprintf("192.168.127.%d", i+10),
			}
		}

		numGoroutines := 20
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()

				taskID := fmt.Sprintf("task-%d", id%5)
				ip, err := nm.GetTapIP(taskID)

				assert.NoError(t, err)
				assert.NotEmpty(t, ip)
			}(i)
		}

		wg.Wait()
	})
}

// TestIPAllocator_ContextEdgeCases tests IP allocator edge cases
func TestIPAllocator_ContextEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		subnet      string
		gateway     string
		vmIDs       []string
		expectError bool
		validate    func(*testing.T, *IPAllocator, []string)
	}{
		{
			name:    "allocate same VM ID twice",
			subnet:  "192.168.127.0/24",
			gateway: "192.168.127.1",
			vmIDs:   []string{"vm-1", "vm-1"},
			expectError: false,
			validate: func(t *testing.T, alloc *IPAllocator, ips []string) {
				// Same VM ID should get same IP
				assert.Equal(t, ips[0], ips[1])
			},
		},
		{
			name:    "allocate many VMs",
			subnet:  "192.168.127.0/24",
			gateway: "192.168.127.1",
			vmIDs: func() []string {
				ids := make([]string, 100)
				for i := 0; i < 100; i++ {
					ids[i] = fmt.Sprintf("vm-%d", i)
				}
				return ids
			}(),
			expectError: false,
			validate: func(t *testing.T, alloc *IPAllocator, ips []string) {
				// All IPs should be unique
				uniqueIPs := make(map[string]bool)
				for _, ip := range ips {
					uniqueIPs[ip] = true
				}
				assert.Equal(t, len(ips), len(uniqueIPs), "All IPs should be unique")
			},
		},
		{
			name:    "allocate with gateway collision",
			subnet:  "192.168.127.0/24",
			gateway: "192.168.127.1",
			vmIDs:   []string{"gateway-vm"},
			expectError: false,
			validate: func(t *testing.T, alloc *IPAllocator, ips []string) {
				// Should not allocate gateway address
				assert.NotEqual(t, "192.168.127.1", ips[0])
			},
		},
		{
			name:        "allocate with invalid subnet",
			subnet:      "invalid-subnet",
			gateway:     "192.168.127.1",
			vmIDs:       []string{"vm-1"},
			expectError: true,
		},
		{
			name:        "allocate with invalid gateway",
			subnet:      "192.168.127.0/24",
			gateway:     "invalid-gateway",
			vmIDs:       []string{"vm-1"},
			expectError: true,
		},
		{
			name:    "allocate with different subnet sizes",
			subnet:  "10.0.0.0/8",
			gateway: "10.0.0.1",
			vmIDs:   []string{"vm-1", "vm-2", "vm-3"},
			expectError: false,
			validate: func(t *testing.T, alloc *IPAllocator, ips []string) {
				for _, ip := range ips {
					parsedIP := net.ParseIP(ip)
					_, subnet, _ := net.ParseCIDR("10.0.0.0/8")
					assert.True(t, subnet.Contains(parsedIP), "IP should be in subnet")
				}
			},
		},
		{
			name:    "allocate with /30 subnet",
			subnet:  "192.168.1.0/30",
			gateway: "192.168.1.1",
			vmIDs:   []string{"vm-1", "vm-2"},
			expectError: false,
			validate: func(t *testing.T, alloc *IPAllocator, ips []string) {
				// Very small subnet, but should still allocate
				assert.NotEmpty(t, ips[0])
				assert.NotEmpty(t, ips[1])
			},
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

			var ips []string
			for _, vmID := range tt.vmIDs {
				ip, err := alloc.Allocate(vmID)
				if err != nil {
					// Some allocations may fail in edge cases
					_ = err
				}
				ips = append(ips, ip)
			}

			if tt.validate != nil {
				tt.validate(t, alloc, ips)
			}
		})
	}
}

// TestIPAllocator_Release tests IP release functionality
func TestIPAllocator_Release(t *testing.T) {
	t.Run("release allocated IP", func(t *testing.T) {
		alloc, err := NewIPAllocator("192.168.127.0/24", "192.168.127.1")
		require.NoError(t, err)

		ip1, err := alloc.Allocate("vm-1")
		require.NoError(t, err)

		// Release
		alloc.Release(ip1)

		// Reallocate - should get different IP (hash-based but deterministic)
		// Actually, same VM ID will get same IP due to hash
		ip2, err := alloc.Allocate("vm-1")
		require.NoError(t, err)
		assert.Equal(t, ip1, ip2, "Same VM ID should get same IP")
	})

	t.Run("release non-allocated IP", func(t *testing.T) {
		alloc, err := NewIPAllocator("192.168.127.0/24", "192.168.127.1")
		require.NoError(t, err)

		// Release IP that was never allocated - should not panic
		alloc.Release("192.168.127.100")
	})

	t.Run("release empty IP", func(t *testing.T) {
		alloc, err := NewIPAllocator("192.168.127.0/24", "192.168.127.1")
		require.NoError(t, err)

		// Release empty IP - should not panic
		alloc.Release("")
	})

	t.Run("concurrent allocate and release", func(t *testing.T) {
		alloc, err := NewIPAllocator("192.168.127.0/24", "192.168.127.1")
		require.NoError(t, err)

		numGoroutines := 20
		var wg sync.WaitGroup
		wg.Add(numGoroutines * 2)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()
				ip, _ := alloc.Allocate(fmt.Sprintf("vm-%d", id))
				alloc.Release(ip)
			}(i)

			go func(id int) {
				defer wg.Done()
				ip, _ := alloc.Allocate(fmt.Sprintf("vm-%d", id))
				_ = ip
			}(i)
		}

		wg.Wait()
	})
}

// TestNetworkManager_PrepareNetwork_EdgeCases tests prepare network edge cases
func TestNetworkManager_PrepareNetwork_EdgeCases(t *testing.T) {
	if syscall.Geteuid() != 0 {
		t.Skip("skipping test: requires root privileges")
	}

	tests := []struct {
		name        string
		config      types.NetworkConfig
		setupFunc   func(*NetworkManager)
		task        *types.Task
		expectError bool
		validate    func(*testing.T, *NetworkManager, *types.Task)
	}{
		{
			name: "prepare with no subnet",
			config: types.NetworkConfig{
				BridgeName: "test-br0",
				// No subnet
			},
			setupFunc: func(nm *NetworkManager) {
				// No setup
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
			expectError: false,
			validate: func(t *testing.T, nm *NetworkManager, task *types.Task) {
				// Should create TAP without IP
				devices := nm.ListTapDevices()
				assert.Greater(t, len(devices), 0)
			},
		},
		{
			name: "prepare with multiple networks",
			config: types.NetworkConfig{
				BridgeName: "test-br0",
				Subnet:     "192.168.127.0/24",
				BridgeIP:   "192.168.127.1/24",
				IPMode:     "static",
			},
			setupFunc: func(nm *NetworkManager) {
				// No setup
			},
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
										Name: "test-br0",
									},
								},
							},
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, nm *NetworkManager, task *types.Task) {
				// Should create multiple TAPs
				devices := nm.ListTapDevices()
				assert.GreaterOrEqual(t, len(devices), 2)
			},
		},
		{
			name: "prepare with custom bridge",
			config: types.NetworkConfig{
				BridgeName: "default-br0",
				Subnet:     "192.168.127.0/24",
				BridgeIP:   "192.168.127.1/24",
			},
			setupFunc: func(nm *NetworkManager) {
				// No setup
			},
			task: &types.Task{
				ID: "custom-bridge-task",
				Networks: []types.NetworkAttachment{
					{
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
					},
				},
			},
			expectError: false,
		},
		{
			name: "prepare with nil task",
			config: types.NetworkConfig{
				BridgeName: "test-br0",
			},
			setupFunc: func(nm *NetworkManager) {
				// No setup
			},
			task:        nil,
			expectError: true,
		},
		{
			name: "prepare with empty networks",
			config: types.NetworkConfig{
				BridgeName: "test-br0",
			},
			setupFunc: func(nm *NetworkManager) {
				// No setup
			},
			task: &types.Task{
				ID:       "empty-nets-task",
				Networks: []types.NetworkAttachment{},
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

			if tt.setupFunc != nil {
				tt.setupFunc(nm)
			}

			ctx := context.Background()
			err := nm.PrepareNetwork(ctx, tt.task)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				// May succeed or fail depending on system
				_ = err
			}

			if tt.validate != nil {
				tt.validate(t, nm, tt.task)
			}

			// Cleanup
			if tt.task != nil {
				_ = nm.CleanupNetwork(context.Background(), tt.task)
			}
			_ = execCmd("ip", "link", "delete", "test-br0")
			_ = execCmd("ip", "link", "delete", "custom-br0")
			_ = execCmd("ip", "link", "delete", "default-br0")
		})
	}
}

// TestNetworkManager_CleanupNetwork_ContextEdgeCases tests cleanup edge cases
func TestNetworkManager_CleanupNetwork_ContextEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*NetworkManager, *types.Task)
		task        *types.Task
		expectError bool
		validate    func(*testing.T, *NetworkManager)
	}{
		{
			name: "cleanup non-existent task",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				// No setup
			},
			task: &types.Task{
				ID: "non-existent",
			},
			expectError: false,
			validate: func(t *testing.T, nm *NetworkManager) {
				// Should not error
				assert.Equal(t, 0, len(nm.tapDevices))
			},
		},
		{
			name: "cleanup nil task",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				nm.tapDevices["existing-task-tap-0"] = &TapDevice{
					Name: "tap-0",
					Bridge: "test-br0",
				}
			},
			task:        nil,
			expectError: false,
			validate: func(t *testing.T, nm *NetworkManager) {
				// Existing devices should remain
				assert.Equal(t, 1, len(nm.tapDevices))
			},
		},
		{
			name: "cleanup with multiple taps",
			setupFunc: func(nm *NetworkManager, task *types.Task) {
				nm.tapDevices[task.ID+"-tap-0"] = &TapDevice{
					Name:   "tap-0",
					Bridge: "test-br0",
					IP:     "192.168.127.10",
				}
				nm.tapDevices[task.ID+"-tap-1"] = &TapDevice{
					Name:   "tap-1",
					Bridge: "test-br0",
					IP:     "192.168.127.11",
				}
			},
			task: &types.Task{
				ID: "multi-tap-task",
			},
			expectError: false,
			validate: func(t *testing.T, nm *NetworkManager) {
				// All taps for task should be removed
				assert.Equal(t, 0, len(nm.tapDevices))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config: types.NetworkConfig{
					BridgeName: "test-br0",
				},
				bridges:    make(map[string]bool),
				tapDevices: make(map[string]*TapDevice),
			}

			if tt.setupFunc != nil {
				tt.setupFunc(nm, tt.task)
			}

			ctx := context.Background()
			err := nm.CleanupNetwork(ctx, tt.task)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.validate != nil {
				tt.validate(t, nm)
			}
		})
	}
}

// TestNetworkManager_GetTapIP_ContextEdgeCases tests GetTapIP edge cases
func TestNetworkManager_GetTapIP_ContextEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*NetworkManager, string)
		taskID      string
		expectError bool
		errorMsg    string
	}{
		{
			name: "get IP for non-existent task",
			setupFunc: func(nm *NetworkManager, taskID string) {
				// No setup
			},
			taskID:      "non-existent",
			expectError: true,
			errorMsg:    "not found",
		},
		{
			name: "get IP for task with no IP",
			setupFunc: func(nm *NetworkManager, taskID string) {
				nm.tapDevices[taskID+"-tap-0"] = &TapDevice{
					Name:   "tap-0",
					Bridge: "test-br0",
					IP:     "", // No IP allocated
				}
			},
			taskID:      "no-ip-task",
			expectError: true,
			errorMsg:    "no IP allocated",
		},
		{
			name: "get IP for task with multiple taps",
			setupFunc: func(nm *NetworkManager, taskID string) {
				nm.tapDevices[taskID+"-tap-0"] = &TapDevice{
					Name:   "tap-0",
					Bridge: "test-br0",
					IP:     "192.168.127.10",
				}
				nm.tapDevices[taskID+"-tap-1"] = &TapDevice{
					Name:   "tap-1",
					Bridge: "test-br0",
					IP:     "192.168.127.11",
				}
			},
			taskID:      "multi-tap-task",
			expectError: false, // Should return first IP
		},
		{
			name: "get IP with empty task ID",
			setupFunc: func(nm *NetworkManager, taskID string) {
				// No setup
			},
			taskID:      "",
			expectError: true,
			errorMsg:    "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config: types.NetworkConfig{
					BridgeName: "test-br0",
				},
				bridges:    make(map[string]bool),
				tapDevices: make(map[string]*TapDevice),
			}

			if tt.setupFunc != nil {
				tt.setupFunc(nm, tt.taskID)
			}

			ip, err := nm.GetTapIP(tt.taskID)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tt.errorMsg))
				}
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, ip)
			}
		})
	}
}

// TestNetworkManager_ListTapDevices tests listing TAP devices
func TestNetworkManager_ListTapDevices(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*NetworkManager)
		expected int
	}{
		{
			name:     "empty list",
			setup:    func(nm *NetworkManager) {},
			expected: 0,
		},
		{
			name: "single device",
			setup: func(nm *NetworkManager) {
				nm.tapDevices["task1-tap-0"] = &TapDevice{
					Name:   "tap-0",
					Bridge: "test-br0",
					IP:     "192.168.127.10",
				}
			},
			expected: 1,
		},
		{
			name: "multiple devices",
			setup: func(nm *NetworkManager) {
				for i := 0; i < 5; i++ {
					nm.tapDevices[fmt.Sprintf("task%d-tap-0", i)] = &TapDevice{
						Name:   fmt.Sprintf("tap-%d", i),
						Bridge: "test-br0",
						IP:     fmt.Sprintf("192.168.127.%d", i+10),
					}
				}
			},
			expected: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nm := &NetworkManager{
				config: types.NetworkConfig{
					BridgeName: "test-br0",
				},
				bridges:    make(map[string]bool),
				tapDevices: make(map[string]*TapDevice),
			}

			tt.setup(nm)

			devices := nm.ListTapDevices()
			assert.Equal(t, tt.expected, len(devices))
		})
	}
}

// Helper function
func execCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}
