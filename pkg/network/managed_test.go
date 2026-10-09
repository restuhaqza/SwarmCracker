//go:build !integration

package network

import (
	"context"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNetworkBridgeName(t *testing.T) {
	withBridge := &types.Network{
		ID: "abcdefgh12345678",
		Spec: types.NetworkSpec{
			DriverConfig: &types.DriverConfig{Bridge: &types.BridgeConfig{Name: "br-custom"}},
		},
	}
	assert.Equal(t, "br-custom", networkBridgeName(withBridge))

	withoutBridge := &types.Network{ID: "abcdefgh12345678"}
	assert.Equal(t, "sc-abcdefgh", networkBridgeName(withoutBridge))

	assert.Equal(t, "", networkBridgeName(nil))
}

func TestManagedHelpers(t *testing.T) {
	assert.False(t, managed(nil))
	assert.False(t, managed(&types.Network{Spec: types.NetworkSpec{}}))
	assert.True(t, managed(&types.Network{Spec: types.NetworkSpec{Subnet: "10.10.0.0/24"}}))

	assert.False(t, hasManagedAttachment(nil))
	assert.False(t, hasManagedAttachment([]types.NetworkAttachment{{Network: types.Network{}}}))
	assert.True(t, hasManagedAttachment([]types.NetworkAttachment{
		{Network: types.Network{Spec: types.NetworkSpec{Subnet: "10.10.0.0/24"}}},
	}))
}

func newManagedTestManager() *NetworkManager {
	return &NetworkManager{
		config:          types.NetworkConfig{BridgeName: "swarm-br0", NATEnabled: true},
		bridges:         map[string]bool{},
		tapDevices:      map[string]*TapDevice{},
		managedNetworks: map[string]*managedNetwork{},
	}
}

func backendNetwork() *types.Network {
	return &types.Network{
		ID: "net-abcdef1234567890",
		Spec: types.NetworkSpec{
			Name:    "backend",
			Driver:  "vxlan",
			Subnet:  "10.10.0.0/24",
			Gateway: "10.10.0.1",
			DriverConfig: &types.DriverConfig{
				Bridge: &types.BridgeConfig{Name: "br-backend"},
			},
		},
	}
}

func TestEnsureManagedNetwork_Materializes(t *testing.T) {
	state := newMockState()
	state.setFail("ip link show br-backend", true) // force bridge creation
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	mn, err := nm.ensureManagedNetwork(context.Background(), backendNetwork())
	require.NoError(t, err)

	assert.Equal(t, "br-backend", mn.bridge)
	assert.Equal(t, "10.10.0.0/24", mn.subnet)
	assert.Equal(t, "10.10.0.1", mn.gateway)
	require.NotNil(t, mn.allocator)

	// Bridge was created and brought up.
	assert.Contains(t, state.calls, "ip link add br-backend type bridge")
	// Gateway address assigned.
	foundAddr := false
	foundIso := false
	for _, c := range state.calls {
		if strings.Contains(c, "addr add 10.10.0.1/24 dev br-backend") {
			foundAddr = true
		}
		if strings.Contains(c, isolationChain) {
			foundIso = true
		}
	}
	assert.True(t, foundAddr, "gateway address should be assigned")
	assert.True(t, foundIso, "isolation chain should be reconciled")

	// Idempotent: second call returns the cached handle without re-creating.
	state2 := newMockState()
	restore2 := setupMocksForTest(state2)
	defer restore2()
	again, err := nm.ensureManagedNetwork(context.Background(), backendNetwork())
	require.NoError(t, err)
	assert.Same(t, mn, again)
	assert.Empty(t, state2.calls, "cached network must not run any command")
}

func TestPrepareManagedNetworks_AllocatesAndAttaches(t *testing.T) {
	state := newMockState()
	state.setFail("ip link show br-backend", true)
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	task := &types.Task{
		ID: "task-1",
		Networks: []types.NetworkAttachment{
			{Network: *backendNetwork()},
		},
	}

	require.NoError(t, nm.prepareManagedNetworks(context.Background(), task))
	require.Len(t, task.Networks[0].Addresses, 1)
	assert.True(t, strings.HasPrefix(task.Networks[0].Addresses[0], "10.10.0."), "got %s", task.Networks[0].Addresses[0])

	foundAttach := false
	for _, c := range state.calls {
		if strings.Contains(c, "master br-backend") {
			foundAttach = true
		}
	}
	assert.True(t, foundAttach, "TAP should be attached to the network bridge")
}

func TestRebuildIsolationRules_DropsBetweenBridges(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	nm.managedNetworks["a"] = &managedNetwork{id: "a", bridge: "br-a"}
	nm.managedNetworks["b"] = &managedNetwork{id: "b", bridge: "br-b"}
	nm.rebuildIsolationRules()

	var drops []string
	for _, c := range state.calls {
		if strings.Contains(c, isolationChain) && strings.Contains(c, "DROP") {
			drops = append(drops, c)
		}
	}
	// default<->a, default<->b, a<->b in both directions = 6 rules.
	assert.Len(t, drops, 6)
	assert.Contains(t, drops, "iptables -A "+isolationChain+" -i br-a -o br-b -j DROP")
}
