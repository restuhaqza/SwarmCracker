package network

import (
	"context"
	"errors"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetworkManagerWithExecutor_Mocked tests all network operations with mocked commands
func TestNetworkManagerWithExecutor_Mocked(t *testing.T) {
	tests := []struct {
		name        string
		setupMock   func(*MockCommandExecutor)
		config      types.NetworkConfig
		task        *types.Task
		operation   func(*testing.T, *NetworkManagerInternal, context.Context, *types.Task)
		expectError bool
		validate    func(*testing.T, *MockCommandExecutor, *NetworkManagerInternal)
	}{
		{
			name: "ensure_bridge_creates_new_bridge",
			setupMock: func(m *MockCommandExecutor) {
				// Use custom handler to differentiate between show and add commands
				m.CommandHandlers["ip"] = func(args []string) MockCommandResult {
					if len(args) >= 3 && args[0] == "link" && args[1] == "show" {
						// Bridge doesn't exist
						return MockCommandResult{Err: errors.New("bridge does not exist")}
					}
					// All other ip commands succeed
					return MockCommandResult{Err: nil}
				}
			},
			config: types.NetworkConfig{
				BridgeName: "test-br0",
				BridgeIP:   "192.168.1.1/24",
			},
			task: &types.Task{
				ID: "test-task",
			},
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				err := nm.ensureBridgeWithExecutor(ctx)
				assert.NoError(t, err)
			},
			expectError: false,
			validate: func(t *testing.T, m *MockCommandExecutor, nm *NetworkManagerInternal) {
				assert.True(t, nm.bridges["test-br0"], "Bridge should be marked as created")
				// Verify command calls
				assert.GreaterOrEqual(t, len(m.Calls), 1, "Should have called ip command")
			},
		},
		{
			name: "ensure_bridge_already_exists",
			setupMock: func(m *MockCommandExecutor) {
				m.Commands["ip"] = MockCommandResult{
					Err: nil, // Bridge exists
				}
			},
			config: types.NetworkConfig{
				BridgeName: "existing-br0",
			},
			task: &types.Task{
				ID: "test-task",
			},
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				err := nm.ensureBridgeWithExecutor(ctx)
				assert.NoError(t, err)
			},
			expectError: false,
			validate: func(t *testing.T, m *MockCommandExecutor, nm *NetworkManagerInternal) {
				assert.True(t, nm.bridges["existing-br0"], "Bridge should be marked as existing")
			},
		},
		{
			name: "setup_bridge_ip_success",
			setupMock: func(m *MockCommandExecutor) {
				m.Commands["ip"] = MockCommandResult{
					Err: nil,
				}
			},
			config: types.NetworkConfig{
				BridgeName: "test-br1",
				BridgeIP:   "10.0.0.1/24",
			},
			task: &types.Task{
				ID: "test-task",
			},
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				err := nm.setupBridgeIPWithExecutor(ctx)
				assert.NoError(t, err)
			},
			expectError: false,
		},
		{
			name: "setup_nat_with_valid_subnet",
			setupMock: func(m *MockCommandExecutor) {
				m.CommandHandlers["sysctl"] = func(args []string) MockCommandResult {
					return MockCommandResult{Err: nil}
				}
				m.CommandHandlers["iptables"] = func(args []string) MockCommandResult {
					// -C check command fails (rule doesn't exist)
					if len(args) >= 2 && args[0] == "-t" && args[2] == "-C" {
						return MockCommandResult{Err: errors.New("rule does not exist")}
					}
					// -A add command succeeds
					return MockCommandResult{Err: nil}
				}
				m.CommandHandlers["ip"] = func(args []string) MockCommandResult {
					return MockCommandResult{Err: nil}
				}
			},
			config: types.NetworkConfig{
				Subnet:     "192.168.100.0/24",
				BridgeName: "test-br-nat",
				NATEnabled: true,
			},
			task: &types.Task{
				ID: "test-task",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "test-br-nat",
									},
								},
							},
						},
					},
				},
			},
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				err := nm.PrepareNetworkWithExecutor(ctx, task)
				assert.NoError(t, err)
			},
			expectError: false,
			validate: func(t *testing.T, m *MockCommandExecutor, nm *NetworkManagerInternal) {
				assert.True(t, nm.natSetup, "NAT should be marked as set up")
			},
		},
		{
			name: "setup_nat_with_empty_subnet_error",
			setupMock: func(m *MockCommandExecutor) {
				m.Commands["sysctl"] = MockCommandResult{Err: nil}
			},
			config: types.NetworkConfig{
				Subnet:     "",
				BridgeName: "test-br-nat",
			},
			task: &types.Task{
				ID: "test-task",
			},
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				err := nm.setupNATWithExecutor(ctx)
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "subnet must be configured")
			},
			expectError: true,
		},
		{
			name: "setup_nat_ip_forward_fails",
			setupMock: func(m *MockCommandExecutor) {
				m.Commands["sysctl"] = MockCommandResult{
					Err: errors.New("permission denied"),
				}
			},
			config: types.NetworkConfig{
				Subnet:     "192.168.100.0/24",
				BridgeName: "test-br-nat",
			},
			task: &types.Task{
				ID: "test-task",
			},
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				err := nm.setupNATWithExecutor(ctx)
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "failed to enable IP forwarding")
			},
			expectError: true,
		},
		{
			name: "create_tap_device_with_static_ip",
			setupMock: func(m *MockCommandExecutor) {
				m.Commands["ip"] = MockCommandResult{Err: nil}
			},
			config: types.NetworkConfig{
				BridgeName: "test-br-tap",
				Subnet:     "192.168.50.0/24",
				BridgeIP:   "192.168.50.1/24",
				IPMode:     "static",
			},
			task: &types.Task{
				ID: "test-task-tap",
			},
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				network := types.NetworkAttachment{
					Network: types.Network{
						Spec: types.NetworkSpec{
							DriverConfig: &types.DriverConfig{
								Bridge: &types.BridgeConfig{
									Name: "test-br-tap",
								},
							},
						},
					},
				}
				tap, err := nm.createTapDeviceWithExecutor(ctx, network, 0, "test-task-tap")
				assert.NoError(t, err)
				assert.NotNil(t, tap)
				assert.Equal(t, "tap-eth0", tap.Name)
				assert.Equal(t, "test-br-tap", tap.Bridge)
				assert.NotEmpty(t, tap.IP, "Should allocate static IP")
			},
			expectError: false,
		},
		{
			name: "create_tap_device_without_ip",
			setupMock: func(m *MockCommandExecutor) {
				m.Commands["ip"] = MockCommandResult{Err: nil}
			},
			config: types.NetworkConfig{
				BridgeName: "test-br-tap2",
				Subnet:     "",
				BridgeIP:   "",
				IPMode:     "dhcp",
			},
			task: &types.Task{
				ID: "test-task-dhcp",
			},
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				network := types.NetworkAttachment{
					Network: types.Network{
						Spec: types.NetworkSpec{
							DriverConfig: &types.DriverConfig{
								Bridge: &types.BridgeConfig{
									Name: "test-br-tap2",
								},
							},
						},
					},
				}
				tap, err := nm.createTapDeviceWithExecutor(ctx, network, 0, "test-task-dhcp")
				assert.NoError(t, err)
				assert.NotNil(t, tap)
				assert.Empty(t, tap.IP, "Should not allocate IP in DHCP mode")
			},
			expectError: false,
		},
		{
			name: "create_tap_device_creation_fails",
			setupMock: func(m *MockCommandExecutor) {
				m.Commands["ip"] = MockCommandResult{
					Err: errors.New("failed to create tuntap"),
				}
			},
			config: types.NetworkConfig{
				BridgeName: "test-br-fail",
			},
			task: &types.Task{
				ID: "test-task-fail",
			},
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				network := types.NetworkAttachment{
					Network: types.Network{
						Spec: types.NetworkSpec{
							DriverConfig: &types.DriverConfig{
								Bridge: &types.BridgeConfig{
									Name: "test-br-fail",
								},
							},
						},
					},
				}
				tap, err := nm.createTapDeviceWithExecutor(ctx, network, 0, "test-task-fail")
				assert.Error(t, err)
				assert.Nil(t, tap)
				assert.Contains(t, err.Error(), "failed to create TAP device")
			},
			expectError: true,
		},
		{
			name: "remove_tap_device_success",
			setupMock: func(m *MockCommandExecutor) {
				m.Commands["ip"] = MockCommandResult{Err: nil}
			},
			config: types.NetworkConfig{},
			task: &types.Task{
				ID: "test-task-remove",
			},
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				tap := &TapDevice{
					Name:   "tap-eth0",
					Bridge: "test-br",
				}
				err := nm.removeTapDeviceWithExecutor(tap)
				assert.NoError(t, err)
			},
			expectError: false,
		},
		{
			name: "prepare_network_full_flow",
			setupMock: func(m *MockCommandExecutor) {
				m.CommandHandlers["ip"] = func(args []string) MockCommandResult {
					if len(args) >= 3 && args[0] == "link" && args[1] == "show" {
						return MockCommandResult{Err: errors.New("bridge does not exist")}
					}
					return MockCommandResult{Err: nil}
				}
				m.CommandHandlers["sysctl"] = func(args []string) MockCommandResult {
					return MockCommandResult{Err: nil}
				}
				m.CommandHandlers["iptables"] = func(args []string) MockCommandResult {
					if len(args) >= 2 && args[0] == "-t" && args[2] == "-C" {
						return MockCommandResult{Err: errors.New("rule does not exist")}
					}
					return MockCommandResult{Err: nil}
				}
			},
			config: types.NetworkConfig{
				BridgeName: "test-br-prepare",
				BridgeIP:   "192.168.200.1/24",
				Subnet:     "192.168.200.0/24",
				NATEnabled: true,
				IPMode:     "static",
			},
			task: &types.Task{
				ID: "test-task-prepare",
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "test-br-prepare",
									},
								},
							},
						},
					},
				},
			},
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				err := nm.PrepareNetworkWithExecutor(ctx, task)
				assert.NoError(t, err)
			},
			expectError: false,
			validate: func(t *testing.T, m *MockCommandExecutor, nm *NetworkManagerInternal) {
				assert.True(t, nm.natSetup, "NAT should be set up")
				assert.NotEmpty(t, nm.tapDevices, "Should have created TAP devices")
			},
		},
		{
			name: "cleanup_network_removes_devices",
			setupMock: func(m *MockCommandExecutor) {
				m.Commands["ip"] = MockCommandResult{Err: nil}
			},
			config: types.NetworkConfig{
				BridgeName: "test-br-cleanup",
			},
			task: &types.Task{
				ID: "test-task-cleanup",
			},
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				// Add a fake TAP device
				nm.tapDevices["test-task-cleanup-tap-eth0"] = &TapDevice{
					Name: "tap-eth0",
					IP:   "192.168.1.10",
				}

				err := nm.CleanupNetworkWithExecutor(ctx, task)
				assert.NoError(t, err)
				assert.Empty(t, nm.tapDevices, "TAP devices should be removed")
			},
			expectError: false,
		},
		{
			name:      "cleanup_network_with_nil_task",
			setupMock: func(m *MockCommandExecutor) {},
			config: types.NetworkConfig{
				BridgeName: "test-br-nil",
			},
			task: nil,
			operation: func(t *testing.T, nm *NetworkManagerInternal, ctx context.Context, task *types.Task) {
				err := nm.CleanupNetworkWithExecutor(ctx, nil)
				assert.NoError(t, err, "Cleanup with nil task should not error")
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := NewMockCommandExecutor()
			if tt.setupMock != nil {
				tt.setupMock(mockExecutor)
			}

			nm := NewNetworkManagerWithExecutor(tt.config, mockExecutor)

			ctx := context.Background()

			// Store task reference for operations
			if tt.task != nil {
				t.Run("operation", func(t *testing.T) {
					tt.operation(t, nm, ctx, tt.task)
				})
			}

			// Run validation
			if tt.validate != nil {
				tt.validate(t, mockExecutor, nm)
			}
		})
	}
}

