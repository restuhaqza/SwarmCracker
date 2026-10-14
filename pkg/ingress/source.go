package ingress

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
)

// ControlClientSource implements Source from a SwarmKit control client. It is
// used by manager nodes, which can list every service and task in the cluster.
type ControlClientSource struct {
	client  api.ControlClient
	timeout time.Duration
}

// NewControlClientSource wraps a control client as a Source.
func NewControlClientSource(client api.ControlClient) *ControlClientSource {
	return &ControlClientSource{client: client, timeout: 10 * time.Second}
}

// Services returns every service in the cluster, with its ingress-published
// ports when it has any. All services are returned, not just ingress-published
// ones, because service DNS records cover every service; BuildRoutes filters to
// the ones that actually publish ports.
func (s *ControlClientSource) Services(ctx context.Context) ([]ServiceSpec, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	resp, err := s.client.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		return nil, err
	}

	out := make([]ServiceSpec, 0, len(resp.Services))
	for _, svc := range resp.Services {
		spec := ServiceSpec{ID: svc.ID, Name: svc.Spec.Annotations.Name}
		if svc.Spec.Endpoint != nil {
			for _, p := range svc.Spec.Endpoint.Ports {
				if p.PublishMode != api.PublishModeIngress {
					continue
				}
				spec.Ports = append(spec.Ports, PortSpec{
					Protocol:      strings.ToLower(p.Protocol.String()),
					PublishedPort: p.PublishedPort,
					TargetPort:    p.TargetPort,
				})
			}
		}
		out = append(out, spec)
	}
	return out, nil
}

// Tasks returns the running tasks with their guest addresses.
func (s *ControlClientSource) Tasks(ctx context.Context) ([]TaskSpec, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	resp, err := s.client.ListTasks(ctx, &api.ListTasksRequest{})
	if err != nil {
		return nil, err
	}

	out := make([]TaskSpec, 0, len(resp.Tasks))
	for _, t := range resp.Tasks {
		if t.Status.State != api.TaskStateRunning {
			continue
		}
		var ips []string
		for _, na := range t.Networks {
			for _, a := range na.Addresses {
				ip := strings.SplitN(a, "/", 2)[0]
				if net.ParseIP(ip) != nil {
					ips = append(ips, ip)
				}
			}
		}
		out = append(out, TaskSpec{ID: t.ID, ServiceID: t.ServiceID, State: TaskStateRunning, IPs: ips})
	}
	return out, nil
}
