package ingress

import (
	"context"
	"errors"
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

// fakeDNS records the service-name records the controller programs, so a test
// can prove they reach the resolver.
type fakeDNS struct {
	mu      sync.Mutex
	last    []network.DNSRecord
	calls   int
	program func([]network.DNSRecord) error
}

func (f *fakeDNS) ProgramDNS(records []network.DNSRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = records
	f.calls++
	if f.program != nil {
		return f.program(records)
	}
	return nil
}

func (f *fakeDNS) records() []network.DNSRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

func (f *fakeDNS) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
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

	require.NoError(t, NewController(src, lb, nil, time.Second).Reconcile(context.Background()))

	routes := lb.routes()
	require.Len(t, routes, 1)
	assert.Equal(t, "svc1", routes[0].ServiceID)
	assert.ElementsMatch(t, []string{"192.168.127.5", "192.168.127.6"}, routes[0].Backends)
	require.Len(t, routes[0].Ports, 1)
	assert.Equal(t, uint32(8080), routes[0].Ports[0].PublishedPort)
	assert.Equal(t, uint32(80), routes[0].Ports[0].TargetPort)
	assert.Equal(t, "tcp", routes[0].Ports[0].Protocol)
}

// TestReconcile_ProgramsDNSRecords proves one reconcile pass programs both the
// load balancer and the resolver: `<name>` and `tasks.<name>` answer with the
// running replicas' IPs, a service with no running replica produces no record at
// all, and the addresses are sorted so identical cluster state yields an
// identical table.
func TestReconcile_ProgramsDNSRecords(t *testing.T) {
	src := &fakeSource{
		services: []ServiceSpec{
			{ID: "svc1", Name: "web"},
			{ID: "svc2", Name: "api"},
			{ID: "svc3", Name: "gone"}, // scaled to zero
		},
		tasks: []TaskSpec{
			{ServiceID: "svc1", State: TaskStateRunning, IPs: []string{"192.168.127.6"}},
			{ServiceID: "svc1", State: TaskStateRunning, IPs: []string{"192.168.127.5"}},
			{ServiceID: "svc1", State: "failed", IPs: []string{"192.168.127.9"}},
			{ServiceID: "svc2", State: TaskStateRunning, IPs: []string{"10.10.0.9"}},
		},
	}
	dns := &fakeDNS{}

	require.NoError(t, NewController(src, &fakeLB{}, dns, time.Second).Reconcile(context.Background()))

	got := map[string][]string{}
	for _, r := range dns.records() {
		got[r.Name] = r.IPs
	}
	assert.Equal(t, []string{"192.168.127.5", "192.168.127.6"}, got["web"], "replicas are sorted")
	assert.Equal(t, []string{"192.168.127.5", "192.168.127.6"}, got["tasks.web"])
	assert.Equal(t, []string{"10.10.0.9"}, got["api"])
	assert.Equal(t, []string{"10.10.0.9"}, got["tasks.api"])
	assert.NotContains(t, got, "gone", "a service with no running replica must not resolve")
	assert.NotContains(t, got, "tasks.gone")
	assert.Equal(t, 1, dns.calls)
}

// TestReconcile_DNSFailureIsReported asserts a resolver failure surfaces as a
// reconcile error instead of being swallowed.
func TestReconcile_DNSFailureIsReported(t *testing.T) {
	src := &fakeSource{
		services: []ServiceSpec{{ID: "svc1", Name: "web"}},
		tasks:    []TaskSpec{{ServiceID: "svc1", State: TaskStateRunning, IPs: []string{"10.0.0.1"}}},
	}
	dns := &fakeDNS{program: func([]network.DNSRecord) error { return errors.New("boom") }}

	err := NewController(src, &fakeLB{}, dns, time.Second).Reconcile(context.Background())
	assert.Error(t, err)
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

	require.NoError(t, NewController(src, lb, nil, time.Second).Reconcile(context.Background()))
	assert.Empty(t, lb.routes(), "a service with no running replica must not be load-balanced")
}

func TestReconcile_SkipsServicesWithoutIngressPorts(t *testing.T) {
	src := &fakeSource{
		services: []ServiceSpec{{ID: "svc1"}}, // no ports
		tasks:    []TaskSpec{{ServiceID: "svc1", State: TaskStateRunning, IPs: []string{"192.168.127.5"}}},
	}
	lb := &fakeLB{}

	require.NoError(t, NewController(src, lb, nil, time.Second).Reconcile(context.Background()))
	assert.Empty(t, lb.routes())
}
