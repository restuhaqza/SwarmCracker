//go:build linux

package agent

// The applier, driven by a test instead of by a guest.
//
// applyIdentityOS runs inside a guest as root and talks to the kernel: it rewrites an
// interface's MAC, replaces the addresses and the routes, sets the clock and mixes
// entropy. A unit test has none of that — no KVM, no CAP_NET_ADMIN, no CAP_SYS_TIME —
// so this file replaces the seams prepare_linux.go declares and holds the applier to
// its decisions: what it applies, what it leaves alone because it is already right, and
// what it refuses. The privileged call is not what is under test here; the decision to
// make it is.
//
// The filesystem is not faked. A test points the paths at its own temporary directory
// and lets the applier read and write real files, so the idempotence the operation
// promises — a second run with the same identity rewrites nothing — is checked against
// real content and real modes rather than against a mock's record of a call.

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// fakeLink is the interface the applier is handed, with no kernel behind it.
type fakeLink struct{ attrs *netlink.LinkAttrs }

func (l *fakeLink) Attrs() *netlink.LinkAttrs { return l.attrs }
func (l *fakeLink) Type() string              { return "device" }

// linuxGuest is a guest a test owns: its files live in a temporary directory, and every
// privileged call is recorded rather than made.
type linuxGuest struct {
	t    *testing.T
	root string

	// attrs is what the interface reports when the applier reads it back.
	attrs *netlink.LinkAttrs

	// What the kernel answers. A zero value is the quiet, working case.
	linkErr         error
	setUpErr        error
	macErr          error
	addrs           []netlink.Addr
	addrListErr     error
	addrDelErr      error
	addrReplaceErr  error
	routes          []netlink.Route
	routeListErr    error
	routeDelErr     error
	routeReplaceErr error

	// The rest of the outside world.
	hostname       string
	hostnameErr    error
	setHostnameErr error
	clockErr       error
	mixErr         error
	keygenPath     string
	keygenErr      error

	// What the applier asked for.
	setUps         int
	macs           []net.HardwareAddr
	deletedAddrs   []string
	replacedAddrs  []string
	deletedRoutes  []netlink.Route
	replacedRoutes []netlink.Route
	hostNames      []string
	clocks         []int64
	mixed          [][]byte

	// now is the guest's wall clock, which starts at the test host's and is moved by the
	// applier's own clock calls.
	now time.Time
}

// newLinuxGuest gives a test a guest whose kernel, clock, entropy pool and ssh-keygen are
// its own. Every seam it replaces is restored when the test ends.
func newLinuxGuest(t *testing.T) *linuxGuest {
	t.Helper()

	g := &linuxGuest{
		t:          t,
		root:       t.TempDir(),
		attrs:      &netlink.LinkAttrs{Name: "eth0", Index: 7, HardwareAddr: mustHardwareAddr(t, "06:00:ac:10:00:0a")},
		hostname:   "template",
		keygenPath: "/usr/bin/ssh-keygen",
		now:        time.Now(),
	}

	previousResolv, previousMachineID, previousDBus := resolvConfPath, machineIDPath, dbusMachineIDPath
	previousHostname, previousHostKeyDir := hostnamePath, hostKeyDir
	previousPrepared, previousToken := preparedPath, agentTokenPath
	resolvConfPath = g.path("etc/resolv.conf")
	machineIDPath = g.path("etc/machine-id")
	dbusMachineIDPath = g.path("var/lib/dbus/machine-id")
	hostnamePath = g.path("etc/hostname")
	hostKeyDir = g.path("etc/ssh")
	preparedPath = g.path("var/lib/swarmcracker/prepared")
	agentTokenPath = g.path("etc/swarmcracker/agent-token")

	previousByName, previousByIndex, previousSetUp := linkByName, linkByIndex, linkSetUp
	previousHardware, previousParseAddr := linkSetHardwareAddr, parseAddr
	previousAddrList, previousAddrDel, previousAddrReplace := addrList, addrDel, addrReplace
	previousRouteList, previousRouteDel, previousRouteReplace := routeList, routeDel, routeReplace
	previousSetHostname, previousSettime, previousNow, previousMix := setHostname, clockSettime, clockNow, mixEntropy
	previousHostName, previousLookPath := hostName, lookPath

	linkByName = func(string) (netlink.Link, error) {
		if g.linkErr != nil {
			return nil, g.linkErr
		}
		return g.link(), nil
	}
	linkByIndex = func(int) (netlink.Link, error) { return g.link(), nil }
	linkSetUp = func(netlink.Link) error { g.setUps++; return g.setUpErr }
	linkSetHardwareAddr = func(_ netlink.Link, mac net.HardwareAddr) error {
		g.macs = append(g.macs, mac)
		if g.macErr == nil {
			g.attrs.HardwareAddr = mac
		}
		return g.macErr
	}
	parseAddr = netlink.ParseAddr
	addrList = func(netlink.Link, int) ([]netlink.Addr, error) {
		return g.addrs, g.addrListErr
	}
	addrDel = func(_ netlink.Link, addr *netlink.Addr) error {
		g.deletedAddrs = append(g.deletedAddrs, addrString(addr))
		if g.addrDelErr == nil {
			g.addrs = withoutAddress(g.addrs, addr)
		}
		return g.addrDelErr
	}
	addrReplace = func(_ netlink.Link, addr *netlink.Addr) error {
		g.replacedAddrs = append(g.replacedAddrs, addrString(addr))
		if g.addrReplaceErr == nil {
			g.addrs = withAddress(g.addrs, addr)
		}
		return g.addrReplaceErr
	}
	routeList = func(netlink.Link, int) ([]netlink.Route, error) {
		return g.routes, g.routeListErr
	}
	routeDel = func(route *netlink.Route) error {
		g.deletedRoutes = append(g.deletedRoutes, *route)
		if g.routeDelErr == nil {
			g.routes = withoutRoute(g.routes, route)
		}
		return g.routeDelErr
	}
	routeReplace = func(route *netlink.Route) error {
		g.replacedRoutes = append(g.replacedRoutes, *route)
		if g.routeReplaceErr == nil {
			g.routes = withRoute(g.routes, route)
		}
		return g.routeReplaceErr
	}
	setHostname = func(name []byte) error {
		g.hostNames = append(g.hostNames, string(name))
		if g.setHostnameErr == nil {
			g.hostname = string(name)
		}
		return g.setHostnameErr
	}
	clockSettime = func(_ int32, ts *unix.Timespec) error {
		g.clocks = append(g.clocks, time.Unix(ts.Sec, ts.Nsec).UnixNano())
		if g.clockErr == nil {
			g.now = time.Unix(ts.Sec, ts.Nsec)
		}
		return g.clockErr
	}
	clockNow = func() time.Time { return g.now }
	mixEntropy = func(_ uintptr, info []byte) error {
		g.mixed = append(g.mixed, append([]byte(nil), info...))
		return g.mixErr
	}
	hostName = func() (string, error) { return g.hostname, g.hostnameErr }
	lookPath = func(string) (string, error) {
		if g.keygenErr != nil {
			return "", g.keygenErr
		}
		return g.keygenPath, nil
	}

	t.Cleanup(func() {
		resolvConfPath, machineIDPath, dbusMachineIDPath = previousResolv, previousMachineID, previousDBus
		hostnamePath, hostKeyDir = previousHostname, previousHostKeyDir
		preparedPath, agentTokenPath = previousPrepared, previousToken
		linkByName, linkByIndex, linkSetUp = previousByName, previousByIndex, previousSetUp
		linkSetHardwareAddr, parseAddr = previousHardware, previousParseAddr
		addrList, addrDel, addrReplace = previousAddrList, previousAddrDel, previousAddrReplace
		routeList, routeDel, routeReplace = previousRouteList, previousRouteDel, previousRouteReplace
		setHostname, clockSettime, clockNow = previousSetHostname, previousSettime, previousNow
		mixEntropy = previousMix
		hostName, lookPath = previousHostName, previousLookPath
	})

	return g
}

// withAddress is the interface's addresses after one is installed: the same address on
// the same network is replaced in place, exactly as the kernel's AddrReplace does.
func withAddress(addrs []netlink.Addr, addr *netlink.Addr) []netlink.Addr {
	out := make([]netlink.Addr, 0, len(addrs)+1)
	replaced := false
	for _, have := range addrs {
		if sameAddress(have, *addr) {
			replaced = true
		}
		out = append(out, have)
	}
	if replaced {
		return withoutAddress(out, addr)
	}
	return append(out, *addr)
}

// withoutAddress is the interface's addresses with one removed, by network.
func withoutAddress(addrs []netlink.Addr, addr *netlink.Addr) []netlink.Addr {
	out := make([]netlink.Addr, 0, len(addrs))
	for _, have := range addrs {
		if sameAddress(have, *addr) {
			continue
		}
		out = append(out, have)
	}
	return out
}

// withRoute is the routing table after a route is installed.
func withRoute(routes []netlink.Route, route *netlink.Route) []netlink.Route {
	out := make([]netlink.Route, 0, len(routes)+1)
	replaced := false
	for _, have := range routes {
		if sameDestination(have.Dst, route.Dst) && have.Gw.Equal(route.Gw) {
			replaced = true
		}
		out = append(out, have)
	}
	if replaced {
		return withoutRoute(out, route)
	}
	return append(out, *route)
}

