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

// TestMarshalJSONRedactsRegistryAuth covers swarmctl's JSON printing path: every
// service and task response that `inspect` prints goes through marshalJSON, so
// the X-Registry-Auth payload stored on the spec can never be printed. The
// redaction itself is pkg/redact, shared with the swarmcracker CLI.
func TestMarshalJSONRedactsRegistryAuth(t *testing.T) {
	container := &api.ContainerSpec{
		Image:       "registry.test/team/app:1",
		PullOptions: &api.ContainerSpec_PullOptions{RegistryAuth: testRegistryAuthPayload},
	}
	svc := &api.Service{
		ID: "svc-1",
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{Name: "web"},
			Task:        api.TaskSpec{Runtime: &api.TaskSpec_Container{Container: container}},
		},
	}
	task := &api.Task{
		ID:        "task-0000000000000000001",
		ServiceID: "svc-1",
		NodeID:    "node-1",
		Spec:      api.TaskSpec{Runtime: &api.TaskSpec_Container{Container: container.Copy()}},
		Status:    api.TaskStatus{State: api.TaskStateRunning},
	}

	cases := map[string]interface{}{
		"service": svc,
		"task":    task,
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
