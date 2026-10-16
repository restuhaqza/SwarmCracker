package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/redact"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

const (
	// defaultHealthPort is the port assumed for each daemon's health server when
	// fanning out live health. It matches the daemon's `--health-addr` default
	// (127.0.0.1:8080); override with `--health-port` when the daemons bind
	// another port.
	defaultHealthPort = 8080
	// healthRequestTimeout bounds each per-node health request, so one slow or
	// unreachable node cannot stall `service ps`/`task ls`.
	healthRequestTimeout = 3 * time.Second
	// healthFanoutTimeout bounds the whole fan-out: a cluster whose node
	// addresses silently drop traffic must not eat the command's own 30s budget.
	healthFanoutTimeout = 10 * time.Second
	// healthMaxConcurrency caps how many node requests are in flight at once.
	healthMaxConcurrency = 8
	// healthLoopbackAddr is tried for a node whose own address did not answer.
	// The CLI usually runs on a node, and a daemon keeping the default
	// 127.0.0.1 bind is only reachable there.
	healthLoopbackAddr = "127.0.0.1"
	// maxHealthResponseBytes caps how much of a node's response is decoded.
	maxHealthResponseBytes = 1 << 20
)

// clusterHealthOptions controls the cluster-wide live-health fan-out performed
// by `service ps` and `task ls`.
type clusterHealthOptions struct {
	// Disabled skips the fan-out entirely (`--no-health`): the HEALTH column
	// falls back to the state-derived value and no node is contacted.
	Disabled bool
	// Port is the port of each node's health server (`--health-port`).
	Port int
	// HTTPClient is used for the fan-out; nil means a client bounded by
	// healthRequestTimeout. Tests inject a client pointed at a fake server.
	HTTPClient *http.Client
}

// validate rejects an unusable --health-port before any RPC is made. It is
// checked even with --no-health so an invalid value is never silently ignored.
func (o clusterHealthOptions) validate() error {
	if o.Port < 1 || o.Port > 65535 {
		return fmt.Errorf("--health-port must be between 1 and 65535 (got %d)", o.Port)
	}
	return nil
}

// liveHealth is the result of the fan-out: which nodes answered and what live
// health they reported, keyed by task ID. When enabled is false the column is
// purely state-derived (the pre-#67 behaviour, and what --no-health asks for).
type liveHealth struct {
	enabled   bool
	byTask    map[string]runtime.VMHealth
	reachable map[string]bool
}

// fetchLiveHealth queries the health endpoint of every node that hosts a
// healthchecked task in tasks, and returns the live health it collected. It
// never fails the command: a node that is unreachable, unknown, or running an
// older daemon simply stays absent from reachable (and its tasks fall back to
// the derived value).
func fetchLiveHealth(ctx context.Context, client api.ControlClient, tasks []*api.Task, opts clusterHealthOptions) liveHealth {
	lh := liveHealth{
		enabled:   !opts.Disabled,
		byTask:    map[string]runtime.VMHealth{},
		reachable: map[string]bool{},
	}
	if opts.Disabled {
		return lh
	}

	// Only healthchecked tasks can have live health, and only tasks that are
	// already assigned to a node can be looked up. Keep each node's task IDs so a
	// loopback answer can only be attributed to the node that owns them.
	nodeTasks := map[string][]string{}
	for _, task := range tasks {
		if taskDeclaresHealthcheck(task) && task.NodeID != "" {
			nodeTasks[task.NodeID] = append(nodeTasks[task.NodeID], task.ID)
		}
	}
	if len(nodeTasks) == 0 {
		return lh
	}

	nodeResp, err := client.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		// Degrade to "unknown" rather than failing the whole command: no node
		// address can be resolved, so no node can be asked and every task shows
		// "-" — the same as if every endpoint were unreachable.
		return lh
	}
	addrByNode := make(map[string]string, len(nodeResp.Nodes))
	for _, n := range nodeResp.Nodes {
		addrByNode[n.ID] = n.Status.Addr
	}

	// Deterministic order keeps the output (and any debugging) stable.
	nodeIDs := make([]string, 0, len(nodeTasks))
	for id := range nodeTasks {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: healthRequestTimeout}
	}

	rows, answered := fanOutNodeHealth(ctx, httpClient, nodeIDs, addrByNode, opts.Port)

	var pending []string
	for _, nodeID := range nodeIDs {
		if answered[nodeID] {
			lh.reachable[nodeID] = true
			for _, row := range rows[nodeID] {
				lh.byTask[row.TaskID] = row.VMHealth
			}
			continue
		}
		pending = append(pending, nodeID)
	}

	// A node whose own address did not answer may still be this machine, whose
	// daemon kept the default loopback bind. Accept the loopback answer only for
	// nodes that own a task it reports, so another node's endpoint can never be
	// misattributed.
	//
	// Known limitation: a task reassigned away from this machine whose local
	// sidecar has not been swept yet still carries its ID here and would be
	// attributed to the node SwarmKit now reports; the window is a daemon restart
	// on this machine plus a reassignment to a node that cannot be reached.
	if len(pending) > 0 {
		loopRows, loopAnswered, _ := getNodeTaskHealth(ctx, httpClient, healthLoopbackAddr, opts.Port)
		if loopAnswered {
			owned := make(map[string]bool, len(loopRows))
			for _, row := range loopRows {
				owned[row.TaskID] = true
			}
			for _, nodeID := range pending {
				if !ownsAny(nodeTasks[nodeID], owned) {
					continue
				}
				lh.reachable[nodeID] = true
				for _, id := range nodeTasks[nodeID] {
					for _, row := range loopRows {
						if row.TaskID == id {
							lh.byTask[row.TaskID] = row.VMHealth
						}
					}
				}
			}
		}
	}

	return lh
}

