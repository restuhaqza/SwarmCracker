//go:build linux

package network

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubExec replaces execCommand with a recording stub that succeeds, so tests
// never touch the host's interfaces or iptables.
func stubExec(t *testing.T) *[]string {
	t.Helper()
	calls := &[]string{}
	orig := execCommand
	execCommand = func(name string, arg ...string) *exec.Cmd {
		*calls = append(*calls, name+" "+strings.Join(arg, " "))
		return exec.Command("true")
	}
	t.Cleanup(func() { execCommand = orig })
	return calls
}

func hasCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

// TestReconcileManagedNetworks_TearsDownUnused verifies an unused user-defined
// network has its VXLAN and bridge deleted (so `network rm` leaves nothing).
func TestReconcileManagedNetworks_TearsDownUnused(t *testing.T) {
	calls := stubExec(t)
	nm := newTestNetworkManager(t)
	alloc, err := NewIPAllocator("10.10.0.0/24", "10.10.0.1")
	require.NoError(t, err)

	vxm := NewVXLANManager("br-backend", 4242, "", nil)
	nm.managedNetworks["n1"] = &managedNetwork{
		id: "n1", name: "backend", bridge: "br-backend", allocator: alloc, vxlanMgr: vxm,
	}

	nm.reconcileManagedNetworks()

	assert.NotContains(t, nm.managedNetworks, "n1")
	assert.True(t, hasCall(*calls, "ip link delete "+vxm.vxlanName()), "VXLAN must be deleted")
	assert.True(t, hasCall(*calls, "ip link delete br-backend"), "bridge must be deleted")
}

// TestReconcileManagedNetworks_KeepsInUse verifies a network with a local TAP is
// left alone.
func TestReconcileManagedNetworks_KeepsInUse(t *testing.T) {
	calls := stubExec(t)
	nm := newTestNetworkManager(t)
	nm.managedNetworks["n1"] = &managedNetwork{id: "n1", name: "backend", bridge: "br-backend"}
	nm.tapDevices["t1-tap-1"] = &TapDevice{Name: "tap-1", Bridge: "br-backend"}

	nm.reconcileManagedNetworks()

	assert.Contains(t, nm.managedNetworks, "n1")
	assert.False(t, hasCall(*calls, "ip link delete br-backend"), "in-use bridge must not be deleted")
}

// TestTeardownManagedNetwork_SkipsWhenInUse covers the re-check under the lock.
func TestTeardownManagedNetwork_SkipsWhenInUse(t *testing.T) {
	stubExec(t)
	nm := newTestNetworkManager(t)
	mn := &managedNetwork{id: "n1", name: "backend", bridge: "br-backend"}
	nm.managedNetworks["n1"] = mn
	nm.tapDevices["t1-tap-1"] = &TapDevice{Name: "tap-1", Bridge: "br-backend"}

	nm.teardownManagedNetwork(mn)

	assert.Contains(t, nm.managedNetworks, "n1")
}

// TestReleaseIP_UsesManagedAllocator verifies an address handed out by a
// user-defined network is returned to that network's allocator.
func TestReleaseIP_UsesManagedAllocator(t *testing.T) {
	nm := newTestNetworkManager(t)
	alloc, err := NewIPAllocator("10.10.0.0/24", "10.10.0.1")
	require.NoError(t, err)
	nm.managedNetworks["n1"] = &managedNetwork{id: "n1", bridge: "br-backend", allocator: alloc}

	ip, err := alloc.Allocate("task-1")
	require.NoError(t, err)

	nm.releaseIP(&TapDevice{Name: "tap-1", Bridge: "br-backend", IP: ip})

	alloc.mu.Lock()
	_, held := alloc.allocated[ip]
	alloc.mu.Unlock()
	assert.False(t, held, "managed allocator must release the address")
}

// TestReleaseIP_UsesDefaultAllocator verifies a default-bridge address is
// returned to the default allocator.
func TestReleaseIP_UsesDefaultAllocator(t *testing.T) {
	nm := newTestNetworkManager(t)
	require.NotNil(t, nm.ipAllocator)

	ip, err := nm.ipAllocator.Allocate("task-1")
	require.NoError(t, err)

	nm.releaseIP(&TapDevice{Name: "tap-0", Bridge: "br0", IP: ip})

	nm.ipAllocator.mu.Lock()
	_, held := nm.ipAllocator.allocated[ip]
	nm.ipAllocator.mu.Unlock()
	assert.False(t, held, "default allocator must release the address")
}
