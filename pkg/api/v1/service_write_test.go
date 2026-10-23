package v1

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegistryAuthIsWriteOnly pins the invariant by construction: the read types
// have no such field at all, so no read handler can leak one.
func TestRegistryAuthIsWriteOnly(t *testing.T) {
	for _, readType := range []any{TaskSpec{}, ServiceSpec{}, Service{}, ServiceDocument{}, ServiceList{}} {
		_, found := reflect.TypeOf(readType).FieldByName("RegistryAuth")
		assert.Falsef(t, found, "%T must not carry RegistryAuth", readType)
	}

	_, found := reflect.TypeOf(TaskSpecWrite{}).FieldByName("RegistryAuth")
	assert.True(t, found, "TaskSpecWrite is the one write-only carrier")
}

func TestServiceWriteMarshalsRegistryAuth(t *testing.T) {
	replicas := int64(2)
	write := ServiceWrite{
		Name:   "web",
		Labels: map[string]string{"team": "payments"},
		Spec: ServiceSpecWrite{
			Mode:     "replicated",
			Replicas: &replicas,
			Task: TaskSpecWrite{
				TaskSpec: TaskSpec{
					Image:   "nginx:alpine",
					Command: []string{"nginx"},
					Env:     map[string]string{"APP_ENV": "prod"},
				},
				RegistryAuth: "cmVnaXN0cnk=",
			},
		},
	}

	data, err := json.Marshal(write)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, "web", decoded["name"])
	spec := decoded["spec"].(map[string]any)
	assert.Equal(t, "replicated", spec["mode"])
	assert.EqualValues(t, 2, spec["replicas"])

	// TaskSpecWrite embeds TaskSpec, so the JSON fields stay flat.
	task := spec["task"].(map[string]any)
	assert.Equal(t, "nginx:alpine", task["image"])
	assert.Equal(t, []any{"nginx"}, task["command"])
	assert.Equal(t, "cmVnaXN0cnk=", task["registryAuth"],
		"the credential must be on the write document")
}

func TestServiceWriteOmitsEmptyOptionals(t *testing.T) {
	write := ServiceWrite{
		Name: "minimal",
		Spec: ServiceSpecWrite{Task: TaskSpecWrite{TaskSpec: TaskSpec{Image: "busybox"}}},
	}

	data, err := json.Marshal(write)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.NotContains(t, decoded, "labels")

	task := decoded["spec"].(map[string]any)["task"].(map[string]any)
	assert.NotContains(t, task, "registryAuth", "an absent credential is absent, not empty")
}

// TestServiceReadNeverMarshalsRegistryAuth is the behavioural half: even a write
// document that carried a credential produces a read document without one.
func TestServiceReadNeverMarshalsRegistryAuth(t *testing.T) {
	write := ServiceWrite{
		Name: "web",
		Spec: ServiceSpecWrite{Task: TaskSpecWrite{
			TaskSpec:     TaskSpec{Image: "nginx"},
			RegistryAuth: "cmVnaXN0cnk=",
		}},
	}
	encodedWrite, err := json.Marshal(write)
	require.NoError(t, err)
	require.Contains(t, string(encodedWrite), "registryAuth")

	document, err := json.Marshal(NewServiceDocument(Service{
		ID:       "svc-1",
		Name:     write.Name,
		Revision: "7",
		Spec:     ServiceSpec{Mode: "replicated", Task: write.Spec.Task.TaskSpec},
	}))
	require.NoError(t, err)
	assert.NotContains(t, string(document), "registryAuth")
	assert.NotContains(t, string(document), "cmVnaXN0cnk=")
}
