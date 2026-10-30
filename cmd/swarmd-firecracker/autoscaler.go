package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/moby/swarmkit/v2/log"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/autoscaler"
	"github.com/restuhaqza/swarmcracker/pkg/broker"
	"github.com/restuhaqza/swarmcracker/pkg/ingress"
	"github.com/restuhaqza/swarmcracker/pkg/metrics"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// defaultAutoscalerInterval is how often a manager reconciles autoscaled
// services. Two guest samples (the poller's 15s cadence) land in one interval,
// which is what a scale-down's hysteresis counts, and 30s is short enough that a
// hot service gets its extra replica while the load is still there.
const defaultAutoscalerInterval = 30 * time.Second

// serviceLister, taskLister, scaleApplier and taskMetricsSource are the loop's
// whole outside world, narrowed to the calls it makes. pkg/client.Client
// implements the three control-plane ones and pkg/broker.Manager the fourth, so
// the daemon wires the loop without an adapter and a test wires it without a
// cluster.
type serviceLister interface {
	ListServices(ctx context.Context) ([]v1.Service, error)
}

type taskLister interface {
	ListServiceTasks(ctx context.Context, ref string) ([]v1.Task, error)
}

type scaleApplier interface {
	UpdateService(ctx context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Service, error)
}

type taskMetricsSource interface {
	Metrics(ctx context.Context, task v1.Task) (v1.TaskMetrics, error)
}

// nodeLister is the cluster view the leadership gate reads. It is optional: the
// loop discovers it on the lister it is handed, because *client.Client answers
// both ListServices and ListNodes, and a caller that can only list services
// simply reaches no verdict and acts (see mayReconcile).
type nodeLister interface {
	ListNodes(ctx context.Context) ([]v1.Node, error)
}

// autoscalerLoop samples every policy-carrying service and applies the decision
// the pure engine returns.
//
// The loop is the only part of autoscaling that holds memory, and it holds all of
// it here rather than in package state: one engine State per service (cooldown and
// hysteresis) and one CPU baseline per task (the previous cumulative reading, so
// two reads over a window become cores per replica). Losing that memory on a
// manager restart only costs one interval of sampling — no decision depends on
// history the cluster itself keeps.
//
// A pass never returns an error and never panics on a service: one service's
// failure is logged and the pass moves on, because a loop that stopped at the
// first unreadable service would stop autoscaling the whole cluster.
type autoscalerLoop struct {
	services serviceLister
	tasks    taskLister
	source   taskMetricsSource
	apply    scaleApplier
	// now is the clock Decide reasons against. It is a field so a test can
	// replay ticks, cooldowns and sample windows without sleeping.
	now func() time.Time
	// nodes is the cluster view the leadership gate resolves the leader from.
	// It is nil when the lister cannot list nodes, which is why the gate fails
	// open rather than treating "unknown" as "not the leader".
	nodes nodeLister
	// self is this daemon's node hostname, set once at construction. The gate
	// compares it with the leader's hostname.
	self string
	// gateNotice records that the fail-open reason has been logged, so a cluster
	// that cannot name a leader notes it once instead of every interval.
	gateNotice bool

	// states is the engine's memory, keyed by service ID.
	states map[string]autoscaler.State
	// readings is each task's previous cumulative CPU sample, keyed by service
	// then task. A replica contributes a rate only when it has two readings.
	readings map[string]map[string]cpuReading
	// published records which services have a current-count series, so the loop
	// can delete it when a service stops autoscaling.
	published map[string]struct{}
}

// cpuReading is one replica's last cumulative CPU reading.
type cpuReading struct {
	Seconds float64
	At      time.Time
}

// newAutoscalerLoop builds a loop over the given sources. It starts nothing: the
// caller runs it (see startAutoscaler) or calls pass directly.
//
// Both identity facts the leadership gate needs are resolved here, once: the node
// list from the lister itself when it can provide one, and this process's
// hostname from the OS, which is the name the daemon's own node config uses when
// --hostname is not set.
func newAutoscalerLoop(
	services serviceLister, tasks taskLister, source taskMetricsSource, apply scaleApplier, now func() time.Time,
	opts ...autoscalerOption,
) *autoscalerLoop {
	if now == nil {
		now = time.Now
	}
	loop := &autoscalerLoop{
		services:  services,
		tasks:     tasks,
		source:    source,
		apply:     apply,
		now:       now,
		states:    make(map[string]autoscaler.State),
		readings:  make(map[string]map[string]cpuReading),
		published: make(map[string]struct{}),
	}
	if nodes, ok := services.(nodeLister); ok {
		loop.nodes = nodes
	}
	if name, err := os.Hostname(); err == nil {
		loop.self = name
	}
	for _, opt := range opts {
		opt(loop)
	}
	return loop
}

