//go:build !integration

package network

import (
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPortTestManager() *NetworkManager {
	nm := NewNetworkManager(types.NetworkConfig{
		BridgeName: "testbr0",
		BridgeIP:   "192.168.127.1/24",
		Subnet:     "192.168.127.0/24",
	}).(*NetworkManager)
	return nm
}

func TestPublishPorts_AddsDNATRules(t *testing.T) {
	state := newMockState()
	// Force the -C existence checks to fail so the -A add path runs.
	state.setFail("iptables -t nat -C", true)
	restore := setupMocksForTest(state)
	defer restore()

	nm := newPortTestManager()
	err := nm.PublishPorts("task1", "192.168.127.5", []types.PublishedPort{
		{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080},
	})
	require.NoError(t, err)

	state.mu.Lock()
	calls := append([]string(nil), state.calls...)
	state.mu.Unlock()

	assert.Contains(t, calls, "iptables -t nat -A PREROUTING -p tcp --dport 8080 -m comment --comment swarmcracker:task1 -j DNAT --to-destination 192.168.127.5:80")
	assert.Contains(t, calls, "iptables -t nat -A OUTPUT -p tcp --dport 8080 -m comment --comment swarmcracker:task1 -j DNAT --to-destination 192.168.127.5:80")
	assert.Contains(t, calls, "iptables -t nat -A POSTROUTING -s 127.0.0.0/8 -d 192.168.127.5/32 -m comment --comment swarmcracker:task1 -j MASQUERADE")
	assert.Contains(t, calls, "sysctl -w net.ipv4.conf.testbr0.route_localnet=1")
}

func TestPublishPorts_NoGuestsIP(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	nm := newPortTestManager()
	err := nm.PublishPorts("task1", "", []types.PublishedPort{{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "guest IP")
}

func TestPublishPorts_EmptyNoOp(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	nm := newPortTestManager()
	require.NoError(t, nm.PublishPorts("task1", "192.168.127.5", nil))

	state.mu.Lock()
	n := len(state.calls)
	state.mu.Unlock()
	assert.Zero(t, n, "no iptables calls expected for an empty port list")
}

func TestPublishPorts_CollisionInMemory(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -t nat -C", true)
	restore := setupMocksForTest(state)
	defer restore()

	nm := newPortTestManager()
	require.NoError(t, nm.PublishPorts("task1", "192.168.127.5", []types.PublishedPort{
		{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080},
	}))

	err := nm.PublishPorts("task2", "192.168.127.6", []types.PublishedPort{
		{Protocol: "tcp", TargetPort: 81, PublishedPort: 8080},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already published")
}

func TestPublishPorts_CollisionFromKernelState(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -t nat -C", true)
	// A leftover rule from a previous daemon run, not in our in-memory map.
	state.setOutput("iptables -t nat -S PREROUTING",
		"-A PREROUTING -p tcp -m tcp --dport 8080 -m comment --comment \"swarmcracker:oldtask\" -j DNAT --to-destination 192.168.127.9:80\n")
	restore := setupMocksForTest(state)
	defer restore()

	nm := newPortTestManager()
	err := nm.PublishPorts("task1", "192.168.127.5", []types.PublishedPort{
		{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already forwarded")
}

func TestPublishPorts_SameTaskIsIdempotent(t *testing.T) {
	state := newMockState()
	state.setFail("iptables -t nat -C", true)
	restore := setupMocksForTest(state)
	defer restore()

	nm := newPortTestManager()
	ports := []types.PublishedPort{{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080}}
	require.NoError(t, nm.PublishPorts("task1", "192.168.127.5", ports))
	// Re-publishing for the same task must not be treated as a collision.
	require.NoError(t, nm.PublishPorts("task1", "192.168.127.5", ports))
}

func TestUnpublishPorts_SweepsByComment(t *testing.T) {
	state := newMockState()
	state.setOutput("iptables -t nat -S PREROUTING",
		"-A PREROUTING -p tcp -m tcp --dport 8080 -m comment --comment \"swarmcracker:task1\" -j DNAT --to-destination 192.168.127.5:80\n")
	state.setOutput("iptables -t nat -S OUTPUT",
		"-A OUTPUT -p tcp -m tcp --dport 8080 -m comment --comment \"swarmcracker:task1\" -j DNAT --to-destination 192.168.127.5:80\n")
	state.setOutput("iptables -t nat -S POSTROUTING",
		"-A POSTROUTING -s 127.0.0.0/8 -d 192.168.127.5/32 -m comment --comment \"swarmcracker:task1\" -j MASQUERADE\n")
	restore := setupMocksForTest(state)
	defer restore()

	nm := newPortTestManager()
	require.NoError(t, nm.UnpublishPorts("task1", nil))

	state.mu.Lock()
	calls := append([]string(nil), state.calls...)
	state.mu.Unlock()

	assert.Contains(t, calls, "iptables -t nat -D PREROUTING -p tcp -m tcp --dport 8080 -m comment --comment swarmcracker:task1 -j DNAT --to-destination 192.168.127.5:80")
	assert.Contains(t, calls, "iptables -t nat -D OUTPUT -p tcp -m tcp --dport 8080 -m comment --comment swarmcracker:task1 -j DNAT --to-destination 192.168.127.5:80")
	assert.Contains(t, calls, "iptables -t nat -D POSTROUTING -s 127.0.0.0/8 -d 192.168.127.5/32 -m comment --comment swarmcracker:task1 -j MASQUERADE")
}

func TestUnpublishPorts_IgnoresOtherTasks(t *testing.T) {
	state := newMockState()
	state.setOutput("iptables -t nat -S PREROUTING",
		"-A PREROUTING -p tcp -m tcp --dport 8080 -m comment --comment \"swarmcracker:other\" -j DNAT --to-destination 192.168.127.9:80\n")
	restore := setupMocksForTest(state)
	defer restore()

	nm := newPortTestManager()
	require.NoError(t, nm.UnpublishPorts("task1", nil))

	state.mu.Lock()
	calls := append([]string(nil), state.calls...)
	state.mu.Unlock()

	for _, c := range calls {
		assert.False(t, strings.HasPrefix(c, "iptables -t nat -D"), "must not delete another task's rule: %s", c)
	}
}
