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
