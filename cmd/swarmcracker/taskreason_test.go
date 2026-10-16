package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

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

// TestTaskErrorReason is the direct table test for the helper: only Status.Err
// is the operator-facing reason. Status.Message is a routine-transition note
// (SwarmKit writes "prepared"/"started" there on the success path) so a task
// carrying only Message yields "" — re-adding the fallback would print
// "started" for every healthy RUNNING task. A task with neither (or a nil task)
// yields no reason at all.
func TestTaskErrorReason(t *testing.T) {
	tests := []struct {
		name string
		task *api.Task
		want string
	}{
		{"nil task", nil, ""},
		{"neither set", taskReasonTask(api.TaskStateRunning, "", ""), ""},
		{"err only", taskReasonTask(api.TaskStatePending, "", "no suitable node (1 node not available)"), "no suitable node (1 node not available)"},
		{"message only is not a reason", taskReasonTask(api.TaskStatePreparing, "prepared", ""), ""},
		{"routine started note is not a reason", taskReasonTask(api.TaskStateRunning, "started", ""), ""},
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
// taskErrorReasonWidth so it cannot destroy the table layout. Truncation counts
// characters, never bytes, so a non-ASCII reason stays valid UTF-8.
func TestTaskErrorColumn(t *testing.T) {
	// 30 CJK runes (90 bytes): well past the 40-byte mark the old byte-based
	// truncation would have sliced through, yet only 30 characters wide.
	cjk := strings.Repeat("节点不可用", 6)

	tests := []struct {
		name string
		task *api.Task
		want string
	}{
		{"neither set", taskReasonTask(api.TaskStateRunning, "", ""), ""},
		{"message only", taskReasonTask(api.TaskStatePreparing, "prepared", ""), ""},
		{"whitespace only", taskReasonTask(api.TaskStateRunning, "", " \n\t "), ""},
		{"newline collapsed", taskReasonTask(api.TaskStatePreparing, "", "no suitable node\nno free resources"), "no suitable node no free resources"},
		{"exactly the width", taskReasonTask(api.TaskStatePending, "", strings.Repeat("a", taskErrorReasonWidth)), strings.Repeat("a", taskErrorReasonWidth)},
		{"over the width", taskReasonTask(api.TaskStatePending, "", strings.Repeat("b", taskErrorReasonWidth+1)), strings.Repeat("b", taskErrorReasonWidth-3) + "..."},
		{
			"non-ascii fits the width in characters, not bytes",
			taskReasonTask(api.TaskStatePending, "", cjk),
			strings.Repeat("节点不可用", 6),
		},
		{
			"non-ascii over the width is cut between runes",
			taskReasonTask(api.TaskStatePending, "", strings.Repeat("节点不可用", 9)),
			strings.Repeat("节点不可用", 7) + "节点" + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := taskErrorColumn(tt.task)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got), "the ERROR cell must stay valid UTF-8")
			assert.LessOrEqual(t, utf8.RuneCountInString(got), taskErrorReasonWidth,
				"the ERROR cell must never exceed its width in characters")
		})
	}
}

// TestTruncateMiddleRuneSafe pins the shared helper the ERROR column reuses:
// the cut falls on a rune boundary (never emitting a broken encoding) and the
// result is at most max characters wide.
func TestTruncateMiddleRuneSafe(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"ascii unchanged", "nginx:alpine", 23, "nginx:alpine"},
		{"ascii cut", "abcdefghij", 7, "abcd..."},
		{"non-ascii cut on a rune boundary", "节点不可用节点", 5, "节点..."},
		{"non-ascii exactly the width", "节点不可用", 5, "节点不可用"},
		{"tiny width", "节点不可用", 2, "节点"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateMiddle(tt.in, tt.max)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got), "truncateMiddle must emit valid UTF-8")
			assert.LessOrEqual(t, utf8.RuneCountInString(got), tt.max)
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
	stuck.Status.Err = "image pull failed\nwill retry"
	stuck.NodeID = "node-b"
	stuck.ServiceID = "svc-1"

	// A routine transition note in Message is not a reason: only Status.Err is.
	noteOnly := plainTask("task-note-000000000000000", api.TaskStateRunning)
	noteOnly.Status.Message = "started"
	noteOnly.NodeID = "node-d"
	noteOnly.ServiceID = "svc-1"

	running := plainTask("task-plain-000000000000000", api.TaskStateRunning)
	running.NodeID = "node-c"
	running.ServiceID = "svc-1"

	client := &fakeTaskClient{tasks: []*api.Task{blocked, stuck, noteOnly, running}}

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

	// A multi-line Err is rendered too, with its newline collapsed.
	stuckRow := taskRow(t, lines, stuck.ID)
	assert.Equal(t, "image pull failed will retry", stuckRow[len(prefixWithImage(stuck, "PREPARING", "node-b", "svc-1")):])

	// A Message-only task (a routine note) prints no reason: the row is exactly
	// the shape it had before ERROR existed.
	noteRow := taskRow(t, lines, noteOnly.ID)
	assert.Equal(t, fmt.Sprintf("%-20s %-20s %-12s %-12s %-12s %s", noteOnly.ID, "svc-1", "RUNNING", "-", "node-d", "nginx:alpine"), noteRow)

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
