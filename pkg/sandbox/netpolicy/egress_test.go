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

// denyAllScript is the ruleset exactly as it was before an allowlist existed. The
// empty allowlist is compared against it byte for byte, because "an allowlist with
// no entries adds no rules" is the easy way to break `egress: none`, and a comparison
// of behaviour would let a changed script pass as long as the change happened to
// allow nothing.
const denyAllScript = `destroy table inet sandbox
table inet sandbox {
	chain input {
		type filter hook input priority filter; policy drop;
		iifname "lo" accept
		counter drop
	}

	chain forward {
		type filter hook forward priority filter; policy drop;
		counter drop
	}

	chain output {
		type filter hook output priority filter; policy drop;
		oifname "lo" accept
		counter drop
	}
}
`

// TestAnEmptyAllowlistIsTheRulesetItAlwaysWas is the regression this slice is most
// likely to cause: a sandbox with no allowlist must be the same sandbox, not a
// sandbox whose table happens to be empty of accepts.
func TestAnEmptyAllowlistIsTheRulesetItAlwaysWas(t *testing.T) {
	gateway := netip.MustParseAddr("10.63.0.1")

	assert.Equal(t, denyAllScript, policyScript(gateway, nil), "a nil allowlist is egress: none")
	assert.Equal(t, denyAllScript, policyScript(gateway, []Destination{}), "so is an empty one")
}

// TestAnAllowlistAcceptsOnlyItsOwnAddressesAndPorts: the table an allowlist
// produces is the deny-all table plus one accept per allowed (address, port) in each
// direction, and nothing else. A rule for another port, another address, another
// protocol or a forwarded packet would each be a hole the caller did not ask for.
func TestAnAllowlistAcceptsOnlyItsOwnAddressesAndPorts(t *testing.T) {
	gateway := netip.MustParseAddr("10.63.0.1")
	literal := netip.MustParseAddr("203.0.113.10")

	script := policyScript(gateway, []Destination{
		{Host: "203.0.113.10", Port: 443, Addresses: []netip.Addr{literal}},
	})

	// What the guest may send, and what the proxy may send back.
	assert.Contains(t, script, "ip daddr 203.0.113.10 tcp dport 443 counter accept")
	assert.Contains(t, script, "ip saddr 203.0.113.10 tcp sport 443 counter accept")

	// Every accept counts, so "was this path used" is a question the kernel
	// answers, and the counters are not only on the drops.
	for _, line := range strings.Split(script, "\n") {
		if strings.HasSuffix(line, "accept") && !strings.Contains(line, `iifname "lo"`) && !strings.Contains(line, `oifname "lo"`) {
			assert.Contains(t, line, "counter", "an accept with no counter says nothing about its own traffic: %q", line)
		}
	}

	// Nothing else opens: no other port, no other address, no ICMP, and the
	// forward chain still accepts nothing at all.
	assert.NotContains(t, script, "dport 80")
	assert.NotContains(t, script, "1.1.1.1")
	assert.NotContains(t, script, "icmp")
	assert.Equal(t, 3, strings.Count(script, "policy drop"), "every chain still denies by policy")
	forward := script[strings.Index(script, "chain forward"):strings.Index(script, "chain output")]
	assert.NotContains(t, forward, "accept", "an allowlisted sandbox still forwards nothing")

	// A literal allowlist has no name for a view to answer, so the sandbox gets no
	// resolver rules either: answering nothing is the DNS half of egress: none.
	assert.NotContains(t, script, "dport 53", "an allowlist of literals opens no resolver")
	assert.NotContains(t, script, "sport 53")
}

// TestTheResolversPortsAreOpenedForAName: a name is resolved by the view, so its
// queries to the gateway are part of what the allowlist has to allow — in both
// directions, and only on the gateway.
func TestTheResolversPortsAreOpenedForAName(t *testing.T) {
	gateway := netip.MustParseAddr("10.63.0.1")
	script := policyScript(gateway, []Destination{{
		Host:      "api.example.com",
		Port:      8443,
		Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.7")},
	}})

	assert.Contains(t, script, "ip daddr 10.63.0.1 udp dport 53 counter accept")
	assert.Contains(t, script, "ip daddr 10.63.0.1 tcp dport 53 counter accept")
	assert.Contains(t, script, "ip saddr 10.63.0.1 udp sport 53 counter accept")
	assert.Contains(t, script, "ip saddr 10.63.0.1 tcp sport 53 counter accept")
	assert.Contains(t, script, "ip daddr 198.51.100.7 tcp dport 8443 counter accept")
	assert.Contains(t, script, "ip saddr 198.51.100.7 tcp sport 8443 counter accept")
}

