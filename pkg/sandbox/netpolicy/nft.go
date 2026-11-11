package netpolicy

import (
	"context"
	"fmt"
	"os"
)

// The policy half of a sandbox's network: one nftables table, inside the sandbox's
// own namespace, that denies everything.
//
// # Why the table lives in the namespace
//
// A policy that lives in the sandbox's namespace is state of that namespace: it
// cannot outlive it, it cannot be applied to the wrong sandbox, and `ip netns
// delete` removes it as one step with the devices it filters. The alternative — one
// node-wide table with a chain per sandbox — needs a rule that selects the
// sandbox's traffic by interface or by address, needs a removal step that a crash
// between the namespace's death and the rule's can leave behind, and is one typo
// away from filtering the node's own service traffic (pkg/network's bridge path).
//
// # What is denied
//
// Everything. The input, forward and output chains are all default-drop, and the
// only packets accepted are those that never leave the namespace (its own
// loopback). A guest's traffic arrives on tap0 and dies there: it is not routed
// out (there is no route out of the namespace), it is not answered (the input
// chain drops packets addressed to the gateway itself), and none of it is NATed
// anywhere. This is the `egress: none` mode of RFC D5; the allowlist mode adds
// explicit accepts for the node's DNS view and egress proxy to this same table, and
// the counters below are what show whether anything ever tried.

// policyScript is the ruleset every sandbox namespace carries, as nftables takes
// it from a file.
//
// It opens by destroying the table: `ip netns exec` re-applies this on every
// attach, and a table left by a half-finished attach must not survive as a second
// set of rules beside the new one. `destroy` — unlike `delete` — does not fail when
// the table is absent, which is what makes the whole script idempotent, and it is
// why an attach is safe to retry.
//
// Each chain ends in an explicit `counter drop` beside the chain's own `policy
// drop`, so a sandbox that tried to send something leaves a count behind that
// `nft list ruleset` shows. A policy-drop chain's own drops are not counted by
// anything, and "nothing was logged" and "nothing was attempted" would then read
// the same to the operator asking why a sandbox is silent.
func policyScript() string {
	return `destroy table inet ` + policyTable + `
table inet ` + policyTable + ` {
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
}

// applyPolicy installs the ruleset inside the sandbox's namespace.
//
// The script goes through a file rather than through nft's command line, because a
// table (and a chain with a policy) is not something nft's one-command-per-PUT
// form expresses; the file is the node's own, in the node's own temporary
// directory, and nft reads it from inside the namespace after `ip netns exec` has
// entered it — the namespace changes where the packets go, not which files exist.
func (m *Manager) applyPolicy(ctx context.Context, name string) error {
	file, err := os.CreateTemp("", "netpolicy-*.nft")
	if err != nil {
		return fmt.Errorf("netpolicy: stage the policy for %s: %w", name, err)
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()

	if _, err := file.WriteString(policyScript()); err != nil {
		_ = file.Close()
		return fmt.Errorf("netpolicy: stage the policy for %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("netpolicy: stage the policy for %s: %w", name, err)
	}

	if _, err := m.exec(ctx, m.ip, "netns", "exec", name, m.nft, "-f", path); err != nil {
		return fmt.Errorf("netpolicy: apply the default-deny policy in %s: %w", name, err)
	}
	return nil
}

// PolicyTable is the nftables table this package installs in every sandbox
// namespace: one table, three default-deny chains, no rule anywhere. It is
// exported so a reader — a test, or an operator's tool — can ask the kernel for
// the table a sandbox carries by name instead of by parsing a script.
func PolicyTable() string { return policyTable }
