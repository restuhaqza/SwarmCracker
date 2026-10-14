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
	// ID is the task ID. Guest addresses are looked up by task ID, because the
	// node running a task is the only place its default-bridge address exists.
	ID string
	// NodeID is the node the task is scheduled on. A reported address is only
	// trusted when it comes from this node.
	NodeID    string
	ServiceID string
	// State is the task state; only "running" tasks are load-balanced.
	State string
	// IPs are the task's guest addresses as the control API knows them. They are
	// only populated for tasks attached to a network the manager's allocator
	// manages (the ingress network, and user-defined networks).
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

// AddressSource supplies the guest addresses of running tasks, keyed by the
// node that reported them and then by task ID. The control API only knows the
// addresses of tasks attached to a network its own allocator manages, so this is
// what lets service DNS cover plain services too.
//
// The reporting node is part of the key on purpose: a task's address is only
// taken from the node the control API says is running it, so one node cannot
// inject addresses for another node's tasks.
type AddressSource interface {
	Addresses() map[string]map[string][]string
}

// Controller reconciles the local ingress datapath with the cluster state and
// exposes the resulting routing table.
type Controller struct {
	source    Source
	lb        LoadBalancer
	dns       DNSProgrammer
	addresses AddressSource
	interval  time.Duration

	mu      sync.RWMutex
	routes  []network.IngressRoute
	records []network.DNSRecord
}

// NewController creates a controller. interval is clamped to a sane minimum.
// dns and addresses may be nil, in which case records are computed but not
// programmed, and only control-API addresses are used.
func NewController(source Source, lb LoadBalancer, dns DNSProgrammer, addresses AddressSource, interval time.Duration) *Controller {
	if interval < time.Second {
		interval = 2 * time.Second
	}
	return &Controller{source: source, lb: lb, dns: dns, addresses: addresses, interval: interval}
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

	var reported map[string]map[string][]string
	if c.addresses != nil {
		reported = c.addresses.Addresses()
	}

	routes := BuildRoutes(services, tasks)
	records := BuildDNSRecords(services, tasks, reported)

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
// Addresses come from the node running each task when it has reported them
// (reported is keyed by node ID and then task ID), falling back to what the
// control API knows. The fallback only covers tasks attached to a network the
// manager's allocator manages, which is why the report channel exists.
//
// A service with no running replica produces no record at all, so scaling down
// or losing a service stops it resolving instead of pointing at dead addresses.
// IPs are sorted so identical cluster state always yields an identical table
// (and therefore an unchanged revision).
func BuildDNSRecords(services []ServiceSpec, tasks []TaskSpec, reported map[string]map[string][]string) []network.DNSRecord {
	backends := make(map[string][]string)
	for _, t := range tasks {
		if t.State != TaskStateRunning {
			continue
		}
		ips := t.IPs
		// Only the node the control API says is running the task may supply its
		// address, so a node cannot inject addresses for another node's tasks.
		if byTask, ok := reported[t.NodeID]; ok {
			if fromNode, ok := byTask[t.ID]; ok && len(fromNode) > 0 {
				ips = fromNode
			}
		}
		for _, ip := range ips {
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
