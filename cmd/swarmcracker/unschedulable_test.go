package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// constraintFilterPhrase is SwarmKit's own wording for a constraint refusal, as
// the reproduction in this file carries it. The diagnostic may only claim a
// constraint is the cause when the task error contains it.
const constraintFilterPhrase = "scheduling constraints not satisfied"

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

// serviceTask builds a task of service svc in state, with no scheduler error:
// the shape every row of an ordinary listing has.
func serviceTask(id, serviceID string, state api.TaskState) *api.Task {
	task := plainTask(id, state)
	task.ServiceID = serviceID
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

// nodesReportingDiskHDD is the cluster the diagnostics must describe: two nodes
// that both report disk=hdd (so node.labels.disk==ssd cannot match), plus one
// label that only the second node carries.
func nodesReportingDiskHDD() []*api.Node {
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
	const reason = "no suitable node (" + constraintFilterPhrase + " on 2 nodes)"

	client := &fakeServiceTaskClient{
		services: []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
		tasks: []*api.Task{
			pendingTask("task-pending-0000000000", "svc-1", reason),
			pendingTask("task-pending-0000000001", "svc-1", reason),
		},
		nodes: nodesReportingDiskHDD(),
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
		nodes:    nodesReportingDiskHDD(),
	}

	var buf bytes.Buffer
	err := listServiceTasksWithClient(context.Background(), client, &buf, "web", "table", "", false, false, derivedHealthOptions())
	require.NoError(t, err, "an unschedulable service must not fail the command")
	assert.Contains(t, buf.String(), "no task is running:")
}

// TestServicePSTerminalTaskDoesNotSuppressHint is the I1 ruling: `service ps`
// lists every state, so a service that ran before (SHUTDOWN/FAILED rows) and now
// cannot be placed must still be diagnosed. The concrete sequence: scale 0,
// add an unsatisfiable constraint, scale 2 — the listing is then terminal rows
// plus the new PENDING ones, and the note is about the latter.
func TestServicePSTerminalTaskDoesNotSuppressHint(t *testing.T) {
	const reason = "no suitable node (" + constraintFilterPhrase + " on 2 nodes)"

	tests := []struct {
		name      string
		terminal  api.TaskState
		terminalN int
	}{
		{name: "a shut down task from before the scale-down", terminal: api.TaskStateShutdown, terminalN: 2},
		{name: "a failed task", terminal: api.TaskStateFailed, terminalN: 1},
		{name: "a rejected task", terminal: api.TaskStateRejected, terminalN: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tasks := make([]*api.Task, 0, tc.terminalN+1)
			for i := 0; i < tc.terminalN; i++ {
				tasks = append(tasks, serviceTask("task-terminal-000000000", "svc-1", tc.terminal))
			}
			tasks = append(tasks, pendingTask("task-pending-0000000000", "svc-1", reason))

			client := &fakeServiceTaskClient{
				services: []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
				tasks:    tasks,
				nodes:    nodesReportingDiskHDD(),
			}

			var buf bytes.Buffer
			require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "table", "", false, false, derivedHealthOptions()))

			assert.Contains(t, buf.String(), `no task is running: constraint "node.labels.disk==ssd" matches no node`,
				"a terminal row must not suppress the diagnosis of the pending one")
		})
	}
}

// TestServicePSResourceRefusalDoesNotBlameConstraint is the I2 ruling: a
// resource refusal on a constraint-carrying service must not be reported as the
// constraint's fault. SwarmKit joins every failing filter into Status.Err, so
// the note may only claim the constraint when the error itself carries the
// constraint filter's explanation.
func TestServicePSResourceRefusalDoesNotBlameConstraint(t *testing.T) {
	const reason = "no suitable node (insufficient resources on 1 node)"

	client := &fakeServiceTaskClient{
		services: []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
		tasks:    []*api.Task{pendingTask("task-pending-0000000000", "svc-1", reason)},
		nodes:    nodesReportingDiskHDD(),
	}

	var buf bytes.Buffer
	require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "table", "", false, false, derivedHealthOptions()))

	out := buf.String()
	assert.NotContains(t, out, "matches no node", "the constraint is not the cause and must not be named as one")
	assert.NotContains(t, out, "match no node")
	assert.Contains(t, out, `no task is running: scheduler reported "`+reason+`"`,
		"the note must quote the scheduler's own reason")
	assert.Contains(t, out, `declared constraints "node.labels.disk==ssd" may not be the cause`,
		"the declared constraint is listed, but not as the cause")
	assert.Contains(t, out, "available node labels: disk=hdd", "the cluster labels are still the evidence")
}

