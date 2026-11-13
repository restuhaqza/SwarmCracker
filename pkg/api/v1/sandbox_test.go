package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSandboxDocumentsCarryTheirEnvelope pins the three documents a sandbox control
// plane serves: each one names its kind, and the sandbox's own host layout — its jail,
// its disk, the node's copy of its guest credential — has no field to travel in.
func TestSandboxDocumentsCarryTheirEnvelope(t *testing.T) {
	doc := NewSandboxDocument(Sandbox{ID: "sbx1", State: "ready", Template: "base@1", GuestIP: "10.63.0.2"})
	assert.Equal(t, SchemaVersion, doc.SchemaVersion)
	assert.Equal(t, KindSandbox, doc.Kind)
	assert.Equal(t, "sbx1", doc.ID)

	list := NewSandboxList(nil)
	assert.Equal(t, KindSandboxList, list.Kind)
	assert.NotNil(t, list.Items, "an empty list is an empty array, never a missing one")
	assert.Empty(t, list.Items)

	removal := NewSandboxRemoval("sbx1", SandboxFreed{ProcessStopped: true, RecordRemoved: true})
	assert.Equal(t, KindSandboxRemoval, removal.Kind)
	assert.Equal(t, "sbx1", removal.Sandbox)
	assert.True(t, removal.Freed.ProcessStopped)
	assert.True(t, removal.Freed.RecordRemoved)

	token := NewSandboxToken("tok_1", "sandbox sbx1", "sbx1", "sc_secret", "", []string{"sandboxes:read"})
	assert.Equal(t, KindSandboxToken, token.Kind)
	assert.Equal(t, "sbx1", token.Sandbox)
	assert.Equal(t, "sc_secret", token.Secret)

	// What a sandbox document must not carry: the paths a node keeps to itself.
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	for _, hostOnly := range []string{"disk", "jail", "agentTokenFile", "netns"} {
		assert.NotContains(t, string(data), hostOnly, "host layout stays on the node")
	}
}

// TestCheckpointDocumentsUseTheContractsKeys pins the JSON *keys*, not only the values: a
// field without a tag marshals as its Go name, which a client reading "kind" never sees.
// The checkpoint documents are the ones this caught.
func TestCheckpointDocumentsUseTheContractsKeys(t *testing.T) {
	doc := NewCheckpointDocument(Checkpoint{Name: "c1", Sandbox: "sbx-1", QuiesceMode: "fsfreeze"})
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw, "kind")
	assert.NotContains(t, raw, "Kind")
	assert.Equal(t, KindCheckpoint, raw["kind"])
	assert.Equal(t, "c1", raw["name"])

	list := NewCheckpointList([]Checkpoint{{Name: "c1", Sandbox: "sbx-1"}})
	data, err = json.Marshal(list)
	require.NoError(t, err)
	raw = nil
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw, "kind")
	assert.NotContains(t, raw, "Kind")

	removal := NewCheckpointRemoval("c1")
	data, err = json.Marshal(removal)
	require.NoError(t, err)
	raw = nil
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw, "kind")
	assert.NotContains(t, raw, "Kind")

	// The sandbox list's fork name keeps the same rule.
	forks := NewSandboxList(nil)
	forks.Checkpoint = "c1"
	data, err = json.Marshal(forks)
	require.NoError(t, err)
	raw = nil
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, "c1", raw["checkpoint"])
	assert.NotContains(t, raw, "Checkpoint")
}
