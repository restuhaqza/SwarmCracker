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
		ID:   id,
		Spec: api.ServiceSpec{Endpoint: &api.EndpointSpec{Ports: ports}},
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
		{ID: "no-endpoint"},
		ingressService("no-ports"),
	}}

	got, err := NewControlClientSource(client).Services(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1, "only ingress-published services are returned")
	assert.Equal(t, "ingress-svc", got[0].ID)
	require.Len(t, got[0].Ports, 1)
	assert.Equal(t, "tcp", got[0].Ports[0].Protocol, "protocol is lowercased for iptables")
	assert.Equal(t, uint32(8080), got[0].Ports[0].PublishedPort)
	assert.Equal(t, uint32(80), got[0].Ports[0].TargetPort)
}

func TestControlClientSource_Services_Error(t *testing.T) {
	_, err := NewControlClientSource(&fakeControlClient{err: errors.New("boom")}).Services(context.Background())
	assert.Error(t, err)
}

func TestControlClientSource_Tasks(t *testing.T) {
	client := &fakeControlClient{tasks: []*api.Task{
		{
			ServiceID: "svc1",
			Status:    api.TaskStatus{State: api.TaskStateRunning},
			Networks:  []*api.NetworkAttachment{{Addresses: []string{"192.168.127.5/24"}}},
		},
		{
			ServiceID: "svc1",
			Status:    api.TaskStatus{State: api.TaskStateFailed},
			Networks:  []*api.NetworkAttachment{{Addresses: []string{"192.168.127.9/24"}}},
		},
		{
			ServiceID: "svc2",
			Status:    api.TaskStatus{State: api.TaskStateRunning},
			Networks:  []*api.NetworkAttachment{{Addresses: []string{"192.168.127.6/24", "not-an-ip"}}},
		},
	}}

	got, err := NewControlClientSource(client).Tasks(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2, "only running tasks are returned")
	assert.Equal(t, "svc1", got[0].ServiceID)
	assert.Equal(t, []string{"192.168.127.5"}, got[0].IPs, "CIDR suffix is stripped")
	assert.Equal(t, []string{"192.168.127.6"}, got[1].IPs, "invalid addresses are dropped")
}

func TestControlClientSource_Tasks_Error(t *testing.T) {
	_, err := NewControlClientSource(&fakeControlClient{err: errors.New("boom")}).Tasks(context.Background())
	assert.Error(t, err)
}
