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
	nm := &NetworkManager{config: types.NetworkConfig{Subnet: "192.168.127.0/24"}}

	assert.False(t, nm.managed(nil))
	assert.False(t, nm.managed(&types.Network{Spec: types.NetworkSpec{}}))
	assert.True(t, nm.managed(&types.Network{Spec: types.NetworkSpec{Subnet: "10.10.0.0/24"}}))

	// The ingress network is carved from the flat L2 subnet by design. Giving
	// it a second bridge on that range puts two gateways on one subnet and
	// makes the published ports unreachable, so it stays on the node bridge.
	assert.False(t, nm.managed(&types.Network{Spec: types.NetworkSpec{Name: "ingress", Subnet: "192.168.127.0/24"}}))
	assert.False(t, nm.managed(&types.Network{Spec: types.NetworkSpec{Subnet: "192.168.127.0/24"}}))
	assert.True(t, nm.managed(&types.Network{Spec: types.NetworkSpec{Subnet: "192.168.127.0/25"}}))

	assert.False(t, nm.hasManagedAttachment(nil))
	assert.False(t, nm.hasManagedAttachment([]types.NetworkAttachment{{Network: types.Network{}}}))
	assert.True(t, nm.hasManagedAttachment([]types.NetworkAttachment{
		{Network: types.Network{Spec: types.NetworkSpec{Subnet: "10.10.0.0/24"}}},
	}))
	assert.False(t, nm.hasManagedAttachment([]types.NetworkAttachment{
		{Network: types.Network{Spec: types.NetworkSpec{Subnet: "192.168.127.0/24"}}},
	}))

	// Without a configured flat subnet the previous behaviour is preserved.
	plain := &NetworkManager{}
	assert.True(t, plain.managed(&types.Network{Spec: types.NetworkSpec{Subnet: "192.168.127.0/24"}}))
}

