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

// v1MediaType is the media type every phase-1 route answers with.
const v1MediaType = "application/vnd.swarmcracker.v1+json"

// requireTypedResponse asserts one response status exists, is declared in the v1
// media type and references schema.
func requireTypedResponse(t *testing.T, doc *openapi3.T, path, status, schema string) {
	t.Helper()

	item := doc.Paths.Find(path)
	require.NotNil(t, item, "%s path missing", path)
	require.NotNil(t, item.Get, "%s GET operation missing", path)

	response := item.Get.Responses.Value(status)
	require.NotNilf(t, response, "%s %s response missing", path, status)
	require.NotNilf(t, response.Value, "%s %s response unresolved", path, status)

	media := response.Value.Content.Get(v1MediaType)
	require.NotNilf(t, media, "%s %s is not declared in %s", path, status, v1MediaType)
	require.NotNilf(t, media.Schema, "%s %s schema missing", path, status)
	require.Equal(t, "#/components/schemas/"+schema, media.Schema.Ref,
		"%s %s must reference %s", path, status, schema)
}

// singleResourceDocumentCases returns each new phase-1 document constructor,
// fully populated, together with the get-by-id path that serves it.
func singleResourceDocumentCases() []struct {
	name  string
	path  string
	value any
} {
	return []struct {
		name  string
		path  string
		value any
	}{
		{
			name: "TaskDocument",
			path: "/v1/tasks/{id}",
			value: NewTaskDocument(Task{
				ID: "t1abc", Name: "web.1.abc123", Revision: "42",
				ServiceID: "svc-1", ServiceName: "web", NodeID: "node-1", NodeName: "node-1",
				Slot: 1, State: "running", DesiredState: "running",
				CreatedAt: "2026-10-19T10:01:00Z", UpdatedAt: "2026-10-19T10:01:04Z",
				Image: "nginx:alpine", Addresses: []string{"10.0.1.7"},
				Ports: []Port{{Target: 80, Protocol: "tcp"}}, ExitCode: ptr[int32](0),
				Health: &TaskHealth{Status: "healthy"},
			}),
		},
		{
			name: "NodeDocument",
			path: "/v1/nodes/{id}",
			value: NewNodeDocument(Node{
				ID: "node-1", Hostname: "node-1", Revision: "9",
				CreatedAt: "2026-10-18T08:00:00Z", UpdatedAt: "2026-10-19T10:03:00Z",
				Role: "manager", Membership: "accepted", Availability: "active", State: "ready",
				Address: "192.168.1.11", Labels: map[string]string{"zone": "a"},
				Resources:    &NodeResources{CPUs: 8, MemoryBytes: 16 << 30},
				Manager:      &NodeManager{Leader: true, Reachable: true, Address: "192.168.1.11:4242", RaftID: 1},
				AgentVersion: "29.0.1",
			}),
		},
		{
			name: "NetworkDocument",
			path: "/v1/networks/{id}",
			value: NewNetworkDocument(Network{
				ID: "net-1", Name: "app", Revision: "2", CreatedAt: "2026-10-19T10:00:00Z",
				Driver: "overlay",
				IPAM:   &NetworkIPAM{Subnet: "10.0.1.0/24", Gateway: "10.0.1.1"},
				Labels: map[string]string{"tier": "data"}, Attachable: true,
			}),
		},
		{
			name: "ConfigDocument",
			path: "/v1/configs/{id}",
			value: NewConfigDocument(Config{
				ID: "cfg-1", Name: "app", Revision: "3",
				CreatedAt: "2026-10-19T10:00:00Z", UpdatedAt: "2026-10-19T10:01:00Z",
				Labels: map[string]string{"team": "payments"},
			}),
		},
		{
			name: "SecretDocument",
			path: "/v1/secrets/{id}",
			value: NewSecretDocument(Secret{
				ID: "sec-1", Name: "token", Revision: "4",
				CreatedAt: "2026-10-19T10:00:00Z", UpdatedAt: "2026-10-19T10:01:00Z",
				Labels: map[string]string{"env": "prod"}, Driver: "external",
			}),
		},
	}
}