// withoutRoute is the routing table with one route removed.
func withoutRoute(routes []netlink.Route, route *netlink.Route) []netlink.Route {
	out := make([]netlink.Route, 0, len(routes))
	for _, have := range routes {
		if sameDestination(have.Dst, route.Dst) && have.Gw.Equal(route.Gw) {
			continue
		}
		out = append(out, have)
	}
	return out
}

// path is a place inside the guest's directory.
func (g *linuxGuest) path(name string) string { return filepath.Join(g.root, name) }

// write puts a file in the guest, as the template that built it would have.
func (g *linuxGuest) write(name, body string, mode os.FileMode) string {
	g.t.Helper()

	path := g.path(name)
	require.NoError(g.t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(g.t, os.WriteFile(path, []byte(body), mode))
	return path
}

// contents reads a file the applier was expected to write.
func (g *linuxGuest) contents(name string) string {
	g.t.Helper()

	body, err := os.ReadFile(g.path(name))
	require.NoError(g.t, err, "%s must exist", name)
	return string(body)
}

// mode is the mode a file carries, which is how "the write was skipped" is told apart
// from "the same bytes were written again".
func (g *linuxGuest) mode(name string) os.FileMode {
	g.t.Helper()

	info, err := os.Stat(g.path(name))
	require.NoError(g.t, err, "%s must exist", name)
	return info.Mode().Perm()
}

func (g *linuxGuest) exists(name string) bool {
	_, err := os.Stat(g.path(name))
	return err == nil
}

func (g *linuxGuest) link() netlink.Link { return &fakeLink{attrs: g.attrs} }

// installKeygen makes the applier's ssh-keygen a script of the test's own: it runs a
// real command (so the applier's exec path, its error handling and its output are the
// real ones) whose effect and exit status the test chooses. The default creates the two
// host keys a guest would get.
func (g *linuxGuest) installKeygen(body string) string {
	g.t.Helper()

	if body == "" {
		body = fmt.Sprintf(`for type in ed25519 rsa; do
  : > "%[1]s/ssh_host_$type""_key"
  : > "%[1]s/ssh_host_$type""_key.pub"
done`, hostKeyDir)
	}
	path := g.write("bin/ssh-keygen", "#!/bin/sh\necho \"$@\" > "+g.path("keygen.argv")+"\n"+body+"\n", 0o755)
	g.keygenPath = path
	return path
}

// mustHardwareAddr parses a MAC a test writes literally.
func mustHardwareAddr(t *testing.T, mac string) net.HardwareAddr {
	t.Helper()

	parsed, err := net.ParseMAC(mac)
	require.NoError(t, err)
	return parsed
}

// mustCIDR parses a network a test writes literally.
func mustCIDR(t *testing.T, cidr string) *net.IPNet {
	t.Helper()

	_, network, err := net.ParseCIDR(cidr)
	require.NoError(t, err)
	return network
}

// guestAddr is an address as netlink reports one.
func guestAddr(t *testing.T, cidr string) netlink.Addr {
	t.Helper()

	addr, err := netlink.ParseAddr(cidr)
	require.NoError(t, err)
	return *addr
}

// directRoute is the on-link route a kernel reports for the interface's own network.
func directRoute(t *testing.T, index int, cidr, src string) netlink.Route {
	t.Helper()

	return netlink.Route{
		LinkIndex: index,
		Dst:       mustCIDR(t, cidr),
		Src:       net.ParseIP(src).To4(),
		Scope:     netlink.SCOPE_LINK,
	}
}

// defaultRoute is a default route as the kernel reports one: with the destination
// spelled out, which is not how a caller asks for it. The two mean the same route, and
// telling them apart is what keeps a repeated prepare from "changing" the default route
// it already installed.
func defaultRoute(index int, gateway string) netlink.Route {
	return netlink.Route{
		LinkIndex: index,
		Dst:       &net.IPNet{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)},
		Gw:        net.ParseIP(gateway).To4(),
	}
}

// addrString renders an address the way the tests compare them.
func addrString(addr *netlink.Addr) string {
	if addr == nil || addr.IPNet == nil {
		return "<nil>"
	}
	return addr.IPNet.String()
}

// TestApplyMAC covers the link address: what a clone is given, and what it is not
// written again.
func TestApplyMAC(t *testing.T) {
	t.Run("a MAC the interface already carries is not written again", func(t *testing.T) {
		g := newLinuxGuest(t)
		changed := newChangeSet()

		require.NoError(t, applyMAC(g.link(), Identity{Interface: "eth0", MAC: "06:00:ac:10:00:0a"}, changed))

		assert.Empty(t, g.macs, "an interface that already carries the identity's MAC is left alone")
		assert.Empty(t, changed.sorted(), "and nothing is reported as changed")
	})

	t.Run("a different MAC replaces the checkpoint's", func(t *testing.T) {
		g := newLinuxGuest(t)
		changed := newChangeSet()

		require.NoError(t, applyMAC(g.link(), Identity{Interface: "eth0", MAC: "06:00:ac:10:00:0b"}, changed))

		require.Len(t, g.macs, 1)
		assert.Equal(t, "06:00:ac:10:00:0b", g.macs[0].String())
		assert.Equal(t, []string{PartMAC}, changed.sorted())
	})

	t.Run("an identity that names no MAC leaves the checkpoint's", func(t *testing.T) {
		g := newLinuxGuest(t)

		require.NoError(t, applyMAC(g.link(), Identity{Interface: "eth0"}, newChangeSet()))

		assert.Empty(t, g.macs)
	})

	t.Run("a MAC that is not a MAC is refused before the kernel is asked", func(t *testing.T) {
		g := newLinuxGuest(t)

		err := applyMAC(g.link(), Identity{Interface: "eth0", MAC: "not-a-mac"}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), `identity mac "not-a-mac"`)
		assert.Empty(t, g.macs)
	})

	t.Run("the kernel's refusal is reported with the interface and the MAC", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.macErr = unix.EPERM

		err := applyMAC(g.link(), Identity{Interface: "eth0", MAC: "06:00:ac:10:00:0b"}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "give eth0 the mac 06:00:ac:10:00:0b")
		assert.ErrorIs(t, err, unix.EPERM)
	})
}

// TestApplyAddresses covers what the interface carries afterwards: the identity's
// address and nothing the checkpoint left behind.
func TestApplyAddresses(t *testing.T) {
	t.Run("the checkpoint's addresses go and the identity's stays", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.addrs = []netlink.Addr{guestAddr(t, "10.97.0.10/24"), guestAddr(t, "192.168.1.5/24")}
		changed := newChangeSet()

		require.NoError(t, applyAddresses(g.link(), Identity{Interface: "eth0", Address: "10.97.0.11/24"}, changed))

		assert.Equal(t, []string{"10.97.0.10/24", "192.168.1.5/24"}, g.deletedAddrs,
			"every address the checkpoint carried goes, including the source's own: it belongs to the machine the checkpoint came from")
		assert.Equal(t, []string{"10.97.0.11/24"}, g.replacedAddrs)
		assert.Equal(t, []string{PartAddress}, changed.sorted())
	})

	t.Run("an interface that already carries the identity's address is not reported as changed", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.addrs = []netlink.Addr{guestAddr(t, "10.97.0.11/24")}
		changed := newChangeSet()

		require.NoError(t, applyAddresses(g.link(), Identity{Interface: "eth0", Address: "10.97.0.11/24"}, changed))

		assert.Empty(t, g.deletedAddrs, "the address is the one the interface already has")
		assert.Empty(t, changed.sorted(), "a repeated prepare of the same identity changes nothing")
	})

	t.Run("an identity that names no address leaves the interface with none", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.addrs = []netlink.Addr{guestAddr(t, "10.97.0.10/24")}
		changed := newChangeSet()

		require.NoError(t, applyAddresses(g.link(), Identity{Interface: "eth0"}, changed))

		assert.Equal(t, []string{"10.97.0.10/24"}, g.deletedAddrs)
		assert.Empty(t, g.replacedAddrs, "nothing is given back to the interface")
		assert.Equal(t, []string{PartAddress}, changed.sorted())
	})

	t.Run("an address that is not CIDR is refused", func(t *testing.T) {
		g := newLinuxGuest(t)

		err := applyAddresses(g.link(), Identity{Interface: "eth0", Address: "10.97.0.11"}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), `identity address "10.97.0.11"`)
		assert.Empty(t, g.deletedAddrs)
	})

	t.Run("a kernel that will not list addresses stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.addrListErr = unix.ENODEV

		err := applyAddresses(g.link(), Identity{Interface: "eth0", Address: "10.97.0.11/24"}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "list the addresses of eth0")
	})

	t.Run("a kernel that will not remove an address stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.addrs = []netlink.Addr{guestAddr(t, "10.97.0.10/24")}
		g.addrDelErr = unix.EPERM

		err := applyAddresses(g.link(), Identity{Interface: "eth0", Address: "10.97.0.11/24"}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "remove 10.97.0.10/24 from eth0")
	})

	t.Run("a kernel that will not add the address stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.addrReplaceErr = unix.EPERM

		err := applyAddresses(g.link(), Identity{Interface: "eth0", Address: "10.97.0.11/24"}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "give eth0 the address 10.97.0.11/24")
	})
}

