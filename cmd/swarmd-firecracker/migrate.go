package main

// This file owns the manager's orchestration of one live migration: the sequence
// that turns "move this task to that node" into an artifact on the source, its
// bytes on the target, a restored replacement through SwarmKit, and a cleanup
// that leaves nothing behind.
//
//	validate  the task runs, its service has one replica, the target is elsewhere
//	          and ready, and no other migration of that service is in flight
//	export    the source dumps the task and describes the artifact
//	relay     each of the artifact's files, one at a time: source -> spool ->
//	          target, verified against the manifest on both hops
//	prepare   the target records the directive that makes its next task restore
//	replace   the service gets a placement constraint pinning it to the target
//	verify    wait for a task of that service on the target to report running
//	cleanup   restore the placement, make every node forget the moved guest,
//	          discard the artifact, drop the spool entry
//
// The manager is the only party both nodes can reach, so it is the relay, and it
// is a control plane, so it never holds a whole file: pkg/migration's spool is a
// directory on disk, and every hop copies through a bounded buffer.
//
// # What is abandoned and what is kept
//
// The split is the moment the service update lands. Before it, the migration can
// still be undone completely, so a failure cleans up: the target's directive (if
// one was recorded), the source's artifact (if one was exported), the spool entry
// and the placement (if it was changed). After it, SwarmKit owns the retry: the
// executioner on the target consumes the directive and restores from the staged
// artifact, and a retry that outlives this call must still find both. So a
// failure in verify keeps the directive and the artifact, restores the placement
// and reports the phase - and a replacement that simply took longer than the
// deadline is reported as pending rather than as a failure, because nothing
// failed: it is still happening.
//
// # One migration per service
//
// A placement constraint applies to every task of a service, so a second
// migration of the same service would pin the service twice and clean up the
// first one's constraint. The in-process map is the whole guard: it is enough
// because a manager is the only caller, and it is cleared on every exit so a
// failed migration never locks a service out of being moved again.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/moby/swarmkit/v2/log"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/broker"
	"github.com/restuhaqza/swarmcracker/pkg/client"
	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
)

const (
	// migrationOverallTimeout bounds one Migrate call: every node call the relay
	// makes inherits it, and it is the binding deadline for the whole flow.
	//
	// It is deliberately shorter than the broker's own transfer allowance
	// (broker.DefaultMigrationTimeout, half an hour). That allowance is what one
	// file transfer may take on a node's side; this slice only offers a move whose
	// artifact is small enough to cross inside a synchronous request, so the call
	// bound - not the node's allowance - is the one that can be met. A larger guest
	// needs a longer call bound (a job the caller polls, which this slice does not
	// offer), and raising this constant alone would only turn a useful 504 into a
	// request that holds a connection for half an hour.
	//
	// Cleanup is not inside that budget: it runs detached, bounded by
	// migrationCleanupTimeout, so the worst case a caller waits is three minutes plus
	// up to one minute of cleanup outside that deadline, which the caller does wait
	// for: a client timeout shorter than four minutes can drop a response for a move
	// this manager still completes.
	migrationOverallTimeout = 3 * time.Minute

	// migrationVerifyTimeout bounds the wait for a replacement to report running.
	// It is separate from and shorter than the overall timeout because it starts
	// after the transfer: the artifact is already on the target, and a service
	// that has not scheduled the replacement in this window is not going to be
	// rescued by waiting longer in this call.
	migrationVerifyTimeout = 2 * time.Minute

	// migrationCleanupTimeout bounds the cleanup a failure runs. It is detached
	// from the caller's context on purpose: a request that gave up must still not
	// leave an artifact on a node or a directive that points at bytes nobody will
	// keep.
	migrationCleanupTimeout = time.Minute

	// migrationPollInterval is the pause between replacement observations. A
	// second is short enough to measure downtime honestly and long enough that a
	// two-minute wait costs a hundred reads, not thousands.
	migrationPollInterval = time.Second

	// migrationSpoolSweepInterval is how often the manager removes spool entries a
	// crashed or cancelled migration left behind. It is much shorter than
	// migration.DefaultSpoolTTL (40 min) so an abandoned artifact is reclaimed
	// within minutes of expiring, and long enough that the sweep is a directory
	// read every few minutes rather than a loop.
	migrationSpoolSweepInterval = 10 * time.Minute

	// migrationTargetConstraint pins the moved task to its target. It is a node ID
	// rather than a name or a label because the task already resolved one.
	migrationTargetConstraint = "node.id==%s"

	// migrationTaskStateRunning is the state SwarmKit reports for a task the node
	// is running.
	migrationTaskStateRunning = "running"
	// migrationTaskStateFailed is the state that makes a replacement a failure
	// rather than a slow start.
	migrationTaskStateFailed = "failed"
	// migrationNodeStateReady and migrationNodeAvailabilityActive are the two
	// conditions a target must meet: a ready node can start a task, an active one
	// may be scheduled on.
	migrationNodeStateReady         = "ready"
	migrationNodeAvailabilityActive = "active"
	migrationServiceModeReplicated  = "replicated"
)

// migrationTaskStatesGone are the states that mean a task no longer runs the
// workload. SwarmKit keeps a stopped task in the service's task list until its
// history retention drops it, so the old task's disappearance cannot be the only
// evidence that it stopped: a task observed complete, shutdown or failed has
// already stopped even though the document is still listed.
var migrationTaskStatesGone = map[string]struct{}{
	"complete": {},
	"shutdown": {},
	"failed":   {},
	"rejected": {},
	"remove":   {},
	"orphaned": {},
}

// migrationClient is the slice of *client.Client this file reads and writes: a
// task and its service, the task's network attachments (which addresses sit on
// which network, for the re-anneal), the target node, the service's tasks while
// verifying, the one service update that pins and unpins the placement, and the
// node list the leadership gate reads.
type migrationClient interface {
	GetTask(ctx context.Context, ref string) (v1.Task, error)
	GetService(ctx context.Context, ref string) (v1.Service, error)
	ListServiceTasks(ctx context.Context, ref string) ([]v1.Task, error)
	GetNode(ctx context.Context, ref string) (v1.Node, error)
	ListNodes(ctx context.Context) ([]v1.Node, error)
	UpdateService(ctx context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Service, error)
	// TaskAttachments pairs a task's addresses with the network each was allocated
	// on. The v1 task document flattens that pairing, and a re-anneal without it
	// would delete on a bridge the guest never used.
	TaskAttachments(ctx context.Context, taskID string) ([]client.TaskAttachment, error)
}

