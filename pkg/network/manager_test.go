package network

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TestIPAllocator_Allocate tests IP allocation.
func TestIPAllocator_Allocate(t *testing.T) {
	tests := []struct {
		name       string
		subnet     string
		gateway    string
		vmID       string
		wantPrefix string
	}{
		{
			name:       "allocate IP in subnet",
			subnet:     "192.168.127.0/24",
			gateway:    "192.168.127.1",
			vmID:       "vm-test-1",
			wantPrefix: "192.168.127.",
		},
		{
			name:       "allocate different IPs for different VMs",
			subnet:     "192.168.127.0/24",
			gateway:    "192.168.127.1",
			vmID:       "vm-test-2",
			wantPrefix: "192.168.127.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocator, err := NewIPAllocator(tt.subnet, tt.gateway)
			if err != nil {
				t.Fatalf("NewIPAllocator() error = %v", err)
			}

			ip, err := allocator.Allocate(tt.vmID)
			if err != nil {
				t.Fatalf("Allocate() error = %v", err)
			}

			if !strings.HasPrefix(ip, tt.wantPrefix) {
				t.Errorf("Allocate() IP = %v, want prefix %v", ip, tt.wantPrefix)
			}

			// Verify IP is in subnet
			parsedIP := net.ParseIP(ip)
			_, subnet, _ := net.ParseCIDR(tt.subnet)
			if !subnet.Contains(parsedIP) {
				t.Errorf("Allocate() IP %v not in subnet %v", ip, tt.subnet)
			}
		})
	}
}

// TestIPAllocator_Deterministic tests that same VM ID gets same IP.
func TestIPAllocator_Deterministic(t *testing.T) {
	allocator, err := NewIPAllocator("192.168.127.0/24", "192.168.127.1")
	if err != nil {
		t.Fatalf("NewIPAllocator() error = %v", err)
	}

	vmID := "vm-deterministic-test"

	ip1, err := allocator.Allocate(vmID)
	if err != nil {
		t.Fatalf("First Allocate() error = %v", err)
	}

	// Release and reallocate
	allocator.Release(ip1)
	ip2, err := allocator.Allocate(vmID)
	if err != nil {
		t.Fatalf("Second Allocate() error = %v", err)
	}

	if ip1 != ip2 {
		t.Errorf("Deterministic allocation failed: got %v, want %v", ip2, ip1)
	}
}

// TestIPAllocator_DifferentVMs tests that different VMs get different IPs.
func TestIPAllocator_DifferentVMs(t *testing.T) {
	allocator, err := NewIPAllocator("192.168.127.0/24", "192.168.127.1")
	if err != nil {
		t.Fatalf("NewIPAllocator() error = %v", err)
	}

	ip1, err := allocator.Allocate("vm-1")
	if err != nil {
		t.Fatalf("Allocate(vm-1) error = %v", err)
	}

	ip2, err := allocator.Allocate("vm-2")
	if err != nil {
		t.Fatalf("Allocate(vm-2) error = %v", err)
	}

	if ip1 == ip2 {
		t.Errorf("Different VMs should get different IPs: both got %v", ip1)
	}
}

// TestNewNetworkManager tests NetworkManager creation.
func TestNewNetworkManager(t *testing.T) {
	config := types.NetworkConfig{
		BridgeName: "test-br0",
		Subnet:     "192.168.127.0/24",
		BridgeIP:   "192.168.127.1/24",
		IPMode:     "static",
		NATEnabled: true,
	}

	nm := NewNetworkManager(config)
	if nm == nil {
		t.Fatal("NewNetworkManager() returned nil")
	}

	nmImpl, ok := nm.(*NetworkManager)
	if !ok {
		t.Fatal("NewNetworkManager() did not return *NetworkManager")
	}

	if nmImpl.ipAllocator == nil {
		t.Error("IP allocator not initialized")
	}
}

// TestNetworkManager_PrepareNetwork tests network preparation.
func TestNetworkManager_PrepareNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// Check if running as root
	if syscall.Geteuid() != 0 {
		t.Skip("skipping test: requires root privileges")
	}

	config := types.NetworkConfig{
		BridgeName: "test-br0",
		Subnet:     "192.168.127.0/24",
		BridgeIP:   "192.168.127.1/24",
		IPMode:     "static",
		NATEnabled: true,
	}

	nm := NewNetworkManager(config).(*NetworkManager)
	ctx := context.Background()

	task := &types.Task{
		ID: "test-task-1",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "nginx:alpine",
			},
		},
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

	// Prepare network
	err := nm.PrepareNetwork(ctx, task)
	if err != nil {
		t.Fatalf("PrepareNetwork() error = %v", err)
	}

	// Verify bridge exists
	output, err := exec.Command("ip", "link", "show", "test-br0").CombinedOutput()
	if err != nil {
		t.Errorf("Bridge not created: %v, output: %s", err, string(output))
	}

	// Verify TAP device exists
	devices := nm.ListTapDevices()
	if len(devices) != 1 {
		t.Errorf("Expected 1 TAP device, got %d", len(devices))
	}

	tap := devices[0]
	if tap.Name == "" {
		t.Error("TAP device name is empty")
	}

	// Verify TAP device exists in system
	output, err = exec.Command("ip", "link", "show", tap.Name).CombinedOutput()
	if err != nil {
		t.Errorf("TAP device not created: %v, output: %s", err, string(output))
	}

	// Verify IP is allocated
	if tap.IP == "" {
		t.Error("TAP device IP is empty")
	}

	// Cleanup
	err = nm.CleanupNetwork(ctx, task)
	if err != nil {
		t.Errorf("CleanupNetwork() error = %v", err)
	}

	// Verify TAP device is removed
	time.Sleep(100 * time.Millisecond)
	output, err = exec.Command("ip", "link", "show", tap.Name).CombinedOutput()
	if err == nil {
		t.Error("TAP device not removed after cleanup")
	}

	// Clean up bridge
	exec.Command("ip", "link", "delete", "test-br0").Run()
}