// TestApplyRoutes covers the default route and the extra ones, including the case the
// checkpoint's own default route has to go.
func TestApplyRoutes(t *testing.T) {
	t.Run("the identity's default route replaces the checkpoint's", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.routes = []netlink.Route{
			defaultRoute(7, "10.97.9.254"),
			directRoute(t, 7, "10.97.0.0/24", "10.97.0.10"),
		}
		changed := newChangeSet()

		require.NoError(t, applyRoutes(g.link(), Identity{Interface: "eth0", Gateway: "10.97.0.1"}, changed))

		require.Len(t, g.deletedRoutes, 1, "the checkpoint's default route is the only one removed")
		assert.Equal(t, "10.97.9.254", g.deletedRoutes[0].Gw.String())
		require.Len(t, g.replacedRoutes, 1)
		assert.Equal(t, "10.97.0.1", g.replacedRoutes[0].Gw.String())
		assert.Nil(t, g.replacedRoutes[0].Dst, "the default route is installed as the default")
		assert.Equal(t, []string{PartRoutes}, changed.sorted())
	})

	t.Run("a default route already in place is not reported as changed", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.routes = []netlink.Route{defaultRoute(7, "10.97.0.1")}
		changed := newChangeSet()

		require.NoError(t, applyRoutes(g.link(), Identity{Interface: "eth0", Gateway: "10.97.0.1"}, changed))

		assert.Empty(t, g.deletedRoutes)
		assert.Empty(t, changed.sorted(),
			"the kernel spells the default route out and the identity does not, and both are the same route")
	})

	t.Run("the checkpoint's default route is removed even when the new one is already in place", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.routes = []netlink.Route{defaultRoute(7, "10.97.0.1"), defaultRoute(7, "10.97.9.254")}
		changed := newChangeSet()

		require.NoError(t, applyRoutes(g.link(), Identity{Interface: "eth0", Gateway: "10.97.0.1"}, changed))

		require.Len(t, g.deletedRoutes, 1, "a clone that kept its source's gateway would leave through its network")
		assert.Equal(t, "10.97.9.254", g.deletedRoutes[0].Gw.String())
	})

	t.Run("extra routes are installed, with and without a next hop", func(t *testing.T) {
		g := newLinuxGuest(t)
		changed := newChangeSet()

		require.NoError(t, applyRoutes(g.link(), Identity{
			Interface: "eth0",
			Routes: []Route{
				{Destination: "10.99.0.0/24"},
				{Destination: "10.100.0.0/24", Gateway: "10.97.0.254"},
			},
		}, changed))

		require.Len(t, g.replacedRoutes, 2)
		assert.Nil(t, g.replacedRoutes[0].Gw, "a route with no gateway is on-link")
		assert.Equal(t, "10.100.0.0/24", g.replacedRoutes[1].Dst.String())
		assert.Equal(t, "10.97.0.254", g.replacedRoutes[1].Gw.String())
		assert.Equal(t, []string{PartRoutes}, changed.sorted())
	})

	t.Run("a route the guest already has is not reported as changed", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.routes = []netlink.Route{{LinkIndex: 7, Dst: mustCIDR(t, "10.99.0.0/24"), Gw: net.ParseIP("10.97.0.254").To4()}}
		changed := newChangeSet()

		require.NoError(t, applyRoutes(g.link(), Identity{
			Interface: "eth0",
			Routes:    []Route{{Destination: "10.99.0.0/24", Gateway: "10.97.0.254"}},
		}, changed))

		assert.Empty(t, changed.sorted())
	})

	t.Run("a gateway that is not an address is refused", func(t *testing.T) {
		g := newLinuxGuest(t)

		err := applyRoutes(g.link(), Identity{Interface: "eth0", Gateway: "the-router"}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), `identity gateway "the-router"`)
	})

	t.Run("a route that is not CIDR is refused", func(t *testing.T) {
		g := newLinuxGuest(t)

		err := applyRoutes(g.link(), Identity{
			Interface: "eth0",
			Routes:    []Route{{Destination: "10.99.0.0"}},
		}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), `identity route "10.99.0.0"`)
	})

	t.Run("a route gateway that is not an address is refused", func(t *testing.T) {
		g := newLinuxGuest(t)

		err := applyRoutes(g.link(), Identity{
			Interface: "eth0",
			Routes:    []Route{{Destination: "10.99.0.0/24", Gateway: "the-router"}},
		}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), `identity route "10.99.0.0/24" gateway "the-router"`)
	})

	t.Run("a kernel that will not set the default route stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.routeReplaceErr = unix.ENETUNREACH

		err := applyRoutes(g.link(), Identity{Interface: "eth0", Gateway: "10.97.0.1"}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "set the default route via 10.97.0.1")
		assert.ErrorIs(t, err, unix.ENETUNREACH)
	})

	t.Run("a kernel that will not remove the checkpoint's default route stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.routes = []netlink.Route{defaultRoute(7, "10.97.9.254")}
		g.routeDelErr = unix.EPERM

		err := applyRoutes(g.link(), Identity{Interface: "eth0", Gateway: "10.97.0.1"}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "remove the default route via 10.97.9.254")
	})

	t.Run("a kernel that will not install an extra route stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.routeReplaceErr = unix.EPERM

		err := applyRoutes(g.link(), Identity{
			Interface: "eth0",
			Routes:    []Route{{Destination: "10.99.0.0/24"}},
		}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "apply the route 10.99.0.0/24")
	})

	t.Run("a kernel that will not list routes stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.routeListErr = unix.ENODEV

		err := applyRoutes(g.link(), Identity{Interface: "eth0", Gateway: "10.97.0.1"}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "list the routes of eth0")
	})
}

// TestApplyDNS covers the resolver configuration: the nameservers are the identity's,
// and everything else in the file is the template's.
func TestApplyDNS(t *testing.T) {
	t.Run("the identity's nameservers are written and the template's lines are kept", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/resolv.conf", "; generated by the image build\nsearch us-east-2.compute.internal\noptions timeout:2 attempts:5\nnameserver 172.31.0.2\n", 0o644)
		changed := newChangeSet()

		require.NoError(t, applyDNS(Identity{DNS: []string{"10.97.0.1", "10.97.0.2"}}, changed))

		assert.Equal(t, "nameserver 10.97.0.1\nnameserver 10.97.0.2\nsearch us-east-2.compute.internal\noptions timeout:2 attempts:5\n",
			g.contents("etc/resolv.conf"))
		assert.Equal(t, os.FileMode(0o644), g.mode("etc/resolv.conf"))
		assert.Equal(t, []string{PartDNS}, changed.sorted())
	})

	t.Run("a resolver configuration that is already right is not written again", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/resolv.conf", "nameserver 10.97.0.1\n", 0o600)
		changed := newChangeSet()

		require.NoError(t, applyDNS(Identity{DNS: []string{"10.97.0.1"}}, changed))

		assert.Empty(t, changed.sorted())
		assert.Equal(t, os.FileMode(0o600), g.mode("etc/resolv.conf"),
			"the file is not rewritten, so its mode is the one it had")
	})

	t.Run("an identity that names no resolver leaves the file alone", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/resolv.conf", "nameserver 172.31.0.2\n", 0o644)

		require.NoError(t, applyDNS(Identity{}, newChangeSet()))

		assert.Equal(t, "nameserver 172.31.0.2\n", g.contents("etc/resolv.conf"))
	})

	t.Run("a nameserver that is not an address is refused and the file is untouched", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/resolv.conf", "nameserver 172.31.0.2\n", 0o644)

		err := applyDNS(Identity{DNS: []string{"dns.example"}}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), `identity nameserver "dns.example"`)
		assert.Equal(t, "nameserver 172.31.0.2\n", g.contents("etc/resolv.conf"))
	})

	t.Run("a resolver configuration that cannot be read stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/resolv.conf/keep", "not a file", 0o644)

		err := applyDNS(Identity{DNS: []string{"10.97.0.1"}}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read")
	})

	// A write that fails after a read that worked needs a directory this test cannot
	// write, which root is allowed to write anyway. What is asserted here is the shape
	// the caller depends on: the hook fails and no half-written resolver configuration is
	// left where a guest would read it. The write's own failure is covered by
	// TestWriteFileAtomic.
	t.Run("a resolver configuration that cannot be reached is a failure and leaves no file", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc", "a file where a directory has to be", 0o644)

		err := applyDNS(Identity{DNS: []string{"10.97.0.1"}}, newChangeSet())

		require.Error(t, err)
		assert.False(t, g.exists("etc/resolv.conf"))
	})
}

