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

// TestSandboxPortDocumentsAreValidatedAgainstTheSchema is the exposure half of the wire
// contract, held to the normative OpenAPI schema rather than to a copy of itself: the
// request a caller posts, the exposure it gets back, the port on the sandbox's own
// document, and the deadline that says when the exposure stops serving. A schema that
// drifted from the documents would let a client generate code for fields the node never
// sends — and the token is the field that must not appear on a read.
func TestSandboxPortDocumentsAreValidatedAgainstTheSchema(t *testing.T) {
	doc := loadOpenAPI(t)

	exposed := SandboxPort{
		GuestPort: 8080,
		Address:   "127.203.229.82:8080",
		URL:       "http://127.203.229.82:8080/",
		ExpiresAt: "2026-11-16T11:01:47Z",
	}
	validateAgainst(t, doc, "SandboxPort", toJSONValue(t, exposed))

	request := SandboxPortRequest{GuestPort: 8080, TTLSeconds: 600}
	validateAgainst(t, doc, "SandboxPortRequest", toJSONValue(t, request))

	exposure := NewSandboxPortExposure("sbx1", exposed, "a-preview-token", exposed.URL+"?token=a-preview-token")
	assert.Equal(t, KindSandboxPort, exposure.Kind)
	validateAgainst(t, doc, "SandboxPortExposure", toJSONValue(t, exposure))

	// A sandbox that names its exposures, and a teardown that reports the listeners it
	// closed, both stay inside the sandbox document's schema.
	sandbox := NewSandboxDocument(Sandbox{ID: "sbx1", State: "ready", Ports: []SandboxPort{exposed}})
	validateAgainst(t, doc, "SandboxDocument", toJSONValue(t, sandbox))
	removal := NewSandboxRemoval("sbx1", SandboxFreed{PreviewsClosed: 2, RecordRemoved: true})
	validateAgainst(t, doc, "SandboxRemoval", toJSONValue(t, removal))

	// The read model carries no credential: the token is in the exposure response, which
	// is returned once, and a reader of the sandbox document gets the address only.
	data, err := json.Marshal(sandbox)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "a-preview-token")
	assert.NotContains(t, string(data), "tokenHash", "not even the digest crosses the API")
	assert.Contains(t, string(data), `"ports":[{"guestPort":8080`)

	require.NotNil(t, doc.Paths.Find("/v1/sandboxes/{id}/ports"))
	require.NotNil(t, doc.Paths.Find("/v1/sandboxes/{id}/ports/{port}"))
}

// toJSONValue renders a document as the generic value the schema validator takes.
func toJSONValue(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var out any
	require.NoError(t, json.Unmarshal(data, &out))
	return out
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

// TestNewCheckpointListAlwaysCarriesAnArray pins the envelope a client can rely on: a node
// with no checkpoints answers with an empty array, not with a missing field a reader has to
// distinguish from "not asked".
func TestNewCheckpointListAlwaysCarriesAnArray(t *testing.T) {
	doc := NewCheckpointList(nil)

	require.NotNil(t, doc.Items)
	assert.Empty(t, doc.Items)
	assert.Equal(t, KindCheckpointList, doc.Kind)
	assert.Equal(t, SchemaVersion, doc.SchemaVersion)
}

// TestSandboxTokenSecretIsNamedTokenOnTheWire pins the one field whose name is not the
// one a caller would guess.
//
// The Go field is Secret, the JSON field is "token", and a harness that decoded the
// document with the Go name read an empty bearer token — which the API then refused for
// being empty. The refusal was correct and the reason was not: a criterion meant to prove
// a sandbox-scoped token cannot leave its sandbox was passing on a token that never
// existed. Nothing caught it because the existing assertions name the Go field, which
// cannot drift from itself.
//
// So this reads the encoded document, and the key is the contract: renaming the tag
// without renaming the field (or the reverse) fails here rather than in a host run.
func TestSandboxTokenSecretIsNamedTokenOnTheWire(t *testing.T) {
	token := NewSandboxToken("tok_1", "sandbox sbx1", "sbx1", "sc_secret", "", []string{"sandboxes:read"})

	data, err := json.Marshal(token)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	assert.Equal(t, "sc_secret", doc["token"], "the bearer secret travels under \"token\":\n%s", data)
	assert.NotContains(t, doc, "secret", "and not under the Go field's name:\n%s", data)
	assert.NotContains(t, doc, "Secret")

	// The rest of the document a caller reads, so a rename of any of them is caught here
	// too: the harness and the SDK both decode these keys.
	for _, key := range []string{"schemaVersion", "kind", "id", "name", "sandbox", "scopes"} {
		assert.Contains(t, doc, key, "the token document carries %q:\n%s", key, data)
	}
	// The secret is never on a read: this document exists only in its creation response.
	assert.Equal(t, KindSandboxToken, doc["kind"])
}
