package swarmkit

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordedSpan is one span a test tracer saw, including how it ended and what it
// hung under.
type recordedSpan struct {
	id     int
	name   string
	attrs  []types.Attribute
	parent int
	err    error
	ended  bool
}

// spanCtxKey carries a test span the way the SDK carries a real one, so the fake
// reproduces parenting instead of approximating it.
type spanCtxKey struct{}

func contextWithSpan(ctx context.Context, span *recordedSpan) context.Context {
	return context.WithValue(ctx, spanCtxKey{}, span)
}

func spanFromContext(ctx context.Context) *recordedSpan {
	span, _ := ctx.Value(spanCtxKey{}).(*recordedSpan)
	return span
}

// recordingTracer captures spans instead of exporting them, so a test can assert
// what the controller would send to a collector.
type recordingTracer struct {
	mu    sync.Mutex
	spans []*recordedSpan
}

func (r *recordingTracer) newSpan(name string, attrs []types.Attribute, parent *recordedSpan) *recordedSpan {
	r.mu.Lock()
	defer r.mu.Unlock()

	span := &recordedSpan{id: len(r.spans) + 1, name: name, attrs: attrs}
	if parent != nil {
		span.parent = parent.id
	}
	r.spans = append(r.spans, span)
	return span
}

func (r *recordingTracer) Start(ctx context.Context, name string, attrs ...types.Attribute) (context.Context, types.EndSpan) {
	span := r.newSpan(name, attrs, spanFromContext(ctx))
	return contextWithSpan(ctx, span), func(err error) {
		span.err = err
		span.ended = true
	}
}

// StartNested mirrors the real adapter: the parent comes from parent, the
// returned context keeps ctx's values.
func (r *recordingTracer) StartNested(ctx, parent context.Context, name string, attrs ...types.Attribute) (context.Context, types.EndSpan) {
	span := r.newSpan(name, attrs, spanFromContext(parent))
	return contextWithSpan(ctx, span), func(err error) {
		span.err = err
		span.ended = true
	}
}

// names lists the spans in the order they were opened.
func (r *recordingTracer) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]string, 0, len(r.spans))
	for _, span := range r.spans {
		out = append(out, span.name)
	}
	return out
}

// find returns the single span with the given name, failing the test otherwise.
func (r *recordingTracer) find(t *testing.T, name string) *recordedSpan {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	var found *recordedSpan
	for _, span := range r.spans {
		if span.name != name {
			continue
		}
		if found != nil {
			t.Fatalf("span %q opened more than once", name)
		}
		found = span
	}
	if found == nil {
		t.Fatalf("span %q was never opened (got %v)", name, r.namesLocked())
	}
	return found
}

// parentOf returns the parent of the named span, or nil.
func (r *recordingTracer) parentOf(t *testing.T, name string) *recordedSpan {
	t.Helper()

	span := r.find(t, name)
	if span.parent == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spans[span.parent-1]
}

func (r *recordingTracer) namesLocked() []string {
	out := make([]string, 0, len(r.spans))
	for _, span := range r.spans {
		out = append(out, span.name)
	}
	return out
}