// TestResolvConf covers the rendering on its own: which lines survive a rewrite.
func TestResolvConf(t *testing.T) {
	t.Run("comments, blank lines and the old nameservers are dropped", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/resolv.conf", "# a comment\n\nnameserver 172.31.0.2\nsearch internal\n; another comment\noptions ndots:5\n", 0o644)

		body, err := resolvConf([]string{"10.97.0.1"})

		require.NoError(t, err)
		assert.Equal(t, "nameserver 10.97.0.1\nsearch internal\noptions ndots:5\n", body)
	})

	t.Run("a guest with no resolver configuration gets one", func(t *testing.T) {
		g := newLinuxGuest(t)

		body, err := resolvConf([]string{"10.97.0.1"})

		require.NoError(t, err)
		assert.Equal(t, "nameserver 10.97.0.1\n", body)
		assert.False(t, g.exists("etc/resolv.conf"), "rendering is not writing")
	})

	t.Run("the first bad nameserver is the one reported", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/resolv.conf", "search internal\n", 0o644)

		_, err := resolvConf([]string{"10.97.0.1", "", "10.97.0.2"})

		require.Error(t, err)
		assert.Contains(t, err.Error(), `identity nameserver ""`)
	})
}

// TestApplyHostname covers the name the guest reports and the file that keeps it.
func TestApplyHostname(t *testing.T) {
	t.Run("a different name is set and written down", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.hostname = "template"
		changed := newChangeSet()

		require.NoError(t, applyHostname(Identity{Hostname: "sbx-clb"}, changed))

		assert.Equal(t, []string{"sbx-clb"}, g.hostNames)
		assert.Equal(t, "sbx-clb\n", g.contents("etc/hostname"))
		assert.Equal(t, []string{PartHostname}, changed.sorted())
	})

	t.Run("a name the guest already has is not reported as changed", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.hostname = "sbx-clb"
		changed := newChangeSet()

		require.NoError(t, applyHostname(Identity{Hostname: "sbx-clb"}, changed))

		assert.Empty(t, g.hostNames)
		assert.False(t, g.exists("etc/hostname"), "the file is not written when the kernel already reports the name")
		assert.Empty(t, changed.sorted())
	})

	t.Run("an identity that names no host leaves the template's", func(t *testing.T) {
		g := newLinuxGuest(t)

		require.NoError(t, applyHostname(Identity{}, newChangeSet()))

		assert.Empty(t, g.hostNames)
	})

	t.Run("a kernel that will not take the name stops the hook before the file is written", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.setHostnameErr = unix.EPERM

		err := applyHostname(Identity{Hostname: "sbx-clb"}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), `set the host name to "sbx-clb"`)
		assert.False(t, g.exists("etc/hostname"),
			"a file that says the guest has a name it does not have is worse than no file")
	})
}

// TestApplyMachineIdentity covers the parts that can only be minted once per machine,
// and the generation stamp that keeps a second resume from minting them again.
func TestApplyMachineIdentity(t *testing.T) {
	identity := func() Identity {
		return Identity{
			Interface: "eth0",
			MAC:       "06:00:ac:10:00:0b",
			Address:   "10.97.0.11/24",
			Gateway:   "10.97.0.1",
			Hostname:  "sbx-clb",
			MachineID: "0123456789abcdef0123456789abcdef",
			Entropy:   "0f1e2d3c",
		}
	}

	t.Run("a clone gets its own machine id, host keys, entropy and generation", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/ssh/ssh_host_ed25519_key", "the source's key", 0o600)
		g.installKeygen("")
		id := identity()
		changed := newChangeSet()

		require.NoError(t, applyMachineIdentity(context.Background(), id, changed))

		assert.Equal(t, id.MachineID+"\n", g.contents("etc/machine-id"))
		assert.False(t, g.exists("var/lib/dbus/machine-id"),
			"the D-Bus copy is only written where the library reads it")
		assert.Equal(t, "-A\n", g.contents("keygen.argv"), "the host keys are regenerated with ssh-keygen -A")
		assert.Equal(t, id.GenerationID()+"\n", g.contents("var/lib/swarmcracker/prepared"))
		assert.Equal(t, []string{PartEntropy, PartHostKeys, PartMachineID}, changed.sorted())
		assert.Equal(t, len("0f1e2d3c")/2*8, changed.entropyBits)
		assert.Equal(t, []string{"ed25519", "rsa"}, changed.hostKeys)

		require.Len(t, g.mixed, 1)
		assert.Equal(t, uint32(32), binary.LittleEndian.Uint32(g.mixed[0][0:4]),
			"the pool is credited 8 bits per byte")
		assert.Equal(t, uint32(4), binary.LittleEndian.Uint32(g.mixed[0][4:8]))
		assert.Equal(t, "0f1e2d3c", hex.EncodeToString(g.mixed[0][8:]))
	})

	t.Run("the same identity a second time mints nothing again", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/ssh/ssh_host_ed25519_key", "the source's key", 0o600)
		g.installKeygen("")
		id := identity()
		require.NoError(t, applyMachineIdentity(context.Background(), id, newChangeSet()))

		// A guest that ran the hook once carries a machine id it was given; a second
		// resume with the same identity must not mint a new one, or every resume would
		// look like a new machine to anything watching.
		require.NoError(t, os.Chmod(machineIDPath, 0o600))
		mixed, keygenRuns := len(g.mixed), g.contents("keygen.argv")
		changed := newChangeSet()

		require.NoError(t, applyMachineIdentity(context.Background(), id, changed))

		assert.Empty(t, changed.sorted())
		assert.Len(t, g.mixed, mixed, "the entropy pool is not fed twice for one machine")
		assert.Equal(t, keygenRuns, g.contents("keygen.argv"), "and the host keys are not made again")
		assert.Equal(t, os.FileMode(0o600), g.mode("etc/machine-id"),
			"the machine id is not rewritten, so its mode is the one it had")
	})

	t.Run("an identity that asks for new host keys gets them without a new machine id", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/ssh/ssh_host_ed25519_key", "the source's key", 0o600)
		g.installKeygen("")
		id := identity()
		require.NoError(t, applyMachineIdentity(context.Background(), id, newChangeSet()))
		require.NoError(t, os.Chmod(machineIDPath, 0o600))
		mixed := len(g.mixed)

		id.HostKeys = true
		changed := newChangeSet()
		require.NoError(t, applyMachineIdentity(context.Background(), id, changed))

		assert.Equal(t, []string{PartHostKeys}, changed.sorted(),
			"a guest that is told to rotate its keys rotates them, and nothing else")
		assert.Len(t, g.mixed, mixed)
		assert.Equal(t, os.FileMode(0o600), g.mode("etc/machine-id"))
	})

	t.Run("entropy that is not hex-encoded stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/ssh/ssh_host_ed25519_key", "the source's key", 0o600)
		g.installKeygen("")
		id := identity()
		id.Entropy = "not hex"

		err := applyMachineIdentity(context.Background(), id, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "not hex-encoded")
	})

	t.Run("a generation that cannot be read stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("var/lib/swarmcracker/prepared/keep", "a directory where the stamp goes", 0o644)

		err := applyMachineIdentity(context.Background(), identity(), newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read")
	})

}

// TestWriteMachineID covers the machine id itself, including the copy only images that
// carry D-Bus read.
func TestWriteMachineID(t *testing.T) {
	t.Run("the identity's machine id is written to systemd's file alone", func(t *testing.T) {
		g := newLinuxGuest(t)

		require.NoError(t, writeMachineID("0123456789abcdef0123456789abcdef"))

		assert.Equal(t, "0123456789abcdef0123456789abcdef\n", g.contents("etc/machine-id"))
		assert.Equal(t, os.FileMode(0o444), g.mode("etc/machine-id"))
		assert.False(t, g.exists("var/lib/dbus/machine-id"),
			"creating a file nothing reads would be a claim about the guest that is not true")
	})

	t.Run("an image that carries D-Bus gets the copy too", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("var/lib/dbus/machine-id", "the source's\n", 0o444)

		require.NoError(t, writeMachineID("0123456789abcdef0123456789abcdef"))

		assert.Equal(t, "0123456789abcdef0123456789abcdef\n", g.contents("var/lib/dbus/machine-id"))
	})

	t.Run("a machine id that is already right is not written again", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/machine-id", "0123456789abcdef0123456789abcdef\n", 0o600)

		require.NoError(t, writeMachineID("0123456789abcdef0123456789abcdef"))

		assert.Equal(t, os.FileMode(0o600), g.mode("etc/machine-id"))
	})

	t.Run("an identity that names no machine id gets a freshly minted one", func(t *testing.T) {
		g := newLinuxGuest(t)

		require.NoError(t, writeMachineID(""))

		minted := strings.TrimSpace(g.contents("etc/machine-id"))
		assert.Len(t, minted, machineIDBytes*2, "a machine id is 16 bytes hex encoded")
		_, err := hex.DecodeString(minted)
		assert.NoError(t, err, "and it is hex")
	})

	t.Run("a machine id that cannot be read stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/machine-id/keep", "a directory where the file goes", 0o644)

		err := writeMachineID("0123456789abcdef0123456789abcdef")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the machine id")
	})

	t.Run("an entropy source that fails stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		previous := randomRead
		randomRead = func([]byte) (int, error) { return 0, unix.EIO }
		t.Cleanup(func() { randomRead = previous })

		err := writeMachineID("")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "mint a machine id")
		assert.False(t, g.exists("etc/machine-id"), "a machine id that could not be minted is not written")
	})
}

