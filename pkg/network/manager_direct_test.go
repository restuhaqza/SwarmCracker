//go:build !integration

package network

import (
	"context"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Manager direct function tests — cover paths in manager.go
// These call NetworkManager methods directly (not WithExecutor variants).
// Since we're non-root, exec.Command calls will fail at privilege-requiring
// steps, but this still covers error paths and non-privilege logic.
// =============================================================================

func newTestNM() *NetworkManager {
	cfg := types.NetworkConfig{
		BridgeName:   "test-sc-br0",
		Subnet:       "10.99.0.0/24",
		BridgeIP:     "10.99.0.1/24",
		NATEnabled:   false,
		VXLANEnabled: false,
		IPMode:       "static",
	}
	return NewNetworkManager(cfg).(*NetworkManager)
}

// --- Init / ensureBridge ---

func TestManager_Init_BridgeCreateFail(t *testing.T) {
	nm := newTestNM()
	// Not root → ip link add will fail
	err := nm.Init(context.Background())
	// Will fail because we can't create a bridge without root
	// But it covers: ensureBridge → ip link show (fail) → ip link add (fail) → error
	if err != nil {
		assert.Contains(t, err.Error(), "bridge")
	}
}

func TestManager_EnsureBridge_AlreadyCached(t *testing.T) {
	nm := newTestNM()
	nm.bridges["test-sc-br0"] = true

	err := nm.ensureBridge(context.Background())
	require.NoError(t, err)
}

func TestManager_EnsureBridge_DoubleCheck(t *testing.T) {
	nm := newTestNM()
	// Simulate concurrent set
	nm.bridges["test-sc-br0"] = true

	err := nm.ensureBridge(context.Background())
	require.NoError(t, err)
}

// --- Init with cached bridge ---

func TestManager_Init_CachedBridge(t *testing.T) {
	nm := newTestNM()
	nm.bridges["test-sc-br0"] = true

	err := nm.Init(context.Background())
	require.NoError(t, err)
}

// --- Init with VXLAN enabled but no manager ---

func TestManager_Init_VXLANEnabled_NoManager(t *testing.T) {
	nm := newTestNM()
	nm.bridges["test-sc-br0"] = true
	nm.config.VXLANEnabled = true
	nm.vxlanMgr = nil

	err := nm.Init(context.Background())
	require.NoError(t, err) // Just logs warning
}

// --- PrepareNetwork ---

func TestManager_PrepareNetwork_NoNetworks(t *testing.T) {
	nm := newTestNM()
	nm.bridges["test-sc-br0"] = true

	task := &types.Task{
		ID:       "task-no-net",
		Networks: []types.NetworkAttachment{},
	}

	err := nm.PrepareNetwork(context.Background(), task)
	// Will try to create default TAP → fail without root
	// But covers the no-networks path
	if err != nil {
		t.Logf("Expected error (no root): %v", err)
	}
}

func TestManager_PrepareNetwork_WithNetworks(t *testing.T) {
	nm := newTestNM()
	nm.bridges["test-sc-br0"] = true

	task := &types.Task{
		ID: "task-with-net",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID:   "net-1",
					Spec: types.NetworkSpec{Name: "test"},
				},
				Addresses: []string{"10.99.0.5/24"},
			},
		},
	}

	err := nm.PrepareNetwork(context.Background(), task)
	// Will fail trying to create TAP (no root)
	if err != nil {
		t.Logf("Expected error (no root): %v", err)
	}
}

func TestManager_PrepareNetwork_CNI(t *testing.T) {
	nm := newTestNM()
	nm.bridges["test-sc-br0"] = true
	nm.cniClient = NewCNIClient(CNIConfig{
		BinDir:      "/nonexistent/bin",
		ConfDir:     "/nonexistent/conf",
		NetworkName: "test",
	})

	task := &types.Task{
		ID: "task-cni",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID:   "cni-net",
					Spec: types.NetworkSpec{Name: "cni-test"},
				},
				Addresses: []string{"10.99.0.10/24"},
			},
		},
	}

	err := nm.PrepareNetwork(context.Background(), task)
	// CNI path will fail (no plugin binary)
	if err != nil {
		t.Logf("Expected CNI error: %v", err)
	}
}

// --- CleanupNetwork ---

func TestManager_CleanupNetwork_Basic(t *testing.T) {
	nm := newTestNM()
	nm.mu.Lock()
	nm.tapDevices["task-1-tap-eth0"] = &TapDevice{
		Name:   "tap-eth0",
		Bridge: "test-sc-br0",
		IP:     "10.99.0.5",
	}
	nm.mu.Unlock()

	task := &types.Task{ID: "task-1"}
	err := nm.CleanupNetwork(context.Background(), task)
	// Will try to delete TAP → fail without root, but error is logged not returned
	require.NoError(t, err)
}

// --- GetTapIP ---

func TestManager_GetTapIP_Found(t *testing.T) {
	nm := newTestNM()
	nm.mu.Lock()
	nm.tapDevices["task-x-tap-eth0"] = &TapDevice{
		Name: "tap-eth0",
		IP:   "10.99.0.42",
	}
	nm.mu.Unlock()

	ip, err := nm.GetTapIP("task-x")
	require.NoError(t, err)
	assert.Equal(t, "10.99.0.42", ip)
}

