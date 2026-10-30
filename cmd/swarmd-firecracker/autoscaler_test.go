package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/autoscaler"
	"github.com/restuhaqza/swarmcracker/pkg/metrics"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// autoscaleBase is the wall clock every test starts from. The loop's clock is
// injected, so a test can replay a tick, a cooldown and a sample window without
// sleeping.
var autoscaleBase = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// testPolicy parses a real policy through the flag grammar, so the label set the
// loop reads is exactly what `service create --autoscale` stores.
func testPolicy(t *testing.T, spec string) types.AutoscalePolicy {
	t.Helper()
	p, err := types.ParseAutoscaleFlag(spec)
	require.NoError(t, err)
	return p
}

// testService is a replicated service carrying a policy's labels. Its name is
// deliberately not its ID: the series are labelled with the ID the rest of the
// metrics use, so a test that reads the name would fail.
func testService(t *testing.T, id string, replicas int64, policy types.AutoscalePolicy) v1.Service {
	t.Helper()
	return v1.Service{
		ID:     id,
		Name:   id + "-name",
		Labels: policy.Labels(),
		Spec:   v1.ServiceSpec{Mode: "replicated", Replicas: &replicas},
	}
}

// ── fakes ─────────────────────────────────────────────────────────

type fakeServiceLister struct {
	services []v1.Service
	err      error
	calls    int
}

func (f *fakeServiceLister) ListServices(context.Context) ([]v1.Service, error) {
	f.calls++
	return f.services, f.err
}

// countingServiceLister counts passes from a goroutine, so the run-loop test can
// observe that a failed list does not stop the loop.
type countingServiceLister struct {
	calls atomic.Int64
}

func (f *countingServiceLister) ListServices(context.Context) ([]v1.Service, error) {
	f.calls.Add(1)
	return nil, errors.New("control socket unavailable")
}

// fakeClusterLister is a control client that can also list nodes, which is what
// the daemon's *client.Client is: the loop discovers the leadership gate from
// the lister it is given. Embedding the fake service lister keeps the service
// calls observable through the same value.
type fakeClusterLister struct {
	*fakeServiceLister
	nodes     []v1.Node
	nodeErr   error
	nodeCalls int
}

func (f *fakeClusterLister) ListNodes(context.Context) ([]v1.Node, error) {
	f.nodeCalls++
	return f.nodes, f.nodeErr
}

type fakeTaskLister struct {
	byService map[string][]v1.Task
	err       error
	errByID   map[string]error
	queried   []string
}

