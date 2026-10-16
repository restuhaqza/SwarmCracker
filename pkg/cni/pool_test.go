package cni

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPoolProvider builds a provider with an explicit pool layout, without touching
// the host: AllocateNetwork only needs the IPAM manager and the config generator,
// and it writes CNI config files into a temp dir.
func newPoolProvider(t *testing.T, cfg *CNIConfig) *CNIProvider {
	t.Helper()
	if cfg.ConfigDir == "" {
		cfg.ConfigDir = t.TempDir()
	}
	if cfg.SubnetSize == 0 {
		cfg.SubnetSize = DefaultSubnetSize
	}
	return &CNIProvider{
		config:    cfg,
		ipamMgr:   NewIPAMManager(cfg),
		configGen: NewConfigGenerator(),
		vxlanPort: DefaultVXLANPort,
		networks:  make(map[string]*AllocatedNetwork),
	}
}

// flatPoolConfig is the layout the daemon wires by default: the predefined networks
// are carved from the flat L2 subnet, user-defined ones from the overlay pool.
func flatPoolConfig(t *testing.T) *CNIConfig {
	t.Helper()
	return &CNIConfig{
		ConfigDir:         t.TempDir(),
		SubnetPool:        "192.168.127.0/24",
		OverlaySubnetPool: "10.0.0.0/8",
		FlatSubnet:        "192.168.127.0/24",
	}
}

// TestAllocateNetwork_UserNetworkIsCarvedFromTheOverlayPool is the #77 case: a
// network created without a subnet used to be handed the flat L2 subnet, because
// the pool was --subnet and a single /24 pool always yields itself.
func TestAllocateNetwork_UserNetworkIsCarvedFromTheOverlayPool(t *testing.T) {
	p := newPoolProvider(t, flatPoolConfig(t))

	allocated, err := p.AllocateNetwork(NetworkAllocationRequest{Name: "backend", Driver: "vxlan"})
	require.NoError(t, err)
	require.NotNil(t, allocated.Subnet)
	assert.Equal(t, "10.0.1.0/24", allocated.Subnet.String(),
		"a user-defined network must be carved from the overlay pool, never the flat L2 subnet")
}

// TestAllocateNetwork_PredefinedNetworkStaysOnTheFlatL2 pins the #52 behaviour this
// change must not disturb: the predefined networks keep the flat-L2 pool, which is
// what makes ingress task IPs reachable cluster-wide.
func TestAllocateNetwork_PredefinedNetworkStaysOnTheFlatL2(t *testing.T) {
	p := newPoolProvider(t, flatPoolConfig(t))

	// The ingress network is marked by the spec, and the gateway bridge by name.
	ingress, err := p.AllocateNetwork(NetworkAllocationRequest{
		Name: IngressNetworkName, Driver: "bridge", Ingress: true,
	})
	require.NoError(t, err)
	require.NotNil(t, ingress.Subnet)
	assert.Equal(t, "192.168.127.0/24", ingress.Subnet.String(),
		"the ingress network must stay on the flat L2 subnet")

	gwBridge, err := p.AllocateNetwork(NetworkAllocationRequest{
		Name: GWBridgeNetworkName, Driver: "bridge",
	})
	require.NoError(t, err)
	require.NotNil(t, gwBridge.Subnet)
	assert.Equal(t, "192.168.127.0/24", gwBridge.Subnet.String(),
		"the gateway bridge is predefined as well")
}

// TestAllocateNetwork_SkipsReservedAndUsedSubnets covers the carving loop: a
// candidate that overlaps the reserved flat L2 subnet, or a subnet a network
// already holds, is skipped rather than handed out.
func TestAllocateNetwork_SkipsReservedAndUsedSubnets(t *testing.T) {
	cfg := flatPoolConfig(t)
	// The flat L2 sits on the first overlay candidate, so it must be skipped.
	cfg.FlatSubnet = "10.0.1.0/24"
	p := newPoolProvider(t, cfg)

	first, err := p.AllocateNetwork(NetworkAllocationRequest{Name: "one", Driver: "vxlan"})
	require.NoError(t, err)
	require.NotNil(t, first.Subnet)
	assert.Equal(t, "10.0.2.0/24", first.Subnet.String(), "the reserved candidate must be skipped")

	second, err := p.AllocateNetwork(NetworkAllocationRequest{Name: "two", Driver: "vxlan"})
	require.NoError(t, err)
	require.NotNil(t, second.Subnet)
	assert.Equal(t, "10.0.3.0/24", second.Subnet.String())
	assert.NotEqual(t, first.Subnet.String(), second.Subnet.String(),
		"distinct networks get distinct subnets")
}