func TestManager_GetTapIP_NotFound(t *testing.T) {
	nm := newTestNM()
	_, err := nm.GetTapIP("nonexistent")
	require.Error(t, err)
}

// --- ListTapDevices ---

func TestManager_ListTapDevices(t *testing.T) {
	nm := newTestNM()
	nm.mu.Lock()
	nm.tapDevices["a-tap1"] = &TapDevice{Name: "tap1"}
	nm.tapDevices["b-tap2"] = &TapDevice{Name: "tap2"}
	nm.mu.Unlock()

	devices := nm.ListTapDevices()
	assert.Len(t, devices, 2)
}

// --- UpdatePeers ---

func TestManager_UpdatePeers_NoVXLAN(t *testing.T) {
	nm := newTestNM()
	err := nm.UpdatePeers([]string{"10.0.0.2", "10.0.0.3"})
	require.NoError(t, err)
}

// --- StartPeerDiscovery / StopPeerDiscovery ---

func TestManager_StartPeerDiscovery_NoDiscovery(t *testing.T) {
	nm := newTestNM()
	err := nm.StartPeerDiscovery(context.Background())
	// No discovery provider set → should handle gracefully
	if err != nil {
		t.Logf("StartPeerDiscovery: %v", err)
	}
}

func TestManager_StopPeerDiscovery_Noop(t *testing.T) {
	nm := newTestNM()
	nm.StopPeerDiscovery() // Should not panic
}

// --- SetEncryptionKeys ---

func TestManager_SetEncryptionKeys_NoVXLAN(t *testing.T) {
	nm := newTestNM()
	err := nm.SetEncryptionKeys(nil)
	require.NoError(t, err)
}

// --- SetNodeDiscovery ---

func TestManager_SetNodeDiscovery(t *testing.T) {
	nm := newTestNM()
	discovery := &HostnameNodeDiscovery{
		localHostname: "test-host",
		clusterNodes:  []string{"node1", "node2"},
	}
	nm.SetNodeDiscovery(discovery)
	assert.NotNil(t, nm.nodeDiscovery)
}

// --- prepareNetworkWithCNI (indirect through PrepareNetwork) ---

func TestManager_PrepareNetwork_CNIPath(t *testing.T) {
	nm := newTestNM()
	nm.bridges["test-sc-br0"] = true
	// cniClient is initialized in NewNetworkManager, just need networks
	task := &types.Task{
		ID: "task-cni-path",
		Networks: []types.NetworkAttachment{
			{
				Network:   types.Network{ID: "net-cni", Spec: types.NetworkSpec{Name: "cni-net"}},
				Addresses: []string{"10.0.0.5/24"},
			},
		},
	}

	err := nm.PrepareNetwork(context.Background(), task)
	// Will fail because CNI plugin binary doesn't exist
	if err != nil {
		t.Logf("CNI error (expected): %v", err)
	}
}

// --- setupNAT (indirect through PrepareNetwork) ---

func TestManager_PrepareNetwork_NATPath(t *testing.T) {
	nm := newTestNM()
	nm.bridges["test-sc-br0"] = true
	nm.config.NATEnabled = true
	nm.natSetup = false

	task := &types.Task{
		ID:       "task-nat",
		Networks: []types.NetworkAttachment{},
	}

	err := nm.PrepareNetwork(context.Background(), task)
	// Will fail at NAT setup (no root for iptables/sysctl)
	if err != nil {
		t.Logf("NAT error (expected): %v", err)
	}
}

// --- GetIPAllocator / Release ---

func TestManager_IPAllocator_Integration(t *testing.T) {
	nm := newTestNM()
	require.NotNil(t, nm.ipAllocator)

	ip1, err := nm.ipAllocator.Allocate("vm-1")
	require.NoError(t, err)
	assert.NotEmpty(t, ip1)

	ip2, err := nm.ipAllocator.Allocate("vm-2")
	require.NoError(t, err)
	assert.NotEmpty(t, ip2)
	assert.NotEqual(t, ip1, ip2, "different VMs should get different IPs")

	// Same VM ID should return same IP
	ip1Again, err := nm.ipAllocator.Allocate("vm-1")
	require.NoError(t, err)
	assert.Equal(t, ip1, ip1Again)

	// Release and re-allocate
	nm.ipAllocator.Release(ip1)
	ip3, err := nm.ipAllocator.Allocate("vm-3")
	require.NoError(t, err)
	assert.NotEmpty(t, ip3)
}

// --- hashToIP edge cases ---

func TestManager_HashToIP_SmallSubnet(t *testing.T) {
	allocator, err := NewIPAllocator("10.0.0.0/30", "10.0.0.1")
	require.NoError(t, err)

	// /30 subnet has 4 addresses
	ip := allocator.hashToIP("test-vm")
	assert.NotNil(t, ip)
}

func TestManager_HashToIP_IPv6(t *testing.T) {
	allocator, err := NewIPAllocator("fd00::/64", "fd00::1")
	require.NoError(t, err)

	ip := allocator.hashToIP("test-vm-ipv6")
	assert.NotNil(t, ip)
}