func (f *fakeTaskLister) ListServiceTasks(_ context.Context, ref string) ([]v1.Task, error) {
	f.queried = append(f.queried, ref)
	if err, ok := f.errByID[ref]; ok {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.byService[ref], nil
}

type appliedPatch struct {
	ref   string
	patch string
	opts  v1.WriteOptions
}

type fakeScaleApplier struct {
	patches []appliedPatch
	err     error
}

func (f *fakeScaleApplier) UpdateService(_ context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Service, error) {
	f.patches = append(f.patches, appliedPatch{ref: ref, patch: string(patch), opts: opts})
	if f.err != nil {
		return v1.Service{}, f.err
	}
	return v1.Service{ID: ref}, nil
}

type fakeMetricsSource struct {
	// samples is keyed by task ID; a missing entry answers the broker's typed
	// not_found, which is what a node without a cached reading does.
	samples map[string]v1.TaskMetrics
	errs    map[string]error
	queried []string
}

func (f *fakeMetricsSource) Metrics(_ context.Context, task v1.Task) (v1.TaskMetrics, error) {
	f.queried = append(f.queried, task.ID)
	if err, ok := f.errs[task.ID]; ok {
		return v1.TaskMetrics{}, err
	}
	sample, ok := f.samples[task.ID]
	if !ok {
		return v1.TaskMetrics{}, output.NewErrorf(output.CodeNotFound, "task %q has no sample", task.ID)
	}
	return sample, nil
}

// autoscaleFixture is one loop plus the fakes it talks to.
type autoscaleFixture struct {
	loop     *autoscalerLoop
	services *fakeServiceLister
	tasks    *fakeTaskLister
	applier  *fakeScaleApplier
	source   *fakeMetricsSource
	// cluster is set by withNodes: the same loop, built over a lister that can
	// also answer ListNodes.
	cluster *fakeClusterLister
	clock   time.Time
}

func newAutoscaleFixture(services ...v1.Service) *autoscaleFixture {
	f := &autoscaleFixture{
		services: &fakeServiceLister{services: services},
		tasks:    &fakeTaskLister{byService: map[string][]v1.Task{}},
		applier:  &fakeScaleApplier{},
		source:   &fakeMetricsSource{samples: map[string]v1.TaskMetrics{}, errs: map[string]error{}},
		clock:    autoscaleBase,
	}
	f.loop = newAutoscalerLoop(f.services, f.tasks, f.source, f.applier, func() time.Time { return f.clock })
	return f
}

// withNodes rebuilds the fixture's loop over a control client that can list
// nodes, as a manager's *client.Client can, and tells it which hostname this
// process answers to. The loop is rebuilt rather than patched so the production
// construction path — a lister that satisfies both interfaces — is what the gate
// tests exercise.
func (f *autoscaleFixture) withNodes(self string, nodes ...v1.Node) {
	f.cluster = &fakeClusterLister{fakeServiceLister: f.services, nodes: nodes}
	f.loop = newAutoscalerLoop(f.cluster, f.tasks, f.source, f.applier, func() time.Time { return f.clock })
	f.loop.self = self
}

// tick runs one reconcile pass at the fixture's clock.
func (f *autoscaleFixture) tick() {
	f.loop.pass(context.Background())
}

// seed gives a service a running replica with a sample taken at the tick clock.
func (f *autoscaleFixture) seed(serviceID string, taskIDs ...string) {
	tasks := make([]v1.Task, 0, len(taskIDs))
	for _, id := range taskIDs {
		tasks = append(tasks, v1.Task{ID: id, ServiceID: serviceID, NodeID: "node-1", State: "running"})
	}
	f.tasks.byService[serviceID] = tasks
}

// sample records one replica reading: cpuSeconds cumulative, taken at observedAt.
func (f *autoscaleFixture) sample(taskID string, cpuSeconds float64, observedAt time.Time) {
	f.source.samples[taskID] = v1.TaskMetrics{
		TaskID:     taskID,
		CPUSeconds: cpuSeconds,
		ObservedAt: observedAt,
	}
}

// rate records a reading that yields the given cores per replica since the
// replica's previous reading, by adding cores × the window to the cumulative
// counter. It is the fixture's version of what a guest reports.
func (f *autoscaleFixture) rate(taskID string, cores float64, at time.Time) {
	previous := f.source.samples[taskID]
	window := at.Sub(previous.ObservedAt).Seconds()
	f.sample(taskID, previous.CPUSeconds+cores*window, at)
}

// hasReplicaSeries reports whether the current-count gauge has a series for one
// service. It reads the registry directly because the gauge is process-wide and
// every test in this file shares it: a per-label read would create the series it
// is asking about, and counting all series would depend on test order.
func hasReplicaSeries(t *testing.T, service string) bool {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "swarmcracker_service_replicas" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "service" && label.GetValue() == service {
					return true
				}
			}
		}
	}
	return false
}

// ── a hot service ─────────────────────────────────────────────────

