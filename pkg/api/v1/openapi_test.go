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

func TestOpenAPINetworkListFixture(t *testing.T) {
	doc := loadOpenAPI(t)

	fixture, err := os.ReadFile(filepath.Join("testdata", "network-list-empty.json"))
	require.NoError(t, err)
	var empty any
	require.NoError(t, json.Unmarshal(fixture, &empty))
	validateAgainst(t, doc, "NetworkList", empty)

	full := NewNetworkList([]Network{{
		ID: "net-1", Name: "app", Revision: "2", Driver: "overlay",
		CreatedAt:  "2026-10-19T10:00:00Z",
		IPAM:       &NetworkIPAM{Subnet: "10.0.1.0/24", Gateway: "10.0.1.1"},
		Labels:     map[string]string{"tier": "data"},
		Attachable: true,
	}})
	data, err := json.Marshal(full)
	require.NoError(t, err)
	var value any
	require.NoError(t, json.Unmarshal(data, &value))
	validateAgainst(t, doc, "NetworkList", value)

	require.NotNil(t, doc.Paths.Find("/v1/networks"))
}

func TestOpenAPIConfigAndSecretLists(t *testing.T) {
	doc := loadOpenAPI(t)

	for _, tc := range []struct {
		name    string
		fixture string
		value   any
	}{
		{
			name:    "ConfigList",
			fixture: "config-list-empty.json",
			value: NewConfigList([]Config{{
				ID: "cfg-1", Name: "app", Revision: "3",
				CreatedAt: "2026-10-19T10:00:00Z",
				Labels:    map[string]string{"team": "payments"},
			}}),
		},
		{
			name:    "SecretList",
			fixture: "secret-list-empty.json",
			value: NewSecretList([]Secret{{
				ID: "sec-1", Name: "token", Revision: "4",
				CreatedAt: "2026-10-19T10:00:00Z",
				Labels:    map[string]string{"env": "prod"},
				Driver:    "external",
			}}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, err := os.ReadFile(filepath.Join("testdata", tc.fixture))
			require.NoError(t, err)
			var empty any
			require.NoError(t, json.Unmarshal(fixture, &empty))
			validateAgainst(t, doc, tc.name, empty)

			data, err := json.Marshal(tc.value)
			require.NoError(t, err)
			var full any
			require.NoError(t, json.Unmarshal(data, &full))
			validateAgainst(t, doc, tc.name, full)
		})
	}

	require.NotNil(t, doc.Paths.Find("/v1/configs"))
	require.NotNil(t, doc.Paths.Find("/v1/secrets"))
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

func TestOpenAPIServiceDocumentFixture(t *testing.T) {
	doc := loadOpenAPI(t)

	value := NewServiceDocument(Service{
		ID:       "svc-1",
		Name:     "web",
		Revision: "17",
		Spec: ServiceSpec{
			Mode:     "replicated",
			Replicas: ptr[int64](3),
			Task:     TaskSpec{Image: "nginx:alpine"},
		},
		Status: ServiceStatus{DesiredTasks: 3, RunningTasks: 3, Converged: true},
	})
	data, err := json.Marshal(value)
	require.NoError(t, err)

	var decoded any
	require.NoError(t, json.Unmarshal(data, &decoded))
	validateAgainst(t, doc, "ServiceDocument", decoded)

	path := doc.Paths.Find("/v1/services/{id}")
	require.NotNil(t, path, "single-service path missing")
	require.NotNil(t, path.Get)
	require.Len(t, path.Get.Parameters, 1)
	require.Equal(t, "id", path.Get.Parameters[0].Value.Name)
	require.True(t, path.Get.Parameters[0].Value.Required)

	tasksPath := doc.Paths.Find("/v1/services/{id}/tasks")
	require.NotNil(t, tasksPath, "service-tasks path missing")
	require.NotNil(t, tasksPath.Get)
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

func TestOpenAPINodeListFixtures(t *testing.T) {
	doc := loadOpenAPI(t)

	fixture, err := os.ReadFile(filepath.Join("testdata", "node-list-empty.json"))
	require.NoError(t, err)

	var empty any
	require.NoError(t, json.Unmarshal(fixture, &empty))
	validateAgainst(t, doc, "NodeList", empty)

	full := NewNodeList([]Node{
		{
			ID:           "n1manager",
			Hostname:     "node-1",
			Revision:     "9",
			CreatedAt:    "2026-10-18T08:00:00Z",
			UpdatedAt:    "2026-10-19T10:03:00Z",
			Role:         "manager",
			Membership:   "accepted",
			Availability: "active",
			State:        "ready",
			Address:      "192.168.1.11",
			Labels:       map[string]string{"zone": "a"},
			Resources:    &NodeResources{CPUs: 8, MemoryBytes: 16 << 30},
			Manager: &NodeManager{
				Leader:    true,
				Reachable: true,
				Address:   "192.168.1.11:4242",
				RaftID:    1,
			},
			AgentVersion: "29.0.1",
		},
		{
			ID:           "n2worker",
			Hostname:     "node-2",
			Revision:     "10",
			CreatedAt:    "2026-10-18T08:05:00Z",
			UpdatedAt:    "2026-10-19T10:04:00Z",
			Role:         "worker",
			Membership:   "accepted",
			Availability: "pause",
			State:        "down",
			Resources:    &NodeResources{CPUs: 3.5, MemoryBytes: 2 << 30},
		},
	})
	data, err := json.Marshal(full)
	require.NoError(t, err)
	var value any
	require.NoError(t, json.Unmarshal(data, &value))
	validateAgainst(t, doc, "NodeList", value)
}

func TestOpenAPIVersionInfo(t *testing.T) {
	doc := loadOpenAPI(t)

	for _, tc := range []struct {
		name  string
		value VersionInfo
	}{
		{
			name:  "populated",
			value: NewVersionInfo([]string{"v1"}, "v0.12.0", "1", "cluster-1", []string{"http-read"}),
		},
		{
			name:  "empty lists and cluster",
			value: NewVersionInfo(nil, "v0.12.0", "1", "", nil),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.value)
			require.NoError(t, err)
			var generic any
			require.NoError(t, json.Unmarshal(data, &generic))
			validateAgainst(t, doc, "VersionInfo", generic)
		})
	}

	path := doc.Paths.Find("/v1/version")
	require.NotNil(t, path, "version path missing")
	require.NotNil(t, path.Get, "version GET operation missing")

	okResponse := path.Get.Responses.Status(200)
	require.NotNil(t, okResponse, "version 200 response missing")
	media := okResponse.Value.Content.Get("application/vnd.swarmcracker.v1+json")
	require.NotNil(t, media, "version 200 media type missing")
	require.NotNil(t, media.Schema)
	require.Equal(t, "#/components/schemas/VersionInfo", media.Schema.Ref)

	require.NotNil(t, path.Get.Responses.Value("4XX"), "version 4XX response missing")
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
