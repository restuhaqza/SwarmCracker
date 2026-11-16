package v1

// The sandbox file and status documents against the normative spec: the Go types must
// validate against the schemas an SDK is generated from, and the routes must state what a
// caller depends on — which scope, which media types, and what a template without `untar`
// is told.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAPISandboxFileDocumentsMatchTheirSchemas validates the two documents this
// package builds against the schemas of the same names, so a field the spec does not know
// about (or one it requires and the code omits) fails here rather than in a client.
func TestOpenAPISandboxFileDocumentsMatchTheirSchemas(t *testing.T) {
	doc := loadOpenAPI(t)

	for _, tc := range []struct {
		schema string
		value  any
	}{
		{
			schema: "SandboxFileWrite",
			value:  NewSandboxFileWrite("workspace/src", 4096, 12, []string{"workspace/src/fifo is a fifo"}),
		},
		{
			schema: "SandboxFileWrite",
			value:  NewSandboxFileWrite("workspace/main.go", 14, 0, nil),
		},
		{
			schema: "SandboxStatus",
			value: NewSandboxStatus(
				Sandbox{ID: "sbx1", State: "ready", Template: "base@1", GuestIP: "10.63.0.2"},
				SandboxGuestInfo{
					Hostname: "sbx1", MachineID: "m1", KernelRelease: "6.1.0", Machine: "x86_64",
					OSName: "Alpine Linux v3.20", OSID: "alpine", CPUs: 2, MemoryTotalBytes: 512 << 20,
					UptimeSeconds: 12.5, Protocol: "2", AgentVersion: "1.2.3", BuildID: "abc", BootID: "boot",
					Tools: []string{"sh"},
				},
				SandboxGuestNet{
					Interfaces: []SandboxNetInterface{{
						Name: "eth0", Index: 2, MAC: "02:00:00:00:00:01", MTU: 1500, Up: true,
						Addresses: []string{"10.63.0.2/30"},
					}},
					Routes:    []SandboxNetRoute{{Family: "ipv4", Destination: "default", Gateway: "10.63.0.1", Device: "eth0"}},
					Resolvers: []string{"1.1.1.1"},
				},
			),
		},
		{
			schema: "SandboxStatus",
			value:  NewSandboxStatus(Sandbox{ID: "sbx1", State: "ready"}, SandboxGuestInfo{}, SandboxGuestNet{}),
		},
	} {
		data, err := json.Marshal(tc.value)
		require.NoError(t, err)
		var generic any
		require.NoError(t, json.Unmarshal(data, &generic))
		validateAgainst(t, doc, tc.schema, generic)
	}
}

// TestSandboxFileDocumentsUseTheContractsKeys pins the JSON keys rather than the values: a
// field without a tag marshals as its Go name, which a client reading "kind" never sees.
func TestSandboxFileDocumentsUseTheContractsKeys(t *testing.T) {
	write := NewSandboxFileWrite("workspace/main.go", 14, 0, nil)
	data, err := json.Marshal(write)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, KindSandboxFileWrite, raw["kind"])
	assert.Equal(t, "workspace/main.go", raw["path"])
	assert.Contains(t, raw, "schemaVersion")
	assert.NotContains(t, raw, "Kind")
	assert.NotContains(t, raw, "Bytes")

	status := NewSandboxStatus(Sandbox{ID: "sbx1", State: "ready"}, SandboxGuestInfo{Hostname: "sbx1"}, SandboxGuestNet{})
	data, err = json.Marshal(status)
	require.NoError(t, err)
	raw = map[string]any{}
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, KindSandboxStatus, raw["kind"])
	assert.Equal(t, "sbx1", raw["id"], "the record travels beside the guest's report, not nested")
	assert.Contains(t, raw, "info")
	assert.Contains(t, raw, "net")
	// Host layout still stays on the node.
	assert.NotContains(t, string(data), "disk")
	assert.NotContains(t, string(data), "jail")
}