// autoscalerOption refines identity at construction. Options rather than
// arguments because every one of them has a working default: the loop can name
// itself and reach the node list on its own.
type autoscalerOption func(*autoscalerLoop)

// withNodeName tells the leadership gate which node this process is, using the
// hostname the daemon already resolved (--hostname wins over os.Hostname()).
func withNodeName(name string) autoscalerOption {
	return func(l *autoscalerLoop) {
		if name != "" {
			l.self = name
		}
	}
}

// startAutoscaler runs the manager-side reconcile loop until ctx is cancelled.
//
// It is manager-only and opt-in per service: a worker has no cluster view, and a
// service without a policy is never even listed to its tasks. interval <= 0 uses
// defaultAutoscalerInterval; a nil dependency disables the loop instead of
// panicking on the first tick. Options are identity refinements only, so existing
// callers keep the defaults.
func startAutoscaler(
	ctx context.Context, list serviceLister, tasks taskLister, source taskMetricsSource, apply scaleApplier,
	interval time.Duration, opts ...autoscalerOption,
) {
	if list == nil || tasks == nil || source == nil || apply == nil {
		log.G(ctx).Warn("Autoscaler disabled: a required manager dependency is unavailable")
		return
	}
	if interval <= 0 {
		interval = defaultAutoscalerInterval
	}

	loop := newAutoscalerLoop(list, tasks, source, apply, time.Now, opts...)
	log.G(ctx).Infof("Autoscaler: reconciling autoscaled services every %s", interval)
	go loop.run(ctx, interval)
}

// startManagerAutoscaler starts the loop when the manager has the node broker
// that reads samples.
//
// A nil broker means newBrokerManager failed — no cluster TLS material, or the
// programmatic API (which builds it) is off. Autoscaling is then off too, and the
// warn says so: inheriting a switch silently is how an operator ends up wondering
// why nothing scaled.
func startManagerAutoscaler(
	ctx context.Context, source *broker.Manager, list serviceLister, tasks taskLister, apply scaleApplier,
	nodeName string,
) {
	if source == nil {
		log.G(ctx).Warn("Autoscaling is off: the node broker manager is unavailable, so there is no " +
			"sample source (it is built with the programmatic API, which --api-disabled and missing " +
			"cluster TLS material both turn off)")
		return
	}
	startAutoscaler(ctx, list, tasks, source, apply, defaultAutoscalerInterval, withNodeName(nodeName))
}

// run ticks the loop until ctx is cancelled. The first pass happens one interval
// after startup, not immediately: a manager that has just become leader is still
// converging, and a decision taken against a half-built service list is noise.
func (l *autoscalerLoop) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.pass(ctx)
		}
	}
}

// pass reconciles every service once. A failed list is a failed pass — the next
// tick retries — because without the service list there is nothing to reconcile.
func (l *autoscalerLoop) pass(ctx context.Context) {
	if !l.mayReconcile(ctx) {
		return
	}

	now := l.now()
	services, err := l.services.ListServices(ctx)
	if err != nil {
		log.G(ctx).WithError(err).Warn("Autoscaler: cannot list services; skipping this pass")
		return
	}

	live := make(map[string]struct{}, len(services))
	for _, svc := range services {
		if l.reconcile(ctx, svc, now) {
			live[svc.ID] = struct{}{}
		}
	}
	l.forget(live)
}

