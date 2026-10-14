// Package ingress implements the ingress routing mesh: it reads the cluster's
// ingress services and their healthy replicas and programs L4 load balancing.
//
// The overlay is a single shared L2 segment, so a rule installed on any node
// can forward to a replica running on any other node. A manager computes the
// authoritative routing table from the control API and serves it; every node
// (including workers) then programs its own load balancing from that table, so
// the published port is reachable on any node.
package ingress

import (
	"context"
	"sort"
	"strings"
	"sync"
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
	ID string
	// Name is the service's name. It is used for service DNS records; the load
	// balancer keys off ID.
	Name string
	// Ports are the service's ingress-published ports, if any.
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

// LoadBalancer programs and clears ingress rules on the local node.
type LoadBalancer interface {
	ProgramIngress(routes []network.IngressRoute) error
	ClearIngress() error
}

// DNSProgrammer programs the node-local service-name records derived from the
// cluster table. It is a separate dependency from LoadBalancer: a node serves
// records whether or not it load-balances anything, and the two are programmed
// from the same reconcile pass.
type DNSProgrammer interface {
	ProgramDNS(records []network.DNSRecord) error
}

// Controller reconciles the local ingress datapath with the cluster state and
// exposes the resulting routing table.
type Controller struct {
	source   Source
	lb       LoadBalancer
	dns      DNSProgrammer
	interval time.Duration

	mu      sync.RWMutex
	routes  []network.IngressRoute
	records []network.DNSRecord
}

// NewController creates a controller. interval is clamped to a sane minimum.
// dns may be nil, in which case service DNS records are computed but not
// programmed.
func NewController(source Source, lb LoadBalancer, dns DNSProgrammer, interval time.Duration) *Controller {
	if interval < time.Second {
		interval = 2 * time.Second
	}
	return &Controller{source: source, lb: lb, dns: dns, interval: interval}
}

// Reconcile performs a single reconciliation pass: it recomputes the routing
// table and the service-name records, publishes both, and programs the local
// load balancer and resolver.
func (c *Controller) Reconcile(ctx context.Context) error {
	services, err := c.source.Services(ctx)
	if err != nil {
		return err
	}
	tasks, err := c.source.Tasks(ctx)
	if err != nil {
		return err
	}

	routes := BuildRoutes(services, tasks)
	records := BuildDNSRecords(services, tasks)

	c.mu.Lock()
	c.routes = routes
	c.records = records
	c.mu.Unlock()

	if err := c.lb.ProgramIngress(routes); err != nil {
		return err
	}
	if c.dns != nil {
		if err := c.dns.ProgramDNS(records); err != nil {
			return err
		}
	}
	return nil
}

// Routes returns a snapshot of the last computed routing table.
func (c *Controller) Routes() []network.IngressRoute {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]network.IngressRoute(nil), c.routes...)
}

// Records returns a snapshot of the last computed service DNS records.
func (c *Controller) Records() []network.DNSRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]network.DNSRecord(nil), c.records...)
}

// Table returns the last computed routing table in its wire form.
func (c *Controller) Table() Table {
	return TableFrom(c.Routes(), c.Records())
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

// BuildRoutes turns the ingress-relevant service and task views into the
// load-balanced routes to program. Services with no running replica are
// omitted so their rules are dropped rather than black-holing to a dead
// backend.
func BuildRoutes(services []ServiceSpec, tasks []TaskSpec) []network.IngressRoute {
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
	return routes
}

// BuildDNSRecords turns the cluster view into the service-name records every
// node serves: `<name>` and `tasks.<name>` answer with the IPs of the service's
// running replicas. A name with several replicas becomes several addresses, so
// a client picks one — the same shape as Docker's `dnsrr` endpoint mode.
//
// A service with no running replica produces no record at all, so scaling down
// or losing a service stops it resolving instead of pointing at dead addresses.
// IPs are sorted so identical cluster state always yields an identical table
// (and therefore an unchanged revision).
func BuildDNSRecords(services []ServiceSpec, tasks []TaskSpec) []network.DNSRecord {
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

	records := make([]network.DNSRecord, 0, 2*len(services))
	for _, s := range services {
		name := strings.TrimSpace(s.Name)
		ips := backends[s.ID]
		if name == "" || len(ips) == 0 {
			continue
		}
		sorted := append([]string(nil), ips...)
		sort.Strings(sorted)
		records = append(records,
			network.DNSRecord{Name: name, IPs: sorted},
			network.DNSRecord{Name: "tasks." + name, IPs: sorted},
		)
	}
	return records
}
