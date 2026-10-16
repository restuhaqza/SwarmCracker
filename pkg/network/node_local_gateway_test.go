//go:build !integration

package network

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNodeLocalGateway pins the per-node addressing of a user-defined network: node k
// holds <subnet>.<k>, and every case where a slot cannot be honoured stays on the
// cluster-wide gateway rather than inventing an address (#76).
func TestNodeLocalGateway(t *testing.T) {
	tests := []struct {
		name           string
		subnet         string
		clusterGateway string
		bridgeIP       string
		peers          int
		want           string
		why            string
	}{
		{
			name: "node 1 holds the cluster gateway", subnet: "10.0.4.0/24",
			clusterGateway: "10.0.4.1", bridgeIP: "192.168.127.1/24", peers: 1, want: "10.0.4.1",
		},
		{
			name: "node 2 holds its own address", subnet: "10.0.4.0/24",
			clusterGateway: "10.0.4.1", bridgeIP: "192.168.127.2/24", peers: 1, want: "10.0.4.2",
		},
		{
			name: "the last reserved slot is still usable", subnet: "10.0.4.0/24",
			clusterGateway: "10.0.4.1", bridgeIP: "192.168.127.16/24", peers: 1, want: "10.0.4.16",
		},
		{
			name: "a wider subnet keeps the last octet", subnet: "10.30.0.0/16",
			clusterGateway: "10.30.0.1", bridgeIP: "192.168.127.2/24", peers: 1, want: "10.30.0.2",
		},
		{
			name: "a single node keeps the cluster gateway", subnet: "10.0.4.0/24",
			clusterGateway: "10.0.4.1", bridgeIP: "192.168.127.1/24", peers: 0, want: "10.0.4.1",
			why: "no peers means no partition",
		},
		{
			name: "a bridge address outside the reserved range keeps it", subnet: "10.0.4.0/24",
			clusterGateway: "10.0.4.1", bridgeIP: "192.168.127.200/24", peers: 1, want: "10.0.4.1",
			why: "no slot is derivable, so nothing is partitioned",
		},
		{
			name: "a slot that is still a usable host wins", subnet: "10.0.4.0/30",
			clusterGateway: "10.0.4.1", bridgeIP: "192.168.127.2/24", peers: 1, want: "10.0.4.2",
			why: "only 10.0.4.3 would be the broadcast on a /30",
		},
		{
			name: "a slot landing on the broadcast keeps it", subnet: "10.0.4.0/28",
			clusterGateway: "10.0.4.1", bridgeIP: "192.168.127.15/24", peers: 1, want: "10.0.4.1",
			why: "10.0.4.15 is the broadcast address of a /28",
		},
		{
			name: "an unparsable bridge address keeps it", subnet: "10.0.4.0/24",
			clusterGateway: "10.0.4.1", bridgeIP: "not-an-ip", peers: 1, want: "10.0.4.1",
		},
		{
			name: "an unparsable subnet keeps it", subnet: "not-a-cidr",
			clusterGateway: "10.0.4.1", bridgeIP: "192.168.127.2/24", peers: 1, want: "10.0.4.1",
		},
		{
			name: "an IPv6 subnet keeps it", subnet: "fd00::/64",
			clusterGateway: "fd00::1", bridgeIP: "192.168.127.2/24", peers: 1, want: "fd00::1",
			why: "the per-node scheme is defined for IPv4 subnets",
		},
		{
			name: "no slot and no recorded gateway stays empty", subnet: "10.0.4.0/24",
			clusterGateway: "", bridgeIP: "192.168.127.1/24", peers: 0, want: "",
			why: "nothing to fall back to and no slot to derive from",
		},
		{
			name: "no recorded gateway still gets the node's address", subnet: "10.0.4.0/24",
			clusterGateway: "", bridgeIP: "192.168.127.2/24", peers: 1, want: "10.0.4.2",
			why: "the cluster gateway is only a fallback; a slot is enough to derive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NodeLocalGateway(tt.subnet, tt.clusterGateway, tt.bridgeIP, tt.peers)
			assert.Equal(t, tt.want, got, tt.why)
		})
	}
}