// TestNewMachineID covers the bytes: 16 of them, hex encoded, and not the same twice.
func TestNewMachineID(t *testing.T) {
	first, err := newMachineID()
	require.NoError(t, err)
	second, err := newMachineID()
	require.NoError(t, err)

	assert.Len(t, first, machineIDBytes*2)
	assert.NotEqual(t, first, second, "two clones must not be one machine")
	_, err = hex.DecodeString(first)
	assert.NoError(t, err)
}

// TestRegenerateHostKeys covers the keys that name a host: a clone must not present the
// keys of the machine its checkpoint came from, and a guest without OpenSSH is not a
// failure unless the identity asked for keys.
func TestRegenerateHostKeys(t *testing.T) {
	identity := Identity{Interface: "eth0", MAC: "06:00:ac:10:00:0b", Address: "10.97.0.11/24"}

	t.Run("the checkpoint's host keys are replaced", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/ssh/ssh_host_ed25519_key", "the source's key", 0o600)
		g.write("etc/ssh/ssh_host_ed25519_key.pub", "the source's public key", 0o644)
		g.installKeygen("")
		changed := newChangeSet()

		require.NoError(t, regenerateHostKeys(context.Background(), identity, changed))

		assert.Equal(t, "", g.contents("etc/ssh/ssh_host_ed25519_key"),
			"the key the checkpoint carried is not left in place")
		assert.NotContains(t, g.contents("etc/ssh/ssh_host_ed25519_key.pub"), "the source's",
			"and neither is the public half a client would pin")
		assert.Equal(t, []string{"ed25519", "rsa"}, changed.hostKeys)
		assert.True(t, changed.parts[PartHostKeys])
	})

	t.Run("a guest without OpenSSH is left alone when nobody asked for keys", func(t *testing.T) {
		g := newLinuxGuest(t)
		changed := newChangeSet()

		require.NoError(t, regenerateHostKeys(context.Background(), identity, changed))

		assert.Empty(t, changed.sorted())
		assert.False(t, g.exists("keygen.argv"), "ssh-keygen is not run")
	})

	t.Run("a guest with no keys at all that is asked for them is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		identityWithKeys := identity
		identityWithKeys.HostKeys = true

		err := regenerateHostKeys(context.Background(), identityWithKeys, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "this guest has none")
		assert.False(t, g.exists("etc/ssh"), "and no keys are made up for it")
	})

	t.Run("a guest with keys and no ssh-keygen is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/ssh/ssh_host_ed25519_key", "the source's key", 0o600)
		g.keygenErr = os.ErrNotExist

		err := regenerateHostKeys(context.Background(), identity, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "regenerate the SSH host keys")
	})

	t.Run("a key that cannot be removed is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		// A directory in the key's place: os.Remove removes an empty one and refuses a
		// full one, which is the failure this branch is for.
		g.write("etc/ssh/ssh_host_ed25519_key/in-the-way", "not removable", 0o600)
		g.installKeygen("")

		err := regenerateHostKeys(context.Background(), identity, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "remove the SSH host key")
	})

	t.Run("ssh-keygen that fails stops the hook with what it said", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/ssh/ssh_host_ed25519_key", "the source's key", 0o600)
		g.installKeygen("echo 'no entropy' >&2\nexit 1")

		err := regenerateHostKeys(context.Background(), identity, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "generate SSH host keys with ssh-keygen -A")
		assert.Contains(t, err.Error(), "no entropy")
	})

	t.Run("a key directory that cannot be listed is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		hostKeyDir = g.path("etc/ssh[") // an unmatched bracket is a bad pattern
		identityWithKeys := identity
		identityWithKeys.HostKeys = true

		err := regenerateHostKeys(context.Background(), identityWithKeys, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "list the SSH host keys")
	})
}

// TestHostKeyTypes covers what the report says the guest now has.
func TestHostKeyTypes(t *testing.T) {
	t.Run("the types are listed without their public halves", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/ssh/ssh_host_rsa_key", "a key", 0o600)
		g.write("etc/ssh/ssh_host_rsa_key.pub", "a public key", 0o644)
		g.write("etc/ssh/ssh_host_ed25519_key", "a key", 0o600)
		g.write("etc/ssh/ssh_config", "not a host key", 0o644)

		types, err := hostKeyTypes()

		require.NoError(t, err)
		assert.Equal(t, []string{"ed25519", "rsa"}, types)
	})

	t.Run("a guest with no keys reports none", func(t *testing.T) {
		newLinuxGuest(t)

		types, err := hostKeyTypes()

		require.NoError(t, err)
		assert.Empty(t, types)
	})

	t.Run("a key directory that cannot be listed is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		hostKeyDir = g.path("etc/ssh[")

		_, err := hostKeyTypes()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "list the SSH host keys")
	})
}

// TestApplyToken covers the credential: the checkpoint's token must not authenticate a
// clone, and the file must not be readable by anyone but its owner.
func TestApplyToken(t *testing.T) {
	const token = "5216d9b54bb3f343bc34c2b3167b65ecd73cf3359ff402ae1b9212367e98b2fc"

	t.Run("the clone's token is installed read-only for its owner", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/swarmcracker/agent-token", "the source's token\n", 0o400)
		changed := newChangeSet()

		require.NoError(t, applyToken(Identity{Token: token}, changed))

		assert.Equal(t, token+"\n", g.contents("etc/swarmcracker/agent-token"))
		assert.Equal(t, os.FileMode(0o400), g.mode("etc/swarmcracker/agent-token"))
		assert.Equal(t, []string{PartToken}, changed.sorted())
		assert.True(t, changed.tokenRotated)
	})

	t.Run("a token that is already installed is not written again", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/swarmcracker/agent-token", token+"\n", 0o600)
		changed := newChangeSet()

		require.NoError(t, applyToken(Identity{Token: token}, changed))

		assert.Empty(t, changed.sorted())
		assert.False(t, changed.tokenRotated)
		assert.Equal(t, os.FileMode(0o600), g.mode("etc/swarmcracker/agent-token"))
	})

	t.Run("an identity that names no token leaves the checkpoint's", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/swarmcracker/agent-token", "the source's token\n", 0o400)

		require.NoError(t, applyToken(Identity{}, newChangeSet()))

		assert.Equal(t, "the source's token\n", g.contents("etc/swarmcracker/agent-token"))
	})

	t.Run("a token that is not a token is refused before the file is touched", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/swarmcracker/agent-token", "the source's token\n", 0o400)

		err := applyToken(Identity{Token: "a token\nwith a newline"}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-printable")
		assert.Equal(t, "the source's token\n", g.contents("etc/swarmcracker/agent-token"))
	})

	t.Run("a token file that cannot be written stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/swarmcracker", "a file where the directory has to be", 0o644)

		err := applyToken(Identity{Token: token}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "create")
	})
}

// TestApplyClock covers the rule the operation lives by: a clock is only ever moved
// forward, so a guest that is ahead keeps its clock and a repeated prepare changes
// nothing.
func TestApplyClock(t *testing.T) {
	now := time.Now()

	t.Run("a guest behind the node's instant is moved forward", func(t *testing.T) {
		g := newLinuxGuest(t)
		changed := newChangeSet()
		want := now.Add(time.Hour).UnixNano()

		require.NoError(t, applyClock(Identity{ClockUnixNano: want}, changed))

		assert.Equal(t, []int64{want}, g.clocks)
		assert.Equal(t, []string{PartClock}, changed.sorted())
	})

	t.Run("a guest that is ahead keeps its clock", func(t *testing.T) {
		g := newLinuxGuest(t)
		changed := newChangeSet()

		require.NoError(t, applyClock(Identity{ClockUnixNano: now.Add(-time.Hour).UnixNano()}, changed))

		assert.Empty(t, g.clocks, "moving a clock backwards breaks every timer a workload holds")
		assert.Empty(t, changed.sorted())
	})

	t.Run("a guest that is behind by less than the tolerance keeps its clock", func(t *testing.T) {
		g := newLinuxGuest(t)

		require.NoError(t, applyClock(Identity{ClockUnixNano: now.Add(-time.Second).UnixNano()}, newChangeSet()))

		assert.Empty(t, g.clocks, "the node's instant and the guest's clock are never exactly equal")
	})

	t.Run("an identity that names no instant leaves the clock alone", func(t *testing.T) {
		g := newLinuxGuest(t)

		require.NoError(t, applyClock(Identity{}, newChangeSet()))

		assert.Empty(t, g.clocks)
	})

	t.Run("a kernel that refuses the new time stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.clockErr = unix.EPERM

		err := applyClock(Identity{ClockUnixNano: now.Add(time.Hour).UnixNano()}, newChangeSet())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "set the wall clock")
	})
}