// TestTheRulesetIsAFunctionOfTheSetNotOfTheOrder: two callers that allow the same
// destinations write the same table, which is what makes an applied policy
// comparable and a diff readable.
func TestTheRulesetIsAFunctionOfTheSetNotOfTheOrder(t *testing.T) {
	gateway := netip.MustParseAddr("10.63.0.1")
	a := netip.MustParseAddr("203.0.113.10")
	b := netip.MustParseAddr("198.51.100.7")

	first := policyScript(gateway, []Destination{
		{Host: "a.example", Port: 443, Addresses: []netip.Addr{a}},
		{Host: "b.example", Port: 80, Addresses: []netip.Addr{b}},
	})
	second := policyScript(gateway, []Destination{
		{Host: "b.example", Port: 80, Addresses: []netip.Addr{b}},
		{Host: "a.example", Port: 443, Addresses: []netip.Addr{a}},
	})
	assert.Equal(t, first, second)

	// One address on two ports is two rules, and one address on one port is one.
	shared := policyScript(gateway, []Destination{
		{Host: "a.example", Port: 443, Addresses: []netip.Addr{a}},
		{Host: "a.example", Port: 8443, Addresses: []netip.Addr{a}},
	})
	assert.Equal(t, 1, strings.Count(shared, "ip daddr 203.0.113.10 tcp dport 443 counter accept"))
	assert.Contains(t, shared, "ip daddr 203.0.113.10 tcp dport 8443 counter accept")
}

// resolverFor is a Resolve that answers from a map, and records what it was asked,
// so a test can prove a literal is not resolved and a name is resolved once.
func resolverFor(t *testing.T, answers map[string][]string) (Resolve, *[]string) {
	t.Helper()
	var asked []string
	resolve := func(_ context.Context, host string) ([]netip.Addr, error) {
		asked = append(asked, host)
		resolved, ok := answers[host]
		if !ok {
			return nil, fmt.Errorf("no such name: %s", host)
		}
		addresses := make([]netip.Addr, 0, len(resolved))
		for _, address := range resolved {
			addresses = append(addresses, netip.MustParseAddr(address))
		}
		return addresses, nil
	}
	return resolve, &asked
}

// TestPlanningResolvesNamesOnceAndLeavesLiteralsAlone: the addresses the guest is
// told and the addresses the namespace accepts are one list, produced once — a
// literal is not looked up, and a name is looked up exactly once per attach.
func TestPlanningResolvesNamesOnceAndLeavesLiteralsAlone(t *testing.T) {
	recorder := &execRecorder{}
	m := newTestManager(t, recorder)
	resolve, asked := resolverFor(t, map[string][]string{
		"api.example.com": {"198.51.100.7", "198.51.100.8"},
	})
	m.resolve = resolve

	n, err := m.Attach(context.Background(), "alpha", []string{"api.example.com:8443", "203.0.113.10:53"})
	require.NoError(t, err)

	assert.Equal(t, []string{"api.example.com"}, *asked, "a literal is not a name and is not resolved")
	require.Len(t, n.Egress, 2, "one entry per destination")

	// Sorted by entry, so the readback is stable.
	assert.Equal(t, "203.0.113.10:53", n.Egress[0].String())
	assert.Equal(t, []netip.Addr{netip.MustParseAddr("203.0.113.10")}, n.Egress[0].Addresses)

	assert.Equal(t, "api.example.com:8443", n.Egress[1].String())
	assert.Equal(t, []netip.Addr{
		netip.MustParseAddr("198.51.100.7"),
		netip.MustParseAddr("198.51.100.8"),
	}, n.Egress[1].Addresses, "a name carries every address the node resolved, in its order")
}

