// Package tracing owns the optional OpenTelemetry trace exporter.
//
// Tracing is off by default. A disabled Config builds no exporter, dials nothing
// and allocates nothing, so a stock install behaves exactly as it did before
// tracing existed; an enabled Config builds one OTLP/HTTP exporter and a batch
// span processor.
//
// The daemon's task lifecycle never imports an OpenTelemetry package: it talks to
// types.Tracer, and this package is the only implementation. That keeps the
// dependency at one seam, so removing or swapping the exporter is a change here
// and nowhere else.
package tracing

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// Defaults applied when a Config leaves a field unset.
const (
	// DefaultServiceName labels every exported span with the component that
	// produced it.
	DefaultServiceName = "swarmd-firecracker"
	// DefaultSampleRatio exports every trace. A daemon the size of a node's
	// agent produces few control-plane spans, so head sampling at 100% is the
	// useful default rather than the expensive one.
	DefaultSampleRatio = 1.0
	// ShutdownTimeout bounds the final flush. Exiting is more important than a
	// complete export, so a collector that has gone away cannot delay shutdown.
	ShutdownTimeout = 5 * time.Second
)

// Config configures the trace exporter.
type Config struct {
	// Enabled turns export on. False is the default and the only state in which
	// no exporter is constructed.
	Enabled bool
	// Endpoint is the OTLP/HTTP base URL of the collector, for example
	// http://127.0.0.1:4318. The exporter appends /v1/traces. An http:// URL
	// exports without TLS, which is what a sidecar or a lab collector on a
	// private network wants; https:// verifies the collector's certificate
	// against the system roots.
	Endpoint string
	// ServiceName is the resource's service.name. Empty means
	// DefaultServiceName.
	ServiceName string
	// ServiceVersion is the resource's service.version, omitted when empty.
	ServiceVersion string
	// SampleRatio is the fraction of traces to export, in (0, 1]. Zero (unset)
	// means DefaultSampleRatio.
	SampleRatio float64
}

// Provider owns the exporter and the SDK it was built with.
//
// A Provider is always usable: with tracing disabled it holds a no-op tracer and
// a no-op shutdown, so the daemon can defer Shutdown unconditionally.
type Provider struct {
	tracer   types.Tracer
	enabled  bool
	shutdown func(context.Context) error
}

// New builds a Provider from cfg.
//
// With cfg.Enabled false it constructs no exporter and attempts no connection;
// nothing can fail. With it true, an unparseable endpoint or an exporter that
// cannot be built is an error: a node that was asked to export traces should not
// start silently exporting none.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if !cfg.Enabled {
		return &Provider{tracer: types.NoopTracer{}}, nil
	}

	endpoint, insecure, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}

	name := cfg.ServiceName
	if name == "" {
		name = DefaultServiceName
	}

	ratio := cfg.SampleRatio
	if ratio <= 0 {
		ratio = DefaultSampleRatio
	}
	if ratio > 1 {
		return nil, fmt.Errorf("tracing sample ratio %v is out of range (0, 1]", cfg.SampleRatio)
	}

	provider, err := newTracerProvider(ctx, providerOptions{
		endpoint:       endpoint,
		insecure:       insecure,
		serviceName:    name,
		serviceVersion: cfg.ServiceVersion,
		ratio:          ratio,
	})
	if err != nil {
		return nil, fmt.Errorf("tracing: %w", err)
	}

	return &Provider{
		tracer:   provider.tracer,
		enabled:  true,
		shutdown: provider.shutdown,
	}, nil
}

// Tracer returns the tracer call sites should use. It is never nil.
func (p *Provider) Tracer() types.Tracer {
	if p == nil {
		return types.NoopTracer{}
	}
	return p.tracer
}

// Enabled reports whether this Provider exports spans.
func (p *Provider) Enabled() bool {
	return p != nil && p.enabled
}

// Shutdown flushes buffered spans and releases the exporter. It is safe on a
// disabled Provider and safe to call once.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.shutdown == nil {
		return nil
	}
	return p.shutdown(ctx)
}

// parseEndpoint splits an OTLP/HTTP base URL into the host:port the exporter
// wants and whether TLS is wanted, rejecting an endpoint that could not work.
//
// The exporter takes a host without a scheme, so anything the operator typed as
// a path or query would be silently ignored; refusing it here is better than
// exporting to the wrong place.
func parseEndpoint(raw string) (hostport string, insecure bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, errors.New("tracing is enabled but no endpoint is set (want e.g. http://127.0.0.1:4318)")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", false, fmt.Errorf("tracing endpoint %q is not a URL: %w", raw, err)
	}
	switch u.Scheme {
	case "http":
		insecure = true
	case "https":
		insecure = false
	default:
		return "", false, fmt.Errorf("tracing endpoint %q must start with http:// or https://", raw)
	}
	if u.Host == "" {
		return "", false, fmt.Errorf("tracing endpoint %q has no host", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return "", false, fmt.Errorf("tracing endpoint %q must be a base URL: the path /v1/traces is appended", raw)
	}
	return u.Host, insecure, nil
}
