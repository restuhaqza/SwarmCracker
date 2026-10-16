package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/moby/swarmkit/v2/api"
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
// older daemon simply stays absent from reachable.
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
	// already assigned to a node can be looked up.
	needed := map[string]struct{}{}
	for _, task := range tasks {
		if taskDeclaresHealthcheck(task) && task.NodeID != "" {
			needed[task.NodeID] = struct{}{}
		}
	}
	if len(needed) == 0 {
		return lh
	}

	nodeResp, err := client.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		// Degrade to "unknown" rather than failing the whole command.
		return lh
	}
	addrByNode := make(map[string]string, len(nodeResp.Nodes))
	for _, n := range nodeResp.Nodes {
		addrByNode[n.ID] = n.Status.Addr
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: healthRequestTimeout}
	}

	for nodeID := range needed {
		addr := addrByNode[nodeID]
		if addr == "" {
			continue
		}
		rows, err := getNodeTaskHealth(ctx, httpClient, addr, opts.Port)
		if err != nil {
			continue
		}
		lh.reachable[nodeID] = true
		for _, row := range rows {
			lh.byTask[row.TaskID] = row.VMHealth
		}
	}
	return lh
}

// getNodeTaskHealth fetches one node's live per-task health.
func getNodeTaskHealth(ctx context.Context, client *http.Client, addr string, port int) ([]runtime.TaskHealth, error) {
	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(addr, strconv.Itoa(port)),
		Path:   runtime.TaskHealthPath,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health endpoint returned %s", resp.Status)
	}

	var out runtime.TaskHealthResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxHealthResponseBytes)).Decode(&out); err != nil {
		return nil, err
	}
	return out.Tasks, nil
}

// taskHealthDisplay is the value shown in the HEALTH column: the daemon's live
// state when it could be fetched, otherwise the state-derived value, and "-"
// when the owning node could not be reached (never a possibly-wrong value).
func taskHealthDisplay(task *api.Task, lh liveHealth) string {
	if !taskDeclaresHealthcheck(task) {
		return "-"
	}
	if !lh.enabled {
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

// taskWithHealth wraps a SwarmKit task so its JSON representation also carries
// the HEALTH value, without mutating or forking the API type. LiveHealth is the
// raw live observation and is omitted when it could not be fetched.
type taskWithHealth struct {
	*api.Task
	Health     string            `json:"health"`
	LiveHealth *runtime.VMHealth `json:"live_health,omitempty"`
}

// writeTasksJSON marshals tasks as JSON with the HEALTH value overlaying the
// state-derived one, plus the raw live observation when it was fetched.
func writeTasksJSON(w io.Writer, tasks []*api.Task, lh liveHealth) error {
	withHealth := make([]taskWithHealth, 0, len(tasks))
	for _, task := range tasks {
		row := taskWithHealth{Task: task, Health: taskHealthDisplay(task, lh)}
		if h, ok := lh.byTask[task.ID]; ok && taskDeclaresHealthcheck(task) {
			live := h
			row.LiveHealth = &live
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