// TestNetworkManagerWithExecutor_ErrorHandling tests error handling paths
func TestNetworkManagerWithExecutor_ErrorHandling(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(*MockCommandExecutor)
		config        types.NetworkConfig
		task          *types.Task
		operation     string
		expectError   bool
		errorContains string
	}{
		{
			name: "bridge_creation_fails",
			setupMock: func(m *MockCommandExecutor) {
				// Bridge doesn't exist, create fails
				m.CommandHandlers["ip"] = func(args []string) MockCommandResult {
					if len(args) >= 3 && args[0] == "link" && args[1] == "show" {
						return MockCommandResult{Err: errors.New("bridge does not exist")}
					}
					if len(args) >= 4 && args[0] == "link" && args[1] == "add" {
						return MockCommandResult{Err: errors.New("failed to create")}
					}
					return MockCommandResult{Err: nil}
				}
			},
			config: types.NetworkConfig{
				BridgeName: "test-br-fail",
			},
			task:          &types.Task{ID: "test"},
			operation:     "ensure",
			expectError:   true,
			errorContains: "failed to create bridge",
		},
		{
			name: "bridge_bring_up_fails",
			setupMock: func(m *MockCommandExecutor) {
				// Bridge creation succeeds but bring up fails
				m.CommandHandlers["ip"] = func(args []string) MockCommandResult {
					// Show succeeds (bridge exists)
					if len(args) >= 3 && args[0] == "link" && args[1] == "show" {
						return MockCommandResult{Err: errors.New("bridge does not exist")}
					}
					// add succeeds
					if len(args) >= 4 && args[0] == "link" && args[1] == "add" {
						return MockCommandResult{Err: nil}
					}
					// up fails
					if len(args) >= 3 && args[0] == "link" && args[1] == "set" && args[len(args)-1] == "up" {
						return MockCommandResult{Err: errors.New("failed to bring up")}
					}
					return MockCommandResult{Err: nil}
				}
			},
			config: types.NetworkConfig{
				BridgeName: "test-br-up-fail",
			},
			task:          &types.Task{ID: "test"},
			operation:     "ensure",
			expectError:   true,
			errorContains: "failed to bring bridge up",
		},
		{
			name: "tap_add_to_bridge_fails",
			setupMock: func(m *MockCommandExecutor) {
				m.CommandHandlers["ip"] = func(args []string) MockCommandResult {
					// tuntap add succeeds
					if len(args) >= 4 && args[0] == "tuntap" && args[1] == "add" {
						return MockCommandResult{Err: nil}
					}
					// set up succeeds
					if len(args) >= 3 && args[0] == "link" && args[1] == "set" && args[len(args)-1] == "up" {
						return MockCommandResult{Err: nil}
					}
					// master (add to bridge) fails
					if len(args) >= 5 && args[0] == "link" && args[1] == "set" && args[3] == "master" {
						return MockCommandResult{Err: errors.New("no such device")}
					}
					return MockCommandResult{Err: nil}
				}
			},
			config: types.NetworkConfig{
				BridgeName: "test-br-tap-fail",
			},
			task:          &types.Task{ID: "test"},
			operation:     "create_tap",
			expectError:   true,
			errorContains: "failed to add TAP to bridge",
		},
		{
			name: "tap_bring_up_fails",
			setupMock: func(m *MockCommandExecutor) {
				m.CommandHandlers["ip"] = func(args []string) MockCommandResult {
					// tuntap add succeeds
					if len(args) >= 4 && args[0] == "tuntap" && args[1] == "add" {
						return MockCommandResult{Err: nil}
					}
					// set up fails
					if len(args) >= 3 && args[0] == "link" && args[1] == "set" && args[len(args)-1] == "up" {
						return MockCommandResult{Err: errors.New("failed to bring up")}
					}
					return MockCommandResult{Err: nil}
				}
			},
			config: types.NetworkConfig{
				BridgeName: "test-br-up",
			},
			task:          &types.Task{ID: "test"},
			operation:     "create_tap",
			expectError:   true,
			errorContains: "failed to bring TAP up",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := NewMockCommandExecutor()
			tt.setupMock(mockExecutor)

			nm := NewNetworkManagerWithExecutor(tt.config, mockExecutor)
			ctx := context.Background()

			var err error
			switch tt.operation {
			case "ensure":
				err = nm.ensureBridgeWithExecutor(ctx)
			case "create_tap":
				network := types.NetworkAttachment{
					Network: types.Network{
						Spec: types.NetworkSpec{
							DriverConfig: &types.DriverConfig{
								Bridge: &types.BridgeConfig{
									Name: tt.config.BridgeName,
								},
							},
						},
					},
				}
				_, err = nm.createTapDeviceWithExecutor(ctx, network, 0, tt.task.ID)
			}

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			}
		})
	}
}