func TestAutoscalerHotServiceScalesUpOnce(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-hot", 2, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-hot", "task-1", "task-2")

	eventsBefore := testutil.ToFloat64(metrics.ServiceScaleEvents.WithLabelValues("svc-hot", "up"))

	// First sighting: two cumulative readings and no previous reading, so no
	// replica contributes a rate and the engine is not asked anything.
	f.sample("task-1", 10, autoscaleBase)
	f.sample("task-2", 10, autoscaleBase)
	f.tick()
	require.Empty(t, f.applier.patches, "a first sighting must not scale")
	assert.Len(t, f.source.queried, 2, "both replicas were asked for a sample")

	// Second sighting: 27 CPU-seconds over 30s is 0.9 cores per replica, over
	// the 55% band of a 50% target, so the service is hot.
	f.clock = autoscaleBase.Add(30 * time.Second)
	f.rate("task-1", 0.9, f.clock)
	f.rate("task-2", 0.9, f.clock)
	f.tick()

	require.Len(t, f.applier.patches, 1, "a hot service is patched exactly once")
	assert.Equal(t, "svc-hot", f.applier.patches[0].ref, "the patch names the service by ID")
	assert.JSONEq(t, `{"spec":{"replicas":3}}`, f.applier.patches[0].patch)
	assert.Empty(t, f.applier.patches[0].opts, "a scale is an unconditional write")
	assert.Equal(t, eventsBefore+1, testutil.ToFloat64(metrics.ServiceScaleEvents.WithLabelValues("svc-hot", "up")))
	assert.Equal(t, float64(3), testutil.ToFloat64(metrics.ServiceReplicas.WithLabelValues("svc-hot")))

	// A third tick inside the 60s scale-up cooldown holds, so the manager does
	// not chase a busy service one replica per interval.
	f.clock = autoscaleBase.Add(45 * time.Second)
	f.rate("task-1", 3.6, f.clock)
	f.rate("task-2", 3.6, f.clock)
	f.tick()
	require.Len(t, f.applier.patches, 1, "the scale-up cooldown must hold the next tick")
}

// ── a cold service ────────────────────────────────────────────────

func TestAutoscalerColdServiceNeedsTwoIntervals(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-cold", 3, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-cold", "task-1")

	f.sample("task-1", 10, autoscaleBase)
	f.tick()
	require.Empty(t, f.applier.patches, "no rate yet")

	// First cold sample: one of the two samples a scale-down needs.
	f.clock = autoscaleBase.Add(30 * time.Second)
	f.rate("task-1", 0.05, f.clock)
	f.tick()
	require.Empty(t, f.applier.patches, "one cold sample is not enough to scale down")
	assert.Equal(t, float64(3), testutil.ToFloat64(metrics.ServiceReplicas.WithLabelValues("svc-cold")),
		"a hold still publishes the current count for a dashboard")

	// Second consecutive cold sample: the hysteresis is satisfied.
	f.clock = autoscaleBase.Add(60 * time.Second)
	f.rate("task-1", 0.05, f.clock)
	f.tick()

	require.Len(t, f.applier.patches, 1)
	assert.JSONEq(t, `{"spec":{"replicas":2}}`, f.applier.patches[0].patch)
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.ServiceScaleEvents.WithLabelValues("svc-cold", "down")))
	assert.Equal(t, float64(2), testutil.ToFloat64(metrics.ServiceReplicas.WithLabelValues("svc-cold")))
}

// ── a service without a policy ────────────────────────────────────

func TestAutoscalerServiceWithoutPolicyIsNeverQueried(t *testing.T) {
	plain := v1.Service{ID: "svc-plain", Name: "plain", Spec: v1.ServiceSpec{Mode: "replicated"}}
	f := newAutoscaleFixture(plain)
	f.tick()

	assert.Empty(t, f.tasks.queried, "a service without a policy must not have its tasks listed")
	assert.Empty(t, f.source.queried, "a service without a policy must not be sampled")
	assert.Empty(t, f.applier.patches)
}

// ── a replica without a sample ────────────────────────────────────

func TestAutoscalerSkipsReplicaWithoutSample(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-nosample", 2, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-nosample", "task-1", "task-2")

	f.sample("task-1", 10, autoscaleBase)
	f.sample("task-2", 10, autoscaleBase)
	f.tick()

	// task-2's node has no cached sample this tick. Its replica is skipped and
	// the remaining reading still decides the round.
	f.clock = autoscaleBase.Add(30 * time.Second)
	f.rate("task-1", 0.9, f.clock)
	delete(f.source.samples, "task-2")
	f.tick()

	require.Len(t, f.applier.patches, 1, "one replica without a sample must not fail the pass")
	assert.JSONEq(t, `{"spec":{"replicas":3}}`, f.applier.patches[0].patch)
}

func TestAutoscalerNoRatesMeansNoDecision(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-blind", 2, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-blind", "task-1", "task-2")

	eventsBefore := testutil.ToFloat64(metrics.ServiceScaleEvents.WithLabelValues("svc-blind", "up"))

	// Both replicas answer not_found: there is nothing to decide, and "no
	// sample" must never be read as "0% busy".
	f.tick()

	assert.Empty(t, f.applier.patches)
	assert.Equal(t, eventsBefore, testutil.ToFloat64(metrics.ServiceScaleEvents.WithLabelValues("svc-blind", "up")))
	assert.False(t, hasReplicaSeries(t, "svc-blind"), "no decision means no gauge series")
}

