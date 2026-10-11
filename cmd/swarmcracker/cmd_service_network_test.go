package main

import (
	"context"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// fakeNetworkClient embeds the (large) generated ControlClient interface and
// overrides only ListNetworks.
type fakeNetworkClient struct {
	api.ControlClient
	networks []*api.Network
}

func (f *fakeNetworkClient) ListNetworks(context.Context, *api.ListNetworksRequest, ...grpc.CallOption) (*api.ListNetworksResponse, error) {
	return &api.ListNetworksResponse{Networks: f.networks}, nil
}

// TestResolveNetworkTargets_Multiple verifies a service can join several
// networks at once, in order, and that a repeated network is attached once.
func TestResolveNetworkTargets_Multiple(t *testing.T) {
	client := &fakeNetworkClient{networks: []*api.Network{
		{ID: "id-a", Spec: api.NetworkSpec{Annotations: api.Annotations{Name: "backend"}}},
		{ID: "id-b", Spec: api.NetworkSpec{Annotations: api.Annotations{Name: "frontend"}}},
	}}

	ids, err := resolveNetworkTargets(context.Background(), client, []string{"backend", "frontend", "backend"})
	require.NoError(t, err)
	assert.Equal(t, []string{"id-a", "id-b"}, ids)
}

func TestResolveNetworkTargets_Single(t *testing.T) {
	client := &fakeNetworkClient{networks: []*api.Network{
		{ID: "id-a", Spec: api.NetworkSpec{Annotations: api.Annotations{Name: "backend"}}},
	}}

	ids, err := resolveNetworkTargets(context.Background(), client, []string{"backend"})
	require.NoError(t, err)
	assert.Equal(t, []string{"id-a"}, ids)
}

func TestResolveNetworkTargets_None(t *testing.T) {
	client := &fakeNetworkClient{}
	ids, err := resolveNetworkTargets(context.Background(), client, nil)
	require.NoError(t, err)
	assert.Nil(t, ids)
}
