package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/redact"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testRegistryAuthPayload is a recognisable X-Registry-Auth payload — the base64
// encoding of "secret-registry-auth-payload". Any output containing it has
// leaked a credential.
const testRegistryAuthPayload = "c2VjcmV0LXJlZ2lzdHJ5LWF1dGgtcGF5bG9hZA=="

// registryAuthContainer is a container spec carrying the recognisable payload.
func registryAuthContainer() *api.ContainerSpec {
	return &api.ContainerSpec{
		Image:       "registry.test/team/app:1",
		PullOptions: &api.ContainerSpec_PullOptions{RegistryAuth: testRegistryAuthPayload},
	}
}

// registryAuthService is a one-container service carrying the payload.
func registryAuthService() *api.Service {
	return &api.Service{
		ID: "svc-1",
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{Name: "web"},
			Task:        api.TaskSpec{Runtime: &api.TaskSpec_Container{Container: registryAuthContainer()}},
		},
	}
}

// registryAuthTask is a running task carrying the payload.
func registryAuthTask() *api.Task {
	return &api.Task{
		ID:        "task-0000000000000000001",
		ServiceID: "svc-1",
		NodeID:    "node-1",
		Spec:      api.TaskSpec{Runtime: &api.TaskSpec_Container{Container: registryAuthContainer()}},
		Status:    api.TaskStatus{State: api.TaskStateRunning},
	}
}

// TestMarshalJSONRedactsRegistryAuth covers swarmctl's JSON printing path: every
// service and task response that `inspect` prints goes through marshalJSON, so
// the X-Registry-Auth payload stored on the spec can never be printed. The
// redaction itself is pkg/redact, shared with the swarmcracker CLI.
func TestMarshalJSONRedactsRegistryAuth(t *testing.T) {
	cases := map[string]interface{}{
		"service": registryAuthService(),
		"task":    registryAuthTask(),
	}

	for name, obj := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := marshalJSON(obj)
			require.NoError(t, err)

			assert.NotContains(t, string(data), testRegistryAuthPayload, "the credential must never be printed")
			assert.Contains(t, string(data), redact.Placeholder, "the placeholder must show that credentials are attached")

			// Everything but the credential is byte-identical to the plain
			// rendering of the response.
			plain, err := json.MarshalIndent(obj, "", "  ")
			require.NoError(t, err)
			expected := strings.Replace(string(plain), testRegistryAuthPayload, redact.Placeholder, 1)
			assert.JSONEq(t, expected, string(data), "only the credential may change")
		})
	}
}

// TestMarshalJSONRedactsRegistryAuthLists covers the other shapes a caller can
// hand the choke point: a slice of services or tasks must be redacted element by
// element, not passed through because the switch only knew the single-response
// types.
func TestMarshalJSONRedactsRegistryAuthLists(t *testing.T) {
	cases := map[string]interface{}{
		"services": []*api.Service{registryAuthService()},
		"tasks":    []*api.Task{registryAuthTask()},
	}

	for name, obj := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := marshalJSON(obj)
			require.NoError(t, err)

			assert.NotContains(t, string(data), testRegistryAuthPayload, "the credential must never be printed")
			assert.Contains(t, string(data), redact.Placeholder, "the placeholder must show that credentials are attached")

			plain, err := json.MarshalIndent(obj, "", "  ")
			require.NoError(t, err)
			expected := strings.Replace(string(plain), testRegistryAuthPayload, redact.Placeholder, 1)
			assert.JSONEq(t, expected, string(data), "only the credential may change")
		})
	}
}

// TestMarshalJSONRefusesUnredactedSpecCarrier covers the fail-closed default: a
// type the switch does not redact but that can still hold a container spec is an
// error, not an unredacted document on stdout. A value that cannot carry a spec
// still prints.
func TestMarshalJSONRefusesUnredactedSpecCarrier(t *testing.T) {
	// A bare spec (and a wrapper holding one) is a shape the switch does not
	// handle; printing it would echo the payload.
	spec := api.ServiceSpec{
		Annotations: api.Annotations{Name: "web"},
		Task:        api.TaskSpec{Runtime: &api.TaskSpec_Container{Container: registryAuthContainer()}},
	}
	for name, obj := range map[string]interface{}{
		"service spec": &spec,
		"spec wrapper": struct{ Spec *api.ServiceSpec }{Spec: &spec},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := marshalJSON(obj)
			require.Error(t, err, "a value that can carry a credential must not print unredacted")
			assert.Contains(t, err.Error(), "refusing to print")
		})
	}

	data, err := marshalJSON(map[string]string{"id": "x"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"x"}`, string(data), "a value that cannot carry a credential still prints")
}

// TestMarshalJSONWithoutRegistryAuthIsUnchanged guards against redaction
// changing the output of a service or task that carries no credential.
func TestMarshalJSONWithoutRegistryAuthIsUnchanged(t *testing.T) {
	task := &api.Task{
		ID:   "task-0000000000000000001",
		Spec: api.TaskSpec{Runtime: &api.TaskSpec_Container{Container: &api.ContainerSpec{Image: "alpine:3"}}},
	}

	data, err := marshalJSON(task)
	require.NoError(t, err)
	expected, err := json.MarshalIndent(task, "", "  ")
	require.NoError(t, err)

	assert.JSONEq(t, string(expected), string(data))
	assert.NotContains(t, string(data), redact.Placeholder)
}