// TestOpenAPISingleResourceDocuments validates every single-resource document
// constructor against its schema and pins each get-by-id path to that schema.
func TestOpenAPISingleResourceDocuments(t *testing.T) {
	doc := loadOpenAPI(t)

	for _, tc := range singleResourceDocumentCases() {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.value)
			require.NoError(t, err)
			var generic any
			require.NoError(t, json.Unmarshal(data, &generic))
			validateAgainst(t, doc, tc.name, generic)

			requireTypedResponse(t, doc, tc.path, "200", tc.name)
			requireTypedResponse(t, doc, tc.path, "401", "ErrorDocument")
			requireTypedResponse(t, doc, tc.path, "403", "ErrorDocument")
			requireTypedResponse(t, doc, tc.path, "4XX", "ErrorDocument")

			params := doc.Paths.Find(tc.path).Get.Parameters
			require.Len(t, params, 1)
			require.Equal(t, "id", params[0].Value.Name)
			require.Equal(t, "path", params[0].Value.In,
				"%s id parameter must be a path parameter", tc.path)
			require.True(t, params[0].Value.Required)
		})
	}
}

// TestOpenAPIResourceDocumentRequiresEnvelope pins the document envelope as
// mandatory: a body without kind or schemaVersion must fail validation.
func TestOpenAPIResourceDocumentRequiresEnvelope(t *testing.T) {
	doc := loadOpenAPI(t)

	for _, tc := range singleResourceDocumentCases() {
		t.Run(tc.name, func(t *testing.T) {
			schema, ok := doc.Components.Schemas[tc.name]
			require.True(t, ok, "schema %q missing", tc.name)
			require.NotNil(t, schema.Value)

			data, err := json.Marshal(tc.value)
			require.NoError(t, err)
			var populated map[string]any
			require.NoError(t, json.Unmarshal(data, &populated))
			require.NoError(t, schema.Value.VisitJSON(populated),
				"populated %s must validate", tc.name)

			for _, field := range []string{"kind", "schemaVersion"} {
				stripped := make(map[string]any, len(populated))
				for key, value := range populated {
					if key != field {
						stripped[key] = value
					}
				}
				require.Error(t, schema.Value.VisitJSON(stripped),
					"%s without %q must fail validation", tc.name, field)
			}
		})
	}
}

// TestOpenAPIBearerSecurity pins the phase-1 token model: one bearer scheme,
// required globally, with no public path. /v1/version needs a token too.
func TestOpenAPIBearerSecurity(t *testing.T) {
	doc := loadOpenAPI(t)

	scheme, ok := doc.Components.SecuritySchemes["bearerAuth"]
	require.True(t, ok, "bearerAuth security scheme missing")
	require.NotNil(t, scheme.Value)
	require.Equal(t, "http", scheme.Value.Type)
	require.Equal(t, "bearer", scheme.Value.Scheme)

	require.NotEmpty(t, doc.Security, "global security requirement missing")
	require.Len(t, doc.Security, 1, "exactly one global security requirement")
	require.NotEmpty(t, doc.Security[0], "global security requirement must not be empty")
	require.Contains(t, doc.Security[0], "bearerAuth",
		"global security must require bearerAuth")

	for path, item := range doc.Paths.Map() {
		if item.Get == nil {
			continue
		}
		// An operation-level security list overrides the global one, so an empty
		// or different list here would make the path unauthenticated or
		// differently authenticated.
		require.Emptyf(t, item.Get.Security, "%s must inherit the global bearer requirement", path)
	}
}

