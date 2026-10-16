//go:build linux

package network

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMasqueradeSpec_Shapes pins the four rule shapes this package has to know
// about. Every install, delete and sweep path builds from here, so a rule can never
// be added in one shape and looked for in another; the physdev guard is what keeps a
// rule away from bridged traffic, and the comment is what marks a rule as ours.
func TestMasqueradeSpec_Shapes(t *testing.T) {
	tests := []struct {
		name          string
		subnet        string
		excludeBridge string
		comment       string
		guard         bool
		want          string
	}{
		{
			name:   "managed rule, as installed now",
			subnet: "10.10.0.0/24", excludeBridge: "br-backend", comment: managedNATComment, guard: true,
			want: "POSTROUTING -s 10.10.0.0/24 ! -o br-backend -m physdev ! --physdev-is-bridged " +
				"-m comment --comment " + managedNATComment + " -j MASQUERADE",
		},
		{
			name:   "managed rule, as an earlier release installed it",
			subnet: "10.10.0.0/24", excludeBridge: "br-backend",
			want: "POSTROUTING -s 10.10.0.0/24 ! -o br-backend -j MASQUERADE",
		},
		{
			name:   "default rule, guarded",
			subnet: "192.168.127.0/24", guard: true,
			want: "POSTROUTING -s 192.168.127.0/24 -m physdev ! --physdev-is-bridged -j MASQUERADE",
		},
		{
			name:   "default rule, as an earlier release installed it",
			subnet: "192.168.127.0/24",
			want:   "POSTROUTING -s 192.168.127.0/24 -j MASQUERADE",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want,
				strings.Join(masqueradeSpec(tt.subnet, tt.excludeBridge, tt.comment, tt.guard), " "))
		})
	}

	assert.Equal(t, masqueradeSpec("10.10.0.0/24", "br-backend", managedNATComment, true),
		managedMasqueradeSpec("10.10.0.0/24", "br-backend"))
	assert.Equal(t, masqueradeSpec("10.10.0.0/24", "br-backend", "", false),
		legacyManagedMasqueradeSpec("10.10.0.0/24", "br-backend"))
}

// TestManagedMasqueradeBridge_ParsesOnlyManagedRules checks the discriminator the
// sweep relies on: only a managed shape carries `! -o`, so the default network's
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

// TestLooksLikeManagedBridge keeps the sweep away from a co-located Docker network,
// whose bridge is `br-<12 hex>` and whose per-network NAT rule is otherwise the same
// shape as ours.
func TestLooksLikeManagedBridge(t *testing.T) {
	tests := map[string]bool{
		"br-backend":      true,
		"br-dnsnet3":      true,
		"br-stale":        true,
		"sc-01234567":     true,
		"br-0123456789ab": false, // Docker
		"br-deadbeef0000": false, // Docker
		"veth0":           false,
		"eth0":            false,
		"":                false,
	}
	for name, want := range tests {
		assert.Equal(t, want, looksLikeManagedBridge(name), "name=%q", name)
	}
}

// TestSetupManagedNAT_InstallsGuardedTaggedRule asserts the installed rule carries
// both the guard (so it never touches bridged traffic) and the comment (so the sweep
// can recognise it), and that an unguarded rule from an earlier release is replaced.
func TestSetupManagedNAT_InstallsGuardedTaggedRule(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -t nat -C POSTROUTING", true) // rule absent
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
	assert.Contains(t, added, "--comment "+managedNATComment, "the rule must be recognisable as ours")
	assert.True(t, hasCall(state.calls,
		"iptables -t nat -D POSTROUTING -s 10.10.0.0/24 ! -o br-backend -j MASQUERADE"),
		"the unguarded rule from an earlier release must be replaced; calls=%v", state.calls)
}

// TestTeardownManagedNetwork_RemovesBothRuleShapes covers the leak behind #70: a
// torn-down network used to leave its subnet-keyed masquerade rule behind, so a
// later network given the same subnet inherited it.
func TestTeardownManagedNetwork_RemovesBothRuleShapes(t *testing.T) {
	calls := stubExec(t)
	nm := newTestNetworkManager(t)
	mn := &managedNetwork{id: "n1", name: "backend", bridge: "br-backend", subnet: "10.10.0.0/24"}
	nm.managedNetworks["n1"] = mn

	nm.teardownManagedNetwork(mn)

	assert.True(t, hasCall(*calls, "iptables -t nat -D POSTROUTING -s 10.10.0.0/24 ! -o br-backend "+
		"-m physdev ! --physdev-is-bridged -m comment --comment "+managedNATComment+" -j MASQUERADE"),
		"the current rule must go with the network; calls=%v", *calls)
	assert.True(t, hasCall(*calls, "iptables -t nat -D POSTROUTING -s 10.10.0.0/24 ! -o br-backend -j MASQUERADE"),
		"so must an unguarded one from an earlier release; calls=%v", *calls)
}