// ── only running replicas are sampled ────────────────────────────

func TestAutoscalerStoppedTaskIsNeverQueriedAndPruned(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-stopped", 2, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-stopped", "task-1")

	f.sample("task-1", 10, autoscaleBase)
	f.tick()
	require.NotEmpty(t, f.loop.readings["svc-stopped"], "the running replica left a baseline")
	require.Equal(t, []string{"task-1"}, f.source.queried)

	// The replacement has not started yet: the old task is shutting down, and
	// asking its node for a sample would be a request that cannot answer.
	f.tasks.byService["svc-stopped"] = []v1.Task{
		{ID: "task-1", ServiceID: "svc-stopped", NodeID: "node-1", State: "shutdown"},
	}
	f.clock = autoscaleBase.Add(30 * time.Second)
	f.tick()

	assert.Len(t, f.source.queried, 1, "a task that is not running must cost zero broker requests")
	assert.Empty(t, f.loop.readings["svc-stopped"], "the baseline of a stopped task is pruned with it")
	assert.Empty(t, f.applier.patches, "a service with no running replica has no data to decide on")
}

// ── failures never stop the pass ──────────────────────────────────

func TestAutoscalerNonPositiveWindowContributesNothing(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-samewindow", 2, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-samewindow", "task-1")

	// Two readings stamped at the same instant describe no interval, however
	// much the counter moved between them.
	f.sample("task-1", 10, autoscaleBase)
	f.tick()
	f.sample("task-1", 40, autoscaleBase)
	f.tick()

	assert.Empty(t, f.applier.patches, "a zero window is not a rate")
}

func TestAutoscalerCounterResetIsNoData(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-reset", 2, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-reset", "task-1")

	// A guest that rebooted reports a smaller cumulative counter, so the rate is
	// negative. Read as "0% busy" that would be a cold sample, and two cold
	// samples in a row scale a service down — so the test drives two negative
	// windows. One would prove nothing: a single cold sample cannot scale down
	// whatever the round is called.
	f.sample("task-1", 500, autoscaleBase)
	f.tick()

	for tick, cold := range []struct {
		at  time.Time
		cpu float64
	}{
		{autoscaleBase.Add(30 * time.Second), 2},
		{autoscaleBase.Add(60 * time.Second), 1},
	} {
		f.sample("task-1", cold.cpu, cold.at)
		f.clock = cold.at
		f.tick()
		assert.Empty(t, f.applier.patches, "tick %d: a counter reset must not scale a service", tick+1)
	}

	assert.Equal(t, float64(0), testutil.ToFloat64(metrics.ServiceScaleEvents.WithLabelValues("svc-reset", "down")),
		"two negative windows misread as 0% busy would have patched down")
	assert.Equal(t, autoscaler.State{}, f.loop.states["svc-reset"],
		"no data must not advance the engine state: a cold-sample count or a cooldown would show here")
}

func TestAutoscalerListErrorDoesNotStopTheLoop(t *testing.T) {
	f := newAutoscaleFixture()
	f.services.err = errors.New("control socket unavailable")

	// A failing list is a failed pass, not a failed loop: the pass returns and
	// the next one runs.
	f.tick()
	f.tick()
	assert.Equal(t, 2, f.services.calls)
	assert.Empty(t, f.applier.patches)
}

