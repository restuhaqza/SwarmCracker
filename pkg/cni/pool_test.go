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

// TestAllocateNetwork_UserNetworkIsCarvedFromTheOverlayPool is the #77 case: a
// network created without a subnet used to be handed the flat L2 subnet, because
// the pool was --subnet and a single /24 pool always yields itself.
func TestAllocateNetwork_UserNetworkIsCarvedFromTheOverlayPool(t *testing.T) {
	p := newPoolProvider(t, &CNIConfig{
		SubnetPool:        "192.168.127.0/24",
		OverlaySubnetPool: "10.0.0.0/8",
		FlatSubnet:        "192.168.127.0/24",
	})

	allocated, err := p.AllocateNetwork("backend", "vxlan", "", "")
	require.NoError(t, err)
	require.NotNil(t, allocated.Subnet)
	assert.Equal(t, "10.0.1.0/24", allocated.Subnet.String(),
		"a user-defined network must be carved from the overlay pool, never the flat L2 subnet")
}

// TestAllocateNetwork_PredefinedNetworkStaysOnTheFlatL2 pins the #52 behaviour this
// change must not disturb: the predefined networks keep the flat-L2 pool, which is
// what makes ingress task IPs reachable cluster-wide.
func TestAllocateNetwork_PredefinedNetworkStaysOnTheFlatL2(t *testing.T) {
	p := newPoolProvider(t, &CNIConfig{
		SubnetPool:        "192.168.127.0/24",
		OverlaySubnetPool: "10.0.0.0/8",
		FlatSubnet:        "192.168.127.0/24",
	})

	ingress, err := p.AllocateNetwork(IngressNetworkName, "bridge", "", "")
	require.NoError(t, err)
	require.NotNil(t, ingress.Subnet)
	assert.Equal(t, "192.168.127.0/24", ingress.Subnet.String(),
		"the ingress network must stay on the flat L2 subnet")

	gwBridge, err := p.AllocateNetwork(GWBridgeNetworkName, "bridge", "", "")
	require.NoError(t, err)
	require.NotNil(t, gwBridge.Subnet)
	assert.Equal(t, "192.168.127.0/24", gwBridge.Subnet.String(),
		"the gateway bridge is predefined as well")
}

// TestAllocateNetwork_SkipsReservedAndUsedSubnets covers the carving loop: a
// candidate that overlaps the reserved flat L2 subnet, or a subnet a network
// already holds, is skipped rather than handed out.
func TestAllocateNetwork_SkipsReservedAndUsedSubnets(t *testing.T) {
	p := newPoolProvider(t, &CNIConfig{
		SubnetPool:        "192.168.127.0/24",
		OverlaySubnetPool: "10.0.0.0/8",
		// The flat L2 sits on the first overlay candidate, so it must be skipped.
		FlatSubnet: "10.0.1.0/24",
	})

	first, err := p.AllocateNetwork("one", "vxlan", "", "")
	require.NoError(t, err)
	require.NotNil(t, first.Subnet)
	assert.Equal(t, "10.0.2.0/24", first.Subnet.String(),
		"the reserved candidate must be skipped")

	second, err := p.AllocateNetwork("two", "vxlan", "", "")
	require.NoError(t, err)
	require.NotNil(t, second.Subnet)
	assert.Equal(t, "10.0.3.0/24", second.Subnet.String())
	assert.NotEqual(t, first.Subnet.String(), second.Subnet.String(),
		"distinct networks get distinct subnets")
}

// TestAllocateNetwork_RejectsAnExplicitSubnetThatIsNotFree is the guard: two
// networks on the same addresses cannot be told apart by route, NAT rule or IPAM
// pool, so an explicit subnet has to be free whichever client asked for it.
func TestAllocateNetwork_RejectsAnExplicitSubnetThatIsNotFree(t *testing.T) {
	p := newPoolProvider(t, &CNIConfig{
		SubnetPool:        "192.168.127.0/24",
		OverlaySubnetPool: "10.0.0.0/8",
		FlatSubnet:        "192.168.127.0/24",
	})

	_, err := p.AllocateNetwork("overlap-flat", "vxlan", "192.168.127.0/24", "")
	require.Error(t, err, "the flat L2 subnet must be refused")
	assert.Contains(t, err.Error(), "overlaps the flat L2 subnet 192.168.127.0/24")

	_, err = p.AllocateNetwork("overlap-super", "vxlan", "192.168.0.0/16", "")
	require.Error(t, err, "a subnet containing the flat L2 subnet overlaps it too")

	_, err = p.AllocateNetwork("first", "vxlan", "10.10.0.0/24", "")
	require.NoError(t, err, "a free explicit subnet is still honored")

	_, err = p.AllocateNetwork("second", "vxlan", "10.10.0.0/24", "")
	require.Error(t, err, "a duplicate explicit subnet must be refused")
	assert.Contains(t, err.Error(), "already has one")

	free, err := p.AllocateNetwork("third", "vxlan", "10.20.0.0/24", "")
	require.NoError(t, err)
	require.NotNil(t, free.Subnet)
	assert.Equal(t, "10.20.0.0/24", free.Subnet.String())
}

// TestAllocateNetwork_NoOverlayPoolFallsBackToTheFlatPool keeps the previous
// behaviour for a configuration that sets no overlay pool (a single-pool
// deployment or a test), so the change is additive.
func TestAllocateNetwork_NoOverlayPoolFallsBackToTheFlatPool(t *testing.T) {
	p := newPoolProvider(t, &CNIConfig{SubnetPool: "10.0.0.0/8"})

	allocated, err := p.AllocateNetwork("legacy", "bridge", "", "")
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
