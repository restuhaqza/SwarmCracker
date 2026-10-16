package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// taskReasonTask builds a plain task whose Status carries message/err, so the
// reason helpers can be exercised without a cluster.
func taskReasonTask(state api.TaskState, message, errMsg string) *api.Task {
	task := plainTask("task-reason-00000000000", state)
	task.Status.Message = message
	task.Status.Err = errMsg
	return task
}

// TestTaskErrorReason is the direct table test for the helper: Err is the
// operator-facing reason and wins over Message; Message is the fallback; a task
// carrying neither (or a nil task) yields no reason at all.
func TestTaskErrorReason(t *testing.T) {
	tests := []struct {
		name string
		task *api.Task
		want string
	}{
		{"nil task", nil, ""},
		{"neither set", taskReasonTask(api.TaskStateRunning, "", ""), ""},
		{"err only", taskReasonTask(api.TaskStatePending, "", "no suitable node (1 node not available)"), "no suitable node (1 node not available)"},
		{"message only", taskReasonTask(api.TaskStatePreparing, "prepared", ""), "prepared"},
		{"err wins over message", taskReasonTask(api.TaskStateRejected, "prepared", "no suitable node (1 node not available)"), "no suitable node (1 node not available)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, taskErrorReason(tt.task))
		})
	}
}

// TestTaskErrorColumn verifies the cell rendering: whitespace collapses so a
// multi-line reason stays on one row, and an over-long reason is truncated to
// taskErrorReasonWidth so it cannot destroy the table layout.
func TestTaskErrorColumn(t *testing.T) {
	tests := []struct {
		name string
		task *api.Task
		want string
	}{
		{"neither set", taskReasonTask(api.TaskStateRunning, "", ""), ""},
		{"whitespace only", taskReasonTask(api.TaskStateRunning, " \n\t ", ""), ""},
		{"newline collapsed", taskReasonTask(api.TaskStatePreparing, "prepared\nwaiting for a restart slot", ""), "prepared waiting for a restart slot"},
		{"exactly the width", taskReasonTask(api.TaskStatePending, "", strings.Repeat("a", taskErrorReasonWidth)), strings.Repeat("a", taskErrorReasonWidth)},
		{"over the width", taskReasonTask(api.TaskStatePending, "", strings.Repeat("b", taskErrorReasonWidth+1)), strings.Repeat("b", taskErrorReasonWidth-3) + "..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := taskErrorColumn(tt.task)
			assert.Equal(t, tt.want, got)
			assert.LessOrEqual(t, len(got), taskErrorReasonWidth, "the ERROR cell must never exceed its width")
		})
	}
}

// taskRow returns the one rendered row starting with id.
func taskRow(t *testing.T, lines []string, id string) string {
	t.Helper()
	for _, line := range lines {
		if strings.HasPrefix(line, id+" ") {
			return line
		}
	}
	t.Fatalf("no row for task %q in:\n%s", id, strings.Join(lines, "\n"))
	return ""
}

// TestListTasksErrorColumn verifies `task ls` appends the reason as the last
// column while every existing column keeps its order — the project's lab scripts
// parse STATUS (field 3) and HEALTH (field 4) positionally — and that a task with
// no reason gains no trailing column at all.
func TestListTasksErrorColumn(t *testing.T) {
	blocked := plainTask("task-blocked-00000000000", api.TaskStatePending)
	blocked.Status.Err = "no suitable node (1 node not available)"
	blocked.NodeID = "node-a"
	blocked.ServiceID = "svc-1"

	stuck := plainTask("task-stuck-0000000000000", api.TaskStatePreparing)
	stuck.Status.Message = "prepared\nwaiting for a restart slot"
	stuck.NodeID = "node-b"
	stuck.ServiceID = "svc-1"

	running := plainTask("task-plain-000000000000000", api.TaskStateRunning)
	running.NodeID = "node-c"
	running.ServiceID = "svc-1"

	client := &fakeTaskClient{tasks: []*api.Task{blocked, stuck, running}}

	var buf bytes.Buffer
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "table", "", false, false, false, "", "", derivedHealthOptions()))
	lines := strings.Split(buf.String(), "\n")

	assert.Equal(t, []string{"ID", "SERVICE", "STATUS", "HEALTH", "NODE", "IMAGE", "ERROR"}, strings.Fields(lines[0]),
		"ERROR must be the last column")
	assert.Equal(t, strings.Repeat("-", len(lines[0])), lines[1], "the rule spans the header it follows")

	// The blocked task carries its reason verbatim, after the unchanged columns.
	row := taskRow(t, lines, blocked.ID)
	prefix := fmt.Sprintf("%-20s %-20s %-12s %-12s %-12s %s ", blocked.ID, "svc-1", "PENDING", "-", "node-a", "nginx:alpine")
	require.True(t, strings.HasPrefix(row, prefix), "row %q must keep its existing columns in order", row)
	assert.Equal(t, "no suitable node (1 node not available)", row[len(prefix):])

	// The Message fallback is rendered too, with its newline collapsed.
	stuckRow := taskRow(t, lines, stuck.ID)
	assert.Equal(t, "prepared waiting for a restart slot", stuckRow[len(prefixWithImage(stuck, "PREPARING", "node-b", "svc-1")):])

	// No reason: the row is exactly the shape it had before ERROR existed.
	runningRow := taskRow(t, lines, running.ID)
	assert.Equal(t, fmt.Sprintf("%-20s %-20s %-12s %-12s %-12s %s", running.ID, "svc-1", "RUNNING", "-", "node-c", "nginx:alpine"), runningRow)
	assert.Len(t, strings.Fields(runningRow), 6, "a task with no reason must not gain a trailing column")
}