// TestAllocateNetwork_RejectsAnExplicitSubnetThatIsNotFree is the guard: two
// networks on the same addresses cannot be told apart by route, NAT rule or IPAM
// pool, so a subnet asked for now has to be free.
func TestAllocateNetwork_RejectsAnExplicitSubnetThatIsNotFree(t *testing.T) {
	p := newPoolProvider(t, flatPoolConfig(t))

	_, err := p.AllocateNetwork(NetworkAllocationRequest{
		Name: "overlap-flat", Driver: "vxlan", RequestedSubnet: "192.168.127.0/24",
	})
	require.Error(t, err, "the flat L2 subnet must be refused")
	assert.Contains(t, err.Error(), "overlaps the flat L2 subnet 192.168.127.0/24")

	_, err = p.AllocateNetwork(NetworkAllocationRequest{
		Name: "overlap-super", Driver: "vxlan", RequestedSubnet: "192.168.0.0/16",
	})
	require.Error(t, err, "a subnet containing the flat L2 subnet overlaps it too")

	_, err = p.AllocateNetwork(NetworkAllocationRequest{
		Name: "first", Driver: "vxlan", RequestedSubnet: "10.10.0.0/24",
	})
	require.NoError(t, err, "a free explicit subnet is still honored")

	_, err = p.AllocateNetwork(NetworkAllocationRequest{
		Name: "second", Driver: "vxlan", RequestedSubnet: "10.10.0.0/24",
	})
	require.Error(t, err, "a duplicate explicit subnet must be refused")
	assert.Contains(t, err.Error(), `overlaps network "first" (10.10.0.0/24)`,
		"the error should name what it collided with")

	free, err := p.AllocateNetwork(NetworkAllocationRequest{
		Name: "third", Driver: "vxlan", RequestedSubnet: "10.20.0.0/24",
	})
	require.NoError(t, err)
	require.NotNil(t, free.Subnet)
	assert.Equal(t, "10.20.0.0/24", free.Subnet.String())
}

// TestAllocateNetwork_ExistingAllocationIsKept covers the upgrade path: a network
// that already holds a subnet keeps it on re-allocation, even when that subnet now
// overlaps the flat L2 (which is exactly what a network created before #77 holds).
// Moving it would break the tasks that already carry addresses from it.
func TestAllocateNetwork_ExistingAllocationIsKept(t *testing.T) {
	p := newPoolProvider(t, flatPoolConfig(t))

	kept, err := p.AllocateNetwork(NetworkAllocationRequest{
		Name: "legacy", Driver: "vxlan", RequestedSubnet: "192.168.127.0/24", ExistingSubnet: true,
	})
	require.NoError(t, err, "a recorded allocation must not be refused")
	require.NotNil(t, kept.Subnet)
	assert.Equal(t, "192.168.127.0/24", kept.Subnet.String())

	// The same subnet asked for fresh is still refused.
	_, err = p.AllocateNetwork(NetworkAllocationRequest{
		Name: "fresh", Driver: "vxlan", RequestedSubnet: "192.168.127.0/24",
	})
	require.Error(t, err)
}

// TestAllocateNetwork_RecordedVXLANIsKept is the #80 case: the VNI is what every
// node builds the network's VXLAN interface from, so a re-allocation must keep it.
// Renumbering a live network leaves its replicas without an L2.
func TestAllocateNetwork_RecordedVXLANIsKept(t *testing.T) {
	p := newPoolProvider(t, flatPoolConfig(t))

	allocated, err := p.AllocateNetwork(NetworkAllocationRequest{
		Name: "net-a", Driver: "vxlan", RequestedSubnet: "10.0.3.0/24",
		ExistingSubnet: true, RecordedVXLAN: 4045883,
	})
	require.NoError(t, err)
	assert.Equal(t, uint32(4045883), allocated.VXLANID, "a recorded VNI must be kept")
}

// TestAllocateNetwork_NeverTakesATakenVNI covers the other half: two networks with
// one VNI cannot both exist on a node, because the interface lookup is by VNI — the
// second interface is never created and that network silently loses cross-node
// connectivity.
func TestAllocateNetwork_NeverTakesATakenVNI(t *testing.T) {
	p := newPoolProvider(t, flatPoolConfig(t))

	// The VNI the next allocation would otherwise take (the first allocation gets
	// index 1).
	taken := GenerateVXLANID("second", 1)
	p.networks["seed"] = &AllocatedNetwork{Name: "seed", VXLANID: taken}

	allocated, err := p.AllocateNetwork(NetworkAllocationRequest{Name: "second", Driver: "vxlan"})
	require.NoError(t, err)
	assert.NotEqual(t, taken, allocated.VXLANID, "a taken VNI must not be handed out")
	assert.NotZero(t, allocated.VXLANID, "the network still needs a VNI")

	// Only the seed may hold the taken value; the new network holds its own.
	holders := 0
	for _, n := range p.networks {
		if n.VXLANID == taken {
			holders++
		}
	}
	assert.Equal(t, 1, holders, "the taken VNI stays with the network that holds it")
}