// TestServicePSJSONFormatPrintsNoHint pins that the machine-readable path gains
// no prose: `--format json` on an unschedulable service stays parseable JSON.
func TestServicePSJSONFormatPrintsNoHint(t *testing.T) {
	client := &fakeServiceTaskClient{
		services: []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
		tasks:    []*api.Task{pendingTask("task-pending-0000000000", "svc-1", "no suitable node")},
		nodes:    nodesReportingDiskHDD(),
	}

	var buf bytes.Buffer
	require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "json", "", false, false, derivedHealthOptions()))

	assert.True(t, json.Valid(buf.Bytes()), "the JSON output must stay valid, got %q", buf.String())
	assert.NotContains(t, buf.String(), "no task is running:")
	assert.NotContains(t, buf.String(), "available node labels")
}

// TestServicePSNoteDegradesWhenListsFail is the ruling that the note is never an
// error: if the service or the node list fails, the command prints the table it
// already has, no note, and still returns nil.
func TestServicePSNoteDegradesWhenListsFail(t *testing.T) {
	tests := []struct {
		name   string
		client *fakeServiceTaskClient
	}{
		{
			name: "the service list fails",
			client: &fakeServiceTaskClient{
				listServicesErr: errors.New("connection reset"),
				tasks:           []*api.Task{pendingTask("task-pending-0000000000", "svc-1", "no suitable node")},
				nodes:           nodesReportingDiskHDD(),
				services:        []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
			},
		},
		{
			name: "the node list fails",
			client: &fakeServiceTaskClient{
				listNodesErr: errors.New("connection reset"),
				tasks:        []*api.Task{pendingTask("task-pending-0000000000", "svc-1", "no suitable node")},
				nodes:        nodesReportingDiskHDD(),
				services:     []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := listServiceTasksWithClient(context.Background(), tc.client, &buf, "web", "table", "", false, false, derivedHealthOptions())
			require.NoError(t, err, "a failed diagnostic read must not fail the command")
			assert.NotContains(t, buf.String(), "no task is running:")
			assert.Contains(t, buf.String(), "Total: 1 task(s)", "the table must still be printed")
		})
	}
}

// TestServicePSScaledToZeroPrintsNoHint pins the empty listing: a service with
// no task at all is not an unschedulable one, so it must not be diagnosed.
func TestServicePSScaledToZeroPrintsNoHint(t *testing.T) {
	client := &fakeServiceTaskClient{
		services: []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
		nodes:    nodesReportingDiskHDD(),
	}

	var buf bytes.Buffer
	require.NoError(t, listServiceTasksWithClient(context.Background(), client, &buf, "web", "table", "", false, false, derivedHealthOptions()))

	assert.Contains(t, buf.String(), "No tasks found for service web")
	assert.NotContains(t, buf.String(), "no task is running:")
}

