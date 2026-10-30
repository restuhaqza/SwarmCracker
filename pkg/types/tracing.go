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
	// Start opens a span named name and returns a context carrying it, so a
	// nested Start becomes its child, plus the function that ends it.
	Start(ctx context.Context, name string, attrs ...Attribute) (context.Context, EndSpan)
}

// NoopTracer is the tracer used when tracing is disabled: it opens no span,
// exports nothing and allocates no context.
type NoopTracer struct{}

// Start implements Tracer.
func (NoopTracer) Start(ctx context.Context, _ string, _ ...Attribute) (context.Context, EndSpan) {
	return ctx, func(error) {}
}
