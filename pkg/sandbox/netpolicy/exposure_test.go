package netpolicy

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The exposure half of a sandbox's policy, held to account. An exposed guest port is the
// one thing in this package that opens a path *into* the sandbox rather than out of it, so
// the tests here are the ones that say what it does not open — and that it does not touch
// what the sandbox may reach.

// TestAnExposureOpensOnlyItsOwnPort: exposing a guest port adds one accept in each
// direction — the node's connection out to the guest, and the guest's replies back — and
// nothing else. No other port, no other address, and no ICMP.
func TestAnExposureOpensOnlyItsOwnPort(t *testing.T) {
	guest := netip.MustParseAddr("10.63.0.2")

	script := exposureScript(guest, []uint16{8080})

	assert.Contains(t, script, `add rule inet sandbox preview_out oifname "tap0" ip daddr 10.63.0.2 tcp dport 8080 counter accept`,
		"the node's connection to the guest is accepted in the output direction, on the guest's own address and the exposed port")
	assert.Contains(t, script, `add rule inet sandbox preview_in iifname "tap0" ip saddr 10.63.0.2 tcp sport 8080 counter accept`,
		"the guest's replies are accepted in the input direction, from the exposed port")

	assert.NotContains(t, script, "8081")
	assert.NotContains(t, script, "icmp")
	assert.NotContains(t, script, "10.63.0.1", "the gateway itself is not opened")
	assert.NotContains(t, script, "masquerade")
	// Every accept counts, so "was this path used" is a question the kernel answers.
	for _, line := range strings.Split(strings.TrimSpace(script), "\n") {
		if strings.HasSuffix(line, "accept") {
			assert.Contains(t, line, "counter")
		}
	}
}

// TestAnExposureIsNotEgress is the invariant the two chains exist for: the exposure half is
// applied on its own, so the egress half is the one the sandbox was attached with — byte
// for byte — whether a port is exposed or not.
func TestAnExposureIsNotEgress(t *testing.T) {
	gateway := netip.MustParseAddr("10.63.0.1")
	guest := netip.MustParseAddr("10.63.0.2")
	destination := Destination{Host: "203.0.113.10", Port: 443, Addresses: []netip.Addr{netip.MustParseAddr("203.0.113.10")}}

	attached := policyScript(gateway, []Destination{destination})

	// The table an attach writes carries no exposure at all: the chains are there and
	// empty, so exposing a port changes nothing an attach wrote.
	assert.Contains(t, attached, "chain "+previewInChain+" {")
	assert.Contains(t, attached, "chain "+previewOutChain+" {")
	assert.Contains(t, attached, "\t\tjump "+previewInChain+"\n")
	assert.Contains(t, attached, "\t\tjump "+previewOutChain+"\n")
	assert.NotContains(t, exposureScript(guest, []uint16{8080}), "ip daddr 203.0.113.10",
		"an exposure names no destination the allowlist was about")

	// And the egress table is a function of the allowlist alone: the script that opens a
	// port and the script that does not are the same script.
	assert.Equal(t, attached, policyScript(gateway, []Destination{destination}))
	assert.NotContains(t, attached, "8080")

	// The forward chain still accepts nothing: an exposure is a path into one namespace,
	// not between namespaces.
	forward := attached[strings.Index(attached, "chain forward"):strings.Index(attached, "chain output")]
	assert.NotContains(t, forward, "accept")
	assert.Equal(t, 3, strings.Count(attached, "policy drop"), "every hook still denies by default")
}

// TestNoExposedPortsEmptiesBothChains: the empty list is what un-exposing the last port
// applies, and what a sandbox that was never exposed carries. It has to leave the chains
// empty rather than leave the previous ports' rules in place.
func TestNoExposedPortsEmptiesBothChains(t *testing.T) {
	guest := netip.MustParseAddr("10.63.0.2")

	empty := exposureScript(guest, nil)
	assert.Equal(t, empty, exposureScript(guest, []uint16{}), "a nil list and an empty one are the same change")
	assert.NotContains(t, empty, "add rule", "an empty exposure adds no rule")
	assert.Equal(t, 2, strings.Count(empty, "flush chain inet sandbox preview_"), "both chains are emptied first")

	// The deny-all attach script is unchanged: a sandbox with no exposure is the same
	// sandbox, not one whose table happens to be empty of accepts.
	assert.Equal(t, denyAllScript, policyScript(netip.MustParseAddr("10.63.0.1"), nil))
}