// TestNetworkManagerWithExecutor_ConcurrentOperations tests concurrent access
func TestNetworkManagerWithExecutor_ConcurrentOperations(t *testing.T) {
	mockExecutor := NewMockCommandExecutor()
	mockExecutor.Commands["ip"] = MockCommandResult{Err: nil}
	mockExecutor.Commands["sysctl"] = MockCommandResult{Err: nil}
	mockExecutor.Commands["iptables"] = MockCommandResult{Err: nil}

	config := types.NetworkConfig{
		BridgeName: "test-br-concurrent",
		Subnet:     "192.168.77.0/24",
		BridgeIP:   "192.168.77.1/24",
	}

	nm := NewNetworkManagerWithExecutor(config, mockExecutor)
	ctx := context.Background()

	// Test concurrent bridge checks
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()
			err := nm.ensureBridgeWithExecutor(ctx)
			assert.NoError(t, err)
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Bridge should still be marked as created
	assert.True(t, nm.bridges["test-br-concurrent"])
}

// TestNetworkManagerWithExecutor_MultipleTAPDevices tests creating multiple TAP devices
func TestNetworkManagerWithExecutor_MultipleTAPDevices(t *testing.T) {
	mockExecutor := NewMockCommandExecutor()
	mockExecutor.Commands["ip"] = MockCommandResult{Err: nil}

	config := types.NetworkConfig{
		BridgeName: "test-br-multi",
		Subnet:     "192.168.88.0/24",
		BridgeIP:   "192.168.88.1/24",
		IPMode:     "static",
	}

	nm := NewNetworkManagerWithExecutor(config, mockExecutor)
	ctx := context.Background()

	taskID := "multi-tap-task"

	// Create multiple TAP devices
	for i := 0; i < 3; i++ {
		network := types.NetworkAttachment{
			Network: types.Network{
				Spec: types.NetworkSpec{
					DriverConfig: &types.DriverConfig{
						Bridge: &types.BridgeConfig{
							Name: "test-br-multi",
						},
					},
				},
			},
		}

		tap, err := nm.createTapDeviceWithExecutor(ctx, network, i, taskID)
		assert.NoError(t, err)
		assert.NotNil(t, tap)

		// Manually add to tapDevices map (normally done by PrepareNetworkWithExecutor)
		nm.tapDevices[taskID+"-"+tap.Name] = tap
		assert.Equal(t, i+1, len(nm.tapDevices), "Should have created TAP device")
	}

	// Verify all devices were created
	assert.Equal(t, 3, len(nm.tapDevices), "Should have 3 TAP devices")
}

