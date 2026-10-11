//go:build !integration

package network

import (
	"fmt"
	"net"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nodeBlockBounds returns the inclusive last-octet bounds of node slot idx for
// a /24 subnet with the given slot count, for use in assertions.
func nodeBlockBounds(t *testing.T, idx, slots uint32) (int, int) {
	t.Helper()
	_, subnet, err := net.ParseCIDR("192.168.127.0/24")
	require.NoError(t, err)
	ones, bits := subnet.Mask.Size()
	require.Equal(t, 32, bits)
	size := uint32(1) << (bits - ones)
	reserved := uint32(reservedInfraHosts)
	usable := size - 2 - reserved
	blockSize := usable / slots
	start := (idx - 1) * blockSize
	end := start + blockSize
	if idx == slots {
		end = usable
	}
	// host offset o maps to last octet 1 + reserved + o
	return int(1 + reserved + start), int(1 + reserved + end - 1)
}

func lastOctet(t *testing.T, ip string) int {
	t.Helper()
	v4 := net.ParseIP(ip).To4()
	require.NotNil(t, v4)
	return int(v4[3])
}

// TestNodeAllocationParams checks how the node slot and slot count are derived
// from the overlay bridge address and the peer count.
func TestNodeAllocationParams(t *testing.T) {
	tests := []struct {
		name      string
		bridgeIP  string
		peerCount int
		wantIndex uint32
		wantSlots uint32
	}{
		{"single node disables partitioning", "192.168.127.1/24", 0, 0, 1},
		{"node 1 of a cluster", "192.168.127.1/24", 2, 1, reservedInfraHosts},
		{"node 3 of a cluster", "192.168.127.3/24", 2, 3, reservedInfraHosts},
		{"bridge outside the per-node range", "192.168.127.99/24", 2, 0, 1},
		{"empty bridge", "", 2, 0, 1},
		{"ipv6 bridge", "fd00::1/64", 2, 0, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, slots := nodeAllocationParams(tt.bridgeIP, tt.peerCount)
			assert.Equal(t, tt.wantIndex, idx)
			assert.Equal(t, tt.wantSlots, slots)
		})
	}
}

// TestIPAllocatorForNode_DisjointAcrossNodes is the collision-freedom test:
// per-node allocators sharing one subnet must never hand out the same address.
func TestIPAllocatorForNode_DisjointAcrossNodes(t *testing.T) {
	const subnet = "192.168.127.0/24"

	for _, slots := range []uint32{2, 3, reservedInfraHosts} {
		t.Run(fmt.Sprintf("%d_slots", slots), func(t *testing.T) {
			seen := make(map[string]uint32)
			for idx := uint32(1); idx <= slots; idx++ {
				gw := fmt.Sprintf("192.168.127.%d", idx)
				alloc, err := NewIPAllocatorForNode(subnet, gw, idx, slots)
				require.NoError(t, err)

				lo, hi := nodeBlockBounds(t, idx, slots)
				for i := 0; i < 200; i++ {
					ip, err := alloc.Allocate(fmt.Sprintf("node%d-vm%d", idx, i))
					if err != nil {
						break // node block exhausted
					}
					octet := lastOctet(t, ip)
					assert.GreaterOrEqual(t, octet, lo, "node %d IP %s below its block", idx, ip)
					assert.LessOrEqual(t, octet, hi, "node %d IP %s above its block", idx, ip)
					if other, dup := seen[ip]; dup {
						t.Fatalf("IP %s allocated by both node %d and node %d", ip, other, idx)
					}
					seen[ip] = idx
				}
			}
			assert.NotEmpty(t, seen)
		})
	}
}

// TestIPAllocatorForNode_BlockBounds checks a single node only uses its slice
// and never the reserved/gateway range.
func TestIPAllocatorForNode_BlockBounds(t *testing.T) {
	alloc, err := NewIPAllocatorForNode("192.168.127.0/24", "192.168.127.1", 1, reservedInfraHosts)
	require.NoError(t, err)

	lo, hi := nodeBlockBounds(t, 1, reservedInfraHosts)
	for i := 0; i < 200; i++ {
		ip, err := alloc.Allocate(fmt.Sprintf("vm-%d", i))
		if err != nil {
			break
		}
		octet := lastOctet(t, ip)
		assert.GreaterOrEqual(t, octet, lo)
		assert.LessOrEqual(t, octet, hi)
	}
}