func TestAutoscalerRunLoopSurvivesListErrors(t *testing.T) {
	lister := &countingServiceLister{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startAutoscaler(ctx, lister, &fakeTaskLister{}, &fakeMetricsSource{}, &fakeScaleApplier{}, 5*time.Millisecond)

	require.Eventually(t, func() bool { return lister.calls.Load() >= 2 }, 2*time.Second, 5*time.Millisecond,
		"the loop must keep reconciling after a failed list")
}

func TestAutoscalerTaskListErrorSkipsOnlyThatService(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	broken := testService(t, "svc-broken", 2, policy)
	healthy := testService(t, "svc-after", 2, policy)
	f := newAutoscaleFixture(broken, healthy)
	f.seed("svc-after", "task-1")
	f.tasks.errByID = map[string]error{"svc-broken": errors.New("task store unavailable")}

	f.sample("task-1", 10, autoscaleBase)
	f.tick()
	assert.Empty(t, f.applier.patches, "the first tick only seeds the reading")

	f.clock = autoscaleBase.Add(30 * time.Second)
	f.rate("task-1", 0.9, f.clock)
	f.tick()

	require.Len(t, f.applier.patches, 1, "a failing service must not stop the services after it")
	assert.Equal(t, "svc-after", f.applier.patches[0].ref)
}

func TestAutoscalerInvalidPolicyIsSkipped(t *testing.T) {
	// A half-written policy: min and max but no metric or target.
	two := int64(2)
	half := v1.Service{
		ID:   "svc-half",
		Name: "half",
		Labels: map[string]string{
			types.AutoscaleMinLabel: "1",
			types.AutoscaleMaxLabel: "3",
		},
		Spec: v1.ServiceSpec{Mode: "replicated", Replicas: &two},
	}
	f := newAutoscaleFixture(half)
	f.tick()

	assert.Empty(t, f.tasks.queried, "an unparseable policy is not acted on")
	assert.Empty(t, f.applier.patches)
}

func TestAutoscalerPatchFailureIsNotCounted(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-reject", 2, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-reject", "task-1")
	f.applier.err = output.NewError(output.CodeConflict, "revision moved")

	f.sample("task-1", 10, autoscaleBase)
	f.tick()
	f.clock = autoscaleBase.Add(30 * time.Second)
	f.rate("task-1", 0.9, f.clock)
	f.tick()

	require.Len(t, f.applier.patches, 1, "the loop tried the patch")
	assert.Equal(t, float64(0), testutil.ToFloat64(metrics.ServiceScaleEvents.WithLabelValues("svc-reject", "up")),
		"a rejected patch is not a scale event")
}

// A failed patch must not arm the cooldown of the change it attempted to make:
// otherwise one rejected write (a conflict, a control-socket blip) costs a whole
// interval of no scaling, and the service waits out a cooldown for a change that
// never happened.
func TestAutoscalerFailedScaleUpArmsNoCooldown(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-retry-up", 2, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-retry-up", "task-1")

	f.sample("task-1", 10, autoscaleBase)
	f.tick()

	f.clock = autoscaleBase.Add(30 * time.Second)
	f.rate("task-1", 0.9, f.clock)
	f.applier.err = output.NewError(output.CodeConflict, "revision moved")
	f.tick()
	require.Len(t, f.applier.patches, 1, "the hot tick tried to scale up")
	assert.Equal(t, autoscaler.State{}, f.loop.states["svc-retry-up"],
		"a rejected patch leaves the previous state untouched: no cooldown, no hysteresis")

	// 10s later, well inside the 60s scale-up cooldown the patch would have
	// armed, the still-hot service is patched again.
	f.applier.err = nil
	f.clock = autoscaleBase.Add(40 * time.Second)
	f.rate("task-1", 0.9, f.clock)
	f.tick()

	require.Len(t, f.applier.patches, 2, "a failed patch must not burn the scale-up cooldown")
	assert.JSONEq(t, `{"spec":{"replicas":3}}`, f.applier.patches[1].patch)
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.ServiceScaleEvents.WithLabelValues("svc-retry-up", "up")))
}

