package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testRegistryAuthPayload is a recognisable X-Registry-Auth payload — the base64
// encoding of "secret-registry-auth-payload". Any output containing it has
// leaked a credential.
const testRegistryAuthPayload = "c2VjcmV0LXJlZ2lzdHJ5LWF1dGgtcGF5bG9hZA=="

// taskWithRegistryAuth builds a running task whose container carries the given
// X-Registry-Auth payload (empty leaves PullOptions nil).
func taskWithRegistryAuth(id, registryAuth string) *api.Task {
	container := &api.ContainerSpec{Image: "registry.test/team/app:1"}
	if registryAuth != "" {
		container.PullOptions = &api.ContainerSpec_PullOptions{RegistryAuth: registryAuth}
	}
	return &api.Task{
		ID:        id,
		ServiceID: "svc-1",
		NodeID:    "node-1",
		Spec:      api.TaskSpec{Runtime: &api.TaskSpec_Container{Container: container}},
		Status:    api.TaskStatus{State: api.TaskStateRunning},
	}
}

// taskSpecMaskedJSON is the task's spec as JSON with its one credential replaced
// by the placeholder: the only spec document redaction is allowed to produce.
func taskSpecMaskedJSON(t *testing.T, task *api.Task) string {
	t.Helper()
	data, err := json.Marshal(task.Spec)
	require.NoError(t, err)
	require.Contains(t, string(data), testRegistryAuthPayload, "the fixture must carry the credential")
	return strings.Replace(string(data), testRegistryAuthPayload, redactedRegistryAuth, 1)
}

// specFromOutput extracts the "spec" object of the first task document in got,
// which is either a task object (inspect) or an array of them (ls / ps).
func specFromOutput(t *testing.T, got string) string {
	t.Helper()
	var doc any
	require.NoError(t, json.Unmarshal([]byte(got), &doc))
	if list, ok := doc.([]any); ok {
		require.NotEmpty(t, list, "the JSON listing must contain the task")
		doc = list[0]
	}
	obj, ok := doc.(map[string]any)
	require.True(t, ok, "output must be a task object or an array of them")
	spec, ok := obj["spec"]
	require.True(t, ok, "the task spec must still be serialized")
	data, err := json.Marshal(spec)
	require.NoError(t, err)
	return string(data)
}

// TestServiceJSONRedactsRegistryAuth verifies both service JSON paths — the
// `service ls` listing and `service inspect` — print a placeholder instead of
// the X-Registry-Auth payload, change nothing else about the spec, and leave the
// response object other code still uses untouched.
func TestServiceJSONRedactsRegistryAuth(t *testing.T) {
	svc := serviceWithRegistryAuth(t, testRegistryAuthPayload)

	cases := map[string]func() ([]byte, error){
		"service ls":                    func() ([]byte, error) { return marshalServicesJSON([]*api.Service{svc}) },
		"service inspect --pretty":      func() ([]byte, error) { return marshalServiceJSON(svc, true) },
		"service inspect (no --pretty)": func() ([]byte, error) { return marshalServiceJSON(svc, false) },
	}

	for name, marshal := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := marshal()
			require.NoError(t, err)

			assert.NotContains(t, string(data), testRegistryAuthPayload, "the credential must never reach stdout")
			assert.Contains(t, string(data), redactedRegistryAuth, "the placeholder must show that credentials are attached")

			// The rest of the spec round-trips: the document differs from the
			// service's own JSON only in the masked credential.
			original, err := json.Marshal(svc)
			require.NoError(t, err)
			expected := strings.Replace(string(original), testRegistryAuthPayload, redactedRegistryAuth, 1)
			assert.JSONEq(t, expected, bareServiceJSON(t, data), "only the credential may change")

			assert.Equal(t, testRegistryAuthPayload, svc.Spec.Task.GetContainer().PullOptions.RegistryAuth,
				"redaction must not mutate the response object")
		})
	}
}

// bareServiceJSON returns the service document inside data: the listing path
// wraps it in a one-element array, the inspect path emits it bare.
func bareServiceJSON(t *testing.T, data []byte) string {
	t.Helper()
	var list []json.RawMessage
	if err := json.Unmarshal(data, &list); err == nil {
		require.Len(t, list, 1, "the listing must hold exactly the service under test")
		return string(list[0])
	}
	return string(data)
}

// TestServiceJSONRedactsPreviousSpecRegistryAuth verifies an update's previous
// spec is masked too: it is a full ServiceSpec, so the credential it carried
// before the update would otherwise leak from the same document.
func TestServiceJSONRedactsPreviousSpecRegistryAuth(t *testing.T) {
	svc := serviceWithRegistryAuth(t, testRegistryAuthPayload)
	svc.PreviousSpec = serviceWithRegistryAuth(t, testRegistryAuthPayload).Spec.Copy()

	data, err := marshalServiceJSON(svc, true)
	require.NoError(t, err)

	assert.NotContains(t, string(data), testRegistryAuthPayload)
	assert.Equal(t, 2, strings.Count(string(data), redactedRegistryAuth), "spec and previous spec must both be masked")
	assert.Equal(t, testRegistryAuthPayload, svc.PreviousSpec.Task.GetContainer().PullOptions.RegistryAuth,
		"the previous spec must not be mutated")
}

