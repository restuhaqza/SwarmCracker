package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	listNodesErr   error
	listNodesCalls int
}

func (f *fakeHealthClient) ListNodes(context.Context, *api.ListNodesRequest, ...grpc.CallOption) (*api.ListNodesResponse, error) {
	f.listNodesCalls++
	if f.listNodesErr != nil {
		return nil, f.listNodesErr
	}
	return &api.ListNodesResponse{Nodes: f.nodes}, nil
}

func (f *fakeHealthClient) ListTasks(context.Context, *api.ListTasksRequest, ...grpc.CallOption) (*api.ListTasksResponse, error) {
	return &api.ListTasksResponse{Tasks: f.tasks}, nil
}

func (f *fakeHealthClient) ListServices(context.Context, *api.ListServicesRequest, ...grpc.CallOption) (*api.ListServicesResponse, error) {
	return &api.ListServicesResponse{Services: f.services}, nil
}

// healthServer starts a fake daemon health endpoint and returns its host, its
// port, and an HTTP client pointed at it.
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

// rawHealthServer starts a server that always answers with a fixed status and
// body, for the "reachable but unusable" cases.
func rawHealthServer(t *testing.T, status int, body string) clusterHealthOptions {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return clusterHealthOptions{Port: port, HTTPClient: srv.Client()}
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
// live row (or a terminal task), and "-" when the owning node could not be
// reached at all.
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
		{"unscheduled task falls back", healthcheckedTaskOn("t-nosched", "", api.TaskStatePending), live, "STARTING"},
		{"terminal FAILED outranks a stale live row", healthcheckedTaskOn("t-live", "node-a", api.TaskStateFailed), live, "FAILED"},
		{"terminal COMPLETED outranks a stale live row", healthcheckedTaskOn("t-live", "node-a", api.TaskStateCompleted), live, "-"},
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

// TestFetchLiveHealth_ListNodesError proves a control-API failure degrades to
// "unknown" instead of failing the command.
func TestFetchLiveHealth_ListNodesError(t *testing.T) {
	client := &fakeHealthClient{
		listNodesErr: errors.New("raft: no leader"),
		tasks:        []*api.Task{healthcheckedTaskOn("t-a", "node-a", api.TaskStateRunning)},
	}

	lh := fetchLiveHealth(context.Background(), client, client.tasks, clusterHealthOptions{Port: defaultHealthPort})

	assert.True(t, lh.enabled)
	assert.Empty(t, lh.byTask)
	assert.Empty(t, lh.reachable)
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

// TestFetchLiveHealth_ReachableButUnusable covers an endpoint that answers with
// something we cannot use (404 from an older daemon, or a malformed body). The
// node is up, so its tasks must fall back to the derived value, not "-".
func TestFetchLiveHealth_ReachableButUnusable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"not found (older daemon)", http.StatusNotFound, "404 page not found\n"},
		{"malformed body", http.StatusOK, "{not json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := rawHealthServer(t, tc.status, tc.body)
			client := &fakeHealthClient{
				nodes: []*api.Node{{ID: "node-a", Status: api.NodeStatus{Addr: "127.0.0.1"}}},
				tasks: []*api.Task{healthcheckedTaskOn("t-a", "node-a", api.TaskStateRunning)},
			}

			lh := fetchLiveHealth(context.Background(), client, client.tasks, opts)

			assert.True(t, lh.reachable["node-a"], "a node that answered must count as reachable")
			assert.Empty(t, lh.byTask)
			assert.Equal(t, "RUNNING", taskHealthDisplay(client.tasks[0], lh), "must fall back to the derived value")
		})
	}
}

// TestFetchLiveHealth_LoopbackFallback covers a node whose own address does not
// answer but which is this machine (the daemon kept the default loopback bind).
func TestFetchLiveHealth_LoopbackFallback(t *testing.T) {
	_, opts := healthServer(t, []runtime.TaskHealth{
		{TaskID: "t-local", VMHealth: runtime.VMHealth{State: runtime.HealthFailing}},
	})
	// 127.0.0.2 is loopback but nothing is bound there: it refuses immediately,
	// and is not healthLoopbackAddr, so the loopback fallback is what answers.
	client := &fakeHealthClient{
		nodes: []*api.Node{{ID: "node-local", Status: api.NodeStatus{Addr: "127.0.0.2"}}},
		tasks: []*api.Task{healthcheckedTaskOn("t-local", "node-local", api.TaskStateRunning)},
	}

	lh := fetchLiveHealth(context.Background(), client, client.tasks, opts)

	assert.True(t, lh.reachable["node-local"])
	assert.Equal(t, runtime.HealthFailing, lh.byTask["t-local"].State)
	assert.Equal(t, "failing", taskHealthDisplay(client.tasks[0], lh))
}

