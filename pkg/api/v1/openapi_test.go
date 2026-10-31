package v1

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
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

			// The id path parameter is the route's identity; a get-by-id may
			// also take an optional query parameter (health), so only the path
			// parameter is pinned here.
			id := findParameter(t, doc.Paths.Find(tc.path).Get.Parameters, "id")
			require.Equal(t, "path", id.In,
				"%s id parameter must be a path parameter", tc.path)
			require.True(t, id.Required)
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
// parameter (state, role, hostname, …) must fail the contract test. `/v1/tasks`
// also declares `label`: the route accepts it (the v1 SDKs forward it) but does
// not filter on it, which its parameter description states.
func TestOpenAPIListParameters(t *testing.T) {
	doc := loadOpenAPI(t)

	for _, tc := range []struct {
		path   string
		params []string
	}{
		{"/v1/services", []string{"limit", "cursor", "label"}},
		{"/v1/services/{id}/tasks", []string{"id", "limit", "cursor"}},
		{"/v1/tasks", []string{"limit", "cursor", "health", "label"}},
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

// TestOpenAPIExec pins the exec route and its wire DTOs. Exec is the one v1
// route whose response is a stream, so the contract must say so explicitly.
func TestOpenAPIExec(t *testing.T) {
	doc := loadOpenAPI(t)

	item := doc.Paths.Find("/v1/tasks/{id}/exec")
	require.NotNil(t, item, "exec path missing")
	require.NotNil(t, item.Post, "exec POST operation missing")
	op := item.Post

	require.Contains(t, op.Description, "tasks:exec", "the operation must name its scope")
	require.Contains(t, op.Description, "tty_not_supported",
		"the TTY follow-up must be documented on the operation")
	require.Contains(t, op.Description, "must be running",
		"exec must state that it needs a live task")

	id := findParameter(t, op.Parameters, "id")
	require.True(t, id.Required)
	require.NotNil(t, id.Schema)
	require.True(t, id.Schema.Value.Type.Is("string"))

	// The operation inherits the global bearer requirement, like every other
	// route in this file.
	require.Empty(t, op.Security, "exec must inherit the global bearer requirement")

	require.NotNil(t, op.RequestBody)
	require.True(t, op.RequestBody.Value.Required)
	requestMedia := op.RequestBody.Value.Content.Get(v1MediaType)
	require.NotNilf(t, requestMedia, "the exec request body must be %s", v1MediaType)
	require.NotNil(t, requestMedia.Schema)
	require.Equal(t, "#/components/schemas/ExecRequest", requestMedia.Schema.Ref)

	ok := op.Responses.Value("200")
	require.NotNil(t, ok, "exec 200 response missing")
	require.NotNil(t, ok.Value)
	stream := ok.Value.Content.Get(NDJSONMediaType)
	require.NotNilf(t, stream, "the exec response must be %s", NDJSONMediaType)
	require.NotNil(t, stream.Schema)
	require.Equal(t, "#/components/schemas/ExecFrame", stream.Schema.Ref)
	require.NotNil(t, ok.Value.Description)
	require.Contains(t, *ok.Value.Description, "per line",
		"the stream must be documented as one JSON object per line")
	require.Contains(t, *ok.Value.Description, "exit",
		"a terminal exit frame must be documented")

	// Every failure is a typed v1 error document.
	for _, status := range []string{"400", "401", "403", "413", "415", "429", "501", "503"} {
		response := op.Responses.Value(status)
		require.NotNilf(t, response, "exec %s response missing", status)
		require.NotNilf(t, response.Value, "exec %s response unresolved", status)
		require.NotNilf(t, response.Value.Content.Get(v1MediaType),
			"exec %s must be declared in %s", status, v1MediaType)
	}

	// The DTO schemas describe exactly what the Go types put on the wire.
	for _, tc := range []struct {
		name  string
		value any
	}{
		{name: "ExecRequest", value: ExecRequest{Command: []string{"echo", "hi"}}},
		{name: "ExecRequest", value: ExecRequest{
			Command:   []string{"env"},
			Env:       map[string]string{"APP": "prod"},
			WorkDir:   "/srv",
			TTY:       true,
			Stdin:     true,
			TimeoutMs: 1500,
		}},
		{name: "ExecFrame", value: StdoutFrame([]byte("hello\n"))},
		{name: "ExecFrame", value: StderrFrame([]byte("warning\n"))},
		{name: "ExecFrame", value: ExitFrame(0)},
		{name: "ExecFrame", value: SignalExitFrame(9)},
		{name: "ExecFrame", value: ErrorFrame("internal")},
	} {
		data, err := json.Marshal(tc.value)
		require.NoError(t, err)
		var generic any
		require.NoError(t, json.Unmarshal(data, &generic))
		validateAgainst(t, doc, tc.name, generic)
	}

	requestSchema := documentSchemaProperties(t, doc, "ExecRequest")
	for _, key := range []string{"command", "env", "workdir", "tty", "stdin", "timeoutMs"} {
		require.Containsf(t, requestSchema, key, "ExecRequest.%s missing", key)
	}
	require.NotContains(t, requestSchema, "timeout_ms", "exec keys are camelCase")

	frameSchema := documentSchemaProperties(t, doc, "ExecFrame")
	for _, key := range []string{"type", "data", "exitCode", "signal", "code"} {
		require.Containsf(t, frameSchema, key, "ExecFrame.%s missing", key)
	}
	require.NotContains(t, frameSchema, "exit_code", "exec keys are camelCase")
}

// TestOpenAPIAttach pins the interactive console route. It is the only
// full-duplex route in the file: the request body is stdin and the response body
// is the console, so both directions are raw bytes and neither is a document.
func TestOpenAPIAttach(t *testing.T) {
	doc := loadOpenAPI(t)

	item := doc.Paths.Find("/v1/tasks/{id}/attach")
	require.NotNil(t, item, "attach path missing")
	require.NotNil(t, item.Post, "attach POST operation missing")
	op := item.Post

	require.Contains(t, op.Description, "tasks:exec", "the operation must name its scope")
	require.Contains(t, op.Description, "full duplex", "the transport ruling must be documented")
	require.Contains(t, op.Description, "console_unavailable",
		"the no-console reason must be documented")
	require.Contains(t, op.Description, "task.attach", "the audit action must be documented")
	require.Contains(t, op.Description, "must be running",
		"attach must state that it needs a live task")
	require.Contains(t, op.Description, "selectors", "attach must state the selector rule")

	id := findParameter(t, op.Parameters, "id")
	require.True(t, id.Required)
	require.True(t, id.Schema.Value.Type.Is("string"))
	require.Len(t, op.Parameters, 1, "attach takes no query parameter")

	// The transport is one request: a body that is not required (a read-only
	// attach) and a raw-byte response.
	require.NotNil(t, op.RequestBody)
	require.NotNil(t, op.RequestBody.Value)
	require.False(t, op.RequestBody.Value.Required, "an empty stdin is a valid attach")
	stdin := op.RequestBody.Value.Content.Get("application/octet-stream")
	require.NotNil(t, stdin, "the request body must be application/octet-stream")
	require.Equal(t, "binary", stdin.Schema.Value.Format)

	ok := op.Responses.Value("200")
	require.NotNil(t, ok, "attach 200 response missing")
	console := ok.Value.Content.Get("application/octet-stream")
	require.NotNil(t, console, "the console stream must be application/octet-stream")
	require.Equal(t, "binary", console.Schema.Value.Format)

	notFound := op.Responses.Value("404")
	require.NotNil(t, notFound, "attach 404 response missing")
	require.Contains(t, *notFound.Value.Description, "console_unavailable",
		"the 404 must name the reason a running task cannot be attached to")

	for _, status := range []string{"400", "401", "403", "404", "429", "501", "503"} {
		response := op.Responses.Value(status)
		require.NotNilf(t, response, "attach %s response missing", status)
		require.NotNilf(t, response.Value, "attach %s response unresolved", status)
		require.NotNilf(t, response.Value.Content.Get(v1MediaType),
			"attach %s must be declared in %s", status, v1MediaType)
	}

	// The capability the 501 names is the one the version document advertises.
	version := doc.Components.Schemas["VersionInfo"]
	require.NotNil(t, version)
	capabilities := version.Value.Properties["capabilities"]
	require.NotNil(t, capabilities)
	require.Contains(t, capabilities.Value.Description, "attach",
		"the capability list must name attach")
}

// TestOpenAPILogsAndFiles pins the two remaining task-scoped routes. Logs are
// the second NDJSON stream and files are the only raw-body route, so the
// contract must say so explicitly instead of implying a JSON envelope.
func TestOpenAPILogsAndFiles(t *testing.T) {
	doc := loadOpenAPI(t)

	logs := doc.Paths.Find("/v1/tasks/{id}/logs")
	require.NotNil(t, logs, "logs path missing")
	require.NotNil(t, logs.Get, "logs GET operation missing")
	op := logs.Get

	require.Contains(t, op.Description, "tasks:logs", "the operation must name its scope")
	require.Contains(t, op.Description, "selectors", "logs must state they are not selector-gated")
	require.Contains(t, op.Description, "eof", "the follow/eof rule must be documented")
	require.Contains(t, op.Description, "after the task exits",
		"logs must state that they stay available after a task exits")

	id := findParameter(t, op.Parameters, "id")
	require.True(t, id.Required)
	for _, name := range []string{"tail", "since", "follow"} {
		param := findParameter(t, op.Parameters, name)
		require.NotNilf(t, param.Schema, "logs %s parameter schema missing", name)
	}

	ok := op.Responses.Value("200")
	require.NotNil(t, ok, "logs 200 response missing")
	stream := ok.Value.Content.Get(NDJSONMediaType)
	require.NotNilf(t, stream, "the log response must be %s", NDJSONMediaType)
	require.Equal(t, "#/components/schemas/LogFrame", stream.Schema.Ref)

	for _, status := range []string{"400", "401", "403", "404", "429", "501", "503"} {
		response := op.Responses.Value(status)
		require.NotNilf(t, response, "logs %s response missing", status)
		require.NotNilf(t, response.Value.Content.Get(v1MediaType),
			"logs %s must be declared in %s", status, v1MediaType)
	}

	files := doc.Paths.Find("/v1/tasks/{id}/files")
	require.NotNil(t, files, "files path missing")
	require.NotNil(t, files.Get, "files GET operation missing")
	require.NotNil(t, files.Put, "files PUT operation missing")

	for _, fileOp := range []*openapi3.Operation{files.Get, files.Put} {
		require.Contains(t, fileOp.Description, "tasks:files", "the operation must name its scope")
		require.Contains(t, fileOp.Description, "task.files", "the audit action must be documented")
		require.Contains(t, fileOp.Description, "must be running",
			"files must state that the task has to be running")

		pathParam := findParameter(t, fileOp.Parameters, "path")
		require.True(t, pathParam.Required, "path is required")
		require.NotContains(t, fileOp.Description, "tar", "there is no archive mode")
	}

	getFile := files.Get.Responses.Value("200")
	require.NotNil(t, getFile, "files GET 200 response missing")
	octets := getFile.Value.Content.Get("application/octet-stream")
	require.NotNil(t, octets, "a file read must be application/octet-stream")
	require.Equal(t, "binary", octets.Schema.Value.Format, "the body is raw bytes")

	for _, status := range []string{"400", "401", "403", "404", "429", "501", "503"} {
		response := files.Get.Responses.Value(status)
		require.NotNilf(t, response, "files GET %s response missing", status)
		require.NotNilf(t, response.Value.Content.Get(v1MediaType),
			"files GET %s must be declared in %s", status, v1MediaType)
	}

	putFile := files.Put
	require.NotNil(t, putFile.RequestBody)
	require.True(t, putFile.RequestBody.Value.Required)
	upload := putFile.RequestBody.Value.Content.Get("application/octet-stream")
	require.NotNil(t, upload, "a file write body must be application/octet-stream")
	require.Equal(t, "binary", upload.Schema.Value.Format, "the body is raw bytes")

	noContent := putFile.Responses.Value("204")
	require.NotNil(t, noContent, "a successful write is 204")
	for _, status := range []string{"400", "401", "403", "404", "413", "415", "429", "501", "503"} {
		response := putFile.Responses.Value(status)
		require.NotNilf(t, response, "files PUT %s response missing", status)
		require.NotNilf(t, response.Value.Content.Get(v1MediaType),
			"files PUT %s must be declared in %s", status, v1MediaType)
	}

	for _, tc := range []struct {
		name  string
		value LogFrame
	}{
		{name: "LogFrame", value: LogDataFrame(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), []byte("hello\n"))},
		{name: "LogFrame", value: LogEOFFrame()},
	} {
		data, err := json.Marshal(tc.value)
		require.NoError(t, err)
		var generic any
		require.NoError(t, json.Unmarshal(data, &generic))
		validateAgainst(t, doc, tc.name, generic)
	}

	frameSchema := documentSchemaProperties(t, doc, "LogFrame")
	for _, key := range []string{"type", "stream", "time", "data"} {
		require.Containsf(t, frameSchema, key, "LogFrame.%s missing", key)
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
					At:           "2026-10-23T10:00:00.123456789Z",
					RequestID:    "req_0123456789ab",
					Actor:        AuditActor{Type: "token", ID: "tok_deadbeef", Name: "ci"},
					Action:       "service.update",
					Resource:     AuditResource{Kind: "Service", ID: "svc-1", Name: "web"},
					Result:       "ok",
					Precondition: "revision",
					SourceIP:     "10.0.0.5",
				},
				{
					At:        "2026-10-23T10:00:01Z",
					RequestID: "req_0123456789ac",
					Actor:     AuditActor{Type: "local"},
					Action:    "config.delete",
					Resource:  AuditResource{Kind: "Config"},
					Result:    "error",
				},
				{
					At:        "2026-10-23T10:00:02Z",
					RequestID: "req_0123456789ad",
					Actor:     AuditActor{Type: AuditActorUnknown},
					Action:    "network.create",
					Resource:  AuditResource{Kind: "Network", Name: "frontend"},
					Result:    "ok",
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

// TestOpenAPIServiceWriteSurface pins the phase-2 write contract: the request
// schemas, the precondition headers and the statuses each route can answer.
func TestOpenAPIServiceWriteSurface(t *testing.T) {
	doc := loadOpenAPI(t)

	replicas := int64(3)
	write := ServiceWrite{
		Name:   "web",
		Labels: map[string]string{"team": "payments"},
		Spec: ServiceSpecWrite{
			Mode:     "replicated",
			Replicas: &replicas,
			Task: TaskSpecWrite{
				TaskSpec: TaskSpec{
					Image:    "nginx:alpine",
					Command:  []string{"nginx"},
					Env:      map[string]string{"APP_ENV": "prod"},
					Ports:    []Port{{Target: 80, Published: 8080, Protocol: "tcp", Mode: "ingress"}},
					Mounts:   []Mount{{Type: "volume", Source: "data", Target: "/data"}},
					Networks: []NetworkAttachment{{ID: "net-1", Name: "app"}},
				},
				RegistryAuth: "cmVnaXN0cnk=",
			},
			Update: &UpdateConfig{Parallelism: 1, FailureAction: "pause"},
		},
	}

	data, err := json.Marshal(write)
	require.NoError(t, err)
	var generic any
	require.NoError(t, json.Unmarshal(data, &generic))
	validateAgainst(t, doc, "ServiceWrite", generic)

	// The credential is documented on the write schema only.
	document, err := json.Marshal(NewServiceDocument(Service{
		ID: "svc-1", Name: "web", Revision: "7",
		Spec: ServiceSpec{Mode: "replicated", Task: TaskSpec{Image: "nginx"}},
	}))
	require.NoError(t, err)
	var readDocument any
	require.NoError(t, json.Unmarshal(document, &readDocument))
	validateAgainst(t, doc, "ServiceDocument", readDocument)

	for _, name := range []string{"Service", "ServiceSpec", "TaskSpec", "ServiceDocument"} {
		schema := doc.Components.Schemas[name]
		require.NotNil(t, schema, name)
		_, documented := schema.Value.Properties["registryAuth"]
		require.Falsef(t, documented, "%s must not document registryAuth", name)
	}

	// Create.
	create := doc.Paths.Find("/v1/services")
	require.NotNil(t, create)
	require.NotNil(t, create.Post, "POST /v1/services missing")
	require.NotNil(t, create.Post.RequestBody)
	requestMedia := create.Post.RequestBody.Value.Content.Get(v1MediaType)
	require.NotNil(t, requestMedia, "create body must be the v1 media type")
	require.Equal(t, "#/components/schemas/ServiceWrite", requestMedia.Schema.Ref)
	requireTypedResponseFor(t, create.Post, "201", "ServiceDocument")
	requireTypedResponseFor(t, create.Post, "400", "ErrorDocument")
	requireTypedResponseFor(t, create.Post, "409", "ErrorDocument")
	requireTypedResponseFor(t, create.Post, "413", "ErrorDocument")
	requireTypedResponseFor(t, create.Post, "415", "ErrorDocument")

	ifNoneMatch := findParameter(t, create.Post.Parameters, "If-None-Match")
	require.Equal(t, "header", ifNoneMatch.In)

	// Patch.
	update := doc.Paths.Find("/v1/services/{id}")
	require.NotNil(t, update)
	require.NotNil(t, update.Patch, "PATCH /v1/services/{id} missing")
	require.NotNil(t, update.Patch.RequestBody)
	patchMedia := update.Patch.RequestBody.Value.Content.Get(MergePatchMediaType)
	require.NotNil(t, patchMedia, "patch body must be application/merge-patch+json")
	require.Equal(t, "#/components/schemas/ServicePatch", patchMedia.Schema.Ref)

	ifMatch := findParameter(t, update.Patch.Parameters, "If-Match")
	assert.True(t, ifMatch.Required, "If-Match is required on PATCH")
	assert.Equal(t, "header", ifMatch.In)

	requireTypedResponseFor(t, update.Patch, "200", "ServiceDocument")
	requireTypedResponseFor(t, update.Patch, "412", "ErrorDocument")
	requireTypedResponseFor(t, update.Patch, "428", "ErrorDocument")
	requireTypedResponseFor(t, update.Patch, "415", "ErrorDocument")

	// Delete.
	require.NotNil(t, update.Delete, "DELETE /v1/services/{id} missing")
	require.NotNil(t, update.Delete.Responses.Status(204), "delete must document 204")
	require.Empty(t, update.Delete.Responses.Status(204).Value.Content,
		"a 204 has no body")
	requireTypedResponseFor(t, update.Delete, "404", "ErrorDocument")
	requireTypedResponseFor(t, update.Delete, "412", "ErrorDocument")

	ignoreMissing := findParameter(t, update.Delete.Parameters, "ignoreMissing")
	assert.Equal(t, "query", ignoreMissing.In)
	assert.False(t, ignoreMissing.Required)
	ifMatch = findParameter(t, update.Delete.Parameters, "If-Match")
	assert.False(t, ifMatch.Required, "If-Match is optional on DELETE")

	// Rollback.
	rollback := doc.Paths.Find("/v1/services/{id}/rollback")
	require.NotNil(t, rollback, "rollback path missing")
	require.NotNil(t, rollback.Post)
	requireTypedResponseFor(t, rollback.Post, "200", "ServiceDocument")
	requireTypedResponseFor(t, rollback.Post, "428", "ErrorDocument")
	assert.True(t, findParameter(t, rollback.Post.Parameters, "If-Match").Required,
		"If-Match is required on rollback")

	// The patch schema refuses spec.mode and unknown fields by construction.
	patchSchema := doc.Components.Schemas["ServicePatch"]
	require.NotNil(t, patchSchema)
	require.NotContains(t, patchSchema.Value.Properties["spec"].Value.Properties, "mode",
		"spec.mode must not be writable")
	require.NoError(t, patchSchema.Value.VisitJSON(map[string]any{
		"spec": map[string]any{"task": map[string]any{"env": map[string]any{"A": nil}}},
	}), "a nested null map value is a valid merge patch")
	require.Error(t, patchSchema.Value.VisitJSON(map[string]any{
		"spec": map[string]any{"mode": "global"},
	}), "spec.mode is immutable")
	require.Error(t, patchSchema.Value.VisitJSON(map[string]any{
		"bogus": true,
	}), "unknown patch fields are refused")
}

// TestOpenAPINamedWriteSurface pins the phase-2 config, secret, network and node
// writes: the request schemas, the preconditions, the statuses and the fact that
// a network has no update.
func TestOpenAPINamedWriteSurface(t *testing.T) {
	doc := loadOpenAPI(t)

	marshal := func(t *testing.T, value any) any {
		t.Helper()
		data, err := json.Marshal(value)
		require.NoError(t, err)
		var generic any
		require.NoError(t, json.Unmarshal(data, &generic))
		return generic
	}

	t.Run("request schemas", func(t *testing.T) {
		validateAgainst(t, doc, "ConfigWrite", marshal(t, ConfigWrite{
			Name: "app", Labels: map[string]string{"team": "payments"}, Data: "listen 8080",
		}))
		validateAgainst(t, doc, "SecretWrite", marshal(t, SecretWrite{
			Name: "token", Data: "aGk=",
		}))
		validateAgainst(t, doc, "SecretWrite", marshal(t, SecretWrite{
			Name: "external", Driver: &SecretDriver{Name: "vault", Options: map[string]string{"path": "secret/app"}},
		}))
		validateAgainst(t, doc, "NetworkWrite", marshal(t, NetworkWrite{
			Name: "frontend", Driver: "overlay",
			IPAM:   &NetworkIPAM{Subnet: "10.0.1.0/24", Gateway: "10.0.1.1"},
			Labels: map[string]string{"tier": "edge"}, Attachable: true,
		}))
		validateAgainst(t, doc, "ConfigPatch", marshal(t, map[string]any{
			"labels": map[string]any{"team": "search", "env": nil},
		}))
		validateAgainst(t, doc, "SecretPatch", marshal(t, map[string]any{
			"labels": map[string]any{"env": "prod"},
		}))
		validateAgainst(t, doc, "NodePatch", marshal(t, map[string]any{
			"availability": "drain",
			"labels":       map[string]any{"zone": "b"},
		}))
	})

	t.Run("secret payload is one of data or driver", func(t *testing.T) {
		schema := doc.Components.Schemas["SecretWrite"]
		require.NotNil(t, schema)
		require.NotNil(t, schema.Value)

		require.Error(t, schema.Value.VisitJSON(map[string]any{
			"name": "token", "data": "aGk=", "driver": map[string]any{"name": "vault"},
		}), "data and driver are mutually exclusive")
		require.Error(t, schema.Value.VisitJSON(map[string]any{"name": "token"}),
			"one of data or driver is required")
	})

	t.Run("no read document carries secret data", func(t *testing.T) {
		for _, name := range []string{"Secret", "SecretDocument", "SecretList"} {
			schema := doc.Components.Schemas[name]
			require.NotNil(t, schema, name)
			if schema.Value.Properties == nil {
				continue
			}
			_, documented := schema.Value.Properties["data"]
			assert.Falsef(t, documented, "%s must not have a data field", name)
		}
	})

	t.Run("config create", func(t *testing.T) {
		item := doc.Paths.Find("/v1/configs")
		require.NotNil(t, item)
		require.NotNil(t, item.Post, "POST /v1/configs missing")
		require.NotNil(t, item.Post.RequestBody)

		media := item.Post.RequestBody.Value.Content.Get(v1MediaType)
		require.NotNil(t, media)
		require.Equal(t, "#/components/schemas/ConfigWrite", media.Schema.Ref)

		requireTypedResponseFor(t, item.Post, "201", "ConfigDocument")
		requireTypedResponseFor(t, item.Post, "413", "ErrorDocument")
		requireTypedResponseFor(t, item.Post, "415", "ErrorDocument")
		assert.False(t, findParameter(t, item.Post.Parameters, "If-None-Match").Required)
	})

	t.Run("config patch and delete", func(t *testing.T) {
		item := doc.Paths.Find("/v1/configs/{id}")
		require.NotNil(t, item)
		require.NotNil(t, item.Patch)
		require.NotNil(t, item.Patch.RequestBody)

		media := item.Patch.RequestBody.Value.Content.Get(MergePatchMediaType)
		require.NotNil(t, media)
		require.Equal(t, "#/components/schemas/ConfigPatch", media.Schema.Ref)

		requireTypedResponseFor(t, item.Patch, "200", "ConfigDocument")
		requireTypedResponseFor(t, item.Patch, "412", "ErrorDocument")
		requireTypedResponseFor(t, item.Patch, "428", "ErrorDocument")
		assert.True(t, findParameter(t, item.Patch.Parameters, "If-Match").Required)

		require.NotNil(t, item.Delete, "DELETE /v1/configs/{id} missing")
		require.NotNil(t, item.Delete.Responses.Status(204))
		assert.False(t, findParameter(t, item.Delete.Parameters, "ignoreMissing").Required)

		// The payload is not writable through a patch.
		patch := doc.Components.Schemas["ConfigPatch"]
		require.Error(t, patch.Value.VisitJSON(map[string]any{"data": "new"}))
		require.Error(t, patch.Value.VisitJSON(map[string]any{"name": "other"}))
	})

	t.Run("secret create, patch and delete", func(t *testing.T) {
		create := doc.Paths.Find("/v1/secrets")
		require.NotNil(t, create)
		require.NotNil(t, create.Post)
		media := create.Post.RequestBody.Value.Content.Get(v1MediaType)
		require.NotNil(t, media)
		require.Equal(t, "#/components/schemas/SecretWrite", media.Schema.Ref)
		requireTypedResponseFor(t, create.Post, "201", "SecretDocument")

		item := doc.Paths.Find("/v1/secrets/{id}")
		require.NotNil(t, item)
		require.NotNil(t, item.Patch)
		patchMedia := item.Patch.RequestBody.Value.Content.Get(MergePatchMediaType)
		require.NotNil(t, patchMedia)
		require.Equal(t, "#/components/schemas/SecretPatch", patchMedia.Schema.Ref)
		assert.True(t, findParameter(t, item.Patch.Parameters, "If-Match").Required)
		require.NotNil(t, item.Delete)

		patch := doc.Components.Schemas["SecretPatch"]
		require.Error(t, patch.Value.VisitJSON(map[string]any{"data": "aGk="}))
		require.Error(t, patch.Value.VisitJSON(map[string]any{"driver": map[string]any{"name": "vault"}}))
	})

	t.Run("network create and delete, no update", func(t *testing.T) {
		create := doc.Paths.Find("/v1/networks")
		require.NotNil(t, create)
		require.NotNil(t, create.Post, "POST /v1/networks missing")
		media := create.Post.RequestBody.Value.Content.Get(v1MediaType)
		require.NotNil(t, media)
		require.Equal(t, "#/components/schemas/NetworkWrite", media.Schema.Ref)
		requireTypedResponseFor(t, create.Post, "201", "NetworkDocument")

		item := doc.Paths.Find("/v1/networks/{id}")
		require.NotNil(t, item)
		require.NotNil(t, item.Delete, "DELETE /v1/networks/{id} missing")
		require.Nil(t, item.Patch, "a network has no update")
		require.Nil(t, item.Put, "a network has no update")
		assert.Contains(t, item.Description, "405",
			"the path must document that PATCH and PUT are refused")
		assert.Contains(t, item.Description, "GET, DELETE")

		notAllowed := item.Get.Responses.Value("405")
		require.NotNil(t, notAllowed, "the 405 response must be declared")
	})
}

// TestOpenAPIConfigWriteAndReadPayloads pins the one payload rule: a config write
// carries data, a config read carries it only behind configs:read-values, and a
// secret read never has the field at all.
func TestOpenAPIConfigWriteAndReadPayloads(t *testing.T) {
	doc := loadOpenAPI(t)

	schema := doc.Components.Schemas["Config"]
	require.NotNil(t, schema)
	data := schema.Value.Properties["data"]
	require.NotNil(t, data)
	require.NotNil(t, data.Value)
	assert.Contains(t, data.Value.Description, "configs:read-values")

	write := doc.Components.Schemas["ConfigWrite"]
	require.NotNil(t, write)
	writeData := write.Value.Properties["data"]
	require.NotNil(t, writeData)
	require.NotNil(t, writeData.Value)
	assert.True(t, writeData.Value.Type.Is("string"))
}

// requireTypedResponseFor is requireTypedResponse's operation-level twin.
func requireTypedResponseFor(t *testing.T, operation *openapi3.Operation, status, schema string) {
	t.Helper()

	response := operation.Responses.Value(status)
	require.NotNilf(t, response, "%s response missing", status)
	require.NotNilf(t, response.Value, "%s response unresolved", status)

	media := response.Value.Content.Get(v1MediaType)
	require.NotNilf(t, media, "%s is not declared in %s", status, v1MediaType)
	require.NotNilf(t, media.Schema, "%s schema missing", status)
	require.Equal(t, "#/components/schemas/"+schema, media.Schema.Ref,
		"%s must reference %s", status, schema)
}

// findParameter returns one named parameter or fails.
func findParameter(t *testing.T, parameters openapi3.Parameters, name string) *openapi3.Parameter {
	t.Helper()
	for _, parameter := range parameters {
		if parameter.Value != nil && parameter.Value.Name == name {
			return parameter.Value
		}
	}
	require.FailNowf(t, "parameter missing", "%q is not declared", name)
	return nil
}

// documentSchemaProperties returns the properties of a *Document schema, which
// is written as an allOf of the resource and the envelope.
func documentSchemaProperties(t *testing.T, doc *openapi3.T, name string) map[string]*openapi3.SchemaRef {
	t.Helper()

	schema, ok := doc.Components.Schemas[name]
	require.True(t, ok, "schema %q missing", name)
	require.NotNil(t, schema.Value)
	if len(schema.Value.Properties) > 0 {
		return schema.Value.Properties
	}
	require.NotEmptyf(t, schema.Value.AllOf, "%s has no properties", name)
	last := schema.Value.AllOf[len(schema.Value.AllOf)-1]
	require.NotNil(t, last.Value)
	return last.Value.Properties
}

// findParameterOrNil is findParameter without failing, for negative assertions.
func findParameterOrNil(operation *openapi3.Operation, name string) *openapi3.Parameter {
	for _, parameter := range operation.Parameters {
		if parameter.Value != nil && parameter.Value.Name == name {
			return parameter.Value
		}
	}
	return nil
}

// TestOpenAPIApplyModifiers pins the idempotency, wait and dry-run surface: the
// parameters each write operation accepts, the replay header, the 501 on
// rollback and the document fields a caller can rely on.
func TestOpenAPIApplyModifiers(t *testing.T) {
	doc := loadOpenAPI(t)

	// Service create: idempotency, dry run and wait.
	create := doc.Paths.Find("/v1/services").Post
	require.NotNil(t, create, "POST /v1/services missing")

	idempotencyKey := findParameter(t, create.Parameters, "Idempotency-Key")
	assert.Equal(t, "header", idempotencyKey.In)
	assert.False(t, idempotencyKey.Required)
	require.NotNil(t, idempotencyKey.Schema.Value)
	require.NotNil(t, idempotencyKey.Schema.Value.MaxLength)
	assert.EqualValues(t, 255, *idempotencyKey.Schema.Value.MaxLength)
	assert.Contains(t, idempotencyKey.Description, "idempotency_key_reuse")
	assert.Contains(t, idempotencyKey.Description, "in_progress")

	dryRun := findParameter(t, create.Parameters, "dryRun")
	assert.Equal(t, "query", dryRun.In)
	assert.False(t, dryRun.Required)

	wait := findParameter(t, create.Parameters, "wait")
	require.NotNil(t, wait.Schema.Value)
	require.Len(t, wait.Schema.Value.Enum, 1)
	assert.Equal(t, "converged", wait.Schema.Value.Enum[0])

	timeout := findParameter(t, create.Parameters, "timeoutMs")
	require.NotNil(t, timeout.Schema.Value.Max)
	assert.EqualValues(t, 600000, *timeout.Schema.Value.Max)
	assert.EqualValues(t, 60000, timeout.Schema.Value.Default)

	replayHeader := create.Responses.Value("201").Value.Headers["Idempotency-Replayed"]
	require.NotNil(t, replayHeader, "201 must document Idempotency-Replayed")

	// Service patch: dry run and wait, but not idempotency (PATCH bodies are not
	// stored: the middleware only wraps POST).
	update := doc.Paths.Find("/v1/services/{id}").Patch
	require.NotNil(t, findParameter(t, update.Parameters, "dryRun"))
	require.NotNil(t, findParameter(t, update.Parameters, "wait"))
	require.NotNil(t, findParameter(t, update.Parameters, "timeoutMs"))
	assert.Nil(t, findParameterOrNil(update, "Idempotency-Key"),
		"only a POST can be idempotency-replayed")

	// Rollback: idempotency + wait, and a documented 501 for a dry run.
	rollback := doc.Paths.Find("/v1/services/{id}/rollback").Post
	require.NotNil(t, findParameter(t, rollback.Parameters, "Idempotency-Key"))
	require.NotNil(t, findParameter(t, rollback.Parameters, "wait"))
	require.NotNil(t, findParameter(t, rollback.Parameters, "dryRun"))
	requireTypedResponseFor(t, rollback, "501", "ErrorDocument")
	notImplemented := doc.Components.Responses["NotImplemented"]
	require.NotNil(t, notImplemented)
	require.NotNil(t, notImplemented.Value.Description)
	assert.Contains(t, *notImplemented.Value.Description, "dry run")

	// Named creates take idempotency + dry run; named patches take dry run only.
	for _, path := range []string{"/v1/configs", "/v1/secrets", "/v1/networks"} {
		post := doc.Paths.Find(path).Post
		require.NotNilf(t, post, "POST %s missing", path)
		require.NotNilf(t, findParameter(t, post.Parameters, "Idempotency-Key"),
			"%s must accept Idempotency-Key", path)
		require.NotNilf(t, findParameter(t, post.Parameters, "dryRun"),
			"%s must accept dryRun", path)
		assert.Nilf(t, findParameterOrNil(post, "wait"),
			"%s must not advertise wait", path)
		require.NotNilf(t, post.Responses.Value("201").Value.Headers["Idempotency-Replayed"],
			"%s must document the replay header", path)
	}
	for _, path := range []string{"/v1/configs/{id}", "/v1/secrets/{id}", "/v1/nodes/{id}"} {
		patch := doc.Paths.Find(path).Patch
		require.NotNilf(t, patch, "PATCH %s missing", path)
		require.NotNilf(t, findParameter(t, patch.Parameters, "dryRun"),
			"%s must accept dryRun", path)
		assert.Nilf(t, findParameterOrNil(patch, "wait"),
			"%s must not advertise wait", path)
	}

	// A delete takes no dry run; its description says so.
	for _, path := range []string{"/v1/services/{id}", "/v1/configs/{id}", "/v1/secrets/{id}", "/v1/networks/{id}"} {
		remove := doc.Paths.Find(path).Delete
		require.NotNilf(t, remove, "DELETE %s missing", path)
		assert.Nilf(t, findParameterOrNil(remove, "dryRun"),
			"%s must not advertise dryRun", path)
		assert.Containsf(t, remove.Description, "dryRun",
			"%s delete description must refuse dryRun", path)
	}

	// Every single-resource document can say it was a dry run; only a service
	// carries wait warnings.
	for _, name := range []string{
		"ServiceDocument", "ConfigDocument", "SecretDocument", "NetworkDocument", "NodeDocument",
	} {
		properties := documentSchemaProperties(t, doc, name)
		assert.Containsf(t, properties, "dryRun", "%s must document dryRun", name)
	}

	serviceProperties := documentSchemaProperties(t, doc, "ServiceDocument")
	assert.Contains(t, serviceProperties, "warnings")

	document := ServiceDocument{
		SchemaVersion: SchemaVersion,
		Kind:          KindService,
		Service: Service{
			ID: "svc-1", Name: "web", Revision: "7",
			Spec:   ServiceSpec{Mode: "replicated", Task: TaskSpec{Image: "nginx"}},
			Status: ServiceStatus{DesiredTasks: 2, Converged: false},
		},
		DryRun:   true,
		Warnings: []Warning{{Code: "wait_timeout", Message: "service did not converge within 150ms"}},
	}
	data, err := json.Marshal(document)
	require.NoError(t, err)
	var decoded any
	require.NoError(t, json.Unmarshal(data, &decoded))
	validateAgainst(t, doc, "ServiceDocument", decoded)

	// The Conflict response documents both idempotency reasons.
	conflict := doc.Components.Responses["Conflict"]
	require.NotNil(t, conflict)
	require.NotNil(t, conflict.Value.Description)
	assert.Contains(t, *conflict.Value.Description, "idempotency_key_reuse")
	assert.Contains(t, *conflict.Value.Description, "in_progress")
}

func TestOpenAPIEvents(t *testing.T) {
	doc := loadOpenAPI(t)
	require.NotNil(t, doc.Paths.Find("/v1/events"))

	event := NewEvent("18422", "2026-10-19T10:04:12Z", "service.updated",
		EventResource{Kind: KindService, ID: "svc-1", Name: "web"}, "17")
	data, err := json.Marshal(event)
	require.NoError(t, err)
	var value any
	require.NoError(t, json.Unmarshal(data, &value))
	validateAgainst(t, doc, "Event", value)
}

// TestOpenAPIHealth pins the health capability surface: the query parameter on
// both task reads, the 207 partial list, the 501 capability failure and the
// single-task 503. The 207 body is a normal TaskList, never an Error document.
func TestOpenAPIHealth(t *testing.T) {
	doc := loadOpenAPI(t)

	marshal := func(t *testing.T, value any) any {
		t.Helper()
		data, err := json.Marshal(value)
		require.NoError(t, err)
		var generic any
		require.NoError(t, json.Unmarshal(data, &generic))
		return generic
	}

	for _, path := range []string{"/v1/tasks", "/v1/tasks/{id}"} {
		item := doc.Paths.Find(path)
		require.NotNilf(t, item, "%s path missing", path)
		require.NotNilf(t, item.Get, "%s GET operation missing", path)

		param := findParameter(t, item.Get.Parameters, "health")
		assert.Equal(t, "query", param.In, "%s health must be a query parameter", path)
		assert.False(t, param.Required, "%s health is optional", path)
		require.NotNilf(t, param.Schema, "%s health schema missing", path)
		assert.True(t, param.Schema.Value.Type.Is("boolean"),
			"%s health must be a boolean", path)
		assert.Contains(t, param.Description, "health",
			"%s health must be documented", path)
	}

	// The list answers a 207 partial with a normal TaskList, readable tasks
	// included.
	requireTypedResponse(t, doc, "/v1/tasks", "207", "TaskList")
	partial := doc.Paths.Find("/v1/tasks").Get.Responses.Value("207")
	require.NotNil(t, partial.Value.Description)
	assert.Contains(t, *partial.Value.Description, "partial")
	assert.Contains(t, *partial.Value.Description, "items")

	// Every partial[].error is a complete Error: the schema requires the same
	// fields as ErrorDocument.error plus requestId.
	partialError := doc.Components.Schemas["Partial"].Value.Properties["error"].Value
	require.NotEmpty(t, partialError.AllOf, "Partial.error must extend Error")
	requestIDRequired := false
	for _, clause := range partialError.AllOf {
		require.NotNil(t, clause.Value)
		for _, field := range clause.Value.Required {
			if field == "requestId" {
				requestIDRequired = true
			}
		}
	}
	assert.True(t, requestIDRequired, "Partial.error must require requestId")

	enriched := NewTaskList([]Task{
		{ID: "t1abc", Revision: "42", ServiceID: "svc-1", State: "running", DesiredState: "running",
			NodeID: "n1", NodeName: "node-1", Health: &TaskHealth{Status: "healthy"}},
		{ID: "t2def", Revision: "43", ServiceID: "svc-1", State: "running", DesiredState: "running",
			NodeID: "n2", NodeName: "node-2"},
	})
	enriched.Partial = []Partial{{
		NodeID:   "n2",
		NodeName: "node-2",
		Error: Error{
			Code:      "unavailable",
			Message:   `health is unavailable for node "node-2"`,
			RequestID: "req_abc123def456",
			Retryable: true,
			Details:   map[string]any{"nodeId": "n2"},
		},
	}}
	validateAgainst(t, doc, "TaskList", marshal(t, enriched))

	// The capability and single-task failures are typed error documents.
	requireTypedResponse(t, doc, "/v1/tasks", "501", "ErrorDocument")
	requireTypedResponse(t, doc, "/v1/tasks/{id}", "501", "ErrorDocument")
	requireTypedResponse(t, doc, "/v1/tasks/{id}", "503", "ErrorDocument")

	// TaskHealth is exactly the Go DTO: five camelCase fields.
	validateAgainst(t, doc, "TaskHealth", marshal(t, TaskHealth{
		Status:        "failing",
		FailingStreak: 2,
		Output:        "connection refused",
		Command:       "curl -fsS http://127.0.0.1/",
		CheckedAt:     "2026-10-24T12:00:00.123456789Z",
	}))

	properties := documentSchemaProperties(t, doc, "TaskHealth")
	for _, key := range []string{"status", "failingStreak", "output", "command", "checkedAt"} {
		assert.Containsf(t, properties, key, "TaskHealth.%s missing", key)
	}
	assert.NotContains(t, properties, "failing_streak", "health keys are camelCase")

	status := doc.Components.Schemas["TaskHealth"].Value.Properties["status"].Value
	assert.ElementsMatch(t,
		[]string{"starting", "healthy", "failing", "unhealthy"}, status.Enum,
		"the status enum must match runtime.VMHealth states")
}

// TestOpenAPISnapshotContract pins the experimental snapshot surface: the eight
// paths, the 501 on every operation, the flat forms' required task parameter and
// a Snapshot schema that matches the Go tags exactly.
func TestOpenAPISnapshotContract(t *testing.T) {
	doc := loadOpenAPI(t)

	decode := func(t *testing.T, value any) any {
		t.Helper()
		data, err := json.Marshal(value)
		require.NoError(t, err)
		var generic any
		require.NoError(t, json.Unmarshal(data, &generic))
		return generic
	}

	validateAgainst(t, doc, "SnapshotDocument", decode(t, NewSnapshotDocument(Snapshot{
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
	})))
	validateAgainst(t, doc, "SnapshotList", decode(t, NewSnapshotList(nil)))
	validateAgainst(t, doc, "SnapshotList", decode(t, NewSnapshotList([]Snapshot{{ID: "snap-1"}})))
	validateAgainst(t, doc, "SnapshotCreateRequest", decode(t, SnapshotCreateRequest{}))
	validateAgainst(t, doc, "SnapshotCreateRequest", decode(t, SnapshotCreateRequest{
		VCPUs:      2,
		MemoryMB:   1024,
		RootfsPath: "/var/lib/firecracker/rootfs/x",
	}))

	properties := documentSchemaProperties(t, doc, "Snapshot")
	for _, key := range []string{
		"id", "taskId", "serviceId", "nodeId", "createdAt",
		"sizeBytes", "memoryMB", "vcpuCount", "rootfsPath", "checksum",
	} {
		assert.Containsf(t, properties, key, "Snapshot.%s missing", key)
	}
	for _, forbidden := range []string{"memoryPath", "statePath", "memory_path", "state_path"} {
		assert.NotContains(t, properties, forbidden,
			"a snapshot's memory and state paths must never be part of the contract")
	}

	operations := map[string]*openapi3.Operation{
		"/v1/tasks/{id}/snapshots":         doc.Paths.Find("/v1/tasks/{id}/snapshots").Post,
		"/v1/tasks/{id}/snapshots get":     doc.Paths.Find("/v1/tasks/{id}/snapshots").Get,
		"/v1/tasks/{id}/snapshots/restore": doc.Paths.Find("/v1/tasks/{id}/snapshots/{sid}/restore").Post,
		"/v1/tasks/{id}/snapshots/delete":  doc.Paths.Find("/v1/tasks/{id}/snapshots/{sid}").Delete,
		"/v1/tasks/{id}/restored":          doc.Paths.Find("/v1/tasks/{id}/restored").Delete,
		"/v1/snapshots":                    doc.Paths.Find("/v1/snapshots").Get,
		"/v1/snapshots/restore":            doc.Paths.Find("/v1/snapshots/{id}/restore").Post,
		"/v1/snapshots/delete":             doc.Paths.Find("/v1/snapshots/{id}").Delete,
	}
	for name, operation := range operations {
		require.NotNilf(t, operation, "%s missing", name)
		require.NotNilf(t, operation.Responses.Status(501),
			"%s must document the capability gate", name)
	}

	create := doc.Paths.Find("/v1/tasks/{id}/snapshots").Post
	require.NotNil(t, create.RequestBody)
	require.False(t, create.RequestBody.Value.Required, "the create body is optional")

	for _, operation := range []*openapi3.Operation{
		doc.Paths.Find("/v1/snapshots").Get,
		doc.Paths.Find("/v1/snapshots/{id}/restore").Post,
		doc.Paths.Find("/v1/snapshots/{id}").Delete,
	} {
		task := findParameter(t, operation.Parameters, "task")
		assert.Equal(t, "query", task.In)
		assert.True(t, task.Required, "a flat snapshot route requires ?task=")
	}

	// Restore is experimental and reuses one socket per task, so the contract must
	// name both the live-socket guard and the residual untracked-process risk.
	for name, operation := range map[string]*openapi3.Operation{
		"task-scoped restore": doc.Paths.Find("/v1/tasks/{id}/snapshots/{sid}/restore").Post,
		"flat restore":        doc.Paths.Find("/v1/snapshots/{id}/restore").Post,
	} {
		assert.Containsf(t, operation.Description, "restore_already_running",
			"%s must document the live-socket guard", name)
		assert.Containsf(t, operation.Description, "untracked",
			"%s must state the residual untracked-process risk", name)
		assert.Containsf(t, operation.Description, "socket_dir_unconfigured",
			"%s must document the unconfigured socket directory", name)
	}

	// The two create-time failures are distinct: a task SwarmKit knows is not
	// running is 404, a socket that disappeared under the node is 409.
	assert.Contains(t, create.Description, "task_not_running")
	assert.Contains(t, create.Description, "vm_socket_missing")

	// Stopping a restored VM is its own documented contract: 204, the
	// no_restored_vm reason, a 405 for any other method and the audit action.
	stop := doc.Paths.Find("/v1/tasks/{id}/restored").Delete
	require.NotNil(t, stop)
	assert.Contains(t, stop.Description, "no_restored_vm")
	assert.Contains(t, stop.Description, "task.restored.stop")
	assert.Contains(t, stop.Description, "SIGTERM")
	require.NotNil(t, stop.Responses.Status(204))
	require.NotNil(t, stop.Responses.Status(404))
	require.NotNil(t, stop.Responses.Status(405))
}

// TestOpenAPIMigrationSurface pins the live-migration route and its document. The
// call is synchronous and its answer is the report, so the contract has to say
// what 201 means for both outcomes, which failure statuses a manager may answer,
// and that the failing phase travels in details.phase.
func TestOpenAPIMigrationSurface(t *testing.T) {
	doc := loadOpenAPI(t)

	item := doc.Paths.Find("/v1/migrations")
	require.NotNil(t, item, "/v1/migrations path missing")
	require.NotNil(t, item.Post, "POST /v1/migrations missing")
	assert.Nil(t, item.Get, "there is no status route: the call is the report")

	op := item.Post
	assert.Contains(t, op.Description, "migrations:write", "the operation must name its scope")
	assert.Contains(t, op.Description, "admin", "the operation must say who holds the scope")
	assert.Contains(t, op.Description, "details.phase", "the failing phase is contract")
	assert.Contains(t, op.Description, "pending", "the pending outcome is contract")
	assert.Contains(t, op.Description, "task.migrate", "the audit action is documented")
	assert.Contains(t, op.Description, "not_leader", "the follower refusal is contract")
	assert.Contains(t, op.Description, "one minute of", "the detached cleanup bound is documented")
	assert.NotContains(t, op.Description, "cleanup included",
		"cleanup runs detached with its own bound, so it is not inside the call's")
	assert.Empty(t, op.Security, "a migration must inherit the global bearer requirement")

	require.NotNil(t, op.RequestBody)
	require.True(t, op.RequestBody.Value.Required)
	request := op.RequestBody.Value.Content.Get(v1MediaType)
	require.NotNilf(t, request, "the migration body must be %s", v1MediaType)
	require.Equal(t, "#/components/schemas/MigrationRequest", request.Schema.Ref)

	requireTypedResponseFor(t, op, "201", "Migration")
	for _, status := range []string{
		"400", "401", "403", "404", "409", "413", "415", "429", "501", "503", "504",
	} {
		requireTypedResponseFor(t, op, status, "ErrorDocument")
	}

	validateAgainst(t, doc, "MigrationRequest", map[string]any{
		"taskId": "task-1", "targetNode": "node-b",
	})
	// Both fields are required: the manager cannot infer either one.
	require.Contains(t, doc.Components.Schemas["MigrationRequest"].Value.Required, "taskId")
	require.Contains(t, doc.Components.Schemas["MigrationRequest"].Value.Required, "targetNode")

	phases := []MigrationPhase{
		{Name: MigrationPhaseValidate, StartedAt: "2026-10-31T12:00:00Z", FinishedAt: "2026-10-31T12:00:00.010Z", Result: MigrationPhaseOK},
		{Name: MigrationPhaseVerify, StartedAt: "2026-10-31T12:01:00Z", Result: MigrationPhasePending, Message: "replacement_not_running"},
	}
	for _, phase := range phases {
		data, err := json.Marshal(phase)
		require.NoError(t, err)
		var generic any
		require.NoError(t, json.Unmarshal(data, &generic))
		validateAgainst(t, doc, "MigrationPhase", generic)
	}

	for _, outcome := range []string{MigrationOutcomeMoved, MigrationOutcomePending} {
		value := Migration{
			SchemaVersion: SchemaVersion,
			Kind:          KindMigration,
			ID:            "mig-0123456789abcdef",
			TaskID:        "task-1",
			ServiceID:     "svc-1",
			SourceNode:    "node-a",
			TargetNode:    "node-b",
			Phases:        phases,
			DowntimeMs:    1234,
			Outcome:       outcome,
		}
		data, err := json.Marshal(value)
		require.NoError(t, err)
		var generic any
		require.NoError(t, json.Unmarshal(data, &generic))
		validateAgainst(t, doc, "Migration", generic)
	}

	properties := doc.Components.Schemas["Migration"].Value.Properties
	for _, key := range []string{
		"schemaVersion", "kind", "id", "taskId", "serviceId", "sourceNode",
		"targetNode", "phases", "downtimeMs", "outcome",
	} {
		assert.Containsf(t, properties, key, "Migration.%s missing", key)
	}
	assert.NotContains(t, properties, "downtime_ms", "migration keys are camelCase")

	capabilities := doc.Components.Schemas["VersionInfo"].Value.Properties["capabilities"]
	require.NotNil(t, capabilities)
	assert.Contains(t, capabilities.Value.Description, "migrations",
		"the capability list must name migrations")
}
