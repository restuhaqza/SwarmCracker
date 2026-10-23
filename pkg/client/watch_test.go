package client

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

func TestWatchCursorLagsBatch(t *testing.T) {
	assert.Equal(t, "9", cursorFor(&api.WatchMessage{Version: &api.Version{Index: 10}}))
	assert.Equal(t, "0", cursorFor(&api.WatchMessage{Version: &api.Version{Index: 0}}))
	assert.Empty(t, cursorFor(&api.WatchMessage{}))
}

type failingWatchReceiver struct{ err error }

func (f failingWatchReceiver) Recv() (*api.WatchMessage, error) { return nil, f.err }

func TestWatchFirstReceiveMapsCompaction(t *testing.T) {
	_, err := receiveWatch(failingWatchReceiver{err: status.Error(codes.OutOfRange, "compacted")})
	require.Error(t, err)
	mapped := watchError(err)
	var typed *output.Error
	require.ErrorAs(t, mapped, &typed)
	assert.Equal(t, output.CodeCursorExpired, typed.Code)
	assert.Equal(t, "relist", typed.Details["action"])
}

type fakeWatchClient struct {
	stream api.Watch_WatchClient
	err    error
}

func (f fakeWatchClient) Watch(context.Context, *api.WatchRequest, ...grpc.CallOption) (api.Watch_WatchClient, error) {
	return f.stream, f.err
}

type fakeWatchStream struct {
	first    error
	messages []*api.WatchMessage
}

func (f *fakeWatchStream) Recv() (*api.WatchMessage, error) {
	if f.first != nil {
		return nil, f.first
	}
	if len(f.messages) == 0 {
		return nil, io.EOF
	}
	message := f.messages[0]
	f.messages = f.messages[1:]
	return message, nil
}

func (f *fakeWatchStream) Header() (metadata.MD, error) { return nil, nil }
func (f *fakeWatchStream) Trailer() metadata.MD         { return nil }
func (f *fakeWatchStream) CloseSend() error             { return nil }
func (f *fakeWatchStream) Context() context.Context     { return context.Background() }
func (f *fakeWatchStream) SendMsg(any) error            { return nil }
func (f *fakeWatchStream) RecvMsg(any) error            { return nil }

// TestWatchEventsReturnsCompactionOnFirstReceive is the real-path 410: SwarmKit
// reports a compacted resume cursor from the first Recv, not from Watch.
func TestWatchEventsReturnsCompactionOnFirstReceive(t *testing.T) {
	c := &Client{watch: fakeWatchClient{stream: &fakeWatchStream{
		first: status.Error(codes.OutOfRange, "requested index is unavailable due to compaction"),
	}}}
	_, _, err := c.WatchEvents(context.Background(), v1.EventOptions{Since: "1"})
	require.Error(t, err)
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeCursorExpired, typed.Code)
	assert.Equal(t, "relist", typed.Details["action"])
}

func TestWatchEventsLagsTheBatchCursor(t *testing.T) {
	event := func(action api.WatchActionKind, revision uint64) *api.WatchMessage_Event {
		service := serviceFixture("svc-1", "web", revision)
		service.Meta = watchMeta(revision)
		return &api.WatchMessage_Event{Action: action, Object: &api.Object{Object: &api.Object_Service{Service: service}}}
	}
	stream := &fakeWatchStream{messages: []*api.WatchMessage{
		{}, // SwarmKit's empty start message.
		{Version: &api.Version{Index: 12}, Events: []*api.WatchMessage_Event{
			event(api.WatchActionKindCreate, 11),
			event(api.WatchActionKindUpdate, 12),
		}},
	}}
	c := &Client{watch: fakeWatchClient{stream: stream}}
	events, errs, err := c.WatchEvents(context.Background(), v1.EventOptions{})
	require.NoError(t, err)

	first := <-events
	second := <-events
	assert.Equal(t, "11", first.Cursor, "the cursor must be the previous batch boundary")
	assert.Equal(t, "11", second.Cursor, "every event in the batch shares the resumable cursor")
	assert.Equal(t, "service.created", first.Type)
	assert.Equal(t, "service.updated", second.Type)
	<-errs
}
