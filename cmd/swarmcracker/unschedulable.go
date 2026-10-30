package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/moby/swarmkit/v2/api"
	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/client/legacy"
)

// Placement diagnostics.
//
// SwarmKit's scheduler records why it could not place a task in Status.Err, and
// the ERROR column prints it, but its own verdict is deliberately generic: the
// constraint filter explains an unsatisfiable constraint as "scheduling
// constraints not satisfied on N node(s)" (manager/scheduler/filter.go:246-251,
// Explain) and never names the constraint or the labels the cluster reports. An
// operator who wrote `--constraint node.labels.disk==ssd` therefore needs the
// service spec and the node labels to see that no node has disk=ssd.
//
// The note below is that second read. It is a diagnostic, never an error: it is
// printed after the table, it changes no exit code, and a listing that cannot
// assemble it prints nothing.

// constraintFilterExplanation is the phrase the constraint filter contributes to
// the scheduler's verdict. The scheduler joins the explanation of *every* failing
// filter with "; " (manager/scheduler/pipeline.go:84-103), so a task refused for
// resources carries "insufficient resources on N node(s)" while a task refused by
// a constraint carries this phrase. The note claims a constraint is the cause only
// when the task error contains it: the presence of a constraint on the service is
// not evidence that the constraint is what failed.
const constraintFilterExplanation = "scheduling constraints not satisfied"

// taskUnplaced reports whether a task is still waiting for the scheduler. NEW
// and PENDING are the only states with no placement decision behind them.
func taskUnplaced(task *api.Task) bool {
	if task == nil {
		return false
	}
	return task.Status.State == api.TaskStateNew || task.Status.State == api.TaskStatePending
}

// taskPlaced reports whether a node was found for the task, so the scheduler did
// not refuse it: everything from ASSIGNED to RUNNING. Terminal states are
// deliberately excluded — `service ps` lists every state, so a service that ran
// before (SHUTDOWN/FAILED rows) and cannot be placed now has terminal rows next
// to the new PENDING ones, and that history must not hide the diagnosis.
func taskPlaced(task *api.Task) bool {
	if task == nil {
		return false
	}
	switch task.Status.State {
	case api.TaskStateAssigned,
		api.TaskStateAccepted,
		api.TaskStatePreparing,
		api.TaskStateReady,
		api.TaskStateStarting,
		api.TaskStateRunning:
		return true
	}
	return false
}

// placementRefused reports whether a whole task listing is evidence that the
// scheduler refused the placement: no task is placed (nothing from ASSIGNED to
// RUNNING), and at least one NEW/PENDING task carries the scheduler's Status.Err.
// Requiring the error keeps a task that was created a moment ago (NEW, not looked
// at yet) from producing a verdict the scheduler never made.
func placementRefused(tasks []*api.Task) bool {
	refused := false
	for _, task := range tasks {
		if taskPlaced(task) {
			return false
		}
		if taskUnplaced(task) && task.Status.Err != "" {
			refused = true
		}
	}
	return refused
}

// refusedReason is the scheduler's verdict to quote: the first unplaced task that
// carries one, with runs of whitespace collapsed so a multi-line error stays on
// one line. Terminal rows are skipped — their error explains why they stopped,
// not why the new tasks cannot start.
func refusedReason(tasks []*api.Task) string {
	for _, task := range tasks {
		if taskUnplaced(task) && task.Status.Err != "" {
			return strings.Join(strings.Fields(task.Status.Err), " ")
		}
	}
	return ""
}

// pendingConstraints returns the placement constraints of the services the
// tasks belong to, deduplicated and sorted. A listing can span services
// (`task ls` does), so the caller gets them all rather than a guess at which one
// was refused: the scheduler evaluated every constraint at once and nothing here
// can tell which of them failed.
func pendingConstraints(tasks []*api.Task, services []*api.Service) []string {
	wanted := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		if task == nil {
			continue
		}
		wanted[task.ServiceID] = struct{}{}
	}

	seen := make(map[string]struct{})
	constraints := make([]string, 0, len(wanted))
	for _, service := range services {
		if service == nil {
			continue
		}
		if _, ok := wanted[service.ID]; !ok {
			continue
		}
		if service.Spec.Task.Placement == nil {
			continue
		}
		for _, constraint := range service.Spec.Task.Placement.Constraints {
			if _, dup := seen[constraint]; dup {
				continue
			}
			seen[constraint] = struct{}{}
			constraints = append(constraints, constraint)
		}
	}

	sort.Strings(constraints)
	return constraints
}