func TestAutoscalerFailedScaleDownArmsNoCooldown(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-retry-down", 3, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-retry-down", "task-1")

	f.sample("task-1", 10, autoscaleBase)
	f.tick()

	// Two cold samples satisfy the hysteresis; the second one's patch is
	// rejected.
	f.clock = autoscaleBase.Add(30 * time.Second)
	f.rate("task-1", 0.05, f.clock)
	f.tick()
	f.clock = autoscaleBase.Add(60 * time.Second)
	f.rate("task-1", 0.05, f.clock)
	f.applier.err = output.NewError(output.CodeConflict, "revision moved")
	f.tick()

	require.Len(t, f.applier.patches, 1, "the second cold sample tried to scale down")
	assert.Equal(t, autoscaler.State{BelowTarget: 1}, f.loop.states["svc-retry-down"],
		"a rejected patch must not reset the hysteresis or arm the scale-down cooldown")

	// 10s later, inside the 300s scale-down cooldown, the evidence still stands.
	f.applier.err = nil
	f.clock = autoscaleBase.Add(70 * time.Second)
	f.rate("task-1", 0.05, f.clock)
	f.tick()

	require.Len(t, f.applier.patches, 2, "a failed patch must not burn the scale-down cooldown")
	assert.JSONEq(t, `{"spec":{"replicas":2}}`, f.applier.patches[1].patch)
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.ServiceScaleEvents.WithLabelValues("svc-retry-down", "down")))
}

// ── leadership gate ──────────────────────────────────────────────

func TestAutoscalerSkipsPassWhenAnotherNodeIsLeader(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-follower", 2, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-follower", "task-1")
	f.withNodes("node-follower",
		v1.Node{ID: "node-1", Hostname: "node-leader", Manager: &v1.NodeManager{Leader: true, Reachable: true}},
		v1.Node{ID: "node-2", Hostname: "node-follower", Manager: &v1.NodeManager{Reachable: true}},
	)

	f.tick()

	assert.Zero(t, f.services.calls, "a follower must not even list the services")
	assert.Empty(t, f.tasks.queried, "a follower must not sample")
	assert.Empty(t, f.applier.patches, "only the leader may patch a service")
}

func TestAutoscalerActsWhenThisNodeIsLeader(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-leader", 2, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-leader", "task-1")
	f.withNodes("node-leader",
		v1.Node{ID: "node-1", Hostname: "node-follower", Manager: &v1.NodeManager{Reachable: true}},
		v1.Node{ID: "node-2", Hostname: "node-leader", Manager: &v1.NodeManager{Leader: true, Reachable: true}},
	)

	f.sample("task-1", 10, autoscaleBase)
	f.tick()
	f.clock = autoscaleBase.Add(30 * time.Second)
	f.rate("task-1", 0.9, f.clock)
	f.tick()

	require.Len(t, f.applier.patches, 1, "the leader decides and applies the scale")
	assert.JSONEq(t, `{"spec":{"replicas":3}}`, f.applier.patches[0].patch)
}

// Fail open: a cluster whose node list names no leader (a manager still forming
// its raft, or a read model without manager status) must keep autoscaling rather
// than go quiet.
func TestAutoscalerActsWhenNoNodeIsLeader(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-noleader", 2, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-noleader", "task-1")
	f.withNodes("node-1",
		v1.Node{ID: "node-1", Hostname: "node-1", Manager: &v1.NodeManager{Reachable: true}},
	)

	f.sample("task-1", 10, autoscaleBase)
	f.tick()
	f.clock = autoscaleBase.Add(30 * time.Second)
	f.rate("task-1", 0.9, f.clock)
	f.tick()

	assert.Equal(t, 2, f.services.calls, "an unresolvable leader must not stop the loop")
	require.Len(t, f.applier.patches, 1, "an unresolvable leader must not stop a decision")
}

// Fail open: a node list that cannot be read (the cluster is still forming, or
// this manager cannot read nodes) must not silently disable autoscaling.
func TestAutoscalerActsWhenNodeListFails(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-nodeerr", 2, policy)
	f := newAutoscaleFixture(svc)
	f.withNodes("node-1")
	f.cluster.nodeErr = errors.New("node store unavailable")

	f.tick()

	assert.Equal(t, 1, f.services.calls, "a failed node list must not stop the pass")
}

// ── initial alignment ─────────────────────────────────────────────

func TestAutoscalerAlignsCountBelowMinimum(t *testing.T) {
	policy := testPolicy(t, "min=2,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-below", 1, policy)
	f := newAutoscaleFixture(svc)

	f.tick()

	require.Len(t, f.applier.patches, 1, "a count below the minimum is aligned on the first pass")
	assert.JSONEq(t, `{"spec":{"replicas":2}}`, f.applier.patches[0].patch)
	assert.Equal(t, float64(0), testutil.ToFloat64(metrics.ServiceScaleEvents.WithLabelValues("svc-below", "up")),
		"alignment is not a load-driven scale event")
	assert.Equal(t, float64(2), testutil.ToFloat64(metrics.ServiceReplicas.WithLabelValues("svc-below")))
	assert.Empty(t, f.tasks.queried, "alignment does not need a sample")
}

