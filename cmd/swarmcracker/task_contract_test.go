package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeTaskLister is the narrow lister the contract path needs, so the JSON and
// name rendering can be exercised without a cluster or certificates.
type fakeTaskLister struct {
	tasks []v1.Task
	err   error
}

func (f *fakeTaskLister) ListTasks(context.Context) ([]v1.Task, error) {
	return f.tasks, f.err
}

func TestTaskListContractEmptyIsParseable(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, listTasksContractWithClient(
		context.Background(), &fakeTaskLister{}, &buf, "json", "", false, "", "",
	))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc), buf.String())
	assert.Equal(t, float64(v1.SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, v1.KindTaskList, doc["kind"])
	items, ok := doc["items"].([]any)
	require.True(t, ok, "items must be a JSON array")
	assert.Empty(t, items)
	next, ok := doc["next"]
	require.True(t, ok, "next must be present")
	assert.Nil(t, next)
}

func TestTaskListContractJSONAndFilter(t *testing.T) {
	lister := &fakeTaskLister{tasks: []v1.Task{
		{
			ID: "task-1", Name: "web.1.aaaaaaaaaaaa", ServiceID: "svc-1", ServiceName: "web",
			NodeID: "node-1", State: "running", DesiredState: "running",
		},
		{
			ID: "task-2", Name: "db.1.bbbbbbbbbbbb", ServiceID: "svc-2", ServiceName: "db",
			NodeID: "node-2", State: "stopped", DesiredState: "shutdown",
		},
	}}

	var buf bytes.Buffer
	require.NoError(t, listTasksContractWithClient(
		context.Background(), lister, &buf, "json", "state=running", false, "", "",
	))

	var doc v1.TaskList
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	require.Len(t, doc.Items, 1)
	assert.Equal(t, "task-1", doc.Items[0].ID)
	assert.Equal(t, "web.1.aaaaaaaaaaaa", doc.Items[0].Name)
}

func TestTaskListContractServiceFilterByName(t *testing.T) {
	lister := &fakeTaskLister{tasks: []v1.Task{
		{ID: "task-1", ServiceID: "svc-1", ServiceName: "web", State: "running"},
		{ID: "task-2", ServiceID: "svc-2", ServiceName: "db", State: "running"},
	}}

	var buf bytes.Buffer
	require.NoError(t, listTasksContractWithClient(
		context.Background(), lister, &buf, "json", "", false, "", "web",
	))

	var doc v1.TaskList
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	require.Len(t, doc.Items, 1)
	assert.Equal(t, "task-1", doc.Items[0].ID)
}

func TestTaskListContractNameMode(t *testing.T) {
	lister := &fakeTaskLister{tasks: []v1.Task{
		{ID: "id1"},
		{ID: "id2"},
	}}

	var buf bytes.Buffer
	require.NoError(t, listTasksContractWithClient(
		context.Background(), lister, &buf, "name", "", false, "", "",
	))
	assert.Equal(t, "id1\nid2\n", buf.String())
}

func TestTaskListContractMapsRPCError(t *testing.T) {
	lister := &fakeTaskLister{err: status.Error(codes.NotFound, "missing")}
	err := listTasksContractWithClient(context.Background(), lister, &bytes.Buffer{}, "json", "", false, "", "")
	require.Error(t, err)

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeNotFound, typed.Code)
}