func TestSameSubnet(t *testing.T) {
	assert.True(t, sameSubnet("192.168.127.0/24", "192.168.127.0/24"))
	assert.True(t, sameSubnet("192.168.127.1/24", "192.168.127.0/24"), "host bits are ignored")
	assert.False(t, sameSubnet("192.168.127.0/24", "192.168.127.0/25"))
	assert.False(t, sameSubnet("nonsense", "192.168.127.0/24"))
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

func TestEnsureManagedNetwork_DerivesGatewayAndVXLAN(t *testing.T) {
	state := newMockState()
	state.setFail("ip link show br-vx", true)
	// Physical uplink discovery for the VXLAN setup.
	state.setOutput("ip route show default", "default via 192.168.1.1 dev eth0\n")
	state.setOutput("ip addr show eth0", "    inet 192.168.1.10/24 brd 192.168.1.255 scope global eth0\n")
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	network := &types.Network{
		ID: "net-vxlan0001",
		Spec: types.NetworkSpec{
			Name:    "vx",
			Driver:  "vxlan",
			Subnet:  "10.30.0.0/24", // gateway omitted -> derived from the subnet
			VXLANID: 4096,
			DriverConfig: &types.DriverConfig{
				Bridge: &types.BridgeConfig{Name: "br-vx"},
			},
		},
	}

	mn, err := nm.ensureManagedNetwork(context.Background(), network)
	require.NoError(t, err)
	assert.Equal(t, "10.30.0.1", mn.gateway, "gateway should default to the first usable host")
	assert.Equal(t, 4096, mn.vxlanID)
}

func TestEnsureManagedNetwork_BridgeAddressFollowsTheNode(t *testing.T) {
	state := newMockState()
	state.setFail("ip link show br-backend", true) // force bridge creation
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	// The second node of a two-node overlay: its own address on the network is .2, and
	// that is what its guests use as their gateway instead of the cluster-wide .1.
	nm.config.BridgeIP = "192.168.127.2/24"
	nm.config.VXLANPeers = []string{"192.168.127.1"}
	nm.nodeIndex, nm.nodeSlots = 2, reservedInfraHosts

	mn, err := nm.ensureManagedNetwork(context.Background(), backendNetwork())
	require.NoError(t, err)

	assert.Equal(t, "10.10.0.2", mn.gateway,
		"the bridge holds this node's own address, not the cluster-wide gateway")
	foundAddr := false
	for _, c := range state.calls {
		if strings.Contains(c, "addr add 10.10.0.2/24 dev br-backend") {
			foundAddr = true
		}
	}
	assert.True(t, foundAddr, "the bridge should hold this node's address; calls: %v", state.calls)
}

// TestEnsureManagedNetwork_ReplacesAStaleBridgeAddress covers the upgrade path: a bridge
// an earlier release created holds the cluster-wide address on every node, because the
// address used to come from the subnet alone. Materializing the network on a second node
// must drop that address — leaving it next to the node's own would keep the duplicate
// (#76) and both would answer ARP on the network's L2.
func TestEnsureManagedNetwork_ReplacesAStaleBridgeAddress(t *testing.T) {
	state := newMockState()
	state.setFail("ip link show br-backend", true) // force bridge creation
	// The bridge as the earlier release left it on this node.
	state.setOutput("ip -4 -o addr show dev br-backend",
		"7: br-backend    inet 10.10.0.1/24 scope global br-backend\\       valid_lft forever preferred_lft forever\n")
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	nm.config.BridgeIP = "192.168.127.2/24"
	nm.config.VXLANPeers = []string{"192.168.127.1"}
	nm.nodeIndex, nm.nodeSlots = 2, reservedInfraHosts

	mn, err := nm.ensureManagedNetwork(context.Background(), backendNetwork())
	require.NoError(t, err)

	assert.Equal(t, "10.10.0.2", mn.gateway)
	assert.Contains(t, state.calls, "ip addr del 10.10.0.1/24 dev br-backend",
		"the stale cluster-wide address must be removed, not joined")
	assert.Contains(t, state.calls, "ip addr add 10.10.0.2/24 dev br-backend")
}

func TestEnsureManagedNetwork_InvalidSubnet(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	_, err := nm.ensureManagedNetwork(context.Background(), &types.Network{
		ID:   "net-bad",
		Spec: types.NetworkSpec{Subnet: "not-a-cidr", DriverConfig: &types.DriverConfig{Bridge: &types.BridgeConfig{Name: "br-bad"}}},
	})
	assert.Error(t, err)
}

func TestSetupManagedNAT_AddsRuleWhenMissing(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -t nat -C POSTROUTING", true) // the rule is absent
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	nm.setupManagedNAT("br-backend", "10.10.0.0/24")

	found := false
	for _, c := range state.calls {
		if strings.HasPrefix(c, "iptables -t nat -A POSTROUTING -s 10.10.0.0/24") {
			found = true
		}
	}
	assert.True(t, found, "a MASQUERADE rule should be added when missing")
}

func TestPrepareManagedNetworks_MixedAttachment(t *testing.T) {
	state := newMockState()
	state.setFail("ip link show br-backend", true)
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	task := &types.Task{
		ID: "task-mixed",
		Networks: []types.NetworkAttachment{
			{Network: *backendNetwork()},
			// A non-managed attachment (no subnet) falls back to the default bridge.
			{Network: types.Network{ID: "default", Spec: types.NetworkSpec{Name: "default"}}},
		},
	}

	require.NoError(t, nm.prepareManagedNetworks(context.Background(), task))
	assert.Len(t, nm.tapDevices, 2, "both attachments should get a TAP")

	foundDefault := false
	for _, c := range state.calls {
		if strings.Contains(c, "master swarm-br0") {
			foundDefault = true
		}
	}
	assert.True(t, foundDefault, "the non-managed attachment should use the default bridge")
}

// TestAttachmentBridgeName pins where a TAP lands. A network on the flat L2
// subnet (the ingress network) must use the node bridge even though the CNI
// provider recorded a per-network bridge for it; a network with its own subnet
// keeps its bridge.
func TestAttachmentBridgeName(t *testing.T) {
	nm := &NetworkManager{config: types.NetworkConfig{BridgeName: "swarm-br0", Subnet: "192.168.127.0/24"}}

	assert.Equal(t, "swarm-br0", nm.attachmentBridgeName(nil))
	assert.Equal(t, "swarm-br0", nm.attachmentBridgeName(&types.Network{
		Spec: types.NetworkSpec{
			Name:         "ingress",
			Subnet:       "192.168.127.0/24",
			DriverConfig: &types.DriverConfig{Bridge: &types.BridgeConfig{Name: "br-ingress"}},
		},
	}), "the ingress network is carved from the flat L2 and must not split it")

	assert.Equal(t, "br-backend", nm.attachmentBridgeName(&types.Network{
		Spec: types.NetworkSpec{
			Subnet:       "10.10.0.0/24",
			DriverConfig: &types.DriverConfig{Bridge: &types.BridgeConfig{Name: "br-backend"}},
		},
	}))

	assert.Equal(t, "br-abcdefgh1234", nm.attachmentBridgeName(&types.Network{
		ID:   "abcdefgh12345678",
		Spec: types.NetworkSpec{Driver: "overlay", Subnet: "10.20.0.0/24"},
	}))
}

// TestNetworkByID pins the lookup the migration re-anneal uses: a network this node
// materialised is answered with the SwarmKit view it was built from, so the caller can
// derive the bridge with the same AttachmentBridgeName call the boot and restore paths
// make. A network this node has not materialised answers false - it has no bridge for
// it, and therefore no stale entry on one.
func TestNetworkByID(t *testing.T) {
	backend := &types.Network{
		ID:   "net-backend1234",
		Spec: types.NetworkSpec{Driver: "overlay", Subnet: "10.10.0.0/24"},
	}
	nm := &NetworkManager{
		config: types.NetworkConfig{BridgeName: "swarm-br0", Subnet: "192.168.127.0/24"},
		managedNetworks: map[string]*managedNetwork{
			"net-backend1234": {id: "net-backend1234", bridge: "sc-net-back", network: backend},
		},
	}

	got, ok := nm.NetworkByID("net-backend1234")
	require.True(t, ok)
	assert.Same(t, backend, got)

	_, ok = nm.NetworkByID("net-never-seen")
	assert.False(t, ok, "a network this node never materialised has no bridge here")

	_, ok = nm.NetworkByID("")
	assert.False(t, ok)

	// And the entry it returns is what derives the device: the overlay rule names the
	// bridge the network's attachments were on, not the node's flat bridge.
	assert.Equal(t, "br-net-backend1",
		AttachmentBridgeName(got, nm.config), "the overlay rule uses the first 12 characters of the ID")
}
