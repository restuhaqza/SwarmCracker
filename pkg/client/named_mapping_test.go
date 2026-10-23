package client

import (
	"context"
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetConfigMapsMetadata(t *testing.T) {
	control := &fakeControl{config: &api.Config{
		ID: "cfg-1",
		Meta: api.Meta{
			Version:   api.Version{Index: 3},
			CreatedAt: &gogotypes.Timestamp{Seconds: 100},
			UpdatedAt: &gogotypes.Timestamp{Seconds: 200},
		},
		Spec: api.ConfigSpec{
			Annotations: api.Annotations{Name: "app", Labels: map[string]string{"team": "payments"}},
			Data:        []byte("must-not-be-returned"),
		},
	}}

	got, err := (&Client{control: control}).GetConfig(context.Background(), "cfg-1")
	require.NoError(t, err)
	assert.Equal(t, "cfg-1", got.ID)
	assert.Equal(t, "app", got.Name)
	assert.Equal(t, "3", got.Revision)
	assert.Equal(t, map[string]string{"team": "payments"}, got.Labels)
	assert.Equal(t, time.Unix(100, 0).UTC().Format(time.RFC3339Nano), got.CreatedAt)
	assert.Equal(t, 1, control.getConfigCall)
	require.NotNil(t, control.lastGetConfigReq, "GetConfig must be called with a request")
	assert.Equal(t, "cfg-1", control.lastGetConfigReq.ConfigID)
}

func TestGetConfigMissingIsNotFound(t *testing.T) {
	_, err := (&Client{control: &fakeControl{}}).GetConfig(context.Background(), "missing")
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetConfigNilResponseIsNotFound(t *testing.T) {
	control := &fakeControl{nilResponse: true}

	_, err := (&Client{control: control}).GetConfig(context.Background(), "cfg-1")
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Equal(t, 1, control.getConfigCall)
	require.NotNil(t, control.lastGetConfigReq)
	assert.Equal(t, "cfg-1", control.lastGetConfigReq.ConfigID)
}

func TestGetConfigFailureIsReturned(t *testing.T) {
	control := &fakeControl{getConfigErr: assert.AnError}
	_, err := (&Client{control: control}).GetConfig(context.Background(), "cfg-1")
	require.ErrorIs(t, err, assert.AnError)
}

func TestGetSecretMapsMetadata(t *testing.T) {
	control := &fakeControl{secret: &api.Secret{
		ID: "sec-1",
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
	}}

	got, err := (&Client{control: control}).GetSecret(context.Background(), "sec-1")
	require.NoError(t, err)
	assert.Equal(t, "sec-1", got.ID)
	assert.Equal(t, "token", got.Name)
	assert.Equal(t, "7", got.Revision)
	assert.Equal(t, "external", got.Driver)
	assert.Equal(t, map[string]string{"env": "prod"}, got.Labels)
	assert.Equal(t, time.Unix(200, 0).UTC().Format(time.RFC3339Nano), got.UpdatedAt)
	assert.Equal(t, 1, control.getSecretCall)
	require.NotNil(t, control.lastGetSecretReq, "GetSecret must be called with a request")
	assert.Equal(t, "sec-1", control.lastGetSecretReq.SecretID)
}

func TestGetSecretMissingIsNotFound(t *testing.T) {
	_, err := (&Client{control: &fakeControl{}}).GetSecret(context.Background(), "missing")
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetSecretNilResponseIsNotFound(t *testing.T) {
	control := &fakeControl{nilResponse: true}

	_, err := (&Client{control: control}).GetSecret(context.Background(), "sec-1")
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Equal(t, 1, control.getSecretCall)
	require.NotNil(t, control.lastGetSecretReq)
	assert.Equal(t, "sec-1", control.lastGetSecretReq.SecretID)
}

func TestGetSecretFailureIsReturned(t *testing.T) {
	control := &fakeControl{getSecretErr: assert.AnError}
	_, err := (&Client{control: control}).GetSecret(context.Background(), "sec-1")
	require.ErrorIs(t, err, assert.AnError)
}

func TestClusterIDReturnsFirstCluster(t *testing.T) {
	control := &fakeControl{clusters: []*api.Cluster{{ID: "cluster-1"}, {ID: "cluster-2"}}}

	got, err := (&Client{control: control}).ClusterID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "cluster-1", got)
	assert.Equal(t, 1, control.clusterCalls)
}

func TestClusterIDEmptyIsNotFound(t *testing.T) {
	_, err := (&Client{control: &fakeControl{}}).ClusterID(context.Background())
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestClusterIDNilResponseIsNotFound(t *testing.T) {
	_, err := (&Client{control: &fakeControl{nilResponse: true}}).ClusterID(context.Background())
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestClusterIDFailureIsReturned(t *testing.T) {
	control := &fakeControl{clusterErr: assert.AnError}
	_, err := (&Client{control: control}).ClusterID(context.Background())
	require.ErrorIs(t, err, assert.AnError)
}

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
