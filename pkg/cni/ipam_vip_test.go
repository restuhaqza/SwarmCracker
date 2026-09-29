package cni

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// poolOwner returns the owner recorded for ip in pool, if any.
func poolOwner(pool *IPPool, ip net.IP) (string, bool) {
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	owner, ok := pool.UsedIPs[ip.String()]
	return owner, ok
}

// TestIPAMManager_AllocateVIP verifies VIPs come from the very top of the
// subnet (broadcast minus 1, then descending) and are tracked in the pool.
func TestIPAMManager_AllocateVIP(t *testing.T) {
	mgr := NewIPAMManager(nil)
	pool, err := mgr.CreatePool("10.0.0.0/24", nil)
	require.NoError(t, err)

	vip, err := mgr.AllocateVIP("10.0.0.0/24", "svc-1")
	require.NoError(t, err)
	require.NotNil(t, vip)

	vip4 := vip.To4()
	require.NotNil(t, vip4, "VIP must be IPv4")

	network := net.ParseIP("10.0.0.0").To4()
	gateway := net.ParseIP("10.0.0.1").To4()
	broadcast := net.ParseIP("10.0.0.255").To4()

	// The highest usable host address of a /24 is the broadcast minus one.
	assert.Equal(t, "10.0.0.254", vip.String())
	assert.True(t, pool.Subnet.Contains(vip), "VIP must be inside the subnet")

	// Must be in the top 16 host addresses of the /24.
	assert.GreaterOrEqual(t, int(vip4[3]), 240, "VIP should be in the top 16 host addresses")
	assert.LessOrEqual(t, int(vip4[3]), 254, "VIP must not be the broadcast address")

	// Never the network, gateway, or broadcast address.
	assert.False(t, vip4.Equal(network), "must not hand out the network address")
	assert.False(t, vip4.Equal(gateway), "must not hand out the gateway")
	assert.False(t, vip4.Equal(broadcast), "must not hand out the broadcast address")

	// Recorded in the pool, owned by the requesting service.
	owner, ok := poolOwner(pool, vip)
	require.True(t, ok, "VIP must be recorded in the pool")
	assert.Equal(t, "vip:svc-1", owner)

	// A second VIP descends from the first, staying in the VIP range.
	vip2, err := mgr.AllocateVIP("10.0.0.0/24", "svc-2")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.253", vip2.String())
	assert.NotEqual(t, vip.String(), vip2.String())
}

// TestIPAMManager_AllocateVIP_NoPool verifies an unknown subnet errors.
func TestIPAMManager_AllocateVIP_NoPool(t *testing.T) {
	mgr := NewIPAMManager(nil)

	vip, err := mgr.AllocateVIP("10.9.9.0/24", "svc-1")
	assert.Error(t, err)
	assert.Nil(t, vip)
}

// TestIPAMManager_ReleaseVIP verifies releasing clears the owner and that
// releasing an unknown VIP errors.
func TestIPAMManager_ReleaseVIP(t *testing.T) {
	mgr := NewIPAMManager(nil)
	pool, err := mgr.CreatePool("10.0.0.0/24", nil)
	require.NoError(t, err)

	vip, err := mgr.AllocateVIP("10.0.0.0/24", "svc-1")
	require.NoError(t, err)

	owner, err := mgr.GetAllocationOwner(vip, "10.0.0.0/24")
	require.NoError(t, err)
	assert.Equal(t, "vip:svc-1", owner)

	require.NoError(t, mgr.ReleaseVIP(vip, "10.0.0.0/24", "svc-1"))

	if owner, ok := poolOwner(pool, vip); ok {
		t.Fatalf("VIP %s still owned by %q after release", vip, owner)
	}
	_, err = mgr.GetAllocationOwner(vip, "10.0.0.0/24")
	assert.Error(t, err, "released VIP must no longer have an owner")

	// Releasing an address that was never allocated (or already released) errors.
	err = mgr.ReleaseVIP(net.ParseIP("10.0.0.200"), "10.0.0.0/24", "svc-1")
	assert.Error(t, err)

	// Releasing a VIP in an unknown pool errors.
	err = mgr.ReleaseVIP(net.ParseIP("10.9.9.200"), "10.9.9.0/24", "svc-1")
	assert.Error(t, err)
}

// TestGetVIPRangeStart pins the VIP range start to the highest usable host
// address of the subnet (broadcast minus one).
func TestGetVIPRangeStart(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		want    string
		wantNil bool
	}{
		{name: "slash24", cidr: "10.0.0.0/24", want: "10.0.0.254"},
		{name: "slash16", cidr: "10.0.0.0/16", want: "10.0.255.254"},
		{name: "slash25", cidr: "10.0.0.0/25", want: "10.0.0.126"},
		{name: "ipv6_nil", cidr: "fd00::/64", wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, subnet, err := net.ParseCIDR(tt.cidr)
			require.NoError(t, err)

			got := getVIPRangeStart(subnet)

			if tt.wantNil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.want, got.String())
		})
	}
}