// migrationBroker is the broker manager's migration transport, as this file uses
// it: export and discard on the source, file and stage for the bytes, prepare and
// unprepare for the target's directive. *broker.Manager implements it, so the
// daemon wires it without an adapter.
type migrationBroker interface {
	Export(ctx context.Context, sourceNode string, task v1.Task) (broker.MigrationExportDocument, error)
	File(ctx context.Context, nodeID string, task v1.Task, artifact migration.Artifact, name string, w io.Writer) (int64, error)
	Stage(ctx context.Context, nodeID string, task v1.Task, artifact migration.Artifact, name string, size int64, sha256Hex string, r io.Reader) error
	Discard(ctx context.Context, nodeID string, task v1.Task, artifact migration.Artifact) error
	// Prepare records the target's directive and carries the artifact's manifest with
	// it: the restore that claims the directive reads artifact.json from the staged
	// directory, and the bytes alone do not say what they are.
	Prepare(ctx context.Context, nodeID, serviceID string, artifact migration.Artifact) error
	// Unprepare removes a directive only while it names artifactID: the node's
	// directive is keyed by service, and another manager may have prepared the
	// service since.
	Unprepare(ctx context.Context, nodeID, serviceID, artifactID string) error
	// Reanneal makes one node forget the moved guest on one attachment, so its next
	// frame re-learns the guest's MAC on the target. networkID names the attachment
	// rather than a device: the node derives the bridge that network is on. It is
	// best effort: a peer that fails is logged, never a reason to report the move as
	// anything but moved.
	Reanneal(ctx context.Context, nodeID, networkID, ip, mac string) error
}

// migrationCoordinator runs one migration at a time per service. Its fields are
// the whole outside world plus the knobs a test turns: the clock, the three
// deadlines and the poll interval.
type migrationCoordinator struct {
	client migrationClient
	broker migrationBroker
	spool  *migration.Store
	// self is this daemon's node hostname. It is what the leadership gate compares
	// the swarm leader's hostname against, exactly like the autoscaler's.
	self string

	now            func() time.Time
	overallTimeout time.Duration
	verifyTimeout  time.Duration
	cleanupTimeout time.Duration
	pollInterval   time.Duration

	mu sync.Mutex
	// gateNotice records that the fail-open decision has been logged once. It
	// repeats only while the cluster cannot say who the leader is.
	gateNotice bool
	inFlight   map[string]struct{}
}

// newMigrationCoordinator builds the coordinator with the production defaults. A
// nil broker or spool would panic on the first migration, so the daemon resolves
// both before it wires this: see managerMigrations. self is this node's hostname,
// used only by the leadership gate, and an empty one makes the gate fail open.
func newMigrationCoordinator(client migrationClient, nodes migrationBroker, spool *migration.Store, self string) *migrationCoordinator {
	return &migrationCoordinator{
		client:         client,
		broker:         nodes,
		spool:          spool,
		self:           self,
		now:            time.Now,
		overallTimeout: migrationOverallTimeout,
		verifyTimeout:  migrationVerifyTimeout,
		cleanupTimeout: migrationCleanupTimeout,
		pollInterval:   migrationPollInterval,
		inFlight:       make(map[string]struct{}),
	}
}

// Migrate moves one task's VM to another node and reports what it did.
//
// The call is synchronous and bounded: the flow runs inside migrationOverallTimeout,
// and a verify that outlives its own (shorter) deadline answers a pending migration
// rather than an error. Cleanup is detached from that budget - it runs with its own
// one-minute bound after the answer is composed - so the caller waits at most three
// minutes, plus whatever the manager's detached cleanup takes, which the caller never
// sees. Every failure is a typed error whose details.phase names the step that
// failed; the migration itself is only a document when the service update landed.
//
// Only the swarm's raft leader may run a migration: the service update, the target's
// directive and the source's artifact are one operation, and two managers running it
// would prepare the same service twice and unpin each other. A request served by a
// follower is refused with a retry-at-the-leader unavailable, and a cluster that
// cannot say who the leader is fails open (see leaderProblem).
func (c *migrationCoordinator) Migrate(ctx context.Context, taskRef, targetNode string) (v1.Migration, error) {
	ctx, cancel := context.WithTimeout(ctx, c.overallTimeout)
	defer cancel()

	// The instant the migration starts is what dates a replacement: a task of this
	// service on the target that predates it belongs to an earlier attempt.
	run := &migrationRun{now: c.now, startedAt: c.now()}

	plan, err := c.validate(ctx, run, taskRef, targetNode)
	if err != nil {
		return v1.Migration{}, migrationPhaseError(v1.MigrationPhaseValidate, err)
	}
	defer c.endInFlight(plan.service.ID)

	state := &migrationState{}
	outcome, downtime, err := c.run(ctx, run, plan, state)
	if err != nil {
		return v1.Migration{}, err
	}
	return run.document(plan, state.artifact, outcome, downtime), nil
}

// migrationPlan is what validate established: the task, the service whose single
// replica moves, the target node and the placement to restore.
type migrationPlan struct {
	task    v1.Task
	service v1.Service
	target  v1.Node
	// constraints is the service's placement as it was before this migration
	// touched it.
	constraints []string
}

// migrationState is how far the flow got, which is what decides what a failure
// must undo. It is also the record of the artifact, so no path has to re-derive
// an ID.
type migrationState struct {
	artifact migration.Artifact
	// exported and placement are what the cleanup must undo: an artifact on the
	// source, and a placement this call changed.
	exported  bool
	pinned    []string
	placement bool
}