// TestSweepStaleManagedNAT is the reconciliation matrix. It removes rules this
// package owns (tagged ones whatever they name, and untagged ones from an earlier
// release whose bridge is gone and whose name is ours), replaces the default
// network's unguarded rule, and leaves everything else alone.
func TestSweepStaleManagedNAT(t *testing.T) {
	state := newMockState()
	state.setOutput("iptables -t nat -S POSTROUTING", strings.Join([]string{
		"-P POSTROUTING ACCEPT",
		// Ours, tagged, and its bridge is gone: removed. iptables quotes a comment
		// containing ":" when printing, so the fixture does too.
		`-A POSTROUTING -s 10.10.0.0/24 ! -o br-gone -m physdev ! --physdev-is-bridged -m comment --comment "` + managedNATComment + `" -j MASQUERADE`,
		// Untagged, from an earlier release, bridge gone, our naming: removed.
		"-A POSTROUTING -s 10.20.0.0/24 ! -o br-also-gone -j MASQUERADE",
		// Untagged, from an earlier release, but its bridge is still there: kept.
		"-A POSTROUTING -s 10.30.0.0/24 ! -o br-live -j MASQUERADE",
		// Docker's network rule has the same shape but a br-<12 hex> device: kept.
		"-A POSTROUTING -s 172.18.0.0/16 ! -o br-0123456789ab -j MASQUERADE",
		// The default network's unguarded rule: replaced.
		"-A POSTROUTING -s 192.168.127.0/24 -j MASQUERADE",
		// The default network's guarded rule: kept.
		"-A POSTROUTING -s 192.168.127.0/24 -m physdev ! --physdev-is-bridged -j MASQUERADE",
		// Not ours: a masquerade on a real interface.
		"-A POSTROUTING -s 10.0.0.0/8 -o eth0 -j MASQUERADE",
		// Not ours: not a masquerade at all.
		"-A POSTROUTING -s 10.40.0.0/24 ! -o br-nope -j ACCEPT",
		"",
	}, "\n"))
	state.setFail("ip link show br-gone", true)
	state.setFail("ip link show br-also-gone", true)
	state.setFail("ip link show br-0123456789ab", true) // even a Docker bridge that is momentarily absent
	// The guarded default rule is not there yet, so installing it must add it.
	state.setFail("iptables -t nat -C POSTROUTING -s 192.168.127.0/24 -m physdev", true)
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	nm.config.Subnet = "192.168.127.0/24"
	nm.sweepStaleManagedNAT()

	var deleted []string
	for _, c := range state.calls {
		if strings.HasPrefix(c, "iptables -t nat -D POSTROUTING") {
			deleted = append(deleted, c)
		}
	}
	require.Len(t, deleted, 3, "exactly three rules may be deleted; calls=%v", state.calls)
	// The tagged rule, with its comment unquoted for -D.
	assert.Contains(t, deleted[0], "--comment "+managedNATComment)
	assert.NotContains(t, deleted[0], `"`+managedNATComment+`"`,
		"iptables compares a comment literally, so the quotes -S added must be removed")
	assert.Contains(t, deleted[1], "-s 10.20.0.0/24")
	assert.Contains(t, deleted[2], "-s 192.168.127.0/24")
	assert.NotContains(t, strings.Join(deleted, " "), "br-0123456789ab",
		"a co-located Docker network's rule must never be deleted")

	// The default rule is reinstalled in its guarded shape.
	guarded := false
	for _, c := range state.calls {
		if strings.HasPrefix(c, "iptables -t nat -A POSTROUTING -s 192.168.127.0/24") &&
			strings.Contains(c, "-m physdev ! --physdev-is-bridged") {
			guarded = true
		}
	}
	assert.True(t, guarded, "the default rule must come back guarded; calls=%v", state.calls)
}

// TestSetupNAT_GuardsTheDefaultRule covers the case the issue's auto-allocated
// reproduction hits: a user-defined network given the default subnet is masqueraded
// by the *default* rule unless that rule carries the guard too.
func TestSetupNAT_GuardsTheDefaultRule(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -t nat -C POSTROUTING", true)
	restore := setupMocksForTest(state)
	defer restore()

	nm := newManagedTestManager()
	nm.config.Subnet = "192.168.127.0/24"
	require.NoError(t, nm.setupNAT(t.Context()))

	var added string
	for _, c := range state.calls {
		if strings.HasPrefix(c, "iptables -t nat -A POSTROUTING") {
			added = c
		}
	}
	require.NotEmpty(t, added, "the default NAT rule should be installed; calls=%v", state.calls)
	assert.Contains(t, added, "-s 192.168.127.0/24")
	assert.Contains(t, added, "-m physdev ! --physdev-is-bridged",
		"the default rule must not apply to bridged traffic either")
	assert.True(t, hasCall(state.calls, "iptables -t nat -D POSTROUTING -s 192.168.127.0/24 -j MASQUERADE"),
		"an unguarded default rule from an earlier release must be replaced; calls=%v", state.calls)
}
