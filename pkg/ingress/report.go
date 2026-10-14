package ingress

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/rs/zerolog/log"
)

// TaskAddress is one task's guest addresses, as reported by the node running it.
type TaskAddress struct {
	TaskID string   `json:"task_id"`
	IPs    []string `json:"ips"`
}

// TaskReport is a node's view of the tasks it runs.
//
// A manager needs it because the guest address of a task on the default bridge
// is allocated by the node's daemon and never written back into the SwarmKit
// task store: the control API can only report addresses for tasks attached to a
// network its own allocator manages (the ingress network, and user-defined
// networks). Without this channel, service DNS could not answer for a plain
// service at all.
type TaskReport struct {
	// NodeID is filled in by the manager from the client certificate; a node
	// does not send it, and the manager ignores whatever it sends.
	NodeID string        `json:"node_id,omitempty"`
	Tasks  []TaskAddress `json:"tasks"`
}

// TaskAddressSource supplies the addresses of the tasks a node runs.
type TaskAddressSource interface {
	LocalTaskAddresses() []TaskAddress
}

// SidecarAddressSource reports the addresses the daemon has recorded for the
// VMs on this node. The per-task sidecar is written when a VM starts and removed
// when it stops, so it is exactly the set of tasks this node runs — and for a
// task on the default bridge it is the only place its guest address exists.
type SidecarAddressSource struct {
	SocketDir string
}

// LocalTaskAddresses implements TaskAddressSource.
func (s SidecarAddressSource) LocalTaskAddresses() []TaskAddress {
	metas := scruntime.ListVMMetadata(s.SocketDir)
	out := make([]TaskAddress, 0, len(metas))
	for _, md := range metas {
		ips := normalizeAddresses(md.IPAddresses)
		if len(ips) == 0 {
			continue
		}
		out = append(out, TaskAddress{TaskID: md.ID, IPs: ips})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}

// normalizeAddresses strips CIDR suffixes, drops anything that is not an IP,
// deduplicates and sorts, so identical node state always reports identically.
func normalizeAddresses(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, a := range in {
		ip := strings.TrimSpace(a)
		if i := strings.Index(ip, "/"); i > 0 {
			ip = ip[:i]
		}
		if net.ParseIP(ip) == nil {
			continue
		}
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// ReportStore keeps the most recent task report per node and forgets one that
// stops being refreshed, so a crashed or partitioned node stops contributing
// addresses: a record must never point at a node nobody can reach.
type ReportStore struct {
	mu     sync.Mutex
	ttl    time.Duration
	now    func() time.Time
	byNode map[string]nodeReport
}

type nodeReport struct {
	at     time.Time
	byTask map[string][]string
}

// NewReportStore creates a store that expires a node's report after ttl.
func NewReportStore(ttl time.Duration) *ReportStore {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	return &ReportStore{ttl: ttl, now: time.Now, byNode: make(map[string]nodeReport)}
}

// Put records a node's latest report, replacing whatever it sent before, so a
// task that stopped on that node stops being reported.
func (s *ReportStore) Put(r TaskReport) {
	if s == nil {
		return
	}
	byTask := make(map[string][]string, len(r.Tasks))
	for _, t := range r.Tasks {
		if t.TaskID == "" {
			continue
		}
		if ips := normalizeAddresses(t.IPs); len(ips) > 0 {
			byTask[t.TaskID] = ips
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.byNode[r.NodeID] = nodeReport{at: s.now(), byTask: byTask}
}

// Addresses returns the guest addresses of every task a node has recently
// reported, keyed by task ID, and drops reports that have expired.
func (s *ReportStore) Addresses() map[string][]string {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string][]string)
	for node, rep := range s.byNode {
		if s.now().Sub(rep.at) > s.ttl {
			delete(s.byNode, node)
			continue
		}
		for task, ips := range rep.byTask {
			out[task] = append([]string(nil), ips...)
		}
	}
	return out
}

// Reporter publishes this node's task addresses to the manager on a timer, so
// the manager can resolve service names to replicas whose addresses only the
// node knows.
type Reporter struct {
	url      string
	client   *http.Client
	source   TaskAddressSource
	interval time.Duration
}

// NewReporter creates a reporter for the given report URL. interval is clamped
// to a sane minimum, and should be well under the manager's report TTL.
func NewReporter(url string, tlsConfig *tls.Config, source TaskAddressSource, interval time.Duration) *Reporter {
	if interval < time.Second {
		interval = 2 * time.Second
	}
	return &Reporter{
		url:      url,
		source:   source,
		interval: interval,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
	}
}

// Run publishes until the context is cancelled. A failed report is logged and
// retried on the next tick: the manager expires a stale report, so missing one
// is survivable and never leaves a record pointing at an unreachable address.
func (r *Reporter) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.reportOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reportOnce(ctx)
		}
	}
}

func (r *Reporter) reportOnce(ctx context.Context) {
	if r.source == nil {
		return
	}
	body, err := json.Marshal(TaskReport{Tasks: r.source.LocalTaskAddresses()})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to encode the task report")
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		log.Warn().Err(err).Msg("Failed to build the task report request")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		log.Debug().Err(err).Msg("Task report failed; the manager will expire this node")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		log.Debug().Int("status", resp.StatusCode).Msg("Task report rejected by the manager")
	}
}
