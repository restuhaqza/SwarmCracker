package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewVersionInfoEmptySlices(t *testing.T) {
	data, err := json.Marshal(NewVersionInfo(nil, "v0.12.0", "1", "cluster-1", nil))
	require.NoError(t, err)

	require.JSONEq(t, `{
		"schemaVersion": 1,
		"kind": "VersionInfo",
		"apiVersions": [],
		"serverVersion": "v0.12.0",
		"grpcProtocolVersion": "1",
		"clusterId": "cluster-1",
		"capabilities": []
	}`, string(data))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	apiVersions, ok := doc["apiVersions"].([]any)
	require.True(t, ok, "apiVersions must be an array, got %T", doc["apiVersions"])
	assert.Empty(t, apiVersions)

	capabilities, ok := doc["capabilities"].([]any)
	require.True(t, ok, "capabilities must be an array, got %T", doc["capabilities"])
	assert.Empty(t, capabilities)

	assert.Equal(t, KindVersionInfo, doc["kind"])
	assert.Equal(t, float64(SchemaVersion), doc["schemaVersion"])
}

func TestNewVersionInfoPopulated(t *testing.T) {
	value := NewVersionInfo(
		[]string{"v1"},
		"v0.12.0",
		"1",
		"cluster-1",
		[]string{"http-read"},
	)

	assert.Equal(t, SchemaVersion, value.SchemaVersion)
	assert.Equal(t, KindVersionInfo, value.Kind)
	assert.Equal(t, []string{"v1"}, value.APIVersions)
	assert.Equal(t, "v0.12.0", value.ServerVersion)
	assert.Equal(t, "1", value.GRPCProtocolVersion)
	assert.Equal(t, "cluster-1", value.ClusterID)
	assert.Equal(t, []string{"http-read"}, value.Capabilities)
}

func TestNewVersionInfoOmitsEmptyClusterID(t *testing.T) {
	data, err := json.Marshal(NewVersionInfo([]string{"v1"}, "v0.12.0", "1", "", []string{"http-read"}))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.NotContains(t, doc, "clusterId")
}
