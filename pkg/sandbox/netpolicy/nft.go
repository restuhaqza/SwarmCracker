package netpolicy

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
)

// The policy half of a sandbox's network: one nftables table, inside the sandbox's
// own namespace, that denies everything and accepts what the allowlist names.
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
// # What is denied, and what an allowlist opens
//
// Everything is denied. The input, forward and output chains are all default-drop,
// and the only packets accepted with no allowlist are those that never leave the
// namespace (its own loopback): a guest's traffic arrives on tap0 and dies there —
// it is not routed out (there is no route out of the namespace), it is not answered
// (the input chain drops packets addressed to the gateway itself), and none of it is
// NATed anywhere. That is the `egress: none` mode of RFC D5.
//
// An allowlist adds explicit, counted accepts to this same table for exactly the
// paths it names — the sandbox's resolver on its own gateway, and each destination
// address on the port that was allowed — and nothing else moves: the chains still
// end in `counter drop`, the forward chain still accepts nothing, and ICMP, other
// ports and other addresses stay as unreachable as they were. The counters are what
// show, per rule, whether a path ever carried real traffic.

// policyScript is the ruleset every sandbox namespace carries, as nftables takes it
// from a file.
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
//
// # What the allowlist adds
//
// Explicit accepts, each with a counter of its own, for exactly the two paths an
// allowlisted sandbox has: its resolver on its own gateway, and the destinations'
// addresses on the port that was allowed — in the input chain for what the guest
// sends, and in the output chain for what the proxy's sockets answer. Both
// directions are matched on the same fixed tuple (address, port), so a packet is
// accepted only when it belongs to a connection the allowlist describes, and the
// counters say which rules carried real traffic and which only ever saw drops.
//
// Nothing else changes: no other address, no other port, no ICMP and no forwarding
// becomes reachable, because the chains still end in `counter drop` and still deny
// by policy. An empty allowlist produces this script unchanged — not a script with
// zero accepts, but the same bytes as the version before the allowlist existed —
// so `egress: none` behaves exactly as it did.
//
// # What an exposure adds
//
// The other direction: a guest port the node reaches, through a preview proxy it runs on
// itself. A preview is inbound — the node's socket, created inside the sandbox's
// namespace, connects to the guest's own address and port, and the guest answers it — so
// its two accepts are the mirror of an egress destination's: one for the node's connection
// out to the guest and one for the guest's replies back, both matched on the guest's
// address and on the one exposed port, and both pinned to the TAP the guest is on.
//
// They are not written by this function. An exposure is a change to a *running* sandbox's
// policy, made by SetInbound, and rewriting the whole table then would mean knowing the
// egress plan — which is a lease this process holds, not a fact about the sandbox: after a
// daemon restart it is gone while the sandbox is still running. So the exposure half lives
// in two chains of its own, preview_in and preview_out, which the base chains jump to
// before they drop, and SetInbound empties and refills exactly those two. The egress rules
// below are then never touched by an exposure: no destination becomes reachable because a
// port was exposed, no egress rule is rewritten by exposing one, and a sandbox with no
// exposure carries both chains empty.
func policyScript(gateway netip.Addr, plan []Destination) string {
	var script strings.Builder

	script.WriteString("destroy table inet " + policyTable + "\n")
	script.WriteString("table inet " + policyTable + " {\n")
	// The preview chains are declared before the base chains so the jumps below name
	// something that already exists, and they are empty until SetInbound fills them.
	script.WriteString("\tchain " + previewInChain + " {\n\t}\n\n")
	script.WriteString("\tchain " + previewOutChain + " {\n\t}\n\n")
	script.WriteString("\tchain input {\n")
	script.WriteString("\t\ttype filter hook input priority filter; policy drop;\n")
	script.WriteString("\t\tiifname \"lo\" accept\n")
	for _, line := range policyAccepts(gateway, plan) {
		script.WriteString("\t\t" + line + "\n")
	}
	script.WriteString("\t\tjump " + previewInChain + "\n")
	script.WriteString("\t\tcounter drop\n")
	script.WriteString("\t}\n\n")

	script.WriteString("\tchain forward {\n")
	script.WriteString("\t\ttype filter hook forward priority filter; policy drop;\n")
	script.WriteString("\t\tcounter drop\n")
	script.WriteString("\t}\n\n")

	script.WriteString("\tchain output {\n")
	script.WriteString("\t\ttype filter hook output priority filter; policy drop;\n")
	script.WriteString("\t\toifname \"lo\" accept\n")
	for _, line := range policyAnswers(gateway, plan) {
		script.WriteString("\t\t" + line + "\n")
	}
	script.WriteString("\t\tjump " + previewOutChain + "\n")
	script.WriteString("\t\tcounter drop\n")
	script.WriteString("\t}\n")

	script.WriteString("}\n")
	return script.String()
}

// exposureScript is what SetInbound installs: the two preview chains emptied and refilled
// with the accepts of the ports that are exposed, and nothing else. Emptying first is what
// makes it a function of the ports it is given rather than of what was there before — an
// exposure that ended leaves no rule behind, and re-applying the same ports is the same
// table.
//
// It is applied with `nft -f` inside the sandbox's namespace. Both chains exist as soon as
// the sandbox is attached (policyScript declares them), so a failure here is a namespace
// without a table rather than a chain that has to be made first, and it is reported.
func exposureScript(guest netip.Addr, exposed []uint16) string {
	var script strings.Builder
	script.WriteString("flush chain inet " + policyTable + " " + previewInChain + "\n")
	script.WriteString("flush chain inet " + policyTable + " " + previewOutChain + "\n")
	for _, rule := range exposureAnswers(guest, exposed) {
		script.WriteString("add rule inet " + policyTable + " " + previewInChain + " " + rule + "\n")
	}
	for _, rule := range exposureAccepts(guest, exposed) {
		script.WriteString("add rule inet " + policyTable + " " + previewOutChain + " " + rule + "\n")
	}
	return script.String()
}

