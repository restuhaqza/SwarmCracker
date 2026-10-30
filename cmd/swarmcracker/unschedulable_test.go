package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unschedulableService builds a service whose only placement constraint is
// constraint. SwarmKit stores the strings verbatim on the task spec.
func unschedulableService(id, name, constraint string) *api.Service {
	svc := &api.Service{
		ID:   id,
		Spec: api.ServiceSpec{Annotations: api.Annotations{Name: name}},
	}
	svc.Spec.Task.Placement = &api.Placement{Constraints: []string{constraint}}
	return svc
}

// pendingTask builds a task the scheduler has examined and refused: it is still
// PENDING, carries no node, and its Status.Err holds the scheduler's verdict.
func pendingTask(id, serviceID, reason string) *api.Task {
	task := plainTask(id, api.TaskStatePending)
	task.ServiceID = serviceID
	task.NodeID = ""
	task.Status.Err = reason
	return task
}

// hintNode builds a node with a hostname, an observed role and node labels.
// The fields mirror what the constraint matcher reads: node.hostname comes from
// the description, node.role from the observed role, node.labels.* from the
// spec annotations.
func hintNode(id, hostname string, role api.NodeRole, labels map[string]string) *api.Node {
	return &api.Node{
		ID:          id,
		Role:        role,
		Description: &api.NodeDescription{Hostname: hostname},
		Spec:        api.NodeSpec{Annotations: api.Annotations{Labels: labels}},
	}
}

// schedulableNodes is the cluster the diagnostics must describe: two nodes that
// both report disk=hdd (so node.labels.disk==ssd cannot match), plus one label
// that only the second node carries.
func schedulableNodes() []*api.Node {
	return []*api.Node{
		hintNode("node-1", "sc-lab-node1", api.NodeRoleManager, map[string]string{"disk": "hdd"}),
		hintNode("node-2", "sc-lab-node2", api.NodeRoleWorker, map[string]string{"disk": "hdd", "rack": "r1"}),
	}
}

// TestServicePSUnschedulableHint is the reproduction: a service whose constraint
// matches no node must say so, and the ERROR column must carry the scheduler's
// reason. Before this change the table showed a PENDING task and nothing that
// named the constraint (SwarmKit's own Err says only "scheduling constraints not
// satisfied on 2 nodes").
func TestServicePSUnschedulableHint(t *testing.T) {
	const reason = "no suitable node (scheduling constraints not satisfied on 2 nodes)"

	client := &fakeServiceTaskClient{
		services: []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
		tasks: []*api.Task{
			pendingTask("task-pending-0000000000", "svc-1", reason),
			pendingTask("task-pending-0000000001", "svc-1", reason),
		},
		nodes: schedulableNodes(),
	}

	var buf bytes.Buffer
	require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "table", "", false, false, derivedHealthOptions()))

	out := buf.String()

	// The ERROR column carries the SwarmKit reason for a pending task. It is the
	// column's width that hides the useful part ("...scheduling constrai..."),
	// which is exactly why the trailing note names the constraint instead.
	row := taskRow(t, strings.Split(out, "\n"), "task-pending")
	assert.Contains(t, row, "no suitable node", "the ERROR column must carry the scheduler's status error")
	assert.Contains(t, row, truncateMiddle(reason, taskErrorReasonWidth),
		"the ERROR column must carry the scheduler's reason, ellipsised to the column width")

	// The trailing hint names the constraint and the labels the cluster reports.
	assert.Contains(t, out, `no task is running: constraint "node.labels.disk==ssd" matches no node`)
	assert.Contains(t, out,
		"(available node labels: disk=hdd, hostname=sc-lab-node1, hostname=sc-lab-node2, rack=r1, role=manager, role=worker)")

	// The hint is one line, after the total, and the table keeps its shape.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := lines[len(lines)-1]
	assert.True(t, strings.HasPrefix(last, "no task is running:"), "the hint must be the last line, got %q", last)
	assert.Equal(t, 1, strings.Count(out, "no task is running:"), "the hint must be printed once")
}

// TestServicePSUnschedulableHintExitsClean documents the ruling that the hint is
// a diagnostic, not an error path: the command returns nil.
func TestServicePSUnschedulableHintExitsClean(t *testing.T) {
	client := &fakeServiceTaskClient{
		services: []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
		tasks:    []*api.Task{pendingTask("task-pending-0000000000", "svc-1", "no suitable node")},
		nodes:    schedulableNodes(),
	}

	var buf bytes.Buffer
	err := listServiceTasksWithClient(context.Background(), client, &buf, "web", "table", "", false, false, derivedHealthOptions())
	require.NoError(t, err, "an unschedulable service must not fail the command")
	assert.Contains(t, buf.String(), "no task is running:")
}

// TestServicePSRunningServicePrintsNoHint is the healthy case: as soon as one
// task is running, the table is unchanged and no hint is printed.
func TestServicePSRunningServicePrintsNoHint(t *testing.T) {
	tests := []struct {
		name  string
		tasks []*api.Task
	}{
		{
			name: "one running task",
			tasks: []*api.Task{
				pendingTask("task-pending-0000000000", "svc-1", "no suitable node"),
				func() *api.Task {
					running := plainTask("task-running-0000000000", api.TaskStateRunning)
					running.ServiceID = "svc-1"
					return running
				}(),
			},
		},
		{
			name: "a task placed but not yet running",
			tasks: []*api.Task{
				func() *api.Task {
					starting := plainTask("task-starting-000000000", api.TaskStateStarting)
					starting.ServiceID = "svc-1"
					return starting
				}(),
			},
		},
		{
			name: "unplaced tasks the scheduler has not refused yet",
			tasks: []*api.Task{
				func() *api.Task {
					fresh := plainTask("task-new-00000000000000", api.TaskStateNew)
					fresh.ServiceID = "svc-1"
					fresh.NodeID = ""
					return fresh
				}(),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeServiceTaskClient{
				services: []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
				tasks:    tc.tasks,
				nodes:    schedulableNodes(),
			}

			var buf bytes.Buffer
			require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "table", "", false, false, derivedHealthOptions()))

			assert.NotContains(t, buf.String(), "no task is running:")
			assert.NotContains(t, buf.String(), "available node labels")
		})
	}
}