func TestFilterV1Tasks(t *testing.T) {
	tasks := []v1.Task{
		{ID: "task-1", ServiceID: "svc-1", ServiceName: "web", NodeID: "node-1abc", State: "running"},
		{ID: "task-2", ServiceID: "svc-2", ServiceName: "db", NodeID: "node-2def", State: "stopped"},
	}

	tests := []struct {
		name      string
		filter    string
		node      string
		serviceID string
		all       bool
		want      []string
	}{
		{name: "empty filter keeps all", want: []string{"task-1", "task-2"}},
		{name: "lowercase state", filter: "state=running", want: []string{"task-1"}},
		{name: "uppercase state is case-insensitive", filter: "state=RUNNING", want: []string{"task-1"}},
		{name: "mixed-case state", filter: "state=Stopped", want: []string{"task-2"}},
		{name: "exact node flag", node: "node-1abc", want: []string{"task-1"}},
		{name: "node flag is exact, not a prefix", node: "node-1", want: nil},
		{name: "node filter is a prefix match", filter: "node=node-2", want: []string{"task-2"}},
		{name: "service ID", serviceID: "svc-1", want: []string{"task-1"}},
		{name: "service name", serviceID: "db", want: []string{"task-2"}},
		{name: "unknown service", serviceID: "cache", want: nil},
		{name: "all is a no-op", all: true, want: []string{"task-1", "task-2"}},
		{name: "all does not disable a filter", filter: "state=running", all: true, want: []string{"task-1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ids []string
			for _, task := range filterV1Tasks(tasks, tt.filter, tt.node, tt.serviceID, tt.all) {
				ids = append(ids, task.ID)
			}
			assert.Equal(t, tt.want, ids)
		})
	}
}

// TestLegacyTaskListJSONEmptyIsArray pins the one legacy shape change in this
// slice: an empty result in --format json is a parseable [] instead of prose, so
// scripts do not have to special-case a healthy empty cluster.
func TestLegacyTaskListJSONEmptyIsArray(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, listTasksWithClient(
		context.Background(), &fakeTaskClient{}, &buf, "json", "", false, false, false, "", "",
		derivedHealthOptions(),
	))
	assert.JSONEq(t, "[]", buf.String())
}

// TestLegacyTaskListTableEmptyKeepsProse keeps the human-facing message as it
// was: the fix applies to JSON only.
func TestLegacyTaskListTableEmptyKeepsProse(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, listTasksWithClient(
		context.Background(), &fakeTaskClient{}, &buf, "table", "", false, false, false, "", "",
		derivedHealthOptions(),
	))
	assert.Contains(t, buf.String(), "No tasks found")
}

// TestTaskListModeValidation proves every -o rejection happens before a client
// is built: each case must return CodeUsage, which the command cannot produce
// after `listTasksContract` dials.
func TestTaskListModeValidation(t *testing.T) {
	tests := []struct {
		name   string
		format string
		setup  func(t *testing.T, cmd *cobra.Command)
	}{
		{name: "ndjson is unsupported", format: "ndjson"},
		{
			name:   "json with quiet",
			format: "json",
			setup: func(t *testing.T, cmd *cobra.Command) {
				require.NoError(t, cmd.Flags().Set("quiet", "true"))
			},
		},
		{
			name:   "name with quiet",
			format: "name",
			setup: func(t *testing.T, cmd *cobra.Command) {
				require.NoError(t, cmd.Flags().Set("quiet", "true"))
			},
		},
		{
			name:   "json with health-port",
			format: "json",
			setup: func(t *testing.T, cmd *cobra.Command) {
				require.NoError(t, cmd.Flags().Set("health-port", "9000"))
			},
		},
		{
			name:   "json with legacy format flag",
			format: "json",
			setup: func(t *testing.T, cmd *cobra.Command) {
				require.NoError(t, cmd.Flags().Set("format", "json"))
			},
		},
		// -o table with an explicit --format is a conflict too: it matches
		// `service ls`, where the two flags are mutually exclusive in every
		// mode, and it must be rejected before the legacy path dials.
		{
			name:   "table with legacy format flag",
			format: "table",
			setup: func(t *testing.T, cmd *cobra.Command) {
				require.NoError(t, cmd.Flags().Set("format", "json"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := outputFormat
			outputFormat = tt.format
			t.Cleanup(func() { outputFormat = prev })

			cmd := newTaskListCommand()
			if tt.setup != nil {
				tt.setup(t, cmd)
			}

			err := cmd.RunE(cmd, nil)
			require.Error(t, err)

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, output.CodeUsage, typed.Code)
		})
	}
}
