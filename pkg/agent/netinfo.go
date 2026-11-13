package agent

// The `net` operation: the guest's own view of its network (RFC D9, SBX-085).
//
// A sandbox's identity is applied inside the guest (prepare.go), so the only
// place that knows what the guest actually wears is the guest: the interface it
// configured, the addresses it ended up with, the routes it installed and the
// resolver it will consult. Reading any of that from the host means reading the
// kernel's tables over a channel that a restore rewrites, and reading it over
// `exec` means the answer depends on whether the template ships `ip`.
//
// The readers here are the guest's own sources: this process's socket interface
// list for the links and addresses, /proc for the route tables, and /etc for the
// resolver. Every table is sorted, so two reads of an unchanged guest produce the
// same report.

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// GuestNet is a guest's network as the guest itself sees it.
type GuestNet struct {
	// Interfaces are the guest's links in interface-index order, loopback
	// included: a workload that binds 127.0.0.1 is a fact about the sandbox.
	Interfaces []NetInterface `json:"interfaces"`
	// Routes are the routes the guest would use, from the main tables of each
	// family. Entries the kernel marks reject are omitted: they describe an
	// unreachable destination, not a path.
	Routes []NetRoute `json:"routes"`
	// Resolvers and Search are the nameservers and search domains of the
	// guest's /etc/resolv.conf, in file order. An empty list means the guest has
	// no resolver configured, not that the read failed.
	Resolvers []string `json:"resolvers"`
	Search    []string `json:"search"`
}

// NetInterface is one of the guest's links.
type NetInterface struct {
	Name string `json:"name"`
	// Index is the kernel's interface index, which is what a route's device
	// column and a workload's socket options refer to.
	Index int    `json:"index"`
	MAC   string `json:"mac"`
	MTU   int    `json:"mtu"`
	Up    bool   `json:"up"`
	// Loopback marks lo. A sandbox's loopback is its own, so a preview or proxy
	// that lives there is invisible to every other sandbox.
	Loopback bool `json:"loopback"`
	// Addresses are the interface's addresses in CIDR form, sorted.
	Addresses []string `json:"addresses"`
}

// NetRoute is one route the guest installed.
type NetRoute struct {
	// Family is "ipv4" or "ipv6".
	Family      string `json:"family"`
	Destination string `json:"destination"`
	// Gateway is the next hop, empty for an on-link route.
	Gateway string `json:"gateway"`
	Device  string `json:"device"`
	Metric  int    `json:"metric"`
}

// Route flags from the kernel's tables: a route is usable when it is up, and an
// entry marked reject describes an unreachable destination rather than a path, so
// both families are filtered the same way. The values are the kernel's
// (include/uapi/linux/route.h).
const (
	routeFlagUp     = 0x0001
	routeFlagReject = 0x0200
)

// readGuestNet reads the guest's interfaces, routes and resolver.
//
// An unreadable /proc route table fails the operation: "no routes" and "the
// kernel's table could not be read" would look identical in a report, and a
// scheduler acting on the first would misjudge the guest. An unreadable
// /etc/resolv.conf is reported as an empty resolver instead, because a guest
// without one is a normal guest and is exactly what a template with no DNS ships.
func readGuestNet() (GuestNet, error) {
	ifaces, err := readInterfaces()
	if err != nil {
		return GuestNet{}, err
	}
	v4, err := readIPv4Routes(filepath.Join(procRoot, "net", "route"))
	if err != nil {
		return GuestNet{}, err
	}
	v6, err := readIPv6Routes(filepath.Join(procRoot, "net", "ipv6_route"))
	if err != nil {
		return GuestNet{}, err
	}
	routes := append(v4, v6...)
	sort.Slice(routes, func(i, j int) bool {
		a, b := routes[i], routes[j]
		if a.Family != b.Family {
			return a.Family < b.Family
		}
		if a.Destination != b.Destination {
			return a.Destination < b.Destination
		}
		if a.Device != b.Device {
			return a.Device < b.Device
		}
		return a.Metric < b.Metric
	})

	resolvers, search := readResolvConf(filepath.Join(etcRoot, "resolv.conf"))
	return GuestNet{Interfaces: ifaces, Routes: routes, Resolvers: resolvers, Search: search}, nil
}

// readInterfaces lists the guest's links and their addresses, in index order,
// with each link's addresses sorted so the report is stable.
func readInterfaces() ([]NetInterface, error) {
	links, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("net: list interfaces: %w", err)
	}
	// The kernel hands them in index order already, but the report's ordering is
	// part of its contract, so it is sorted rather than assumed.
	sort.Slice(links, func(i, j int) bool { return links[i].Index < links[j].Index })

	out := make([]NetInterface, 0, len(links))
	for _, link := range links {
		addrs, err := link.Addrs()
		if err != nil {
			return nil, fmt.Errorf("net: addresses of %s: %w", link.Name, err)
		}
		formatted := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			// An interface address is always a prefix; String() gives its CIDR
			// form, which is what a route and a workload's bind address use.
			formatted = append(formatted, addr.String())
		}
		sort.Strings(formatted)
		out = append(out, NetInterface{
			Name:      link.Name,
			Index:     link.Index,
			MAC:       link.HardwareAddr.String(),
			MTU:       link.MTU,
			Up:        link.Flags&net.FlagUp != 0,
			Loopback:  link.Flags&net.FlagLoopback != 0,
			Addresses: formatted,
		})
	}
	return out, nil
}

