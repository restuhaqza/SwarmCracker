package main

// This file owns `swarmcracker vm migrate`: the operator's one command for
// moving a task's microVM to another node.
//
//	swarmcracker vm migrate <task> --to <node> --timeout 5m
//
// The move itself is one manager API call (POST /v1/migrations): the manager
// exports the task on its own node, relays the artifact, prepares the target and
// replaces the service, and answers the report the command prints. Nothing about
// the move is resolved here - the task and the node are references the manager
// resolves - but two refusals are worth a round trip less: a task that is not
// running and a service that has more than one replica can never move, and the
// command says so locally instead of posting a request whose answer is already
// known.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/spf13/cobra"
)

const (
	// defaultMigrateTimeout is the command's default deadline. It matches the
	// client's own for this call and is deliberately longer than the manager's
	// three-minute flow: the answer is the point, and a client that gives up
	// early drops a report of a move the manager still finishes.
	defaultMigrateTimeout = 5 * time.Minute

	// migrationTaskStateRunning is the state a task must be in to move.
	migrationTaskStateRunning = "running"

	// migrationServiceModeReplicated is the only service mode a migration can pin.
	migrationServiceModeReplicated = "replicated"

	// minMigrationRefPrefix is the shortest ID prefix the CLI will resolve
	// itself, matching the client's reference rule: two hex characters match too
	// many objects to be a useful identifier.
	minMigrationRefPrefix = 4
)

// migrationClient is the client surface `vm migrate` needs: the two lists a
// reference is resolved from, the service whose replica count is refused, and
// the call itself. *client.Client implements it, so the command is testable
// with a fake.
type migrationClient interface {
	ListNodes(ctx context.Context) ([]v1.Node, error)
	ListTasks(ctx context.Context) ([]v1.Task, error)
	GetService(ctx context.Context, ref string) (v1.Service, error)
	Migrate(ctx context.Context, taskID, targetNode string) (v1.Migration, error)
}

// newVMMigrateCommand creates the `vm migrate` command.
func newVMMigrateCommand() *cobra.Command {
	var (
		target  string
		timeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "migrate <task> --to <node>",
		Short: "Move a running microVM to another node",
		Long: `Move one task's microVM to another node of the cluster.

The task is a task ID or any unique prefix of one; --to is a node ID, a
hostname, or a unique prefix of a node ID. A move replaces the service's task
with a pinned one, so the service must have exactly one replica and the task
must be running: the command refuses the rest before it posts anything.

The call is synchronous and the VM is down only while the replacement starts,
which the report measures. --timeout (default 5m) bounds the wait: the manager's
flow is bounded by three minutes plus the cleanup it performs before it answers,
so a shorter deadline drops the report of a move the manager still completes.

The move requires the manager API: pass -H (and --ca/--token-file) so the
request reaches a manager; a follower answers 503 unavailable naming the leader
to retry at, and a manager with migrations disabled answers 501.`,
		Example: `  swarmcracker task ls
  swarmcracker -H https://manager:4244 --ca ca.crt --token-file token \
      vm migrate 5f3a1b2c9d --to worker-2
  swarmcracker -H https://manager:4244 --ca ca.crt --token-file token \
      vm migrate web-1 --to 7c1e --timeout 8m`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(target) == "" {
				return output.NewError(output.CodeUsage,
					"a target node is required: pass --to <node id, hostname or unique prefix>")
			}
			if timeout <= 0 {
				return output.NewError(output.CodeUsage,
					"--timeout must be positive: it bounds how long the command waits for the manager's report")
			}
			// The route exists on the manager's API only: a task's memory image
			// moves between hosts, and only a manager can drive that. Saying so is
			// better than failing later with an unreachable local cluster.
			if apiHost == "" {
				return output.NewError(output.CodeUsage,
					"vm migrate requires the manager API: add --host, --ca and --token-file")
			}

			c, err := newContractClient()
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			// The command's deadline is the caller's: the client reads it to size
			// its request, because its own default was chosen for reads.
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			return migrateTaskWithClient(ctx, c, cmd.OutOrStdout(), args[0], target)
		},
	}

	cmd.Flags().StringVar(&target, "to", "",
		"Target node: a node ID, hostname, or a unique prefix of a node ID (required)")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultMigrateTimeout,
		"Deadline for the whole move, including the manager's cleanup")

	return cmd
}

