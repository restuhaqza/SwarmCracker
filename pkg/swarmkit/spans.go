package swarmkit

import (
	"context"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// Span names for the control-plane operations a task goes through. They are
// constants so tests can assert on them and a collector can alert on them
// without matching strings that a rename could change silently.
const (
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

// startSpan opens a control-plane span for the task this controller owns. The
// attributes are captured once, when the controller is created, so this never
// reads c.task (which Update may reassign) and needs no lock — a span cannot
// deadlock a teardown that already holds c.mu.
//
// A controller built without a tracer (the default, and every test's) gets a
// no-op span: the call site never branches on whether tracing is on.
func (c *Controller) startSpan(ctx context.Context, name string) (context.Context, types.EndSpan) {
	if c.tracer == nil {
		return ctx, func(error) {}
	}
	return c.tracer.Start(ctx, name, c.spanAttrs...)
}

// prepareImage turns the task's OCI image into a root filesystem, tracing the
// step separately: it is usually the longest part of Prepare, and without its
// own span a slow boot cannot be told apart from a slow pull.
func (c *Controller) prepareImage(ctx context.Context, task *types.Task) error {
	ctx, endSpan := c.startSpan(ctx, SpanImagePrepare)
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