// TestAddEntropyOS covers the buffer the ioctl is handed: a rand_pool_info, which is two
// native ints followed by the bytes.
func TestAddEntropyOS(t *testing.T) {
	t.Run("the entropy is credited and passed through", func(t *testing.T) {
		g := newLinuxGuest(t)

		require.NoError(t, addEntropyOS([]byte{0x0f, 0x1e, 0x2d, 0x3c}))

		require.Len(t, g.mixed, 1)
		assert.Equal(t, uint32(32), binary.LittleEndian.Uint32(g.mixed[0][0:4]))
		assert.Equal(t, uint32(4), binary.LittleEndian.Uint32(g.mixed[0][4:8]))
		assert.Equal(t, []byte{0x0f, 0x1e, 0x2d, 0x3c}, g.mixed[0][8:])
	})

	t.Run("a kernel that refuses the entropy stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.mixErr = unix.EPERM

		err := addEntropyOS([]byte{0x0f})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "mix 1 bytes into the entropy pool")
	})

	t.Run("an entropy device that cannot be opened stops the hook", func(t *testing.T) {
		g := newLinuxGuest(t)
		previous := entropyDevice
		entropyDevice = g.path("etc") // a directory is not a character device
		t.Cleanup(func() { entropyDevice = previous })

		err := addEntropyOS([]byte{0x0f})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "open")
	})
}

// TestChangeSet covers the report's list of changed parts.
func TestChangeSet(t *testing.T) {
	changed := newChangeSet()
	changed.mark(PartRoutes)
	changed.mark(PartMAC)
	changed.mark(PartMAC)

	assert.Equal(t, []string{PartMAC, PartRoutes}, changed.sorted(),
		"a part that changed is reported once, in a stable order")
	assert.Empty(t, newChangeSet().sorted())
	while := newChangeSet()
	while.mark(PartClock)
	while.mark(PartClock)
	assert.Equal(t, []string{PartClock}, while.sorted())
}

// TestObserveIdentity covers the report: what the node holds the guest to, read back
// from the guest rather than from the request that was sent.
func TestObserveIdentity(t *testing.T) {
	t.Run("the report is the guest's own answer", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.attrs.HardwareAddr = mustHardwareAddr(t, "06:00:ac:10:00:0b")
		g.addrs = []netlink.Addr{guestAddr(t, "10.97.0.11/24"), guestAddr(t, "169.254.1.1/16")}
		g.routes = []netlink.Route{
			defaultRoute(7, "10.97.0.1"),
			directRoute(t, 7, "10.97.0.0/24", "10.97.0.11"),
		}
		g.hostname = "sbx-clb"
		g.write("etc/machine-id", "94be7b572fcb5fa21b64229d0c8facee\n", 0o444)
		g.write("etc/resolv.conf", "nameserver 10.97.0.1\nnameserver 10.97.0.2\nsearch internal\n", 0o644)

		report, err := observeIdentity(g.link())

		require.NoError(t, err)
		assert.Equal(t, "eth0", report.Interface)
		assert.Equal(t, "06:00:ac:10:00:0b", report.MAC)
		assert.Equal(t, []string{"10.97.0.11/24", "169.254.1.1/16"}, report.Addresses)
		assert.Equal(t, []string{"default via 10.97.0.1 dev eth0", "10.97.0.0/24 dev eth0 src 10.97.0.11 scope link"}, report.Routes)
		assert.Equal(t, "sbx-clb", report.Hostname)
		assert.Equal(t, "94be7b572fcb5fa21b64229d0c8facee", report.MachineID)
		assert.Equal(t, []string{"10.97.0.1", "10.97.0.2"}, report.DNS)
		assert.NotZero(t, report.ClockUnixNano)
	})

	t.Run("an interface with no MAC reports none rather than an empty string", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.attrs.HardwareAddr = nil

		report, err := observeIdentity(g.link())

		require.NoError(t, err)
		assert.Equal(t, "", report.MAC)
	})

	t.Run("an interface that cannot be read back is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.linkErr = unix.ENODEV

		_, err := observeIdentity(g.link())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read back eth0")
	})

	t.Run("an interface whose addresses cannot be listed is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.addrListErr = unix.ENODEV

		_, err := observeIdentity(g.link())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "list the addresses of eth0")
	})

	t.Run("an interface whose routes cannot be listed is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.routeListErr = unix.ENODEV

		_, err := observeIdentity(g.link())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "list the routes of eth0")
	})

	t.Run("a host name that cannot be read is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.hostnameErr = unix.EIO

		_, err := observeIdentity(g.link())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the host name")
	})

	t.Run("a machine id that cannot be read is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/machine-id/keep", "a directory where the file goes", 0o644)

		_, err := observeIdentity(g.link())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the machine id")
	})

	t.Run("a resolver configuration that cannot be read is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/resolv.conf/keep", "a directory where the file goes", 0o644)

		_, err := observeIdentity(g.link())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read")
	})
}

// TestReachMetadataServiceOS covers the path the identity is read over: the interface is
// up, the metadata address is routed, and an interface with no address of its own gets
// one for the length of the read.
func TestReachMetadataServiceOS(t *testing.T) {
	t.Run("an interface with an address is brought up and given the metadata route", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.addrs = []netlink.Addr{guestAddr(t, "10.97.0.10/24")}

		require.NoError(t, reachMetadataServiceOS("eth0"))

		assert.Equal(t, 1, g.setUps)
		assert.Empty(t, g.replacedAddrs, "the interface already carries an address")
		require.Len(t, g.replacedRoutes, 1)
		assert.Equal(t, "169.254.169.254/32", g.replacedRoutes[0].Dst.String())
		assert.Equal(t, netlink.SCOPE_LINK, g.replacedRoutes[0].Scope)
	})

	t.Run("an interface with no address gets a link-local one for the read", func(t *testing.T) {
		g := newLinuxGuest(t)

		require.NoError(t, reachMetadataServiceOS("eth0"))

		assert.Equal(t, []string{identityBootstrapAddress}, g.replacedAddrs,
			"the metadata service answers an address it is routed to, and its reply needs a source")
	})

	t.Run("an interface the guest does not have is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.linkErr = unix.ENODEV

		err := reachMetadataServiceOS("eth0")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "interface eth0")
	})

	t.Run("an interface that cannot be brought up is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.setUpErr = unix.EPERM

		err := reachMetadataServiceOS("eth0")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "bring eth0 up")
	})

	t.Run("an interface whose addresses cannot be listed is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.addrListErr = unix.ENODEV

		err := reachMetadataServiceOS("eth0")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "list the addresses of eth0")
	})

	t.Run("an interface that cannot be given the bootstrap address is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.addrReplaceErr = unix.EPERM

		err := reachMetadataServiceOS("eth0")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "give eth0 the bootstrap address "+identityBootstrapAddress)
	})

	t.Run("a metadata route that cannot be installed is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.routeReplaceErr = unix.EPERM

		err := reachMetadataServiceOS("eth0")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "route 169.254.169.254 to the metadata service through eth0")
	})
}

// The clone these tests restore, and the guest they restore onto.
const (
	hookCloneToken = "5216d9b54bb3f343bc34c2b3167b65ecd73cf3359ff402ae1b9212367e98b2fc"
	hookCloneMID   = "94be7b572fcb5fa21b64229d0c8facee"
)

// hookCloneIdentity is the identity a node publishes for one clone of a checkpoint.
func hookCloneIdentity() Identity {
	return Identity{
		Interface:     "eth0",
		MAC:           "06:00:ac:10:00:0b",
		Address:       "10.97.0.11/24",
		Gateway:       "10.97.0.1",
		DNS:           []string{"10.97.0.1"},
		Hostname:      "sbx-clb",
		MachineID:     hookCloneMID,
		Token:         hookCloneToken,
		Entropy:       "0f1e2d3c",
		ClockUnixNano: time.Now().Add(time.Hour).UnixNano(),
		HostKeys:      true,
	}
}

// restoredGuest is a guest that has just been resumed from a checkpoint taken from another
// machine: the source's MAC, address, gateway, resolver, machine id, host keys, token and
// clock.
func restoredGuest(t *testing.T) *linuxGuest {
	t.Helper()

	g := newLinuxGuest(t)
	g.attrs.HardwareAddr = mustHardwareAddr(t, "06:00:ac:10:00:0a")
	g.addrs = []netlink.Addr{guestAddr(t, "10.97.0.10/24")}
	g.routes = []netlink.Route{defaultRoute(7, "10.97.9.254")}
	g.hostname = "sbx-src"
	g.mixed = nil
	g.write("etc/machine-id", "cbe8e88684ba6b78d497c13e41e3ca17\n", 0o444)
	g.write("etc/ssh/ssh_host_ed25519_key", "the source's key", 0o600)
	g.write("etc/resolv.conf", "search us-east-2.compute.internal\nnameserver 172.31.0.2\n", 0o644)
	g.write("etc/swarmcracker/agent-token", "the source's token\n", 0o400)
	g.installKeygen("")
	return g
}

