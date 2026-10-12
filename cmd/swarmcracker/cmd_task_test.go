package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// fakeTaskClient embeds the generated ControlClient interface and overrides
// only the RPCs the task commands use, so the table/JSON rendering can be
// exercised without a cluster.
type fakeTaskClient struct {
	api.ControlClient
	tasks []*api.Task
}

func (f *fakeTaskClient) ListTasks(context.Context, *api.ListTasksRequest, ...grpc.CallOption) (*api.ListTasksResponse, error) {
	return &api.ListTasksResponse{Tasks: f.tasks}, nil
}

func (f *fakeTaskClient) GetTask(_ context.Context, req *api.GetTaskRequest, _ ...grpc.CallOption) (*api.GetTaskResponse, error) {
	for _, t := range f.tasks {
		if t.ID == req.TaskID {
			return &api.GetTaskResponse{Task: t}, nil
		}
	}
	return nil, fmt.Errorf("task not found: %s", req.TaskID)
}

// healthcheckedTask builds a task whose container declares a healthcheck and
// whose state is set to state.
func healthcheckedTask(id string, state api.TaskState) *api.Task {
	return &api.Task{
		ID:        id,
		ServiceID: "svc-00000000000000000000",
		NodeID:    "node-0000000000000000000",
		Spec: api.TaskSpec{Runtime: &api.TaskSpec_Container{Container: &api.ContainerSpec{
			Image:       "nginx:alpine",
			Healthcheck: &api.HealthConfig{Test: []string{"CMD-SHELL", "true"}},
		}}},
		Status: api.TaskStatus{State: state},
	}
}

// plainTask builds a task with no declared healthcheck.
func plainTask(id string, state api.TaskState) *api.Task {
	return &api.Task{
		ID:        id,
		ServiceID: "svc-00000000000000000000",
		NodeID:    "node-0000000000000000000",
		Spec: api.TaskSpec{Runtime: &api.TaskSpec_Container{Container: &api.ContainerSpec{
			Image: "nginx:alpine",
		}}},
		Status: api.TaskStatus{State: state},
	}
}

// TestTaskHealth is the direct table test for the shared helper: the HEALTH
// value is derived from the health-gated state, and is "-" whenever the task
// declares no healthcheck.
func TestTaskHealth(t *testing.T) {
	tests := []struct {
		name string
		task *api.Task
		want string
	}{
		{"nil task", nil, "-"},
		{"nil container", &api.Task{Spec: api.TaskSpec{}, Status: api.TaskStatus{State: api.TaskStateRunning}}, "-"},
		{"no healthcheck", plainTask("t", api.TaskStateRunning), "-"},
		{"new", healthcheckedTask("t", api.TaskStateNew), "STARTING"},
		{"pending", healthcheckedTask("t", api.TaskStatePending), "STARTING"},
		{"assigned", healthcheckedTask("t", api.TaskStateAssigned), "STARTING"},
		{"accepted", healthcheckedTask("t", api.TaskStateAccepted), "STARTING"},
		{"preparing", healthcheckedTask("t", api.TaskStatePreparing), "STARTING"},
		{"ready", healthcheckedTask("t", api.TaskStateReady), "STARTING"},
		{"starting", healthcheckedTask("t", api.TaskStateStarting), "STARTING"},
		{"running", healthcheckedTask("t", api.TaskStateRunning), "RUNNING"},
		{"failed", healthcheckedTask("t", api.TaskStateFailed), "FAILED"},
		{"rejected", healthcheckedTask("t", api.TaskStateRejected), "FAILED"},
		{"completed", healthcheckedTask("t", api.TaskStateCompleted), "-"},
		{"shutdown", healthcheckedTask("t", api.TaskStateShutdown), "-"},
		{"remove", healthcheckedTask("t", api.TaskStateRemove), "-"},
		{"orphaned", healthcheckedTask("t", api.TaskStateOrphaned), "-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, taskHealth(tt.task))
		})
	}
}

// healthColumn extracts the HEALTH field from each data row of a rendered
// table. Rows are whitespace-delimited; every column value is a non-empty
// token, so Fields gives one field per column.
func healthColumn(t *testing.T, out, header string) []string {
	t.Helper()
	lines := strings.Split(out, "\n")
	require.NotEmpty(t, lines)
	require.Equal(t, strings.Fields(header), strings.Fields(lines[0]), "unexpected header")

	healthIdx := -1
	for i, h := range strings.Fields(header) {
		if h == "HEALTH" {
			healthIdx = i
		}
	}
	require.NotEqual(t, -1, healthIdx, "header %q has no HEALTH column", header)

	var got []string
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "Total:") {
			continue
		}
		require.Greater(t, len(fields), healthIdx, "row %q has no HEALTH column", line)
		got = append(got, fields[healthIdx])
	}
	return got
}

