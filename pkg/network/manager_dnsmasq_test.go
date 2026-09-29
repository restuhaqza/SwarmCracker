package network

import (
	"context"
	"os"
	"strconv"
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
	nm := &NetworkManager{config: types.NetworkConfig{BridgeName: "no-such-bridge-xyz"}}
	assert.Equal(t, "", nm.liveDnsmasqPID("no-such-bridge-xyz"))
}

func TestSetupDHCP_ReusesRunningServer(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	// A tracked dnsmasq that is alive: point the pid file at this test process,
	// which the mocked `kill -0` probe reports as running.
	pidFile := "/tmp/dnsmasq-testbr0.pid"
	require.NoError(t, os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644))
	t.Cleanup(func() { _ = os.Remove(pidFile) })

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", Subnet: "10.0.0.0/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	require.NoError(t, nm.setupDHCP(context.Background()))

	// The healthy server must be reused, not killed and restarted.
	for _, call := range state.calls {
		assert.NotContains(t, call, "dnsmasq --interface",
			"setupDHCP must not start a new dnsmasq while one is running")
	}
}

func TestSetupDHCP_StartsWhenTrackedPIDIsGone(t *testing.T) {
	state := newMockState()
	// Report the liveness probe as failing so the stale PID is ignored.
	state.setFail("kill -0", true)
	restore := setupMocksForTest(state)
	defer restore()

	pidFile := "/tmp/dnsmasq-testbr0.pid"
	require.NoError(t, os.WriteFile(pidFile, []byte("999999"), 0o644))
	t.Cleanup(func() { _ = os.Remove(pidFile) })

	config := types.NetworkConfig{BridgeName: "testbr0", BridgeIP: "10.0.0.1/24", Subnet: "10.0.0.0/24"}
	nm := NewNetworkManager(config).(*NetworkManager)

	require.NoError(t, nm.setupDHCP(context.Background()))

	var started bool
	for _, call := range state.calls {
		if strings.HasPrefix(call, "dnsmasq --interface") {
			started = true
		}
	}
	assert.True(t, started, "a stale PID must not suppress starting dnsmasq")
}