// TestOpenAPISandboxFileRoutes pins the routes as calls: the scope each requires, the media
// types each reads and writes, and the two answers a caller has to be able to tell apart
// (an unsupported body, and a template that cannot take a tree).
func TestOpenAPISandboxFileRoutes(t *testing.T) {
	doc := loadOpenAPI(t)

	files := doc.Paths.Find("/v1/sandboxes/{id}/files")
	require.NotNil(t, files, "the sandbox files path is missing")
	require.NotNil(t, files.Get, "files GET is missing")
	require.NotNil(t, files.Put, "files PUT is missing")

	require.Contains(t, files.Get.Description, "sandboxes:files", "the read must name its scope")
	require.Contains(t, files.Get.Description, "sandbox_selector_mismatch",
		"the read must state that a per-sandbox token is required")
	require.Contains(t, files.Get.Description, "unsupported_file_kind",
		"the read must name what happens to a link, a device or a socket")
	require.Contains(t, files.Get.Description, "sandbox.files", "the audit action must be documented")

	path := findParameter(t, files.Get.Parameters, "path")
	require.True(t, path.Required, "a copy without a path names the file root")

	read := files.Get.Responses.Value("200")
	require.NotNil(t, read, "files GET 200 is missing")
	for _, media := range []string{"application/octet-stream", "application/x-tar"} {
		content := read.Value.Content.Get(media)
		require.NotNilf(t, content, "a read must be able to answer %s", media)
		require.Equal(t, "binary", content.Schema.Value.Format, "%s is raw bytes", media)
	}
	require.Nil(t, read.Value.Content.Get(v1MediaType), "a file read is not a JSON document")

	require.Contains(t, files.Put.Description, "sandboxes:files", "the write must name its scope")
	require.Contains(t, files.Put.Description, "template_capability_missing",
		"the write must state what a template without untar is told")
	require.Contains(t, files.Put.Description, "untar", "the write must name the capability")
	require.Contains(t, files.Put.Description, "sandbox.files", "the audit action must be documented")

	require.NotNil(t, files.Put.RequestBody)
	require.True(t, files.Put.RequestBody.Value.Required)
	for _, media := range []string{"application/octet-stream", "application/x-tar"} {
		require.NotNilf(t, files.Put.RequestBody.Value.Content.Get(media),
			"a write must accept %s", media)
	}
	mode := findParameter(t, files.Put.Parameters, "mode")
	require.False(t, mode.Required, "the mode has a default")
	requireTypedResponseFor(t, files.Put, "200", "SandboxFileWrite")
	for _, status := range []string{"400", "401", "403", "404", "409", "415", "429", "501"} {
		response := files.Put.Responses.Value(status)
		require.NotNilf(t, response, "files PUT %s response missing", status)
		require.NotNilf(t, response.Value.Content.Get(v1MediaType),
			"files PUT %s must be declared in %s", status, v1MediaType)
	}

	logs := doc.Paths.Find("/v1/sandboxes/{id}/logs")
	require.NotNil(t, logs, "the sandbox logs path is missing")
	require.NotNil(t, logs.Get, "logs GET is missing")
	require.Contains(t, logs.Get.Description, "sandboxes:read", "the console read must name its scope")
	require.Contains(t, logs.Get.Description, "logs_unavailable",
		"the console read must state what a node without a log directory answers")
	require.Contains(t, logs.Get.Description, "sandbox.logs", "the audit action must be documented")
	console := logs.Get.Responses.Value("200").Value.Content.Get("text/plain")
	require.NotNil(t, console, "a console read must answer text/plain")
	require.True(t, console.Schema.Value.Type.Is("string"), "the console is a byte stream")

	status := doc.Paths.Find("/v1/sandboxes/{id}/status")
	require.NotNil(t, status, "the sandbox status path is missing")
	require.NotNil(t, status.Get, "status GET is missing")
	require.Contains(t, status.Get.Description, "sandboxes:read", "status must name its scope")
	require.Contains(t, status.Get.Description, "sandbox.status", "the audit action must be documented")
	require.Contains(t, status.Get.Description, "409",
		"status must state that a sandbox that is not ready cannot be read")
	requireTypedResponseFor(t, status.Get, "200", "SandboxStatus")

	// The status document carries the record the sandbox route serves, so a caller reads
	// its state from the same place in both answers.
	properties := documentSchemaProperties(t, doc, "SandboxStatus")
	require.Contains(t, properties, "info")
	require.Contains(t, properties, "net")
	schema := doc.Components.Schemas["SandboxStatus"]
	require.NotEmpty(t, schema.Value.AllOf)
	require.Equal(t, "#/components/schemas/Sandbox", schema.Value.AllOf[0].Ref,
		"the status document is the sandbox document plus the guest's reports")
}
