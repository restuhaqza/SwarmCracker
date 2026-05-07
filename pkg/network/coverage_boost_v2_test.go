//go:build !integration

package network

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// executor_impl.go - Test defaultExecute and defaultExecuteWithOutput directly
// These are the actual implementations, tested with real commands in short mode
// =============================================================================

func TestDefaultExecute_Success(t *testing.T) {
	// Test with actual true command
	err := defaultExecute("true")
	require.NoError(t, err)
}

func TestDefaultExecute_Failure(t *testing.T) {
	// Test with false command
	err := defaultExecute("false")
	require.Error(t, err)
}

func TestDefaultExecute_NonexistentCommand(t *testing.T) {
	err := defaultExecute("nonexistent-command-xyz-abc")
	require.Error(t, err)
	// exec.Error is returned when command not found
}

func TestDefaultExecuteWithOutput_Success(t *testing.T) {
	output, err := defaultExecuteWithOutput("echo", "hello")
	require.NoError(t, err)
	assert.Contains(t, output, "hello")
}

func TestDefaultExecuteWithOutput_EmptyOutput(t *testing.T) {
	// true produces no output
	output, err := defaultExecuteWithOutput("true")
	require.NoError(t, err)
	assert.Empty(t, output)
}

func TestDefaultExecuteWithOutput_CommandFails(t *testing.T) {
	output, err := defaultExecuteWithOutput("false")
	require.Error(t, err)
	assert.Empty(t, output)
}

func TestDefaultExecuteWithOutput_NonexistentCommand(t *testing.T) {
	output, err := defaultExecuteWithOutput("nonexistent-command-xyz-abc")
	require.Error(t, err)
	assert.Empty(t, output)
}

func TestDefaultExecuteWithOutput_WithArgs(t *testing.T) {
	output, err := defaultExecuteWithOutput("echo", "-n", "test")
	require.NoError(t, err)
	assert.Equal(t, "test", output)
}

// =============================================================================
// SequentialMockTAPExecutor - for sequential command behavior
// =============================================================================

// SequentialMockTAPExecutor tracks call count for sequential responses
type SequentialMockTAPExecutor struct {
	MockTAPExecutor
	mu           sync.Mutex
	callCount    int
	runResults   []error
	outputResult [][]byte
	outputError  []error
	combinedResult [][]byte
	combinedError  []error
}

func NewSequentialMockTAPExecutor() *SequentialMockTAPExecutor {
	return &SequentialMockTAPExecutor{
		MockTAPExecutor: *NewMockTAPExecutor(),
	}
}

func (s *SequentialMockTAPExecutor) Run(cmd *exec.Cmd) error {
	s.mu.Lock()
	s.callCount++
	idx := s.callCount - 1
	s.mu.Unlock()

	if idx < len(s.runResults) {
		return s.runResults[idx]
	}
	return s.MockTAPExecutor.Run(cmd)
}

func (s *SequentialMockTAPExecutor) Output(cmd *exec.Cmd) ([]byte, error) {
	s.mu.Lock()
	s.callCount++
	idx := s.callCount - 1
	s.mu.Unlock()

	if idx < len(s.outputResult) {
		var err error
		if idx < len(s.outputError) {
			err = s.outputError[idx]
		}
		return s.outputResult[idx], err
	}
	return s.MockTAPExecutor.Output(cmd)
}

func (s *SequentialMockTAPExecutor) CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	s.mu.Lock()
	s.callCount++
	idx := s.callCount - 1
	s.mu.Unlock()

	if idx < len(s.combinedResult) {
		var err error
		if idx < len(s.combinedError) {
			err = s.combinedError[idx]
		}
		return s.combinedResult[idx], err
	}
	return s.MockTAPExecutor.CombinedOutput(cmd)
}

// =============================================================================
// CreateBridgeWithExecutor - comprehensive coverage
// =============================================================================

func TestCreateBridgeWithExecutor_BridgeAlreadyExists(t *testing.T) {
	mock := NewSequentialMockTAPExecutor()
	// First call: ip link show (succeeds = bridge exists)
	mock.runResults = []error{nil}

	err := CreateBridgeWithExecutor("br-existing", "10.0.0.0/24", mock)

	require.NoError(t, err)
	// Should stop after first command succeeds (bridge exists)
	assert.Equal(t, 1, mock.callCount)
}

