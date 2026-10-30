package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/moby/swarmkit/v2/log"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/autoscaler"
	"github.com/restuhaqza/swarmcracker/pkg/broker"
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
func newAutoscalerLoop(
	services serviceLister, tasks taskLister, source taskMetricsSource, apply scaleApplier, now func() time.Time,
) *autoscalerLoop {
	if now == nil {
		now = time.Now
	}
	return &autoscalerLoop{
		services:  services,
		tasks:     tasks,
		source:    source,
		apply:     apply,
		now:       now,
		states:    make(map[string]autoscaler.State),
		readings:  make(map[string]map[string]cpuReading),
		published: make(map[string]struct{}),
	}
}

// startAutoscaler runs the manager-side reconcile loop until ctx is cancelled.
//
// It is manager-only and opt-in per service: a worker has no cluster view, and a
// service without a policy is never even listed to its tasks. interval <= 0 uses
// defaultAutoscalerInterval; a nil dependency disables the loop instead of
// panicking on the first tick.
func startAutoscaler(
	ctx context.Context, list serviceLister, tasks taskLister, source taskMetricsSource, apply scaleApplier, interval time.Duration,
) {
	if list == nil || tasks == nil || source == nil || apply == nil {
		log.G(ctx).Warn("Autoscaler disabled: a required manager dependency is unavailable")
		return
	}
	if interval <= 0 {
		interval = defaultAutoscalerInterval
	}

	loop := newAutoscalerLoop(list, tasks, source, apply, time.Now)
	log.G(ctx).Infof("Autoscaler: reconciling autoscaled services every %s", interval)
	go loop.run(ctx, interval)
}

// startManagerAutoscaler starts the loop when the manager has the node broker
// that reads samples. A nil broker means newBrokerManager failed, which is
// already reported as "exec disabled"; autoscaling stays off for the same
// reason rather than ticking every interval with nothing to read.
func startManagerAutoscaler(
	ctx context.Context, source *broker.Manager, list serviceLister, tasks taskLister, apply scaleApplier,
) {
	if source == nil {
		return
	}
	startAutoscaler(ctx, list, tasks, source, apply, defaultAutoscalerInterval)
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
		if err := l.scale(ctx, svc.ID, target); err != nil {
			log.G(ctx).WithError(err).Warnf(
				"Autoscaler: cannot align service %s from %d to %d replicas", svc.ID, replicas, target)
			return true
		}
		l.publish(svc.ID, target)
		log.G(ctx).Warnf("Autoscaler: aligned service %s from %d to %d replicas (policy bounds changed); not counted as a scale event",
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
	l.states[svc.ID] = next

	if decision.Direction == autoscaler.DirectionNone {
		// A hold reports the observed count unchanged. Publishing it here is
		// what keeps the gauge a current count rather than a last-changed one.
		l.publish(svc.ID, decision.Replicas)
		log.G(ctx).Debugf("Autoscaler: service %s at %d replica(s): %s", svc.ID, decision.Replicas, decision.Reason)
		return true
	}

	// The patch is the decision exactly as the engine returned it: the engine
	// moves by one and owns the bounds and cooldown rules, so the loop never
	// adjusts the number.
	if err := l.scale(ctx, svc.ID, decision.Replicas); err != nil {
		log.G(ctx).WithError(err).Warnf(
			"Autoscaler: cannot scale service %s to %d replicas: %s", svc.ID, decision.Replicas, decision.Reason)
		return true
	}
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
func (l *autoscalerLoop) scale(ctx context.Context, serviceID string, replicas int) error {
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))
	_, err := l.apply.UpdateService(ctx, serviceID, patch, v1.WriteOptions{})
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
		// A task with no ID or no node has nothing to ask; the broker would
		// answer "not assigned", which is not a sample.
		if task.ID == "" || task.NodeID == "" {
			continue
		}
		seen[task.ID] = struct{}{}

		sample, err := l.source.Metrics(ctx, task)
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
		if sample.ObservedAt.IsZero() {
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
		readings[task.ID] = cpuReading{Seconds: sample.CPUSeconds, At: sample.ObservedAt}

		if sample.ObservedAt.After(round.sampledAt) {
			round.sampledAt = sample.ObservedAt
		}
		if !hadPrevious {
			continue
		}
		window := sample.ObservedAt.Sub(previous.At).Seconds()
		if window <= 0 {
			continue
		}
		// A negative rate (a counter that went backwards, e.g. a rebooted
		// guest) is passed through rather than hidden: Decide treats a negative
		// reading as no data and holds the whole round, which is the honest
		// answer to a broken probe.
		round.cores = append(round.cores, (sample.CPUSeconds-previous.Seconds)/window)
	}

	// A task that is gone must not keep a baseline: it contributes to no round,
	// and the map would otherwise grow with every replica ever replaced.
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
