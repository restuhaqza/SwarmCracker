package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSecretListEmpty(t *testing.T) {
	data, err := json.Marshal(NewSecretList(nil))
	require.NoError(t, err)

	require.JSONEq(t, `{
		"schemaVersion": 1,
		"kind": "SecretList",
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

func TestSecretListGoldenFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "secret-list-empty.json"))
	require.NoError(t, err)

	data, err := json.Marshal(NewSecretList(nil))
	require.NoError(t, err)
	require.JSONEq(t, string(fixture), string(data))
}

func TestSecretDocumentEnvelope(t *testing.T) {
	secret := Secret{
		ID:        "sec-1",
		Name:      "token",
		Revision:  "4",
		CreatedAt: "2026-10-19T10:00:00Z",
		UpdatedAt: "2026-10-19T11:00:00Z",
		Labels:    map[string]string{"env": "prod"},
		Driver:    "external",
	}

	data, err := json.Marshal(NewSecretDocument(secret))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.Equal(t, float64(SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, KindSecret, doc["kind"])
	assert.Equal(t, "sec-1", doc["id"])
	assert.Equal(t, "token", doc["name"])
	assert.NotContains(t, doc, "data", "secret payload must never be serialized")
	assert.NotContains(t, doc, "items", "a document is not a list")
}