// validate runs the preflight: this manager is the leader, a running task of a
// single-replica replicated service, a different ready and active target, and no
// migration of the same service already running. It resolves the target before it
// takes the in-flight slot, so a refused service never blocks the map.
func (c *migrationCoordinator) validate(
	ctx context.Context, run *migrationRun, taskRef, targetNodeRef string,
) (migrationPlan, error) {
	var plan migrationPlan

	// The gate comes first: a follower must not export, prepare or replace
	// anything, because the rest of the flow would then be competing with the
	// leader's own migration of the same service. It is inside the validate phase
	// because it is a preflight answer about the request, not a step of the move.
	finish := run.start(v1.MigrationPhaseValidate)

	if err := c.leaderProblem(ctx); err != nil {
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}

	if strings.TrimSpace(targetNodeRef) == "" {
		err := invalidMigrationField("targetNode", "must not be empty")
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}

	task, err := c.client.GetTask(ctx, taskRef)
	if err != nil {
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}
	if task.State != migrationTaskStateRunning {
		err := output.NewErrorf(output.CodeInvalid,
			"task %q is %s: a live migration moves a running task", task.ID, task.State).
			WithDetails(map[string]any{"fields": map[string]any{"taskId": "must name a running task"}, "state": task.State})
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}
	if task.NodeID == "" {
		err := output.NewErrorf(output.CodeInvalid,
			"task %q names no node: a live migration moves a task that is scheduled", task.ID).
			WithDetails(map[string]any{"fields": map[string]any{"taskId": "must name a scheduled task"}})
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}
	if task.ServiceID == "" {
		err := output.NewErrorf(output.CodeInvalid,
			"task %q belongs to no service: a live migration replaces a service's task", task.ID).
			WithDetails(map[string]any{"fields": map[string]any{"taskId": "must name a service task"}})
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}

	service, err := c.client.GetService(ctx, task.ServiceID)
	if err != nil {
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}
	if service.Spec.Mode != migrationServiceModeReplicated {
		err := output.NewErrorf(output.CodeInvalid,
			"service %q is %s: a live migration pins a single replica to one node", service.ID, service.Spec.Mode).
			WithDetails(map[string]any{"reason": "service_mode", "serviceId": service.ID})
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}
	if service.Spec.Replicas == nil || *service.Spec.Replicas != 1 {
		replicas := int64(0)
		if service.Spec.Replicas != nil {
			replicas = *service.Spec.Replicas
		}
		err := output.NewErrorf(output.CodeInvalid,
			"service %q has %d replicas: a live migration moves a service with exactly one", service.ID, replicas).
			WithDetails(map[string]any{"reason": "replicas", "serviceId": service.ID, "replicas": replicas})
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}

	if targetNodeRef == task.NodeID {
		err := output.NewErrorf(output.CodeInvalid,
			"target node %q is the node the task already runs on", task.NodeID).
			WithDetails(map[string]any{"fields": map[string]any{"targetNode": "must name another node"}, "nodeId": task.NodeID})
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}

	node, err := c.client.GetNode(ctx, targetNodeRef)
	if err != nil {
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}
	if node.ID == task.NodeID {
		err := output.NewErrorf(output.CodeInvalid,
			"target node %q is the node the task already runs on", node.ID).
			WithDetails(map[string]any{"fields": map[string]any{"targetNode": "must name another node"}, "nodeId": node.ID})
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}
	if node.State != migrationNodeStateReady || node.Availability != migrationNodeAvailabilityActive {
		err := output.NewErrorf(output.CodeInvalid,
			"target node %q is %s/%s: a live migration needs a ready, active node", node.ID, node.State, node.Availability).
			WithDetails(map[string]any{"reason": "node_not_schedulable", "nodeId": node.ID,
				"state": node.State, "availability": node.Availability})
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}

	if !c.beginInFlight(service.ID) {
		err := output.NewErrorf(output.CodeConflict,
			"a migration of service %q is already in flight", service.ID).
			WithDetails(map[string]any{"reason": "migration_in_flight", "serviceId": service.ID})
		finish(v1.MigrationPhaseError, publicFailure(err))
		return plan, err
	}

	plan = migrationPlan{
		task:        task,
		service:     service,
		target:      node,
		constraints: constraintsOf(service),
	}
	finish(v1.MigrationPhaseOK, "")
	return plan, nil
}

// leaderProblem reports nil when this manager may run the migration, and a typed
// unavailable when another node is the swarm's raft leader.
//
// Every manager runs the API, so a call that arrives at a follower would export on
// the source, stage on the target and update the service through SwarmKit while the
// leader - or the follower that won the last election - does the same: both prepare
// the same service (the node keeps one directive, so the later prepare replaces the
// earlier) and both pin and unpin the placement, and the loser's cleanup deletes the
// winner's directive while the winner still answers "moved". Only the leader acts.
//
// The gate fails open, exactly like the autoscaler's: a cluster that reports no
// leader, a node list this manager cannot read and a daemon that does not know its own
// hostname all mean "no verdict", and the answer to no verdict is to run the
// migration rather than to refuse every move on a manager set that cannot report who
// is in charge. The reason is logged once, at debug, because it repeats per call.
func (c *migrationCoordinator) leaderProblem(ctx context.Context) error {
	if c.self == "" {
		c.noteNoVerdict(ctx, "this daemon's hostname is unknown")
		return nil
	}

	nodes, err := c.client.ListNodes(ctx)
	if err != nil {
		c.noteNoVerdict(ctx, "the node list failed: "+publicFailure(err))
		return nil
	}

	var leader *v1.Node
	for i := range nodes {
		if manager := nodes[i].Manager; manager != nil && manager.Leader {
			leader = &nodes[i]
			break
		}
	}
	switch {
	case leader == nil:
		c.noteNoVerdict(ctx, "no node reports itself as the swarm leader")
		return nil
	case leader.Hostname == "":
		c.noteNoVerdict(ctx, "the swarm leader has no hostname")
		return nil
	}

	if leader.Hostname == c.self {
		return nil
	}

	return output.NewErrorf(output.CodeUnavailable,
		"node %q is the swarm leader, not this manager: retry the migration there", leader.Hostname).
		WithDetails(map[string]any{"reason": "not_leader", "leader": leader.Hostname})
}

// noteNoVerdict logs a failed-open gate decision the first time it happens. Once is
// enough: the same missing cluster fact repeats on every call, and a debug line per
// migration would bury the ones that ran.
func (c *migrationCoordinator) noteNoVerdict(ctx context.Context, reason string) {
	c.mu.Lock()
	noticed := c.gateNotice
	c.gateNotice = true
	c.mu.Unlock()
	if noticed {
		return
	}
	log.G(ctx).Debugf("Migration: cannot tell whether this node is the swarm leader (%s); migrating anyway", reason)
}

// run walks export through cleanup. It returns the outcome and the measured
// downtime, or the typed failure of the phase that stopped it - by then that
// phase has already recorded itself and anything it left behind has been decided.
func (c *migrationCoordinator) run(
	ctx context.Context, run *migrationRun, plan migrationPlan, state *migrationState,
) (string, time.Duration, error) {
	finish := run.start(v1.MigrationPhaseExport)
	doc, err := c.broker.Export(ctx, plan.task.NodeID, plan.task)
	if err == nil {
		err = artifactDescribesTask(doc.Artifact, plan.task)
	}
	if err == nil {
		err = doc.Artifact.Validate()
	}
	if err != nil {
		finish(v1.MigrationPhaseError, publicFailure(err))
		return "", 0, c.abandon(ctx, plan, state, v1.MigrationPhaseExport, err)
	}
	state.artifact = doc.Artifact
	state.exported = true
	finish(v1.MigrationPhaseOK, "")

	finish = run.start(v1.MigrationPhaseRelay)
	for _, file := range artifactFiles(state.artifact) {
		if err := c.relayFile(ctx, plan, state.artifact, file); err != nil {
			finish(v1.MigrationPhaseError, publicFailure(err))
			return "", 0, c.abandon(ctx, plan, state, v1.MigrationPhaseRelay, err)
		}
	}
	finish(v1.MigrationPhaseOK, "")

	finish = run.start(v1.MigrationPhasePrepare)
	if err := c.broker.Prepare(ctx, plan.target.ID, plan.service.ID, state.artifact); err != nil {
		finish(v1.MigrationPhaseError, publicFailure(err))
		// A prepare whose answer was lost may have landed anyway: the node records
		// the directive before it answers. abandon removes it, and the removal is
		// artifact-scoped, so it can only remove this migration's directive.
		return "", 0, c.abandon(ctx, plan, state, v1.MigrationPhasePrepare, err)
	}
	finish(v1.MigrationPhaseOK, "")

	finish = run.start(v1.MigrationPhaseReplace)
	if _, err := c.replace(ctx, plan, state); err != nil {
		finish(v1.MigrationPhaseError, publicFailure(err))
		return "", 0, c.abandon(ctx, plan, state, v1.MigrationPhaseReplace, err)
	}
	finish(v1.MigrationPhaseOK, "")

	finish = run.start(v1.MigrationPhaseVerify)
	outcome, downtime, reason, err := c.verify(ctx, run.startedAt, plan)
	if err != nil {
		finish(v1.MigrationPhaseError, publicFailure(err))
		// The update landed, so the directive and the artifact are the retry's:
		// keep them and only restore the placement this call changed.
		c.keepAfterReplace(ctx, plan, state)
		return "", 0, migrationPhaseError(v1.MigrationPhaseVerify, err)
	}
	if outcome == v1.MigrationOutcomePending {
		finish(v1.MigrationPhasePending, reason)
	} else {
		finish(v1.MigrationPhaseOK, "")
	}

	finish = run.start(v1.MigrationPhaseCleanup)
	finish(v1.MigrationPhaseOK, c.cleanup(ctx, plan, state, outcome))

	return outcome, downtime, nil
}

