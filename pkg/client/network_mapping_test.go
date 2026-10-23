package client

import (
	"context"
	"testing"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListNetworksMapsAndFiltersBuiltins(t *testing.T) {
	control := &fakeControl{networks: []*api.Network{
		{ID: "net-ingress", Spec: api.NetworkSpec{Ingress: true, Annotations: api.Annotations{Name: "ingress"}}},
		{ID: "net-gw", Spec: api.NetworkSpec{Annotations: api.Annotations{Name: "docker_gwbridge"}}},
		nil,
		{
			ID: "net-b",
			Meta: api.Meta{
				Version:   api.Version{Index: 2},
				CreatedAt: &gogotypes.Timestamp{Seconds: 200},
			},
			Spec: api.NetworkSpec{
				Annotations:  api.Annotations{Name: "backend", Labels: map[string]string{"tier": "data"}},
				DriverConfig: &api.Driver{Name: "overlay"},
				IPAM:         &api.IPAMOptions{Configs: []*api.IPAMConfig{nil, {Subnet: "10.0.1.0/24", Gateway: "10.0.1.1"}}},
				Attachable:   true,
			},
		},
		{
			ID: "net-a",
			Meta: api.Meta{
				Version:   api.Version{Index: 1},
				CreatedAt: &gogotypes.Timestamp{Seconds: 100},
			},
			Spec: api.NetworkSpec{Annotations: api.Annotations{Name: "frontend"}},
		},
	}}

	got, err := (&Client{control: control}).ListNetworks(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "net-a", got[0].ID)
	assert.Equal(t, "net-b", got[1].ID)
	assert.Equal(t, "backend", got[1].Name)
	assert.Equal(t, "2", got[1].Revision)
	assert.Equal(t, "overlay", got[1].Driver)
	assert.True(t, got[1].Attachable)
	assert.Equal(t, map[string]string{"tier": "data"}, got[1].Labels)
	require.NotNil(t, got[1].IPAM)
	assert.Equal(t, "10.0.1.0/24", got[1].IPAM.Subnet)
	assert.Equal(t, "10.0.1.1", got[1].IPAM.Gateway)
	assert.Nil(t, got[0].IPAM)
}

func TestListNetworksEmptyAndFailure(t *testing.T) {
	empty, err := (&Client{control: &fakeControl{}}).ListNetworks(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)

	failed, err := (&Client{control: &fakeControl{networkErr: assert.AnError}}).ListNetworks(context.Background())
	require.ErrorIs(t, err, assert.AnError)
	assert.Nil(t, failed)
}