// availableNodeLabels returns the values a node constraint can match, as
// key=value: node.hostname, node.role and every node label. The result is
// deduplicated and sorted, so two nodes that report the same label print it once
// and an operator can see at a glance which keys exist.
func availableNodeLabels(nodes []*api.Node) []string {
	seen := make(map[string]struct{})
	var labels []string
	add := func(format string, args ...any) {
		value := fmt.Sprintf(format, args...)
		if _, dup := seen[value]; dup {
			return
		}
		seen[value] = struct{}{}
		labels = append(labels, value)
	}

	for _, node := range nodes {
		if node == nil {
			continue
		}
		if node.Description != nil && node.Description.Hostname != "" {
			add("hostname=%s", node.Description.Hostname)
		}
		// node.role matches the *observed* role (manager/worker), which is what
		// the constraint matcher reads; printing the desired role would explain
		// a refusal with a value the scheduler never compared.
		add("role=%s", strings.ToLower(node.Role.String()))
		for key, value := range node.Spec.Annotations.Labels {
			add("%s=%s", key, value)
		}
	}

	sort.Strings(labels)
	return labels
}

// unschedulableNote is the trailing diagnostic for a refused placement, or ""
// when there is no evidence to report.
//
// The constraint branch is only taken when reason carries the constraint filter's
// own explanation: a task refused because the nodes were full does not make the
// service's constraint the culprit, and claiming so would send an operator to
// change a constraint that was never evaluated. With no such evidence the note
// quotes the scheduler verbatim and lists the declared constraints as declared,
// not as the cause. Every constraint is named, because the command cannot know
// which of several failed.
func unschedulableNote(reason string, constraints, labels []string) string {
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" {
		return ""
	}

	available := "none"
	if len(labels) > 0 {
		available = strings.Join(labels, ", ")
	}

	if len(constraints) > 0 && strings.Contains(reason, constraintFilterExplanation) {
		noun, verb := "constraint", "matches"
		if len(constraints) > 1 {
			noun, verb = "constraints", "match"
		}
		return fmt.Sprintf("no task is running: %s %s %s no node (available node labels: %s)",
			noun, strings.Join(quoteAll(constraints), "; "), verb, available)
	}

	if len(constraints) == 0 {
		return fmt.Sprintf("no task is running: scheduler reported %q (the service declares no placement constraints)", reason)
	}
	return fmt.Sprintf("no task is running: scheduler reported %q; declared constraints %s may not be the cause (available node labels: %s)",
		reason, strings.Join(quoteAll(constraints), "; "), available)
}

// quoteAll renders each value in double quotes, for the note's lists.
func quoteAll(values []string) []string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = fmt.Sprintf("%q", value)
	}
	return quoted
}

// writeUnschedulableNote prints the diagnostic after the task table. It writes
// to the same writer as the table, after the total line, because that is where
// this command already puts its trailing notes; and it never returns an error,
// so an unschedulable service cannot change the command's exit code.
func writeUnschedulableNote(ctx context.Context, client legacy.ControlClient, w io.Writer, tasks []*api.Task) {
	if !placementRefused(tasks) {
		return
	}

	services, err := client.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		log.Debug().Err(err).Msg("Skipping the unschedulable-placement note: cannot list services")
		return
	}
	nodes, err := client.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		log.Debug().Err(err).Msg("Skipping the unschedulable-placement note: cannot list nodes")
		return
	}

	note := unschedulableNote(
		refusedReason(tasks),
		pendingConstraints(tasks, services.Services),
		availableNodeLabels(nodes.Nodes),
	)
	if note == "" {
		return
	}
	fmt.Fprintf(w, "\n%s\n", note)
}
