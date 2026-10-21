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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeControl struct {
	services       []*api.Service
	service        *api.Service
	serviceErr     error
	getServiceCall int
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

func (f *fakeControl) GetService(_ context.Context, req *api.GetServiceRequest, _ ...grpc.CallOption) (*api.GetServiceResponse, error) {
	f.getServiceCall++
	if f.serviceErr != nil {
		return nil, f.serviceErr
	}
	if f.service != nil {
		return &api.GetServiceResponse{Service: f.service}, nil
	}
	for _, svc := range f.services {
		if svc.ID == req.ServiceID {
			return &api.GetServiceResponse{Service: svc}, nil
		}
	}
	return &api.GetServiceResponse{}, nil
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

func TestGetServiceMapsStatus(t *testing.T) {
	control := &fakeControl{
		service: testService(),
		tasks: []*api.Task{
			{ServiceID: "svc-1", Status: api.TaskStatus{State: api.TaskStateRunning}},
			{ServiceID: "svc-1", Status: api.TaskStatus{State: api.TaskStateStarting}},
		},
	}

	got, err := (&Client{control: control}).GetService(context.Background(), "svc-1")
	require.NoError(t, err)
	assert.Equal(t, "web", got.Name)
	assert.Equal(t, "replicated", got.Spec.Mode)
	assert.Equal(t, 3, got.Status.DesiredTasks)
	assert.Equal(t, 1, got.Status.RunningTasks)
	assert.Equal(t, 1, got.Status.TasksInTransition)
	assert.False(t, got.Status.Converged)
	assert.Equal(t, 1, control.getServiceCall)
}

func TestGetServiceNoServiceIsNotFound(t *testing.T) {
	_, err := (&Client{control: &fakeControl{}}).GetService(context.Background(), "missing")
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetServiceTaskFailureIsReturned(t *testing.T) {
	control := &fakeControl{service: testService(), taskErr: assert.AnError}
	_, err := (&Client{control: control}).GetService(context.Background(), "svc-1")
	require.ErrorIs(t, err, assert.AnError)
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

func TestModeStringVariants(t *testing.T) {
	cases := []struct {
		name string
		spec api.ServiceSpec
		want string
	}{
		{"replicated", api.ServiceSpec{Mode: &api.ServiceSpec_Replicated{Replicated: &api.ReplicatedService{}}}, "replicated"},
		{"global", api.ServiceSpec{Mode: &api.ServiceSpec_Global{Global: &api.GlobalService{}}}, "global"},
		{"replicated-job", api.ServiceSpec{Mode: &api.ServiceSpec_ReplicatedJob{ReplicatedJob: &api.ReplicatedJob{}}}, "replicated-job"},
		{"global-job", api.ServiceSpec{Mode: &api.ServiceSpec_GlobalJob{GlobalJob: &api.GlobalJob{}}}, "global-job"},
		{"unknown", api.ServiceSpec{}, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, modeString(tc.spec))
		})
	}
}

func TestMountsAndMountTypeStrings(t *testing.T) {
	assert.Nil(t, mounts(nil))

	got := mounts([]api.Mount{
		{Type: api.MountTypeBind, Source: "/host", Target: "/container", ReadOnly: true},
		{Type: api.MountTypeVolume, Source: "data", Target: "/data"},
		{Type: api.MountTypeTmpfs, Target: "/tmp"},
		{Type: api.MountTypeNamedPipe, Target: `\\.\pipe\test`},
		{Type: api.MountTypeCluster, Source: "cluster-vol", Target: "/cluster"},
	})
	require.Len(t, got, 5)
	assert.Equal(t, v1.Mount{Type: "bind", Source: "/host", Target: "/container", ReadOnly: true}, got[0])
	assert.Equal(t, "volume", got[1].Type)
	assert.Equal(t, "tmpfs", got[2].Type)
	assert.Equal(t, "npipe", got[3].Type)
	assert.Equal(t, "cluster", got[4].Type)

	assert.Equal(t, "bind", mountTypeString(api.MountTypeBind))
	assert.Equal(t, "volume", mountTypeString(api.MountTypeVolume))
	assert.Equal(t, "tmpfs", mountTypeString(api.MountTypeTmpfs))
	assert.Equal(t, "npipe", mountTypeString(api.MountTypeNamedPipe))
	assert.Equal(t, "cluster", mountTypeString(api.MountTypeCluster))
	assert.Equal(t, "", mountTypeString(api.Mount_MountType(99)))
}

func TestPortsEndpointAndNetworksEdgeCases(t *testing.T) {
	assert.Nil(t, ports(nil))
	assert.Nil(t, ports(&api.EndpointSpec{}))
	assert.Nil(t, endpoint(nil))
	assert.Nil(t, endpoint(&api.Endpoint{}))
	assert.Nil(t, endpoint(&api.Endpoint{Ports: []*api.PortConfig{nil}}))
	assert.Nil(t, networks(nil))

	portList := ports(&api.EndpointSpec{Ports: []*api.PortConfig{
		nil,
		{TargetPort: 53, PublishedPort: 5353, Protocol: api.ProtocolUDP, PublishMode: api.PublishModeHost},
		{TargetPort: 99, Protocol: api.ProtocolSCTP},
	}})
	require.Len(t, portList, 2)
	assert.Equal(t, "udp", portList[0].Protocol)
	assert.Equal(t, "host", portList[0].Mode)
	assert.Equal(t, "sctp", portList[1].Protocol)

	ep := endpoint(&api.Endpoint{Ports: []*api.PortConfig{nil, {TargetPort: 80, PublishedPort: 8080}}})
	require.NotNil(t, ep)
	require.Len(t, ep.Ports, 1)
	assert.Equal(t, uint32(80), ep.Ports[0].Target)

	nets := networks([]*api.NetworkAttachmentConfig{
		nil,
		{Target: "net-1", Aliases: []string{"web"}},
	})
	require.Len(t, nets, 1)
	assert.Equal(t, "net-1", nets[0].ID)
	assert.Equal(t, []string{"web"}, nets[0].Aliases)
}

func TestHealthcheckEnvAndCopyMapEdgeCases(t *testing.T) {
	assert.Nil(t, healthcheck(nil))
	assert.Nil(t, healthcheck(&api.HealthConfig{}))
	assert.Nil(t, envMap(nil))
	assert.Nil(t, envMap([]string{"MALFORMED", "=value"}))
	assert.Equal(t, map[string]string{"A": "1"}, envMap([]string{"A=1"}))
	assert.Nil(t, copyMap(nil))
	assert.Equal(t, map[string]string{"k": "v"}, copyMap(map[string]string{"k": "v"}))
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
