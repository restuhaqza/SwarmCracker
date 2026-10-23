package legacy

import (
	"context"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type fakeControl struct {
	api.ControlClient
	services []*api.Service
	nodes    []*api.Node
	tasks    []*api.Task
	err      error
}

func (f *fakeControl) ListServices(context.Context, *api.ListServicesRequest, ...grpc.CallOption) (*api.ListServicesResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &api.ListServicesResponse{Services: f.services}, nil
}

func (f *fakeControl) ListNodes(context.Context, *api.ListNodesRequest, ...grpc.CallOption) (*api.ListNodesResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &api.ListNodesResponse{Nodes: f.nodes}, nil
}

func (f *fakeControl) ListTasks(context.Context, *api.ListTasksRequest, ...grpc.CallOption) (*api.ListTasksResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &api.ListTasksResponse{Tasks: f.tasks}, nil
}

func TestResolveServiceRef(t *testing.T) {
	client := &fakeControl{services: []*api.Service{
		{ID: "svc-abc123", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "web"}}},
	}}

	id, err := ResolveServiceRef(context.Background(), client, "web")
	require.NoError(t, err)
	assert.Equal(t, "svc-abc123", id)

	id, err = ResolveServiceRef(context.Background(), client, "svc-abc")
	require.NoError(t, err)
	assert.Equal(t, "svc-abc123", id)

	_, err = ResolveServiceRef(context.Background(), client, "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "service not found")
}

func TestResolveServiceRefListFailure(t *testing.T) {
	_, err := ResolveServiceRef(context.Background(), &fakeControl{err: assert.AnError}, "web")
	require.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, err.Error(), "failed to list services")
}

func TestResolveNodeRef(t *testing.T) {
	client := &fakeControl{nodes: []*api.Node{{
		ID:          "node-abc123",
		Description: &api.NodeDescription{Hostname: "manager-1"},
	}}}

	id, err := ResolveNodeRef(context.Background(), client, "manager-1")
	require.NoError(t, err)
	assert.Equal(t, "node-abc123", id)

	_, err = ResolveNodeRef(context.Background(), &fakeControl{err: assert.AnError}, "manager-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list nodes")
}

func TestResolveTaskRef(t *testing.T) {
	client := &fakeControl{tasks: []*api.Task{{ID: "task-abc123"}}}

	id, err := ResolveTaskRef(context.Background(), client, "task-abc")
	require.NoError(t, err)
	assert.Equal(t, "task-abc123", id)

	_, err = ResolveTaskRef(context.Background(), &fakeControl{err: assert.AnError}, "task-abc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list tasks")
}
