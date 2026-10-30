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
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// fakeTaskClient embeds the generated ControlClient interface and overrides
// only the RPCs the task commands use, so the table/JSON rendering can be
// exercised without a cluster.
type fakeTaskClient struct {
	api.ControlClient
	tasks    []*api.Task
	services []*api.Service
	nodes    []*api.Node
}

func (f *fakeTaskClient) ListTasks(context.Context, *api.ListTasksRequest, ...grpc.CallOption) (*api.ListTasksResponse, error) {
	return &api.ListTasksResponse{Tasks: f.tasks}, nil
}

func (f *fakeTaskClient) ListServices(context.Context, *api.ListServicesRequest, ...grpc.CallOption) (*api.ListServicesResponse, error) {
	return &api.ListServicesResponse{Services: f.services}, nil
}

func (f *fakeTaskClient) ListNodes(context.Context, *api.ListNodesRequest, ...grpc.CallOption) (*api.ListNodesResponse, error) {
	return &api.ListNodesResponse{Nodes: f.nodes}, nil
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
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "table", "", false, false, false, "", "", derivedHealthOptions()))

	out := buf.String()
	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "SERVICE")
	assert.Contains(t, out, "STATUS")
	assert.Contains(t, out, "HEALTH")
	assert.Contains(t, out, "NODE")
	assert.Contains(t, out, "IMAGE")

	got := healthColumn(t, out, "ID SERVICE STATUS HEALTH NODE IMAGE ERROR")
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
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "json", "", false, false, false, "", "", derivedHealthOptions()))

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
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "table", "", true, false, false, "", "", derivedHealthOptions()))

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

// fakeTaskHealthLister is the narrow lister the --health path needs, so the
// rendering and the partial exit can be exercised without certificates or a
// live manager.
type fakeTaskHealthLister struct {
	doc     v1.TaskList
	partial bool
	err     error
}

func (f *fakeTaskHealthLister) ListTasksHealth(context.Context) (v1.TaskList, bool, error) {
	return f.doc, f.partial, f.err
}

// partialTaskHealthDoc is one readable task, one task on a node that did not
// answer, and the partial entry the manager reports for that node.
func partialTaskHealthDoc() v1.TaskList {
	doc := v1.NewTaskList([]v1.Task{
		{
			ID: "task-1", ServiceID: "svc-1", NodeID: "node-1", NodeName: "worker-1",
			State: "running", Image: "nginx:alpine",
			Health: &v1.TaskHealth{Status: "unhealthy"},
		},
		{
			ID: "task-2", ServiceID: "svc-2", NodeID: "node-2", NodeName: "worker-2",
			State: "running", Image: "redis:7",
		},
	})
	doc.Partial = []v1.Partial{{NodeID: "node-2", NodeName: "worker-2"}}
	return doc
}

// TestTaskListHealthJSONPartial proves the JSON path prints the document first,
// including its partial array, and only then returns the partial error that maps
// to exit 7.
func TestTaskListHealthJSONPartial(t *testing.T) {
	lister := &fakeTaskHealthLister{doc: partialTaskHealthDoc(), partial: true}

	var buf bytes.Buffer
	err := listTasksWithHealthClient(context.Background(), lister, &buf, "json", "", false, false, "", "")
	require.Error(t, err)
	assert.Equal(t, 7, output.ExitCode(err))

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodePartial, typed.Code)
	assert.Contains(t, typed.Message, "worker-2")
	// cliExitCode is what main applies, and 7 is the documented partial status.
	assert.Equal(t, 7, cliExitCode(err))

	var doc v1.TaskList
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc), buf.String())
	require.Len(t, doc.Items, 2)
	require.Len(t, doc.Partial, 1)
	require.NotNil(t, doc.Items[0].Health)
	assert.Equal(t, "unhealthy", doc.Items[0].Health.Status)
}

// TestTaskListHealthPartialStderrDocument pins the document main writes to
// stderr in -o json mode: exactly the v1 Error envelope with code "partial".
func TestTaskListHealthPartialStderrDocument(t *testing.T) {
	err := partialTasksError([]v1.Partial{{NodeID: "node-2", NodeName: "worker-2"}})

	var stderr bytes.Buffer
	require.NoError(t, output.WriteError(&stderr, err))

	var doc v1.ErrorDocument
	require.NoError(t, json.Unmarshal(stderr.Bytes(), &doc), stderr.String())
	assert.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
	assert.Equal(t, v1.KindError, doc.Kind)
	assert.Equal(t, string(output.CodePartial), doc.Error.Code)
	assert.Contains(t, doc.Error.Message, "worker-2")
}

// TestTaskListHealthPartialErrorCountsDistinctNodes proves the message counts
// nodes, not entries: an aggregated partial list that names the same node twice
// (one partial per page per failed node) must read "1 node(s)" once.
func TestTaskListHealthPartialErrorCountsDistinctNodes(t *testing.T) {
	err := partialTasksError([]v1.Partial{
		{NodeID: "node-2"},
		{NodeID: "node-2", NodeName: "worker-2"},
		{NodeID: "node-3", NodeName: "worker-3"},
	})

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodePartial, typed.Code)
	assert.Equal(t, "health is partial: 2 node(s) did not answer: worker-2, worker-3", typed.Message)
	assert.Equal(t, 7, output.ExitCode(err))
}

// TestTaskListHealthPartialErrorRepeatsNoName covers the no-node-ID path: two
// entries that carry only the same name are one node.
func TestTaskListHealthPartialErrorRepeatsNoName(t *testing.T) {
	err := partialTasksError([]v1.Partial{
		{NodeName: "worker-2"},
		{NodeName: "worker-2"},
	})

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "health is partial: 1 node(s) did not answer: worker-2", typed.Message)
}

