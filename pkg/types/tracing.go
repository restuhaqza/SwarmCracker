package types

import "context"

// Attribute is one span attribute.
//
// The value is a string so that this interface, and every caller of it, stays
// free of any tracing library's types. An implementation converts to its own
// representation; a caller only ever names a key and a value.
type Attribute struct {
	Key   string
	Value string
}

// EndSpan finishes a span. err is the operation's outcome: nil for success,
// non-nil to record the span as failed. Calling it more than once is harmless.
type EndSpan func(err error)

// Tracer opens spans for control-plane operations.
//
// This is deliberately the whole tracing surface the executor sees. The
// OpenTelemetry SDK lives behind it in pkg/tracing, so the task lifecycle never
// imports an exporter, a propagator or an SDK package, and disabling tracing
// removes the exporter without touching call sites.
type Tracer interface {
	// Start opens a span named name, as a child of whatever span ctx carries,
	// and returns a context carrying the new span plus the function that ends
	// it. The returned context keeps ctx's values and cancellation.
	Start(ctx context.Context, name string, attrs ...Attribute) (context.Context, EndSpan)

	// StartNested opens a span as a child of parent's span while still deriving
	// the new context from ctx.
	//
	// It exists because SwarmKit calls Prepare, Start and Wait with a different
	// context each time: parenting those to one another through ctx alone would
	// mean running the work under a stored context and losing the caller's
	// cancellation. This separates the two: ctx keeps the cancellation, parent
	// carries the span to attach to.
	StartNested(ctx, parent context.Context, name string, attrs ...Attribute) (context.Context, EndSpan)
}

// NoopTracer is the tracer used when tracing is disabled: it opens no span,
// exports nothing and allocates no context.
type NoopTracer struct{}

// Start implements Tracer.
func (NoopTracer) Start(ctx context.Context, _ string, _ ...Attribute) (context.Context, EndSpan) {
	return ctx, func(error) {}
}

// StartNested implements Tracer.
func (n NoopTracer) StartNested(ctx, _ context.Context, _ string, _ ...Attribute) (context.Context, EndSpan) {
	return ctx, func(error) {}
}