// mayReconcile reports whether this manager is the one that should decide this
// pass.
//
// Every manager runs the loop, so on a multi-manager swarm a pass on every one of
// them would sample the same replicas and patch the same service: one write wins,
// the others log a warning, and each manager arms its own cooldown — so the
// managers can disagree about when the next change is allowed. Only the raft
// leader acts; the others return without even listing services.
//
// The gate fails open. A cluster whose node list names no leader, a node list this
// manager cannot read, and a daemon that does not know its own hostname all mean
// "no verdict", and the safe answer to no verdict is to keep autoscaling: a
// manager set that cannot report its builder info must not silently stop scaling
// the whole cluster. The reason is logged once, at debug, because it repeats every
// interval.
func (l *autoscalerLoop) mayReconcile(ctx context.Context) bool {
	if l.nodes == nil {
		l.noteNoVerdict(ctx, "the control client cannot list nodes")
		return true
	}
	nodes, err := l.nodes.ListNodes(ctx)
	if err != nil {
		l.noteNoVerdict(ctx, fmt.Sprintf("the node list failed: %v", err))
		return true
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
		l.noteNoVerdict(ctx, "no node reports itself as the swarm leader")
		return true
	case leader.Hostname == "":
		l.noteNoVerdict(ctx, "the swarm leader has no hostname")
		return true
	case l.self == "":
		l.noteNoVerdict(ctx, "this daemon's hostname is unknown")
		return true
	}

	if leader.Hostname != l.self {
		log.G(ctx).Debugf(
			"Autoscaler: %s is the swarm leader and this node is %s; skipping this pass", leader.Hostname, l.self)
		return false
	}
	return true
}

// noteNoVerdict logs a failed-open gate decision the first time it happens. Once
// is enough: the same missing cluster fact repeats every interval, and a debug
// line per tick would bury the passes that did run.
func (l *autoscalerLoop) noteNoVerdict(ctx context.Context, reason string) {
	if l.gateNotice {
		return
	}
	l.gateNotice = true
	log.G(ctx).Debugf("Autoscaler: cannot tell whether this node is the swarm leader (%s); reconciling anyway", reason)
}

// forget drops the memory of services that no longer autoscale — deleted, or
// still there but with the policy removed. A long-lived manager would otherwise
// keep a cooldown, a hysteresis count and a CPU baseline per service forever,
// and a policy re-applied later would inherit state from before it was removed.
// The current-count gauge goes with them: it describes a live decision, and a
// service that stopped autoscaling has no such decision to report.
func (l *autoscalerLoop) forget(live map[string]struct{}) {
	for id := range l.states {
		if _, ok := live[id]; !ok {
			delete(l.states, id)
		}
	}
	for id := range l.readings {
		if _, ok := live[id]; !ok {
			delete(l.readings, id)
		}
	}
	for id := range l.published {
		if _, ok := live[id]; !ok {
			delete(l.published, id)
			metrics.DeleteServiceReplicas(id)
		}
	}
}

// publish sets the current-count gauge and remembers that the series exists, so
// forget can delete it once the service stops autoscaling.
func (l *autoscalerLoop) publish(serviceID string, replicas int) {
	metrics.RecordServiceReplicas(serviceID, replicas)
	l.published[serviceID] = struct{}{}
}

