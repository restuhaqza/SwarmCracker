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
// constraints not satisfied on N node(s)" (manager/scheduler/filter.go, Explain)
// and never names the constraint or the labels the cluster reports. An operator
// who wrote `--constraint node.labels.disk==ssd` therefore needs the service
// spec and the node labels to see that no node has disk=ssd.
//
// The note below is that second read. It is a diagnostic, never an error: it is
// printed after the table, it changes no exit code, and a listing that cannot
// assemble it prints nothing.

// taskUnplaced reports whether a task is still waiting for the scheduler. NEW
// and PENDING are the only states with no placement decision behind them; every
// later state means a node was found, whether or not the workload then worked.
func taskUnplaced(task *api.Task) bool {
	return task.Status.State == api.TaskStateNew || task.Status.State == api.TaskStatePending
}

// placementRefused reports whether a whole task listing is evidence that the
// scheduler refused the placement: every task is still unplaced, and at least
// one carries the scheduler's Status.Err. Requiring the error keeps a task that
// was created a moment ago (NEW, not looked at yet) from producing a verdict.
func placementRefused(tasks []*api.Task) bool {
	if len(tasks) == 0 {
		return false
	}
	refused := false
	for _, task := range tasks {
		if !taskUnplaced(task) {
			return false
		}
		if task.Status.Err != "" {
			refused = true
		}
	}
	return refused
}

// pendingConstraints returns the placement constraints of the services the
// tasks belong to, deduplicated and sorted. A listing can span services
// (`task ls` does), so the caller gets them all rather than a guess at which one
// was refused: the scheduler rejected every constraint at once and nothing here
// can tell which of them failed.
func pendingConstraints(tasks []*api.Task, services []*api.Service) []string {
	wanted := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		wanted[task.ServiceID] = struct{}{}
	}

	seen := make(map[string]struct{})
	constraints := make([]string, 0, len(wanted))
	for _, service := range services {
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
// when there is nothing to explain (no constraint to name). Every constraint is
// named, because the command cannot know which of several failed.
func unschedulableNote(constraints, labels []string) string {
	if len(constraints) == 0 {
		return ""
	}

	noun, verb := "constraint", "matches"
	if len(constraints) > 1 {
		noun, verb = "constraints", "match"
	}
	quoted := make([]string, len(constraints))
	for i, constraint := range constraints {
		quoted[i] = fmt.Sprintf("%q", constraint)
	}

	available := "none"
	if len(labels) > 0 {
		available = strings.Join(labels, ", ")
	}

	return fmt.Sprintf("no task is running: %s %s %s no node (available node labels: %s)",
		noun, strings.Join(quoted, "; "), verb, available)
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
		pendingConstraints(tasks, services.Services),
		availableNodeLabels(nodes.Nodes),
	)
	if note == "" {
		return
	}
	fmt.Fprintf(w, "\n%s\n", note)
}