// TestOpenAPICommonResponseHeaders asserts the shared response headers are
// declared on a representative list and get response.
func TestOpenAPICommonResponseHeaders(t *testing.T) {
	doc := loadOpenAPI(t)

	apiVersion, ok := doc.Components.Headers["X-SwarmCracker-Api-Version"]
	require.True(t, ok, "X-SwarmCracker-Api-Version component header missing")
	require.NotNil(t, apiVersion.Value)
	require.NotNil(t, apiVersion.Value.Schema)
	require.NotNil(t, apiVersion.Value.Schema.Value)
	require.True(t, apiVersion.Value.Schema.Value.Type.Is("integer"))
	require.Len(t, apiVersion.Value.Schema.Value.Enum, 1)
	require.EqualValues(t, 1, apiVersion.Value.Schema.Value.Enum[0])

	requestID, ok := doc.Components.Headers["X-Request-Id"]
	require.True(t, ok, "X-Request-Id component header missing")
	require.NotNil(t, requestID.Value)
	require.NotNil(t, requestID.Value.Schema.Value)
	require.True(t, requestID.Value.Schema.Value.Type.Is("string"))

	etag, ok := doc.Components.Headers["ETag"]
	require.True(t, ok, "ETag component header missing")
	require.NotNil(t, etag.Value)
	require.NotNil(t, etag.Value.Schema.Value)
	require.True(t, etag.Value.Schema.Value.Type.Is("string"))

	for _, tc := range []struct{ path, status string }{
		{"/v1/services", "200"},
		{"/v1/services/{id}", "200"},
		{"/v1/tasks", "200"},
		{"/v1/tasks/{id}", "200"},
		{"/v1/services", "4XX"},
	} {
		response := doc.Paths.Find(tc.path).Get.Responses.Value(tc.status)
		require.NotNilf(t, response, "%s %s response missing", tc.path, tc.status)

		for _, name := range []string{"X-SwarmCracker-Api-Version", "X-Request-Id"} {
			header, ok := response.Value.Headers[name]
			require.Truef(t, ok, "%s %s must declare %s", tc.path, tc.status, name)
			require.NotNilf(t, header.Value, "%s %s header %s unresolved", tc.path, tc.status, name)
		}

		if tc.status == "200" {
			header, ok := response.Value.Headers["ETag"]
			require.Truef(t, ok, "%s 200 must declare ETag", tc.path)
			require.NotNil(t, header.Value)
		}
	}

	// A conditional read must be part of the contract, not just the docs.
	for _, path := range []string{"/v1/services", "/v1/services/{id}"} {
		notModified := doc.Paths.Find(path).Get.Responses.Value("304")
		require.NotNilf(t, notModified, "%s 304 response missing", path)
		require.NotNil(t, notModified.Value)
		require.Empty(t, notModified.Value.Content, "%s 304 must not carry a body", path)
	}
}

