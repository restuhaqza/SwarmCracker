package ingress

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSource struct {
	services []ServiceSpec
	tasks    []TaskSpec
}

func (f *fakeSource) Services(context.Context) ([]ServiceSpec, error) { return f.services, nil }
func (f *fakeSource) Tasks(context.Context) ([]TaskSpec, error)       { return f.tasks, nil }

type fakeLB struct {
	mu      sync.Mutex
	last    []network.IngressRoute
	calls   int
	cleared int
}

func (f *fakeLB) ProgramIngress(routes []network.IngressRoute) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = routes
	f.calls++
	return nil
}

func (f *fakeLB) ClearIngress() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = nil
	f.cleared++
	return nil
}

func (f *fakeLB) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeLB) routes() []network.IngressRoute {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

func TestReconcile_UsesOnlyRunningReplicas(t *testing.T) {
	src := &fakeSource{
		services: []ServiceSpec{{
			ID:    "svc1",
			Ports: []PortSpec{{Protocol: "tcp", PublishedPort: 8080, TargetPort: 80}},
		}},
		tasks: []TaskSpec{
			{ServiceID: "svc1", State: TaskStateRunning, IPs: []string{"192.168.127.6"}},
			{ServiceID: "svc1", State: TaskStateRunning, IPs: []string{"192.168.127.5"}},
			{ServiceID: "svc1", State: "failed", IPs: []string{"192.168.127.9"}},
			{ServiceID: "other", State: TaskStateRunning, IPs: []string{"192.168.127.7"}},
		},
	}
	lb := &fakeLB{}

	require.NoError(t, NewController(src, lb, time.Second).Reconcile(context.Background()))

	routes := lb.routes()
	require.Len(t, routes, 1)
	assert.Equal(t, "svc1", routes[0].ServiceID)
	assert.ElementsMatch(t, []string{"192.168.127.5", "192.168.127.6"}, routes[0].Backends)
	require.Len(t, routes[0].Ports, 1)
	assert.Equal(t, uint32(8080), routes[0].Ports[0].PublishedPort)
	assert.Equal(t, uint32(80), routes[0].Ports[0].TargetPort)
	assert.Equal(t, "tcp", routes[0].Ports[0].Protocol)
}

func TestReconcile_DropsServiceWithNoHealthyReplica(t *testing.T) {
	src := &fakeSource{
		services: []ServiceSpec{{
			ID:    "svc1",
			Ports: []PortSpec{{Protocol: "tcp", PublishedPort: 8080, TargetPort: 80}},
		}},
		tasks: []TaskSpec{
			{ServiceID: "svc1", State: "failed", IPs: []string{"192.168.127.9"}},
		},
	}
	lb := &fakeLB{}

	require.NoError(t, NewController(src, lb, time.Second).Reconcile(context.Background()))
	assert.Empty(t, lb.routes(), "a service with no running replica must not be load-balanced")
}

func TestReconcile_SkipsServicesWithoutIngressPorts(t *testing.T) {
	src := &fakeSource{
		services: []ServiceSpec{{ID: "svc1"}}, // no ports
		tasks:    []TaskSpec{{ServiceID: "svc1", State: TaskStateRunning, IPs: []string{"192.168.127.5"}}},
	}
	lb := &fakeLB{}

	require.NoError(t, NewController(src, lb, time.Second).Reconcile(context.Background()))
	assert.Empty(t, lb.routes())
}
