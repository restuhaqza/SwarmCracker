package main

import (
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSubnetOverlapError covers the client-side guard for #77: a requested subnet
// that overlaps a network's is refused before the network is created, so the user
// gets a clear error instead of a network that exists and can never be allocated.
func TestSubnetOverlapError(t *testing.T) {
	networks := []*api.Network{
		{
			Spec: api.NetworkSpec{Annotations: api.Annotations{Name: "ingress"}},
			IPAM: &api.IPAMOptions{Configs: []*api.IPAMConfig{{Subnet: "192.168.127.0/24"}}},
		},
		{
			Spec: api.NetworkSpec{Annotations: api.Annotations{Name: "backend"}},
			IPAM: &api.IPAMOptions{Configs: []*api.IPAMConfig{{Subnet: "10.10.0.0/24"}}},
		},
		{
			Spec: api.NetworkSpec{Annotations: api.Annotations{Name: "no-ipam"}},
		},
	}

	// The flat L2 subnet, held by the ingress network.
	err := subnetOverlapError(networks, "clash", "192.168.127.0/24")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `overlaps network "ingress" (192.168.127.0/24)`)

	// A subnet that contains it overlaps it too.
	require.Error(t, subnetOverlapError(networks, "clash-super", "192.168.0.0/16"))

	// A subnet another network already holds.
	err = subnetOverlapError(networks, "dup", "10.10.0.0/24")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `overlaps network "backend"`)

	// A free subnet is fine, including one inside the overlay range.
	require.NoError(t, subnetOverlapError(networks, "free", "10.20.0.0/24"))

	// Re-creating the same name is not an overlap with itself.
	require.NoError(t, subnetOverlapError(networks, "backend", "10.10.0.0/24"))

	// Malformed input is reported as such rather than silently accepted.
	err = subnetOverlapError(networks, "bad", "not-a-cidr")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --subnet")

	// A network with no IPAM (or a nil entry) is skipped, not fatal.
	sparse := []*api.Network{nil, {Spec: api.NetworkSpec{Annotations: api.Annotations{Name: "sparse"}}}}
	require.NoError(t, subnetOverlapError(sparse, "y", "10.0.0.0/24"))
}