// relayFile moves one file of the artifact through the spool: the source into it,
// the spool into the target. The spool publishes a file only when its size and
// digest are the manifest's, the target checks the same two values again, and the
// reader verifies the bytes as the stage consumes them - so a file either arrives
// as the artifact describes it or not at all.
//
// The spool holds one file per artifact, which is why the entry is removed between
// files: that is the hand-off, not a cache.
func (c *migrationCoordinator) relayFile(
	ctx context.Context, plan migrationPlan, artifact migration.Artifact, file migration.File,
) error {
	writer, err := c.spool.Writer(artifact.ID, file)
	if err != nil {
		return err
	}

	written, err := c.broker.File(ctx, plan.task.NodeID, plan.task, artifact, file.Name, writer)
	if err != nil {
		// The bytes already written are refused, not published: a partial file
		// that looked complete would be staged into a guest.
		_ = writer.Abort()
		return err
	}
	if written != file.Size {
		_ = writer.Abort()
		return output.NewErrorf(output.CodeConflict,
			"file %q arrived as %d bytes: the artifact describes %d", file.Name, written, file.Size).
			WithDetails(map[string]any{"reason": "size_mismatch", "name": file.Name, "size": file.Size})
	}
	if err := writer.Close(); err != nil {
		// Close removes the slot and publishes nothing when the transfer did not
		// match the manifest, so the error is the whole cleanup.
		return err
	}

	reader, err := c.spool.Open(artifact.ID, file)
	if err != nil {
		_ = c.spool.Remove(artifact.ID)
		return err
	}
	stageErr := c.broker.Stage(ctx, plan.target.ID, plan.task, artifact, file.Name, file.Size, file.SHA256, reader)
	closeErr := reader.Close()
	if stageErr != nil {
		// The target refused the file; the spool entry is worthless either way,
		// and removing it lets a retried migration spool the next file.
		_ = c.spool.Remove(artifact.ID)
		return stageErr
	}
	if closeErr != nil {
		_ = c.spool.Remove(artifact.ID)
		return output.NewErrorf(output.CodeInternal,
			"the spooled file %q could not be released after it was staged", file.Name).
			WithDetails(map[string]any{"reason": "spool_unavailable", "name": file.Name})
	}
	return c.spool.Remove(artifact.ID)
}

// replace pins the service to the target and lets SwarmKit stop the old task and
// start the new one. The revision is the snapshot validate took, so a service
// that changed while the artifact was in transit refuses this update instead of
// having its new spec silently overwritten: that race is an error, not a retry.
//
// A failed update is not proof that nothing happened. SwarmKit commits the update
// and then the answer travels back through the control socket and the client, and an
// answer lost on that path - a deadline, a closed connection, a manager that was
// leader when it wrote and lost the election after - leaves a service that is already
// pinned. Treating that as "never landed" would restore the placement under a
// replacement that is starting, delete the target's directive and discard the
// artifact: a service with a task on the way and nothing to restore. So a failure
// reads the service back, and constraints that are exactly the list this call wrote
// mean the update landed and only the answer was lost.
func (c *migrationCoordinator) replace(
	ctx context.Context, plan migrationPlan, state *migrationState,
) (v1.Service, error) {
	pinned := make([]string, 0, len(plan.constraints)+1)
	pinned = append(pinned, plan.constraints...)
	pinned = append(pinned, fmt.Sprintf(migrationTargetConstraint, plan.target.ID))

	patch, err := migrationPlacementPatch(pinned)
	if err != nil {
		return v1.Service{}, err
	}

	service, err := c.client.UpdateService(ctx, plan.service.ID, patch, v1.WriteOptions{IfMatch: plan.service.Revision})
	if err != nil {
		current, landed, readErr := c.readPinned(ctx, plan, pinned)
		if readErr != nil || !landed {
			if readErr != nil {
				log.G(ctx).WithError(readErr).
					Debug("Migration: could not read the service back after a failed update")
			}
			return v1.Service{}, err
		}
		log.G(ctx).WithError(err).
			Warn("Migration: the service update's answer was lost, but the update landed: keeping the placement, the directive and the artifact")
		service = current
	}
	state.pinned = pinned
	state.placement = true
	return service, nil
}

// readPinned reports whether the service already carries exactly the constraints the
// failed update would have written, and returns the service it read. It is the one
// read that can tell a committed-but-unacknowledged update from a refused one.
func (c *migrationCoordinator) readPinned(
	ctx context.Context, plan migrationPlan, pinned []string,
) (v1.Service, bool, error) {
	current, err := c.client.GetService(ctx, plan.service.ID)
	if err != nil {
		return v1.Service{}, false, err
	}
	return current, constraintsEqual(constraintsOf(current), pinned), nil
}

// verify waits for the replacement to report running on the target. It observes
// the old task too: the instant it is gone is the start of the downtime the
// report carries, and a target that never loses the old task is a different
// reason from a replacement that never starts.
//
// startedAt is when this migration began: only a task of this service on the target
// that was created after it can be this call's replacement (see observeReplacement).
//
// A deadline is pending, not an error: the service was replaced, the directive
// and the artifact are the retry's, and the answer says how far it got. A failed
// replacement or an unreadable task list is an error, because waiting longer
// cannot fix either.
func (c *migrationCoordinator) verify(
	ctx context.Context, startedAt time.Time, plan migrationPlan,
) (outcome string, downtime time.Duration, reason string, err error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, c.verifyTimeout)
	defer cancel()

	updateReturned := c.now()
	var stoppedAt time.Time
	oldGoneSeen := false

	for {
		tasks, listErr := c.client.ListServiceTasks(deadlineCtx, plan.service.ID)
		if listErr != nil {
			return "", 0, "", listErr
		}

		observed := observeReplacement(tasks, plan.service.ID, plan.task.ID, plan.target.ID, startedAt)
		now := c.now()
		if observed.oldGone {
			oldGoneSeen = true
			if stoppedAt.IsZero() {
				stoppedAt = now
			}
		}
		if observed.replacement != nil {
			if stoppedAt.IsZero() {
				// The old task was never observed gone: the update's own return is
				// the last instant it was known to exist, so measure from there.
				stoppedAt = updateReturned
			}
			return v1.MigrationOutcomeMoved, now.Sub(stoppedAt), "", nil
		}
		if observed.failed {
			return "", 0, "replacement_failed", output.NewErrorf(output.CodeUnavailable,
				"the replacement of service %q on node %q failed", plan.service.ID, plan.target.ID).
				WithDetails(map[string]any{"reason": "replacement_failed", "serviceId": plan.service.ID, "nodeId": plan.target.ID})
		}

		timer := time.NewTimer(c.pollInterval)
		select {
		case <-deadlineCtx.Done():
			timer.Stop()
			if !oldGoneSeen {
				return v1.MigrationOutcomePending, 0, reasonOldTaskRunning, nil
			}
			return v1.MigrationOutcomePending, 0, reasonReplacementNotRunning, nil
		case <-timer.C:
		}
	}
}

