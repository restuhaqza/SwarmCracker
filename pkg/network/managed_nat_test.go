//go:build linux

package network

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManagedMasqueradeSpec_ExcludesBridgedTraffic pins the guard that keeps a
// masquerade rule from touching frames that are only being bridged between two
// guests. Without it, a rule left behind by a torn-down network that reused this
// subnet rewrote the source of guest-to-guest frames to the bridge address and
// destroyed the return path (#70).
func TestManagedMasqueradeSpec_ExcludesBridgedTraffic(t *testing.T) {
	spec := managedMasqueradeSpec("10.10.0.0/24", "br-dnsnet3")

	assert.Equal(t, "POSTROUTING", spec[0])
	assert.Equal(t,
		"POSTROUTING -s 10.10.0.0/24 ! -o br-dnsnet3 -m physdev ! --physdev-is-bridged -j MASQUERADE",
		strings.Join(spec, " "))
}

// TestManagedMasqueradeBridge_ParsesOnlyManagedRules checks the discriminator the
// sweep relies on: only the managed shape carries `! -o`, so the default network's
// rule and unrelated masquerades are never mistaken for one.
func TestManagedMasqueradeBridge_ParsesOnlyManagedRules(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"managed rule", "-A POSTROUTING -s 10.10.0.0/24 ! -o br-backend -j MASQUERADE", "br-backend"},
		{"managed rule with the guard", "-A POSTROUTING -s 10.10.0.0/24 ! -o br-backend -m physdev ! --physdev-is-bridged -j MASQUERADE", "br-backend"},
		{"default network rule", "-A POSTROUTING -s 192.168.127.0/24 -j MASQUERADE", ""},
		{"masquerade on a real interface", "-A POSTROUTING -s 10.0.0.0/8 -o eth0 -j MASQUERADE", ""},
		{"not masquerade", "-A POSTROUTING -s 10.10.0.0/24 ! -o br-backend -j ACCEPT", ""},
		{"not the postrouting chain", "-A PREROUTING -s 10.10.0.0/24 ! -o br-backend -j MASQUERADE", ""},
		{"policy line", "-P POSTROUTING ACCEPT", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, managedMasqueradeBridge(strings.Fields(tt.line)))
		})
	}
}

// TestSetupManagedNAT_InstallsGuardedRule asserts the installed rule carries the
// physdev guard, not just the subnet and the bridge.
func TestSetupManagedNAT_InstallsGuardedRule(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -t nat -C POSTROUTING", true) // the rule is absent
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	nm.setupManagedNAT("br-backend", "10.10.0.0/24")

	var added string
	for _, c := range state.calls {
		if strings.HasPrefix(c, "iptables -t nat -A POSTROUTING") {
			added = c
		}
	}
	require.NotEmpty(t, added, "a MASQUERADE rule should be added when missing; calls=%v", state.calls)
	assert.Contains(t, added, "-s 10.10.0.0/24")
	assert.Contains(t, added, "! -o br-backend")
	assert.Contains(t, added, "-m physdev ! --physdev-is-bridged",
		"MASQUERADE must never apply to bridged traffic")
}

// TestTeardownManagedNetwork_RemovesNATRule covers the leak behind #70: a
// torn-down network used to leave its subnet-keyed masquerade rule behind, so a
// later network given the same subnet inherited it and had its guest-to-guest
// frames masqueraded.
func TestTeardownManagedNetwork_RemovesNATRule(t *testing.T) {
	calls := stubExec(t)
	nm := newTestNetworkManager(t)
	mn := &managedNetwork{id: "n1", name: "backend", bridge: "br-backend", subnet: "10.10.0.0/24"}
	nm.managedNetworks["n1"] = mn

	nm.teardownManagedNetwork(mn)

	want := "iptables -t nat -D POSTROUTING -s 10.10.0.0/24 ! -o br-backend " +
		"-m physdev ! --physdev-is-bridged -j MASQUERADE"
	assert.True(t, hasCall(*calls, want),
		"the network's masquerade rule must go with its bridge; calls=%v", *calls)
}

// TestSweepStaleManagedNAT removes rules whose bridge no longer exists and leaves
// everything else alone: the default network's rule, a rule for a bridge that is
// still there, and rules that are not managed masquerades.
func TestSweepStaleManagedNAT(t *testing.T) {
	state := newMockState()
	state.setOutput("iptables -t nat -S POSTROUTING", strings.Join([]string{
		"-P POSTROUTING ACCEPT",
		"-A POSTROUTING -s 192.168.127.0/24 -j MASQUERADE",
		"-A POSTROUTING -s 10.10.0.0/24 ! -o br-gone -j MASQUERADE",
		"-A POSTROUTING -s 10.20.0.0/24 ! -o br-live -m physdev ! --physdev-is-bridged -j MASQUERADE",
		"-A POSTROUTING -s 10.30.0.0/24 ! -o br-also-gone -j ACCEPT",
		"",
	}, "\n"))
	state.setFail("ip link show br-gone", true)
	state.setFail("ip link show br-also-gone", true)
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	nm.sweepStaleManagedNAT()

	var deleted []string
	for _, c := range state.calls {
		if strings.HasPrefix(c, "iptables -t nat -D POSTROUTING") {
			deleted = append(deleted, c)
		}
	}
	require.Len(t, deleted, 1, "only the stale managed rule may be deleted; calls=%v", state.calls)
	assert.Contains(t, deleted[0], "-s 10.10.0.0/24")
	assert.Contains(t, deleted[0], "! -o br-gone")
}