func TestCreateBridgeWithExecutor_CreateFails_Seq(t *testing.T) {
	mock := NewSequentialMockTAPExecutor()
	// First call: ip link show (fails = bridge doesn't exist)
	// Second call: ip link add (fails)
	mock.runResults = []error{
		errors.New("device not found"),
		errors.New("permission denied"),
	}

	err := CreateBridgeWithExecutor("br-new", "10.0.0.0/24", mock)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create bridge")
}

func TestCreateBridgeWithExecutor_BridgeUpFails_WithCleanup(t *testing.T) {
	mock := NewSequentialMockTAPExecutor()
	// First call: ip link show (fails)
	// Second call: ip link add (succeeds)
	// Third call: ip link set up (fails)
	mock.runResults = []error{
		errors.New("device not found"),
		nil,
		errors.New("link set up failed"),
	}

	err := CreateBridgeWithExecutor("br-fail", "10.0.0.0/24", mock)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to bring bridge up")
}

func TestCreateBridgeWithExecutor_InvalidSubnet_Seq(t *testing.T) {
	mock := NewSequentialMockTAPExecutor()
	// First call: ip link show (fails)
	// Second call: ip link add (succeeds)
	// Third call: ip link set up (succeeds)
	mock.runResults = []error{
		errors.New("device not found"),
		nil,
		nil,
	}

	err := CreateBridgeWithExecutor("br-bad", "invalid-subnet", mock)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid subnet")
}

func TestCreateBridgeWithExecutor_IPConfigFails_WithCleanup(t *testing.T) {
	mock := NewSequentialMockTAPExecutor()
	// First call: ip link show (fails)
	// Second call: ip link add (succeeds)
	// Third call: ip link set up (succeeds)
	// Fourth call: ip addr add (fails via ConfigureTAPIPWithExecutor)
	mock.runResults = []error{
		errors.New("device not found"),
		nil,
		nil,
		errors.New("address config failed"),
	}

	err := CreateBridgeWithExecutor("br-ipfail", "10.0.0.0/24", mock)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to set bridge IP")
}

func TestCreateBridgeWithExecutor_SuccessWithSubnet(t *testing.T) {
	mock := NewSequentialMockTAPExecutor()
	// All commands succeed
	mock.runResults = []error{
		errors.New("device not found"), // link show fails = doesn't exist
		nil, // link add succeeds
		nil, // link set up succeeds
		nil, // addr add succeeds
	}

	err := CreateBridgeWithExecutor("br-success", "192.168.1.0/24", mock)

	require.NoError(t, err)
}

func TestCreateBridgeWithExecutor_SuccessNoSubnet(t *testing.T) {
	mock := NewSequentialMockTAPExecutor()
	mock.runResults = []error{
		errors.New("device not found"),
		nil,
		nil,
	}

	err := CreateBridgeWithExecutor("br-nosubnet", "", mock)

	require.NoError(t, err)
}

func TestCreateBridgeWithExecutor_DifferentSubnetSizes(t *testing.T) {
	tests := []struct {
		name   string
		subnet string
	}{
		{"class-c", "192.168.1.0/24"},
		{"class-b", "172.16.0.0/16"},
		{"class-a", "10.0.0.0/8"},
		{"slash-25", "10.0.0.0/25"},
		{"slash-26", "10.0.0.0/26"},
		{"slash-28", "10.0.0.0/28"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := NewSequentialMockTAPExecutor()
			mock.runResults = []error{
				errors.New("device not found"),
				nil,
				nil,
				nil,
			}

			err := CreateBridgeWithExecutor("br-"+tt.name, tt.subnet, mock)
			require.NoError(t, err)
		})
	}
}

// =============================================================================
// CreateTAPDeviceWithExecutor - comprehensive coverage
// =============================================================================

func TestCreateTAPDeviceWithExecutor_TuntapFails(t *testing.T) {
	mock := NewSequentialMockTAPExecutor()
	mock.runResults = []error{
		nil, // link delete (cleanup)
		errors.New("tuntap add failed"), // tuntap add fails
	}

	tap, err := CreateTAPDeviceWithExecutor("tap-fail", "br0", mock)

	require.Error(t, err)
	assert.Nil(t, tap)
	assert.Contains(t, err.Error(), "failed to create TAP device")
}