// reconcile handles one service: policy, pause, initial alignment, samples and
// finally the decision. It reports whether the service carries a usable policy,
// which is what pass keys the loop's memory on.
func (l *autoscalerLoop) reconcile(ctx context.Context, svc v1.Service, now time.Time) bool {
	policy, err := types.AutoscaleFromLabels(svc.Labels)
	if err != nil {
		// A half-written policy is a configuration bug on one service. It is
		// reported and skipped, never acted on with guessed defaults.
		log.G(ctx).WithError(err).Warnf("Autoscaler: service %s declares an invalid autoscale policy; skipping it", svc.ID)
		return false
	}
	if policy == nil {
		return false
	}
	if svc.Spec.Replicas == nil {
		// The policy parser refuses global mode at create, so this is a service
		// whose labels were set by hand. There is no replica count to scale.
		log.G(ctx).Debugf("Autoscaler: service %s is not replicated; skipping it", svc.ID)
		return true
	}

	replicas := int(*svc.Spec.Replicas)
	if replicas == 0 {
		// 0 replicas is the operator's pause. It is neither aligned nor scaled:
		// restarting a paused service is an explicit action. The gauge is still
		// published, so a dashboard shows the pause instead of the count the
		// service ran before it.
		l.publish(svc.ID, 0)
		log.G(ctx).Debugf("Autoscaler: service %s is paused (0 replicas); leaving it alone", svc.ID)
		return true
	}

	// Initial alignment is its own step, and it is not a decision: a service can
	// sit outside its policy because it was created without --replicas or
	// because the policy was tightened later. Moving it to the nearest bound is
	// recorded as alignment, does not move the scale-event counter and does not
	// touch the engine's state, so the first real decision still reasons from a
	// clean cooldown and hysteresis.
	if target, outside := alignmentTarget(replicas, *policy); outside {
		if err := l.scale(ctx, svc, target); err != nil {
			log.G(ctx).WithError(err).Warnf(
				"Autoscaler: cannot align service %s from %d to %d replicas", svc.ID, replicas, target)
			return true
		}
		l.publish(svc.ID, target)
		log.G(ctx).Warnf("Autoscaler: aligned service %s from %d to %d replicas to fit its policy; not counted as a scale event",
			svc.ID, replicas, target)
		return true
	}

	round := l.sample(ctx, svc.ID)
	// The skipped replicas are noted once per service per pass, not once per
	// task: a node that is rebooting must not flood the log for every replica.
	if round.noSample > 0 {
		log.G(ctx).Debugf("Autoscaler: service %s: %d replica(s) reported no sample; skipping them", svc.ID, round.noSample)
	}
	if round.failed > 0 {
		log.G(ctx).WithError(round.firstErr).Debugf(
			"Autoscaler: service %s: %d replica metric read(s) failed", svc.ID, round.failed)
	}
	if len(round.cores) == 0 {
		// No replica reported two readings over a positive window, so this is
		// not "0% busy" — it is "nothing measured". No decision is taken and no
		// series is written.
		return true
	}

	decision, next := autoscaler.Decide(*policy,
		autoscaler.Observation{Replicas: replicas, Cores: round.cores, SampledAt: round.sampledAt},
		l.states[svc.ID], now)

	if decision.Direction == autoscaler.DirectionNone {
		// A hold is the whole outcome, so its state is committed at once: the
		// hysteresis count it carries is the evidence the next tick builds on,
		// and a hold applies no change that could fail.
		l.states[svc.ID] = next
		// A hold reports the observed count unchanged. Publishing it here is
		// what keeps the gauge a current count rather than a last-changed one.
		l.publish(svc.ID, decision.Replicas)
		log.G(ctx).Debugf("Autoscaler: service %s at %d replica(s): %s", svc.ID, decision.Replicas, decision.Reason)
		return true
	}

	// A change is only real once the patch lands. The new state — the cooldown
	// this change arms and the reset hysteresis — is committed after the write,
	// never before: a rejected patch (a conflict, a control-socket blip) that
	// armed the cooldown would cost a whole cooldown of no scaling, and would
	// have thrown away the evidence for a change that never happened.
	//
	// The patch is the decision exactly as the engine returned it: the engine
	// moves by one and owns the bounds and cooldown rules, so the loop never
	// adjusts the number.
	if err := l.scale(ctx, svc, decision.Replicas); err != nil {
		log.G(ctx).WithError(err).Warnf(
			"Autoscaler: cannot scale service %s to %d replicas: %s", svc.ID, decision.Replicas, decision.Reason)
		return true
	}
	l.states[svc.ID] = next
	l.publish(svc.ID, decision.Replicas)
	metrics.RecordServiceScaleEvent(svc.ID, string(decision.Direction))
	log.G(ctx).Infof("Autoscaler: service %s %s: %d replicas (%s)",
		svc.ID, decision.Direction, decision.Replicas, decision.Reason)
	return true
}

// alignmentTarget reports the bound a count outside the policy belongs at. The
// bounds are narrowed the same way autoscaler.Decide narrows them — a floor
// below 1 is raised and a ceiling below the floor collapses to it — so the loop
// and the engine can never disagree about what "inside the policy" means.
func alignmentTarget(replicas int, policy types.AutoscalePolicy) (int, bool) {
	floor, ceiling := policy.Min, policy.Max
	if floor < 1 {
		floor = 1
	}
	if ceiling < floor {
		ceiling = floor
	}
	switch {
	case replicas < floor:
		return floor, true
	case replicas > ceiling:
		return ceiling, true
	default:
		return replicas, false
	}
}

// scale applies one replica count through the control API.
//
// The write is conditional on the revision of the service the decision was made
// from (svc, as this pass listed it), because the local client path refuses an
// unconditional service write: it requires an If-Match precondition and answers
// "precondition_required" without one. Passing this pass's revision makes the
// patch a compare-and-swap — a pass that lost the race to another writer gets a
// conflict instead of overwriting a change it never saw, and the next pass
// retries from a fresh list. The revision is deliberately not cached and not
// retried here: one pass, one attempt, and the list is the only source of truth.
func (l *autoscalerLoop) scale(ctx context.Context, svc v1.Service, replicas int) error {
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))
	_, err := l.apply.UpdateService(ctx, svc.ID, patch, v1.WriteOptions{IfMatch: svc.Revision})
	return err
}

