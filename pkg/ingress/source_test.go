package ingress

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// fakeControlClient embeds the (large) generated ControlClient interface and
// overrides only the two list calls the source uses.
type fakeControlClient struct {
	api.ControlClient
	services []*api.Service
	tasks    []*api.Task
	err      error
}

func (f *fakeControlClient) ListServices(context.Context, *api.ListServicesRequest, ...grpc.CallOption) (*api.ListServicesResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &api.ListServicesResponse{Services: f.services}, nil
}

func (f *fakeControlClient) ListTasks(context.Context, *api.ListTasksRequest, ...grpc.CallOption) (*api.ListTasksResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &api.ListTasksResponse{Tasks: f.tasks}, nil
}

func ingressService(id string, ports ...*api.PortConfig) *api.Service {
	return &api.Service{
		ID: id,
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{Name: id},
			Endpoint:    &api.EndpointSpec{Ports: ports},
		},
	}
}

func TestControlClientSource_Services(t *testing.T) {
	client := &fakeControlClient{services: []*api.Service{
		ingressService("ingress-svc", &api.PortConfig{
			Protocol: api.ProtocolTCP, TargetPort: 80, PublishedPort: 8080, PublishMode: api.PublishModeIngress,
		}),
		ingressService("host-svc", &api.PortConfig{
			Protocol: api.ProtocolUDP, TargetPort: 53, PublishedPort: 5353, PublishMode: api.PublishModeHost,
		}),
		{ID: "no-endpoint", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "no-endpoint"}}},
		ingressService("no-ports"),
	}}

	got, err := NewControlClientSource(client).Services(context.Background())
	require.NoError(t, err)
	// Every service is returned: service DNS covers services that publish no
	// ports at all, and BuildRoutes filters the load-balanced ones itself.
	require.Len(t, got, 4)

	byID := make(map[string]ServiceSpec, len(got))
	for _, s := range got {
		byID[s.ID] = s
	}

	assert.Equal(t, "ingress-svc", byID["ingress-svc"].Name, "the service name travels with the spec")
	require.Len(t, byID["ingress-svc"].Ports, 1)
	assert.Equal(t, "tcp", byID["ingress-svc"].Ports[0].Protocol, "protocol is lowercased for iptables")
	assert.Equal(t, uint32(8080), byID["ingress-svc"].Ports[0].PublishedPort)
	assert.Equal(t, uint32(80), byID["ingress-svc"].Ports[0].TargetPort)

	assert.Empty(t, byID["host-svc"].Ports, "host-published ports are not load-balanced")
	assert.Empty(t, byID["no-endpoint"].Ports)
	assert.Empty(t, byID["no-ports"].Ports)
}

func TestControlClientSource_Services_Error(t *testing.T) {
	_, err := NewControlClientSource(&fakeControlClient{err: errors.New("boom")}).Services(context.Background())
	assert.Error(t, err)
}

func TestControlClientSource_Tasks(t *testing.T) {
	client := &fakeControlClient{tasks: []*api.Task{
		{
			ID:        "t1",
			NodeID:    "node-1",
			ServiceID: "svc1",
			Status:    api.TaskStatus{State: api.TaskStateRunning},
			Networks:  []*api.NetworkAttachment{{Addresses: []string{"192.168.127.5/24"}}},
		},
		{
			ID:        "t2",
			NodeID:    "node-1",
			ServiceID: "svc1",
			Status:    api.TaskStatus{State: api.TaskStateFailed},
			Networks:  []*api.NetworkAttachment{{Addresses: []string{"192.168.127.9/24"}}},
		},
		{
			ID:        "t3",
			NodeID:    "node-2",
			ServiceID: "svc2",
			Status:    api.TaskStatus{State: api.TaskStateRunning},
			Networks:  []*api.NetworkAttachment{{Addresses: []string{"192.168.127.6/24", "not-an-ip"}}},
		},
	}}

	got, err := NewControlClientSource(client).Tasks(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2, "only running tasks are returned")
	assert.Equal(t, "t1", got[0].ID, "the task ID is the key the address report joins on")
	assert.Equal(t, "node-1", got[0].NodeID, "the node is what makes a reported address trustworthy")
	assert.Equal(t, "svc1", got[0].ServiceID)
	assert.Equal(t, []string{"192.168.127.5"}, got[0].IPs, "CIDR suffix is stripped")
	assert.Equal(t, "t3", got[1].ID)
	assert.Equal(t, "node-2", got[1].NodeID)
	assert.Equal(t, []string{"192.168.127.6"}, got[1].IPs, "invalid addresses are dropped")
}

func TestControlClientSource_Tasks_Error(t *testing.T) {
	_, err := NewControlClientSource(&fakeControlClient{err: errors.New("boom")}).Tasks(context.Background())
	assert.Error(t, err)
}