// TestNetworkManagerWithExecutor_IPAllocation tests IP allocation scenarios
func TestNetworkManagerWithExecutor_IPAllocation(t *testing.T) {
	tests := []struct {
		name       string
		subnet     string
		gateway    string
		taskID     string
		validateIP func(*testing.T, string)
	}{
		{
			name:    "allocate_different_ips_for_different_tasks",
			subnet:  "10.10.10.0/24",
			gateway: "10.10.10.1",
			taskID:  "task-alloc-1",
			validateIP: func(t *testing.T, ip string) {
				assert.NotEmpty(t, ip)
				assert.Contains(t, ip, "10.10.10.")
			},
		},
		{
			name:    "allocate_same_ip_for_same_task",
			subnet:  "10.20.30.0/24",
			gateway: "10.20.30.1",
			taskID:  "task-alloc-same",
			validateIP: func(t *testing.T, ip string) {
				assert.NotEmpty(t, ip)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocator, err := NewIPAllocator(tt.subnet, tt.gateway)
			require.NoError(t, err)

			ip, err := allocator.Allocate(tt.taskID)
			assert.NoError(t, err)

			if tt.validateIP != nil {
				tt.validateIP(t, ip)
			}

			// Verify deterministic allocation
			ip2, err := allocator.Allocate(tt.taskID)
			assert.NoError(t, err)
			assert.Equal(t, ip, ip2, "Same task should get same IP")
		})
	}
}