// prefixWithImage rebuilds the fixed task ls columns up to and including IMAGE
// and the separator that precedes the ERROR cell.
func prefixWithImage(task *api.Task, state, nodeID, svcID string) string {
	image := ""
	if task.Spec.GetContainer() != nil {
		image = task.Spec.GetContainer().Image
	}
	return fmt.Sprintf("%-20s %-20s %-12s %-12s %-12s %s ", task.ID, svcID, state, taskHealth(task), nodeID, image)
}

// TestListServiceTasksErrorColumn verifies `service ps` appends the reason last
// and leaves STATUS and HEALTH (the fields the lab scripts read) in position.
func TestListServiceTasksErrorColumn(t *testing.T) {
	blocked := plainTask("task-blocked-00000000000", api.TaskStatePending)
	blocked.Status.Err = "no suitable node (1 node not available)"
	blocked.NodeID = "node-a"
	blocked.ServiceID = "svc-1"

	client := &fakeServiceTaskClient{
		services: []*api.Service{{ID: "svc-1", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "web"}}}},
		tasks:    []*api.Task{blocked},
	}

	var buf bytes.Buffer
	require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "table", "", false, false, derivedHealthOptions()))
	lines := strings.Split(buf.String(), "\n")

	assert.Equal(t, []string{"ID", "STATUS", "HEALTH", "NODE", "IMAGE", "ERROR"}, strings.Fields(lines[0]),
		"ERROR must be the last column")
	assert.Equal(t, strings.Repeat("-", len(lines[0])), lines[1], "the rule spans the header it follows")

	// service ps truncates the task ID to 12 characters unless --no-trunc is set.
	row := taskRow(t, lines, "task-blocked")
	prefix := fmt.Sprintf("%-20s %-12s %-12s %-20s %s ", "task-blocked", "PENDING", "-", "node-a", "nginx:alpine")
	require.True(t, strings.HasPrefix(row, prefix), "row %q must keep its existing columns in order", row)
	assert.Equal(t, "no suitable node (1 node not available)", row[len(prefix):])

	fields := strings.Fields(row)
	require.GreaterOrEqual(t, len(fields), 5)
	assert.Equal(t, "PENDING", fields[1], "STATUS must stay field 2")
	assert.Equal(t, "-", fields[2], "HEALTH must stay field 3")
}

// TestListTasksJSONStillCarriesErrorReason documents that the JSON path needs no
// change: redact.Task copies the whole task, so status.err/status.message were
// already emitted, and this change invents no new field.
func TestListTasksJSONStillCarriesErrorReason(t *testing.T) {
	blocked := plainTask("task-blocked-00000000000", api.TaskStatePending)
	blocked.Status.Err = "no suitable node (1 node not available)"
	client := &fakeTaskClient{tasks: []*api.Task{blocked}}

	var buf bytes.Buffer
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "json", "", false, false, false, "", "", derivedHealthOptions()))

	var got []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got, 1)
	status, ok := got[0]["status"].(map[string]any)
	require.True(t, ok, "the task status must still be emitted")
	assert.Equal(t, "no suitable node (1 node not available)", status["err"])
	_, hasReason := got[0]["reason"]
	assert.False(t, hasReason, "the JSON field set must not gain a reason key")
	_, hasError := got[0]["error"]
	assert.False(t, hasError, "the JSON field set must not gain an error key")
}