// policyAccepts is what the guest may send: its resolver's queries to the gateway,
// and a connection to each allowed destination on the port that was allowed. Each
// rule counts, so `nft list ruleset` says whether the path was used.
func policyAccepts(gateway netip.Addr, plan []Destination) []string {
	var lines []string
	if hasNamedDestination(plan) {
		lines = append(lines,
			fmt.Sprintf("ip daddr %s udp dport %d counter accept", gateway, viewPort),
			fmt.Sprintf("ip daddr %s tcp dport %d counter accept", gateway, viewPort),
		)
	}
	for _, rule := range destinationRules(plan) {
		lines = append(lines, fmt.Sprintf("ip daddr %s tcp dport %d counter accept", rule.address, rule.port))
	}
	return lines
}

// policyAnswers is what the sandbox's own sockets may send back to the guest: the
// view's answers from the gateway, and each relay's answers from the destination
// address. The guest's source port is ephemeral and is not part of the match; the
// tuple the guest chose (address, port) is.
func policyAnswers(gateway netip.Addr, plan []Destination) []string {
	var lines []string
	if hasNamedDestination(plan) {
		lines = append(lines,
			fmt.Sprintf("ip saddr %s udp sport %d counter accept", gateway, viewPort),
			fmt.Sprintf("ip saddr %s tcp sport %d counter accept", gateway, viewPort),
		)
	}
	for _, rule := range destinationRules(plan) {
		lines = append(lines, fmt.Sprintf("ip saddr %s tcp sport %d counter accept", rule.address, rule.port))
	}
	return lines
}

// exposureAccepts is what the node's own socket may send to reach an exposed guest
// port: the connection into the guest, on the port that was exposed, out the TAP the
// guest is on. The port beside the guest's address is the whole match, so exposing one
// port opens exactly one port.
func exposureAccepts(guest netip.Addr, exposed []uint16) []string {
	ports := exposedPorts(exposed)
	lines := make([]string, 0, len(ports))
	for _, port := range ports {
		lines = append(lines, fmt.Sprintf("oifname %q ip daddr %s tcp dport %d counter accept", TapName, guest, port))
	}
	return lines
}

// exposureAnswers is what the guest may send back: its replies to the node's socket,
// from the port that was exposed, in on the TAP. The node's source port is ephemeral
// and is not part of the match; the port the guest answered from is.
func exposureAnswers(guest netip.Addr, exposed []uint16) []string {
	ports := exposedPorts(exposed)
	lines := make([]string, 0, len(ports))
	for _, port := range ports {
		lines = append(lines, fmt.Sprintf("iifname %q ip saddr %s tcp sport %d counter accept", TapName, guest, port))
	}
	return lines
}

// exposedPorts is the ports in a set, once, ascending: a ruleset is a function of the
// exposed ports and not of the order a caller happened to expose them in, which is
// what makes it readable in a diff and comparable in a test.
func exposedPorts(exposed []uint16) []uint16 {
	if len(exposed) == 0 {
		return nil
	}
	seen := make(map[uint16]bool, len(exposed))
	ports := make([]uint16, 0, len(exposed))
	for _, port := range exposed {
		if port == 0 || seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports
}

// allowRule is one (address, port) pair the namespace accepts: the unit the input
// and the output chain both match, so the two directions of one connection are one
// rule each and cannot drift apart.
type allowRule struct {
	address netip.Addr
	port    uint16
}

// destinationRules is every pair in a plan, once, in a stable order: a ruleset is a
// function of the allowlist and not of the order the caller happened to write it
// in, which is what makes it readable in a diff and comparable in a test.
func destinationRules(plan []Destination) []allowRule {
	seen := make(map[allowRule]bool)
	var rules []allowRule
	for _, d := range plan {
		for _, addr := range d.Addresses {
			rule := allowRule{address: addr, port: d.Port}
			if seen[rule] {
				continue
			}
			seen[rule] = true
			rules = append(rules, rule)
		}
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].address != rules[j].address {
			return rules[i].address.Less(rules[j].address)
		}
		return rules[i].port < rules[j].port
	})
	return rules
}

// applyPolicy installs the ruleset inside the sandbox's namespace.
//
// The script goes through a file rather than through nft's command line, because a
// table (and a chain with a policy) is not something nft's one-command-per-PUT
// form expresses; the file is the node's own, in the node's own temporary
// directory, and nft reads it from inside the namespace after `ip netns exec` has
// entered it — the namespace changes where the packets go, not which files exist.
func (m *Manager) applyPolicy(ctx context.Context, name string, n Network, plan []Destination) error {
	file, err := os.CreateTemp("", "netpolicy-*.nft")
	if err != nil {
		return fmt.Errorf("netpolicy: stage the policy for %s: %w", name, err)
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()

	if _, err := file.WriteString(policyScript(n.Gateway, plan)); err != nil {
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
// namespace: one table whose three chains deny by policy and drop with a counter,
// plus the accepts an allowlist names. It is exported so a reader — a test, or an
// operator's tool — can ask the kernel for the table a sandbox carries by name
// instead of by parsing a script.
func PolicyTable() string { return policyTable }