// migrateTaskWithClient resolves both references, refuses what cannot move, and
// then posts the one request that moves the task.
//
// The local refusals run in the order they are cheapest: a reference that
// matches nothing costs a list read, a stopped task costs the same read, and the
// replica count costs one service read. None of them can be answered correctly
// by posting and reading the manager's refusal, so none of them posts.
func migrateTaskWithClient(
	ctx context.Context, c migrationClient, w io.Writer, taskRef, targetRef string,
) error {
	node, err := resolveMigrationNode(ctx, c, targetRef)
	if err != nil {
		return err
	}

	task, err := resolveMigrationTask(ctx, c, taskRef)
	if err != nil {
		return err
	}
	if task.State != migrationTaskStateRunning {
		return output.NewErrorf(output.CodeInvalid,
			"task %q is %s: a live migration moves a running task", task.ID, task.State).
			WithDetails(map[string]any{"reason": "task_not_running", "taskId": task.ID, "state": task.State})
	}
	if err := migrationServiceProblem(ctx, c, task); err != nil {
		return err
	}
	// The move pins the service to the target, so a target the task already runs
	// on is a request with nothing to do - and a manager would answer it after a
	// round trip that resolved both references anyway.
	if node.ID == task.NodeID {
		return output.NewErrorf(output.CodeInvalid,
			"target node %q is the node task %q already runs on", node.ID, task.ID).
			WithDetails(map[string]any{"reason": "target_is_source", "nodeId": node.ID})
	}

	doc, err := c.Migrate(ctx, task.ID, node.ID)
	if err != nil {
		// The manager's document is the answer: its code, its message and its
		// details.phase cross unchanged, so main prints it and exits with its code.
		return err
	}

	return writeMigrationReport(w, doc)
}

// migrationServiceProblem refuses a service a live migration cannot pin: no
// service at all, a mode other than replicated, or a replica count other than
// one. A placement constraint applies to every task of a service, so a service
// with two replicas cannot be moved by pinning one.
func migrationServiceProblem(ctx context.Context, c migrationClient, task v1.Task) error {
	if strings.TrimSpace(task.ServiceID) == "" {
		return output.NewErrorf(output.CodeInvalid,
			"task %q belongs to no service: a live migration replaces a service's task", task.ID).
			WithDetails(map[string]any{"reason": "task_without_service", "taskId": task.ID})
	}
	service, err := c.GetService(ctx, task.ServiceID)
	if err != nil {
		return err
	}
	if service.Spec.Mode != migrationServiceModeReplicated {
		return output.NewErrorf(output.CodeInvalid,
			"service %q is %s: a live migration pins a single replica of a replicated service",
			service.ID, service.Spec.Mode).
			WithDetails(map[string]any{"reason": "service_mode", "serviceId": service.ID, "mode": service.Spec.Mode})
	}
	if service.Spec.Replicas == nil || *service.Spec.Replicas != 1 {
		replicas := int64(0)
		if service.Spec.Replicas != nil {
			replicas = *service.Spec.Replicas
		}
		return output.NewErrorf(output.CodeInvalid,
			"service %q has %d replicas: a live migration moves a service with exactly one",
			service.ID, replicas).
			WithDetails(map[string]any{"reason": "replicas", "serviceId": service.ID, "replicas": replicas})
	}
	return nil
}

// resolveMigrationNode resolves --to against the cluster's nodes: an exact ID, an
// exact hostname, or a unique ID prefix of at least minMigrationRefPrefix
// characters. A miss names the nodes the cluster does have, so a typo is fixed
// without a second command.
func resolveMigrationNode(ctx context.Context, c migrationClient, ref string) (v1.Node, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return v1.Node{}, output.NewError(output.CodeUsage, "a target node is required")
	}

	nodes, err := c.ListNodes(ctx)
	if err != nil {
		return v1.Node{}, err
	}

	for i := range nodes {
		if nodes[i].ID == ref {
			return nodes[i], nil
		}
	}
	// A hostname beats a prefix for the same reason a service name does: a node
	// literally named "7c1e" must resolve even though it is also four characters
	// of another node's ID.
	for i := range nodes {
		if nodes[i].Hostname == ref {
			return nodes[i], nil
		}
	}

	if len(ref) < minMigrationRefPrefix {
		return v1.Node{}, output.NewErrorf(output.CodeUsage,
			"node reference %q is too short: use an exact node ID, an exact hostname, or at least %d characters of an ID",
			ref, minMigrationRefPrefix).
			WithDetails(map[string]any{"parameter": "to"})
	}
	return uniqueNodeByPrefix(ref, nodes)
}

// uniqueNodeByPrefix resolves an ID prefix, reporting the candidates when it is
// ambiguous so the operator can retype a longer one.
func uniqueNodeByPrefix(ref string, nodes []v1.Node) (v1.Node, error) {
	var matches []v1.Node
	for i := range nodes {
		if strings.HasPrefix(nodes[i].ID, ref) {
			matches = append(matches, nodes[i])
		}
	}

	switch len(matches) {
	case 0:
		if len(nodes) == 0 {
			return v1.Node{}, output.NewError(output.CodeNotFound,
				"the cluster has no nodes to move to").
				WithDetails(map[string]any{"parameter": "to"})
		}
		return v1.Node{}, output.NewErrorf(output.CodeNotFound,
			"node %q matches no node: want an exact node ID, an exact hostname, or at least %d characters of an ID (nodes: %s)",
			ref, minMigrationRefPrefix, nodeChoices(nodes)).
			WithDetails(map[string]any{"parameter": "to"})
	case 1:
		return matches[0], nil
	default:
		return v1.Node{}, output.NewErrorf(output.CodeAmbiguous,
			"node reference %q matches %d nodes: %s", ref, len(matches), nodeChoices(matches)).
			WithDetails(map[string]any{
				"parameter":  "to",
				"candidates": nodeChoices(matches),
			})
	}
}