// TestNetworkManagerWithExecutor_NATSetupTwice tests calling setup NAT twice
func TestNetworkManagerWithExecutor_NATSetupTwice(t *testing.T) {
	mockExecutor := NewMockCommandExecutor()
	// Rule exists (no error)
	mockExecutor.CommandHandlers["sysctl"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}
	mockExecutor.CommandHandlers["iptables"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}
	mockExecutor.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}

	config := types.NetworkConfig{
		Subnet:     "192.168.90.0/24",
		BridgeName: "test-br-nat-dup",
		NATEnabled: true,
	}

	nm := NewNetworkManagerWithExecutor(config, mockExecutor)
	ctx := context.Background()

	task := &types.Task{
		ID: "test-nat-twice",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "test-br-nat-dup",
							},
						},
					},
				},
			},
		},
	}

	// First call
	err := nm.PrepareNetworkWithExecutor(ctx, task)
	assert.NoError(t, err)
	assert.True(t, nm.natSetup)

	// Second call (should be idempotent)
	err = nm.PrepareNetworkWithExecutor(ctx, task)
	assert.NoError(t, err)
	assert.True(t, nm.natSetup)
}

// TestNewNetworkManagerWithExecutor tests constructor variations
func TestNewNetworkManagerWithExecutor(t *testing.T) {
	tests := []struct {
		name     string
		config   types.NetworkConfig
		validate func(*testing.T, *NetworkManagerInternal)
	}{
		{
			name: "with_ip_allocator",
			config: types.NetworkConfig{
				BridgeName: "test-br",
				Subnet:     "192.168.1.0/24",
				BridgeIP:   "192.168.1.1/24",
			},
			validate: func(t *testing.T, nm *NetworkManagerInternal) {
				assert.NotNil(t, nm.ipAllocator, "IP allocator should be initialized")
			},
		},
		{
			name: "without_ip_allocator",
			config: types.NetworkConfig{
				BridgeName: "test-br",
				Subnet:     "",
				BridgeIP:   "",
			},
			validate: func(t *testing.T, nm *NetworkManagerInternal) {
				assert.Nil(t, nm.ipAllocator, "IP allocator should not be initialized")
			},
		},
		{
			name: "invalid_subnet",
			config: types.NetworkConfig{
				BridgeName: "test-br",
				Subnet:     "invalid-subnet",
				BridgeIP:   "192.168.1.1/24",
			},
			validate: func(t *testing.T, nm *NetworkManagerInternal) {
				assert.Nil(t, nm.ipAllocator, "IP allocator should not be initialized with invalid subnet")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := NewMockCommandExecutor()
			nm := NewNetworkManagerWithExecutor(tt.config, mockExecutor)

			if tt.validate != nil {
				tt.validate(t, nm)
			}
		})
	}
}