// TestTaskJSONRedactsRegistryAuth verifies every task JSON path — `task inspect`,
// `task ls` and `service ps` — masks the credential, shows the placeholder, and
// leaves the task object other code still uses untouched.
func TestTaskJSONRedactsRegistryAuth(t *testing.T) {
	t.Run("task inspect", func(t *testing.T) {
		task := taskWithRegistryAuth("task-0000000000000000001", testRegistryAuthPayload)
		client := &fakeTaskClient{tasks: []*api.Task{task}}

		var buf bytes.Buffer
		require.NoError(t, inspectTaskWithClient(context.Background(), client, &buf, task.ID, "json", true))

		assertTaskOutputRedacted(t, buf.String(), task)
	})

	t.Run("task ls", func(t *testing.T) {
		task := taskWithRegistryAuth("task-0000000000000000002", testRegistryAuthPayload)
		client := &fakeTaskClient{tasks: []*api.Task{task}}

		var buf bytes.Buffer
		require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "json", "", false, false, false, "", "", derivedHealthOptions()))

		assertTaskOutputRedacted(t, buf.String(), task)
	})

	t.Run("service ps", func(t *testing.T) {
		task := taskWithRegistryAuth("task-0000000000000000003", testRegistryAuthPayload)
		client := &fakeServiceTaskClient{
			services: []*api.Service{{ID: "svc-1", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "web"}}}},
			tasks:    []*api.Task{task},
		}

		var buf bytes.Buffer
		require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "json", "", false, false, derivedHealthOptions()))

		assertTaskOutputRedacted(t, buf.String(), task)
	})
}

// assertTaskOutputRedacted checks one task JSON document: no credential, a
// visible placeholder, a spec that otherwise round-trips, and no mutation of the
// task object.
func assertTaskOutputRedacted(t *testing.T, got string, task *api.Task) {
	t.Helper()
	assert.NotContains(t, got, testRegistryAuthPayload, "the credential must never reach stdout")
	assert.Contains(t, got, redactedRegistryAuth, "the placeholder must show that credentials are attached")
	assert.JSONEq(t, taskSpecMaskedJSON(t, task), specFromOutput(t, got), "only the credential may change in the spec")
	assert.Equal(t, testRegistryAuthPayload, task.Spec.GetContainer().PullOptions.RegistryAuth,
		"redaction must not mutate the task object")
}

// TestTaskTableOutputCannotCarryRegistryAuth asserts the text renderings of the
// task paths print no credential — they only ever render the image, state and
// identity columns.
func TestTaskTableOutputCannotCarryRegistryAuth(t *testing.T) {
	newClient := func() *fakeTaskClient {
		return &fakeTaskClient{tasks: []*api.Task{taskWithRegistryAuth("task-0000000000000000001", testRegistryAuthPayload)}}
	}
	newServiceClient := func() *fakeServiceTaskClient {
		return &fakeServiceTaskClient{
			services: []*api.Service{{ID: "svc-1", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "web"}}}},
			tasks:    []*api.Task{taskWithRegistryAuth("task-0000000000000000002", testRegistryAuthPayload)},
		}
	}

	cases := map[string]func(w *bytes.Buffer) error{
		"task ls": func(w *bytes.Buffer) error {
			return listTasksWithClient(context.Background(), newClient(), w, "table", "", false, false, false, "", "", derivedHealthOptions())
		},
		"task inspect": func(w *bytes.Buffer) error {
			return inspectTaskWithClient(context.Background(), newClient(), w, "task-0000000000000000001", "table", true)
		},
		"service ps": func(w *bytes.Buffer) error {
			return listServiceTasksWithClient(context.Background(), newServiceClient(), w, "web", "table", "", false, false, derivedHealthOptions())
		},
	}

	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, run(&buf))
			assert.NotContains(t, buf.String(), testRegistryAuthPayload)
			assert.NotContains(t, buf.String(), redactedRegistryAuth)
		})
	}
}

// TestJSONWithoutRegistryAuthIsUnchanged verifies a service or task that carries
// no credential serializes exactly as before: no invented PullOptions and no
// placeholder.
func TestJSONWithoutRegistryAuthIsUnchanged(t *testing.T) {
	svc := serviceWithRegistryAuth(t, "")
	assert.Nil(t, redactedService(svc).Spec.Task.GetContainer().PullOptions, "a credential-less spec must keep a nil PullOptions")

	data, err := marshalServiceJSON(svc, true)
	require.NoError(t, err)
	expected, err := json.MarshalIndent(svc, "", "  ")
	require.NoError(t, err)
	assert.JSONEq(t, string(expected), string(data))
	assert.NotContains(t, string(data), redactedRegistryAuth)

	task := taskWithRegistryAuth("task-0000000000000000001", "")
	assert.Nil(t, redactedTask(task).Spec.GetContainer().PullOptions)

	client := &fakeTaskClient{tasks: []*api.Task{task}}
	var buf bytes.Buffer
	require.NoError(t, inspectTaskWithClient(context.Background(), client, &buf, task.ID, "json", true))
	expectedTask, err := json.MarshalIndent(task, "", "  ")
	require.NoError(t, err)
	assert.JSONEq(t, string(expectedTask), buf.String())
}

// TestRedactedNilSpecs covers the nil guards the JSON paths rely on: a nil
// service or task copies to nil rather than panicking.
func TestRedactedNilSpecs(t *testing.T) {
	assert.Nil(t, redactedService(nil))
	assert.Nil(t, redactedTask(nil))

	data, err := marshalServiceJSON(nil, true)
	require.NoError(t, err)
	assert.Equal(t, "null", string(data), "a nil service marshals as null")
}
