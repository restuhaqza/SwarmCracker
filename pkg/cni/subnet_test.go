package cni

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerateSubnet_CarvesFromPool pins the octet placement: a /8 pool with
// /24 subnets must vary the third octet (10.0.N.0/24), not the first, which
// previously produced the invalid 1.0.0.0/24.
func TestGenerateSubnet_CarvesFromPool(t *testing.T) {
	cases := []struct {
		pool     string
		size     int
		index    uint32
		expected string
	}{
		{"10.0.0.0/8", 24, 0, "10.0.0.0/24"},
		{"10.0.0.0/8", 24, 1, "10.0.1.0/24"},
		{"10.0.0.0/8", 24, 255, "10.0.255.0/24"},
		{"10.0.0.0/8", 24, 256, "10.1.0.0/24"},
		{"172.16.0.0/12", 24, 1, "172.16.1.0/24"},
		{"10.0.0.0/16", 24, 3, "10.0.3.0/24"},
		// Pool equal to the subnet size: no variable octets, return the pool.
		{"192.168.127.0/24", 24, 1, "192.168.127.0/24"},
	}
	for _, tc := range cases {
		got, err := GenerateSubnet(tc.pool, tc.size, tc.index)
		require.NoError(t, err, "%s /%d idx %d", tc.pool, tc.size, tc.index)
		assert.Equal(t, tc.expected, got, "%s /%d idx %d", tc.pool, tc.size, tc.index)
	}
}

func TestGenerateSubnet_RejectsBadCombos(t *testing.T) {
	_, err := GenerateSubnet("10.0.0.0/24", 16, 0) // subnet smaller than pool
	assert.Error(t, err)

	_, err = GenerateSubnet("10.0.0.0/8", 33, 0) // subnet size out of range
	assert.Error(t, err)
}
