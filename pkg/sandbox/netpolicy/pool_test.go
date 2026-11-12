package netpolicy

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// list is a helper for the occupancy fixtures: the listed CIDRs as prefixes.
func list(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(cidr)
		require.NoError(t, err)
		out = append(out, prefix)
	}
	return out
}

// TestPoolHandsOutOneAddressSpacePerSandbox is SBX-036's criterion: the sandboxes a
// node can hold are no longer bounded by a shared /24. A hundred of them get a
// hundred different /30s, and none of them shares an address with another.
func TestPoolHandsOutOneAddressSpacePerSandbox(t *testing.T) {
	pool := netip.MustParsePrefix(DefaultPool)
	leases := make(map[string]netip.Prefix)

	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("sandbox-%d", i)
		prefix, ok := carve(pool, nil, leases)
		require.True(t, ok, "the pool ran out after %d sandboxes", i)
		leases[id] = prefix
	}

	require.Len(t, leases, 100)
	distinct := make(map[netip.Prefix]string, len(leases))
	for id, prefix := range leases {
		require.True(t, pool.Overlaps(prefix), "%s of %s is outside the pool", prefix, id)
		other, taken := distinct[prefix]
		require.False(t, taken, "%s and %s both hold %s", other, id, prefix)
		distinct[prefix] = id
	}
}

// TestPoolSkipsWhatTheNodeAlreadyUses: an address the node has, and a range it
// routes, are both in use. The first candidate is occupied in each case, so a pool
// that ignored occupancy would answer the same address the node owns.
func TestPoolSkipsWhatTheNodeAlreadyUses(t *testing.T) {
	pool := netip.MustParsePrefix("10.63.0.0/29")

	// The first /30 (10.63.0.0/30) is the node's own; the answer must be the second.
	got, ok := carve(pool, list(t, "10.63.0.1/30"), nil)
	require.True(t, ok)
	assert.Equal(t, "10.63.0.4/30", got.String())

	// A /29 route covers the first two /30s; the answer must be the third.
	got, ok = carve(pool, list(t, "10.63.0.0/29"), nil)
	require.False(t, ok, "a /29 pool whose only two /30s are covered has nothing left")

	wide := netip.MustParsePrefix("10.63.0.0/28")
	got, ok = carve(wide, list(t, "10.63.0.0/29"), nil)
	require.True(t, ok)
	assert.Equal(t, "10.63.0.8/30", got.String())
}

// TestPoolSkipsAnotherSandboxsLease: the host cannot answer for a /30 that is only
// inside a namespace it does not list, so the leases are checked as well.
func TestPoolSkipsAnotherSandboxsLease(t *testing.T) {
	pool := netip.MustParsePrefix("10.63.0.0/29")
	leases := map[string]netip.Prefix{"first": netip.MustParsePrefix("10.63.0.0/30")}

	got, ok := carve(pool, nil, leases)
	require.True(t, ok)
	assert.Equal(t, "10.63.0.4/30", got.String())
}

// TestPoolAnswersTheSameAddressWhileASandboxHoldsIt: an address is the identity a
// guest is already wearing, so reserving twice for one sandbox is one address.
func TestPoolAnswersTheSameAddressWhileASandboxHoldsIt(t *testing.T) {
	recorder := &execRecorder{}
	m := newTestManager(t, recorder)
	ctx := context.Background()

	first, took, err := m.reserve(ctx, "alpha")
	require.NoError(t, err)
	assert.True(t, took, "the first reservation is what took the address")

	second, tookAgain, err := m.reserve(ctx, "alpha")
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.False(t, tookAgain, "a second reservation of a held address took nothing")

	other, tookOther, err := m.reserve(ctx, "beta")
	require.NoError(t, err)
	assert.NotEqual(t, first, other, "two sandboxes do not share an address")
	assert.True(t, tookOther)

	m.forget("alpha")
	third, _, err := m.reserve(ctx, "gamma")
	require.NoError(t, err)
	assert.Equal(t, first, third, "a released address is handed to the next sandbox")
}

