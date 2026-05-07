//go:build !integration

package network

import (
	"context"
	"errors"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// defaultConfig returns a network config for testing
func testNetConfig() types.NetworkConfig {
	return types.NetworkConfig{
		BridgeName:   "test-br0",
		Subnet:       "10.0.0.0/24",
		BridgeIP:     "10.0.0.1/24",
		NATEnabled:   true,
		VXLANEnabled: false,
		IPMode:       "static",
	}
}

// =============================================================================
// ensureBridgeWithExecutor tests
// =============================================================================

func TestEnsureBridge_AlreadyCached(t *testing.T) {
	mock := NewMockCommandExecutor()
	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	nmi.bridges["test-br0"] = true

	err := nmi.ensureBridgeWithExecutor(context.Background())
	require.NoError(t, err)
	assert.Empty(t, mock.Calls, "no commands should run for cached bridge")
}

func TestEnsureBridge_BridgeAlreadyExists(t *testing.T) {
	mock := NewMockCommandExecutor()
	// ip link show succeeds → bridge already exists
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		return MockCommandResult{Output: []byte("test-br0 exists"), Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	err := nmi.ensureBridgeWithExecutor(context.Background())
	require.NoError(t, err)
}

func TestEnsureBridge_CreateNew(t *testing.T) {
	mock := NewMockCommandExecutor()
	callCount := 0
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		callCount++
		// First call: ip link show → fail (not exists)
		if callCount == 1 {
			return MockCommandResult{Err: errors.New("not found")}
		}
		// ip link add, ip link set up, ip addr show, ip addr add → succeed
		return MockCommandResult{Output: []byte("ok"), Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	err := nmi.ensureBridgeWithExecutor(context.Background())
	require.NoError(t, err)
	assert.True(t, nmi.bridges["test-br0"])
}

func TestEnsureBridge_CreateBridgeFail(t *testing.T) {
	mock := NewMockCommandExecutor()
	callCount := 0
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		callCount++
		if callCount == 1 {
			return MockCommandResult{Err: errors.New("not found")}
		}
		// ip link add fails
		if callCount == 2 && len(args) >= 4 && args[1] == "add" {
			return MockCommandResult{Err: errors.New("permission denied")}
		}
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	err := nmi.ensureBridgeWithExecutor(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create bridge")
}

func TestEnsureBridge_BringUpFail(t *testing.T) {
	mock := NewMockCommandExecutor()
	callCount := 0
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		callCount++
		if callCount == 1 {
			return MockCommandResult{Err: errors.New("not found")}
		}
		// ip link add → ok, ip addr → ok, ip link set up → fail
		if len(args) >= 3 && args[0] == "link" && args[1] == "set" && args[3] == "up" {
			return MockCommandResult{Err: errors.New("interface error")}
		}
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	err := nmi.ensureBridgeWithExecutor(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to bring bridge up")
}

// =============================================================================
// setupBridgeIPWithExecutor tests
// =============================================================================

func TestSetupBridgeIP_Success(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		return MockCommandResult{Output: []byte("ok"), Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	err := nmi.setupBridgeIPWithExecutor(context.Background())
	require.NoError(t, err)
}

func TestSetupBridgeIP_AlreadySet(t *testing.T) {
	mock := NewMockCommandExecutor()
	callCount := 0
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		callCount++
		// ip addr show → success (bridge has IP)
		if len(args) >= 2 && args[0] == "addr" && args[1] == "show" {
			return MockCommandResult{Output: []byte("inet 10.0.0.1/24"), Err: nil}
		}
		// ip addr add → "exists" error (acceptable)
		if len(args) >= 2 && args[0] == "addr" && args[1] == "add" {
			return MockCommandResult{Err: errors.New("file exists")}
		}
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	err := nmi.setupBridgeIPWithExecutor(context.Background())
	require.NoError(t, err)
}

// =============================================================================
// setupNATWithExecutor tests
// =============================================================================

func TestSetupNAT_NoSubnet(t *testing.T) {
	mock := NewMockCommandExecutor()
	cfg := testNetConfig()
	cfg.Subnet = ""
	nmi := NewNetworkManagerWithExecutor(cfg, mock)

	err := nmi.setupNATWithExecutor(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subnet must be configured")
}

func TestSetupNAT_IPForwardFail(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["sysctl"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: errors.New("sysctl failed")}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	err := nmi.setupNATWithExecutor(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IP forwarding")
}

func TestSetupNAT_Success(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["sysctl"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}
	mock.CommandHandlers["iptables"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	err := nmi.setupNATWithExecutor(context.Background())
	require.NoError(t, err)
}

func TestSetupNAT_CheckRuleExists(t *testing.T) {
	mock := NewMockCommandExecutor()
	callCount := 0
	mock.CommandHandlers["sysctl"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}
	mock.CommandHandlers["iptables"] = func(args []string) MockCommandResult {
		callCount++
		// First -C check succeeds (rule already exists)
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	err := nmi.setupNATWithExecutor(context.Background())
	require.NoError(t, err)
}

// =============================================================================
// createTapDeviceWithExecutor tests
// =============================================================================

func TestCreateTapDevice_Success(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	network := types.NetworkAttachment{
		Network: types.Network{
			ID:   "test-net",
			Spec: types.NetworkSpec{Name: "test"},
		},
	}

	tap, err := nmi.createTapDeviceWithExecutor(context.Background(), network, 0, "task-1")
	require.NoError(t, err)
	require.NotNil(t, tap)
	assert.Equal(t, "tap-eth0", tap.Name)
	assert.Equal(t, "test-br0", tap.Bridge)
}

func TestCreateTapDevice_CreateFail(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		// tuntap add fails
		if len(args) >= 2 && args[0] == "tuntap" && args[1] == "add" {
			return MockCommandResult{Err: errors.New("tuntap failed")}
		}
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	network := types.NetworkAttachment{
		Network: types.Network{ID: "test-net", Spec: types.NetworkSpec{Name: "test"}},
	}

	tap, err := nmi.createTapDeviceWithExecutor(context.Background(), network, 0, "task-1")
	require.Error(t, err)
	assert.Nil(t, tap)
	assert.Contains(t, err.Error(), "failed to create TAP device")
}

func TestCreateTapDevice_BringUpFail(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		if len(args) >= 2 && args[0] == "tuntap" {
			return MockCommandResult{Err: nil}
		}
		if len(args) >= 3 && args[0] == "link" && args[1] == "set" && args[3] == "up" {
			return MockCommandResult{Err: errors.New("bring up failed")}
		}
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	network := types.NetworkAttachment{
		Network: types.Network{ID: "test-net", Spec: types.NetworkSpec{Name: "test"}},
	}

	tap, err := nmi.createTapDeviceWithExecutor(context.Background(), network, 0, "task-1")
	require.Error(t, err)
	assert.Nil(t, tap)
	assert.Contains(t, err.Error(), "failed to bring TAP up")
}

func TestCreateTapDevice_MasterFail(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		if len(args) >= 2 && args[0] == "tuntap" {
			return MockCommandResult{Err: nil}
		}
		// Match "ip link set <name> master <bridge>"
		if len(args) >= 3 && args[0] == "link" && args[1] == "set" {
			for _, a := range args {
				if a == "master" {
					return MockCommandResult{Err: errors.New("master failed")}
				}
			}
		}
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	network := types.NetworkAttachment{
		Network: types.Network{ID: "test-net", Spec: types.NetworkSpec{Name: "test"}},
	}

	tap, err := nmi.createTapDeviceWithExecutor(context.Background(), network, 0, "task-1")
	require.Error(t, err)
	assert.Nil(t, tap)
	assert.Contains(t, err.Error(), "failed to add TAP to bridge")
}

func TestCreateTapDevice_WithCustomBridge(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	network := types.NetworkAttachment{
		Network: types.Network{
			ID:   "test-net",
			Spec: types.NetworkSpec{
				Name: "test",
				DriverConfig: &types.DriverConfig{
					Bridge: &types.BridgeConfig{Name: "custom-br0"},
				},
			},
		},
	}

	tap, err := nmi.createTapDeviceWithExecutor(context.Background(), network, 0, "task-1")
	require.NoError(t, err)
	assert.Equal(t, "custom-br0", tap.Bridge)
}

// =============================================================================
// PrepareNetworkWithExecutor tests
// =============================================================================

func TestPrepareNetwork_Success(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}
	mock.CommandHandlers["sysctl"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}
	mock.CommandHandlers["iptables"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	task := &types.Task{
		ID: "task-prepare-1",
		Networks: []types.NetworkAttachment{
			{Network: types.Network{ID: "net1", Spec: types.NetworkSpec{Name: "test"}}},
		},
	}

	err := nmi.PrepareNetworkWithExecutor(context.Background(), task)
	require.NoError(t, err)
}

func TestPrepareNetwork_BridgeFail(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		// First call fails and create also fails
		return MockCommandResult{Err: errors.New("bridge error")}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	task := &types.Task{
		ID:       "task-fail",
		Networks: []types.NetworkAttachment{},
	}

	err := nmi.PrepareNetworkWithExecutor(context.Background(), task)
	require.Error(t, err)
}

func TestPrepareNetwork_TapCreateFail(t *testing.T) {
	mock := NewMockCommandExecutor()
	callCount := 0
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		callCount++
		// ensureBridge succeeds (first call is link show → already cached)
		if len(args) >= 3 && args[0] == "link" && args[1] == "show" {
			return MockCommandResult{Output: []byte("ok"), Err: nil}
		}
		// But tuntap create fails
		if len(args) >= 2 && args[0] == "tuntap" {
			return MockCommandResult{Err: errors.New("tuntap error")}
		}
		return MockCommandResult{Err: nil}
	}

	cfg := testNetConfig()
	cfg.NATEnabled = false
	nmi := NewNetworkManagerWithExecutor(cfg, mock)
	nmi.bridges["test-br0"] = true

	task := &types.Task{
		ID: "task-tap-fail",
		Networks: []types.NetworkAttachment{
			{Network: types.Network{ID: "net1", Spec: types.NetworkSpec{Name: "test"}}},
		},
	}

	err := nmi.PrepareNetworkWithExecutor(context.Background(), task)
	require.Error(t, err)
}

// =============================================================================
// CleanupNetworkWithExecutor tests
// =============================================================================

func TestCleanupNetwork_Success(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	nmi.mu.Lock()
	nmi.tapDevices["task-1-tap-eth0"] = &TapDevice{
		Name:   "tap-eth0",
		Bridge: "test-br0",
		IP:     "10.0.0.5",
	}
	nmi.mu.Unlock()

	task := &types.Task{ID: "task-1"}
	err := nmi.CleanupNetworkWithExecutor(context.Background(), task)
	require.NoError(t, err)

	nmi.mu.RLock()
	_, exists := nmi.tapDevices["task-1-tap-eth0"]
	nmi.mu.RUnlock()
	assert.False(t, exists, "TAP device should be removed")
}

func TestCleanupNetwork_NilTask(t *testing.T) {
	mock := NewMockCommandExecutor()
	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)

	err := nmi.CleanupNetworkWithExecutor(context.Background(), nil)
	require.NoError(t, err)
}

func TestCleanupNetwork_DeleteFail(t *testing.T) {
	mock := NewMockCommandExecutor()
	callCount := 0
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		callCount++
		// ip link delete fails
		if len(args) >= 2 && args[0] == "link" && args[1] == "delete" {
			return MockCommandResult{Err: errors.New("delete failed")}
		}
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	nmi.mu.Lock()
	nmi.tapDevices["task-2-tap-eth0"] = &TapDevice{
		Name:   "tap-eth0",
		Bridge: "test-br0",
		IP:     "10.0.0.6",
	}
	nmi.mu.Unlock()

	task := &types.Task{ID: "task-2"}
	// Should not return error (just logs)
	err := nmi.CleanupNetworkWithExecutor(context.Background(), task)
	require.NoError(t, err)
}

// =============================================================================
// removeTapDeviceWithExecutor tests
// =============================================================================

func TestRemoveTapDevice_Success(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	err := nmi.removeTapDeviceWithExecutor(&TapDevice{Name: "tap-eth0"})
	require.NoError(t, err)
}

func TestRemoveTapDevice_Fail(t *testing.T) {
	mock := NewMockCommandExecutor()
	mock.CommandHandlers["ip"] = func(args []string) MockCommandResult {
		if len(args) >= 2 && args[1] == "delete" {
			return MockCommandResult{Err: errors.New("delete failed")}
		}
		return MockCommandResult{Err: nil}
	}

	nmi := NewNetworkManagerWithExecutor(testNetConfig(), mock)
	err := nmi.removeTapDeviceWithExecutor(&TapDevice{Name: "tap-eth0"})
	require.Error(t, err)
}

// =============================================================================
// DHCP / setupDHCP tests
// =============================================================================

func TestSetupDHCP_NoSubnet(t *testing.T) {
	cfg := testNetConfig()
	cfg.Subnet = ""
	nm := &NetworkManager{
		config:     cfg,
		bridges:    make(map[string]bool),
		tapDevices: make(map[string]*TapDevice),
	}
	err := nm.setupDHCP(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subnet")
}

func TestSetupDHCP_NoBridgeIP(t *testing.T) {
	cfg := testNetConfig()
	cfg.BridgeIP = ""
	nm := &NetworkManager{
		config:     cfg,
		bridges:    make(map[string]bool),
		tapDevices: make(map[string]*TapDevice),
	}
	err := nm.setupDHCP(context.Background())
	require.Error(t, err)
}
