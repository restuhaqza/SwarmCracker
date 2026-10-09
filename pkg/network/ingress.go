package network

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

// ingressCommentPrefix namespaces the iptables comments used by the ingress
// routing mesh so they can be swept independently of per-task host publishing
// (which uses "swarmcracker:<task-id>").
const ingressCommentPrefix = "swarmcracker:ingress:"

// IngressPort is one published port on an ingress service.
type IngressPort struct {
	// Protocol is "tcp" or "udp".
	Protocol string
	// PublishedPort is the host port on every node.
	PublishedPort uint32
	// TargetPort is the port inside each replica.
	TargetPort uint32
}

// IngressRoute is the desired load-balanced state for a single service: the
// host ports it exposes and the replica IPs traffic may be distributed across.
// The replica IPs are guest addresses on the shared L2 overlay, so a rule set
// installed on any node can reach a replica running on any other node.
type IngressRoute struct {
	ServiceID string
	Ports     []IngressPort
	Backends  []string
}

// ProgramIngress reconciles the ingress load-balancing rules on this node with
// the desired set of routes. It is idempotent: when the computed rule set is
// unchanged since the last call it does nothing, so callers can invoke it on a
// timer without churning the rules.
//
// Traffic to a published host port is distributed round-robin across the
// service's healthy replicas using iptables' statistic nth matcher. A single
// backend is handled with a plain DNAT.
func (nm *NetworkManager) ProgramIngress(routes []IngressRoute) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	desired := nm.buildIngressRules(routes)
	key := strings.Join(desired, "\n")
	if nm.ingressProgrammed && key == nm.ingressRules {
		return nil
	}

	// Allow locally-originated loopback traffic to reach replicas so
	// `curl 127.0.0.1:<port>` works as it does with Docker.
	nm.enableRouteLocalnet()

	// Rebuild from scratch: sweep any existing ingress rules (including ones
	// left by a previous daemon run) then add the desired set.
	nm.removeCommentPrefixRules(ingressCommentPrefix)
	for _, rule := range desired {
		if err := nm.ensureRule(strings.Fields(rule)); err != nil {
			return fmt.Errorf("failed to program ingress rule %q: %w", rule, err)
		}
	}

	nm.ingressRules = key
	nm.ingressProgrammed = true
	return nil
}

// ClearIngress removes every ingress rule from this node.
func (nm *NetworkManager) ClearIngress() error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	nm.removeCommentPrefixRules(ingressCommentPrefix)
	nm.ingressRules = ""
	nm.ingressProgrammed = false
	return nil
}

// buildIngressRules renders the full, deterministic iptables rule set for the
// given routes. Routes, ports and backends are sorted so identical desired
// state always produces an identical rule set.
func (nm *NetworkManager) buildIngressRules(routes []IngressRoute) []string {
	sorted := make([]IngressRoute, 0, len(routes))
	for _, r := range routes {
		if r.ServiceID == "" || len(r.Ports) == 0 || len(r.Backends) == 0 {
			continue
		}
		cp := r
		cp.Backends = dedupeSortedStrings(r.Backends)
		if len(cp.Backends) == 0 {
			continue
		}
		sorted = append(sorted, cp)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ServiceID < sorted[j].ServiceID })

	var rules []string
	for _, r := range sorted {
		ports := append([]IngressPort(nil), r.Ports...)
		sort.Slice(ports, func(i, j int) bool {
			if ports[i].PublishedPort != ports[j].PublishedPort {
				return ports[i].PublishedPort < ports[j].PublishedPort
			}
			return ports[i].Protocol < ports[j].Protocol
		})

		comment := ingressCommentPrefix + r.ServiceID
		n := len(r.Backends)
		for _, p := range ports {
			pub := strconv.FormatUint(uint64(p.PublishedPort), 10)
			target := strconv.FormatUint(uint64(p.TargetPort), 10)

			for _, chain := range dnatChains {
				if n == 1 {
					rules = append(rules, strings.Join([]string{
						chain, "-p", p.Protocol, "--dport", pub,
						"-m", "comment", "--comment", comment,
						"-j", "DNAT", "--to-destination", r.Backends[0] + ":" + target,
					}, " "))
					continue
				}
				// Distribute only new connections: the matcher must never split
				// a single TCP flow across backends. The probability ladder
				// (1/n, 1/(n-1), ..., plain) guarantees every packet matches
				// exactly one rule, unlike a fixed nth counter that can be
				// left unmatched as rules re-evaluate.
				for i, backend := range r.Backends {
					base := []string{
						chain, "-p", p.Protocol, "--dport", pub,
						"-m", "conntrack", "--ctstate", "NEW",
					}
					if i < n-1 {
						base = append(base,
							"-m", "statistic", "--mode", "random",
							"--probability", strconv.FormatFloat(1.0/float64(n-i), 'f', 6, 64),
						)
					}
					base = append(base,
						"-m", "comment", "--comment", comment,
						"-j", "DNAT", "--to-destination", backend+":"+target,
					)
					rules = append(rules, strings.Join(base, " "))
				}
			}

			// Masquerade ingress-forwarded traffic so replica replies always
			// return through this node, for host-originated and external
			// clients alike.
			for _, backend := range r.Backends {
				rules = append(rules, strings.Join([]string{
					"POSTROUTING", "-d", backend + "/32",
					"-m", "comment", "--comment", comment,
					"-j", "MASQUERADE",
				}, " "))
			}
		}
	}
	return rules
}

// removeCommentPrefixRules deletes every nat-table rule whose --comment starts
// with prefix across the sweep chains.
func (nm *NetworkManager) removeCommentPrefixRules(prefix string) {
	for _, chain := range sweepChains {
		out, err := execCommand("iptables", "-t", "nat", "-S", chain).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 || fields[0] != "-A" || fields[1] != chain {
				continue
			}
			comment := ""
			for i := 0; i < len(fields)-1; i++ {
				if fields[i] == "--comment" {
					comment = unquote(fields[i+1])
					break
				}
			}
			if !strings.HasPrefix(comment, prefix) {
				continue
			}
			rest := append([]string(nil), fields[2:]...)
			for i := 0; i < len(rest)-1; i++ {
				if rest[i] == "--comment" {
					rest[i+1] = unquote(rest[i+1])
				}
			}
			delArgs := append([]string{"-t", "nat", "-D", chain}, rest...)
			if err := execCommand("iptables", delArgs...).Run(); err != nil {
				log.Debug().Err(err).Str("chain", chain).Msg("Failed to delete ingress rule")
			}
		}
	}
}

func dedupeSortedStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