// Reason strings the pending verify reports. They are the "which half timed out"
// the report has to answer: a service that never lost its task did not move, and
// a service that lost it but scheduled no replacement is a cluster problem the
// retry will pick up.
const (
	reasonOldTaskRunning        = "old_task_not_gone"
	reasonReplacementNotRunning = "replacement_not_running"
)

// keepAfterReplace is the failure rule for a verify that ended before a running
// replacement: the target's staged artifact and the directive are what the retry
// restores from, so they stay, and the placement this call changed is restored.
//
// The source's export goes as soon as the update has landed, for the reason
// cleanup gives: the retry reads the TARGET's staged copy, so keeping the source's
// only leaks a memory image per attempt.
func (c *migrationCoordinator) keepAfterReplace(ctx context.Context, plan migrationPlan, state *migrationState) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cleanupTimeout)
	defer cancel()

	if state.placement {
		if err := c.restorePlacement(cleanupCtx, plan, state); err != nil {
			log.G(ctx).WithError(err).Warn("Migration: could not restore the service placement after a failed verify")
		}
		if err := c.broker.Discard(cleanupCtx, plan.task.NodeID, plan.task, state.artifact); err != nil {
			log.G(ctx).WithError(err).
				Warn("Migration: could not discard the source's export after a failed verify")
		}
	}
	if err := c.spool.Remove(state.artifact.ID); err != nil {
		log.G(ctx).WithError(err).Debug("Migration: could not remove the spool entry after a failed verify")
	}
}

// cleanup is the success path's last step. Restoring the placement, discarding the
// source's export and removing the spool entry are best effort: the migration
// already landed, and a cleanup that failed must not turn a moved service into an
// error the caller would retry. What could not be done is summarised in the phase's
// message - the failure's code and reason, never its text, which names this node's
// or a peer's paths - and logged in full here.
//
// What the cleanup undoes is decided by whether the update landed, not by the
// outcome. state.placement is exactly that: it is set once the service carries the
// pinned constraint, including the committed-but-unacknowledged case. The source's
// export goes as soon as the update has landed, because from that instant the retry
// is the replacement's own restore and it reads the TARGET's staged artifact - the
// bytes prepare staged there, named by the manifest prepare wrote beside them - not
// the source's directory. Keeping the export "for the retry" leaked it: on the lab a
// pending move left 600 MB of guest memory per attempt on the source node, 1.8 GB
// after three, while every retry restored from the target and none read them. A
// move that never landed keeps the export, which is then the only copy - the abandon
// path owns that case and discards it itself.
//
// A moved guest is also re-annealed here: every node - the target included, because
// it still holds the stale port entry - forgets the guest's neighbour and FDB
// entries, so its next frame re-learns the MAC on the node that now runs it. A
// pending move re-anneals nothing: where the old task never went away the guest is
// still on the source, and stripping the entries that point there would only add to
// the outage; where it did, there is no running guest on the target to learn yet. The
// re-anneal runs before the export is discarded (a retry may still need its bytes if
// this call's answer is lost) and after the placement is restored, and it shares this
// step's one-minute budget so the caller's wait keeps the bound the route documents.
func (c *migrationCoordinator) cleanup(
	ctx context.Context, plan migrationPlan, state *migrationState, outcome string,
) string {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cleanupTimeout)
	defer cancel()

	var problems []string
	// Everything below is this call's to undo only because the update landed: the
	// replacement is that service's task and the target holds the retry's copy. A
	// move that never landed has none of it - and its export, the only copy, was
	// already discarded by abandon.
	if state.placement {
		if err := c.restorePlacement(cleanupCtx, plan, state); err != nil {
			log.G(ctx).WithError(err).Warn("Migration: cleanup could not restore the service placement")
			problems = append(problems, "restore the service placement: "+publicFailure(err))
		}
		if outcome == v1.MigrationOutcomeMoved {
			// The guest is up on the target, so every node must forget where it used
			// to be - the target included, because its overlay port still maps the
			// guest's MAC to the source's VTEP. A node that fails is logged and does
			// not join problems: the move happened, and reachability recovers on the
			// next ARP either way. It runs before the export is discarded, because a
			// retry may still need those bytes if this call's answer is lost.
			c.reannealPeers(cleanupCtx, plan, state)
		}
		if err := c.broker.Discard(cleanupCtx, plan.task.NodeID, plan.task, state.artifact); err != nil {
			log.G(ctx).WithError(err).Warn("Migration: cleanup could not discard the artifact on the source")
			problems = append(problems, "discard the artifact on the source: "+publicFailure(err))
		}
	}
	if err := c.spool.Remove(state.artifact.ID); err != nil {
		log.G(ctx).WithError(err).Warn("Migration: cleanup could not remove the spool entry")
		problems = append(problems, "remove the spool entry: "+publicFailure(err))
	}
	return strings.Join(problems, "; ")
}

