package netpolicy

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
)

// The address half of a sandbox's network: one /30 per sandbox, and the reason a
// node can hold more sandboxes than a shared /24 has addresses.
//
// The lease table here is a cache, not the state: the state is on the host — the
// namespaces that exist and the addresses and routes they carry — and every
// allocation is checked against it. A node that restarts therefore hands out the
// address its own namespaces already use to nobody, without a persisted lease file
// to reconcile (and therefore without a lease file to go stale).

// reserve gives one sandbox its /30: the same one it already has if it has one, and
// otherwise the first /30 of the pool that neither the host nor another sandbox is
// using. took reports whether this call is what took the address, which is what
// decides whether a failed attach may give it back.
func (m *Manager) reserve(ctx context.Context, id string) (prefix netip.Prefix, took bool, err error) {
	taken, err := m.occupancy(ctx)
	if err != nil {
		return netip.Prefix{}, false, fmt.Errorf("netpolicy: read what this node already uses before allocating for sandbox %q: %w", id, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if leased, ok := m.leases[id]; ok {
		return leased, false, nil
	}

	prefix, ok := carve(m.pool, taken, m.leases)
	if !ok {
		return netip.Prefix{}, false, fmt.Errorf("%w: pool %s has no /%d left that this node is not already using",
			errNoPoolSpace, m.pool, PrefixLength)
	}
	m.leases[id] = prefix
	return prefix, true, nil
}

// forget gives back the sandbox's lease.
func (m *Manager) forget(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.leases, id)
}

// carve picks one /30 out of pool, skipping every candidate that overlaps a prefix
// the node is already using and every candidate already leased to another sandbox.
//
// It walks the pool in ascending order, so the answer is a function of the pool and
// of what is in use rather than of a clock or a hash: the same node in the same
// state allocates the same /30 twice, which is what makes a failure reproducible.
func carve(pool netip.Prefix, taken []netip.Prefix, leases map[string]netip.Prefix) (netip.Prefix, bool) {
	base := pool.Masked().Addr().As4()
	start := binary.BigEndian.Uint32(base[:])
	// The number of addresses in the pool, and one /30 is four of them. A pool
	// smaller than a /30 is refused by New, so this is at least one candidate.
	addresses := uint32(1) << uint(32-pool.Bits())

	var candidate [4]byte
	for offset := uint32(0); offset < addresses; offset += 4 {
		binary.BigEndian.PutUint32(candidate[:], start+offset)
		next := netip.PrefixFrom(netip.AddrFrom4(candidate), PrefixLength)

		if overlapsAny(next, taken) {
			continue
		}
		if leasedToAnother(next, leases) {
			continue
		}
		return next, true
	}
	return netip.Prefix{}, false
}

// overlapsAny reports whether prefix overlaps any of others. Two prefixes overlap
// when one contains the other's first address, which netip answers for both
// directions in one call.
func overlapsAny(prefix netip.Prefix, others []netip.Prefix) bool {
	for _, other := range others {
		if prefix.Overlaps(other) {
			return true
		}
	}
	return false
}

// leasedToAnother reports whether a sandbox already holds an address from prefix.
// It is a separate question from occupancy, which only knows about the host: a
// sandbox's /30 is inside a namespace the host's own listing does not necessarily
// show as a sibling, and two leases inside one run must not collide either.
func leasedToAnother(prefix netip.Prefix, leases map[string]netip.Prefix) bool {
	for _, leased := range leases {
		if prefix.Overlaps(leased) {
			return true
		}
	}
	return false
}

// hostOccupancy reads what the node is already using, as one answer: the addresses
// and the explicit routes of the node's own namespace, and of every namespace the
// node holds.
//
// Routes are included beside addresses because a range can be in use without an
// address on any local interface — a route to a subnet through a gateway is how the
// node reaches it — and a pool carved out of such a range would hand a sandbox an
// address the node already routes somewhere else. The default route is excluded: it
// covers every address and would leave no pool at all.
//
// Reading the namespaces is what makes the lease table a cache rather than the
// state. A sandbox whose manager died still has a namespace with its /30 on it, and
// this is what keeps the next allocation from handing that address to a second
// sandbox.
func hostOccupancy(execFn Exec, ip string) Occupancy {
	return func(ctx context.Context) ([]netip.Prefix, error) {
		var out []netip.Prefix

		addresses, err := prefixesOf(ctx, execFn, ip, "", "-o", "-4", "addr", "show")
		if err != nil {
			return nil, err
		}
		out = append(out, addresses...)

		routes, err := routePrefixesOf(ctx, execFn, ip)
		if err != nil {
			return nil, err
		}
		out = append(out, routes...)

		names, err := namespaceNames(ctx, execFn, ip)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			held, err := prefixesOf(ctx, execFn, ip, name, "-o", "-4", "addr", "show")
			if err != nil {
				return nil, err
			}
			out = append(out, held...)
		}

		return out, nil
	}
}