// TestServicePSRunningServicePrintsNoHint is the healthy case: as soon as one
// task is placed, the table is unchanged and no hint is printed.
func TestServicePSRunningServicePrintsNoHint(t *testing.T) {
	tests := []struct {
		name  string
		tasks []*api.Task
	}{
		{
			name: "one running task",
			tasks: []*api.Task{
				pendingTask("task-pending-0000000000", "svc-1", "no suitable node"),
				serviceTask("task-running-0000000000", "svc-1", api.TaskStateRunning),
			},
		},
		{
			name: "a task placed but not yet running",
			tasks: []*api.Task{
				serviceTask("task-starting-000000000", "svc-1", api.TaskStateStarting),
			},
		},
		{
			name: "a task assigned to a node",
			tasks: []*api.Task{
				pendingTask("task-pending-0000000000", "svc-1", "no suitable node"),
				serviceTask("task-assigned-000000000", "svc-1", api.TaskStateAssigned),
			},
		},
		{
			name: "a task preparing on a node",
			tasks: []*api.Task{
				pendingTask("task-pending-0000000000", "svc-1", "no suitable node"),
				serviceTask("task-preparing-00000000", "svc-1", api.TaskStatePreparing),
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
				nodes:    nodesReportingDiskHDD(),
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
	const reason = "no suitable node (" + constraintFilterPhrase + " on 2 nodes)"

	client := &fakeTaskClient{
		services: []*api.Service{unschedulableService("svc-1", "web", "node.labels.disk==ssd")},
		tasks:    []*api.Task{pendingTask("task-pending-0000000000", "svc-1", reason)},
		nodes:    nodesReportingDiskHDD(),
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
		tasks:    []*api.Task{serviceTask("task-running-0000000000", "svc-1", api.TaskStateRunning)},
		nodes:    nodesReportingDiskHDD(),
	}

	var buf bytes.Buffer
	require.NoError(t, listTasksWithClient(context.Background(), client, &buf, "table", "", false, false, false, "", "", derivedHealthOptions()))

	assert.NotContains(t, buf.String(), "no task is running:")
}

// TestPlacementRefused pins the gate itself: no task is placed (nothing
// ASSIGNED..RUNNING) and at least one NEW/PENDING task carries the scheduler's
// error. Terminal states and unrefused NEW tasks must not trigger it.
func TestPlacementRefused(t *testing.T) {
	tests := []struct {
		name  string
		tasks []*api.Task
		want  bool
	}{
		{
			name:  "refused pending tasks",
			tasks: []*api.Task{pendingTask("task-1", "svc-1", "no suitable node")},
			want:  true,
		},
		{
			name:  "a new task the scheduler has not examined",
			tasks: []*api.Task{serviceTask("task-1", "svc-1", api.TaskStateNew)},
			want:  false,
		},
		{
			name: "one running task suppresses the diagnosis",
			tasks: []*api.Task{
				pendingTask("task-1", "svc-1", "no suitable node"),
				serviceTask("task-2", "svc-1", api.TaskStateRunning),
			},
			want: false,
		},
		{
			name: "a task assigned to a node suppresses the diagnosis",
			tasks: []*api.Task{
				pendingTask("task-1", "svc-1", "no suitable node"),
				serviceTask("task-2", "svc-1", api.TaskStateAssigned),
			},
			want: false,
		},
		{
			name: "terminal rows do not suppress the diagnosis",
			tasks: []*api.Task{
				serviceTask("task-1", "svc-1", api.TaskStateShutdown),
				serviceTask("task-2", "svc-1", api.TaskStateFailed),
				pendingTask("task-3", "svc-1", "no suitable node"),
			},
			want: true,
		},
		{
			name:  "terminal rows alone are not a refusal",
			tasks: []*api.Task{serviceTask("task-1", "svc-1", api.TaskStateShutdown)},
			want:  false,
		},
		{
			name:  "no tasks at all",
			tasks: nil,
			want:  false,
		},
		{
			name:  "a nil task in the listing",
			tasks: []*api.Task{nil},
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, placementRefused(tc.tasks))
		})
	}
}

// TestRefusedReason pins which error the note quotes: the first unplaced task
// that carries one, with whitespace collapsed so a multi-line error stays on one
// line.
func TestRefusedReason(t *testing.T) {
	tests := []struct {
		name  string
		tasks []*api.Task
		want  string
	}{
		{
			name:  "the scheduler's verdict",
			tasks: []*api.Task{pendingTask("task-1", "svc-1", "no suitable node")},
			want:  "no suitable node",
		},
		{
			name: "a multi-line error is collapsed",
			tasks: []*api.Task{
				pendingTask("task-1", "svc-1", "no suitable node\n(insufficient resources on 1 node)"),
			},
			want: "no suitable node (insufficient resources on 1 node)",
		},
		{
			name: "a terminal row's error is not quoted",
			tasks: []*api.Task{
				func() *api.Task {
					failed := serviceTask("task-1", "svc-1", api.TaskStateFailed)
					failed.Status.Err = "healthcheck failed"
					return failed
				}(),
				pendingTask("task-2", "svc-1", "no suitable node"),
			},
			want: "no suitable node",
		},
		{
			name:  "nothing to quote",
			tasks: []*api.Task{nil},
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, refusedReason(tc.tasks))
		})
	}
}

