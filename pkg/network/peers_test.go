//go:build !integration

package network

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

// TestAllVXLANPeers_Dedup verifies the peer set shared by the default overlay
// and every user-defined network is trimmed and deduplicated.
func TestAllVXLANPeers_Dedup(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{
		Subnet:       "192.168.127.0/24",
		BridgeIP:     "192.168.127.1/24",
		BridgeName:   "br0",
		IPMode:       "static",
		VXLANEnabled: true,
		VXLANPeers:   []string{"10.0.0.2", " 10.0.0.3 ", "10.0.0.2", ""},
	}).(*NetworkManager)

	assert.Equal(t, []string{"10.0.0.2", "10.0.0.3"}, nm.allVXLANPeers())
}

func TestAllVXLANPeers_None(t *testing.T) {
	nm := NewNetworkManager(types.NetworkConfig{
		Subnet:     "192.168.127.0/24",
		BridgeIP:   "192.168.127.1/24",
		BridgeName: "br0",
		IPMode:     "static",
	}).(*NetworkManager)

	assert.Empty(t, nm.allVXLANPeers())
}
