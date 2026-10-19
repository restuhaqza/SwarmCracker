package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewNodeListEmpty(t *testing.T) {
	data, err := json.Marshal(NewNodeList(nil))
	require.NoError(t, err)

	require.JSONEq(t, `{
		"schemaVersion": 1,
		"kind": "NodeList",
		"items": [],
		"next": null
	}`, string(data))
}

func TestNodeListEmptyGoldenFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "node-list-empty.json"))
	require.NoError(t, err)

	data, err := json.Marshal(NewNodeList(nil))
	require.NoError(t, err)
	require.JSONEq(t, string(fixture), string(data))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	items, ok := doc["items"].([]any)
	require.True(t, ok, "items must be a JSON array")
	assert.Empty(t, items)
	assert.Nil(t, doc["next"])
	assert.Equal(t, float64(SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, KindNodeList, doc["kind"])
}

func TestNodeDocumentEnvelope(t *testing.T) {
	worker := Node{
		ID:           "n1worker",
		Hostname:     "node-1",
		Revision:     "9",
		CreatedAt:    "2026-10-18T08:00:00Z",
		UpdatedAt:    "2026-10-19T10:03:00Z",
		Role:         "worker",
		Membership:   "accepted",
		Availability: "active",
		State:        "ready",
		Address:      "192.168.1.12",
		Labels:       map[string]string{"zone": "a"},
		Resources:    &NodeResources{CPUs: 8, MemoryBytes: 16 << 30},
	}

	data, err := json.Marshal(NewNodeDocument(worker))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.Equal(t, float64(SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, KindNode, doc["kind"])
	assert.Equal(t, "node-1", doc["hostname"])
	assert.Equal(t, "worker", doc["role"])
	assert.NotContains(t, doc, "manager", "workers must omit manager, not emit null")
	assert.NotContains(t, doc, "agentVersion", "agentVersion is omitted when unreported")

	resources, ok := doc["resources"].(map[string]any)
	require.True(t, ok, "resources must be a JSON object")
	assert.Equal(t, float64(8), resources["cpus"])
	assert.Equal(t, float64(16<<30), resources["memoryBytes"])
}

func TestNodeManagerIsEmittedWhenPresent(t *testing.T) {
	manager := Node{
		ID:           "n2manager",
		Hostname:     "node-2",
		Revision:     "10",
		Role:         "manager",
		Membership:   "accepted",
		Availability: "active",
		State:        "ready",
		Manager: &NodeManager{
			Leader:    true,
			Reachable: true,
			Address:   "192.168.1.11:4242",
			RaftID:    1,
		},
	}

	data, err := json.Marshal(NewNodeDocument(manager))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	got, ok := doc["manager"].(map[string]any)
	require.True(t, ok, "manager must be a JSON object")
	assert.Equal(t, true, got["leader"])
	assert.Equal(t, true, got["reachable"])
	assert.Equal(t, "192.168.1.11:4242", got["address"])
	assert.Equal(t, float64(1), got["raftId"])

	// A non-leader, unreachable manager still emits the object and both
	// booleans: the key is about presence of manager status, not its values.
	data, err = json.Marshal(Node{
		ID:           "n3demoted",
		Revision:     "11",
		Role:         "manager",
		Membership:   "accepted",
		Availability: "pause",
		State:        "down",
		Manager:      &NodeManager{Leader: false, Reachable: false},
	})
	require.NoError(t, err)
	doc = nil
	require.NoError(t, json.Unmarshal(data, &doc))

	got, ok = doc["manager"].(map[string]any)
	require.True(t, ok, "manager must be emitted even when both booleans are false")
	assert.Equal(t, false, got["leader"])
	assert.Equal(t, false, got["reachable"])
	assert.NotContains(t, got, "address")
	assert.NotContains(t, got, "raftId")
}