// TestPoolRefusesWhenItIsFull: a node out of addresses must say so rather than hand
// out one it is using. The message names the pool, because that is what an operator
// has to grow.
func TestPoolRefusesWhenItIsFull(t *testing.T) {
	m, err := New(Config{
		Pool:      netip.MustParsePrefix("10.63.0.0/30"),
		Exec:      (&execRecorder{}).exec,
		LookPath:  testBinaries(t),
		Occupancy: func(context.Context) ([]netip.Prefix, error) { return nil, nil },
	})
	require.NoError(t, err)

	_, _, err = m.reserve(context.Background(), "alpha")
	require.NoError(t, err)

	_, _, err = m.reserve(context.Background(), "beta")
	require.ErrorIs(t, err, errNoPoolSpace)
	assert.Contains(t, err.Error(), "10.63.0.0/30")
}

// TestOccupancyReadsTheNodesOwnAnswer is the probe that keeps a sandbox pool out of
// a range another component is already using.
//
// The listing is the real shape `ip -o` prints, continuation and all: a long record
// is wrapped with a backslash, and a reader that split on newlines alone would read
// a record's tail as a record of its own. The default route is in it because a
// pool carved out of "everything" would be no pool at all.
func TestOccupancyReadsTheNodesOwnAnswer(t *testing.T) {
	addresses := "41: br0    inet 10.0.0.1/24 brd 10.0.0.255 scope global br0\\       valid_lft forever preferred_lft forever\n" +
		"17: swarm-br0    inet 192.168.127.1/24 scope global swarm-br0\\       valid_lft forever preferred_lft forever\n" +
		"1: lo    inet 127.0.0.1/8 scope host lo\\       valid_lft forever preferred_lft forever\n"
	routes := "default via 192.168.18.1 dev wlp3s0 proto dhcp src 192.168.18.25 metric 600 \n" +
		"192.168.1.0/24 via 10.1.0.2 dev test-br0 linkdown \n" +
		"10.5.5.0/24 dev br-storage proto kernel scope link src 10.5.5.1 \n"
	sandbox := "2: tap0    inet 10.63.0.1/30 scope global tap0\\       valid_lft forever preferred_lft forever\n"

	recorder := &execRecorder{reply: func(argv []string) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "netns list"):
			return []byte("sbx-keeper (id: 0)\n"), nil
		case strings.Contains(joined, "netns exec sbx-keeper") && strings.Contains(joined, "addr show"):
			return []byte(sandbox), nil
		case strings.HasSuffix(joined, "addr show"):
			return []byte(addresses), nil
		case strings.HasSuffix(joined, "route show"):
			return []byte(routes), nil
		default:
			return nil, fmt.Errorf("unexpected command %q", joined)
		}
	}}

	occupancy := hostOccupancy(recorder.exec, "/usr/bin/ip")
	got, err := occupancy(context.Background())
	require.NoError(t, err)

	want := map[string]bool{
		"10.0.0.0/24":      true, // the node's own address
		"192.168.127.0/24": true,
		"127.0.0.0/8":      true,
		"192.168.1.0/24":   true, // a route through a gateway, with no local address
		"10.5.5.0/24":      true,
		"10.63.0.0/30":     true, // an address of a namespace the node holds
	}
	have := make(map[string]bool, len(got))
	for _, prefix := range got {
		have[prefix.String()] = true
	}
	assert.Equal(t, want, have, "the occupancy is every address and every explicit route, and no default route")
}

// TestOccupancyFailsLoudlyWhenItCannotReadTheNode: an allocator that cannot tell
// what is in use must refuse to allocate, not guess.
func TestOccupancyFailsLoudlyWhenItCannotReadTheNode(t *testing.T) {
	recorder := &execRecorder{reply: func([]string) ([]byte, error) {
		return nil, errors.New("ip: cannot read")
	}}
	m := newTestManager(t, recorder)
	m.occupancy = hostOccupancy(recorder.exec, "/usr/bin/ip")

	_, err := m.Attach(context.Background(), "alpha", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already uses")
	assert.Empty(t, recorder.ran("netns add"), "nothing was created before the node's state was known")
}

// TestNewRefusesWhatCannotEnforceThePolicy: both binaries are load-bearing, so a
// node that cannot run one must not build a manager that would create a sandbox
// without a namespace or without a policy. A sandbox that cannot be denied is the
// one thing this package exists to prevent.
func TestNewRefusesWhatCannotEnforceThePolicy(t *testing.T) {
	find := func(found map[string]string) func(string) (string, error) {
		return func(name string) (string, error) {
			if path, ok := found[name]; ok {
				return path, nil
			}
			return "", fmt.Errorf("executable file not found in $PATH: %s", name)
		}
	}

	t.Run("no iproute2", func(t *testing.T) {
		_, err := New(Config{LookPath: find(map[string]string{})})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "iproute2")
	})

	t.Run("no nftables anywhere", func(t *testing.T) {
		_, err := New(Config{LookPath: find(map[string]string{"ip": "/usr/bin/ip"})})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nftables")
	})

	t.Run("nftables off PATH is still found", func(t *testing.T) {
		m, err := New(Config{LookPath: find(map[string]string{"ip": "/usr/bin/ip", "/usr/sbin/nft": "/usr/sbin/nft"})})
		require.NoError(t, err)
		assert.Equal(t, "/usr/sbin/nft", m.nft)
	})

	t.Run("an IPv6 pool", func(t *testing.T) {
		_, err := New(Config{Pool: netip.MustParsePrefix("fd00::/64"), LookPath: testBinaries(t)})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not IPv4")
	})

	t.Run("a pool smaller than one sandbox", func(t *testing.T) {
		_, err := New(Config{Pool: netip.MustParsePrefix("10.63.0.0/31"), LookPath: testBinaries(t)})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "10.63.0.0/31")
	})

	t.Run("the default pool", func(t *testing.T) {
		m, err := New(Config{LookPath: testBinaries(t)})
		require.NoError(t, err)
		assert.Equal(t, DefaultPool, m.Pool().String())
	})
}

