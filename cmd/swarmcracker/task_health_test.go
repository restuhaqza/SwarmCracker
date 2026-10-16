package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// derivedHealthOptions is what the purely state-derived tests pass: it disables
// the fan-out so no node is contacted, matching the pre-#67 rendering.
func derivedHealthOptions() clusterHealthOptions {
	return clusterHealthOptions{Port: defaultHealthPort, Disabled: true}
}

// fakeHealthClient embeds the generated ControlClient and overrides the RPCs
// the fan-out needs, counting ListNodes so a test can prove --no-health skips it.
type fakeHealthClient struct {
	api.ControlClient
	nodes          []*api.Node
	tasks          []*api.Task
	services       []*api.Service
	listNodesCalls int
}

func (f *fakeHealthClient) ListNodes(context.Context, *api.ListNodesRequest, ...grpc.CallOption) (*api.ListNodesResponse, error) {
	f.listNodesCalls++
	return &api.ListNodesResponse{Nodes: f.nodes}, nil
}

func (f *fakeHealthClient) ListTasks(context.Context, *api.ListTasksRequest, ...grpc.CallOption) (*api.ListTasksResponse, error) {
	return &api.ListTasksResponse{Tasks: f.tasks}, nil
}

func (f *fakeHealthClient) ListServices(context.Context, *api.ListServicesRequest, ...grpc.CallOption) (*api.ListServicesResponse, error) {
	return &api.ListServicesResponse{Services: f.services}, nil
}

// healthServer starts a fake daemon health endpoint and returns its host, port
// and an HTTP client pointed at it.
func healthServer(t *testing.T, tasks []runtime.TaskHealth) (host string, opts clusterHealthOptions) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != runtime.TaskHealthPath {
			t.Errorf("unexpected path %q, want %q", r.URL.Path, runtime.TaskHealthPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runtime.TaskHealthResponse{Tasks: tasks})
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return u.Hostname(), clusterHealthOptions{Port: port, HTTPClient: srv.Client()}
}

func healthcheckedTaskOn(id, nodeID string, state api.TaskState) *api.Task {
	task := healthcheckedTask(id, state)
	task.NodeID = nodeID
	return task
}

func TestHealthOptionsValidate(t *testing.T) {
	assert.NoError(t, clusterHealthOptions{Port: 8080}.validate())
	assert.NoError(t, clusterHealthOptions{Port: defaultHealthPort, Disabled: true}.validate())
	assert.Error(t, clusterHealthOptions{Port: 0}.validate())
	assert.Error(t, clusterHealthOptions{Port: -1}.validate())
	assert.Error(t, clusterHealthOptions{Port: 70000}.validate())
}

// TestTaskHealthDisplay is the direct table test for the overlay rule: live
// health when it was fetched, the derived value on a reachable node without a
// live row, and "-" when the owning node could not be reached.
func TestTaskHealthDisplay(t *testing.T) {
	live := liveHealth{
		enabled:   true,
		byTask:    map[string]runtime.VMHealth{"t-live": {State: runtime.HealthFailing}},
		reachable: map[string]bool{"node-a": true},
	}
	tests := []struct {
		name string
		task *api.Task
		lh   liveHealth
		want string
	}{
		{"nil task", nil, live, "-"},
		{"no healthcheck", plainTask("t-plain", api.TaskStateRunning), live, "-"},
		{"disabled prefers derived", healthcheckedTaskOn("t-live", "node-a", api.TaskStateRunning), liveHealth{enabled: false}, "RUNNING"},
		{"live overrides derived", healthcheckedTaskOn("t-live", "node-a", api.TaskStateRunning), live, "failing"},
		{"reachable without live row falls back", healthcheckedTaskOn("t-other", "node-a", api.TaskStateRunning), live, "RUNNING"},
		{"reachable, not started", healthcheckedTaskOn("t-other", "node-a", api.TaskStateStarting), live, "STARTING"},
		{"unreachable node", healthcheckedTaskOn("t-x", "node-b", api.TaskStateRunning), live, "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, taskHealthDisplay(tt.task, tt.lh))
		})
	}
}

// TestFetchLiveHealth_FanOut exercises the fan-out against a fake endpoint and
// proves a node with no address is simply not reachable.
func TestFetchLiveHealth_FanOut(t *testing.T) {
	host, opts := healthServer(t, []runtime.TaskHealth{
		{TaskID: "t-a", VMHealth: runtime.VMHealth{State: runtime.HealthFailing, FailingStreak: 2}},
	})

	client := &fakeHealthClient{
		nodes: []*api.Node{
			{ID: "node-a", Status: api.NodeStatus{Addr: host}},
			{ID: "node-b"}, // no address: cannot be reached
		},
		tasks: []*api.Task{
			healthcheckedTaskOn("t-a", "node-a", api.TaskStateRunning),
			healthcheckedTaskOn("t-b", "node-b", api.TaskStateRunning),
			func() *api.Task { // no healthcheck: must not trigger a lookup
				task := plainTask("t-plain", api.TaskStateRunning)
				task.NodeID = "node-a"
				return task
			}(),
		},
	}

	lh := fetchLiveHealth(context.Background(), client, client.tasks, opts)

	assert.True(t, lh.reachable["node-a"])
	assert.False(t, lh.reachable["node-b"])
	assert.Equal(t, runtime.HealthFailing, lh.byTask["t-a"].State)
	assert.Equal(t, 2, lh.byTask["t-a"].FailingStreak)
	_, ok := lh.byTask["t-b"]
	assert.False(t, ok)
}