// TestTaskListHealthJSONComplete pins the 200 path: one document, no error, exit
// 0.
func TestTaskListHealthJSONComplete(t *testing.T) {
	doc := v1.NewTaskList([]v1.Task{
		{ID: "task-1", State: "running", Health: &v1.TaskHealth{Status: "healthy"}},
	})

	var buf bytes.Buffer
	err := listTasksWithHealthClient(context.Background(), &fakeTaskHealthLister{doc: doc}, &buf,
		"json", "", false, false, "", "")
	require.NoError(t, err)
	assert.Equal(t, 0, output.ExitCode(err))

	var got v1.TaskList
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got), buf.String())
	require.Len(t, got.Items, 1)
	assert.Empty(t, got.Partial)
}

// TestTaskListHealthNamePartial proves -o name keeps its stdout shape and still
// reports the partial.
func TestTaskListHealthNamePartial(t *testing.T) {
	lister := &fakeTaskHealthLister{doc: partialTaskHealthDoc(), partial: true}

	var buf bytes.Buffer
	err := listTasksWithHealthClient(context.Background(), lister, &buf, "name", "", false, false, "", "")
	assert.Equal(t, "task-1\ntask-2\n", buf.String())
	require.Error(t, err)
	assert.Equal(t, 7, output.ExitCode(err))

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodePartial, typed.Code)
}

// TestTaskListHealthTable proves the table prints the API's HEALTH value and
// that the partial error is what names the silent node on stderr.
func TestTaskListHealthTable(t *testing.T) {
	lister := &fakeTaskHealthLister{doc: partialTaskHealthDoc(), partial: true}

	var buf bytes.Buffer
	err := listTasksWithHealthClient(context.Background(), lister, &buf, "table", "", false, false, "", "")
	require.Error(t, err)
	assert.Equal(t, 7, output.ExitCode(err))
	assert.Contains(t, err.Error(), "worker-2")

	out := buf.String()
	assert.Equal(t, []string{"unhealthy", "-"}, healthColumn(t, out, "ID SERVICE STATUS HEALTH NODE IMAGE ERROR"))
	assert.Contains(t, out, "RUNNING")
	assert.Contains(t, out, "nginx:alpine")
}

// TestTaskListHealthTableQuiet keeps --quiet's ID-only output on the API path.
func TestTaskListHealthTableQuiet(t *testing.T) {
	lister := &fakeTaskHealthLister{doc: partialTaskHealthDoc(), partial: true}

	var buf bytes.Buffer
	err := listTasksWithHealthClient(context.Background(), lister, &buf, "table", "", true, false, "", "")
	assert.Equal(t, "task-1\ntask-2\n", buf.String())
	assert.Equal(t, 7, output.ExitCode(err))
}

// TestTaskListHealthMapsClientError keeps the stable-code mapping of a failed
// health read: a non-partial failure never prints a document.
func TestTaskListHealthMapsClientError(t *testing.T) {
	lister := &fakeTaskHealthLister{err: output.NewError(output.CodeUnavailable, "node broker is down")}

	var buf bytes.Buffer
	err := listTasksWithHealthClient(context.Background(), lister, &buf, "json", "", false, false, "", "")
	require.Error(t, err)
	assert.Empty(t, buf.String())

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeUnavailable, typed.Code)
	assert.Equal(t, 6, output.ExitCode(err))
}

// TestTaskListHealthFlagValidation proves every rejection happens before a
// client is built: each case must return CodeUsage.
func TestTaskListHealthFlagValidation(t *testing.T) {
	prevHost, prevFormat := apiHost, outputFormat
	t.Cleanup(func() {
		apiHost, outputFormat = prevHost, prevFormat
	})

	tests := []struct {
		name    string
		host    string
		wantMsg string
		setup   func(t *testing.T, cmd *cobra.Command)
	}{
		{
			name:    "requires a remote host",
			wantMsg: "--host",
		},
		{
			name:    "rejected with no-health",
			host:    "https://mgr:4244",
			wantMsg: "--no-health",
			setup: func(t *testing.T, cmd *cobra.Command) {
				require.NoError(t, cmd.Flags().Set("no-health", "true"))
			},
		},
		{
			name:    "rejected with a changed health-port",
			host:    "https://mgr:4244",
			wantMsg: "--health-port",
			setup: func(t *testing.T, cmd *cobra.Command) {
				require.NoError(t, cmd.Flags().Set("health-port", "9000"))
			},
		},
		{
			name:    "rejected with the legacy format flag",
			host:    "https://mgr:4244",
			wantMsg: "--format",
			setup: func(t *testing.T, cmd *cobra.Command) {
				require.NoError(t, cmd.Flags().Set("format", "json"))
			},
		},
		{
			name:    "rejected with ndjson output",
			host:    "https://mgr:4244",
			wantMsg: "unsupported output format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiHost, outputFormat = tt.host, ""
			if tt.name == "rejected with ndjson output" {
				outputFormat = "ndjson"
			}

			cmd := newTaskListCommand()
			require.NoError(t, cmd.Flags().Set("health", "true"))
			if tt.setup != nil {
				tt.setup(t, cmd)
			}

			err := cmd.RunE(cmd, nil)
			require.Error(t, err)

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, output.CodeUsage, typed.Code)
			assert.Contains(t, typed.Message, tt.wantMsg)
		})
	}
}

// TestTaskListHealthFlagExists pins the new flag and its default.
func TestTaskListHealthFlagExists(t *testing.T) {
	cmd := newTaskListCommand()

	flag := cmd.Flags().Lookup("health")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
	assert.Contains(t, flag.Usage, "exit 7")
}