// TestAttachRefusesACancelledContext: an attach that is already abandoned does not
// create a namespace, which is what a caller that timed out is relying on.
func TestAttachRefusesACancelledContext(t *testing.T) {
	recorder := &execRecorder{}
	m := newTestManager(t, recorder)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.Attach(ctx, "alpha", nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, recorder.sequence())
}

// TestNetworkWithoutAGatewayHasNoResolver: the address is what the resolver is
// named after, so a network that was never allocated answers none rather than
// inventing one.
func TestNetworkWithoutAGatewayHasNoResolver(t *testing.T) {
	assert.Nil(t, Network{}.DNS())
}

// TestPoolIsTheOneTheManagerWasBuiltWith: the pool is the operator's, and a reader
// asking for it gets the masked form they configured.
func TestPoolIsTheOneTheManagerWasBuiltWith(t *testing.T) {
	m, err := New(Config{Pool: netip.MustParsePrefix("10.63.5.1/16"), LookPath: testBinaries(t)})
	require.NoError(t, err)
	assert.Equal(t, "10.63.0.0/16", m.Pool().String())
}

// TestAttachLeavesNothingBehindWhenAnAllocationFails: a node whose state cannot be
// read does not get a sandbox, and nothing was created for it.
func TestAttachLeavesNothingBehindWhenAnAllocationFails(t *testing.T) {
	recorder := &execRecorder{}
	m, err := New(Config{
		Pool:     netip.MustParsePrefix("10.63.0.0/30"),
		Exec:     recorder.exec,
		LookPath: testBinaries(t),
		Occupancy: func(context.Context) ([]netip.Prefix, error) {
			return []netip.Prefix{netip.MustParsePrefix("10.63.0.0/30")}, nil
		},
	})
	require.NoError(t, err)

	_, err = m.Attach(context.Background(), "alpha", nil)
	require.ErrorIs(t, err, errNoPoolSpace)
	assert.Empty(t, recorder.sequence(), "a sandbox with no address gets no namespace")
}

// TestPolicyScriptIsDenyByDefault holds the ruleset itself to account: three chains,
// every one of them a default drop, and — with no allowlist — the only packets
// accepted are the namespace's own loopback. A chain that accepted anything else,
// or that lost its policy, would be an egress path.
func TestPolicyScriptIsDenyByDefault(t *testing.T) {
	script := policyScript(netip.MustParseAddr("10.63.0.1"), nil)

	assert.Contains(t, script, "destroy table inet "+policyTable, "the script replaces any table a half-finished attach left")
	for _, chain := range []string{"input", "forward", "output"} {
		assert.Contains(t, script, "chain "+chain+" {")
	}
	assert.Equal(t, 3, strings.Count(script, "policy drop"), "every hook denies by default")
	assert.NotContains(t, script, "policy accept")
	assert.NotContains(t, script, "masquerade", "a sandbox network is never NATed")
	assert.NotContains(t, script, "ct state")

	accepts := strings.Count(script, " accept")
	assert.Equal(t, 2, accepts, "only the namespace's own loopback is accepted, got %d accepts", accepts)
	assert.NotContains(t, script, "iifname \"eth0\"", "the guest's traffic is not accepted anywhere")
}