// TestFetchLiveHealth_NoHealthSkipsLookup proves --no-health never contacts the
// control API for nodes nor the endpoint.
func TestFetchLiveHealth_NoHealthSkipsLookup(t *testing.T) {
	client := &fakeHealthClient{
		nodes: []*api.Node{{ID: "node-a", Status: api.NodeStatus{Addr: "127.0.0.1"}}},
		tasks: []*api.Task{healthcheckedTaskOn("t-a", "node-a", api.TaskStateRunning)},
	}

	lh := fetchLiveHealth(context.Background(), client, client.tasks, derivedHealthOptions())

	assert.False(t, lh.enabled)
	assert.Zero(t, client.listNodesCalls, "ListNodes must not be called with --no-health")
	assert.Empty(t, lh.byTask)
}

// TestFetchLiveHealth_UnreachableNode checks a node whose endpoint refuses the
// connection (port 1 on loopback) stays unreachable instead of erroring.
func TestFetchLiveHealth_UnreachableNode(t *testing.T) {
	client := &fakeHealthClient{
		nodes: []*api.Node{{ID: "node-a", Status: api.NodeStatus{Addr: "127.0.0.1"}}},
		tasks: []*api.Task{healthcheckedTaskOn("t-a", "node-a", api.TaskStateRunning)},
	}

	lh := fetchLiveHealth(context.Background(), client, client.tasks,
		clusterHealthOptions{Port: 1, HTTPClient: &http.Client{Timeout: 500 * time.Millisecond}})

	assert.False(t, lh.reachable["node-a"])
	assert.Empty(t, lh.byTask)
}

// TestListTasksWithLiveHealth is the end-to-end rendering test: a RUNNING task
// on a reachable node failing its checks shows "failing" instead of "RUNNING",
// and the JSON carries the raw live observation.
func TestListTasksWithLiveHealth(t *testing.T) {
	host, opts := healthServer(t, []runtime.TaskHealth{
		{TaskID: "task-failing-0000000000", VMHealth: runtime.VMHealth{State: runtime.HealthFailing, FailingStreak: 1, Command: "true", UpdatedAt: time.Unix(0, 0).UTC()}},
	})
	client := &fakeHealthClient{
		nodes: []*api.Node{{ID: "node-a", Status: api.NodeStatus{Addr: host}}},
		tasks: []*api.Task{
			healthcheckedTaskOn("task-failing-0000000000", "node-a", api.TaskStateRunning),
			healthcheckedTaskOn("task-other-000000000000", "node-a", api.TaskStateStarting),
			plainTask("task-plain-0000000000000", api.TaskStateRunning),
		},
	}

	var buf bytes.Buffer
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "table", "", false, false, false, "", "", opts))
	assert.Equal(t, []string{"failing", "STARTING", "-"}, healthColumn(t, buf.String(), "ID SERVICE STATUS HEALTH NODE IMAGE"))

	buf.Reset()
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "json", "", false, false, false, "", "", opts))
	var got []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got, 3)
	assert.Equal(t, "failing", got[0]["health"])
	live, ok := got[0]["live_health"].(map[string]any)
	require.True(t, ok, "live_health missing from JSON")
	assert.Equal(t, runtime.HealthFailing, live["state"])
	assert.Equal(t, float64(1), live["failing_streak"])
	assert.Nil(t, got[1]["live_health"], "a task without a live row must not carry live_health")
}

// TestServicePSWithLiveHealth proves the fan-out is wired into `service ps`
// too, and that an unreachable node degrades to "-" while the command succeeds.
func TestServicePSWithLiveHealth(t *testing.T) {
	host, opts := healthServer(t, []runtime.TaskHealth{
		{TaskID: "task-live-0000000000000", VMHealth: runtime.VMHealth{State: runtime.HealthUnhealthy}},
	})
	client := &fakeHealthClient{
		services: []*api.Service{{ID: "svc-1", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "web"}}}},
		nodes: []*api.Node{
			{ID: "node-a", Status: api.NodeStatus{Addr: host}},
			{ID: "node-b"}, // no address
		},
	}
	for _, task := range []*api.Task{
		healthcheckedTaskOn("task-live-0000000000000", "node-a", api.TaskStateRunning),
		healthcheckedTaskOn("task-gone-0000000000000", "node-b", api.TaskStateRunning),
	} {
		task.ServiceID = "svc-1"
		client.tasks = append(client.tasks, task)
	}

	var buf bytes.Buffer
	require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "table", "", false, false, opts))
	assert.Equal(t, []string{"unhealthy", "-"}, healthColumn(t, buf.String(), "ID STATUS HEALTH NODE IMAGE"))
}