// prefixesOf reads the prefixes one namespace carries: the node's own when name is
// empty, and otherwise the addresses of that namespace, read from inside it.
func prefixesOf(ctx context.Context, execFn Exec, ip, name string, args ...string) ([]netip.Prefix, error) {
	argv := make([]string, 0, len(args)+4)
	if name != "" {
		argv = append(argv, "netns", "exec", name, ip)
	}
	argv = append(argv, args...)

	out, err := execFn(ctx, ip, argv...)
	if err != nil {
		return nil, fmt.Errorf("read the addresses %s carries: %w", where(name), err)
	}
	return addressPrefixes(string(out)), nil
}

// routePrefixesOf reads the destinations the node routes explicitly.
func routePrefixesOf(ctx context.Context, execFn Exec, ip string) ([]netip.Prefix, error) {
	out, err := execFn(ctx, ip, "-o", "-4", "route", "show")
	if err != nil {
		return nil, fmt.Errorf("read the node's IPv4 routes: %w", err)
	}
	return routePrefixes(string(out)), nil
}

// addressPrefixes picks the address of every entry out of an `ip addr` listing.
func addressPrefixes(listing string) []netip.Prefix {
	var prefixes []netip.Prefix
	for _, fields := range fieldsPerLine(listing) {
		for i, field := range fields {
			if field != "inet" || i+1 >= len(fields) {
				continue
			}
			if prefix, err := netip.ParsePrefix(fields[i+1]); err == nil {
				prefixes = append(prefixes, prefix.Masked())
			}
			break
		}
	}
	return prefixes
}

// routePrefixes picks the destination of every explicit route out of an `ip route`
// listing, treating an address without a length as the /32 it is. The default route
// is skipped: it is a destination that matches everything.
func routePrefixes(listing string) []netip.Prefix {
	var prefixes []netip.Prefix
	for _, fields := range fieldsPerLine(listing) {
		if len(fields) == 0 || fields[0] == "default" {
			continue
		}
		destination := fields[0]
		if destination == "local" || destination == "broadcast" || destination == "anycast" || destination == "unreachable" {
			if len(fields) < 2 {
				continue
			}
			destination = fields[1]
		}
		if prefix, err := netip.ParsePrefix(destination); err == nil {
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		if addr, err := netip.ParseAddr(destination); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
		}
	}
	return prefixes
}

// fieldsPerLine splits a listing into one field slice per record.
//
// iproute2's oneline form wraps a long record with a trailing backslash and a
// newline, so the wrap is undone first: a reader that split on newlines alone
// would see a record's tail as a record of its own.
func fieldsPerLine(listing string) [][]string {
	lines := strings.Split(unwrapOneline(listing), "\n")
	out := make([][]string, 0, len(lines))
	for _, line := range lines {
		if fields := strings.Fields(line); len(fields) > 0 {
			out = append(out, fields)
		}
	}
	return out
}

// unwrapOneline undoes iproute2's oneline continuation so a record is one line.
func unwrapOneline(listing string) string {
	return strings.ReplaceAll(listing, "\\\n", " ")
}

// namespaceNames lists the namespaces the node holds.
func namespaceNames(ctx context.Context, execFn Exec, ip string) ([]string, error) {
	out, err := execFn(ctx, ip, "netns", "list")
	if err != nil {
		return nil, fmt.Errorf("list the node's network namespaces: %w", err)
	}
	var names []string
	for _, fields := range fieldsPerLine(string(out)) {
		names = append(names, fields[0])
	}
	return names, nil
}

// where names the namespace a message is about, or the node itself.
func where(name string) string {
	if name == "" {
		return "the node's own namespace"
	}
	return name
}