// TestNetworkManager_GetTapIP tests IP retrieval.
func TestNetworkManager_GetTapIP(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	if syscall.Geteuid() != 0 {
		t.Skip("skipping test: requires root privileges")
	}

	config := types.NetworkConfig{
		BridgeName: "test-br0",
		Subnet:     "192.168.127.0/24",
		BridgeIP:   "192.168.127.1/24",
		IPMode:     "static",
	}

	nm := NewNetworkManager(config).(*NetworkManager)
	ctx := context.Background()

	task := &types.Task{
		ID: "test-task-ip",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "nginx:alpine",
			},
		},
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

	err := nm.PrepareNetwork(ctx, task)
	if err != nil {
		t.Fatalf("PrepareNetwork() error = %v", err)
	}

	ip, err := nm.GetTapIP(task.ID)
	if err != nil {
		t.Errorf("GetTapIP() error = %v", err)
	}

	if ip == "" {
		t.Error("GetTapIP() returned empty IP")
	}

	// Verify IP is in subnet
	parsedIP := net.ParseIP(ip)
	_, subnet, _ := net.ParseCIDR(config.Subnet)
	if !subnet.Contains(parsedIP) {
		t.Errorf("IP %v not in subnet %v", ip, config.Subnet)
	}

	// Cleanup
	nm.CleanupNetwork(ctx, task)
	exec.Command("ip", "link", "delete", "test-br0").Run()
}

// TestNetworkManager_MultipleVMs tests multiple VMs get different IPs.
func TestNetworkManager_MultipleVMs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	if syscall.Geteuid() != 0 {
		t.Skip("skipping test: requires root privileges")
	}

	config := types.NetworkConfig{
		BridgeName: "test-br0",
		Subnet:     "192.168.127.0/24",
		BridgeIP:   "192.168.127.1/24",
		IPMode:     "static",
	}

	nm := NewNetworkManager(config).(*NetworkManager)
	ctx := context.Background()

	task1 := &types.Task{
		ID: "test-task-multiple-1",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "nginx:alpine",
			},
		},
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

	task2 := &types.Task{
		ID: "test-task-multiple-2",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "nginx:alpine",
			},
		},
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

	err := nm.PrepareNetwork(ctx, task1)
	if err != nil {
		t.Fatalf("PrepareNetwork(task1) error = %v", err)
	}

	err = nm.PrepareNetwork(ctx, task2)
	if err != nil {
		t.Fatalf("PrepareNetwork(task2) error = %v", err)
	}

	ip1, err := nm.GetTapIP(task1.ID)
	if err != nil {
		t.Errorf("GetTapIP(task1) error = %v", err)
	}

	ip2, err := nm.GetTapIP(task2.ID)
	if err != nil {
		t.Errorf("GetTapIP(task2) error = %v", err)
	}

	if ip1 == ip2 {
		t.Errorf("Different tasks should get different IPs: both got %v", ip1)
	}

	// Cleanup
	nm.CleanupNetwork(ctx, task1)
	nm.CleanupNetwork(ctx, task2)
	exec.Command("ip", "link", "delete", "test-br0").Run()
}

// TestNetworkManager_NoSubnet tests behavior when subnet is not configured.
func TestNetworkManager_NoSubnet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	if syscall.Geteuid() != 0 {
		t.Skip("skipping test: requires root privileges")
	}

	config := types.NetworkConfig{
		BridgeName: "test-br0",
		// No subnet configured
	}

	nm := NewNetworkManager(config).(*NetworkManager)
	ctx := context.Background()

	task := &types.Task{
		ID: "test-task-no-subnet",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "nginx:alpine",
			},
		},
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "network-1",
				},
			},
		},
	}

	err := nm.PrepareNetwork(ctx, task)
	if err != nil {
		t.Fatalf("PrepareNetwork() error = %v", err)
	}

	// TAP should be created but without IP
	devices := nm.ListTapDevices()
	if len(devices) != 1 {
		t.Errorf("Expected 1 TAP device, got %d", len(devices))
	}

	if devices[0].IP != "" {
		t.Errorf("Expected no IP when subnet not configured, got %v", devices[0].IP)
	}

	// Cleanup
	nm.CleanupNetwork(ctx, task)
	exec.Command("ip", "link", "delete", "test-br0").Run()
}
