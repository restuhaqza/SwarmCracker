package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewConfigListEmpty(t *testing.T) {
	data, err := json.Marshal(NewConfigList(nil))
	require.NoError(t, err)

	require.JSONEq(t, `{
		"schemaVersion": 1,
		"kind": "ConfigList",
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

func TestConfigListGoldenFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "config-list-empty.json"))
	require.NoError(t, err)

	data, err := json.Marshal(NewConfigList(nil))
	require.NoError(t, err)
	require.JSONEq(t, string(fixture), string(data))
}

func TestConfigDocumentEnvelope(t *testing.T) {
	config := Config{
		ID:        "cfg-1",
		Name:      "app",
		Revision:  "3",
		CreatedAt: "2026-10-19T10:00:00Z",
		UpdatedAt: "2026-10-19T11:00:00Z",
		Labels:    map[string]string{"team": "payments"},
	}

	data, err := json.Marshal(NewConfigDocument(config))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.Equal(t, float64(SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, KindConfig, doc["kind"])
	assert.Equal(t, "cfg-1", doc["id"])
	assert.Equal(t, "app", doc["name"])
	assert.NotContains(t, doc, "data", "config payload must never be serialized")
	assert.NotContains(t, doc, "value", "config payload must never be serialized")
	assert.NotContains(t, doc, "items", "a document is not a list")
}

// TestConfigDocumentDataRoundTrips pins the one field that carries a config
// payload: it only appears when a value getter populated it, and never as an
// empty string.
func TestConfigDocumentDataRoundTrips(t *testing.T) {
	payload := "listen 8080"
	doc := NewConfigDocument(Config{ID: "cfg-1", Name: "app", Revision: "3", Data: &payload})

	data, err := json.Marshal(doc)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, payload, decoded["data"])

	empty := ""
	data, err = json.Marshal(NewConfigDocument(Config{ID: "cfg-2", Data: &empty}))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "", decoded["data"], "an empty payload must still be distinguishable from absent")
}