// TestApplyIdentityOS covers the whole applier: the order it works in, what it reports,
// and that a guest which cannot carry the identity is not handed over wearing a stale
// one.
func TestApplyIdentityOS(t *testing.T) {
	clone := hookCloneIdentity
	restored := restoredGuest

	t.Run("the guest ends up carrying the identity in every part", func(t *testing.T) {
		g := restored(t)
		id := clone()

		report, err := applyIdentityOS(context.Background(), id)

		require.NoError(t, err)
		assert.Equal(t, id.MAC, report.MAC)
		assert.Equal(t, []string{id.Address}, report.Addresses,
			"the source's address went with the interface it belonged to")
		assert.Contains(t, report.Routes, "default via 10.97.0.1 dev eth0")
		assert.NotContains(t, report.Routes, "default via 10.97.9.254 dev eth0",
			"the checkpoint's gateway is not left in the table")
		assert.Equal(t, id.Hostname, report.Hostname)
		assert.Equal(t, id.MachineID, report.MachineID)
		assert.Equal(t, id.DNS, report.DNS)
		assert.True(t, report.TokenRotated)
		assert.Equal(t, []string{"ed25519", "rsa"}, report.HostKeys)
		assert.Equal(t, 32, report.EntropyBits)
		assert.NotZero(t, report.ClockUnixNano)

		assert.Equal(t, []string{
			PartAddress, PartClock, PartDNS, PartEntropy, PartHostKeys,
			PartHostname, PartMAC, PartMachineID, PartRoutes, PartToken,
		}, report.Changed)

		assert.Equal(t, hookCloneMID+"\n", g.contents("etc/machine-id"))
		assert.Equal(t, hookCloneToken+"\n", g.contents("etc/swarmcracker/agent-token"))
		assert.Equal(t, os.FileMode(0o400), g.mode("etc/swarmcracker/agent-token"),
			"a token another guest user could read is not a credential")
		assert.Equal(t, "-A\n", g.contents("keygen.argv"))
	})

	t.Run("a second run of the same identity changes only the boot identity", func(t *testing.T) {
		g := restored(t)
		// The identity a node publishes on every resume: it does not ask for rotation,
		// so the generation decides — the first run on this clone mints its machine
		// identity, and every run after it leaves the machine alone. (An identity that
		// does set HostKeys is honoured every time, which TestApplyMachineIdentity
		// covers: asking for new keys is what that flag is for.)
		id := clone()
		id.HostKeys = false
		_, err := applyIdentityOS(context.Background(), id)
		require.NoError(t, err)
		require.NoError(t, os.Chmod(agentTokenPath, 0o600))
		keygenRuns := g.contents("keygen.argv")

		report, err := applyIdentityOS(context.Background(), id)

		require.NoError(t, err)
		assert.Empty(t, report.Changed,
			"a hook that runs again with the identity the guest already has rewrites nothing")
		assert.False(t, report.TokenRotated)
		assert.Equal(t, os.FileMode(0o600), g.mode("etc/swarmcracker/agent-token"),
			"the token is not written again")
		assert.Equal(t, keygenRuns, g.contents("keygen.argv"), "and the host keys are not made again")
	})

	t.Run("a guest that cannot take the routes fails instead of keeping the source's", func(t *testing.T) {
		g := restored(t)
		g.routeReplaceErr = unix.ENETUNREACH

		report, err := applyIdentityOS(context.Background(), clone())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "set the default route via 10.97.0.1")
		assert.ErrorIs(t, err, unix.ENETUNREACH)
		assert.Equal(t, PrepareReport{}, report,
			"a failed hook reports nothing: the caller must not read a half-applied identity out of it")
	})

	t.Run("a metadata route that cannot be restored fails the launch", func(t *testing.T) {
		g := restored(t)
		g.routeReplaceErr = unix.EPERM
		id := clone()
		id.Gateway = ""
		id.Routes = nil

		_, err := applyIdentityOS(context.Background(), id)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "route 169.254.169.254 to the metadata service through eth0",
			"the address change can take the metadata route with it, and a guest that cannot read its identity twice cannot be prepared twice")
	})

	t.Run("an interface the guest does not have fails the launch", func(t *testing.T) {
		g := restored(t)
		g.linkErr = unix.ENODEV

		_, err := applyIdentityOS(context.Background(), clone())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "interface eth0")
	})

	t.Run("an interface that cannot be brought up fails the launch", func(t *testing.T) {
		g := restored(t)
		g.setUpErr = unix.EPERM

		_, err := applyIdentityOS(context.Background(), clone())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "bring eth0 up")
	})

	t.Run("a clock that cannot be set fails the launch", func(t *testing.T) {
		g := restored(t)
		g.clockErr = unix.EPERM

		_, err := applyIdentityOS(context.Background(), clone())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "set the wall clock")
	})

	t.Run("a token that cannot be rotated fails the launch", func(t *testing.T) {
		g := restored(t)
		// A directory where the token file goes: the applier can read it (the read is
		// only a shortcut for "already the same") and cannot install over it, which is
		// the case a node has to hear about rather than resume with the checkpoint's
		// credential.
		require.NoError(t, os.Remove(agentTokenPath))
		require.NoError(t, os.Mkdir(agentTokenPath, 0o700))

		_, err := applyIdentityOS(context.Background(), clone())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "install")
		info, err := os.Stat(g.path("etc/swarmcracker/agent-token"))
		require.NoError(t, err)
		assert.True(t, info.IsDir(),
			"the token the applier could not install is not reported as rotated, and the launch failed with it")
	})

	t.Run("host keys that cannot be regenerated fail the launch", func(t *testing.T) {
		g := restored(t)
		g.installKeygen("exit 1")

		_, err := applyIdentityOS(context.Background(), clone())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "generate SSH host keys")
	})
}

// TestPrepareFiles covers the small files the applier keeps its state in.
func TestPrepareFiles(t *testing.T) {
	t.Run("a machine id that is not there reads as empty", func(t *testing.T) {
		newLinuxGuest(t)

		mid, err := readMachineID()

		require.NoError(t, err)
		assert.Equal(t, "", mid, "a template with no machine id is not a failure")
	})

	t.Run("a machine id is read without its newline", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/machine-id", "94be7b572fcb5fa21b64229d0c8facee\n", 0o444)

		mid, err := readMachineID()

		require.NoError(t, err)
		assert.Equal(t, "94be7b572fcb5fa21b64229d0c8facee", mid)
	})

	t.Run("a machine id that cannot be read is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/machine-id/keep", "a directory where the file goes", 0o644)

		_, err := readMachineID()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the machine id")
	})

	t.Run("the nameservers are read out of the resolver configuration", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/resolv.conf", "search internal\nnameserver 10.97.0.1\noptions ndots:5\nnameserver 10.97.0.2 # a comment\n", 0o644)

		servers, err := readNameservers()

		require.NoError(t, err)
		assert.Equal(t, []string{"10.97.0.1"}, servers,
			"a nameserver line is `nameserver <address>` and nothing else; what this reads back is the file the applier wrote")
	})

	t.Run("a guest with no resolver configuration has no nameservers", func(t *testing.T) {
		newLinuxGuest(t)

		servers, err := readNameservers()

		require.NoError(t, err)
		assert.Empty(t, servers)
	})

	t.Run("a resolver configuration that cannot be read is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/resolv.conf/keep", "a directory where the file goes", 0o644)

		_, err := readNameservers()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read")
	})

	t.Run("a guest that has not been prepared has no generation", func(t *testing.T) {
		newLinuxGuest(t)

		generation, err := readPreparedGeneration()

		require.NoError(t, err)
		assert.Equal(t, "", generation)
	})

	t.Run("the generation stamp is written where the next run reads it", func(t *testing.T) {
		g := newLinuxGuest(t)

		require.NoError(t, writePreparedGeneration("0123456789abcdef"))

		assert.Equal(t, "0123456789abcdef\n", g.contents("var/lib/swarmcracker/prepared"))
		generation, err := readPreparedGeneration()
		require.NoError(t, err)
		assert.Equal(t, "0123456789abcdef", generation)
	})

	t.Run("a generation that cannot be written is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("var/lib/swarmcracker", "a file where the directory has to be", 0o644)

		err := writePreparedGeneration("0123456789abcdef")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "create")
	})
}

// TestWriteFileAtomic covers the write itself: content, mode, and the rule that a reader
// never sees half a file.
func TestWriteFileAtomic(t *testing.T) {
	t.Run("the content and the mode are the caller's", func(t *testing.T) {
		g := newLinuxGuest(t)

		require.NoError(t, writeFileAtomic(g.path("etc/agent-token"), []byte("a token"), 0o400))

		assert.Equal(t, "a token", g.contents("etc/agent-token"))
		assert.Equal(t, os.FileMode(0o400), g.mode("etc/agent-token"))
	})

	t.Run("the directories on the way are created", func(t *testing.T) {
		g := newLinuxGuest(t)

		require.NoError(t, writeFileAtomic(g.path("var/lib/swarmcracker/prepared"), []byte("x"), 0o644))

		assert.True(t, g.exists("var/lib/swarmcracker/prepared"))
	})

	t.Run("an existing file is replaced, not appended to", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc/resolv.conf", "nameserver 172.31.0.2\n", 0o644)

		require.NoError(t, writeFileAtomic(g.path("etc/resolv.conf"), []byte("nameserver 10.97.0.1\n"), 0o644))

		assert.Equal(t, "nameserver 10.97.0.1\n", g.contents("etc/resolv.conf"))
	})

	t.Run("no staging file is left behind", func(t *testing.T) {
		g := newLinuxGuest(t)

		require.NoError(t, writeFileAtomic(g.path("etc/resolv.conf"), []byte("nameserver 10.97.0.1\n"), 0o644))

		entries, err := os.ReadDir(g.path("etc"))
		require.NoError(t, err)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		assert.Equal(t, []string{"resolv.conf"}, names)
	})

	t.Run("a path whose directory is a file is a failure", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.write("etc", "a file where a directory has to be", 0o644)

		err := writeFileAtomic(g.path("etc/resolv.conf"), []byte("x"), 0o644)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "create")
	})
}

