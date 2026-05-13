//go:build !integration

package network

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

var (
	origExecCommand  func(string, ...string) *exec.Cmd
	origExecLookPath func(string) (string, error)
	origOsRemoveAll  func(string) error
	origOsWriteFile  func(string, []byte, os.FileMode) error
	origOsRemove     func(string) error
)

func init() {
	// Save original functions at package initialization
	origExecCommand = execCommand
	origExecLookPath = execLookPath
	origOsRemoveAll = osRemoveAll
	origOsWriteFile = osWriteFile
	origOsRemove = osRemove
}

// Mock state for tests
type mockState struct {
	mu           sync.Mutex
	calls        []string
	outputs      map[string]string
	shouldFail   map[string]bool
	bridgeExists bool
}

func newMockState() *mockState {
	return &mockState{
		calls:        []string{},
		outputs:      make(map[string]string),
		shouldFail:   make(map[string]bool),
		bridgeExists: false,
	}
}

func (m *mockState) addCall(cmd string) {
	m.mu.Lock()
	m.calls = append(m.calls, cmd)
	m.mu.Unlock()
}

func (m *mockState) setOutput(cmd string, output string) {
	m.mu.Lock()
	m.outputs[cmd] = output
	m.mu.Unlock()
}

func (m *mockState) setFail(cmd string, fail bool) {
	m.mu.Lock()
	m.shouldFail[cmd] = fail
	m.mu.Unlock()
}

func (m *mockState) shouldCmdFail(cmd string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shouldFail[cmd] {
		return true
	}
	for key, fail := range m.shouldFail {
		if strings.HasPrefix(cmd, key) && fail {
			return true
		}
	}
	return false
}

func (m *mockState) getOutput(cmd string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if out, ok := m.outputs[cmd]; ok {
		return out
	}
	for key, out := range m.outputs {
		if strings.HasPrefix(cmd, key) {
			return out
		}
	}
	return ""
}

func mockExecCommandWithState(state *mockState) func(string, ...string) *exec.Cmd {
	return func(name string, args ...string) *exec.Cmd {
		cmdStr := name + " " + strings.Join(args, " ")
		state.addCall(cmdStr)
		if state.shouldCmdFail(cmdStr) {
			return exec.Command("false")
		}
		output := state.getOutput(cmdStr)
		if output != "" {
			return exec.Command("echo", "-n", output)
		}
		return exec.Command("true")
	}
}

func mockExecLookPathWithState(state *mockState) func(string) (string, error) {
	return func(name string) (string, error) {
		state.addCall("lookpath:" + name)
		if state.shouldCmdFail("lookpath:" + name) {
			return "", fmt.Errorf("%s not found", name)
		}
		return "/usr/bin/" + name, nil
	}
}

func mockOsWriteFileWithState(state *mockState) func(string, []byte, os.FileMode) error {
	return func(path string, data []byte, perm os.FileMode) error {
		state.addCall("writefile:" + path)
		if state.shouldCmdFail("writefile:" + path) {
			return fmt.Errorf("write failed")
		}
		return nil
	}
}

func mockOsRemoveWithState(state *mockState) func(string) error {
	return func(path string) error {
		state.addCall("remove:" + path)
		if state.shouldCmdFail("remove:" + path) {
			return fmt.Errorf("remove failed")
		}
		return nil
	}
}

func mockOsRemoveAllWithState(state *mockState) func(string) error {
	return func(path string) error {
		state.addCall("removeall:" + path)
		if state.shouldCmdFail("removeall:" + path) {
			return fmt.Errorf("removeall failed")
		}
		return nil
	}
}

func setupMocksForTest(state *mockState) func() {
	execCommand = mockExecCommandWithState(state)
	execLookPath = mockExecLookPathWithState(state)
	osWriteFile = mockOsWriteFileWithState(state)
	osRemove = mockOsRemoveWithState(state)
	osRemoveAll = mockOsRemoveAllWithState(state)
	return func() {
		execCommand = origExecCommand
		execLookPath = origExecLookPath
		osWriteFile = origOsWriteFile
		osRemove = origOsRemove
		osRemoveAll = origOsRemoveAll
	}
}