// TestIPAllocatorForNode_ExhaustsBlock verifies a node errors when its block is
// full instead of silently spilling into another node's block.
func TestIPAllocatorForNode_ExhaustsBlock(t *testing.T) {
	const slots = 16
	alloc, err := NewIPAllocatorForNode("192.168.127.0/24", "192.168.127.1", 1, slots)
	require.NoError(t, err)

	_, subnet, err := net.ParseCIDR("192.168.127.0/24")
	require.NoError(t, err)
	_ = subnet
	lo, hi := nodeBlockBounds(t, 1, slots)
	capacity := hi - lo + 1

	for i := 0; i < capacity; i++ {
		_, err := alloc.Allocate(fmt.Sprintf("vm-%d", i))
		require.NoError(t, err, "allocation %d should fit the block", i)
	}
	_, err = alloc.Allocate("one-too-many")
	assert.Error(t, err)
}

// TestIPAllocatorForNode_SingleNodeWholeSubnet ensures nodeSlots<=1 preserves
// the legacy whole-subnet capacity.
func TestIPAllocatorForNode_SingleNodeWholeSubnet(t *testing.T) {
	alloc, err := NewIPAllocatorForNode("192.168.127.0/24", "192.168.127.1", 0, 1)
	require.NoError(t, err)

	ips := make(map[string]bool)
	for i := 0; i < 100; i++ {
		ip, err := alloc.Allocate(fmt.Sprintf("vm-%d", i))
		require.NoError(t, err)
		assert.False(t, ips[ip], "duplicate IP %s", ip)
		ips[ip] = true
	}
	assert.Equal(t, 100, len(ips))
}

// TestIPAllocatorForNode_SmallSubnetFallsBack keeps small subnets unpartitioned
// (they are too small to reserve infrastructure, so /30 semantics are kept).
func TestIPAllocatorForNode_SmallSubnetFallsBack(t *testing.T) {
	alloc, err := NewIPAllocatorForNode("192.168.1.0/30", "192.168.1.1", 1, 16)
	require.NoError(t, err)

	ip, err := alloc.Allocate("vm")
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.2", ip)
}

// TestNewNetworkManager_PartitionsWithPeers checks the manager enables per-node
// partitioning for a multi-node overlay and that the default allocator stays
// inside the node's block.
func TestNewNetworkManager_PartitionsWithPeers(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{
		Subnet:       "192.168.127.0/24",
		BridgeIP:     "192.168.127.2/24",
		BridgeName:   "br0",
		IPMode:       "static",
		VXLANEnabled: true,
		VXLANPeers:   []string{"10.0.0.1", "10.0.0.3"},
	}).(*NetworkManager)

	require.NotNil(t, nm.ipAllocator)
	assert.Equal(t, uint32(2), nm.nodeIndex)
	assert.Equal(t, uint32(reservedInfraHosts), nm.nodeSlots)

	lo, hi := nodeBlockBounds(t, 2, reservedInfraHosts)
	ip, err := nm.ipAllocator.Allocate("task-1")
	require.NoError(t, err)
	octet := lastOctet(t, ip)
	assert.GreaterOrEqual(t, octet, lo)
	assert.LessOrEqual(t, octet, hi)
}

// TestNewNetworkManager_SingleNodeUnpartitioned checks a node with no peers
// keeps the whole subnet.
func TestNewNetworkManager_SingleNodeUnpartitioned(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{
		Subnet:     "192.168.127.0/24",
		BridgeIP:   "192.168.127.1/24",
		BridgeName: "br0",
		IPMode:     "static",
	}).(*NetworkManager)

	require.NotNil(t, nm.ipAllocator)
	assert.Equal(t, uint32(0), nm.nodeIndex)
	assert.Equal(t, uint32(1), nm.nodeSlots)
}
