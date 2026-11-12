//go:build !integration

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The IPv4 route table below is /proc/net/route's own shape: the addresses are the
// 32-bit values in host byte order, so "0000000A" is 10.0.0.0 and not 0.0.0.10.
// Every address here is written so that reading it as network order gives a
// different, still valid address, which is what makes a byte-order mistake visible.
const fixtureIPv4Routes = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
eth0	00000000	0102000A	0003	0	0	100	00000000	0	0	0
eth0	0000000A	00000000	0001	0	0	0	00FFFFFF	0	0	0
eth0	0000A8C0	00000000	0001	0	0	0	0000FFFF	0	0	0
eth0	00000000	00000000	0203	0	0	0	00000000	0	0	0
lo	0100007F	00000000	0001	0	0	0	000000FF	0	0	0
`

// The IPv6 table is /proc/net/ipv6_route's shape: 32 hex digits per address,
// network order, a hex prefix length and a hex metric, then the device. The last
// entry is the kernel's rejected default, which is not a path.
const fixtureIPv6Routes = `20010db8000000000000000000000001 80 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000001 00000000 00000001 eth0
20010db8000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000001 00000000 00000001 eth0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000100 00000001 00000000 00000003 eth0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo
`

func writeRouteFixture(t *testing.T, dir, name, body string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

// TestReadIPv4RoutesReadsHostByteOrderAddresses is the pair of claims the parser
// exists for: an address and its mask are read in the kernel's byte order, and the
// entry's device and metric come with them.
func TestReadIPv4RoutesReadsHostByteOrderAddresses(t *testing.T) {
	path := writeRouteFixture(t, t.TempDir(), "route", fixtureIPv4Routes)

	routes, err := readIPv4Routes(path)
	require.NoError(t, err)

	// The rejected default (flags 0x0203) is not a path, so it is not in the table.
	require.Len(t, routes, 4)

	assert.Equal(t, NetRoute{
		Family: "ipv4", Destination: "0.0.0.0/0", Gateway: "10.0.2.1", Device: "eth0", Metric: 100,
	}, routes[0])
	assert.Equal(t, NetRoute{
		Family: "ipv4", Destination: "10.0.0.0/24", Device: "eth0", Metric: 0,
	}, routes[1])
	assert.Equal(t, NetRoute{
		Family: "ipv4", Destination: "192.168.0.0/16", Device: "eth0", Metric: 0,
	}, routes[2])
	assert.Equal(t, NetRoute{
		Family: "ipv4", Destination: "127.0.0.1/8", Device: "lo", Metric: 0,
	}, routes[3])
}

// TestReadIPv6RoutesReadsHexPrefixAndMetric pins the columns an IPv6 entry is made
// of, including that the prefix length and the metric are hex, and that the
// kernel's rejected default is left out of a table of paths.
func TestReadIPv6RoutesReadsHexPrefixAndMetric(t *testing.T) {
	path := writeRouteFixture(t, t.TempDir(), "ipv6_route", fixtureIPv6Routes)

	routes, err := readIPv6Routes(path)
	require.NoError(t, err)
	require.Len(t, routes, 3, "the rejected default is not a path")

	assert.Equal(t, NetRoute{
		Family: "ipv6", Destination: "2001:db8::1/128", Device: "eth0", Metric: 1024,
	}, routes[0])
	assert.Equal(t, NetRoute{
		Family: "ipv6", Destination: "2001:db8::/64", Device: "eth0", Metric: 1024,
	}, routes[1])
	assert.Equal(t, NetRoute{
		Family: "ipv6", Destination: "::/0", Gateway: "fe80::1", Device: "eth0", Metric: 256,
	}, routes[2])
}

// TestReadResolvConfReadsNameserversAndSearch covers the two directives the report
// carries, and that a comment is not configuration.
func TestReadResolvConfReadsNameserversAndSearch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resolv.conf")
	body := "# managed by the node\n" +
		"nameserver 10.64.0.1\n" +
		"nameserver 10.64.0.2 ; a stale second resolver\n" +
		"search svc.local example.internal\n" +
		"options ndots:1\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	resolvers, search := readResolvConf(path)
	assert.Equal(t, []string{"10.64.0.1", "10.64.0.2"}, resolvers)
	assert.Equal(t, []string{"svc.local", "example.internal"}, search)
}

// TestReadResolvConfWithoutAFileIsEmpty proves a template with no resolver is
// reported as one rather than failing: the guest is a normal guest, it just
// resolves nothing.
func TestReadResolvConfWithoutAFileIsEmpty(t *testing.T) {
	resolvers, search := readResolvConf(filepath.Join(t.TempDir(), "absent"))
	assert.Empty(t, resolvers)
	assert.Empty(t, search)
}

// TestReadGuestNetIsSortedAndComplete reads this host's own tables, which is the
// only place the readers' agreement with the kernel can be observed without a
// guest: every interface it lists is one the kernel lists, and the routes are
// ordered the way the report promises.
func TestReadGuestNetIsSortedAndComplete(t *testing.T) {
	report, err := readGuestNet()
	require.NoError(t, err)

	require.NotEmpty(t, report.Interfaces, "a host with no interfaces at all is not a host")
	for i, iface := range report.Interfaces {
		assert.NotEmpty(t, iface.Name)
		if i > 0 {
			assert.Less(t, report.Interfaces[i-1].Index, iface.Index, "interfaces are in index order")
		}
	}
	// The route tables are read from the real /proc here, so their content is this
	// host's; what is checked is the report's own contract.
	for i, route := range report.Routes {
		assert.Contains(t, []string{"ipv4", "ipv6"}, route.Family)
		assert.NotEmpty(t, route.Destination)
		if i > 0 {
			previous := report.Routes[i-1]
			assert.LessOrEqual(t, previous.Family, route.Family, "routes are grouped by family")
		}
	}
}