// TestPlanningBoundsWhatOneNameBecomes: the addresses installed are the addresses
// answered, so a name is capped once and the cap applies to both.
func TestPlanningBoundsWhatOneNameBecomes(t *testing.T) {
	recorder := &execRecorder{}
	m := newTestManager(t, recorder)
	resolve, _ := resolverFor(t, map[string][]string{
		"wide.example.com": {"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4", "198.51.100.5"},
	})
	m.resolve = resolve

	n, err := m.Attach(context.Background(), "alpha", []string{"wide.example.com:443"})
	require.NoError(t, err)
	require.Len(t, n.Egress, 1)
	assert.Len(t, n.Egress[0].Addresses, maxAddressesPerName)
}

// TestPlanningRefusesADestinationItCannotHonour is the validation table: each of
// these would be a rule that does nothing, a rule that reaches the sandbox network,
// or a hole the allowance did not describe.
func TestPlanningRefusesADestinationItCannotHonour(t *testing.T) {
	recorder := &execRecorder{}
	m := newTestManager(t, recorder)
	resolve, _ := resolverFor(t, map[string][]string{})
	m.resolve = resolve

	for _, tc := range []struct {
		name  string
		allow []string
		want  string
	}{
		{"an empty entry", []string{""}, "is empty"},
		{"no port", []string{"example.com"}, "is not host:port"},
		{"a port that is not a number", []string{"example.com:http"}, "not a number"},
		{"port zero", []string{"example.com:0"}, "not a TCP port"},
		{"a port above the range", []string{"example.com:65536"}, "not a TCP port"},
		{"a URL", []string{"http://example.com:80"}, "is not host:port"},
		{"a path after the name", []string{"example.com/path:80"}, "is not a DNS name"},
		{"IPv6", []string{"[2001:db8::1]:443"}, "IPv6"},
		{"the unspecified address", []string{"0.0.0.0:80"}, "unspecified"},
		{"a loopback address", []string{"127.0.0.1:80"}, "loopback"},
		{"a multicast address", []string{"224.0.0.1:80"}, "multicast"},
		{"the broadcast address", []string{"255.255.255.255:80"}, "broadcast"},
		{"a sandbox's own link", []string{"10.63.0.2:80"}, "inside its own link"},
		{"another sandbox's address", []string{"10.63.9.9:80"}, "inside the sandbox pool"},
		{"the same destination twice", []string{"203.0.113.10:443", "203.0.113.10:443"}, "twice"},
		{"a name this node cannot resolve", []string{"nowhere.example.com:443"}, "cannot resolve"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.Attach(context.Background(), "alpha", tc.allow)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Empty(t, recorder.sequence(), "a refused allowlist touches nothing on the node")
		})
	}
}

// TestARefusedAllowlistTakesNoAddress: the pool is untouched by an attach that never
// happened, which is what lets the caller fix the allowlist and retry.
func TestARefusedAllowlistTakesNoAddress(t *testing.T) {
	recorder := &execRecorder{}
	m := newTestManager(t, recorder)

	_, err := m.Attach(context.Background(), "alpha", []string{"127.0.0.1:80"})
	require.Error(t, err)

	n, err := m.Attach(context.Background(), "beta", nil)
	require.NoError(t, err)
	assert.Equal(t, "10.63.0.0/30", n.CIDR.String(), "the refused attach held no address")
}

// TestAttachInstallsTheAddressesStartsTheEndpointsThenThePolicy holds the order of
// an allowlisted attach to account. The addresses have to be the namespace's before a
// listener can bind them, and the policy has to be last: the endpoints accept nothing
// until the table names them, and a sandbox is never described as allocated while it
// is still open.
func TestAttachInstallsTheAddressesStartsTheEndpointsThenThePolicy(t *testing.T) {
	recorder := &execRecorder{reply: func(argv []string) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "link show") {
			return nil, errors.New("Device \"tap0\" does not exist.")
		}
		return nil, nil
	}}
	m := newTestManager(t, recorder)
	resolve, _ := resolverFor(t, map[string][]string{"api.example.com": {"198.51.100.7"}})
	m.resolve = resolve

	n, err := m.Attach(context.Background(), "alpha", []string{"api.example.com:8443"})
	require.NoError(t, err)

	addressRuns := recorder.ran("addr replace 198.51.100.7/32 dev lo")
	require.Len(t, addressRuns, 1)
	assert.Contains(t, addressRuns[0], "netns exec sbx-alpha",
		"the egress address is an address of the sandbox's namespace, not the node's")

	sequence := recorder.sequence()
	addressAt := indexOf(sequence, addressRuns[0])
	startedAt := indexOf(sequence, "<egress start alpha>")
	policyRun := recorder.ran("nft", "-f")
	require.Len(t, policyRun, 1)
	policyAt := indexOf(sequence, policyRun[0])

	assert.Positive(t, addressAt)
	assert.Positive(t, startedAt)
	assert.Less(t, addressAt, startedAt, "the namespace owns the address before a listener binds it")
	assert.Less(t, startedAt, policyAt, "the policy is applied last")

	require.Len(t, n.Egress, 1)
	assert.Equal(t, "api.example.com", n.Egress[0].Host)
	assert.Equal(t, uint16(8443), n.Egress[0].Port)
}

