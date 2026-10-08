//go:build !integration

package network

import (
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIPAllocator_AvoidsReservedInfraRange ensures guest IPs never land in the
// low block reserved for node bridges, which collide on the shared L2 overlay.
func TestIPAllocator_AvoidsReservedInfraRange(t *testing.T) {
	alloc, err := NewIPAllocator("192.168.127.0/24", "192.168.127.1")
	require.NoError(t, err)

	for i := 0; i < 200; i++ {
		ip, err := alloc.Allocate(fmt.Sprintf("vm-%d", i))
		require.NoError(t, err)

		v4 := net.ParseIP(ip).To4()
		require.NotNil(t, v4)
		assert.GreaterOrEqual(t, int(v4[3]), reservedInfraHosts+1,
			"allocated %s must not use the reserved infrastructure range", ip)
	}
}

// TestIPAllocator_ReservedRangeExcludesNodeBridges checks the specific bridge
// addresses used by the multi-node lab (192.168.127.1, .2, .3) are never handed
// to a VM.
func TestIPAllocator_ReservedRangeExcludesNodeBridges(t *testing.T) {
	alloc, err := NewIPAllocator("192.168.127.0/24", "192.168.127.1")
	require.NoError(t, err)

	for i := 0; i < 254; i++ {
		ip, err := alloc.Allocate(fmt.Sprintf("task-%d", i))
		if err != nil {
			break // subnet exhausted
		}
		assert.NotContains(t, []string{"192.168.127.1", "192.168.127.2", "192.168.127.3"}, ip)
	}
}

// TestIPAllocator_SmallSubnetUnaffected keeps /30 behavior unchanged: the block
// is too small to reserve, so the host address after the gateway is used.
func TestIPAllocator_SmallSubnetUnaffected(t *testing.T) {
	alloc, err := NewIPAllocator("192.168.1.0/30", "192.168.1.1")
	require.NoError(t, err)

	ip, err := alloc.Allocate("vm")
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.2", ip)
}