// TestUnschedulableNote pins the message itself: the constraint is claimed only
// when the error carries the constraint filter's explanation, every declared
// constraint is named, and the labels are the ones the cluster reports.
func TestUnschedulableNote(t *testing.T) {
	const constraintReason = "no suitable node (" + constraintFilterPhrase + " on 2 nodes)"
	const resourceReason = "no suitable node (insufficient resources on 1 node)"

	tests := []struct {
		name        string
		reason      string
		constraints []string
		labels      []string
		want        string
	}{
		{
			name:        "one constraint the scheduler rejected",
			reason:      constraintReason,
			constraints: []string{"node.labels.disk==ssd"},
			labels:      []string{"hostname=sc-lab-node1", "role=manager"},
			want:        `no task is running: constraint "node.labels.disk==ssd" matches no node (available node labels: hostname=sc-lab-node1, role=manager)`,
		},
		{
			name:        "several constraints are all named",
			reason:      constraintReason,
			constraints: []string{"node.labels.disk==ssd", "node.role!=worker"},
			labels:      []string{"role=manager"},
			want:        `no task is running: constraints "node.labels.disk==ssd"; "node.role!=worker" match no node (available node labels: role=manager)`,
		},
		{
			name:        "a cluster with no labels",
			reason:      constraintReason,
			constraints: []string{"node.labels.disk==ssd"},
			labels:      nil,
			want:        `no task is running: constraint "node.labels.disk==ssd" matches no node (available node labels: none)`,
		},
		{
			name:        "a resource refusal on a constrained service never blames the constraint",
			reason:      resourceReason,
			constraints: []string{"node.labels.disk==ssd"},
			labels:      []string{"disk=hdd"},
			want: `no task is running: scheduler reported "` + resourceReason +
				`"; declared constraints "node.labels.disk==ssd" may not be the cause (available node labels: disk=hdd)`,
		},
		{
			name:        "a service with no constraint",
			reason:      resourceReason,
			constraints: nil,
			labels:      []string{"disk=hdd"},
			want:        `no task is running: scheduler reported "` + resourceReason + `" (the service declares no placement constraints)`,
		},
		{
			name:        "the constraint filter among other failures is evidence enough",
			reason:      "no suitable node (insufficient resources on 1 node; " + constraintFilterPhrase + " on 2 nodes)",
			constraints: []string{"node.labels.disk==ssd"},
			labels:      []string{"disk=hdd"},
			want:        `no task is running: constraint "node.labels.disk==ssd" matches no node (available node labels: disk=hdd)`,
		},
		{
			name:        "no evidence at all",
			reason:      "",
			constraints: []string{"node.labels.disk==ssd"},
			labels:      []string{"role=manager"},
			want:        "",
		},
		{
			name:        "no reason and no constraint",
			reason:      "",
			constraints: nil,
			labels:      []string{"role=manager"},
			want:        "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, unschedulableNote(tc.reason, tc.constraints, tc.labels))
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
			nodes: nodesReportingDiskHDD(),
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
		{
			name:  "a nil node in the listing",
			nodes: []*api.Node{nil},
			want:  nil,
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

	// A listing with no tasks, and one carrying nil entries, names nothing and
	// does not panic.
	assert.Empty(t, pendingConstraints(nil, services))
	assert.Empty(t, pendingConstraints([]*api.Task{nil}, append(services, nil)))
}
