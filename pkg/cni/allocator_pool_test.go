package cni

import (
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAllocate_AutoAllocatedSubnetSurvivesAnUpgrade is the regression test for the
// hazard the review found: an auto-allocated network has no requested subnet, so a
// re-allocation used to carve a fresh one. Pre-fix the carve was idempotent (the
// pool was a single /24, so it always yielded itself); now that user networks come
// from the overlay pool, a restart or upgrade would move the subnet while the tasks
// keep the addresses they already hold.
func TestAllocate_AutoAllocatedSubnetSurvivesAnUpgrade(t *testing.T) {
	// Before the fix: user networks were carved from the flat L2 subnet, so this is
	// what an auto-allocated network holds today. FlatSubnet did not exist yet, so
	// the pool is not reserved against itself.
	oldProvider := newPoolProvider(t, &CNIConfig{
		SubnetPool: "192.168.127.0/24",
	})
	oldAlloc, err := NewCNINetworkAllocator(oldProvider, nil)
	require.NoError(t, err)

	n := &api.Network{
		ID: "net-auto",
		Spec: api.NetworkSpec{
			Annotations:  api.Annotations{Name: "auto"},
			DriverConfig: &api.Driver{Name: "vxlan"},
		},
	}
	require.NoError(t, oldAlloc.Allocate(n))
	require.NotNil(t, n.IPAM)
	require.NotEmpty(t, n.IPAM.Configs)
	recordedSubnet := n.IPAM.Configs[0].Subnet
	require.Equal(t, "192.168.127.0/24", recordedSubnet,
		"the pre-fix pool hands out the flat L2 subnet")
	require.NotNil(t, n.DriverState)
	recordedVNI := n.DriverState.Options["vxlan_vni"]
	require.NotEmpty(t, recordedVNI, "an overlay network records its VNI")

	// The upgrade: the same network is re-allocated by a daemon that now carves
	// user networks from the overlay pool.
	newProvider := newPoolProvider(t, &CNIConfig{
		SubnetPool:        "192.168.127.0/24",
		OverlaySubnetPool: "10.0.0.0/8",
		FlatSubnet:        "192.168.127.0/24",
	})
	newAlloc, err := NewCNINetworkAllocator(newProvider, nil)
	require.NoError(t, err)

	// A decoy is allocated first, so a value that was regenerated rather than kept
	// would come out different (a restart re-allocates in an arbitrary order).
	decoy := &api.Network{
		ID: "net-decoy",
		Spec: api.NetworkSpec{
			Annotations:  api.Annotations{Name: "decoy"},
			DriverConfig: &api.Driver{Name: "vxlan"},
		},
	}
	require.NoError(t, newAlloc.Allocate(decoy))

	require.NoError(t, newAlloc.Allocate(n))
	assert.Equal(t, recordedSubnet, n.IPAM.Configs[0].Subnet,
		"an upgrade must not move an auto-allocated network's subnet")
	assert.Equal(t, recordedVNI, n.DriverState.Options["vxlan_vni"],
		"an upgrade must not renumber a network's VNI (#80)")
}

// TestAllocate_DeletedNetworkReleasesItsSubnet is the other half of the review's
// finding: the provider's ledger has to be pruned on deallocation, or re-creating a
// network is refused because a deleted one still holds its subnet.
func TestAllocate_DeletedNetworkReleasesItsSubnet(t *testing.T) {
	a := newTestAllocator(t)

	mk := func(id, name string) *api.Network {
		// Spec.IPAM is the user's request; the runtime IPAM is what the allocator
		// records after a successful allocation.
		return &api.Network{
			ID: id,
			Spec: api.NetworkSpec{
				Annotations: api.Annotations{Name: name},
				IPAM:        &api.IPAMOptions{Configs: []*api.IPAMConfig{{Subnet: "10.10.0.0/24"}}},
			},
		}
	}

	first := mk("net-a", "a")
	require.NoError(t, a.Allocate(first))

	second := mk("net-b", "b")
	require.Error(t, a.Allocate(second), "a subnet in use must be refused")

	require.NoError(t, a.Deallocate(first))

	third := mk("net-c", "c")
	require.NoError(t, a.Allocate(third),
		"a deleted network must not keep its subnet reserved")
}