// TestExposedPortsAreASetInAStableOrder: the change is a function of the set of exposed
// ports, not of the order they were exposed in or of how many times one was named, so two
// callers that exposed the same ports apply the same rules.
func TestExposedPortsAreASetInAStableOrder(t *testing.T) {
	guest := netip.MustParseAddr("10.63.0.2")

	first := exposureScript(guest, []uint16{9090, 8080, 8080})
	second := exposureScript(guest, []uint16{8080, 9090})
	assert.Equal(t, first, second, "the same set twice is the same change")
	assert.Equal(t, 1, strings.Count(first, "dport 8080"), "a port exposed twice is one rule")
	assert.Equal(t, 1, strings.Count(first, "sport 8080"))
	assert.Equal(t, 1, strings.Count(first, "dport 9090"))
}

// SetInbound is the operation an exposure is made of, and a unit test can hold it to
// account without a namespace: the commands it runs are the manager's own seam, and the
// script it applies is a file it stages first.

// stagedScript captures the ruleset SetInbound hands to nft.
type stagedScript struct{ content string }

// recorderForSetInbound returns a manager whose nft reads the staged file into the
// receiver, so a test can assert exactly what was applied.
func recorderForSetInbound(t *testing.T, staged *stagedScript, err error) (*Manager, *execRecorder) {
	t.Helper()
	recorder := &execRecorder{reply: func(argv []string) ([]byte, error) {
		for i, arg := range argv {
			if arg == "-f" && i+1 < len(argv) {
				if data, readErr := os.ReadFile(argv[i+1]); readErr == nil {
					staged.content = string(data)
				}
			}
		}
		return nil, err
	}}
	return newTestManager(t, recorder), recorder
}

// TestSetInboundAppliesExactlyTheExposureChains: the rules that open a guest port are
// applied inside the sandbox's namespace, from the script this package builds, and with
// nothing else in it.
func TestSetInboundAppliesExactlyTheExposureChains(t *testing.T) {
	staged := &stagedScript{}
	manager, recorder := recorderForSetInbound(t, staged, nil)

	require.NoError(t, manager.SetInbound(context.Background(), "/var/run/netns/sbx-p1", "10.63.0.2", []uint16{8080}))

	assert.Equal(t, exposureScript(netip.MustParseAddr("10.63.0.2"), []uint16{8080}), staged.content,
		"the applied ruleset is the one this package builds, byte for byte")
	assert.NotEmpty(t, recorder.ran("/usr/bin/ip netns exec sbx-p1", "/usr/sbin/nft -f"),
		"the script is applied inside the sandbox's own namespace")

	// An empty list is how the last port is taken away: the chains are emptied and no
	// rule is added back.
	require.NoError(t, manager.SetInbound(context.Background(), "/var/run/netns/sbx-p1", "10.63.0.2", nil))
	assert.NotContains(t, staged.content, "add rule")
	assert.Equal(t, exposureScript(netip.MustParseAddr("10.63.0.2"), nil), staged.content)
}

// TestSetInboundRefusesWhatItCannotApply: an address that is not one, a path that cannot
// name a namespace, and an nft that refuses are all reported rather than applied to
// something else.
func TestSetInboundRefusesWhatItCannotApply(t *testing.T) {
	staged := &stagedScript{}
	manager, _ := recorderForSetInbound(t, staged, nil)
	ctx := context.Background()

	err := manager.SetInbound(ctx, "/var/run/netns/sbx-p1", "ten-sixty-three", []uint16{8080})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not an address")

	err = manager.SetInbound(ctx, "/var/run/netns/sbx-p1", "::1", []uint16{8080})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an IPv4 address")

	err = manager.SetInbound(ctx, "/", "10.63.0.2", []uint16{8080})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot name a network namespace")

	refusing, _ := recorderForSetInbound(t, staged, errors.New("nft: No such file or directory"))
	err = refusing.SetInbound(ctx, "/var/run/netns/sbx-p1", "10.63.0.2", []uint16{8080})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sbx-p1", "the refusal names the namespace it was applying to")
}

// TestDialInNamespaceIsALiteralDial: the relay's dial takes an ip:port and refuses a name,
// because a name here would be resolved inside a namespace whose resolver is the sandbox's
// own view, which is not what the caller asked for.
func TestDialInNamespaceIsALiteralDial(t *testing.T) {
	_, err := DialInNamespace(context.Background(), "/var/run/netns/sbx-p1", "guest.example:8080")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "literal ip:port")

	// A namespace that is not there is reported as that, and no socket is created.
	_, err = DialInNamespace(context.Background(), "/var/run/netns/sbx-absent", "10.63.0.2:8080")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sbx-absent")
}
