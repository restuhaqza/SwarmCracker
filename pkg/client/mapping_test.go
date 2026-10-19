package client

import (
	"context"
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type fakeControl struct {
	services       []*api.Service
	tasks          []*api.Task
	taskErr        error
	listTasksCalls int
	nodes          []*api.Node
	nodeErr        error
	listNodesCalls int
}

func (f *fakeControl) ListServices(context.Context, *api.ListServicesRequest, ...grpc.CallOption) (*api.ListServicesResponse, error) {
	return &api.ListServicesResponse{Services: f.services}, nil
}

func (f *fakeControl) ListTasks(context.Context, *api.ListTasksRequest, ...grpc.CallOption) (*api.ListTasksResponse, error) {
	f.listTasksCalls++
	if f.taskErr != nil {
		return nil, f.taskErr
	}
	return &api.ListTasksResponse{Tasks: f.tasks}, nil
}

func (f *fakeControl) ListNodes(context.Context, *api.ListNodesRequest, ...grpc.CallOption) (*api.ListNodesResponse, error) {
	f.listNodesCalls++
	if f.nodeErr != nil {
		return nil, f.nodeErr
	}
	return &api.ListNodesResponse{Nodes: f.nodes}, nil
}

func TestListServicesMapsServiceAndCounts(t *testing.T) {
	svc := testService()
	control := &fakeControl{
		services: []*api.Service{svc},
		tasks: []*api.Task{
			{ServiceID: "svc-1", Status: api.TaskStatus{State: api.TaskStateRunning}},
			{ServiceID: "svc-1", Status: api.TaskStatus{State: api.TaskStateStarting}},
			{ServiceID: "svc-other", Status: api.TaskStatus{State: api.TaskStateRunning}},
		},
	}

	items, err := (&Client{control: control}).ListServices(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)

	got := items[0]
	assert.Equal(t, "svc-1", got.ID)
	assert.Equal(t, "web", got.Name)
	assert.Equal(t, "17", got.Revision)
	assert.Equal(t, "payments", got.Labels["team"])
	assert.Equal(t, "replicated", got.Spec.Mode)
	require.NotNil(t, got.Spec.Replicas)
	assert.Equal(t, int64(3), *got.Spec.Replicas)
	assert.Equal(t, "nginx:alpine", got.Spec.Task.Image)
	assert.Equal(t, map[string]string{"APP_ENV": "prod"}, got.Spec.Task.Env)
	assert.Equal(t, int64(2), got.Spec.Task.VCPUs)
	assert.Equal(t, int64(512<<20), got.Spec.Task.MemoryBytes)
	require.Len(t, got.Spec.Task.Ports, 1)
	assert.Equal(t, uint32(80), got.Spec.Task.Ports[0].Target)
	assert.Equal(t, uint32(8080), got.Spec.Task.Ports[0].Published)
	assert.Equal(t, "tcp", got.Spec.Task.Ports[0].Protocol)
	require.Len(t, got.Spec.Task.Networks, 1)
	assert.Equal(t, "net-1", got.Spec.Task.Networks[0].ID)
	require.NotNil(t, got.Spec.Task.Healthcheck)
	assert.Equal(t, []string{"CMD", "true"}, got.Spec.Task.Healthcheck.Test)
	assert.Equal(t, int64(10000), got.Spec.Task.Healthcheck.IntervalMs)
	require.NotNil(t, got.Spec.Task.RestartPolicy)
	assert.Equal(t, "any", got.Spec.Task.RestartPolicy.Condition)
	require.NotNil(t, got.Spec.Update)
	assert.Equal(t, "pause", got.Spec.Update.FailureAction)
	require.NotNil(t, got.Spec.Placement)
	assert.Equal(t, []string{"node.labels.zone==a"}, got.Spec.Placement.Constraints)

	assert.Equal(t, 3, got.Status.DesiredTasks)
	assert.Equal(t, 1, got.Status.RunningTasks)
	assert.Equal(t, 1, got.Status.TasksInTransition)
	assert.False(t, got.Status.Converged)
	require.NotNil(t, got.Status.Endpoint)
	require.Len(t, got.Status.Endpoint.Ports, 1)
	assert.Equal(t, uint32(8080), got.Status.Endpoint.Ports[0].Published)
}

func TestListServicesEmptySkipsTaskCall(t *testing.T) {
	control := &fakeControl{}
	items, err := (&Client{control: control}).ListServices(context.Background())
	require.NoError(t, err)
	assert.Empty(t, items)
	assert.NotNil(t, items, "empty list must not be nil")
	assert.Zero(t, control.listTasksCalls)
}

func TestListServicesTaskFailureIsReturned(t *testing.T) {
	control := &fakeControl{
		services: []*api.Service{testService()},
		taskErr:  assert.AnError,
	}
	_, err := (&Client{control: control}).ListServices(context.Background())
	require.ErrorIs(t, err, assert.AnError)
}

func TestCountTasks(t *testing.T) {
	counts := countTasks([]*api.Task{
		{ServiceID: "a", DesiredState: api.TaskStateRunning, Status: api.TaskStatus{State: api.TaskStateRunning}},
		{ServiceID: "a", Status: api.TaskStatus{State: api.TaskStateNew}},
		{ServiceID: "a", Status: api.TaskStatus{State: api.TaskStateFailed}},
		{ServiceID: "b", Status: api.TaskStatus{State: api.TaskStateShutdown}},
	})
	assert.Equal(t, taskCounts{total: 3, desired: 1, running: 1, transition: 1}, counts["a"])
	assert.Equal(t, taskCounts{total: 1}, counts["b"])
}

func TestServiceFromSwarmkitGlobalMode(t *testing.T) {
	svc := testService()
	svc.Spec.Mode = &api.ServiceSpec_Global{Global: &api.GlobalService{}}
	got := serviceFromSwarmkit(svc, taskCounts{total: 4, desired: 4, running: 4})
	assert.Equal(t, "global", got.Spec.Mode)
	assert.Nil(t, got.Spec.Replicas)
	assert.Equal(t, 4, got.Status.DesiredTasks)
	assert.True(t, got.Status.Converged)
}

func testService() *api.Service {
	return &api.Service{
		ID: "svc-1",
		Meta: api.Meta{
			Version:   api.Version{Index: 17},
			CreatedAt: &gogotypes.Timestamp{Seconds: 1700000000},
			UpdatedAt: &gogotypes.Timestamp{Seconds: 1700000100},
		},
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{
				Name:   "web",
				Labels: map[string]string{"team": "payments"},
			},
			Mode: &api.ServiceSpec_Replicated{
				Replicated: &api.ReplicatedService{Replicas: 3},
			},
			Task: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image:   "nginx:alpine",
						Command: []string{"nginx"},
						Args:    []string{"-g", "daemon off;"},
						Env:     []string{"APP_ENV=prod", "MALFORMED"},
						Healthcheck: &api.HealthConfig{
							Test:     []string{"CMD", "true"},
							Interval: gogotypes.DurationProto(10 * time.Second),
							Timeout:  gogotypes.DurationProto(3 * time.Second),
							Retries:  3,
						},
					},
				},
				Resources: &api.ResourceRequirements{
					Limits: &api.Resources{NanoCPUs: 2_000_000_000, MemoryBytes: 512 << 20},
				},
				Restart: &api.RestartPolicy{
					Condition: api.RestartOnAny,
					Delay:     gogotypes.DurationProto(5 * time.Second),
				},
				Placement: &api.Placement{Constraints: []string{"node.labels.zone==a"}},
				Networks:  []*api.NetworkAttachmentConfig{{Target: "net-1", Aliases: []string{"web"}}},
			},
			Endpoint: &api.EndpointSpec{Ports: []*api.PortConfig{{
				TargetPort:    80,
				PublishedPort: 8080,
				Protocol:      api.ProtocolTCP,
				PublishMode:   api.PublishModeIngress,
			}}},
			Update: &api.UpdateConfig{
				Parallelism:   1,
				Delay:         10 * time.Second,
				FailureAction: api.UpdateConfig_PAUSE,
				Order:         api.UpdateConfig_STOP_FIRST,
				Monitor:       gogotypes.DurationProto(30 * time.Second),
			},
		},
		Endpoint: &api.Endpoint{Ports: []*api.PortConfig{{
			TargetPort:    80,
			PublishedPort: 8080,
			Protocol:      api.ProtocolTCP,
			PublishMode:   api.PublishModeIngress,
		}}},
	}
}

func TestServiceDocumentUsesV1Kind(t *testing.T) {
	doc := v1.NewServiceDocument(v1.Service{ID: "svc-1", Name: "web"})
	assert.Equal(t, v1.KindService, doc.Kind)
	assert.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
}
