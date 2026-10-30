package tracing

import (
	"context"
	"os"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// instrumentationName names this package in the spans it produces, so a
// collector can tell SwarmCracker's own spans apart from any it inherits.
const instrumentationName = "github.com/restuhaqza/swarmcracker/pkg/tracing"

// providerOptions is the resolved form of Config that newTracerProvider needs.
type providerOptions struct {
	endpoint       string
	insecure       bool
	serviceName    string
	serviceVersion string
	ratio          float64
}

// provider is the slice of the SDK the rest of this package uses, so a test can
// substitute a recording implementation.
type provider struct {
	tracer   types.Tracer
	shutdown func(context.Context) error
}

// newTracerProvider builds the SDK provider and its OTLP/HTTP exporter.
//
// It is a package variable so a test can replace it: the disabled path is
// asserted by checking that this is never reached, which is the property that
// keeps a stock daemon from opening a socket at all.
var newTracerProvider = func(ctx context.Context, opts providerOptions) (provider, error) {
	exporter, err := newExporter(ctx, opts)
	if err != nil {
		return provider{}, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(newResource(opts.serviceName, opts.serviceVersion)),
		// ParentBased keeps a caller's sampling decision when a trace crosses a
		// process boundary, and applies the node's ratio to traces that start
		// here.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(opts.ratio))),
	)

	return provider{
		tracer:   otelTracer{tracer: tp.Tracer(instrumentationName)},
		shutdown: tp.Shutdown,
	}, nil
}

// newExporter builds the OTLP/HTTP span exporter. It is separate from
// newTracerProvider so the two can be asserted independently.
var newExporter = func(ctx context.Context, opts providerOptions) (sdktrace.SpanExporter, error) {
	httpOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(opts.endpoint)}
	if opts.insecure {
		httpOpts = append(httpOpts, otlptracehttp.WithInsecure())
	}
	return otlptracehttp.New(ctx, httpOpts...)
}

// newResource describes this node to the collector.
func newResource(serviceName, serviceVersion string) *resource.Resource {
	attrs := []attribute.KeyValue{
		attribute.String("service.name", serviceName),
		// The hostname is what distinguishes two nodes exporting to one
		// collector; without it their spans are indistinguishable.
		attribute.String("service.instance.id", hostname()),
	}
	if serviceVersion != "" {
		attrs = append(attrs, attribute.String("service.version", serviceVersion))
	}
	return resource.NewSchemaless(attrs...)
}

// hostname is a package variable so a test can pin the instance id.
var hostname = func() string {
	name, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return name
}

// otelTracer adapts the OpenTelemetry tracer to types.Tracer.
type otelTracer struct {
	tracer oteltrace.Tracer
}

// Start implements types.Tracer.
func (o otelTracer) Start(ctx context.Context, name string, attrs ...types.Attribute) (context.Context, types.EndSpan) {
	ctx, span := o.tracer.Start(ctx, name, oteltrace.WithAttributes(toOTelAttributes(attrs)...))
	return ctx, endSpan(span)
}

// StartNested implements types.Tracer: the span parents to parent's span while
// the returned context still comes from ctx, so the caller's cancellation and
// values survive.
func (o otelTracer) StartNested(ctx, parent context.Context, name string, attrs ...types.Attribute) (context.Context, types.EndSpan) {
	if p := oteltrace.SpanFromContext(parent); p.SpanContext().IsValid() {
		ctx = oteltrace.ContextWithSpan(ctx, p)
	}
	return o.Start(ctx, name, attrs...)
}

// endSpan turns span completion into the caller's EndSpan.
func endSpan(span oteltrace.Span) types.EndSpan {
	return func(err error) {
		if err != nil {
			// RecordError keeps the message as an error event; SetStatus is what
			// makes the span show as failed in a trace view. Both are wanted:
			// one carries the detail, the other the verdict.
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// toOTelAttributes converts the package-neutral attributes to the SDK's.
func toOTelAttributes(attrs []types.Attribute) []attribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, attribute.String(a.Key, a.Value))
	}
	return out
}