// fanOutNodeHealth queries every node that has an address, concurrently and
// bounded, and reports what each node answered.
func fanOutNodeHealth(ctx context.Context, client *http.Client, nodeIDs []string, addrByNode map[string]string, port int) (map[string][]runtime.TaskHealth, map[string]bool) {
	rows := make(map[string][]runtime.TaskHealth, len(nodeIDs))
	answered := make(map[string]bool, len(nodeIDs))

	type result struct {
		nodeID   string
		rows     []runtime.TaskHealth
		answered bool
	}
	results := make([]result, len(nodeIDs))

	fctx, cancel := context.WithTimeout(ctx, healthFanoutTimeout)
	defer cancel()

	sem := make(chan struct{}, healthMaxConcurrency)
	var wg sync.WaitGroup
	for i, nodeID := range nodeIDs {
		addr := addrByNode[nodeID]
		if addr == "" {
			continue
		}
		wg.Add(1)
		go func(i int, nodeID, addr string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-fctx.Done():
				return
			}
			defer func() { <-sem }()
			r, ok, _ := getNodeTaskHealth(fctx, client, addr, port)
			results[i] = result{nodeID: nodeID, rows: r, answered: ok}
		}(i, nodeID, addr)
	}
	wg.Wait()

	for _, r := range results {
		if r.nodeID == "" {
			continue
		}
		rows[r.nodeID] = r.rows
		answered[r.nodeID] = r.answered
	}
	return rows, answered
}

// getNodeTaskHealth fetches one node's live per-task health. answered reports
// whether the node responded at all: a non-200 (for example a 404 from a daemon
// without the endpoint) or an undecodable body still means the node is up, so
// its tasks should fall back to the derived value rather than to "-".
func getNodeTaskHealth(ctx context.Context, client *http.Client, addr string, port int) (tasks []runtime.TaskHealth, answered bool, err error) {
	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(addr, strconv.Itoa(port)),
		Path:   runtime.TaskHealthPath,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, false, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, true, fmt.Errorf("health endpoint returned %s", resp.Status)
	}

	var out runtime.TaskHealthResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxHealthResponseBytes)).Decode(&out); err != nil {
		return nil, true, err
	}
	return out.Tasks, true, nil
}

// ownsAny reports whether ids intersects set.
func ownsAny(ids []string, set map[string]bool) bool {
	for _, id := range ids {
		if set[id] {
			return true
		}
	}
	return false
}

// taskHealthDisplay is the value shown in the HEALTH column: the daemon's live
// state when it could be fetched, otherwise the state-derived value, and "-"
// when the owning node could not be reached at all (never a possibly-wrong
// value).
func taskHealthDisplay(task *api.Task, lh liveHealth) string {
	if !taskDeclaresHealthcheck(task) {
		return "-"
	}
	// No live health to show yet for a task that is terminal (its verdict is its
	// state), or that SwarmKit has not scheduled to a node at all. A sidecar that
	// has not been swept yet must not outrank either.
	if !lh.enabled || !liveHealthApplies(task.Status.State) || task.NodeID == "" {
		return taskHealth(task)
	}
	if h, ok := lh.byTask[task.ID]; ok {
		return h.State
	}
	if lh.reachable[task.NodeID] {
		// The node answered but has no live health for this task yet (it has not
		// started) or any more (it stopped): the derived value is the honest one.
		return taskHealth(task)
	}
	return "-"
}

// liveHealthApplies reports whether a task's state is one where a live
// healthcheck verdict is meaningful — the VM is up, or is being health-gated
// towards RUNNING. Terminal states carry their own verdict (FAILED) or none.
func liveHealthApplies(state api.TaskState) bool {
	switch state {
	case api.TaskStateFailed, api.TaskStateRejected, api.TaskStateCompleted,
		api.TaskStateShutdown, api.TaskStateRemove, api.TaskStateOrphaned:
		return false
	default:
		return true
	}
}

// taskWithHealth wraps a SwarmKit task so its JSON representation also carries
// the HEALTH value, without mutating or forking the API type. LiveHealth is the
// raw live observation and is omitted when it could not be fetched.
type taskWithHealth struct {
	*api.Task
	Health     string            `json:"health"`
	LiveHealth *runtime.VMHealth `json:"live_health,omitempty"`
}

// writeTasksJSON marshals tasks as JSON with the HEALTH value overlaying the
// state-derived one, plus the raw live observation when it was fetched. Each
// task is serialized through redact.Task, so a registry credential on its spec
// can never reach stdout.
func writeTasksJSON(w io.Writer, tasks []*api.Task, lh liveHealth) error {
	withHealth := make([]taskWithHealth, 0, len(tasks))
	for _, task := range tasks {
		row := taskWithHealth{Task: redact.Task(task), Health: taskHealthDisplay(task, lh)}
		if taskDeclaresHealthcheck(task) && liveHealthApplies(task.Status.State) {
			if h, ok := lh.byTask[task.ID]; ok {
				live := h
				row.LiveHealth = &live
			}
		}
		withHealth = append(withHealth, row)
	}
	data, err := json.MarshalIndent(withHealth, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal tasks: %w", err)
	}
	fmt.Fprintln(w, string(data))
	return nil
}