// TestFetchLiveHealth_LoopbackNotMisattributed proves a loopback answer is only
// accepted for a node that owns a task it reports: another local node's endpoint
// must not be attributed to this node.
func TestFetchLiveHealth_LoopbackNotMisattributed(t *testing.T) {
	_, opts := healthServer(t, []runtime.TaskHealth{
		{TaskID: "someone-elses-task", VMHealth: runtime.VMHealth{State: runtime.HealthFailing}},
	})
	client := &fakeHealthClient{
		nodes: []*api.Node{{ID: "node-local", Status: api.NodeStatus{Addr: "127.0.0.2"}}},
		tasks: []*api.Task{healthcheckedTaskOn("t-local", "node-local", api.TaskStateRunning)},
	}

	lh := fetchLiveHealth(context.Background(), client, client.tasks, opts)

	assert.False(t, lh.reachable["node-local"])
	assert.Empty(t, lh.byTask)
	assert.Equal(t, "-", taskHealthDisplay(client.tasks[0], lh))
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
	assert.Equal(t, []string{"failing", "STARTING", "-"}, healthColumn(t, buf.String(), "ID SERVICE STATUS HEALTH NODE IMAGE ERROR"))

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

// TestWriteTasksJSON_TerminalAndNil covers the JSON path's two guards: a terminal
// task must not carry a stale live row (neither as health nor as live_health),
// and a nil task must not panic the writer.
func TestWriteTasksJSON_TerminalAndNil(t *testing.T) {
	failed := healthcheckedTaskOn("t-failed", "node-a", api.TaskStateFailed)
	lh := liveHealth{
		enabled:   true,
		byTask:    map[string]runtime.VMHealth{"t-failed": {State: runtime.HealthHealthy}},
		reachable: map[string]bool{"node-a": true},
	}

	var buf bytes.Buffer
	require.NoError(t, writeTasksJSON(&buf, []*api.Task{failed, nil}, lh))

	var got []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got, 2)
	assert.Equal(t, "FAILED", got[0]["health"], "a terminal task reports its state, not a stale live row")
	assert.Nil(t, got[0]["live_health"], "a terminal task must not carry live_health")
	assert.Equal(t, "-", got[1]["health"], "a nil task renders as no health")
}

// TestListTasksQuietSkipsFanOut proves --quiet (a table run) prints only IDs and
// never resolves nodes.
func TestListTasksQuietSkipsFanOut(t *testing.T) {
	_, opts := healthServer(t, []runtime.TaskHealth{
		{TaskID: "task-a-00000000000000", VMHealth: runtime.VMHealth{State: runtime.HealthHealthy}},
	})
	client := &fakeHealthClient{
		nodes: []*api.Node{{ID: "node-a", Status: api.NodeStatus{Addr: "127.0.0.1"}}},
		tasks: []*api.Task{healthcheckedTaskOn("task-a-00000000000000", "node-a", api.TaskStateRunning)},
	}

	var buf bytes.Buffer
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "table", "", true, false, false, "", "", opts))

	assert.Equal(t, "task-a-00000000000000\n", buf.String())
	assert.Zero(t, client.listNodesCalls, "the quiet path must not fan out")
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
	assert.Equal(t, []string{"unhealthy", "-"}, healthColumn(t, buf.String(), "ID STATUS HEALTH NODE IMAGE ERROR"))
}

// TestInvalidHealthPortIsRejected proves the flag is validated before any RPC,
// on both commands, even with --no-health.
func TestInvalidHealthPortIsRejected(t *testing.T) {
	client := &fakeHealthClient{
		services: []*api.Service{{ID: "svc-1", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "web"}}}},
	}
	bad := clusterHealthOptions{Port: 0, Disabled: true}

	var buf bytes.Buffer
	err := listServiceTasksWithClient(context.Background(), client, &buf, "web", "table", "", false, false, bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--health-port")

	buf.Reset()
	err = listTasksWithClient(context.Background(), client, &buf, "table", "", false, false, false, "", "", bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--health-port")
}

// TestTaskListCommandFlags pins the flags the fan-out is wired to, so a
// silently-dropped flag would fail here.
func TestTaskListCommandFlags(t *testing.T) {
	cmd := newTaskListCommand()

	healthPort := cmd.Flags().Lookup("health-port")
	require.NotNil(t, healthPort)
	assert.Equal(t, strconv.Itoa(defaultHealthPort), healthPort.DefValue)

	noHealth := cmd.Flags().Lookup("no-health")
	require.NotNil(t, noHealth)
	assert.Equal(t, "false", noHealth.DefValue)
	// Both flags come from the same defaults the fan-out uses.
	assert.Equal(t, "Skip the live-health lookup; derive HEALTH from the task state only", noHealth.Usage)
}
