//go:build !integration

package network

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetworkManager_Shutdown_Idempotent verifies that Shutdown can be called
// repeatedly without error and that each call attempts to tear down the NAT
// rules. The exec seam is mocked so no real ip/iptables/dnsmasq is invoked.
func TestNetworkManager_Shutdown_Idempotent(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	config := types.NetworkConfig{
		BridgeName: "testbr0",
		Subnet:     "10.0.0.0/24",
		BridgeIP:   "10.0.0.1/24",
		NATEnabled: true,
	}
	nm := NewNetworkManager(config).(*NetworkManager)
	nm.natSetup = true

	require.NoError(t, nm.Shutdown(), "first Shutdown should succeed")
	require.NoError(t, nm.Shutdown(), "second Shutdown must tolerate missing state")

	state.mu.Lock()
	calls := append([]string(nil), state.calls...)
	state.mu.Unlock()

	// teardownNAT issues three `iptables ... -D ...` deletes per Shutdown.
	deletes := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "iptables ") && strings.Contains(c, " -D ") {
			deletes++
		}
	}
	assert.GreaterOrEqual(t, deletes, 6,
		"teardownNAT should attempt its three delete rules on each Shutdown; calls=%v", calls)
}

// TestNetworkManager_teardownNAT covers both the no-subnet no-op path and the
// path that attempts to delete the recorded MASQUERADE/FORWARD rules, even when
// iptables reports the rules are absent (errors must be swallowed).
func TestNetworkManager_teardownNAT(t *testing.T) {
	t.Run("no subnet is a no-op", func(t *testing.T) {
		state := newMockState()
		restore := setupMocksForTest(state)
		defer restore()

		nm := NewNetworkManager(types.NetworkConfig{BridgeName: "testbr0"}).(*NetworkManager)
		require.NoError(t, nm.teardownNAT())

		state.mu.Lock()
		calls := append([]string(nil), state.calls...)
		state.mu.Unlock()

		for _, c := range calls {
			assert.False(t, strings.HasPrefix(c, "iptables "),
				"teardownNAT must not call iptables without a subnet: %s", c)
		}
	})

	t.Run("removes recorded rules and tolerates failures", func(t *testing.T) {
		state := newMockState()
		// Every iptables invocation fails: teardownNAT must still return nil.
		state.setFail("iptables", true)
		restore := setupMocksForTest(state)
		defer restore()

		nm := NewNetworkManager(types.NetworkConfig{
			BridgeName: "testbr0",
			Subnet:     "10.0.0.0/24",
		}).(*NetworkManager)
		require.NoError(t, nm.teardownNAT())

		state.mu.Lock()
		calls := append([]string(nil), state.calls...)
		state.mu.Unlock()

		want := []string{
			"iptables -t nat -D POSTROUTING -s 10.0.0.0/24 -j MASQUERADE",
			"iptables -D FORWARD -i testbr0 -j ACCEPT",
			"iptables -D FORWARD -o testbr0 -j ACCEPT",
		}
		for _, w := range want {
			found := false
			for _, c := range calls {
				if c == w {
					found = true
					break
				}
			}
			assert.True(t, found, "expected teardown call %q; calls=%v", w, calls)
		}
	})
}

// TestNetworkManager_killByPID checks that invalid input never panics and that
// a real forked child is actually terminated by the `kill` invocation.
func TestNetworkManager_killByPID(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{}).(*NetworkManager)

	t.Run("empty pid does not panic", func(t *testing.T) {
		assert.NotPanics(t, func() { nm.killByPID("") })
	})

	t.Run("invalid pid does not panic", func(t *testing.T) {
		assert.NotPanics(t, func() { nm.killByPID("not-a-pid") })
	})

	t.Run("nonexistent pid does not panic", func(t *testing.T) {
		assert.NotPanics(t, func() { nm.killByPID("99999999") })
	})

	t.Run("live child is terminated", func(t *testing.T) {
		child := exec.Command("sleep", "30")
		require.NoError(t, child.Start())
		// Safety net in case the assertion below never runs.
		defer func() {
			if child.Process != nil {
				_ = child.Process.Kill()
			}
		}()

		nm.killByPID(strconv.Itoa(child.Process.Pid))

		done := make(chan error, 1)
		go func() { done <- child.Wait() }()

		select {
		case err := <-done:
			require.Error(t, err, "killed child should report a non-zero wait status")
			assert.Contains(t, err.Error(), "signal: terminated")
		case <-time.After(5 * time.Second):
			_ = child.Process.Kill()
			t.Fatal("child was not terminated by killByPID within timeout")
		}
	})
}

// TestNetworkManager_cleanupDnsmasq verifies cleanupDnsmasq tolerates a missing
// pid file and, when one is present, reads it and removes it.
func TestNetworkManager_cleanupDnsmasq(t *testing.T) {
	t.Run("no pid file returns nil", func(t *testing.T) {
		state := newMockState()
		restore := setupMocksForTest(state)
		defer restore()

		nm := NewNetworkManager(types.NetworkConfig{}).(*NetworkManager)
		require.NoError(t, nm.cleanupDnsmasq())
	})

	t.Run("reads and removes pid file", func(t *testing.T) {
		state := newMockState()
		restore := setupMocksForTest(state)
		defer restore()

		// Use a definitely-unused PID so the real `kill` is harmless.
		pidFile := "/tmp/dnsmasq-swarmcracker-covtest.pid"
		require.NoError(t, os.WriteFile(pidFile, []byte("99999999\n"), 0600))
		defer func() { _ = os.Remove(pidFile) }()

		nm := NewNetworkManager(types.NetworkConfig{}).(*NetworkManager)
		require.NoError(t, nm.cleanupDnsmasq())

		state.mu.Lock()
		calls := append([]string(nil), state.calls...)
		state.mu.Unlock()

		foundRemove := false
		for _, c := range calls {
			if c == "remove:"+pidFile {
				foundRemove = true
				break
			}
		}
		assert.True(t, foundRemove, "cleanupDnsmasq should remove %s; calls=%v", pidFile, calls)
	})
}