// nodeChoices renders nodes as "id (hostname)" for a refusal, sorted by ID so the
// answer is stable.
func nodeChoices(nodes []v1.Node) string {
	choices := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.Hostname != "" {
			choices = append(choices, node.ID+" ("+node.Hostname+")")
			continue
		}
		choices = append(choices, node.ID)
	}
	sort.Strings(choices)
	return strings.Join(choices, ", ")
}

// resolveMigrationTask resolves the task argument: an exact ID or a unique ID
// prefix of at least minMigrationRefPrefix characters, against one task list read.
func resolveMigrationTask(ctx context.Context, c migrationClient, ref string) (v1.Task, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return v1.Task{}, output.NewError(output.CodeUsage, "a task reference is required")
	}

	tasks, err := c.ListTasks(ctx)
	if err != nil {
		return v1.Task{}, err
	}
	for i := range tasks {
		if tasks[i].ID == ref {
			return tasks[i], nil
		}
	}

	if len(ref) < minMigrationRefPrefix {
		return v1.Task{}, output.NewErrorf(output.CodeUsage,
			"task reference %q is too short: use an exact task ID or at least %d characters of one",
			ref, minMigrationRefPrefix).
			WithDetails(map[string]any{"parameter": "task"})
	}

	var matches []v1.Task
	for i := range tasks {
		if strings.HasPrefix(tasks[i].ID, ref) {
			matches = append(matches, tasks[i])
		}
	}

	switch len(matches) {
	case 0:
		return v1.Task{}, output.NewErrorf(output.CodeNotFound,
			"task %q matches no task: want an exact task ID or at least %d characters of one",
			ref, minMigrationRefPrefix).
			WithDetails(map[string]any{"parameter": "task"})
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, task := range matches {
			ids = append(ids, task.ID)
		}
		return v1.Task{}, output.NewErrorf(output.CodeAmbiguous,
			"task reference %q matches %d tasks: %s", ref, len(matches), strings.Join(ids, ", ")).
			WithDetails(map[string]any{"parameter": "task", "candidates": ids})
	}
}

// writeMigrationReport prints the report: what moved, where from and to, one line
// per phase with its duration, the measured downtime, and the outcome last.
//
// A phase with no finish instant is still running (a pending verify), so its
// duration is "-" rather than zero. The downtime line says "not measured" for a
// report that never saw a replacement running, because an unmeasured downtime is
// not zero seconds.
func writeMigrationReport(w io.Writer, doc v1.Migration) error {
	fmt.Fprintf(w, "Migration %s\n", doc.ID)
	fmt.Fprintf(w, "  task      %s\n", doc.TaskID)
	if doc.ServiceID != "" {
		fmt.Fprintf(w, "  service   %s\n", doc.ServiceID)
	}
	fmt.Fprintf(w, "  from      %s\n", doc.SourceNode)
	fmt.Fprintf(w, "  to        %s\n", doc.TargetNode)

	for _, phase := range doc.Phases {
		line := fmt.Sprintf("  %-9s %-7s %8s", phase.Name, phase.Result, phaseDuration(phase))
		if phase.Message != "" {
			line += "  (" + phase.Message + ")"
		}
		fmt.Fprintln(w, line)
	}

	fmt.Fprintf(w, "  downtime  %s\n", migrationDowntime(doc.DowntimeMs))
	fmt.Fprintf(w, "  outcome   %s\n", doc.Outcome)
	return nil
}

// phaseDuration renders how long one phase took, from the report's own instants.
// "-" means the phase had not finished when the report was written.
func phaseDuration(phase v1.MigrationPhase) string {
	if phase.StartedAt == "" || phase.FinishedAt == "" {
		return "-"
	}
	started, err := time.Parse(time.RFC3339Nano, phase.StartedAt)
	if err != nil {
		return "-"
	}
	finished, err := time.Parse(time.RFC3339Nano, phase.FinishedAt)
	if err != nil {
		return "-"
	}
	if finished.Before(started) {
		return "-"
	}
	return formatMigrationDuration(finished.Sub(started))
}

// formatMigrationDuration prints one duration at the resolution a human reads a
// move at: milliseconds under a second, one decimal above it.
func formatMigrationDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(100 * time.Millisecond).String()
}

// migrationDowntime renders the report's measured downtime. It keeps the
// millisecond the report carries: the downtime is the number the move is judged
// by, so it is printed at the precision it was measured at rather than rounded.
func migrationDowntime(ms int64) string {
	if ms <= 0 {
		return "not measured"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(time.Millisecond).String()
}