// TestIPAMManager_AllocateIP_Reserved verifies the network, gateway, and
// broadcast addresses are never handed out, and that a /24 pool hands out
// exactly its 253 allocatable addresses before reporting exhaustion.
func TestIPAMManager_AllocateIP_Reserved(t *testing.T) {
	mgr := NewIPAMManager(nil)
	_, err := mgr.CreatePool("10.0.0.0/24", nil)
	require.NoError(t, err)

	network := net.ParseIP("10.0.0.0").To4()
	gateway := net.ParseIP("10.0.0.1").To4()
	broadcast := net.ParseIP("10.0.0.255").To4()

	// /24 = 256 addresses, minus network and broadcast, minus the reserved
	// gateway = 253 allocatable addresses.
	const allocatable = 253

	seen := make(map[string]struct{}, allocatable)
	for i := 0; i < allocatable; i++ {
		ip, err := mgr.AllocateIP("10.0.0.0/24", "owner")
		require.NoError(t, err, "allocation %d should succeed", i)
		require.NotNil(t, ip)

		ip4 := ip.To4()
		require.NotNil(t, ip4)
		assert.False(t, ip4.Equal(network), "handed out network address %s", ip)
		assert.False(t, ip4.Equal(gateway), "handed out gateway %s", ip)
		assert.False(t, ip4.Equal(broadcast), "handed out broadcast %s", ip)

		_, dup := seen[ip.String()]
		assert.False(t, dup, "duplicate allocation %s", ip)
		seen[ip.String()] = struct{}{}
	}
	assert.Len(t, seen, allocatable)

	// The pool is now exhausted.
	ip, err := mgr.AllocateIP("10.0.0.0/24", "owner")
	assert.Error(t, err)
	assert.Nil(t, ip)
}

// TestIPAMManager_AllocateIP_Exhaustion verifies a minimal /30 pool yields a
// single usable address (.2) and then reports exhaustion rather than handing
// out the broadcast (.3) or looping forever.
func TestIPAMManager_AllocateIP_Exhaustion(t *testing.T) {
	mgr := NewIPAMManager(nil)
	_, err := mgr.CreatePool("10.0.0.0/30", nil)
	require.NoError(t, err)

	// /30: network .0, gateway .1, broadcast .3 => only .2 is allocatable.
	ip, err := mgr.AllocateIP("10.0.0.0/30", "owner")
	require.NoError(t, err)
	require.NotNil(t, ip)
	assert.Equal(t, "10.0.0.2", ip.String())

	ip2, err := mgr.AllocateIP("10.0.0.0/30", "owner")
	assert.Error(t, err)
	assert.Nil(t, ip2)

	// Unknown pool still errors clearly.
	ip3, err := mgr.AllocateIP("10.9.9.0/30", "owner")
	assert.Error(t, err)
	assert.Nil(t, ip3)
}

// TestIPAMManager_AllocateVIP_SmallSubnet verifies that a /30 pool hands out
// only its single usable host address (.2) as a VIP, never the network (.0),
// gateway (.1), or broadcast (.3) address, and reports exhaustion afterwards.
func TestIPAMManager_AllocateVIP_SmallSubnet(t *testing.T) {
	mgr := NewIPAMManager(nil)
	_, err := mgr.CreatePool("10.0.0.0/30", nil)
	require.NoError(t, err)

	// /30: network .0, gateway .1, broadcast .3 => only .2 is usable.
	vip, err := mgr.AllocateVIP("10.0.0.0/30", "svc-1")
	require.NoError(t, err)
	require.NotNil(t, vip, "VIP must not be nil for a supported subnet")
	assert.Equal(t, "10.0.0.2", vip.String())

	// The pool is exhausted: no network, gateway, or broadcast address is
	// handed out, and the failure is reported (not (nil, nil)).
	vip2, err := mgr.AllocateVIP("10.0.0.0/30", "svc-2")
	assert.Error(t, err)
	assert.Nil(t, vip2)
}

// TestIPAMManager_AllocateVIP_IPv6Unsupported verifies that IPv6 VIP
// allocation fails loudly instead of returning a nil IP with no error (and
// recording the bogus owner "<nil>").
func TestIPAMManager_AllocateVIP_IPv6Unsupported(t *testing.T) {
	mgr := NewIPAMManager(nil)
	_, err := mgr.CreatePool("fd00::/64", nil)
	require.NoError(t, err)

	vip, err := mgr.AllocateVIP("fd00::/64", "svc-1")
	assert.Error(t, err)
	assert.Nil(t, vip)
}
