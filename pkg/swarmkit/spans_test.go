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

// recordedSpan is one span a test tracer saw, including how it ended.
type recordedSpan struct {
	name  string
	attrs []types.Attribute
	err   error
	ended bool
}

// recordingTracer captures spans instead of exporting them, so a test can assert
// what the controller would send to a collector.
type recordingTracer struct {
	mu    sync.Mutex
	spans []*recordedSpan
}

func (r *recordingTracer) Start(ctx context.Context, name string, attrs ...types.Attribute) (context.Context, types.EndSpan) {
	span := &recordedSpan{name: name, attrs: attrs}

	r.mu.Lock()
	r.spans = append(r.spans, span)
	r.mu.Unlock()

	return ctx, func(err error) {
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
		tracer:     tracer,
	}
	ctrl.spanAttrs = taskSpanAttributes(ctrl.convertTask(), ctrl.config.Hostname)
	return ctrl
}

// TestPrepareTracesAnImageChildSpan pins the shape of a prepare: the operation
// span wraps the image step, so a slow boot can be attributed to a pull.
func TestPrepareTracesAnImageChildSpan(t *testing.T) {
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

	assert.Equal(t, []string{SpanTaskPrepare, SpanImagePrepare}, tracer.names())

	prepare := tracer.find(t, SpanTaskPrepare)
	assert.True(t, prepare.ended, "Prepare returned, so its span must be ended")
	assert.NoError(t, prepare.err)
	assert.Equal(t, "task-prepare-trace", prepare.attr("task.id"))
	assert.Equal(t, "svc-1", prepare.attr("service.id"))
	assert.Equal(t, "node-1", prepare.attr("node.id"))
	assert.Equal(t, "nginx:latest", prepare.attr("image.ref"))
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

// TestStartSpanWithoutTracerDoesNotPanic covers every controller that is built
// without tracing — the default, and the way the existing unit tests construct
// one. Span helpers must cost nothing and never panic.
func TestStartSpanWithoutTracerDoesNotPanic(t *testing.T) {
	ctrl := &Controller{task: &api.Task{ID: "task-notrace"}}

	var (
		ctx context.Context
		end types.EndSpan
	)
	require.NotPanics(t, func() {
		ctx, end = ctrl.startSpan(context.Background(), SpanTaskStart)
		end(nil)
	})
	assert.NotNil(t, ctx)
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
