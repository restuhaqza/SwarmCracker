package redact

import (
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

// serviceWithRegistryAuth builds a one-container service carrying the given
// X-Registry-Auth payload (empty leaves PullOptions nil).
func serviceWithRegistryAuth(registryAuth string) *api.Service {
	container := &api.ContainerSpec{Image: "registry.test/team/app:1"}
	if registryAuth != "" {
		container.PullOptions = &api.ContainerSpec_PullOptions{RegistryAuth: registryAuth}
	}
	return &api.Service{
		ID: "svc-1",
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{Name: "web"},
			Task: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{Container: container},
			},
		},
	}
}

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

// assertOnlyCredentialChanged compares the JSON of a redacted object with the
// JSON of the original: they must be identical apart from the one credential
// replaced by the placeholder.
func assertOnlyCredentialChanged(t *testing.T, redacted, original any, wantMasks int) {
	t.Helper()
	gotJSON, err := json.Marshal(redacted)
	require.NoError(t, err)
	origJSON, err := json.Marshal(original)
	require.NoError(t, err)

	assert.NotContains(t, string(gotJSON), testRegistryAuthPayload, "the credential must never be serialized")
	assert.Equal(t, wantMasks, strings.Count(string(gotJSON), Placeholder), "every credential occurrence must be masked")
	expected := strings.ReplaceAll(string(origJSON), testRegistryAuthPayload, Placeholder)
	assert.JSONEq(t, expected, string(gotJSON), "only the credential may change")
}

// TestServiceRedactsRegistryAuth verifies Service masks the credential of a
// service's own spec, shows the placeholder, and leaves the original untouched.
func TestServiceRedactsRegistryAuth(t *testing.T) {
	svc := serviceWithRegistryAuth(testRegistryAuthPayload)

	redacted := Service(svc)
	require.NotSame(t, svc, redacted, "redaction must return a copy")

	assertOnlyCredentialChanged(t, redacted, svc, 1)
	assert.Equal(t, Placeholder, redacted.Spec.Task.GetContainer().PullOptions.RegistryAuth)
	assert.Equal(t, testRegistryAuthPayload, svc.Spec.Task.GetContainer().PullOptions.RegistryAuth,
		"redaction must not mutate the caller's service")
}

// TestServiceRedactsPreviousSpecRegistryAuth verifies an update's previous spec
// is masked too: it is a full ServiceSpec, so the credential it carried before
// the update would otherwise leak from the same document.
func TestServiceRedactsPreviousSpecRegistryAuth(t *testing.T) {
	svc := serviceWithRegistryAuth(testRegistryAuthPayload)
	svc.PreviousSpec = serviceWithRegistryAuth(testRegistryAuthPayload).Spec.Copy()

	redacted := Service(svc)

	assertOnlyCredentialChanged(t, redacted, svc, 2)
	assert.Equal(t, testRegistryAuthPayload, svc.PreviousSpec.Task.GetContainer().PullOptions.RegistryAuth,
		"the previous spec must not be mutated")
}

// TestTaskRedactsRegistryAuth verifies Task masks the credential of a task spec
// and leaves the original untouched.
func TestTaskRedactsRegistryAuth(t *testing.T) {
	task := taskWithRegistryAuth("task-0000000000000000001", testRegistryAuthPayload)

	redacted := Task(task)
	require.NotSame(t, task, redacted, "redaction must return a copy")

	assertOnlyCredentialChanged(t, redacted, task, 1)
	assert.Equal(t, Placeholder, redacted.Spec.GetContainer().PullOptions.RegistryAuth)
	assert.Equal(t, testRegistryAuthPayload, task.Spec.GetContainer().PullOptions.RegistryAuth,
		"redaction must not mutate the caller's task")
}

// TestWithoutRegistryAuthIsUnchanged verifies a service or task that carries no
// credential keeps a nil PullOptions and serializes exactly as before: no
// invented PullOptions and no placeholder.
func TestWithoutRegistryAuthIsUnchanged(t *testing.T) {
	svc := serviceWithRegistryAuth("")
	require.Nil(t, Service(svc).Spec.Task.GetContainer().PullOptions,
		"a credential-less spec must keep a nil PullOptions")
	assertOnlyCredentialChanged(t, Service(svc), svc, 0)

	task := taskWithRegistryAuth("task-0000000000000000001", "")
	require.Nil(t, Task(task).Spec.GetContainer().PullOptions)
	assertOnlyCredentialChanged(t, Task(task), task, 0)
}

// TestNilSpecs covers the nil guards the JSON paths rely on: a nil service or
// task copies to nil rather than panicking, and marshals as null.
func TestNilSpecs(t *testing.T) {
	assert.Nil(t, Service(nil))
	assert.Nil(t, Task(nil))

	data, err := json.Marshal(Service(nil))
	require.NoError(t, err)
	assert.Equal(t, "null", string(data), "a nil service marshals as null")
}