// TestAttachInstallsOneAddressPerAddressAndOneEndpointSetPerSandbox: two
// destinations that resolve to the same address are one address on the namespace and
// one set of endpoints carrying both ports.
func TestAttachInstallsOneAddressPerAddress(t *testing.T) {
	recorder := &execRecorder{}
	m := newTestManager(t, recorder)

	_, err := m.Attach(context.Background(), "alpha", []string{"203.0.113.10:443", "203.0.113.10:8443"})
	require.NoError(t, err)

	assert.Len(t, recorder.ran("addr replace 203.0.113.10/32 dev lo"), 1)
	assert.Len(t, recorder.ran("<egress start alpha>"), 1, "one endpoint set for the sandbox, not one per entry")
}

// TestAttachClosesTheEndpointsItStartedWhenThePolicyFails: a sandbox whose table
// could not be installed must not keep listeners the policy does not describe.
func TestAttachClosesTheEndpointsItStartedWhenThePolicyFails(t *testing.T) {
	recorder := &execRecorder{reply: func(argv []string) ([]byte, error) {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "nft") {
			return []byte("nft: command not found"), errors.New("exit status 127")
		}
		return nil, nil
	}}
	m := newTestManager(t, recorder)

	_, err := m.Attach(context.Background(), "alpha", []string{"203.0.113.10:443"})
	require.Error(t, err)

	sequence := recorder.sequence()
	startedAt := indexOf(sequence, "<egress start alpha>")
	closedAt := indexOf(sequence, "<egress close>")
	require.Positive(t, startedAt)
	require.Positive(t, closedAt)
	assert.Less(t, startedAt, closedAt)
	assert.Len(t, recorder.ran("netns delete sbx-alpha"), 1, "and the namespace this call created is gone")
}

// TestDetachClosesTheEndpointsOnce: a sandbox's sockets belong to its namespace, so a
// teardown that left them open would leave the namespace alive with nothing naming
// it. A second detach has nothing left to close, which is what makes a retried
// teardown safe.
func TestDetachClosesTheEndpointsOnce(t *testing.T) {
	recorder := &execRecorder{}
	m := newTestManager(t, recorder)

	_, err := m.Attach(context.Background(), "alpha", []string{"203.0.113.10:443"})
	require.NoError(t, err)

	require.NoError(t, m.Detach(context.Background(), "alpha"))
	require.NoError(t, m.Detach(context.Background(), "alpha"))
	assert.Len(t, recorder.ran("<egress close>"), 1)
}

// TestARetriedAttachReplacesItsEndpoints: an attach that runs twice on one sandbox
// starts one set of endpoints and closes the one it found, so the sandbox does not
// accumulate listeners.
func TestARetriedAttachReplacesItsEndpoints(t *testing.T) {
	recorder := &execRecorder{reply: func(argv []string) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "netns list") {
			return []byte("sbx-alpha\n"), nil
		}
		return nil, nil
	}}
	m := newTestManager(t, recorder)

	_, err := m.Attach(context.Background(), "alpha", []string{"203.0.113.10:443"})
	require.NoError(t, err)
	_, err = m.Attach(context.Background(), "alpha", []string{"203.0.113.10:443"})
	require.NoError(t, err)

	assert.Len(t, recorder.ran("<egress start alpha>"), 2)
	assert.Len(t, recorder.ran("<egress close>"), 1, "the endpoints the first attach started are closed by the second")
}