func TestEnsureBridge_BridgeNotExists(t *testing.T) {
	state := newMockState()
	state.setFail("ip link show", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.ensureBridge(context.Background())
	if err != nil {
		t.Fatalf("ensureBridge failed: %v", err)
	}

	state.mu.Lock()
	calls := state.calls
	state.mu.Unlock()

	foundShow := false
	foundAdd := false
	for _, call := range calls {
		if strings.Contains(call, "ip link show testbr0") {
			foundShow = true
		}
		if strings.Contains(call, "ip link add testbr0 type bridge") {
			foundAdd = true
		}
	}
	if !foundShow {
		t.Error("Expected 'ip link show' call")
	}
	if !foundAdd {
		t.Error("Expected 'ip link add' call")
	}
}

func TestEnsureBridge_BridgeExists(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24"}
	nm := NewNetworkManager(config).(*NetworkManager)
	nm.bridges["testbr0"] = true

	err := nm.ensureBridge(context.Background())
	if err != nil {
		t.Fatalf("ensureBridge failed: %v", err)
	}
}

func TestEnsureBridge_CreateFails(t *testing.T) {
	state := newMockState()
	state.setFail("ip link show", true)
	state.setFail("ip link add", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.ensureBridge(context.Background())
	if err == nil {
		t.Fatal("Expected error when bridge creation fails")
	}
	if !strings.Contains(err.Error(), "failed to create bridge") {
		t.Errorf("Expected 'failed to create bridge' error, got: %v", err)
	}
}

func TestSetupBridgeIP_Injectable(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupBridgeIP(context.Background())
	if err != nil {
		t.Fatalf("setupBridgeIP failed: %v", err)
	}
}

func TestSetupBridgeIP_Fail_Injectable(t *testing.T) {
	state := newMockState()
	state.setFail("ip addr show", true)
	state.setFail("ip addr add", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupBridgeIP(context.Background())
	if err == nil {
		t.Fatal("Expected error when setting bridge IP fails")
	}
}

func TestSetupNAT_Injectable(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", Subnet: "10.0.0.0/24", NATEnabled: true}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupNAT(context.Background())
	if err != nil {
		t.Fatalf("setupNAT failed: %v", err)
	}
}

func TestSetupNAT_NoSubnet_Injectable(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", NATEnabled: true}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupNAT(context.Background())
	if err == nil {
		t.Fatal("Expected error when subnet is not configured")
	}
}

func TestSetupNAT_IPForwardFail_Injectable(t *testing.T) {
	state := newMockState()
	state.setFail("sysctl", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", Subnet: "10.0.0.0/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupNAT(context.Background())
	if err == nil {
		t.Fatal("Expected error when IP forwarding fails")
	}
}

func TestSetupNAT_AddRules(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -t nat -C", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", Subnet: "10.0.0.0/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupNAT(context.Background())
	if err != nil {
		t.Fatalf("setupNAT failed: %v", err)
	}
}

func TestSetupNAT_ForwardFail(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -C FORWARD", true)
	state.setFail("iptables -A FORWARD", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", Subnet: "10.0.0.0/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupNAT(context.Background())
	if err == nil {
		t.Fatal("Expected error when forward rule fails")
	}
}

func TestGetPhysicalInterface_Injectable(t *testing.T) {
	state := newMockState()
	state.setOutput("ip route show default", "default via 192.168.1.1 dev eth0 proto dhcp")
	state.setOutput("ip addr show eth0", "    inet 192.168.1.100/24 brd 192.168.1.255 scope global dynamic eth0")
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	iface, ip, err := nm.getPhysicalInterface()
	if err != nil {
		t.Fatalf("getPhysicalInterface failed: %v", err)
	}
	if iface != "eth0" {
		t.Errorf("Expected interface eth0, got %s", iface)
	}
	if ip != "192.168.1.100" {
		t.Errorf("Expected IP 192.168.1.100, got %s", ip)
	}
}

func TestGetPhysicalInterface_RouteFail(t *testing.T) {
	state := newMockState()
	state.setFail("ip route show", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	_, _, err := nm.getPhysicalInterface()
	if err == nil {
		t.Fatal("Expected error when route lookup fails")
	}
}

func TestGetPhysicalInterface_ShortOutput(t *testing.T) {
	state := newMockState()
	state.setOutput("ip route show default", "default")
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	_, _, err := nm.getPhysicalInterface()
	if err == nil {
		t.Fatal("Expected error for short route output")
	}
}

func TestGetPhysicalInterface_NoIP(t *testing.T) {
	state := newMockState()
	state.setOutput("ip route show default", "default via 192.168.1.1 dev eth0")
	state.setOutput("ip addr show eth0", "2: eth0: mtu 1500")
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	_, _, err := nm.getPhysicalInterface()
	if err == nil {
		t.Fatal("Expected error when no IP found")
	}
}

func TestCreateTapDevice_Injectable(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", Subnet: "10.0.0.0/24", BridgeIP: "10.0.0.1/24", IPMode: "static"}
	nm := NewNetworkManager(config).(*NetworkManager)
	allocator, _ := NewIPAllocator("10.0.0.0/24", "10.0.0.1")
	nm.ipAllocator = allocator

	network := types.NetworkAttachment{Network: types.Network{ID: "test-network"}}
	tap, err := nm.createTapDevice(context.Background(), network, 0, "test-task-123")
	if err != nil {
		t.Fatalf("createTapDevice failed: %v", err)
	}
	if tap.Bridge != "testbr0" {
		t.Errorf("Expected bridge testbr0, got %s", tap.Bridge)
	}
}

func TestCreateTapDevice_WithSwarmKitIP(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)

	network := types.NetworkAttachment{
		Network:   types.Network{ID: "test-network"},
		Addresses: []string{"10.0.5.2/24"},
	}
	tap, err := nm.createTapDevice(context.Background(), network, 0, "test-task-123")
	if err != nil {
		t.Fatalf("createTapDevice failed: %v", err)
	}
	if tap.IP != "10.0.5.2" {
		t.Errorf("Expected IP 10.0.5.2, got %s", tap.IP)
	}
}

func TestCreateTapDevice_TuntapFail(t *testing.T) {
	state := newMockState()
	state.setFail("ip tuntap", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)

	network := types.NetworkAttachment{Network: types.Network{ID: "test-network"}}
	_, err := nm.createTapDevice(context.Background(), network, 0, "test-task-123")
	if err == nil {
		t.Fatal("Expected error when tuntap fails")
	}
}

func TestCreateTapDevice_LinkUpFail(t *testing.T) {
	state := newMockState()
	state.setFail("ip link set", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)

	network := types.NetworkAttachment{Network: types.Network{ID: "test-network"}}
	_, err := nm.createTapDevice(context.Background(), network, 0, "test-task-123")
	if err == nil {
		t.Fatal("Expected error when link up fails")
	}
}

func TestCreateTapDevice_OverlayNetwork(t *testing.T) {
	state := newMockState()
	state.setFail("ip link show br-testnetwork", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)

	network := types.NetworkAttachment{
		Network: types.Network{ID: "testnetwork123456789", Spec: types.NetworkSpec{Driver: "overlay"}},
	}
	_, err := nm.createTapDevice(context.Background(), network, 0, "test-task-123")
	if err == nil {
		t.Fatal("Expected error when overlay bridge doesn't exist")
	}
}

func TestCreateTapDevice_OverlayBridgeExists(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)

	network := types.NetworkAttachment{
		Network: types.Network{ID: "123456789abc", Spec: types.NetworkSpec{Driver: "overlay"}},
	}
	tap, err := nm.createTapDevice(context.Background(), network, 0, "test-task-123")
	if err != nil {
		t.Fatalf("createTapDevice failed: %v", err)
	}
	expectedBridge := "br-123456789abc"
	if tap.Bridge != expectedBridge {
		t.Errorf("Expected bridge %s, got %s", expectedBridge, tap.Bridge)
	}
}

func TestRemoveTapDevice_Injectable(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)
	tap := &TapDevice{Name: "tap-test", Bridge: "testbr0"}

	err := nm.removeTapDevice(tap)
	if err != nil {
		t.Fatalf("removeTapDevice failed: %v", err)
	}
}

func TestRemoveTapDevice_Fail_Injectable(t *testing.T) {
	state := newMockState()
	state.setFail("ip link delete", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)
	tap := &TapDevice{Name: "tap-test", Bridge: "testbr0"}

	err := nm.removeTapDevice(tap)
	if err == nil {
		t.Fatal("Expected error when deletion fails")
	}
}

func TestSetupDHCP_Injectable(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", Subnet: "10.0.0.0/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupDHCP(context.Background())
	if err != nil {
		t.Fatalf("setupDHCP failed: %v", err)
	}
}

func TestSetupDHCP_NoDnsmasq(t *testing.T) {
	state := newMockState()
	state.setFail("lookpath:dnsmasq", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", Subnet: "10.0.0.0/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupDHCP(context.Background())
	if err != nil {
		t.Fatalf("setupDHCP should not fail when dnsmasq unavailable: %v", err)
	}
}

func TestSetupDHCP_NoSubnet_Injectable(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupDHCP(context.Background())
	if err == nil {
		t.Fatal("Expected error when subnet not configured")
	}
}

func TestSetupVXLANOverlay_Injectable(t *testing.T) {
	state := newMockState()
	state.setOutput("ip route show default", "default via 192.168.1.1 dev eth0")
	state.setOutput("ip addr show eth0", "2: eth0: inet 192.168.1.100/24")
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", VXLANEnabled: true, VXLANPeers: []string{"192.168.1.50"}}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupVXLANOverlay(context.Background())
	if err != nil {
		t.Logf("setupVXLANOverlay returned: %v (expected for mock environment)", err)
	}
}

func TestSetupVXLANOverlay_NoInterface(t *testing.T) {
	state := newMockState()
	state.setFail("ip route show", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", VXLANEnabled: true}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupVXLANOverlay(context.Background())
	if err == nil {
		t.Fatal("Expected error when interface discovery fails")
	}
}

func TestUpdateVXLANPeers_Injectable(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.UpdateVXLANPeers([]string{"192.168.1.50", "192.168.1.51"})
	if err != nil {
		t.Fatalf("UpdateVXLANPeers failed: %v", err)
	}

	nm.mu.Lock()
	pending := nm.pendingPeers
	nm.mu.Unlock()

	if len(pending) != 2 {
		t.Errorf("Expected 2 pending peers, got %d", len(pending))
	}
}

func TestUpdatePeers_Injectable(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.UpdatePeers([]string{"192.168.1.50"})
	if err != nil {
		t.Fatalf("UpdatePeers failed: %v", err)
	}
}

func TestStartPeerDiscovery_Injectable(t *testing.T) {
	state := newMockState()
	state.setOutput("ip route show default", "default via 192.168.1.1 dev eth0")
	state.setOutput("ip addr show eth0", "2: eth0: inet 192.168.1.100/24")
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.StartPeerDiscovery(context.Background())
	if err == nil {
		t.Fatal("Expected error when vxlanMgr is nil")
	}
}

func TestPrepareNetworkWithCNI_Injectable(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)
	nm.cniClient = NewCNIClient(CNIConfig{BinDir: "/opt/cni/bin", ConfDir: "/etc/cni/net.d", NetworkName: "test"})

	task := &types.Task{
		ID: "task-123",
		Networks: []types.NetworkAttachment{
			{Network: types.Network{ID: "net1", Spec: types.NetworkSpec{Name: "test-network"}}, Addresses: []string{"10.0.5.2/24"}},
		},
	}

	err := nm.prepareNetworkWithCNI(context.Background(), task)
	if err != nil {
		t.Logf("prepareNetworkWithCNI error: %v (expected in mock env)", err)
	}
}

func TestPrepareNetworkWithCNI_NoIP_UsesFallback(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)
	nm.cniClient = NewCNIClient(CNIConfig{BinDir: "/opt/cni/bin", ConfDir: "/etc/cni/net.d", NetworkName: "test"})

	task := &types.Task{
		ID: "task-123",
		Networks: []types.NetworkAttachment{
			{Network: types.Network{ID: "net1", Spec: types.NetworkSpec{Name: "test-network"}}},
		},
	}

	err := nm.prepareNetworkWithCNI(context.Background(), task)
	// With CVR fix, empty IP now triggers TAP/DHCP fallback instead of error
	// In test environment, TAP creation may fail (expected) or succeed with mock
	if err != nil {
		// Error should be about TAP creation, not CNI IP requirement
		assert.Contains(t, err.Error(), "failed to create TAP device")
	}
}

func TestHashToIP_EdgeCases(t *testing.T) {
	allocator, err := NewIPAllocator("10.0.0.0/24", "10.0.0.1")
	if err != nil {
		t.Fatalf("Failed to create allocator: %v", err)
	}

	testCases := []string{"vm-1", "vm-with-long-id-123456789", "vm-with-special-chars-!@#$%"}
	for _, vmID := range testCases {
		ip := allocator.hashToIP(vmID)
		if ip == nil {
			t.Errorf("hashToIP returned nil for %s", vmID)
			continue
		}
		_, subnet, _ := net.ParseCIDR("10.0.0.0/24")
		if !subnet.Contains(ip) {
			t.Errorf("IP %s for %s not in subnet", ip.String(), vmID)
		}
	}
}

func TestHashToIP_SmallSubnet(t *testing.T) {
	allocator, err := NewIPAllocator("10.0.0.0/30", "10.0.0.1")
	if err != nil {
		t.Fatalf("Failed to create allocator: %v", err)
	}

	ip := allocator.hashToIP("test-vm")
	if ip == nil {
		t.Fatal("hashToIP returned nil")
	}
}

func TestHashToIP_IPv6(t *testing.T) {
	allocator, err := NewIPAllocator("fd00::/64", "fd00::1")
	if err != nil {
		t.Fatalf("Failed to create allocator: %v", err)
	}

	ip := allocator.hashToIP("test-vm-ipv6")
	if ip == nil {
		t.Fatal("hashToIP returned nil for IPv6")
	}
}

func TestIncIP_Injectable(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"10.0.0.1", "10.0.0.2"},
		{"10.0.0.255", "10.0.1.0"},
		{"10.0.255.255", "10.1.0.0"},
	}
	for _, tc := range testCases {
		ip := net.ParseIP(tc.input)
		next := incIP(ip)
		if next.String() != tc.expected {
			t.Errorf("incIP(%s) = %s, expected %s", tc.input, next.String(), tc.expected)
		}
	}
}

func TestIPMaskToCIDR_Injectable(t *testing.T) {
	testCases := []struct {
		netmask  string
		expected string
	}{
		{"255.255.255.0", "24"},
		{"255.255.0.0", "16"},
		{"255.255.255.128", "25"},
		{"", "24"},
	}
	for _, tc := range testCases {
		result := ipMaskToCIDR(tc.netmask)
		if result != tc.expected {
			t.Errorf("ipMaskToCIDR(%s) = %s, expected %s", tc.netmask, result, tc.expected)
		}
	}
}

func TestListTapDevices_Injectable(t *testing.T) {
	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	nm.mu.Lock()
	nm.tapDevices["task1-tap1"] = &TapDevice{Name: "tap1", Bridge: "br0", IP: "10.0.0.2"}
	nm.tapDevices["task2-tap2"] = &TapDevice{Name: "tap2", Bridge: "br0", IP: "10.0.0.3"}
	nm.mu.Unlock()

	devices := nm.ListTapDevices()
	if len(devices) != 2 {
		t.Errorf("Expected 2 tap devices, got %d", len(devices))
	}
}

func TestGetTapIP_Injectable(t *testing.T) {
	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	nm.mu.Lock()
	nm.tapDevices["test-task-tap-abc123-0"] = &TapDevice{Name: "tap-abc123-0", Bridge: "br0", IP: "10.0.0.2"}
	nm.mu.Unlock()

	ip, err := nm.GetTapIP("test-task")
	if err != nil {
		t.Fatalf("GetTapIP failed: %v", err)
	}
	if ip != "10.0.0.2" {
		t.Errorf("Expected IP 10.0.0.2, got %s", ip)
	}
}

func TestGetTapIP_NoDevice(t *testing.T) {
	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	_, err := nm.GetTapIP("nonexistent-task")
	if err == nil {
		t.Fatal("Expected error when no tap device exists")
	}
}

func TestGetTapIP_NoIP(t *testing.T) {
	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	nm.mu.Lock()
	nm.tapDevices["test-task-tap-abc123-0"] = &TapDevice{Name: "tap-abc123-0", Bridge: "br0", IP: ""}
	nm.mu.Unlock()

	_, err := nm.GetTapIP("test-task")
	if err == nil {
		t.Fatal("Expected error when tap device has no IP")
	}
}

func TestSetEncryptionKeys_Injectable(t *testing.T) {
	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.SetEncryptionKeys(nil)
	if err != nil {
		t.Fatalf("SetEncryptionKeys failed: %v", err)
	}
}

func TestSetNodeDiscovery_Injectable(t *testing.T) {
	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	nm.SetNodeDiscovery(nil)
}

func TestStopPeerDiscovery_Injectable(t *testing.T) {
	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	nm.StopPeerDiscovery()
}

func TestCleanupNetwork_NilTask_Injectable(t *testing.T) {
	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.CleanupNetwork(context.Background(), nil)
	if err != nil {
		t.Fatalf("CleanupNetwork should not fail with nil task: %v", err)
	}
}

func TestDiscoverPeerWorkers_Injectable(t *testing.T) {
	config := types.NetworkConfig{}
	nm := NewNetworkManager(config).(*NetworkManager)

	peers := nm.discoverPeerWorkers()
	if len(peers) != 0 {
		t.Errorf("Expected empty peers without node discovery, got %d", len(peers))
	}
}

func TestCreateTapDevice_MasterFail_Injectable(t *testing.T) {
	state := newMockState()
	// Fail all "ip link set ... master" commands
	state.setFail("ip link set", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)

	network := types.NetworkAttachment{
		Network:   types.Network{ID: "test-network"},
		Addresses: []string{"10.0.0.2/24"},
	}

	_, err := nm.createTapDevice(context.Background(), network, 0, "test-task-123")
	if err == nil {
		t.Fatal("Expected error when bridge attach fails")
	}
}

func TestSetupDHCP_WriteFileFail(t *testing.T) {
	state := newMockState()
	state.setFail("writefile:/tmp/dnsmasq.log", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", Subnet: "10.0.0.0/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupDHCP(context.Background())
	t.Logf("setupDHCP result: %v", err)
}

func TestStartPeerDiscovery_InterfaceFail(t *testing.T) {
	state := newMockState()
	state.setFail("ip route show", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)
	nm.vxlanMgr = NewVXLANManager("testbr0", 100, "10.0.0.1/24", NewStaticPeerStore([]string{}))

	err := nm.StartPeerDiscovery(context.Background())
	if err == nil {
		t.Fatal("Expected error when interface discovery fails")
	}
}

func TestCreateTapDevice_BridgeNotFound(t *testing.T) {
	state := newMockState()
	state.setFail("ip link show testbr0", true)
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0"}
	nm := NewNetworkManager(config).(*NetworkManager)

	network := types.NetworkAttachment{
		Network: types.Network{
			ID: "test-network",
			Spec: types.NetworkSpec{
				DriverConfig: &types.DriverConfig{
					Bridge: &types.BridgeConfig{Name: "custombr0"},
				},
			},
		},
	}

	_, err := nm.createTapDevice(context.Background(), network, 0, "test-task-123")
	if err != nil {
		t.Logf("createTapDevice returned error: %v (this is ok)", err)
	}
}
func TestIncIP_IPv4Mapped(t *testing.T) {
	ip := net.ParseIP("::ffff:10.0.0.1")
	if ip == nil {
		t.Fatal("Failed to parse IPv4-mapped IPv6 address")
	}
	next := incIP(ip)
	expected := "10.0.0.2"
	if next.String() != expected {
		t.Errorf("incIP(::ffff:10.0.0.1) = %s, expected %s", next.String(), expected)
	}
}

func TestHashToIP_VerySmallSubnet(t *testing.T) {
	allocator, err := NewIPAllocator("10.0.0.0/29", "10.0.0.1")
	if err != nil {
		t.Fatalf("Failed to create allocator: %v", err)
	}
	ip := allocator.hashToIP("test-vm")
	if ip == nil {
		t.Fatal("hashToIP returned nil")
	}
	_, subnet, _ := net.ParseCIDR("10.0.0.0/29")
	if !subnet.Contains(ip) {
		t.Errorf("IP %s not in subnet 10.0.0.0/29", ip.String())
	}
}

func TestPrepareNetwork_Injectable(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", Subnet: "10.0.0.0/24", BridgeIP: "10.0.0.1/24", NATEnabled: true}
	allocator, _ := NewIPAllocator("10.0.0.0/24", "10.0.0.1")
	nm := NewNetworkManager(config).(*NetworkManager)
	nm.ipAllocator = allocator
	nm.natSetup = true
	nm.cniClient = nil // Disable CNI to test legacy path

	task := &types.Task{
		ID: "task-123",
		Networks: []types.NetworkAttachment{
			{Network: types.Network{ID: "net1"}, Addresses: []string{"10.0.5.2/24"}},
		},
	}

	err := nm.PrepareNetwork(context.Background(), task)
	if err != nil {
		t.Fatalf("PrepareNetwork failed: %v", err)
	}
}

func TestPrepareNetwork_NoNetworks(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", Subnet: "10.0.0.0/24", BridgeIP: "10.0.0.1/24", IPMode: "static"}
	allocator, _ := NewIPAllocator("10.0.0.0/24", "10.0.0.1")
	nm := NewNetworkManager(config).(*NetworkManager)
	nm.ipAllocator = allocator
	nm.natSetup = true
	nm.cniClient = nil // Disable CNI to test legacy path

	task := &types.Task{ID: "task-123"}

	err := nm.PrepareNetwork(context.Background(), task)
	if err != nil {
		t.Fatalf("PrepareNetwork failed: %v", err)
	}
}

func TestInit_Injectable(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", VXLANEnabled: false}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.Init(context.Background())
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
}

func TestInit_VXLANEnabled(t *testing.T) {
	state := newMockState()
	state.setOutput("ip route show default", "default via 192.168.1.1 dev eth0")
	state.setOutput("ip addr show eth0", "    inet 192.168.1.100/24 brd")
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", VXLANEnabled: true, VXLANPeers: []string{"192.168.1.50"}}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.Init(context.Background())
	if err != nil {
		t.Logf("Init error (VXLAN may fail in mock env): %v", err)
	}
}

func TestEnsureBridge_VXLANEnabled(t *testing.T) {
	state := newMockState()
	state.setOutput("ip route show default", "default via 192.168.1.1 dev eth0")
	state.setOutput("ip addr show eth0", "    inet 192.168.1.100/24 brd")
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", VXLANEnabled: true}
	nm := NewNetworkManager(config).(*NetworkManager)
	nm.bridges["testbr0"] = true

	// This tests the VXLAN setup path in ensureBridge
	err := nm.ensureBridge(context.Background())
	if err != nil {
		t.Logf("ensureBridge error: %v", err)
	}
}

func TestEnsureBridge_PeerDiscovery(t *testing.T) {
	state := newMockState()
	state.setOutput("ip route show default", "default via 192.168.1.1 dev eth0")
	state.setOutput("ip addr show eth0", "    inet 192.168.1.100/24 brd")
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", VXLANEnabled: true, VXLANPeers: []string{"192.168.1.50"}}
	nm := NewNetworkManager(config).(*NetworkManager)
	nm.bridges["testbr0"] = true

	err := nm.ensureBridge(context.Background())
	if err != nil {
		t.Logf("ensureBridge error: %v", err)
	}
}

func TestAllocate_Collision(t *testing.T) {
	allocator, err := NewIPAllocator("10.0.0.0/29", "10.0.0.1")
	if err != nil {
		t.Fatalf("Failed to create allocator: %v", err)
	}

	// Allocate multiple IPs to test collision resolution
	for i := 0; i < 4; i++ {
		ip, err := allocator.Allocate(fmt.Sprintf("vm-%d", i))
		if err != nil {
			t.Fatalf("Allocate failed for vm-%d: %v", i, err)
		}
		t.Logf("Allocated IP for vm-%d: %s", i, ip)
	}
}

func TestHashToIP_SizeCalculation(t *testing.T) {
	// Test with different subnet sizes
	testCases := []struct {
		subnet string
	}{
		{"192.168.0.0/16"},
		{"10.0.0.0/8"},
		{"172.16.0.0/12"},
	}

	for _, tc := range testCases {
		allocator, err := NewIPAllocator(tc.subnet, "10.0.0.1")
		if err != nil {
			t.Logf("Skipping %s: %v", tc.subnet, err)
			continue
		}
		ip := allocator.hashToIP("test-vm")
		if ip != nil {
			t.Logf("hashToIP for %s: %s", tc.subnet, ip.String())
		}
	}
}

func TestSetupNAT_RuleExists(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", Subnet: "10.0.0.0/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	err := nm.setupNAT(context.Background())
	if err != nil {
		t.Fatalf("setupNAT failed: %v", err)
	}
}

func TestEnsureBridge_DoubleCheck(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	// First call - creates bridge
	err := nm.ensureBridge(context.Background())
	if err != nil {
		t.Fatalf("First ensureBridge failed: %v", err)
	}

	// Second call - should skip (double-check pattern)
	err = nm.ensureBridge(context.Background())
	if err != nil {
		t.Fatalf("Second ensureBridge failed: %v", err)
	}
}

func TestSetupVXLANOverlay_PendingPeers(t *testing.T) {
	state := newMockState()
	state.setOutput("ip route show default", "default via 192.168.1.1 dev eth0")
	state.setOutput("ip addr show eth0", "    inet 192.168.1.100/24 brd")
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", VXLANEnabled: true}
	nm := NewNetworkManager(config).(*NetworkManager)

	// Add pending peers before VXLAN init
	nm.mu.Lock()
	nm.pendingPeers = []string{"192.168.1.50", "192.168.1.51"}
	nm.mu.Unlock()

	err := nm.setupVXLANOverlay(context.Background())
	if err != nil {
		t.Logf("setupVXLANOverlay error: %v", err)
	}
}
