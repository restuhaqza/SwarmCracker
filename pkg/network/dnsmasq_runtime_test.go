//go:build !integration

package network

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runtimeTestManager builds a manager whose dnsmasq files live in a temporary
// directory, so nothing here touches the host's /run.
func runtimeTestManager(t *testing.T) *NetworkManager {
	t.Helper()
	nm := NewNetworkManager(types.NetworkConfig{
		BridgeName: "testbr0",
		BridgeIP:   "10.0.0.1/24",
		Subnet:     "10.0.0.0/24",
	}).(*NetworkManager)
	nm.runtimeDir = t.TempDir()
	return nm
}

// TestDnsmasqFilesLiveOutsideTmp is the placement guarantee behind #72: every
// per-bridge file is under the runtime directory, none of them is created in
// world-writable /tmp, and the directory cannot be written by another local user.
func TestDnsmasqFilesLiveOutsideTmp(t *testing.T) {
	nm := runtimeTestManager(t)
	dir := nm.dnsmasqRuntimeDir()

	require.NoError(t, nm.ensureDnsmasqRuntimeDir())

	for _, p := range []string{
		nm.dnsRecordsPath(),
		nm.dnsmasqPIDPath(),
		nm.dnsmasqLogPath(),
		nm.dnsmasqLeasePath(),
	} {
		assert.Equal(t, dir, filepath.Dir(p), "%s must live in the runtime directory", p)
		assert.NotEqual(t, "/tmp", filepath.Dir(p), "%s must not be in /tmp", p)
	}

	require.NoError(t, writeDnsmasqFile(nm.dnsRecordsPath(), []byte("10.0.0.5\tweb\n"), 0o644))
	assert.FileExists(t, nm.dnsRecordsPath())

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Zero(t, info.Mode().Perm()&0o002, "the runtime directory must not be world-writable")
	assert.NotZero(t, info.Mode().Perm()&0o050,
		"dnsmasq's group must be able to read and search it")
}

// TestEnsureDnsmasqRuntimeDir_HandsItToDnsmasqUser covers the ownership handover:
// when it can, the daemon gives the directory to the group dnsmasq drops to, so
// dnsmasq can create and rewrite its pid and lease files after the drop while no
// other local user can touch them.
func TestEnsureDnsmasqRuntimeDir_HandsItToDnsmasqUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("handing the directory over requires root")
	}

	nm := runtimeTestManager(t)
	require.NoError(t, nm.ensureDnsmasqRuntimeDir())

	gid, err := dnsmasqRunAsGroup()
	require.NoError(t, err)

	info, err := os.Stat(nm.dnsmasqRuntimeDir())
	require.NoError(t, err)
	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok, "expected a syscall.Stat_t")
	assert.Equal(t, gid, int(stat.Gid), "the directory must belong to dnsmasq's group")
	assert.Equal(t, os.FileMode(0o770), info.Mode().Perm())
}

// TestEnsureDnsmasqRuntimeDir_FallsBackWithoutDnsmasqUser covers the degraded
// path: when the user dnsmasq drops to cannot be resolved, the directory is still
// created — searchable, but not group-writable — and setupDHCP keeps working.
func TestEnsureDnsmasqRuntimeDir_FallsBackWithoutDnsmasqUser(t *testing.T) {
	nm := runtimeTestManager(t)

	orig := dnsmasqRunAsGroup
	dnsmasqRunAsGroup = func() (int, error) { return 0, os.ErrNotExist }
	defer func() { dnsmasqRunAsGroup = orig }()

	require.NoError(t, nm.ensureDnsmasqRuntimeDir())

	info, err := os.Stat(nm.dnsmasqRuntimeDir())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	assert.Zero(t, info.Mode().Perm()&0o002, "the fallback must still not be world-writable")
}

// TestEnsureDnsmasqRuntimeDir_RefusesSymlink covers the placement check: a symlink
// where the directory belongs is refused, so nothing is ever written through it.
func TestEnsureDnsmasqRuntimeDir_RefusesSymlink(t *testing.T) {
	nm := runtimeTestManager(t)

	base := t.TempDir()
	target := filepath.Join(base, "target")
	require.NoError(t, os.Mkdir(target, 0o755))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(target, link))
	nm.runtimeDir = link

	err := nm.ensureDnsmasqRuntimeDir()
	require.Error(t, err, "a symlinked runtime directory must be refused")
	assert.Contains(t, err.Error(), "symlink")
}

// TestWriteDnsmasqFile_DoesNotFollowSymlink is the write-side guarantee: a symlink
// planted at a predictable file name cannot redirect a root write, and the
// reconcile reports the failure instead of writing through it.
func TestWriteDnsmasqFile_DoesNotFollowSymlink(t *testing.T) {
	nm := runtimeTestManager(t)
	require.NoError(t, nm.ensureDnsmasqRuntimeDir())

	victim := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(victim, []byte("untouched"), 0o600))

	// Exactly what a local user could do while these files lived in /tmp.
	require.NoError(t, os.Symlink(victim, nm.dnsRecordsPath()))

	err := nm.ProgramDNS([]DNSRecord{{Name: "web", IPs: []string{"10.0.0.5"}}})
	require.Error(t, err, "writing through a planted symlink must fail")

	got, readErr := os.ReadFile(victim)
	require.NoError(t, readErr)
	assert.Equal(t, "untouched", string(got), "the symlink target must be untouched")
}

// TestLiveDnsmasqPID_RefusesPlantedPID covers the reuse-side guarantee: a pid file
// naming a process that is not this bridge's dnsmasq must not make the daemon
// believe a server it did not start is healthy, and cleanup must not signal that
// process — with the files in a shared /tmp, either was reachable by a local user.
func TestLiveDnsmasqPID_RefusesPlantedPID(t *testing.T) {
	skipIfHostHasDnsmasqPidFiles(t)

	nm := runtimeTestManager(t)
	require.NoError(t, nm.ensureDnsmasqRuntimeDir())

	child := exec.Command("sleep", "30")
	require.NoError(t, child.Start())
	defer func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}()

	// A live process, but not a dnsmasq bound to this bridge.
	require.NoError(t, os.WriteFile(nm.dnsmasqPIDPath(),
		[]byte(strconv.Itoa(child.Process.Pid)+"\n"), 0o644))

	assert.Equal(t, "", nm.liveDnsmasqPID("testbr0"),
		"a planted PID must not be reported as a live dnsmasq")

	require.NoError(t, nm.cleanupDnsmasq())
	assert.NoError(t, child.Process.Signal(syscall.Signal(0)),
		"the planted process must still be alive after cleanup")
}

// TestKillDnsmasqPID_RefusesNonProcessID covers the input side: contents that are
// not a plain PID are refused before any kill runs. A value like "-1" would
// otherwise ask kill to signal every process this user may signal — as root, the
// whole system.
func TestKillDnsmasqPID_RefusesNonProcessID(t *testing.T) {
	state := newMockState()
	restore := setupMocksForTest(state)
	defer restore()

	nm := runtimeTestManager(t)

	for _, pid := range []string{"-1", "-15", "0", "not-a-pid", "", "12345678901", "12 34", "1;2"} {
		assert.False(t, nm.killDnsmasqPID(pid, "testbr0"), "PID %q must be refused", pid)
	}

	state.mu.Lock()
	calls := append([]string(nil), state.calls...)
	state.mu.Unlock()
	for _, c := range calls {
		assert.False(t, strings.HasPrefix(c, "kill "),
			"no kill must be attempted for a non-PID: %s", c)
	}
}