// TestTaskLsUnschedulableHint proves `task ls` shares the diagnostic, because it
// shares the row builder.
func TestTaskLsUnschedulableHint(t *testing.T) {
	const reason = "no suitable node (scheduling constraints not satisfied on 2 nodes)"

	client := &fakeTaskClient{
		services: []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
		tasks:    []*api.Task{pendingTask("task-pending-0000000000", "svc-1", reason)},
		nodes:    schedulableNodes(),
	}

	var buf bytes.Buffer
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "table", "", false, false, false, "", "", derivedHealthOptions()))

	out := buf.String()
	assert.Contains(t, taskRow(t, strings.Split(out, "\n"), "task-pending-0000000000"), "no suitable node")
	assert.Contains(t, out, `no task is running: constraint "node.labels.disk==ssd" matches no node`)
	assert.Contains(t, out, "disk=hdd, hostname=sc-lab-node1, hostname=sc-lab-node2, rack=r1, role=manager, role=worker")
}

// TestTaskLsRunningServicePrintsNoHint pins the same unchanged-table rule for
// `task ls`.
func TestTaskLsRunningServicePrintsNoHint(t *testing.T) {
	client := &fakeTaskClient{
		services: []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
		tasks: []*api.Task{
			func() *api.Task {
				running := plainTask("task-running-0000000000", api.TaskStateRunning)
				running.ServiceID = "svc-1"
				return running
			}(),
		},
		nodes: schedulableNodes(),
	}

	var buf bytes.Buffer
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "table", "", false, false, false, "", "", derivedHealthOptions()))

	assert.NotContains(t, buf.String(), "no task is running:")
}

// TestUnschedulableNote pins the message itself: every unsatisfiable constraint
// is named (never just the first — the command cannot know which one failed),
// and the labels are the ones the cluster reports.
func TestUnschedulableNote(t *testing.T) {
	tests := []struct {
		name        string
		constraints []string
		labels      []string
		want        string
	}{
		{
			name:        "one constraint",
			constraints: []string{"node.labels.disk==ssd"},
			labels:      []string{"hostname=sc-lab-node1", "role=manager"},
			want:        `no task is running: constraint "node.labels.disk==ssd" matches no node (available node labels: hostname=sc-lab-node1, role=manager)`,
		},
		{
			name:        "several constraints are all named",
			constraints: []string{"node.labels.disk==ssd", "node.role!=worker"},
			labels:      []string{"role=manager"},
			want:        `no task is running: constraints "node.labels.disk==ssd"; "node.role!=worker" match no node (available node labels: role=manager)`,
		},
		{
			name:        "a cluster with no labels",
			constraints: []string{"node.labels.disk==ssd"},
			labels:      nil,
			want:        `no task is running: constraint "node.labels.disk==ssd" matches no node (available node labels: none)`,
		},
		{
			name:        "no constraint to name",
			constraints: nil,
			labels:      []string{"role=manager"},
			want:        "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, unschedulableNote(tc.constraints, tc.labels))
		})
	}
}

// TestAvailableNodeLabels pins the rendering of the label hint: hostname and
// role are the keys a constraint can match without a label, every node label is
// included, and the result is deduplicated and sorted so two nodes that report
// the same label print it once.
func TestAvailableNodeLabels(t *testing.T) {
	tests := []struct {
		name  string
		nodes []*api.Node
		want  []string
	}{
		{
			name:  "two nodes",
			nodes: schedulableNodes(),
			want:  []string{"disk=hdd", "hostname=sc-lab-node1", "hostname=sc-lab-node2", "rack=r1", "role=manager", "role=worker"},
		},
		{
			name:  "no nodes",
			nodes: nil,
			want:  nil,
		},
		{
			name:  "a node with no description and no labels",
			nodes: []*api.Node{{ID: "node-1", Role: api.NodeRoleWorker}},
			want:  []string{"role=worker"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, availableNodeLabels(tc.nodes))
		})
	}
}

// TestPendingConstraints pins the constraint source: the spec of the service the
// task belongs to, deduplicated and sorted so the note is deterministic.
func TestPendingConstraints(t *testing.T) {
	services := []*api.Service{
		unschedulableService("svc-1", "web", "node.labels.disk==ssd"),
		unschedulableService("svc-2", "api", "node.role==manager"),
	}
	services[0].Spec.Task.Placement.Constraints = []string{"node.labels.disk==ssd", "node.role==manager"}
	services[1].Spec.Task.Placement.Constraints = []string{"node.role==manager", "node.labels.disk==ssd"}

	tasks := []*api.Task{
		pendingTask("task-1", "svc-1", "no suitable node"),
		pendingTask("task-2", "svc-2", "no suitable node"),
	}

	assert.Equal(t, []string{"node.labels.disk==ssd", "node.role==manager"}, pendingConstraints(tasks, services))

	// A task whose service is gone names no constraint, because there is none
	// left to name.
	assert.Empty(t, pendingConstraints([]*api.Task{pendingTask("task-3", "svc-gone", "no suitable node")}, services))
}
