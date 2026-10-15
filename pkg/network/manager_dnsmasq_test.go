package network

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDnsmasqArgvMatches(t *testing.T) {
	tests := []struct {
		name   string
		argv   []string
		bridge string
		want   bool
	}{
		{
			name:   "full path dnsmasq on bridge",
			argv:   []string{"/usr/sbin/dnsmasq", "--interface", "swarm-br0", "--bind-interfaces"},
			bridge: "swarm-br0",
			want:   true,
		},
		{
			name:   "different bridge",
			argv:   []string{"dnsmasq", "--interface", "other0"},
			bridge: "swarm-br0",
			want:   false,
		},
		{
			name:   "not dnsmasq",
			argv:   []string{"/usr/sbin/nginx", "--interface", "swarm-br0"},
			bridge: "swarm-br0",
			want:   false,
		},
		{name: "empty argv", argv: nil, bridge: "swarm-br0", want: false},
		{
			name:   "interface flag without value",
			argv:   []string{"dnsmasq", "--interface"},
			bridge: "swarm-br0",
			want:   false,
		},
		{
			name:   "interface=value form is not matched",
			argv:   []string{"dnsmasq", "--interface=swarm-br0"},
			bridge: "swarm-br0",
			want:   false,
		},
		{
			name:   "substring bridge name is not matched",
			argv:   []string{"dnsmasq", "--interface", "swarm-br0-extra"},
			bridge: "swarm-br0",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, dnsmasqArgvMatches(tt.argv, tt.bridge))
		})
	}
}

func TestLiveDnsmasqPID_NoFile(t *testing.T) {
	nm := &NetworkManager{
		config:     types.NetworkConfig{BridgeName: "no-such-bridge-xyz"},
		runtimeDir: t.TempDir(),
	}
	assert.Equal(t, "", nm.liveDnsmasqPID("no-such-bridge-xyz"))
}

// stubDnsmasqArgv makes one PID look like a dnsmasq bound to bridge, so a test
// can exercise the reuse and cleanup paths without spawning a process that looks
// like dnsmasq. Every other PID keeps its real argv, so the /proc scan still sees
// what is really running. The validation itself is covered against a real process
// in dnsmasq_runtime_test.go.
func stubDnsmasqArgv(t *testing.T, bridge, pid string) {
	t.Helper()
	orig := dnsmasqArgvForPID
	dnsmasqArgvForPID = func(p string) []string {
		if p == pid {
			return []string{"/usr/sbin/dnsmasq", "--interface", bridge, "--addn-hosts", "records.hosts"}
		}
		return orig(p)
	}
	t.Cleanup(func() { dnsmasqArgvForPID = orig })
}

func TestSetupDHCP_ReusesRunningServer(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	// A tracked dnsmasq that is alive (the mocked `kill -0` probe reports the
	// PID as running) and that can serve service records. The PID itself is never
	// signalled: the record-capability check is injected, so the test does not
	// have to point the pid file at a process it must keep alive, and the argv
	// reader is stubbed so the PID passes the "is this really our dnsmasq?" check
	// that a pid file cannot be trusted for.
	origCapable := dnsmasqRecordsCapable
	dnsmasqRecordsCapable = func(string) bool { return true }
	defer func() { dnsmasqRecordsCapable = origCapable }()
	stubDnsmasqArgv(t, "testbr0", "999999")

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", Subnet: "10.0.0.0/24"}
	nm := NewNetworkManager(config).(*NetworkManager)
	nm.runtimeDir = t.TempDir()
	require.NoError(t, os.WriteFile(nm.dnsmasqPIDPath(), []byte("999999"), 0o644))

	require.NoError(t, nm.setupDHCP(context.Background()))

	// The healthy server must be reused, not killed and restarted.
	for _, call := range state.calls {
		assert.NotContains(t, call, "dnsmasq --interface",
			"setupDHCP must not start a new dnsmasq while one is running")
	}
}

// TestSetupDHCP_ReplacesRecordlessServer covers the upgrade path: an instance
// that predates service records (no --addn-hosts) is alive and healthy but
// cannot answer service names, so it is replaced instead of reused.
func TestSetupDHCP_ReplacesRecordlessServer(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	// Default capability: no tracked dnsmasq can serve records, so nothing is
	// reusable and a fresh server must be started. No pid file is written on
	// purpose: nothing is signalled here anyway, because a PID that is not a
	// dnsmasq bound to the bridge is refused (see killDnsmasqPID).
	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", Subnet: "10.0.0.0/24"}
	nm := NewNetworkManager(config).(*NetworkManager)
	nm.runtimeDir = t.TempDir()

	require.NoError(t, nm.setupDHCP(context.Background()))

	var started bool
	for _, call := range state.calls {
		if strings.HasPrefix(call, "dnsmasq --interface") {
			started = true
		}
	}
	assert.True(t, started, "a dnsmasq that cannot serve records must be replaced")
}

func TestSetupDHCP_StartsWhenTrackedPIDIsGone(t *testing.T) {
	state := newMockState()
	// Report the liveness probe as failing so the stale PID is ignored.
	state.setFail("kill -0", true)
	restore := setupMocksForTest(state)
	defer restore()

	stubDnsmasqArgv(t, "testbr0", "999999")

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", Subnet: "10.0.0.0/24"}
	nm := NewNetworkManager(config).(*NetworkManager)
	nm.runtimeDir = t.TempDir()
	require.NoError(t, os.WriteFile(nm.dnsmasqPIDPath(), []byte("999999"), 0o644))

	require.NoError(t, nm.setupDHCP(context.Background()))

	var started bool
	for _, call := range state.calls {
		if strings.HasPrefix(call, "dnsmasq --interface") {
			started = true
		}
	}
	assert.True(t, started, "a stale PID must not suppress starting dnsmasq")
}