// reannealPeers tells every node to forget the guest the move just relocated.
//
// A moved guest keeps its MAC (it is the same VM, restored from its own memory
// image), so a node's bridge still has an FDB entry pointing the guest's MAC at the
// old node's VTEP, and its neighbour table still has the guest's address behind that
// MAC. Both age out eventually; until they do, every frame for the guest crosses the
// overlay to the node that no longer has it. Deleting the entries makes the node ask
// again, and the answer now comes from the target.
//
// The target is included, not skipped: it is the node holding the stale port entry
// this operation exists for - its bridge learned the guest's MAC on the overlay port
// before the VM arrived and still points that entry at the source's VTEP, so only
// deleting it there makes the target's own bridge send the guest's frames to its
// local TAP.
//
// One request per IPv4 address - not one request carrying a list - because the
// attachment is what decides the bridge and the address is what decides the neighbour
// entry: one request per (attachment, address) keeps the two together, and a node that
// fails one still forgets the others.
//
// The guest's address comes from the old task's attachments and its MAC from the
// artifact's source task - the VM's own identity, which the snapshot recorded and
// the guest keeps, per interface index. Nothing is read from a device: the
// host-side TAP's MAC is not the guest's.
//
// Every failure is logged per node and attachment. A cluster that cannot list its
// nodes, or a task whose attachments cannot be read, is logged once and the
// migration still answers moved: the guest is up, and reachability recovers on the
// next ARP either way.
func (c *migrationCoordinator) reannealPeers(ctx context.Context, plan migrationPlan, state *migrationState) {
	targets, err := c.reannealTargets(ctx, plan.task, state.artifact.SourceTaskID)
	if err != nil {
		log.G(ctx).WithError(err).Warn(
			"Migration: the moved task's network attachments could not be read, so no node was told to forget the guest")
		return
	}
	if len(targets) == 0 {
		log.G(ctx).Debug("Migration: the moved task carries no IPv4 attachment, so no node has a guest to forget")
		return
	}

	nodes, err := c.client.ListNodes(ctx)
	if err != nil {
		log.G(ctx).WithError(err).Warn("Migration: the node list failed, so the moved guest's peers were not re-annealed")
		return
	}

	for _, node := range nodes {
		for _, target := range targets {
			if err := c.broker.Reanneal(ctx, node.ID, target.networkID, target.ip, target.mac); err != nil {
				log.G(ctx).WithError(err).Warnf(
					"Migration: node %q could not forget the moved guest %s (%s) on network %s; it recovers on its next ARP",
					node.ID, target.ip, target.mac, target.networkID)
			}
		}
	}
}

// reannealTarget is one address of a moved guest, on the attachment that carried it.
type reannealTarget struct {
	// networkID is the attachment's network, which is what each node turns into the
	// bridge the guest was on.
	networkID string
	ip        string
	mac       string
}

// reannealTargets reads the task's attachments and returns one target per IPv4
// address, in attachment order. It reports the read's error rather than guessing:
// without a network ID there is no bridge to forget, and deleting on the node's own
// bridge instead is the failure this operation exists to avoid.
func (c *migrationCoordinator) reannealTargets(
	ctx context.Context, task v1.Task, sourceTaskID string,
) ([]reannealTarget, error) {
	attachments, err := c.client.TaskAttachments(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	return guestTargets(attachments, sourceTaskID), nil
}

// guestTargets is the pure half: which addresses a moved guest holds and the MAC
// each one's interface has. The MAC's index is the attachment index, because that
// is the interface index the VM was configured with (runtime.GuestMAC is the same
// rule the boot path uses).
func guestTargets(attachments []client.TaskAttachment, sourceTaskID string) []reannealTarget {
	var targets []reannealTarget
	for index, attachment := range attachments {
		if strings.TrimSpace(attachment.NetworkID) == "" {
			continue
		}
		mac := scruntime.GuestMAC(sourceTaskID, index)
		for _, address := range attachment.Addresses {
			ip := ipv4Host(address)
			if ip == "" {
				continue
			}
			targets = append(targets, reannealTarget{
				networkID: strings.TrimSpace(attachment.NetworkID),
				ip:        ip,
				mac:       mac,
			})
		}
	}
	return targets
}

// ipv4Host returns the bare address of one allocated CIDR, or "" when it is not an
// IPv4 one. A re-anneal deletes IPv4 neighbour and FDB state; an IPv6 attachment
// has no entry there, so it is skipped rather than sent as a value every node would
// refuse.
func ipv4Host(address string) string {
	host, _, found := strings.Cut(strings.TrimSpace(address), "/")
	if !found {
		host = strings.TrimSpace(address)
	}
	if host == "" || net.ParseIP(host) == nil || strings.Contains(host, ":") {
		return ""
	}
	return host
}

// abandon undoes what the flow did before the service update landed: the target's
// directive first (so nothing restores bytes that are about to be deleted), the
// source's artifact, the spool entry and the placement if it was changed. Each
// step is best effort - the first failure is the caller's error, and a cleanup
// that fails again is logged, never substituted.
//
// The directive removal runs whenever an artifact exists, not only when a prepare was
// acknowledged. A prepare whose answer was lost may have been recorded on the node
// anyway, and leaving it behind would make the service's next task restore an artifact
// this call is about to discard. The removal names the artifact, so it can only remove
// this migration's directive: one that another migration prepared is left alone (and
// logged, not reported), because it is that migration's to keep.
func (c *migrationCoordinator) abandon(
	ctx context.Context, plan migrationPlan, state *migrationState, phase string, cause error,
) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cleanupTimeout)
	defer cancel()

	c.unprepare(cleanupCtx, ctx, plan, state)
	if state.exported {
		if err := c.broker.Discard(cleanupCtx, plan.task.NodeID, plan.task, state.artifact); err != nil {
			log.G(ctx).WithError(err).Warn("Migration: could not discard the source's artifact after abandoning the migration")
		}
	}
	if state.artifact.ID != "" {
		if err := c.spool.Remove(state.artifact.ID); err != nil {
			log.G(ctx).WithError(err).Warn("Migration: could not remove the spool entry after abandoning the migration")
		}
	}
	if state.placement {
		if err := c.restorePlacement(cleanupCtx, plan, state); err != nil {
			log.G(ctx).WithError(err).Warn("Migration: could not restore the service placement after abandoning the migration")
		}
	}

	return migrationPhaseError(phase, cause)
}

// unprepare removes the target's directive for this migration's artifact, if this
// migration produced one. logCtx is the caller's context (it may already be
// cancelled): the removal runs on cleanupCtx, and only the log line is bound to the
// request.
func (c *migrationCoordinator) unprepare(
	cleanupCtx, logCtx context.Context, plan migrationPlan, state *migrationState,
) {
	if state.artifact.ID == "" {
		// Nothing was exported, so nothing can have been prepared for it.
		return
	}
	err := c.broker.Unprepare(cleanupCtx, plan.target.ID, plan.service.ID, state.artifact.ID)
	switch {
	case err == nil:
		return
	case isDirectiveReplaced(err):
		// A newer migration owns the service's directive. Leaving it is the whole
		// point of naming the artifact: deleting it would abandon a move that is
		// still happening.
		log.G(logCtx).Debug("Migration: the target's directive now belongs to a newer migration; leaving it in place")
	default:
		log.G(logCtx).WithError(err).Warn("Migration: could not remove the target's directive after abandoning the migration")
	}
}

// restorePlacement puts the service's placement back. It only acts while the
// constraints are still the ones this migration wrote: a service whose placement
// someone else changed since is their decision, not this migration's to undo.
//
// The revision is read fresh, so the cleanup's If-Match is the current one; a
// conflict - another writer landed between the read and the update - is retried
// once against a fresh read, and then reported.
func (c *migrationCoordinator) restorePlacement(
	ctx context.Context, plan migrationPlan, state *migrationState,
) error {
	var lastErr error
	for attempt := 0; ; attempt++ {
		current, err := c.client.GetService(ctx, plan.service.ID)
		if err != nil {
			return err
		}
		have := constraintsOf(current)
		if constraintsEqual(have, plan.constraints) {
			// Already restored: a retry of the same cleanup is a success.
			return nil
		}
		if !constraintsEqual(have, state.pinned) {
			// Someone else owns the placement now.
			return nil
		}

		patch, err := migrationPlacementPatch(plan.constraints)
		if err != nil {
			return err
		}
		if _, err := c.client.UpdateService(ctx, plan.service.ID, patch, v1.WriteOptions{IfMatch: current.Revision}); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt >= 1 || !isConflict(lastErr) {
			return lastErr
		}
	}
}