// TestListTasksHealthColumn verifies `task ls` renders a HEALTH column with the
// derived values and keeps the other columns.
func TestListTasksHealthColumn(t *testing.T) {
	client := &fakeTaskClient{tasks: []*api.Task{
		healthcheckedTask("task-healthy-000000000000", api.TaskStateRunning),
		healthcheckedTask("task-starting-00000000000", api.TaskStateStarting),
		healthcheckedTask("task-failed-0000000000000", api.TaskStateFailed),
		plainTask("task-plain-000000000000000", api.TaskStateRunning),
	}}

	var buf bytes.Buffer
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "table", "", false, false, false, "", ""))

	out := buf.String()
	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "SERVICE")
	assert.Contains(t, out, "STATUS")
	assert.Contains(t, out, "HEALTH")
	assert.Contains(t, out, "NODE")
	assert.Contains(t, out, "IMAGE")

	got := healthColumn(t, out, "ID SERVICE STATUS HEALTH NODE IMAGE")
	assert.Equal(t, []string{"RUNNING", "STARTING", "FAILED", "-"}, got)
}

// TestListTasksJSONHealth verifies the JSON output carries the derived health
// for each task without disturbing the SwarmKit task fields.
func TestListTasksJSONHealth(t *testing.T) {
	client := &fakeTaskClient{tasks: []*api.Task{
		healthcheckedTask("task-healthy-000000000000", api.TaskStateRunning),
		plainTask("task-plain-000000000000000", api.TaskStateRunning),
	}}

	var buf bytes.Buffer
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "json", "", false, false, false, "", ""))

	var got []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got, 2)
	assert.Equal(t, "RUNNING", got[0]["health"])
	assert.Equal(t, "-", got[1]["health"])
	// The embedded task fields must survive the wrapper.
	assert.Equal(t, "task-healthy-000000000000", got[0]["id"])
}

// TestListTasksQuietUnchanged verifies --quiet still prints only IDs (no health
// column leaking into machine-readable output).
func TestListTasksQuietUnchanged(t *testing.T) {
	client := &fakeTaskClient{tasks: []*api.Task{
		healthcheckedTask("task-healthy-000000000000", api.TaskStateRunning),
	}}

	var buf bytes.Buffer
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "table", "", true, false, false, "", ""))

	assert.Equal(t, "task-healthy-000000000000\n", buf.String())
}

// TestInspectTaskHealth verifies the human-readable inspect prints the declared
// healthcheck config and the failure reason from Status.Err.
func TestInspectTaskHealth(t *testing.T) {
	task := healthcheckedTask("task-0000000000000000001", api.TaskStateFailed)
	task.Spec.GetContainer().Healthcheck = &api.HealthConfig{
		Test:        []string{"CMD-SHELL", "curl -f localhost/health"},
		Interval:    gogotypes.DurationProto(5 * time.Second),
		Timeout:     gogotypes.DurationProto(2 * time.Second),
		Retries:     4,
		StartPeriod: gogotypes.DurationProto(10 * time.Second),
	}
	task.Status.Err = "healthcheck failed: curl -f localhost/health: connection refused"

	client := &fakeTaskClient{tasks: []*api.Task{task}}

	var buf bytes.Buffer
	require.NoError(t, inspectTaskWithClient(context.Background(), client, &buf, "task-0000000000000000001", "table", true))

	out := buf.String()
	assert.Contains(t, out, "Healthcheck:")
	assert.Contains(t, out, "CMD-SHELL curl -f localhost/health")
	assert.Contains(t, out, "Interval: 5s")
	assert.Contains(t, out, "Timeout: 2s")
	assert.Contains(t, out, "Retries: 4")
	assert.Contains(t, out, "Start Period: 10s")
	assert.Contains(t, out, "healthcheck failed: curl -f localhost/health: connection refused")
}

// TestInspectTaskNoHealthcheck verifies the health section is omitted entirely
// when the task declares no healthcheck.
func TestInspectTaskNoHealthcheck(t *testing.T) {
	task := plainTask("task-0000000000000000002", api.TaskStateRunning)
	client := &fakeTaskClient{tasks: []*api.Task{task}}

	var buf bytes.Buffer
	require.NoError(t, inspectTaskWithClient(context.Background(), client, &buf, "task-0000000000000000002", "table", true))

	out := buf.String()
	assert.NotContains(t, out, "Healthcheck:")
	assert.Contains(t, out, "Status: RUNNING")
}

// TestInspectTaskJSONUnchanged verifies the default JSON format still marshals
// the raw task (healthcheck config and Status.Err are already part of it).
func TestInspectTaskJSONUnchanged(t *testing.T) {
	task := healthcheckedTask("task-0000000000000000003", api.TaskStateFailed)
	task.Status.Err = "healthcheck failed: true"
	client := &fakeTaskClient{tasks: []*api.Task{task}}

	var buf bytes.Buffer
	require.NoError(t, inspectTaskWithClient(context.Background(), client, &buf, "task-0000000000000000003", "json", true))

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "task-0000000000000000003", got["id"])
	spec, ok := got["spec"].(map[string]any)
	require.True(t, ok, "spec should be an object")
	// The oneof wrapper is marshalled under "Runtime" (gogo does not put the
	// lower-case json tag on the interface field); this is pre-existing
	// behaviour of the JSON output, not something this change alters.
	runtime, ok := spec["Runtime"].(map[string]any)
	require.True(t, ok, "spec.Runtime should be an object")
	container, ok := runtime["container"].(map[string]any)
	require.True(t, ok, "spec.Runtime.container should be an object")
	assert.Contains(t, container, "healthcheck")
}