// readIPv4Routes parses /proc/net/route, whose addresses are 32-bit values in
// host byte order written as hex: the little-endian layout of the IPv4 address,
// so the bytes are reversed before they are an address.
func readIPv4Routes(path string) ([]NetRoute, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("net: read ipv4 routes: %w", err)
	}
	defer func() { _ = f.Close() }()

	var routes []NetRoute
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// Header, then: Iface Destination Gateway Flags RefCnt Use Metric Mask
		// MTU Window IRTT.
		if len(fields) < 8 || fields[0] == "Iface" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 64)
		if err != nil || flags&routeFlagUp == 0 || flags&routeFlagReject != 0 {
			continue
		}
		dest, err := ipv4FromProc(fields[1])
		if err != nil {
			continue
		}
		mask, err := ipv4FromProc(fields[7])
		if err != nil {
			continue
		}
		ones, bits := net.IPMask(mask.AsSlice()).Size()
		if bits == 0 {
			// A non-contiguous mask is not expressible as a CIDR, so it cannot be
			// reported as one; skipping it is honest, and such a route is not
			// something a guest's own configuration produces.
			continue
		}
		metric, _ := strconv.Atoi(fields[6])

		route := NetRoute{
			Family:      "ipv4",
			Destination: netip.PrefixFrom(dest, ones).String(),
			Device:      fields[0],
			Metric:      metric,
		}
		if gateway, err := ipv4FromProc(fields[2]); err == nil && gateway.IsValid() && !gateway.IsUnspecified() {
			route.Gateway = gateway.String()
		}
		routes = append(routes, route)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("net: read ipv4 routes: %w", err)
	}
	return routes, nil
}

// ipv4FromProc converts one 32-bit hex field of /proc/net/route to an address.
//
// The field is decoded as the four bytes the kernel wrote rather than as a 32-bit
// integer, so every step stays inside the width it is read at and no value is ever
// narrowed. The kernel writes the address in host byte order, which on the
// little-endian architectures the guest runs on reverses the bytes the digits
// spell, so the address is assembled from them in reverse. A field that is not
// exactly eight hex digits is refused instead of being interpreted.
func ipv4FromProc(field string) (netip.Addr, error) {
	if len(field) != 8 {
		return netip.Addr{}, fmt.Errorf("want 8 hex digits, got %q", field)
	}
	var raw [4]byte
	if _, err := hex.Decode(raw[:], []byte(field)); err != nil {
		return netip.Addr{}, err
	}
	return netip.AddrFrom4([4]byte{raw[3], raw[2], raw[1], raw[0]}), nil
}

// readIPv6Routes parses /proc/net/ipv6_route, whose columns are hex:
// destination, destination prefix length, source, source prefix length, next hop,
// metric, reference count, use and flags, then the device name (which may be
// absent).
func readIPv6Routes(path string) ([]NetRoute, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("net: read ipv6 routes: %w", err)
	}
	defer func() { _ = f.Close() }()

	var routes []NetRoute
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 9 {
			continue
		}
		flags, err := strconv.ParseUint(fields[8], 16, 64)
		if err != nil || flags&routeFlagUp == 0 || flags&routeFlagReject != 0 {
			continue
		}
		dest, ok := ipv6FromProc(fields[0])
		if !ok {
			continue
		}
		ones, err := strconv.ParseUint(fields[1], 16, 8)
		if err != nil || int(ones) > 128 {
			continue
		}
		metric, _ := strconv.ParseInt(fields[5], 16, 32)

		route := NetRoute{
			Family:      "ipv6",
			Destination: netip.PrefixFrom(dest, int(ones)).String(),
			Metric:      int(metric),
		}
		if len(fields) > 9 {
			route.Device = fields[9]
		}
		if gateway, ok := ipv6FromProc(fields[4]); ok && !gateway.IsUnspecified() {
			route.Gateway = gateway.String()
		}
		routes = append(routes, route)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("net: read ipv6 routes: %w", err)
	}
	return routes, nil
}

// ipv6FromProc converts one 32-hex-digit address field of /proc/net/ipv6_route,
// which is written in network byte order. It is decoded as the sixteen bytes the
// digits spell, so the conversion from text to address happens once and nothing
// is narrowed on the way.
func ipv6FromProc(field string) (netip.Addr, bool) {
	if len(field) != 32 {
		return netip.Addr{}, false
	}
	var raw [16]byte
	if _, err := hex.Decode(raw[:], []byte(field)); err != nil {
		return netip.Addr{}, false
	}
	return netip.AddrFrom16(raw), true
}

// readResolvConf parses the nameservers and search domains of a resolv.conf. It
// returns what it found for a missing file rather than an error: a template with
// no resolver is a normal template, and the empty list says so.
func readResolvConf(path string) (resolvers, search []string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// A comment starts at '#' or ';' in a resolv.conf, and the rest of the
		// line is not configuration.
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "nameserver":
			resolvers = append(resolvers, fields[1])
		case "search":
			search = append(search, fields[1:]...)
		}
	}
	return resolvers, search
}