// attr returns one attribute value, or "" when the span does not carry it.
func (s *recordedSpan) attr(key string) string {
	for _, a := range s.attrs {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

// newTracingController builds a controller the way the executor does — with its
// span attributes captured at construction — around the given tracer.
func newTracingController(tracer types.Tracer, task *api.Task) *Controller {
	ctrl := &Controller{
		task:       task,
		config:     &Config{RootfsDir: "/tmp"},
		imagePrep:  &mockImagePrepSuccess{},
		networkMgr: &mockNetworkManagerFull{},
		vmmMgr:     &mockVMMManagerSuccess{},
		trans:      &MockTaskTranslator{},
		tracer:     tracer,
	}
	ctrl.spanAttrs = taskSpanAttributes(ctrl.convertTask(), ctrl.config.Hostname)
	return ctrl
}

// TestPrepareTracesUnderOneRoot pins the shape of a task's trace: a root span,
// the operation under it, and the image step under that — so "what took the
// time" is answerable from one tree.
func TestPrepareTracesUnderOneRoot(t *testing.T) {
	tracer := &recordingTracer{}
	task := &api.Task{
		ID:        "task-prepare-trace",
		ServiceID: "svc-1",
		NodeID:    "node-1",
		Spec: api.TaskSpec{Runtime: &api.TaskSpec_Container{
			Container: &api.ContainerSpec{Image: "nginx:latest"},
		}},
	}
	ctrl := newTracingController(tracer, task)

	require.NoError(t, ctrl.Prepare(context.Background()))

	assert.Equal(t, []string{SpanTaskLifecycle, SpanTaskPrepare, SpanImagePrepare}, tracer.names())

	assert.Nil(t, tracer.parentOf(t, SpanTaskLifecycle), "the root has no parent")
	require.NotNil(t, tracer.parentOf(t, SpanTaskPrepare))
	assert.Equal(t, SpanTaskLifecycle, tracer.parentOf(t, SpanTaskPrepare).name)
	require.NotNil(t, tracer.parentOf(t, SpanImagePrepare))
	assert.Equal(t, SpanTaskPrepare, tracer.parentOf(t, SpanImagePrepare).name)

	prepare := tracer.find(t, SpanTaskPrepare)
	assert.True(t, prepare.ended, "Prepare returned, so its span must be ended")
	assert.NoError(t, prepare.err)
	assert.Equal(t, "task-prepare-trace", prepare.attr("task.id"))
	assert.Equal(t, "svc-1", prepare.attr("service.id"))
	assert.Equal(t, "node-1", prepare.attr("node.id"))
	assert.Equal(t, "nginx:latest", prepare.attr("image.ref"))
}

// TestEveryPhaseSharesOneRoot covers the reason the root exists: SwarmKit calls
// Prepare, Start and Wait with a fresh context each time, so without it the trace
// view would show unrelated traces for one task.
func TestEveryPhaseSharesOneRoot(t *testing.T) {
	tracer := &recordingTracer{}
	ctrl := newTracingController(tracer, &api.Task{ID: "task-phases"})

	require.NoError(t, ctrl.Prepare(context.Background()))
	require.NoError(t, ctrl.Start(context.Background()))
	require.NoError(t, ctrl.Wait(context.Background()))
	require.NoError(t, ctrl.Remove(context.Background()))

	root := tracer.find(t, SpanTaskLifecycle)
	for _, name := range []string{SpanTaskPrepare, SpanTaskStart, SpanTaskWait, SpanTaskRemove, SpanTaskTranslate, SpanImagePrepare} {
		parent := tracer.parentOf(t, name)
		require.NotNilf(t, parent, "span %q has no parent", name)

		// Walk to the top: everything must end at the single root.
		for parent.name != SpanTaskLifecycle {
			next := tracer.parentOf(t, parent.name)
			require.NotNilf(t, next, "span %q is not attached to the root", parent.name)
			parent = next
		}
		assert.Equal(t, root.id, parent.id)
	}

	assert.True(t, tracer.find(t, SpanTaskLifecycle).ended, "Remove ends the task's root span")
}

// TestPrepareSpanRecordsTheFailure pins that a failed operation is exported as
// failed, not as a success that happened to return early.
func TestPrepareSpanRecordsTheFailure(t *testing.T) {
	tracer := &recordingTracer{}
	ctrl := newTracingController(tracer, &api.Task{ID: "task-fail"})
	ctrl.imagePrep = &mockImagePrepError{err: errors.New("pull failed")}

	err := ctrl.Prepare(context.Background())
	require.Error(t, err)

	prepare := tracer.find(t, SpanTaskPrepare)
	require.Error(t, prepare.err)
	assert.Contains(t, prepare.err.Error(), "pull failed")

	image := tracer.find(t, SpanImagePrepare)
	require.Error(t, image.err)
	assert.Contains(t, image.err.Error(), "pull failed")
}

// TestWaitSpanCoversTheTaskLifetime pins the span that makes a trace readable:
// task.wait is open for as long as the VM runs.
func TestWaitSpanCoversTheTaskLifetime(t *testing.T) {
	tracer := &recordingTracer{}
	ctrl := newTracingController(tracer, &api.Task{ID: "task-wait"})

	require.NoError(t, ctrl.Wait(context.Background()))

	wait := tracer.find(t, SpanTaskWait)
	assert.True(t, wait.ended)
	assert.Equal(t, "task-wait", wait.attr("task.id"))
}

// TestStartNestedKeepsTheCallerContext pins why the interface has two methods:
// the span parents to the task's root, but the work still runs under the context
// the caller passed, so SwarmKit's cancellation is not lost.
func TestStartNestedKeepsTheCallerContext(t *testing.T) {
	tracer := &recordingTracer{}
	ctrl := newTracingController(tracer, &api.Task{ID: "task-ctx"})

	// The root exists already, as it does for any task past its first phase.
	ctrl.rootCtx, ctrl.rootEnd = tracer.Start(context.Background(), SpanTaskLifecycle)
	root := tracer.find(t, SpanTaskLifecycle)

	type key struct{}
	callerCtx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "kept"))
	cancel()

	ctx, end := ctrl.startSpan(callerCtx, SpanTaskStart)
	defer end(nil)

	assert.Equal(t, "kept", ctx.Value(key{}), "the caller's values must survive")
	assert.ErrorIs(t, ctx.Err(), context.Canceled, "the caller's cancellation must survive")
	assert.Equal(t, root.id, tracer.find(t, SpanTaskStart).parent)
}

