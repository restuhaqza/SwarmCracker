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
