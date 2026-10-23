package client

import (
	"context"
	"testing"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetNetworkMapsNetwork(t *testing.T) {
	control := &fakeControl{network: &api.Network{
		ID: "net-1",
		Meta: api.Meta{
			Version:   api.Version{Index: 2},
			CreatedAt: &gogotypes.Timestamp{Seconds: 100},
		},
		Spec: api.NetworkSpec{
			Annotations:  api.Annotations{Name: "app", Labels: map[string]string{"tier": "data"}},
			DriverConfig: &api.Driver{Name: "overlay"},
			Attachable:   true,
		},
	}}

	got, err := (&Client{control: control}).GetNetwork(context.Background(), "net-1")
	require.NoError(t, err)
	assert.Equal(t, "net-1", got.ID)
	assert.Equal(t, "app", got.Name)
	assert.Equal(t, "2", got.Revision)
	assert.Equal(t, "overlay", got.Driver)
	assert.True(t, got.Attachable)
	assert.Equal(t, map[string]string{"tier": "data"}, got.Labels)
	assert.Equal(t, 1, control.getNetworkCall)
}

func TestGetNetworkMissingIsNotFound(t *testing.T) {
	_, err := (&Client{control: &fakeControl{}}).GetNetwork(context.Background(), "missing")
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetNetworkFailureIsReturned(t *testing.T) {
	control := &fakeControl{getNetworkErr: assert.AnError}
	_, err := (&Client{control: control}).GetNetwork(context.Background(), "net-1")
	require.ErrorIs(t, err, assert.AnError)
}

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
