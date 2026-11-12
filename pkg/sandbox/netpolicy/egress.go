package netpolicy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
)

// The egress half of a sandbox's network: the destinations the caller allows, the
// addresses the node resolved them to, and the meaning of "an address inside the
// sandbox's namespace".
//
// # How a sandbox reaches an allowed destination
//
// The guest has one next hop — the gateway its own /30 allocated to the namespace
// — and the namespace has no route out of itself. So an allowed destination is not
// reached by forwarding the guest's packets anywhere: the namespace *owns* the
// destination's address on its loopback, and a listener the node holds there
// accepts the guest's connection and dials the real destination from the node's
// own networking. From the guest's side nothing is special: it resolves a name,
// connects to the address it got on the port it wanted, and speaks its protocol.
// From the node's side the destination is an allowlist entry that the sandbox
// namespace answers for.
//
// That keeps every piece of egress state inside the namespace it belongs to: the
// address is a /32 the namespace carries, the rules that accept the connection are
// the namespace's own table, and the listener is a socket of that namespace. There
// is no NAT, no route and no rule on the node, so a sandbox cannot leak egress
// into another sandbox's namespace and cannot outlive its own.

const (
	// maxAddressesPerName bounds what one allowlist name becomes. A name is
	// resolved once, at attach, so the addresses the guest is told and the
	// addresses the node accepts are one set; a name that resolves to more
	// addresses than this (a large CDN name) is installed with the first ones,
	// which is the set the guest's resolver is answered with too.
	maxAddressesPerName = 4
)

// Destination is one allowed destination as the node installed it: what the
// caller named, the TCP port on it, and the IPv4 addresses the node resolved for
// it. An empty list of destinations is `egress: none`: the sandbox reaches
// nothing, which is what every sandbox had before this existed.
type Destination struct {
	// Host is the entry as the caller wrote it: a DNS name or an IPv4 literal.
	Host string
	// Port is the TCP port the guest may use on that host, and the port the
	// namespace accepts. A destination is one port: allowing a host does not
	// allow its other ports.
	Port uint16
	// Addresses are the IPv4 addresses the node resolved for Host. The guest's
	// connections to any of them on Port are what the proxy accepts, and for a
	// name they are the answers the sandbox's DNS view gives.
	Addresses []netip.Addr
}

// String is the entry in the form the caller gave it, "host:port".
func (d Destination) String() string {
	return net.JoinHostPort(d.Host, fmt.Sprint(d.Port))
}

// named reports whether the destination is addressed by a name, which is what
// makes the sandbox's resolver answer for it.
func (d Destination) named() bool {
	_, err := netip.ParseAddr(d.Host)
	return err != nil
}

// planEgress turns the caller's allowlist into the destinations a sandbox can
// install, before anything on the node is touched: every entry is parsed, every
// name is resolved by the node once, and every address is refused if it cannot be
// an egress destination for this sandbox.
//
// Resolving here rather than per connection is what makes the policy and the DNS
// view one answer: the addresses the guest is told and the addresses the namespace
// accepts are the same list, so a name can never resolve to an address the sandbox
// is not allowed to use.
func (m *Manager) planEgress(ctx context.Context, n Network, allow []string) ([]Destination, error) {
	if len(allow) == 0 {
		return nil, nil
	}

	plan := make([]Destination, 0, len(allow))
	seen := make(map[string]bool, len(allow))
	for _, entry := range allow {
		d, err := parseDestination(entry)
		if err != nil {
			return nil, err
		}
		key := d.String()
		if seen[key] {
			return nil, fmt.Errorf("netpolicy: sandbox %q allows %s twice: an allowlist is a set of destinations", n.ID, key)
		}
		seen[key] = true

		d.Addresses, err = m.addressesOf(ctx, n, d)
		if err != nil {
			return nil, err
		}
		plan = append(plan, d)
	}

	sort.Slice(plan, func(i, j int) bool { return plan[i].String() < plan[j].String() })
	return plan, nil
}

// addressesOf is the addresses one destination is installed with: the literal
// itself, or what the node's resolver answers for the name.
func (m *Manager) addressesOf(ctx context.Context, n Network, d Destination) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(d.Host); err == nil {
		if err := m.destinationProblem(n, d, literal); err != nil {
			return nil, err
		}
		return []netip.Addr{literal.Unmap()}, nil
	}

	resolved, err := m.resolve(ctx, d.Host)
	if err != nil {
		return nil, fmt.Errorf("netpolicy: sandbox %q allows %s, which this node cannot resolve: %w", n.ID, d.Host, err)
	}
	addresses := make([]netip.Addr, 0, len(resolved))
	for _, addr := range resolved {
		if !addr.Is4() && !addr.Is4In6() {
			continue
		}
		addr = addr.Unmap()
		if err := m.destinationProblem(n, d, addr); err != nil {
			return nil, err
		}
		if len(addresses) == maxAddressesPerName {
			break
		}
		addresses = appendNew(addresses, addr)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("netpolicy: sandbox %q allows %s, which resolves to no IPv4 address this node can reach", n.ID, d.Host)
	}
	return addresses, nil
}

