package swarmkit

import (
	"context"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// Span names for the control-plane operations a task goes through. They are
// constants so tests can assert on them and a collector can alert on them
// without matching strings that a rename could change silently.
const (
	// SpanTaskLifecycle is the root span of one task. Every other span below is
	// a descendant of it, which is what makes a task readable as one trace
	// instead of five unrelated ones: SwarmKit calls Prepare, Start, Wait and
	// the teardown separately, with a fresh context each time.
	SpanTaskLifecycle = "task.lifecycle"
	// SpanTaskPrepare covers image preparation and network setup.
	SpanTaskPrepare = "task.prepare"
	// SpanTaskStart covers translation, port publishing and the VM boot.
	SpanTaskStart = "task.start"
	// SpanTaskWait spans the task's running lifetime: SwarmKit calls it once the
	// task is up and it returns when the VM ends, so its duration is the task's.
	SpanTaskWait = "task.wait"
	// SpanTaskShutdown is the graceful stop.
	SpanTaskShutdown = "task.shutdown"
	// SpanTaskTerminate is the forced stop.
	SpanTaskTerminate = "task.terminate"
	// SpanTaskRemove tears the task's resources down.
	SpanTaskRemove = "task.remove"

	// SpanImagePrepare covers turning an OCI image into a root filesystem,
	// usually the longest step of Prepare.
	SpanImagePrepare = "image.prepare"
	// SpanTaskTranslate covers building the Firecracker config from the task.
	SpanTaskTranslate = "task.translate"
)

// startSpan opens a lifecycle-operation span, parented to the task's root span.
//
// The returned context is derived from ctx, not from the root: ctx is what the
// operation actually runs under, so the caller's cancellation still reaches the
// image pull, the VM wait and every other call.
func (c *Controller) startSpan(ctx context.Context, name string) (context.Context, types.EndSpan) {
	if c.tracer == nil {
		return ctx, func(error) {}
	}
	return c.tracer.StartNested(ctx, c.rootSpan(ctx), name, c.spanAttrs...)
}

// startChild opens a span nested under whatever span ctx already carries — used
// for the steps inside an operation (image.prepare inside task.prepare), which
// must not be reparented to the root.
func (c *Controller) startChild(ctx context.Context, name string) (context.Context, types.EndSpan) {
	if c.tracer == nil {
		return ctx, func(error) {}
	}
	return c.tracer.Start(ctx, name, c.spanAttrs...)
}

// rootSpan returns the context carrying the task's root span, opening it on
// first use. The root is created from whichever call comes first; a task whose
// image pull fails before it ever boots still gets a trace with the failure in
// it.
func (c *Controller) rootSpan(ctx context.Context) context.Context {
	c.spanMu.Lock()
	defer c.spanMu.Unlock()

	if c.rootCtx == nil {
		c.rootCtx, c.rootEnd = c.tracer.Start(ctx, SpanTaskLifecycle, c.spanAttrs...)
	}
	return c.rootCtx
}

// finishLifecycle ends the root span, once. It is called by every terminal
// operation because which one comes last depends on how the task ended: Remove
// after a clean scale-down, Shutdown or Terminate when the task is stopped in
// place, and Close when the executor is torn down with tasks still tracked.
func (c *Controller) finishLifecycle(err error) {
	c.spanMu.Lock()
	end := c.rootEnd
	c.rootEnd = nil
	c.spanMu.Unlock()

	if end != nil {
		end(err)
	}
}

// prepareImage turns the task's OCI image into a root filesystem, tracing the
// step separately: it is usually the longest part of Prepare, and without its
// own span a slow boot cannot be told apart from a slow pull.
func (c *Controller) prepareImage(ctx context.Context, task *types.Task) error {
	ctx, endSpan := c.startChild(ctx, SpanImagePrepare)
	err := c.imagePrep.Prepare(ctx, task)
	endSpan(err)
	return err
}

// taskSpanAttributes returns the attributes every span of one task carries.
//
// service.id is empty for a standalone `vm create` VM, so it is omitted rather
// than exported as an empty string, which a collector would treat as a real
// value.
func taskSpanAttributes(task *types.Task, nodeHostname string) []types.Attribute {
	attrs := []types.Attribute{
		{Key: "node.hostname", Value: nodeHostname},
	}
	if task == nil {
		return attrs
	}
	if task.ID != "" {
		attrs = append(attrs, types.Attribute{Key: "task.id", Value: task.ID})
	}
	if task.ServiceID != "" {
		attrs = append(attrs, types.Attribute{Key: "service.id", Value: task.ServiceID})
	}
	if task.NodeID != "" {
		attrs = append(attrs, types.Attribute{Key: "node.id", Value: task.NodeID})
	}
	if container, err := task.Spec.GetContainer(); err == nil && container.Image != "" {
		attrs = append(attrs, types.Attribute{Key: "image.ref", Value: container.Image})
	}
	return attrs
}
