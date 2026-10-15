//go:build !integration

package network

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipIfHostHasDnsmasqPidFiles refuses to run any test that reaches
// cleanupDnsmasq when the host already has dnsmasq pid files — in the runtime
// directory or in the legacy /tmp location.
//
// cleanupDnsmasq signals a PID only when it really is a dnsmasq bound to that
// bridge (see killDnsmasqPID), and a leftover file on a developer's host could be
// exactly that; the execCommand seam does not intercept the kill, so exercising it
// there could stop a live dnsmasq.
func skipIfHostHasDnsmasqPidFiles(t *testing.T) {
	t.Helper()

	for _, pattern := range []string{
		filepath.Join(dnsmasqRuntimeDirDefault, "dnsmasq-*.pid"),
		"/tmp/dnsmasq-*.pid",
	} {
		matches, err := filepath.Glob(pattern)
		require.NoError(t, err)
		if len(matches) > 0 {
			t.Skipf("host has existing dnsmasq pid files %v; refusing to risk killing a real process", matches)
		}
	}
}

// uniqueDnsmasqPidFile returns a pid-file path under the legacy /tmp location
// that still matches the /tmp/dnsmasq-*.pid glob but is unique to this test
// process, so concurrent test runs cannot collide. The PID it holds is not a
// dnsmasq, so cleanupDnsmasq removes the file without signalling anything.
func uniqueDnsmasqPidFile(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("/tmp/dnsmasq-covtest-%d-%d.pid", os.Getpid(), time.Now().UnixNano())
}

// TestNetworkManager_Shutdown_Idempotent verifies that Shutdown can be called
// repeatedly without error and that each call attempts to tear down the NAT
// rules. The exec/remove seams are mocked, so no real ip/iptables runs; Shutdown
// also reaches cleanupDnsmasq, which is guarded by
// skipIfHostHasDnsmasqPidFiles because killByPID is not seam-intercepted and
// would otherwise SIGTERM any live dnsmasq PID found in /tmp.
func TestNetworkManager_Shutdown_Idempotent(t *testing.T) {
	skipIfHostHasDnsmasqPidFiles(t)

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
//
// cleanupDnsmasq signals a PID only when it is really a dnsmasq bound to that
// bridge, but the kill call is not behind a seam, so both subtests first refuse to
// run if the host has any pre-existing dnsmasq pid files. The "reads and removes"
// subtest then creates its own uniquely named file containing a definitely-unused
// PID.
func TestNetworkManager_cleanupDnsmasq(t *testing.T) {
	t.Run("no pid file returns nil", func(t *testing.T) {
		skipIfHostHasDnsmasqPidFiles(t)

		state := newMockState()
		restore := setupMocksForTest(state)
		defer restore()

		nm := NewNetworkManager(types.NetworkConfig{}).(*NetworkManager)
		require.NoError(t, nm.cleanupDnsmasq())

		state.mu.Lock()
		calls := append([]string(nil), state.calls...)
		state.mu.Unlock()

		for _, c := range calls {
			assert.False(t, strings.HasPrefix(c, "remove:"),
				"cleanupDnsmasq should not remove anything when no pid file exists: %s", c)
		}
	})

	t.Run("reads and removes pid file", func(t *testing.T) {
		skipIfHostHasDnsmasqPidFiles(t)
		if os.Geteuid() != 0 {
			// The pid file has to look like one this host wrote, and only root can
			// create a root-owned one.
			t.Skip("planting a trusted pid file requires root")
		}

		state := newMockState()
		restore := setupMocksForTest(state)
		defer restore()

		// Use a definitely-unused PID so the real `kill` is harmless.
		pidFile := uniqueDnsmasqPidFile(t)
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