// TestAllocateNetwork_DistinctVXLANsForDistinctNetworks checks the ordinary case.
func TestAllocateNetwork_DistinctVXLANsForDistinctNetworks(t *testing.T) {
	p := newPoolProvider(t, flatPoolConfig(t))

	first, err := p.AllocateNetwork(NetworkAllocationRequest{Name: "one", Driver: "vxlan"})
	require.NoError(t, err)
	second, err := p.AllocateNetwork(NetworkAllocationRequest{Name: "two", Driver: "vxlan"})
	require.NoError(t, err)

	assert.NotZero(t, first.VXLANID)
	assert.NotZero(t, second.VXLANID)
	assert.NotEqual(t, first.VXLANID, second.VXLANID, "two networks must not share a VNI")
}

// TestAllocateNetwork_BridgeNetworksGetNoVNI keeps the driver distinction.
func TestAllocateNetwork_BridgeNetworksGetNoVNI(t *testing.T) {
	p := newPoolProvider(t, flatPoolConfig(t))

	allocated, err := p.AllocateNetwork(NetworkAllocationRequest{Name: "plain", Driver: "bridge"})
	require.NoError(t, err)
	assert.Zero(t, allocated.VXLANID, "a bridge network is not an overlay and gets no VNI")
}

// TestReleaseNetwork_FreesTheSubnet keeps the ledger honest: a deleted network must
// not keep its subnet reserved for the lifetime of the daemon.
func TestReleaseNetwork_FreesTheSubnet(t *testing.T) {
	p := newPoolProvider(t, flatPoolConfig(t))

	first, err := p.AllocateNetwork(NetworkAllocationRequest{
		Name: "first", Driver: "vxlan", RequestedSubnet: "10.10.0.0/24",
	})
	require.NoError(t, err)

	_, err = p.AllocateNetwork(NetworkAllocationRequest{
		Name: "second", Driver: "vxlan", RequestedSubnet: "10.10.0.0/24",
	})
	require.Error(t, err, "the subnet is taken while the network exists")

	p.ReleaseNetwork(first.Name, first.Subnet.String())

	_, err = p.AllocateNetwork(NetworkAllocationRequest{
		Name: "third", Driver: "vxlan", RequestedSubnet: "10.10.0.0/24",
	})
	require.NoError(t, err, "a released subnet must be usable again")
}

// TestAllocateNetwork_OverlayPoolEqualToTheFlatSubnetFailsClearly covers the
// operator footgun: if the overlay pool is the flat L2 subnet, every candidate is
// reserved. The error has to say so rather than look like an empty pool.
func TestAllocateNetwork_OverlayPoolEqualToTheFlatSubnetFailsClearly(t *testing.T) {
	cfg := flatPoolConfig(t)
	cfg.OverlaySubnetPool = "192.168.127.0/24"
	p := newPoolProvider(t, cfg)

	_, err := p.AllocateNetwork(NetworkAllocationRequest{Name: "net", Driver: "vxlan"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no free /24 subnet in pool 192.168.127.0/24")
	assert.Contains(t, err.Error(), "reserved")
}

// TestAllocateNetwork_NoOverlayPoolFallsBackToTheFlatPool keeps the previous
// behaviour for a configuration that sets no overlay pool (a single-pool
// deployment or a test), so the provider change is additive. The daemon always
// sets one, so this is not the production path.
func TestAllocateNetwork_NoOverlayPoolFallsBackToTheFlatPool(t *testing.T) {
	p := newPoolProvider(t, &CNIConfig{SubnetPool: "10.0.0.0/8"})

	allocated, err := p.AllocateNetwork(NetworkAllocationRequest{Name: "legacy", Driver: "bridge"})
	require.NoError(t, err)
	require.NotNil(t, allocated.Subnet)
	assert.Equal(t, "10.0.1.0/24", allocated.Subnet.String(),
		"an unset overlay pool keeps carving from SubnetPool")
}

func TestCidrsOverlap(t *testing.T) {
	_, a, _ := net.ParseCIDR("10.0.0.0/24")
	_, inner, _ := net.ParseCIDR("10.0.0.128/25")
	_, sibling, _ := net.ParseCIDR("10.0.1.0/24")
	_, wide, _ := net.ParseCIDR("10.0.0.0/16")

	assert.True(t, cidrsOverlap(a, inner), "a /25 inside a /24 overlaps it")
	assert.True(t, cidrsOverlap(a, wide), "a /24 inside a /16 overlaps it")
	assert.True(t, cidrsOverlap(wide, a), "overlap is symmetric")
	assert.False(t, cidrsOverlap(a, sibling), "sibling /24s do not overlap")
	assert.False(t, cidrsOverlap(a, nil))
	assert.False(t, cidrsOverlap(nil, nil))
}
