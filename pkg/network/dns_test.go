//go:build !integration

package network

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dnsTestManager builds a manager for a bridge name no real dnsmasq uses, so the
// /proc scan in ProgramDNS finds nothing and the tests never signal a real
// process.
func dnsTestManager(t *testing.T) *NetworkManager {
	t.Helper()
	nm := NewNetworkManager(types.NetworkConfig{
		BridgeName: "sc-dnsbr0",
		BridgeIP:   "192.168.127.1/24",
		Subnet:     "192.168.127.0/24",
	}).(*NetworkManager)
	// The records file lives in the runtime directory; a temp directory keeps the
	// test off the host's /run and is cleaned up with it.
	nm.runtimeDir = t.TempDir()
	return nm
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
	nm := dnsTestManager(t)
	path := nm.dnsRecordsPath()

	writes := 0
	orig := writeDnsmasqFile
	writeDnsmasqFile = func(p string, data []byte, perm os.FileMode) error {
		writes++
		return orig(p, data, perm)
	}
	defer func() { writeDnsmasqFile = orig }()

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
	nm := dnsTestManager(t)
	path := nm.dnsRecordsPath()

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

// TestProgramDNS_CreatesRuntimeDir covers the reconcile path on a node where no
// task has started yet (and where DHCP — and so setupDHCP — is disabled): the
// reconciler must create the runtime directory itself rather than fail every
// cycle because the records file has nowhere to live. Before #72 the file was in
// /tmp, which always exists, so nothing had to create it.
func TestProgramDNS_CreatesRuntimeDir(t *testing.T) {
	nm := dnsTestManager(t)
	nm.runtimeDir = filepath.Join(t.TempDir(), "not-created-yet")
	require.NoDirExists(t, nm.runtimeDir)

	require.NoError(t, nm.ProgramDNS([]DNSRecord{{Name: "web", IPs: []string{"10.0.0.5"}}}))

	assert.DirExists(t, nm.runtimeDir)
	data, err := os.ReadFile(nm.dnsRecordsPath())
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.5\tweb\n", string(data))
}

// TestProgramDNS_WriteFailureIsReported asserts a write error surfaces instead of
// being swallowed by the reconcile loop.
func TestProgramDNS_WriteFailureIsReported(t *testing.T) {
	nm := dnsTestManager(t)

	orig := writeDnsmasqFile
	writeDnsmasqFile = func(string, []byte, os.FileMode) error { return os.ErrPermission }
	defer func() { writeDnsmasqFile = orig }()

	err := nm.ProgramDNS([]DNSRecord{{Name: "web", IPs: []string{"192.168.127.5"}}})
	assert.Error(t, err)
	assert.False(t, nm.dnsProgrammed, "a failed write must not be remembered as programmed")
}

// TestPrepareNetwork_ResolverAnnotation covers the producer side of the guest
// resolver: a task is pointed at the bridge gateway only when a resolver is
// actually running on that bridge, and only when the task stays on the default
// bridge — a user-defined network has its own gateway and no resolver, so
// pointing a guest there would leave it with no DNS at all.
func TestPrepareNetwork_ResolverAnnotation(t *testing.T) {
	base := func() *NetworkManager {
		return NewNetworkManager(types.NetworkConfig{
			BridgeName: "testbr0",
			BridgeIP:   "192.168.127.1/24",
			Subnet:     "192.168.127.0/24",
		}).(*NetworkManager)
	}
	stubDnsmasq := func(pids []string) func() {
		orig := findDnsmasqPIDs
		findDnsmasqPIDs = func(string) []string { return pids }
		return func() { findDnsmasqPIDs = orig }
	}

	t.Run("a live resolver on the bridge is published", func(t *testing.T) {
		state := newMockState()
		restore := setupMocksForTest(state)
		defer restore()
		defer stubDnsmasq([]string{"4242"})()

		task := &types.Task{ID: "t1", Annotations: map[string]string{}}
		require.NoError(t, base().PrepareNetwork(context.Background(), task))
		assert.Equal(t, "192.168.127.1", task.Annotations[types.AnnotationResolver])
	})

	t.Run("no resolver, no annotation", func(t *testing.T) {
		state := newMockState()
		restore := setupMocksForTest(state)
		defer restore()
		defer stubDnsmasq(nil)()

		task := &types.Task{ID: "t1", Annotations: map[string]string{}}
		require.NoError(t, base().PrepareNetwork(context.Background(), task))
		assert.NotContains(t, task.Annotations, types.AnnotationResolver,
			"a guest must keep the image's resolver when nothing serves the bridge")
	})

	t.Run("a user-defined network is not pointed at the default resolver", func(t *testing.T) {
		state := newMockState()
		restore := setupMocksForTest(state)
		defer restore()
		defer stubDnsmasq([]string{"4242"})()

		task := &types.Task{
			ID:          "t1",
			Annotations: map[string]string{},
			Networks: []types.NetworkAttachment{{
				Network:   types.Network{ID: "n1", Spec: types.NetworkSpec{Subnet: "10.10.0.0/24", Gateway: "10.10.0.1"}},
				Addresses: []string{"10.10.0.17/24"},
			}},
		}
		require.NoError(t, base().PrepareNetwork(context.Background(), task))
		assert.NotContains(t, task.Annotations, types.AnnotationResolver,
			"a user-defined network has its own gateway and no resolver")
	})
}

// TestPrepareNetwork_ResolverAnnotationNilAnnotations asserts the annotation is
// added even when the task arrived without an annotation map.
func TestPrepareNetwork_ResolverAnnotationNilAnnotations(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	orig := findDnsmasqPIDs
	findDnsmasqPIDs = func(string) []string { return []string{"4242"} }
	defer func() { findDnsmasqPIDs = orig }()

	nm := NewNetworkManager(types.NetworkConfig{
		BridgeName: "testbr0",
		BridgeIP:   "192.168.127.1/24",
		Subnet:     "192.168.127.0/24",
	}).(*NetworkManager)

	task := &types.Task{ID: "t1"}
	require.NoError(t, nm.PrepareNetwork(context.Background(), task))
	assert.Equal(t, "192.168.127.1", task.Annotations[types.AnnotationResolver])
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
