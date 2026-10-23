package client

import (
	"context"
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListConfigsMapsMetadataAndOrder(t *testing.T) {
	control := &fakeControl{configs: []*api.Config{
		{
			ID: "cfg-b",
			Meta: api.Meta{
				Version:   api.Version{Index: 2},
				CreatedAt: &gogotypes.Timestamp{Seconds: 200},
			},
			Spec: api.ConfigSpec{
				Annotations: api.Annotations{Name: "b", Labels: map[string]string{"team": "payments"}},
				Data:        []byte("must-not-be-returned"),
			},
		},
		nil,
		{
			ID: "cfg-a",
			Meta: api.Meta{
				Version:   api.Version{Index: 1},
				CreatedAt: &gogotypes.Timestamp{Seconds: 100},
			},
			Spec: api.ConfigSpec{Annotations: api.Annotations{Name: "a"}},
		},
	}}

	got, err := (&Client{control: control}).ListConfigs(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "cfg-a", got[0].ID)
	assert.Equal(t, "b", got[1].Name)
	assert.Equal(t, "2", got[1].Revision)
	assert.Equal(t, map[string]string{"team": "payments"}, got[1].Labels)
	assert.Equal(t, time.Unix(200, 0).UTC().Format(time.RFC3339Nano), got[1].CreatedAt)
}

func TestListConfigsEmptyAndFailure(t *testing.T) {
	empty, err := (&Client{control: &fakeControl{}}).ListConfigs(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)

	failed, err := (&Client{control: &fakeControl{configErr: assert.AnError}}).ListConfigs(context.Background())
	require.ErrorIs(t, err, assert.AnError)
	assert.Nil(t, failed)
}

func TestListSecretsMapsMetadataAndDriver(t *testing.T) {
	control := &fakeControl{secrets: []*api.Secret{{
		ID: "sec-a",
		Meta: api.Meta{
			Version:   api.Version{Index: 7},
			CreatedAt: &gogotypes.Timestamp{Seconds: 100},
			UpdatedAt: &gogotypes.Timestamp{Seconds: 200},
		},
		Spec: api.SecretSpec{
			Annotations: api.Annotations{Name: "token", Labels: map[string]string{"env": "prod"}},
			Data:        []byte("must-not-be-returned"),
			Driver:      &api.Driver{Name: "external"},
		},
	}}}

	got, err := (&Client{control: control}).ListSecrets(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "token", got[0].Name)
	assert.Equal(t, "7", got[0].Revision)
	assert.Equal(t, "external", got[0].Driver)
	assert.Equal(t, map[string]string{"env": "prod"}, got[0].Labels)
	assert.Equal(t, time.Unix(100, 0).UTC().Format(time.RFC3339Nano), got[0].CreatedAt)
	assert.Equal(t, time.Unix(200, 0).UTC().Format(time.RFC3339Nano), got[0].UpdatedAt)
}

func TestListSecretsEmptyAndFailure(t *testing.T) {
	empty, err := (&Client{control: &fakeControl{}}).ListSecrets(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)

	failed, err := (&Client{control: &fakeControl{secretErr: assert.AnError}}).ListSecrets(context.Background())
	require.ErrorIs(t, err, assert.AnError)
	assert.Nil(t, failed)
}
