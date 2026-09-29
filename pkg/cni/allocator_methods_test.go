package cni

import (
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestAllocator builds a CNINetworkAllocator backed by a CNIProvider whose
// plugin and config directories are temporary directories. It is shared by the
// network/service/task allocator test files.
func newTestAllocator(t *testing.T) *CNINetworkAllocator {
	t.Helper()

	provider := setupCNIProvider(t)
	allocator, err := NewCNINetworkAllocator(provider, nil)
	require.NoError(t, err, "NewCNINetworkAllocator should succeed")
	require.NotNil(t, allocator, "allocator should not be nil")
	return allocator
}

func testNetwork() *api.Network {
	return &api.Network{
		ID: "net-1",
		Spec: api.NetworkSpec{
			Annotations: api.Annotations{Name: "test-net"},
		},
	}
}

func TestCNINetworkAllocator_Allocate_Bridge(t *testing.T) {
	allocator := newTestAllocator(t)
	network := testNetwork()

	require.False(t, allocator.IsAllocated(network), "network should not be allocated before Allocate")

	err := allocator.Allocate(network)
	require.NoError(t, err, "Allocate should succeed for the default bridge driver")

	assert.True(t, allocator.IsAllocated(network), "network should be allocated after Allocate")
	assert.NotNil(t, network.DriverState, "network DriverState should be populated after Allocate")
	assert.Equal(t, "bridge", network.DriverState.Name, "DriverState driver name should be bridge")
	assert.NotNil(t, network.IPAM, "network IPAM should be populated after Allocate")
	require.Len(t, network.IPAM.Configs, 1, "IPAM should contain exactly one config")
	assert.NotEmpty(t, network.IPAM.Configs[0].Subnet, "IPAM subnet should be set")

	allocated, err := allocator.GetAllocatedNetwork("net-1")
	require.NoError(t, err, "GetAllocatedNetwork should find the allocated network")
	assert.Equal(t, "net-1", allocated.ID, "allocated network should carry the SwarmKit network ID")
	assert.Equal(t, "test-net", allocated.Name, "allocated network should carry the network name")
	assert.Equal(t, "bridge", allocated.Driver, "allocated network should use the bridge driver")
}

func TestCNINetworkAllocator_Allocate_Nil(t *testing.T) {
	allocator := newTestAllocator(t)

	err := allocator.Allocate(nil)
	assert.Error(t, err, "Allocate(nil) should return an error")
}

func TestCNINetworkAllocator_Allocate_Idempotent(t *testing.T) {
	allocator := newTestAllocator(t)
	network := testNetwork()

	require.NoError(t, allocator.Allocate(network), "first Allocate should succeed")
	sizeAfterFirst := len(allocator.allocatedNets)

	err := allocator.Allocate(network)
	require.NoError(t, err, "second Allocate on the same network should be a no-op success")
	assert.Equal(t, sizeAfterFirst, len(allocator.allocatedNets), "map size should be unchanged on idempotent Allocate")
	assert.True(t, allocator.IsAllocated(network), "network should remain allocated")
}

func TestCNINetworkAllocator_Deallocate(t *testing.T) {
	allocator := newTestAllocator(t)
	network := testNetwork()

	require.NoError(t, allocator.Allocate(network), "Allocate should succeed")
	require.True(t, allocator.IsAllocated(network), "network should be allocated before Deallocate")

	err := allocator.Deallocate(network)
	require.NoError(t, err, "Deallocate should succeed")
	assert.False(t, allocator.IsAllocated(network), "network should no longer be allocated")
	assert.Empty(t, allocator.ListAllocatedNetworks(), "no networks should remain allocated")

	_, err = allocator.GetAllocatedNetwork("net-1")
	assert.Error(t, err, "GetAllocatedNetwork should fail after Deallocate")
}

func TestCNINetworkAllocator_Deallocate_NotAllocated(t *testing.T) {
	allocator := newTestAllocator(t)
	network := testNetwork()

	// Never allocated: Deallocate must be a no-op success.
	err := allocator.Deallocate(network)
	assert.NoError(t, err, "Deallocate of an unknown network should return nil")
	assert.False(t, allocator.IsAllocated(network), "network should still not be allocated")
}

func TestCNINetworkAllocator_Deallocate_Nil(t *testing.T) {
	allocator := newTestAllocator(t)

	err := allocator.Deallocate(nil)
	assert.Error(t, err, "Deallocate(nil) should return an error")
}

func TestCNINetworkAllocator_GetListAllocated(t *testing.T) {
	allocator := newTestAllocator(t)

	_, err := allocator.GetAllocatedNetwork("missing")
	assert.Error(t, err, "GetAllocatedNetwork should error for a missing ID")
	assert.Empty(t, allocator.ListAllocatedNetworks(), "list should be empty initially")

	network := testNetwork()
	require.NoError(t, allocator.Allocate(network), "Allocate should succeed")

	allocated, err := allocator.GetAllocatedNetwork("net-1")
	require.NoError(t, err, "GetAllocatedNetwork should find the allocated network")
	require.NotNil(t, allocated, "allocated network should not be nil")

	networks := allocator.ListAllocatedNetworks()
	require.Len(t, networks, 1, "ListAllocatedNetworks should return the one allocated network")
	assert.Equal(t, "net-1", networks[0].ID, "listed network should match the allocated ID")
}
