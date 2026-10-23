package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewNetworkListEmpty(t *testing.T) {
	data, err := json.Marshal(NewNetworkList(nil))
	require.NoError(t, err)

	require.JSONEq(t, `{
		"schemaVersion": 1,
		"kind": "NetworkList",
		"items": [],
		"next": null
	}`, string(data))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	items, ok := doc["items"].([]any)
	require.True(t, ok)
	assert.Empty(t, items)
	assert.Nil(t, doc["next"])
}

func TestNetworkListGoldenFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "network-list-empty.json"))
	require.NoError(t, err)

	data, err := json.Marshal(NewNetworkList(nil))
	require.NoError(t, err)
	require.JSONEq(t, string(fixture), string(data))
}

func TestNetworkDocumentEnvelope(t *testing.T) {
	network := Network{
		ID:         "net-1",
		Name:       "app",
		Revision:   "2",
		CreatedAt:  "2026-10-19T10:00:00Z",
		Driver:     "overlay",
		IPAM:       &NetworkIPAM{Subnet: "10.0.1.0/24", Gateway: "10.0.1.1"},
		Labels:     map[string]string{"tier": "data"},
		Attachable: true,
	}

	data, err := json.Marshal(NewNetworkDocument(network))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.Equal(t, float64(SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, KindNetwork, doc["kind"])
	assert.Equal(t, "net-1", doc["id"])
	assert.Equal(t, "app", doc["name"])
	assert.Equal(t, "overlay", doc["driver"])
	assert.NotContains(t, doc, "items", "a document is not a list")
}
