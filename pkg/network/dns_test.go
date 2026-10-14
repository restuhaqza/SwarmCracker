//go:build !integration

package network

import (
	"os"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dnsTestManager builds a manager for a bridge name no real dnsmasq uses, so the
// /proc scan in ProgramDNS finds nothing and the tests never signal a real
// process.
func dnsTestManager() *NetworkManager {
	return NewNetworkManager(types.NetworkConfig{
		BridgeName: "sc-dnsbr0",
		BridgeIP:   "192.168.127.1/24",
		Subnet:     "192.168.127.0/24",
	}).(*NetworkManager)
}

func TestRenderDNSRecords(t *testing.T) {
	tests := []struct {
		name string
		in   []DNSRecord
		want string
	}{
		{"no records", nil, ""},
		{
			"one line per address, sorted and deduplicated",
			[]DNSRecord{
				{Name: "web", IPs: []string{"192.168.127.6", "192.168.127.5", "192.168.127.6"}},
				{Name: "tasks.web", IPs: []string{"192.168.127.5"}},
			},
			"192.168.127.5\ttasks.web\n192.168.127.5\tweb\n192.168.127.6\tweb\n",
		},
		{
			"names that could inject a hosts entry are dropped",
			[]DNSRecord{
				{Name: "bad name", IPs: []string{"10.0.0.1"}},
				{Name: "web\naddress=/evil/1.2.3.4", IPs: []string{"10.0.0.1"}},
				{Name: "ok", IPs: []string{"not-an-ip", "10.0.0.2"}},
			},
			"10.0.0.2\tok\n",
		},
		{"a name with no usable address emits nothing", []DNSRecord{{Name: "web"}}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, renderDNSRecords(tt.in))
		})
	}
}

// TestProgramDNS_WritesAndIsIdempotent covers the reconcile contract: the first
// call writes the rendered records, an unchanged desired set writes nothing (so
// the reconciler can run on a timer), a change rewrites, and clearing the set
// empties the file instead of leaving stale names behind.
func TestProgramDNS_WritesAndIsIdempotent(t *testing.T) {
	nm := dnsTestManager()
	path := nm.dnsRecordsPath()
	t.Cleanup(func() { _ = os.Remove(path) })

	writes := 0
	orig := osWriteFile
	osWriteFile = func(p string, data []byte, perm os.FileMode) error {
		writes++
		return orig(p, data, perm)
	}
	defer func() { osWriteFile = orig }()

	records := []DNSRecord{{Name: "web", IPs: []string{"192.168.127.5"}}}
	require.NoError(t, nm.ProgramDNS(records))
	require.Equal(t, 1, writes)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "192.168.127.5\tweb\n", string(data))

	require.NoError(t, nm.ProgramDNS(records))
	assert.Equal(t, 1, writes, "an unchanged reconcile must not rewrite the file")

	require.NoError(t, nm.ProgramDNS([]DNSRecord{{Name: "web", IPs: []string{"192.168.127.5", "192.168.127.6"}}}))
	assert.Equal(t, 2, writes)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "192.168.127.5\tweb\n192.168.127.6\tweb\n", string(data))

	require.NoError(t, nm.ProgramDNS(nil))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Empty(t, string(data), "a cleared record set leaves no stale names")
}

// TestProgramDNS_RewritesAfterExternalTruncation covers the dnsmasq replacement
// path: setupDHCP truncates the hosts file before starting a new server, so a
// cached record set must not stop the reconciler from writing the file again.
func TestProgramDNS_RewritesAfterExternalTruncation(t *testing.T) {
	nm := dnsTestManager()
	path := nm.dnsRecordsPath()
	t.Cleanup(func() { _ = os.Remove(path) })

	records := []DNSRecord{{Name: "web", IPs: []string{"192.168.127.5"}}}
	require.NoError(t, nm.ProgramDNS(records))

	// A new dnsmasq is started with an empty hosts file.
	require.NoError(t, os.WriteFile(path, []byte{}, 0644))

	require.NoError(t, nm.ProgramDNS(records))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "192.168.127.5\tweb\n", string(data),
		"the records must be restored after the file is reset underneath us")
}

// TestProgramDNS_WriteFailureIsReported asserts a write error surfaces instead of
// being swallowed by the reconcile loop.
func TestProgramDNS_WriteFailureIsReported(t *testing.T) {
	nm := dnsTestManager()

	orig := osWriteFile
	osWriteFile = func(string, []byte, os.FileMode) error { return os.ErrPermission }
	defer func() { osWriteFile = orig }()

	err := nm.ProgramDNS([]DNSRecord{{Name: "web", IPs: []string{"192.168.127.5"}}})
	assert.Error(t, err)
	assert.False(t, nm.dnsProgrammed, "a failed write must not be remembered as programmed")
}

func TestBridgeGatewayIP(t *testing.T) {
	cases := map[string]string{
		"192.168.127.1/24": "192.168.127.1",
		"192.168.127.1":    "192.168.127.1",
		"10.10.0.1/16":     "10.10.0.1",
		"":                 "",
		"not-an-ip":        "",
	}
	for in, want := range cases {
		nm := &NetworkManager{config: types.NetworkConfig{BridgeIP: in}}
		assert.Equal(t, want, nm.bridgeGatewayIP(), "bridgeGatewayIP(%q)", in)
	}
}

func TestValidDNSName(t *testing.T) {
	for _, ok := range []string{"web", "tasks.web", "my-svc", "a_b", "svc1"} {
		assert.True(t, validDNSName(ok), "%q must be accepted", ok)
	}
	for _, bad := range []string{
		"", "has space", "web\naddress=/evil/1.2.3.4", "a/b", "web:", "web;", "ünicode",
	} {
		assert.False(t, validDNSName(bad), "%q must be rejected", bad)
	}
	assert.False(t, validDNSName(strings.Repeat("a", 254)), "an over-long name is rejected")
}

// TestDnsmasqServesRecords covers the upgrade path: an instance that cannot
// serve records must not be treated as reusable.
func TestDnsmasqServesRecords(t *testing.T) {
	assert.False(t, dnsmasqServesRecords(nil))
	assert.False(t, dnsmasqServesRecords([]string{"999999"}), "no such process")
	assert.False(t, dnsmasqServesRecords([]string{"1"}), "pid 1 is not a dnsmasq started with --addn-hosts")
}
