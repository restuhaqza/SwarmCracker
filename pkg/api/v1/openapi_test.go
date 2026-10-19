package v1

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
)

func loadOpenAPI(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "..", "api", "openapi.yaml"))
	require.NoError(t, err)
	require.NoError(t, doc.Validate(context.Background()))
	return doc
}

func validateAgainst(t *testing.T, doc *openapi3.T, name string, value any) {
	t.Helper()
	schema, ok := doc.Components.Schemas[name]
	require.True(t, ok, "schema %q missing", name)
	require.NotNil(t, schema.Value)
	require.NoError(t, schema.Value.VisitJSON(value), "document does not match %s", name)
}

func TestOpenAPIServiceListFixtures(t *testing.T) {
	doc := loadOpenAPI(t)

	fixture, err := os.ReadFile(filepath.Join("testdata", "service-list-empty.json"))
	require.NoError(t, err)

	var empty any
	require.NoError(t, json.Unmarshal(fixture, &empty))
	validateAgainst(t, doc, "ServiceList", empty)

	full := NewServiceList([]Service{{
		ID:       "svc-1",
		Name:     "web",
		Labels:   map[string]string{"team": "payments"},
		Revision: "17",
		Spec: ServiceSpec{
			Mode:     "replicated",
			Replicas: ptr[int64](3),
			Task: TaskSpec{
				Image:       "nginx:alpine",
				Command:     []string{"nginx"},
				Args:        []string{"-g", "daemon off;"},
				Env:         map[string]string{"APP_ENV": "prod"},
				VCPUs:       1,
				MemoryBytes: 512 << 20,
				Ports:       []Port{{Target: 80, Published: 8080, Protocol: "tcp", Mode: "ingress"}},
				Mounts:      []Mount{{Type: "volume", Source: "data", Target: "/data"}},
				Networks:    []NetworkAttachment{{ID: "net-1", Name: "app"}},
				Healthcheck: &Healthcheck{Test: []string{"CMD", "true"}, IntervalMs: 10000},
			},
			Update:    &UpdateConfig{Parallelism: 1, FailureAction: "pause", Order: "stop-first"},
			Placement: &Placement{Constraints: []string{"node.labels.zone==a"}},
		},
		Status: ServiceStatus{
			DesiredTasks: 3, RunningTasks: 3, Converged: true,
			Endpoint: &Endpoint{Ports: []Port{{Target: 80, Published: 8080}}},
		},
	}})
	data, err := json.Marshal(full)
	require.NoError(t, err)
	var value any
	require.NoError(t, json.Unmarshal(data, &value))
	validateAgainst(t, doc, "ServiceList", value)
}

func TestOpenAPITaskListFixtures(t *testing.T) {
	doc := loadOpenAPI(t)

	fixture, err := os.ReadFile(filepath.Join("testdata", "task-list-empty.json"))
	require.NoError(t, err)

	var empty any
	require.NoError(t, json.Unmarshal(fixture, &empty))
	validateAgainst(t, doc, "TaskList", empty)

	full := NewTaskList([]Task{
		{
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
			CreatedAt:    "2026-10-19T10:01:00Z",
			UpdatedAt:    "2026-10-19T10:01:04Z",
			Image:        "nginx:1.27-alpine",
			Addresses:    []string{"10.0.1.7"},
			Ports:        []Port{{Target: 80, Protocol: "tcp"}},
			ExitCode:     ptr[int32](137),
			Health: &TaskHealth{
				Status:        "healthy",
				FailingStreak: 2,
				Output:        "ok",
				Command:       "curl -f http://localhost/",
				CheckedAt:     "2026-10-19T10:04:10Z",
			},
		},
		{
			ID:           "t2def",
			Revision:     "43",
			ServiceID:    "q1svc",
			State:        "failed",
			DesiredState: "shutdown",
			Message:      "task failed",
			Error:        "signal: killed",
		},
	})
	data, err := json.Marshal(full)
	require.NoError(t, err)
	var value any
	require.NoError(t, json.Unmarshal(data, &value))
	validateAgainst(t, doc, "TaskList", value)
}

func TestOpenAPIErrorDocument(t *testing.T) {
	doc := loadOpenAPI(t)
	value := NewErrorDocument(Error{
		Code:      "conflict",
		Message:   "stale revision",
		RequestID: "req_123",
		Retryable: false,
		Details:   map[string]any{"currentRevision": "19"},
	})
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var generic any
	require.NoError(t, json.Unmarshal(data, &generic))
	validateAgainst(t, doc, "ErrorDocument", generic)
}
