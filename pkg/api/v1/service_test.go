package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServiceListEmpty(t *testing.T) {
	data, err := json.Marshal(NewServiceList(nil))
	require.NoError(t, err)

	require.JSONEq(t, `{
		"schemaVersion": 1,
		"kind": "ServiceList",
		"items": [],
		"next": null
	}`, string(data))
}

func TestServiceListEmptyGoldenFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "service-list-empty.json"))
	require.NoError(t, err)

	data, err := json.Marshal(NewServiceList(nil))
	require.NoError(t, err)
	require.JSONEq(t, string(fixture), string(data))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	items, ok := doc["items"].([]any)
	require.True(t, ok, "items must be a JSON array")
	assert.Empty(t, items)
	assert.Nil(t, doc["next"])
	assert.Equal(t, float64(SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, KindServiceList, doc["kind"])
}

func TestServiceDocumentEnvelope(t *testing.T) {
	svc := Service{
		ID:       "svc-1",
		Name:     "web",
		Revision: "17",
		Spec: ServiceSpec{
			Mode:     "replicated",
			Replicas: ptr[int64](3),
			Task:     TaskSpec{Image: "nginx:alpine"},
		},
		Status: ServiceStatus{DesiredTasks: 3, RunningTasks: 3, Converged: true},
	}

	data, err := json.Marshal(NewServiceDocument(svc))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.Equal(t, float64(SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, KindService, doc["kind"])
	assert.Equal(t, "web", doc["name"])
	assert.NotContains(t, doc, "registryAuth", "credentials must never be serialized")
}

func ptr[T any](v T) *T { return &v }