// migrationPlacementPatch is the merge patch that replaces the service's placement
// constraints wholesale. A nil or empty list clears them: the service had none
// before this migration, so it must have none after it.
func migrationPlacementPatch(constraints []string) ([]byte, error) {
	placement := map[string]any{"constraints": constraints}
	if len(constraints) == 0 {
		placement["constraints"] = nil
	}
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"placement": placement}})
	if err != nil {
		return nil, output.NewErrorf(output.CodeInternal, "the service placement patch could not be encoded").
			WithDetails(map[string]any{"reason": "patch_unencodable"})
	}
	return patch, nil
}

// constraintsOf returns a copy of a service's placement constraints.
func constraintsOf(service v1.Service) []string {
	if service.Spec.Placement == nil || len(service.Spec.Placement.Constraints) == 0 {
		return nil
	}
	return append([]string(nil), service.Spec.Placement.Constraints...)
}

// constraintsEqual compares two constraint lists in order: a placement is a
// sequence, and a list that was reordered is a list someone rewrote.
func constraintsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// artifactFiles returns the files the manifest describes, in the order they move:
// the state file (which names the devices the target recreates), the memory image,
// then the disk copy when the snapshot shipped one.
func artifactFiles(artifact migration.Artifact) []migration.File {
	files := []migration.File{artifact.StateFile, artifact.MemoryFile}
	if artifact.DiskFile != (migration.File{}) {
		files = append(files, artifact.DiskFile)
	}
	return files
}

// artifactDescribesTask refuses an artifact that answers for another task or
// another service. Export already checked both against the node's answer; checking
// them again here is what keeps a future caller of the transport from staging a
// dump as a replacement for a service it does not describe.
func artifactDescribesTask(artifact migration.Artifact, task v1.Task) error {
	if artifact.RequestedTaskID != task.ID {
		return output.NewErrorf(output.CodeInternal,
			"the exported artifact names task %q, not %q", artifact.RequestedTaskID, task.ID).
			WithDetails(map[string]any{"reason": "artifact_mismatch", "field": "requestedTaskId",
				"want": task.ID, "got": artifact.RequestedTaskID})
	}
	if artifact.ServiceID != task.ServiceID {
		return output.NewErrorf(output.CodeInternal,
			"the exported artifact names service %q, not %q", artifact.ServiceID, task.ServiceID).
			WithDetails(map[string]any{"reason": "artifact_mismatch", "field": "serviceId",
				"want": task.ServiceID, "got": artifact.ServiceID})
	}
	return nil
}

// invalidMigrationField is the 400 for a request field this call cannot use.
func invalidMigrationField(field, reason string) error {
	return output.NewErrorf(output.CodeInvalid, "request field %q %s", field, reason).
		WithDetails(map[string]any{"fields": map[string]any{field: reason}})
}

// migrationPhaseError names the phase a failure happened in without discarding
// the failure's own classification: a timeout stays a timeout, a conflict stays a
// conflict, and the caller gets the phase in details.phase.
//
// A cause that carries no classification of its own is classified here, and the
// answer is what the caller retries on: a context deadline or a cancellation is a
// timeout (the call was bounded, and running it again is the remedy), and anything
// else is internal. An untyped cause becomes neither a 500 with a peer's text in it
// nor a message that names a node-local path: the text stays in the log, and the
// document says only what kind of failure it was.
func migrationPhaseError(phase string, cause error) error {
	var typed *output.Error
	if errors.As(cause, &typed) {
		details := make(map[string]any, len(typed.Details)+1)
		for key, value := range typed.Details {
			details[key] = value
		}
		details["phase"] = phase
		copied := *typed
		copied.Details = details
		return &copied
	}

	code := migrationCauseCode(cause)
	reason := "phase_failed"
	message := "migration " + phase + " failed"
	switch code {
	case output.CodeTimeout:
		reason = "phase_timeout"
		message = "migration " + phase + " did not finish before its deadline"
		if errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
			reason = "phase_cancelled"
			message = "migration " + phase + " was cancelled before it finished"
		}
	}
	return output.NewError(code, message).
		WithDetails(map[string]any{"phase": phase, "reason": reason})
}

// migrationCauseCode classifies a cause that carries no classification of its own.
// A bounded call that ran out of time, and one the caller cancelled, are both a
// timeout: the work may be retried, and a 500 would suggest the manager is broken.
func migrationCauseCode(cause error) output.Code {
	switch {
	case errors.Is(cause, context.DeadlineExceeded), errors.Is(cause, context.Canceled):
		return output.CodeTimeout
	default:
		return output.CodeInternal
	}
}

// publicFailure is how a public document names a failure: its stable code and, when
// it has one, its stable reason. It never carries the failure's text. That text is
// written by this node or a peer - pkg/snapshot names absolute state files,
// pkg/runtime names the directive it could not remove, a broker error names the
// socket it dialled - and none of that belongs in a report a caller may store. The
// text is logged at the point of failure instead.
func publicFailure(err error) string {
	if err == nil {
		return ""
	}
	var typed *output.Error
	if !errors.As(err, &typed) {
		return string(migrationCauseCode(err))
	}
	if reason, ok := typed.Details["reason"].(string); ok && reason != "" {
		return fmt.Sprintf("%s (%s)", typed.Code, reason)
	}
	return string(typed.Code)
}

// isConflict reports whether err is a lost race: SwarmKit's revision conflict is
// the typed conflict the client returns for a stale If-Match.
func isConflict(err error) bool {
	var typed *output.Error
	return errors.As(err, &typed) && typed.Code == output.CodeConflict
}

// isDirectiveReplaced reports whether the target left a directive in place because a
// newer migration prepared the service. It is not a failure of this migration's
// cleanup: there is nothing left of this call's to remove.
func isDirectiveReplaced(err error) bool {
	var typed *output.Error
	return errors.As(err, &typed) && typed.Details["reason"] == "directive_replaced"
}

// beginInFlight reserves a service's migration slot. It is false when another
// migration of the same service is already running.
func (c *migrationCoordinator) beginInFlight(serviceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, busy := c.inFlight[serviceID]; busy {
		return false
	}
	if c.inFlight == nil {
		c.inFlight = make(map[string]struct{})
	}
	c.inFlight[serviceID] = struct{}{}
	return true
}

// endInFlight releases the slot. It runs on every exit, so a failed migration
// never locks its service out of being moved again.
func (c *migrationCoordinator) endInFlight(serviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.inFlight, serviceID)
}

// migrationObservation is one reading of a service's tasks: whether the old task
// is gone, whether a replacement is running on the target, and whether a
// replacement on the target failed.
type migrationObservation struct {
	oldGone     bool
	replacement *v1.Task
	failed      bool
}