// TestFinishLifecycleIsIdempotent covers the paths that overlap: Shutdown,
// Terminate, Remove and Close can all observe the same task ending.
func TestFinishLifecycleIsIdempotent(t *testing.T) {
	tracer := &recordingTracer{}
	ctrl := newTracingController(tracer, &api.Task{ID: "task-once"})

	ctrl.rootSpan(context.Background())
	require.NotPanics(t, func() {
		ctrl.finishLifecycle(nil)
		ctrl.finishLifecycle(errors.New("again"))
		ctrl.finishLifecycle(nil)
	})
	assert.Len(t, tracer.spans, 1)
}

// TestStartSpanWithoutTracerDoesNotPanic covers every controller that is built
// without tracing — the default, and the way the existing unit tests construct
// one. Span helpers must cost nothing and never panic.
func TestStartSpanWithoutTracerDoesNotPanic(t *testing.T) {
	ctrl := &Controller{task: &api.Task{ID: "task-notrace"}}

	require.NotPanics(t, func() {
		ctx, end := ctrl.startSpan(context.Background(), SpanTaskStart)
		end(nil)
		assert.NotNil(t, ctx)

		_, endChild := ctrl.startChild(context.Background(), SpanImagePrepare)
		endChild(nil)

		ctrl.finishLifecycle(nil)
	})
	assert.Nil(t, ctrl.rootCtx, "a controller without a tracer must not build a root span")
}

// TestTaskSpanAttributesOmitsEmptyValues pins that absent attributes are left
// out: exporting service.id="" would look like a real, empty service to a
// collector, which is worse than no label.
func TestTaskSpanAttributesOmitsEmptyValues(t *testing.T) {
	t.Run("full task", func(t *testing.T) {
		attrs := taskSpanAttributes(&types.Task{
			ID:        "t1",
			ServiceID: "s1",
			NodeID:    "n1",
			Spec:      types.TaskSpec{Runtime: &types.Container{Image: "alpine:3.20"}},
		}, "node-host")

		got := map[string]string{}
		for _, a := range attrs {
			got[a.Key] = a.Value
		}
		assert.Equal(t, map[string]string{
			"node.hostname": "node-host",
			"task.id":       "t1",
			"service.id":    "s1",
			"node.id":       "n1",
			"image.ref":     "alpine:3.20",
		}, got)
	})

	t.Run("standalone VM", func(t *testing.T) {
		attrs := taskSpanAttributes(&types.Task{ID: "t2"}, "node-host")

		got := map[string]string{}
		for _, a := range attrs {
			got[a.Key] = a.Value
		}
		assert.Equal(t, map[string]string{"node.hostname": "node-host", "task.id": "t2"}, got)
	})

	t.Run("nil task", func(t *testing.T) {
		attrs := taskSpanAttributes(nil, "node-host")
		require.Len(t, attrs, 1)
		assert.Equal(t, "node.hostname", attrs[0].Key)
	})
}
