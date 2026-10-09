// Package ingress implements the manager-side routing mesh datapath: it reads
// the cluster's ingress services and their healthy replicas and programs L4
// load balancing on the local node.
//
// The overlay is a single shared L2 segment, so a rule installed on any node
// can forward to a replica running on any other node. For this first (MVP)
// phase the reconciler runs on manager nodes, which can see every service and
// task; per-node mesh fan-out is a later phase.
package ingress

import (
	"context"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/rs/zerolog/log"
)

// PortSpec is one ingress-published port.
type PortSpec struct {
	Protocol      string
	PublishedPort uint32
	TargetPort    uint32
}

// ServiceSpec is an ingress-relevant view of a service.
type ServiceSpec struct {
	ID    string
	Ports []PortSpec
}

// TaskSpec is an ingress-relevant view of a task.
type TaskSpec struct {
	ServiceID string
	// State is the task state; only "running" tasks are load-balanced.
	State string
	// IPs are the task's guest addresses (one per network attachment).
	IPs []string
}

// TaskStateRunning is the state a task must be in to receive traffic.
const TaskStateRunning = "running"

// Source supplies the cluster view the reconciler needs.
type Source interface {
	Services(ctx context.Context) ([]ServiceSpec, error)
	Tasks(ctx context.Context) ([]TaskSpec, error)
}

// LoadBalancer programs ingress rules on the local node.
type LoadBalancer interface {
	ProgramIngress(routes []network.IngressRoute) error
}

// Controller reconciles the local ingress datapath with the cluster state.
type Controller struct {
	source   Source
	lb       LoadBalancer
	interval time.Duration
}

// NewController creates a controller. interval is clamped to a sane minimum.
func NewController(source Source, lb LoadBalancer, interval time.Duration) *Controller {
	if interval < time.Second {
		interval = 2 * time.Second
	}
	return &Controller{source: source, lb: lb, interval: interval}
}

// Reconcile performs a single reconciliation pass.
func (c *Controller) Reconcile(ctx context.Context) error {
	services, err := c.source.Services(ctx)
	if err != nil {
		return err
	}
	tasks, err := c.source.Tasks(ctx)
	if err != nil {
		return err
	}

	backends := make(map[string][]string)
	for _, t := range tasks {
		if t.State != TaskStateRunning {
			continue
		}
		for _, ip := range t.IPs {
			if ip != "" {
				backends[t.ServiceID] = append(backends[t.ServiceID], ip)
			}
		}
	}

	routes := make([]network.IngressRoute, 0, len(services))
	for _, s := range services {
		if len(s.Ports) == 0 {
			continue
		}
		bs := backends[s.ID]
		if len(bs) == 0 {
			// No healthy replica: drop the service's rules so traffic fails
			// fast instead of black-holing to a dead backend.
			continue
		}
		ports := make([]network.IngressPort, 0, len(s.Ports))
		for _, p := range s.Ports {
			ports = append(ports, network.IngressPort{
				Protocol:      p.Protocol,
				PublishedPort: p.PublishedPort,
				TargetPort:    p.TargetPort,
			})
		}
		routes = append(routes, network.IngressRoute{
			ServiceID: s.ID,
			Ports:     ports,
			Backends:  bs,
		})
	}

	return c.lb.ProgramIngress(routes)
}

// Run reconciles on a timer until the context is cancelled.
func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	if err := c.Reconcile(ctx); err != nil {
		log.Warn().Err(err).Msg("Initial ingress reconcile failed")
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Reconcile(ctx); err != nil {
				log.Warn().Err(err).Msg("Ingress reconcile failed")
			}
		}
	}
}
