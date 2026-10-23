package client

import (
	"encoding/json"
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func watchMeta(revision uint64) api.Meta {
	ts, _ := gogotypes.TimestampProto(time.Date(2026, 10, 19, 10, 4, 12, 0, time.UTC))
	return api.Meta{Version: api.Version{Index: revision}, UpdatedAt: ts}
}

func TestEventFromWatchMapsService(t *testing.T) {
	service := serviceFixture("svc-1", "web", 17)
	service.Meta = watchMeta(17)
	service.Spec.Annotations.Labels = map[string]string{"team": "payments"}
	event, ok := eventFromWatch(&api.WatchMessage_Event{
		Action: api.WatchActionKindUpdate,
		Object: &api.Object{Object: &api.Object_Service{Service: service}},
	}, "18422", v1.EventOptions{IncludeObject: true})
	require.True(t, ok)

	assert.Equal(t, v1.KindEvent, event.Kind)
	assert.Equal(t, "18422", event.Cursor)
	assert.Equal(t, "service.updated", event.Type)
	assert.Equal(t, v1.EventResource{Kind: v1.KindService, ID: "svc-1", Name: "web"}, event.Resource)
	assert.Equal(t, "17", event.Revision)
	assert.Equal(t, "2026-10-19T10:04:12Z", event.At)
	assert.Equal(t, map[string]string{"team": "payments"}, event.Labels)
	require.NotNil(t, event.Object)
	_, ok = event.Object.(v1.Service)
	assert.True(t, ok)
}

func TestEventFromWatchMapsTaskNodeAndRemoved(t *testing.T) {
	task := &api.Task{
		ID:                 "task-1",
		Meta:               watchMeta(4),
		Status:             api.TaskStatus{State: api.TaskStateRunning},
		Annotations:        api.Annotations{Name: "web.1", Labels: map[string]string{"team": "payments"}},
		ServiceAnnotations: api.Annotations{Labels: map[string]string{"team": "payments"}},
	}
	event, ok := eventFromWatch(&api.WatchMessage_Event{
		Action: api.WatchActionKindCreate,
		Object: &api.Object{Object: &api.Object_Task{Task: task}},
	}, "5", v1.EventOptions{})
	require.True(t, ok)
	assert.Equal(t, "task.created", event.Type)
	assert.Equal(t, "web.1", event.Resource.Name)
	assert.Equal(t, map[string]string{"team": "payments"}, event.Labels)
	assert.Nil(t, event.Object, "include=object was not requested")

	node := nodeFixture("node-1", "worker-a", 9)
	event, ok = eventFromWatch(&api.WatchMessage_Event{
		Action: api.WatchActionKindRemove,
		Object: &api.Object{Object: &api.Object_Node{Node: node}},
	}, "10", v1.EventOptions{})
	require.True(t, ok)
	assert.Equal(t, "node.removed", event.Type)
	assert.Equal(t, "worker-a", event.Resource.Name)
}

func TestEventFromWatchConfigValuesGate(t *testing.T) {
	config := configFixture("cfg-1", "app", 6, nil, "listen 8080")

	plain, ok := eventFromWatch(&api.WatchMessage_Event{
		Action: api.WatchActionKindUpdate,
		Object: &api.Object{Object: &api.Object_Config{Config: config}},
	}, "1", v1.EventOptions{IncludeObject: true})
	require.True(t, ok)
	plainObject, ok := plain.Object.(v1.Config)
	require.True(t, ok)
	assert.Nil(t, plainObject.Data, "a plain event reader never sees config data")

	privileged, ok := eventFromWatch(&api.WatchMessage_Event{
		Action: api.WatchActionKindUpdate,
		Object: &api.Object{Object: &api.Object_Config{Config: config}},
	}, "1", v1.EventOptions{IncludeObject: true, IncludeConfigValues: true})
	require.True(t, ok)
	privilegedObject, ok := privileged.Object.(v1.Config)
	require.True(t, ok)
	require.NotNil(t, privilegedObject.Data)
	assert.Equal(t, "listen 8080", *privilegedObject.Data)
}

func TestEventFromWatchSecretNeverCarriesData(t *testing.T) {
	secret := secretFixture("sec-1", "token", 3, nil, "c2VjcmV0")
	event, ok := eventFromWatch(&api.WatchMessage_Event{
		Action: api.WatchActionKindUpdate,
		Object: &api.Object{Object: &api.Object_Secret{Secret: secret}},
	}, "2", v1.EventOptions{IncludeObject: true})
	require.True(t, ok)
	secrets, ok := event.Object.(v1.Secret)
	require.True(t, ok)
	body, err := json.Marshal(secrets)
	require.NoError(t, err)
	assert.NotContains(t, string(body), `"data"`)
}

func TestEventFromWatchSkipsUnknownObject(t *testing.T) {
	_, ok := eventFromWatch(&api.WatchMessage_Event{
		Action: api.WatchActionKindCreate,
		Object: &api.Object{},
	}, "1", v1.EventOptions{})
	assert.False(t, ok)
}

func TestWatchRequestTypesAndCursor(t *testing.T) {
	req, err := watchRequest(v1.EventOptions{Types: []string{"service", "task"}, Since: "17"})
	require.NoError(t, err)
	require.Len(t, req.Entries, 2)
	assert.Equal(t, "service", req.Entries[0].Kind)
	assert.Equal(t, api.WatchActionKindCreate|api.WatchActionKindUpdate|api.WatchActionKindRemove, req.Entries[0].Action)
	require.NotNil(t, req.ResumeFrom)
	assert.Equal(t, uint64(17), req.ResumeFrom.Index)

	_, err = watchRequest(v1.EventOptions{Types: []string{"volume"}})
	assert.Error(t, err)

	_, err = watchRequest(v1.EventOptions{Since: "not-a-number"})
	assert.Error(t, err)
}