// TestIdentityHelpers covers the small decisions the applier makes about what a route or
// an address is, which is where a wrong answer is a wrong kernel call.
func TestIdentityHelpers(t *testing.T) {
	t.Run("an address is optional and has to be CIDR", func(t *testing.T) {
		empty, err := parseOptionalAddress("")
		require.NoError(t, err)
		assert.Nil(t, empty, "an identity that says nothing about the address says nothing")

		addr, err := parseOptionalAddress("10.97.0.11/24")
		require.NoError(t, err)
		assert.Equal(t, "10.97.0.11/24", addr.IPNet.String())

		_, err = parseOptionalAddress("10.97.0.11")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `identity address "10.97.0.11"`)
	})

	t.Run("two addresses are the same address on the same network", func(t *testing.T) {
		assert.True(t, sameAddress(guestAddr(t, "10.97.0.11/24"), guestAddr(t, "10.97.0.11/24")))
		assert.False(t, sameAddress(guestAddr(t, "10.97.0.11/24"), guestAddr(t, "10.97.0.12/24")))
		assert.False(t, sameAddress(guestAddr(t, "10.97.0.11/24"), guestAddr(t, "10.97.0.11/16")),
			"the same address on another network is another address")
		assert.False(t, sameAddress(netlink.Addr{}, guestAddr(t, "10.97.0.11/24")),
			"an address with no network is not an address")
	})

	t.Run("an address can be looked for in a list", func(t *testing.T) {
		have := []netlink.Addr{guestAddr(t, "10.97.0.10/24"), guestAddr(t, "10.97.0.11/24")}

		assert.True(t, anyAddress(have, guestAddr(t, "10.97.0.11/24")))
		assert.False(t, anyAddress(have, guestAddr(t, "10.97.0.12/24")))
		assert.False(t, anyAddress(nil, guestAddr(t, "10.97.0.11/24")))
	})

	t.Run("a default route is a default route however it is spelled", func(t *testing.T) {
		assert.True(t, isDefaultRoute(netlink.Route{Dst: nil}), "a caller asks for the default route with no destination")
		assert.True(t, isDefaultRoute(defaultRoute(7, "10.97.0.1")), "the kernel reports it as 0.0.0.0/0")
		assert.False(t, isDefaultRoute(directRoute(t, 7, "10.97.0.0/24", "10.97.0.10")))
	})

	t.Run("a route is looked for by what it means, not by how it is written", func(t *testing.T) {
		kernel := []netlink.Route{defaultRoute(7, "10.97.0.1"), directRoute(t, 7, "10.97.0.0/24", "10.97.0.10")}

		assert.True(t, anyRoute(kernel, &netlink.Route{Gw: net.ParseIP("10.97.0.1").To4()}),
			"the caller asks for the default route with a nil destination and the kernel has 0.0.0.0/0")
		assert.False(t, anyRoute(kernel, &netlink.Route{Gw: net.ParseIP("10.97.9.254").To4()}))
		assert.True(t, anyRoute(kernel, &netlink.Route{Dst: mustCIDR(t, "10.97.0.0/24")}))
		assert.False(t, anyRoute(kernel, &netlink.Route{Dst: mustCIDR(t, "10.99.0.0/24")}))
		assert.False(t, anyRoute(nil, &netlink.Route{Gw: net.ParseIP("10.97.0.1").To4()}))
	})

	t.Run("two destinations are the same destination when both are nothing", func(t *testing.T) {
		assert.True(t, sameDestination(nil, nil))
		assert.False(t, sameDestination(nil, mustCIDR(t, "10.97.0.0/24")))
		assert.False(t, sameDestination(mustCIDR(t, "10.97.0.0/24"), nil))
		assert.True(t, sameDestination(mustCIDR(t, "10.97.0.0/24"), mustCIDR(t, "10.97.0.0/24")))
	})

	t.Run("a route reads the way an operator expects", func(t *testing.T) {
		newLinuxGuest(t) // the device a route names is the interface, so the guest has to exist

		assert.Equal(t, "default", formatRoute(netlink.Route{}))
		assert.Equal(t, "10.99.0.0/24", formatRoute(netlink.Route{Dst: mustCIDR(t, "10.99.0.0/24")}))
		assert.Equal(t, "default via 10.97.0.1 dev eth0", formatRoute(defaultRoute(7, "10.97.0.1")))
		assert.Equal(t, "10.97.0.0/24 dev eth0 src 10.97.0.10 scope link",
			formatRoute(directRoute(t, 7, "10.97.0.0/24", "10.97.0.10")))
		assert.Equal(t, "default via 10.97.0.1",
			formatRoute(netlink.Route{Gw: net.ParseIP("10.97.0.1").To4()}),
			"a route with no interface index names no device")
	})

	t.Run("an interface whose addresses cannot be listed is an error, not an empty list", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.addrListErr = unix.ENODEV

		_, err := ipv4Addresses(g.link())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "list the addresses of eth0")
	})

	t.Run("an interface whose routes cannot be listed is an error, not an empty list", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.routeListErr = unix.ENODEV

		_, err := ipv4Routes(g.link())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "list the routes of eth0")
	})

	t.Run("the metadata route is a link-scoped route to one address", func(t *testing.T) {
		g := newLinuxGuest(t)
		require.NoError(t, ensureMetadataRoute(g.link()))

		require.Len(t, g.replacedRoutes, 1)
		assert.Equal(t, "169.254.169.254/32", g.replacedRoutes[0].Dst.String())
		assert.Equal(t, netlink.SCOPE_LINK, g.replacedRoutes[0].Scope)
		assert.Equal(t, 7, g.replacedRoutes[0].LinkIndex)
	})

	t.Run("a metadata route that cannot be installed is an error", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.routeReplaceErr = unix.EPERM

		err := ensureMetadataRoute(g.link())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "route 169.254.169.254 to the metadata service through eth0")
	})
}

// TestSetClockOS covers the syscall the clock decision ends in.
func TestSetClockOS(t *testing.T) {
	t.Run("the instant is passed to the kernel", func(t *testing.T) {
		g := newLinuxGuest(t)
		want := time.Unix(1794072710, 0).UnixNano()

		require.NoError(t, setClockOS(want))

		assert.Equal(t, []int64{want}, g.clocks)
	})

	t.Run("a kernel that refuses the time is an error", func(t *testing.T) {
		g := newLinuxGuest(t)
		g.clockErr = unix.EPERM

		err := setClockOS(time.Now().UnixNano())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "set the wall clock")
	})
}

// TestApplyIdentityOSFailsTheLaunchInEveryStep proves the hook is all-or-nothing. A step
// that cannot be carried out fails the launch whatever the step is, because a guest that
// applied half an identity is a machine that is neither its template's nor its own — and
// the caller gets no report to mistake for success.
func TestApplyIdentityOSFailsTheLaunchInEveryStep(t *testing.T) {
	each := []struct {
		name    string
		broken  func(*linuxGuest)
		wantErr string
	}{
		{
			name:    "a MAC the kernel refuses",
			broken:  func(g *linuxGuest) { g.macErr = unix.EPERM },
			wantErr: "give eth0 the mac 06:00:ac:10:00:0b",
		},
		{
			name:    "an address the kernel refuses",
			broken:  func(g *linuxGuest) { g.addrReplaceErr = unix.EPERM },
			wantErr: "give eth0 the address 10.97.0.11/24",
		},
		{
			name: "a resolver configuration that cannot be read",
			broken: func(g *linuxGuest) {
				g.write("blocked", "a file where a directory has to be", 0o644)
				resolvConfPath = g.path("blocked/resolv.conf")
			},
			wantErr: "read",
		},
		{
			name:    "a host name the kernel refuses",
			broken:  func(g *linuxGuest) { g.setHostnameErr = unix.EPERM },
			wantErr: `set the host name to "sbx-clb"`,
		},
		{
			name: "a machine id that cannot be written",
			broken: func(g *linuxGuest) {
				require.NoError(t, os.Remove(machineIDPath))
				require.NoError(t, os.Mkdir(machineIDPath, 0o755))
			},
			wantErr: "read the machine id",
		},
		{
			name:    "entropy the kernel will not take",
			broken:  func(g *linuxGuest) { g.mixErr = unix.EPERM },
			wantErr: "bytes into the entropy pool",
		},
	}

	for _, tc := range each {
		t.Run(tc.name, func(t *testing.T) {
			g := restoredGuest(t)
			tc.broken(g)

			report, err := applyIdentityOS(context.Background(), hookCloneIdentity())

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Equal(t, PrepareReport{}, report,
				"a failed hook reports nothing the caller could read as an adopted identity")
		})
	}
}