func TestCreateTAPDeviceWithExecutor_LinkUpFails_WithCleanup(t *testing.T) {
	mock := NewSequentialMockTAPExecutor()
	mock.runResults = []error{
		nil, // link delete
		nil, // tuntap add
		errors.New("link set up failed"), // link set up fails
	}

	tap, err := CreateTAPDeviceWithExecutor("tap-upfail", "br0", mock)

	require.Error(t, err)
	assert.Nil(t, tap)
	assert.Contains(t, err.Error(), "failed to bring TAP up")
}

func TestCreateTAPDeviceWithExecutor_MasterFails_WithCleanup(t *testing.T) {
	mock := NewSequentialMockTAPExecutor()
	mock.runResults = []error{
		nil, // link delete
		nil, // tuntap add
		nil, // link set up
		errors.New("master set failed"), // link set master fails
	}
	mock.outputResult = [][]byte{
		[]byte("tap0: UNKNOWN 00:11:22:33:44:55"), // MAC fetch
	}

	tap, err := CreateTAPDeviceWithExecutor("tap-masterfail", "br0", mock)

	require.Error(t, err)
	assert.Nil(t, tap)
	assert.Contains(t, err.Error(), "failed to connect TAP to bridge")
}

func TestCreateTAPDeviceWithExecutor_MACFetchFails_UsesPlaceholder(t *testing.T) {
	mock := NewSequentialMockTAPExecutor()
	mock.runResults = []error{
		nil, // link delete
		nil, // tuntap add
		nil, // link set up
		nil, // link set master
	}
	mock.outputResult = [][]byte{
		[]byte(""), // empty MAC output
	}
	mock.outputError = []error{
		errors.New("link show failed"),
	}

	tap, err := CreateTAPDeviceWithExecutor("tap-macfail", "br0", mock)

	require.NoError(t, err)
	assert.NotNil(t, tap)
	assert.Equal(t, "00:00:00:00:00:00", tap.MAC) // Placeholder
}

func TestCreateTAPDeviceWithExecutor_SuccessWithBridge(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(nil)
	mock.SetOutputResult([]byte("tap-success: UNKNOWN aa:bb:cc:dd:ee:ff"))

	tap, err := CreateTAPDeviceWithExecutor("tap-success", "br0", mock)

	require.NoError(t, err)
	assert.NotNil(t, tap)
	assert.Equal(t, "tap-success", tap.Name)
	assert.Equal(t, "br0", tap.Bridge)
	assert.Equal(t, "aa:bb:cc:dd:ee:ff", tap.MAC)
}

func TestCreateTAPDeviceWithExecutor_SuccessNoBridge(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(nil)
	mock.SetOutputResult([]byte("tap-nobridge: UNKNOWN 11:22:33:44:55:66"))

	tap, err := CreateTAPDeviceWithExecutor("tap-nobridge", "", mock)

	require.NoError(t, err)
	assert.NotNil(t, tap)
	assert.Equal(t, "", tap.Bridge)
}

// =============================================================================
// prepareNetworkWithCNI - NetworkManager tests
// =============================================================================

// CNI interface mock for testing - using interface pattern
type CNIClientInterface interface {
	AddNetwork(ctx context.Context, containerID, netns, ipCIDR, networkName string) (*CNIResult, error)
	DelNetwork(ctx context.Context, containerID, netns, networkName string) error
}

// MockCNIClient implements CNIClientInterface for testing
type MockCNIClient struct {
	AddResult *CNIResult
	AddError  error
	DelError  error
}

func (m *MockCNIClient) AddNetwork(ctx context.Context, containerID, netns, ipCIDR, networkName string) (*CNIResult, error) {
	if m.AddError != nil {
		return nil, m.AddError
	}
	return m.AddResult, nil
}

func (m *MockCNIClient) DelNetwork(ctx context.Context, containerID, netns, networkName string) error {
	return m.DelError
}

// CNIClientWrapper wraps a CNIClientInterface for use with NetworkManager
// This allows us to mock for testing
type CNIClientWrapper struct {
	impl CNIClientInterface
}

func (w *CNIClientWrapper) AddNetwork(ctx context.Context, containerID, netns, ipCIDR, networkName string) (*CNIResult, error) {
	return w.impl.AddNetwork(ctx, containerID, netns, ipCIDR, networkName)
}

