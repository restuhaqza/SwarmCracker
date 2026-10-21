package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTaskListEmpty(t *testing.T) {
	data, err := json.Marshal(NewTaskList(nil))
	require.NoError(t, err)

	require.JSONEq(t, `{
		"schemaVersion": 1,
		"kind": "TaskList",
		"items": [],
		"next": null
	}`, string(data))
}

func TestTaskListEmptyGoldenFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "task-list-empty.json"))
	require.NoError(t, err)

	data, err := json.Marshal(NewTaskList(nil))
	require.NoError(t, err)
	require.JSONEq(t, string(fixture), string(data))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	items, ok := doc["items"].([]any)
	require.True(t, ok, "items must be a JSON array")
	assert.Empty(t, items)
	assert.Nil(t, doc["next"])
	assert.Equal(t, float64(SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, KindTaskList, doc["kind"])
}

func TestTaskDocumentEnvelope(t *testing.T) {
	task := Task{
		ID:           "t1abc",
		Name:         "web.1.abc123",
		Revision:     "42",
		ServiceID:    "q1svc",
		ServiceName:  "web",
		NodeID:       "n2node",
		NodeName:     "node-2",
		Slot:         1,
		State:        "running",
		DesiredState: "running",
		Image:        "nginx:1.27-alpine",
		Addresses:    []string{"10.0.1.7"},
		Ports:        []Port{{Target: 80, Protocol: "tcp"}},
	}

	data, err := json.Marshal(NewTaskDocument(task))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.Equal(t, float64(SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, KindTask, doc["kind"])
	assert.Equal(t, "web.1.abc123", doc["name"])
	assert.NotContains(t, doc, "health", "health is phase 3 and must be omitted when nil")
}

func TestTaskHealthIsOptionalAndShaped(t *testing.T) {
	withHealth := Task{
		ID:           "t1abc",
		Revision:     "42",
		ServiceID:    "q1svc",
		State:        "running",
		DesiredState: "running",
		ExitCode:     ptr[int32](137),
		Health: &TaskHealth{
			Status:        "healthy",
			FailingStreak: 2,
			Output:        "ok",
			Command:       "curl -f http://localhost/",
			CheckedAt:     "2026-10-19T10:04:10Z",
		},
	}

	data, err := json.Marshal(withHealth)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	health, ok := doc["health"].(map[string]any)
	require.True(t, ok, "health must be a JSON object")
	assert.Equal(t, "healthy", health["status"])
	assert.Equal(t, float64(2), health["failingStreak"])
	assert.Equal(t, float64(137), doc["exitCode"])

	bare := Task{
		ID:           "t2def",
		Revision:     "43",
		ServiceID:    "q1svc",
		State:        "complete",
		DesiredState: "shutdown",
	}
	data, err = json.Marshal(bare)
	require.NoError(t, err)

	var bareDoc map[string]any
	require.NoError(t, json.Unmarshal(data, &bareDoc))
	assert.NotContains(t, bareDoc, "exitCode", "exitCode is omitted unless set")
	assert.NotContains(t, bareDoc, "health", "health is omitted unless set")
}
