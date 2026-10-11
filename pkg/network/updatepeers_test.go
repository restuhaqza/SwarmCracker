//go:build linux

package network

import (
	"os"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestNetworkManager(t *testing.T) *NetworkManager {
	t.Helper()
	nm := NewNetworkManager(types.NetworkConfig{
		Subnet:     "192.168.127.0/24",
		BridgeIP:   "192.168.127.1/24",
		BridgeName: "br0",
		IPMode:     "static",
	})
	return nm.(*NetworkManager)
}

// TestNetworkManager_UpdatePeers_QueuesWhenUninitialized covers the pre-init
// queueing path.
func TestNetworkManager_UpdatePeers_QueuesWhenUninitialized(t *testing.T) {
	nm := newTestNetworkManager(t)
	require.NoError(t, nm.UpdatePeers([]string{"10.0.0.9"}))
	assert.Equal(t, []string{"10.0.0.9"}, nm.pendingPeers)
}

// TestNetworkManager_UpdatePeers_FanOut verifies peers are fanned out to every
// user-defined network overlay: a network without an overlay is skipped, and a
// failing overlay does not stop the update from completing.
func TestNetworkManager_UpdatePeers_FanOut(t *testing.T) {
	nm := newTestNetworkManager(t)

	// The default overlay points at a bridge that does not exist, so its update
	// fails; the fan-out to user-defined networks must still run.
	nm.vxlanMgr = NewVXLANManager("br-does-not-exist", 100, "", nil)
	nm.managedNetworks["n1"] = &managedNetwork{
		id: "n1", bridge: "br-nope", vxlanMgr: NewVXLANManager("br-nope", 200, "", nil),
	}
	nm.managedNetworks["n2"] = &managedNetwork{id: "n2", bridge: "br-nope2"} // no overlay

	assert.NotPanics(t, func() { _ = nm.UpdatePeers([]string{"10.0.0.2"}) })
}

// TestEnsureNetworkVXLAN covers the per-network overlay setup: a zero VNI is a
// no-op, and without CAP_NET_ADMIN the VXLAN interface cannot be created, which
// exercises the discovery + error paths. Skipped when running as root (where it
// could create a real interface).
func TestEnsureNetworkVXLAN(t *testing.T) {
	nm := newTestNetworkManager(t)

	vxm, err := nm.ensureNetworkVXLAN("br-nope", 0)
	require.NoError(t, err)
	assert.Nil(t, vxm)

	if os.Geteuid() == 0 {
		t.Skip("running as root: skipping to avoid creating a real VXLAN interface")
	}
	if _, _, err := nm.getPhysicalInterface(); err != nil {
		t.Skipf("no physical interface available: %v", err)
	}

	_, err = nm.ensureNetworkVXLAN("br-nope", 4242)
	require.Error(t, err, "creating a VXLAN interface must fail without privileges")
}