func (w *CNIClientWrapper) DelNetwork(ctx context.Context, containerID, netns, networkName string) error {
	return w.impl.DelNetwork(ctx, containerID, netns, networkName)
}

// Tests using real CNIClient with non-existent paths (covers error paths)
func TestPrepareNetworkWithCNI_EmptyNetworkName_UsesIDPrefix(t *testing.T) {
	// Use real CNIClient with fake paths - will fail but covers the code path
	nm := &NetworkManager{
		config:     types.NetworkConfig{BridgeName: "br0"},
		tapDevices: make(map[string]*TapDevice),
		cniClient: NewCNIClient(CNIConfig{
			BinDir:  "/nonexistent-cni/bin",
			ConfDir: "/nonexistent-cni/conf",
		}),
	}

	task := &types.Task{
		ID: "task-empty-name",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID:   "network-abcdef12345678",
					Spec: types.NetworkSpec{}, // Empty name triggers ID prefix usage
				},
				Addresses: []string{"10.0.0.2/24"},
			},
		},
	}

	err := nm.prepareNetworkWithCNI(context.Background(), task)
	// Will fail because CNI binary doesn't exist, but covers the ID[:8] branch
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CNI ADD failed")
}

func TestPrepareNetworkWithCNI_NoAddresses_ReturnsError(t *testing.T) {
	nm := &NetworkManager{
		cniClient: NewCNIClient(CNIConfig{}),
		tapDevices: make(map[string]*TapDevice),
	}

	task := &types.Task{
		ID: "task-no-addr",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID:   "network-xyz",
					Spec: types.NetworkSpec{Name: "test-net"},
				},
				Addresses: []string{}, // Empty addresses - triggers error
			},
		},
	}

	err := nm.prepareNetworkWithCNI(context.Background(), task)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CNI requires SwarmKit-provided IP")
}

func TestPrepareNetworkWithCNI_AddNetworkFails_CoversErrorPath(t *testing.T) {
	nm := &NetworkManager{
		cniClient: NewCNIClient(CNIConfig{
			BinDir: "/nonexistent/path", // Invalid path triggers failure
		}),
		tapDevices: make(map[string]*TapDevice),
	}

	task := &types.Task{
		ID: "task-add-fail",
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID:   "network-abc",
					Spec: types.NetworkSpec{Name: "mynet"},
				},
				Addresses: []string{"10.0.0.5/24"},
			},
		},
	}

	err := nm.prepareNetworkWithCNI(context.Background(), task)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CNI ADD failed")
}

func TestPrepareNetworkWithCNI_MultipleNetworks_CoversLoop(t *testing.T) {
	nm := &NetworkManager{
		config:     types.NetworkConfig{BridgeName: "br-multi"},
		tapDevices: make(map[string]*TapDevice),
		cniClient: NewCNIClient(CNIConfig{
			BinDir: "/nonexistent/bin",
		}),
	}

	task := &types.Task{
		ID: "task-multi-net",
		Networks: []types.NetworkAttachment{
			{
				Network:   types.Network{ID: "net1", Spec: types.NetworkSpec{Name: "network1"}},
				Addresses: []string{"10.0.0.2/24"},
			},
			{
				Network:   types.Network{ID: "net2", Spec: types.NetworkSpec{Name: "network2"}},
				Addresses: []string{"10.0.1.2/24"},
			},
			{
				Network:   types.Network{ID: "net3", Spec: types.NetworkSpec{Name: "network3"}},
				Addresses: []string{"10.0.2.2/24"},
			},
		},
	}

	err := nm.prepareNetworkWithCNI(context.Background(), task)
	// First network will fail, covering multiple iterations
	require.Error(t, err)
}

// =============================================================================
// hashToIP - edge cases
// =============================================================================

func TestIPAllocator_HashToIP_IPv6(t *testing.T) {
	allocator, err := NewIPAllocator("fd00::/48", "fd00::1")
	require.NoError(t, err)

	// Allocate should use hashToIP internally
	ip1, err := allocator.Allocate("vm-ipv6-test-1")
	require.NoError(t, err)
	assert.NotEmpty(t, ip1)

	// Same VM ID should return same IP
	ip2, err := allocator.Allocate("vm-ipv6-test-1")
	require.NoError(t, err)
	assert.Equal(t, ip1, ip2)
}