// destinationProblem refuses an address that cannot be an egress destination. Each
// refusal is a destination that would either do nothing (the guest's own loopback
// and its own link answer inside the guest, so a rule for them is never reached)
// or be an attempt to use the node's egress to reach the sandbox network itself.
func (m *Manager) destinationProblem(n Network, d Destination, addr netip.Addr) error {
	switch {
	case addr.IsUnspecified():
		return fmt.Errorf("netpolicy: sandbox %q allows %s, which resolves to the unspecified address %s", n.ID, d.String(), addr)
	case addr.IsLoopback():
		return fmt.Errorf("netpolicy: sandbox %q allows %s, which resolves to the loopback address %s: a guest's own loopback answers first", n.ID, d.String(), addr)
	case addr.IsMulticast():
		return fmt.Errorf("netpolicy: sandbox %q allows %s, which resolves to the multicast address %s", n.ID, d.String(), addr)
	case addr.Is4() && addr == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
		return fmt.Errorf("netpolicy: sandbox %q allows %s, which resolves to the broadcast address", n.ID, d.String())
	case n.CIDR.IsValid() && n.CIDR.Contains(addr):
		return fmt.Errorf("netpolicy: sandbox %q allows %s, which is inside its own link %s", n.ID, d.String(), n.CIDR)
	case m.pool.Contains(addr):
		return fmt.Errorf("netpolicy: sandbox %q allows %s, which is inside the sandbox pool %s: a sandbox does not reach another sandbox's network", n.ID, d.String(), m.pool)
	}
	return nil
}

// parseDestination reads one allowlist entry, "host:port". The host is a DNS name
// or an IPv4 literal and nothing else: no scheme, no path, no IPv6 (this slice's
// egress is TCP over IPv4, which is what the node's own networking is built on).
func parseDestination(entry string) (Destination, error) {
	text := strings.TrimSpace(entry)
	if text == "" {
		return Destination{}, fmt.Errorf("netpolicy: an egress allowlist entry is empty")
	}

	host, port, err := net.SplitHostPort(text)
	if err != nil {
		return Destination{}, fmt.Errorf("netpolicy: egress allowlist entry %q is not host:port: %w", entry, err)
	}

	parsed, err := parsePort(port)
	if err != nil {
		return Destination{}, fmt.Errorf("netpolicy: egress allowlist entry %q: %w", entry, err)
	}

	if literal, err := netip.ParseAddr(host); err == nil {
		if !literal.Is4() {
			return Destination{}, fmt.Errorf("netpolicy: egress allowlist entry %q is IPv6: this slice allows TCP over IPv4", entry)
		}
		return Destination{Host: literal.Unmap().String(), Port: parsed}, nil
	}

	if err := nameProblem(host); err != nil {
		return Destination{}, fmt.Errorf("netpolicy: egress allowlist entry %q: %w", entry, err)
	}
	return Destination{Host: strings.ToLower(host), Port: parsed}, nil
}

// parsePort is a port number as an allowlist entry carries it. Port 0 is refused:
// it names no service, and a rule for it would match nothing.
func parsePort(text string) (uint16, error) {
	var port int
	if _, err := fmt.Sscanf(text, "%d", &port); err != nil || fmt.Sprint(port) != text {
		return 0, fmt.Errorf("the port %q is not a number", text)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("the port %d is not a TCP port", port)
	}
	return uint16(port), nil
}

// nameProblem refuses a host that cannot be a name the node resolves. It is
// deliberately a shape check and not a resolver call: whether the name exists is
// the resolver's answer, and this is the answer to "did the caller mean a name".
func nameProblem(host string) error {
	if host == "" {
		return fmt.Errorf("no host")
	}
	if len(host) > 253 {
		return fmt.Errorf("the name is longer than a DNS name")
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("the name has an empty or over-long label")
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			default:
				return fmt.Errorf("the name %q is not a DNS name", host)
			}
		}
	}
	return nil
}

// denyAll is what a sandbox with no allowlist is: the destinations it may reach,
// as the empty set. It exists so a reader of the policy sees the mode named rather
// than an empty slice whose meaning has to be remembered.
func denyAll(plan []Destination) bool { return len(plan) == 0 }