// sampleRound is one service's round of per-replica readings.
type sampleRound struct {
	// cores holds one cores-per-replica reading per replica that contributed
	// one, already divided by its own window.
	cores []float64
	// sampledAt is the newest sample in the round, carried into the decision's
	// observation.
	sampledAt time.Time
	// noSample counts replicas whose node answered not_found (no cached
	// sample); failed counts every other read error. Both are skipped, and both
	// are counted so one line per service can note them per pass.
	noSample int
	failed   int
	firstErr error
}

// sample reads every replica of one service and turns the cumulative counters
// into cores per replica.
//
// Cores per replica is (sample - previous) / (sampleTime - previousTime) in
// seconds. A replica with no previous reading contributes nothing on its first
// sighting — one cumulative number names no load — and a window that is zero or
// negative contributes nothing either, because it describes no interval. A
// replica's reading is remembered even when the round ends up unused, so a
// single missed tick widens the next window instead of restarting it.
func (l *autoscalerLoop) sample(ctx context.Context, serviceID string) sampleRound {
	tasks, err := l.tasks.ListServiceTasks(ctx, serviceID)
	if err != nil {
		log.G(ctx).WithError(err).Warnf("Autoscaler: cannot list tasks for service %s; skipping it", serviceID)
		return sampleRound{}
	}

	readings, ok := l.readings[serviceID]
	if !ok {
		readings = make(map[string]cpuReading)
		l.readings[serviceID] = readings
	}

	var round sampleRound
	seen := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		// Only a running replica burns the service's CPU. A task that is
		// shutting down, failed or complete is history: sampling it would keep
		// a request per dead task per interval, forever, and its frozen counter
		// would poison the average with a stale rate. A stopped task is not
		// remembered (see the prune below), so it costs nothing at all.
		if task.State != ingress.TaskStateRunning {
			continue
		}
		// A task with no ID or no node has nothing to ask; the broker would
		// answer "not assigned", which is not a sample.
		if task.ID == "" || task.NodeID == "" {
			continue
		}
		seen[task.ID] = struct{}{}

		reading, err := l.source.Metrics(ctx, task)
		if err != nil {
			if isNoSample(err) {
				round.noSample++
			} else {
				round.failed++
				if round.firstErr == nil {
					round.firstErr = err
				}
			}
			continue
		}
		if reading.ObservedAt.IsZero() {
			// A reading with no timestamp gives no window. Remembering it would
			// poison the baseline, so it is dropped and the replica waits for a
			// stamped sample.
			round.failed++
			if round.firstErr == nil {
				round.firstErr = output.NewErrorf(output.CodeInternal,
					"task %q sample has no observedAt", task.ID)
			}
			continue
		}

		previous, hadPrevious := readings[task.ID]
		readings[task.ID] = cpuReading{Seconds: reading.CPUSeconds, At: reading.ObservedAt}

		if reading.ObservedAt.After(round.sampledAt) {
			round.sampledAt = reading.ObservedAt
		}
		if !hadPrevious {
			continue
		}
		window := reading.ObservedAt.Sub(previous.At).Seconds()
		if window <= 0 {
			continue
		}
		// A negative rate (a counter that went backwards, e.g. a rebooted
		// guest) is passed through rather than hidden: Decide treats a negative
		// reading as no data and holds the whole round, which is the honest
		// answer to a broken probe.
		round.cores = append(round.cores, (reading.CPUSeconds-previous.Seconds)/window)
	}

	// A task that is gone or no longer running must not keep a baseline: it
	// contributes to no round, and the map would otherwise grow with every
	// replica ever replaced.
	for taskID := range readings {
		if _, ok := seen[taskID]; !ok {
			delete(readings, taskID)
		}
	}
	return round
}

// isNoSample reports whether err is a node's "this task has no cached sample",
// which the broker relays as a typed not_found. It is the one per-replica error
// that is expected in normal operation: a replica boots, or its poller has not
// run yet.
func isNoSample(err error) bool {
	var typed *output.Error
	if !errors.As(err, &typed) {
		return false
	}
	return typed.Code == output.CodeNotFound
}