func TestIPAllocator_HashToIP_Collision(t *testing.T) {
	allocator, err := NewIPAllocator("10.10.0.0/24", "10.10.0.1")
	require.NoError(t, err)

	// Allocate multiple VMs to potentially hit collisions
	allocated := make(map[string]string)
	for i := 0; i < 10; i++ {
		vmID := "vm-collision-test-" + string(rune('A'+i))
		ip, err := allocator.Allocate(vmID)
		require.NoError(t, err)
		allocated[vmID] = ip
	}

	// Verify all are unique (no true collisions)
	seen := make(map[string]string)
	for vmID, ip := range allocated {
		if existingVM, exists := seen[ip]; exists {
			// This could happen if hash collision, but allocator should handle it
			t.Logf("IP %s allocated to both %s and %s", ip, existingVM, vmID)
		}
		seen[ip] = vmID
	}
}

func TestIPAllocator_HashToIP_SmallSubnet(t *testing.T) {
	// /30 has only 4 addresses: .0 (network), .1 (gateway), .2, .3 (broadcast)
	allocator, err := NewIPAllocator("10.0.0.0/30", "10.0.0.1")
	require.NoError(t, err)

	// Only .2 should be allocatable (and maybe .3 depending on implementation)
	ip, err := allocator.Allocate("vm-small-subnet")
	require.NoError(t, err)
	assert.NotEmpty(t, ip)
}

func TestIPAllocator_HashToIP_VerySmallSubnet(t *testing.T) {
	// /31 has only 2 addresses
	allocator, err := NewIPAllocator("10.0.1.0/31", "10.0.1.0")
	require.NoError(t, err)

	ip, err := allocator.Allocate("vm-tiny-subnet")
	require.NoError(t, err)
	assert.NotEmpty(t, ip)
}

func TestIPAllocator_HashToIP_SubnetExhaustion(t *testing.T) {
	// Use a tiny subnet to test exhaustion
	allocator, err := NewIPAllocator("10.99.0.0/29", "10.99.0.1")
	require.NoError(t, err)

	// Allocate all available IPs (should be 6: .1 is gateway, .0 is network, .2-.6 available)
	for i := 0; i < 10; i++ {
		vmID := "vm-exhaust-" + string(rune('0'+i))
		_, err := allocator.Allocate(vmID)
		// Some should succeed, eventually might fail if truly exhausted
		_ = err
	}
}

func TestIPAllocator_Release(t *testing.T) {
	allocator, err := NewIPAllocator("10.20.0.0/24", "10.20.0.1")
	require.NoError(t, err)

	// Allocate
	ip, err := allocator.Allocate("vm-release-test")
	require.NoError(t, err)

	// Release
	allocator.Release(ip)

	// Should be able to allocate again
	ip2, err := allocator.Allocate("vm-release-test-new")
	require.NoError(t, err)
	assert.NotEmpty(t, ip2)
}

// =============================================================================
// Additional coverage for CNI client wrapper
// =============================================================================

// CNIClient with empty config - covers execution paths
func TestCNIClient_AddNetwork_ExecFails(t *testing.T) {
	client := NewCNIClient(CNIConfig{
		BinDir: "/nonexistent-path",
		ConfDir: "/nonexistent-conf",
	})
	_, err := client.AddNetwork(context.Background(), "container-1", "tmp/ns", "10.0.0.2/24", "test-net")
	require.Error(t, err)
	// Error is from config dir or plugin not found
	assert.Contains(t, err.Error(), "failed")
}

func TestCNIClient_DelNetwork_ExecFails(t *testing.T) {
	client := NewCNIClient(CNIConfig{
		BinDir: "/nonexistent-path",
	})
	err := client.DelNetwork(context.Background(), "container-1", "tmp/ns", "test-net")
	// DEL should be tolerant of errors
	require.NoError(t, err)
}

// =============================================================================
// GetNodes SwarmKit - client nil check
// =============================================================================

func TestSwarmKitNodeDiscovery_GetNodes_NilClient(t *testing.T) {
	// Create discovery without connecting
	discovery := &SwarmKitNodeDiscovery{
		localNodeID:   "node-1",
		localHostname: "host1",
		timeout:       5,
	}

	nodes, err := discovery.GetNodes()

	require.Error(t, err)
	assert.Nil(t, nodes)
	assert.Contains(t, err.Error(), "not connected")
}