// TestOpenAPIListParameters pins the exact query surface of every list path.
// Phase 1 only implements limit, cursor and label, so an added-but-unserved
// parameter (state, role, hostname, …) must fail the contract test.
func TestOpenAPIListParameters(t *testing.T) {
	doc := loadOpenAPI(t)

	for _, tc := range []struct {
		path   string
		params []string
	}{
		{"/v1/services", []string{"limit", "cursor", "label"}},
		{"/v1/services/{id}/tasks", []string{"id", "limit", "cursor"}},
		{"/v1/tasks", []string{"limit", "cursor"}},
		{"/v1/nodes", []string{"label", "limit", "cursor"}},
		{"/v1/networks", []string{"limit", "cursor", "label"}},
		{"/v1/configs", []string{"limit", "cursor", "label"}},
		{"/v1/secrets", []string{"limit", "cursor", "label"}},
	} {
		item := doc.Paths.Find(tc.path)
		require.NotNilf(t, item, "%s path missing", tc.path)
		require.NotNilf(t, item.Get, "%s GET operation missing", tc.path)

		names := make([]string, 0, len(item.Get.Parameters))
		for _, param := range item.Get.Parameters {
			require.NotNilf(t, param.Value, "%s parameter unresolved", tc.path)
			names = append(names, param.Value.Name)

			if param.Value.In == "path" {
				require.Truef(t, param.Value.Required,
					"%s path parameter %s must be required", tc.path, param.Value.Name)
				continue
			}
			require.Equalf(t, "query", param.Value.In,
				"%s parameter %s must be a query parameter", tc.path, param.Value.Name)
			require.Falsef(t, param.Value.Required,
				"%s query parameter %s must be optional", tc.path, param.Value.Name)
			require.NotNilf(t, param.Value.Schema, "%s parameter %s has no schema",
				tc.path, param.Value.Name)
		}

		require.ElementsMatchf(t, tc.params, names,
			"%s must declare exactly these parameters", tc.path)
	}
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

// TestOpenAPIAuditSurface pins GET /v1/audit and the audit schemas. The route is
// the only way an operator reads the log, so its document shape is contract.
func TestOpenAPIAuditSurface(t *testing.T) {
	doc := loadOpenAPI(t)

	for _, tc := range []struct {
		name  string
		value AuditList
	}{
		{name: "empty", value: NewAuditList(nil)},
		{
			name: "populated",
			value: NewAuditList([]AuditRecord{
				{
					At:        "2026-10-23T10:00:00.123456789Z",
					RequestID: "req_0123456789ab",
					Actor:     AuditActor{Type: "token", ID: "tok_deadbeef", Name: "ci"},
					Action:    "service.update",
					Resource:  AuditResource{Kind: "Service", ID: "svc-1", Name: "web"},
					Result:    "ok",
					SourceIP:  "10.0.0.5",
				},
				{
					At:        "2026-10-23T10:00:01Z",
					RequestID: "req_0123456789ac",
					Actor:     AuditActor{Type: "local"},
					Action:    "config.delete",
					Resource:  AuditResource{Kind: "Config"},
					Result:    "error",
				},
			}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.value)
			require.NoError(t, err)
			var generic any
			require.NoError(t, json.Unmarshal(data, &generic))
			validateAgainst(t, doc, "AuditList", generic)
		})
	}

	requireTypedResponse(t, doc, "/v1/audit", "200", "AuditList")
	requireTypedResponse(t, doc, "/v1/audit", "403", "ErrorDocument")
	requireTypedResponse(t, doc, "/v1/audit", "501", "ErrorDocument")
	requireTypedResponse(t, doc, "/v1/audit", "4XX", "ErrorDocument")

	path := doc.Paths.Find("/v1/audit")
	require.NotNil(t, path)
	require.NotNil(t, path.Get)
	require.NotNil(t, path.Get.Responses.Status(401), "audit 401 response missing")
	require.NotNil(t, path.Get.Responses.Status(304), "audit 304 response missing")

	names := make([]string, 0, len(path.Get.Parameters))
	for _, param := range path.Get.Parameters {
		require.NotNil(t, param.Value)
		names = append(names, param.Value.Name)
		require.Equal(t, "query", param.Value.In, param.Value.Name)
		require.False(t, param.Value.Required, param.Value.Name)
		require.NotNil(t, param.Value.Schema)
	}
	require.ElementsMatch(t, []string{"limit", "cursor"}, names)
}

// TestOpenAPIConfigDataIsOptional pins the payload field: documented, typed as a
// string, and never required, because only configs:read-values produces it.
func TestOpenAPIConfigDataIsOptional(t *testing.T) {
	doc := loadOpenAPI(t)

	payload := "listen 8080"
	value := NewConfigDocument(Config{
		ID: "cfg-1", Name: "app", Revision: "3", Data: &payload,
	})
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var generic any
	require.NoError(t, json.Unmarshal(data, &generic))
	validateAgainst(t, doc, "ConfigDocument", generic)

	schema := doc.Components.Schemas["Config"]
	require.NotNil(t, schema)
	require.NotNil(t, schema.Value)

	field, ok := schema.Value.Properties["data"]
	require.True(t, ok, "Config.data must be documented")
	require.NotNil(t, field.Value)
	require.True(t, field.Value.Type.Is("string"))
	require.NotContains(t, schema.Value.Required, "data",
		"a plain configs:read response omits the payload")
}
