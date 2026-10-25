package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSnapshotDocumentShape pins the public Snapshot document: the v1 envelope,
// the camel-cased fields the contract names and, just as importantly, the paths
// that must never cross the wire.
func TestSnapshotDocumentShape(t *testing.T) {
	doc := NewSnapshotDocument(Snapshot{
		ID:         "snap-a1b2c3d4e5f60718",
		TaskID:     "task-1",
		ServiceID:  "svc-1",
		NodeID:     "node-1",
		CreatedAt:  "2026-10-24T12:00:00.123456789Z",
		SizeBytes:  268435456,
		MemoryMB:   256,
		VCPUCount:  2,
		RootfsPath: "/var/lib/firecracker/rootfs/task-1",
		Checksum:   "0f343b0931126a20f133d67c2b018a3b",
	})

	require.Equal(t, SchemaVersion, doc.SchemaVersion)
	require.Equal(t, KindSnapshot, doc.Kind)

	data, err := json.Marshal(doc)
	require.NoError(t, err)

	var value map[string]any
	require.NoError(t, json.Unmarshal(data, &value))

	assert.Equal(t, KindSnapshot, value["kind"])
	assert.Equal(t, float64(SchemaVersion), value["schemaVersion"])
	assert.Equal(t, "snap-a1b2c3d4e5f60718", value["id"])
	assert.Equal(t, "task-1", value["taskId"])
	assert.Equal(t, "svc-1", value["serviceId"])
	assert.Equal(t, "node-1", value["nodeId"])
	assert.Equal(t, "2026-10-24T12:00:00.123456789Z", value["createdAt"])
	assert.Equal(t, float64(268435456), value["sizeBytes"])
	assert.Equal(t, float64(256), value["memoryMB"])
	assert.Equal(t, float64(2), value["vcpuCount"])
	assert.Equal(t, "/var/lib/firecracker/rootfs/task-1", value["rootfsPath"])
	assert.Equal(t, "0f343b0931126a20f133d67c2b018a3b", value["checksum"])

	for _, forbidden := range []string{"memoryPath", "statePath", "memory_path", "state_path"} {
		assert.NotContains(t, value, forbidden,
			"a snapshot's memory and state files are node-local and must never be published")
	}
}

// TestSnapshotListNeverNilItems pins the empty list: a caller decoding items must
// not have to distinguish [] from null.
func TestSnapshotListNeverNilItems(t *testing.T) {
	data, err := json.Marshal(NewSnapshotList(nil))
	require.NoError(t, err)

	var value map[string]any
	require.NoError(t, json.Unmarshal(data, &value))

	assert.Equal(t, KindSnapshotList, value["kind"])
	assert.Equal(t, float64(SchemaVersion), value["schemaVersion"])
	assert.Equal(t, []any{}, value["items"])
	assert.Nil(t, value["next"], "next is present and null on the last page")
	assert.NotContains(t, value, "partial", "an unpartial list omits the key")
}

// TestSnapshotListCarriesPages pins the list envelope's page fields.
func TestSnapshotListCarriesPages(t *testing.T) {
	next := "cursor-2"
	doc := NewSnapshotList([]Snapshot{{ID: "snap-1"}})
	doc.Next = &next
	doc.Partial = []Partial{{NodeID: "node-1"}}

	data, err := json.Marshal(doc)
	require.NoError(t, err)

	var value map[string]any
	require.NoError(t, json.Unmarshal(data, &value))

	assert.Equal(t, "cursor-2", value["next"])
	items, ok := value["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	partial, ok := value["partial"].([]any)
	require.True(t, ok)
	require.Len(t, partial, 1)
}

// TestSnapshotOmitsUnsetOptionalFields pins the schema's optional keys: an
// omitted field is absent, not "" or 0.
func TestSnapshotOmitsUnsetOptionalFields(t *testing.T) {
	data, err := json.Marshal(NewSnapshotDocument(Snapshot{ID: "snap-1"}))
	require.NoError(t, err)

	var value map[string]any
	require.NoError(t, json.Unmarshal(data, &value))

	assert.Equal(t, "snap-1", value["id"])
	for _, optional := range []string{
		"taskId", "serviceId", "nodeId", "createdAt", "sizeBytes",
		"memoryMB", "vcpuCount", "rootfsPath", "checksum",
	} {
		assert.NotContains(t, value, optional, "%s must be omitted when unset", optional)
	}
}

// TestSnapshotCreateRequestTags pins the create body's wire names: memoryMB is
// camel-cased even though the internal snapshot field is MemoryMB.
func TestSnapshotCreateRequestTags(t *testing.T) {
	data, err := json.Marshal(SnapshotCreateRequest{
		VCPUs:      2,
		MemoryMB:   1024,
		RootfsPath: "/var/lib/firecracker/rootfs/task-1",
	})
	require.NoError(t, err)

	var value map[string]any
	require.NoError(t, json.Unmarshal(data, &value))

	assert.Equal(t, float64(2), value["vcpus"])
	assert.Equal(t, float64(1024), value["memoryMB"])
	assert.Equal(t, "/var/lib/firecracker/rootfs/task-1", value["rootfsPath"])
}