func TestAutoscalerAlignsCountAboveMaximum(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-above", 7, policy)
	f := newAutoscaleFixture(svc)
	f.tick()

	require.Len(t, f.applier.patches, 1)
	assert.JSONEq(t, `{"spec":{"replicas":5}}`, f.applier.patches[0].patch)
	assert.Equal(t, float64(0), testutil.ToFloat64(metrics.ServiceScaleEvents.WithLabelValues("svc-above", "down")))
}

func TestAutoscalerAlignedCountIsLeftAlone(t *testing.T) {
	policy := testPolicy(t, "min=2,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-inside", 3, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-inside", "task-1")

	f.tick()

	assert.Empty(t, f.applier.patches, "a count inside the policy needs no alignment")
	assert.Equal(t, []string{"svc-inside"}, f.tasks.queried, "the loop went on to sample the service")
}

// ── a paused service ─────────────────────────────────────────────

func TestAutoscalerPausedServiceIsLeftAlone(t *testing.T) {
	policy := testPolicy(t, "min=2,max=5,metric=cpu,target=50")
	// Replicas 0 is the operator's pause, even though it is below min.
	svc := testService(t, "svc-paused", 0, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-paused", "task-1")
	f.sample("task-1", 10, autoscaleBase)

	f.tick()

	assert.Empty(t, f.applier.patches, "a paused service is never aligned or scaled")
	assert.Empty(t, f.tasks.queried, "a paused service is not sampled")
	require.True(t, hasReplicaSeries(t, "svc-paused"),
		"the pause is published so a dashboard does not keep the pre-pause count")
	assert.Equal(t, float64(0), testutil.ToFloat64(metrics.ServiceReplicas.WithLabelValues("svc-paused")))
}

// ── the loop's own lifecycle ─────────────────────────────────────

func TestAutoscalerForgetsStateOfServiceWithoutPolicy(t *testing.T) {
	policy := testPolicy(t, "min=1,max=5,metric=cpu,target=50")
	svc := testService(t, "svc-forget", 2, policy)
	f := newAutoscaleFixture(svc)
	f.seed("svc-forget", "task-1")
	f.sample("task-1", 10, autoscaleBase)

	f.tick()
	require.NotEmpty(t, f.loop.readings["svc-forget"], "the first tick records a baseline")
	f.loop.publish("svc-forget", 2) // as if an earlier decision had published it
	require.True(t, hasReplicaSeries(t, "svc-forget"))

	// The operator removed the policy: the loop stops reconciling the service,
	// drops its memory and stops publishing a count.
	f.services.services[0].Labels = nil
	f.tick()

	assert.Empty(t, f.loop.readings, "the CPU baseline is dropped with the policy")
	assert.Empty(t, f.loop.states, "the engine state is dropped with the policy")
	assert.False(t, hasReplicaSeries(t, "svc-forget"), "the current-count series is deleted with the policy")
}

func TestAutoscalerIgnoresUnreplicatedService(t *testing.T) {
	policy := testPolicy(t, "min=2,max=5,metric=cpu,target=50")
	svc := v1.Service{
		ID:     "svc-global",
		Name:   "global",
		Labels: policy.Labels(),
		Spec:   v1.ServiceSpec{Mode: "global"},
	}
	f := newAutoscaleFixture(svc)
	f.tick()

	assert.Empty(t, f.applier.patches, "a service without a replica count cannot be scaled")
}

func TestAutoscalerStartDefaultsInterval(t *testing.T) {
	assert.Equal(t, 30*time.Second, defaultAutoscalerInterval)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A cancelled context must not keep the goroutine alive, and a zero interval
	// must fall back to the default rather than panic in time.NewTicker.
	lister := &fakeServiceLister{}
	startAutoscaler(ctx, lister, &fakeTaskLister{}, &fakeMetricsSource{}, &fakeScaleApplier{}, 0)
	assert.Zero(t, lister.calls)
}