// observeReplacement reads one task list. A replacement has to clear four bars: it
// belongs to the service being moved, it is assigned to the target, its ID is not the
// old task's, and it was created at or after since - the instant this migration
// started. The last one is what makes a retry honest: a service can already have a
// running task on this target from an earlier attempt (or from an earlier migration
// that moved it back), and reporting that task as the replacement would call a move
// finished before SwarmKit has created anything.
//
// The field used is CreatedAt, parsed from the task document. Slot would distinguish
// an earlier attempt of the same service here too, but it is only unique per service
// and a task restored from a snapshot keeps the slot it was replaced at; CreatedAt is
// the instant SwarmKit created *this* task, which is the question being asked. A task
// this build cannot date is never believed: waiting until the deadline is the honest
// answer for one.
//
// The replacement must also be running, not merely created: SwarmKit reports a task
// it has only scheduled as new or pending, and a migration is not done until the guest
// answers.
//
// The old task is gone when it is no longer listed or when it is listed in a state
// that no longer runs the workload (see migrationTaskStatesGone): SwarmKit keeps a
// stopped task in the list for its retention window, so its absence is not the
// signal.
//
// A failed task counts only under the same four bars: a service that already has a
// failed task on this target from an earlier attempt has not just failed again, and a
// retry of a migration must not be refused because the previous one is still listed.
func observeReplacement(
	tasks []v1.Task, serviceID, oldTaskID, targetNodeID string, since time.Time,
) migrationObservation {
	observation := migrationObservation{oldGone: true}

	for i := range tasks {
		task := &tasks[i]
		if task.ID == oldTaskID {
			if _, gone := migrationTaskStatesGone[task.State]; !gone {
				observation.oldGone = false
			}
			continue
		}
		if task.NodeID != targetNodeID || task.ServiceID != serviceID {
			continue
		}
		switch task.State {
		case migrationTaskStateRunning:
			if created := taskCreatedAt(*task); observation.replacement == nil && !created.IsZero() && !created.Before(since) {
				observation.replacement = task
			}
		case migrationTaskStateFailed:
			created := taskCreatedAt(*task)
			if !created.IsZero() && !created.Before(since) {
				observation.failed = true
			}
		}
	}
	return observation
}

// taskCreatedAt parses a task's creation instant, or returns the zero time when
// the document does not carry one this build can read.
func taskCreatedAt(task v1.Task) time.Time {
	if task.CreatedAt == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339Nano, task.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return at
}

// managerMigrations builds the manager-side live-migration transport: the broker
// manager relays the bytes, and the spool (a directory under the manager's state
// directory) holds them between the two nodes. Either missing leaves the route
// answering 501 rather than failing startup.
//
// stateDir is the same directory the daemon was started with, so the spool is a
// sibling of the API's own state and survives a restart for the TTL: a retry of a
// pending migration finds the bytes the first call spooled. The store is returned
// so the caller can schedule its sweep; the transport may be nil while the store
// is not, and then the sweep still runs.
//
// self is this node's own hostname - the name the daemon resolved at startup, which
// --hostname overrides. It is not part of the transport: it is what the leadership
// gate compares the swarm leader against, so one manager cannot run a migration that
// another is already running.
func managerMigrations(
	ctx context.Context, stateDir string, c *client.Client, nodes *broker.Manager, self string,
) (*migrationCoordinator, *migration.Store) {
	spool, err := migration.NewStore(migration.StoreConfig{Root: migration.SpoolRoot(stateDir)})
	if err != nil {
		log.G(ctx).WithError(err).Warn("Live migration is off: the manager's migration spool is unavailable")
		return nil, nil
	}
	if nodes == nil {
		log.G(ctx).Warn("Live migration is off: the node broker manager is unavailable, and there is no relay")
		return nil, spool
	}
	return newMigrationCoordinator(c, nodes, spool, self), spool
}

// startMigrationSpoolSweeper removes spool entries nobody is holding: the ones a
// manager crash, a cancelled transfer or a discarded migration context left
// behind. It sweeps once at startup - a restart is exactly when abandoned
// artifacts are found - and then every migrationSpoolSweepInterval until ctx ends,
// so it never outlives the manager that started it.
//
// This is a backstop, not a retention policy: a live transfer is protected by the
// spool's own reservation (pkg/migration's Sweep consults it), and every path this
// package runs removes its entry itself.
func startMigrationSpoolSweeper(ctx context.Context, spool *migration.Store) {
	if spool == nil {
		return
	}

	go func() {
		sweepMigrationSpool(ctx, spool)

		ticker := time.NewTicker(migrationSpoolSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepMigrationSpool(ctx, spool)
			}
		}
	}()
}

// sweepMigrationSpool runs one sweep and reports what it reclaimed. A failure is
// logged, never retried in a loop: the next tick is the retry.
func sweepMigrationSpool(ctx context.Context, spool *migration.Store) {
	removed, err := spool.Sweep()
	if err != nil {
		log.G(ctx).WithError(err).Warn("Migration: the spool could not be swept; it may still hold abandoned entries")
		return
	}
	if removed > 0 {
		log.G(ctx).Infof("Migration: swept %d abandoned spool entries", removed)
	}
}

// migrationRun records the phases as they happen. The zero-value clock is never
// used: newMigrationCoordinator always sets now.
type migrationRun struct {
	now func() time.Time
	// startedAt is the instant the migration began, before the first phase. It dates
	// the replacement a verify may accept: a task created before it is not this
	// call's.
	startedAt time.Time
	phases    []v1.MigrationPhase
}

// start appends a phase and returns the function that finishes it. Every phase is
// appended before its work starts, so a failure still names where it happened, and
// the returned function is called exactly once on every path.
func (r *migrationRun) start(name string) func(result, message string) {
	r.phases = append(r.phases, v1.MigrationPhase{
		Name:      name,
		StartedAt: r.now().UTC().Format(time.RFC3339Nano),
	})
	index := len(r.phases) - 1

	return func(result, message string) {
		r.phases[index].FinishedAt = r.now().UTC().Format(time.RFC3339Nano)
		r.phases[index].Result = result
		r.phases[index].Message = message
	}
}

// document is the report of a migration that landed. It copies the phases so the
// caller cannot rewrite the run's own record.
func (r *migrationRun) document(
	plan migrationPlan, artifact migration.Artifact, outcome string, downtime time.Duration,
) v1.Migration {
	phases := make([]v1.MigrationPhase, len(r.phases))
	copy(phases, r.phases)

	return v1.Migration{
		SchemaVersion: v1.SchemaVersion,
		Kind:          v1.KindMigration,
		ID:            artifact.ID,
		TaskID:        plan.task.ID,
		ServiceID:     plan.service.ID,
		SourceNode:    plan.task.NodeID,
		TargetNode:    plan.target.ID,
		Phases:        phases,
		DowntimeMs:    downtime.Milliseconds(),
		Outcome:       outcome,
	}
}
