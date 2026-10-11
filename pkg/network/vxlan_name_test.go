//go:build linux

package network

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestVXLANManager_vxlanName verifies the VXLAN interface name always fits in
// IFNAMSIZ (15 bytes), even for long bridge names, while keeping the
// conventional "<bridge>-vxlan" name when it fits.
func TestVXLANManager_vxlanName(t *testing.T) {
	cases := []struct {
		bridge string
		want   string
	}{
		{"swarm-br0", "swarm-br0-vxlan"}, // exactly 15 bytes
		{"br0", "br0-vxlan"},
		{"br-backend", ""},  // 16 bytes -> short deterministic name
		{"br-frontend", ""}, // 17 bytes -> short deterministic name
	}

	for _, tc := range cases {
		t.Run(tc.bridge, func(t *testing.T) {
			got := NewVXLANManager(tc.bridge, 100, "", nil).vxlanName()
			assert.LessOrEqual(t, len(got), 15, "name %q exceeds IFNAMSIZ", got)
			if tc.want != "" {
				assert.Equal(t, tc.want, got)
			} else {
				assert.NotEqual(t, tc.bridge+"-vxlan", got)
			}
		})
	}

	// Deterministic and unique per bridge.
	a := NewVXLANManager("br-backend", 100, "", nil).vxlanName()
	b := NewVXLANManager("br-backend", 100, "", nil).vxlanName()
	c := NewVXLANManager("br-frontend", 100, "", nil).vxlanName()
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
}
