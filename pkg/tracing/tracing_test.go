package tracing

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// countingProvider swaps the SDK builder for a counter, so a test can assert the
// disabled path never reaches it. It reports the number of constructions.
func countingProvider(t *testing.T) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	prev := newTracerProvider
	newTracerProvider = func(context.Context, providerOptions) (provider, error) {
		calls.Add(1)
		return provider{tracer: types.NoopTracer{}, shutdown: func(context.Context) error { return nil }}, nil
	}
	t.Cleanup(func() { newTracerProvider = prev })
	return &calls
}

// TestNewDisabledBuildsNoProvider pins the default: tracing off must not
// construct an exporter, because constructing one is what allocates the batches,
// the connection pool and the goroutine that would export.
func TestNewDisabledBuildsNoProvider(t *testing.T) {
	calls := countingProvider(t)

	for _, cfg := range []Config{
		{},                                     // zero value: off
		{Enabled: false, Endpoint: "http://x"}, // explicitly off with an endpoint set
	} {
		p, err := New(context.Background(), cfg)
		require.NoError(t, err)
		assert.False(t, p.Enabled())
		assert.NotNil(t, p.Tracer(), "the tracer must be usable without a branch at the call site")
		require.NoError(t, p.Shutdown(context.Background()))
	}

	assert.Zero(t, calls.Load(), "a disabled config must not construct a provider")
}

// TestDisabledDialsNothing is the egress assertion: with tracing off, pointing
// the config at a live address must not open a connection to it, not even when
// spans are opened and ended.
func TestDisabledDialsNothing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	var accepted atomic.Int64
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()

	p, err := New(context.Background(), Config{Enabled: false, Endpoint: "http://" + ln.Addr().String()})
	require.NoError(t, err)

	ctx, end := p.Tracer().Start(context.Background(), "task.start", types.Attribute{Key: "task.id", Value: "t1"})
	end(nil)
	_ = ctx
	require.NoError(t, p.Shutdown(context.Background()))

	// A TCP connect is not instantaneous even on loopback, but the accept
	// goroutine would have run long before this deadline if one were attempted.
	time.Sleep(150 * time.Millisecond)
	assert.Zero(t, accepted.Load(), "disabled tracing must not dial the configured endpoint")
}

// TestEnabledExportsOneTrace decodes what a collector would receive: one span
// with the caller's name, attributes, and failure status.
func TestEnabledExportsOneTrace(t *testing.T) {
	requests := make(chan *coltrace.ExportTraceServiceRequest, 4)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/traces", r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var req coltrace.ExportTraceServiceRequest
		if assert.NoError(t, proto.Unmarshal(body, &req)) {
			requests <- &req
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, err := New(context.Background(), Config{
		Enabled:     true,
		Endpoint:    srv.URL,
		ServiceName: "swarmd-test",
	})
	require.NoError(t, err)
	require.True(t, p.Enabled())

	_, end := p.Tracer().Start(context.Background(), "task.start",
		types.Attribute{Key: "task.id", Value: "task-42"},
		types.Attribute{Key: "service.id", Value: "svc-7"},
	)
	end(nil)

	// Shutdown flushes the batch processor, so the export request is on the wire
	// by the time it returns.
	require.NoError(t, p.Shutdown(context.Background()))

	select {
	case req := <-requests:
		spans := allSpans(req)
		require.Len(t, spans, 1, "one span was opened, so one must be exported")

		span := spans[0]
		assert.Equal(t, "task.start", span.GetName())

		attrs := map[string]string{}
		for _, kv := range span.GetAttributes() {
			attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
		}
		assert.Equal(t, "task-42", attrs["task.id"])
		assert.Equal(t, "svc-7", attrs["service.id"])

		require.NotEmpty(t, req.GetResourceSpans())
		resourceAttrs := map[string]string{}
		for _, kv := range req.GetResourceSpans()[0].GetResource().GetAttributes() {
			resourceAttrs[kv.GetKey()] = kv.GetValue().GetStringValue()
		}
		assert.Equal(t, "swarmd-test", resourceAttrs["service.name"])
	case <-time.After(5 * time.Second):
		t.Fatal("no trace export reached the collector")
	}
}

// TestEndSpanRecordsFailure pins that an operation's error reaches the trace as
// a failed span; without it every span would look successful.
func TestEndSpanRecordsFailure(t *testing.T) {
	requests := make(chan *coltrace.ExportTraceServiceRequest, 4)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req coltrace.ExportTraceServiceRequest
		if assert.NoError(t, proto.Unmarshal(body, &req)) {
			requests <- &req
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, err := New(context.Background(), Config{Enabled: true, Endpoint: srv.URL})
	require.NoError(t, err)

	_, end := p.Tracer().Start(context.Background(), "task.prepare")
	end(assert.AnError)
	require.NoError(t, p.Shutdown(context.Background()))

	select {
	case req := <-requests:
		spans := allSpans(req)
		require.Len(t, spans, 1)
		assert.Equal(t, tracepb.Status_STATUS_CODE_ERROR, spans[0].GetStatus().GetCode())
		assert.Contains(t, spans[0].GetStatus().GetMessage(), assert.AnError.Error())
	case <-time.After(5 * time.Second):
		t.Fatal("no trace export reached the collector")
	}
}

// TestNewEnabledRejectsBadConfig keeps a misconfigured node from starting with a
// silent export failure.
func TestNewEnabledRejectsBadConfig(t *testing.T) {
	t.Run("missing endpoint", func(t *testing.T) {
		_, err := New(context.Background(), Config{Enabled: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no endpoint")
	})

	t.Run("ratio out of range", func(t *testing.T) {
		_, err := New(context.Background(), Config{Enabled: true, Endpoint: "http://127.0.0.1:4318", SampleRatio: 1.5})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of range")
	})
}

// TestParseEndpoint pins what an operator may type. The exporter takes a bare
// host:port, so anything with a path or no scheme would be exported somewhere
// unintended; refusing it at startup is the loud failure.
func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		hostport string
		insecure bool
		wantErr  string
	}{
		{name: "http", raw: "http://127.0.0.1:4318", hostport: "127.0.0.1:4318", insecure: true},
		{name: "https", raw: "https://collector.internal:4318", hostport: "collector.internal:4318"},
		{name: "trailing slash", raw: "http://localhost:4318/", hostport: "localhost:4318", insecure: true},
		{name: "empty", raw: "", wantErr: "no endpoint"},
		{name: "no scheme", raw: "127.0.0.1:4318", wantErr: "not a URL"},
		{name: "no host", raw: "http://", wantErr: "has no host"},
		{name: "path", raw: "http://localhost:4318/v1/traces", wantErr: "base URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hostport, insecure, err := parseEndpoint(tt.raw)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.hostport, hostport)
			assert.Equal(t, tt.insecure, insecure)
		})
	}
}

// TestShutdownIsSafeOnNilProvider lets the daemon defer Shutdown without
// branching on whether tracing was configured.
func TestShutdownIsSafeOnNilProvider(t *testing.T) {
	var p *Provider
	assert.False(t, p.Enabled())
	assert.NotPanics(t, func() { require.NoError(t, p.Shutdown(context.Background())) })
	assert.NotNil(t, p.Tracer())
}

// TestNoopEndSpanIsIdempotent pins that a double End cannot panic, since a
// defer and an explicit call both ending a span is an easy mistake.
func TestNoopEndSpanIsIdempotent(t *testing.T) {
	_, end := types.NoopTracer{}.Start(context.Background(), "x")
	assert.NotPanics(t, func() {
		end(nil)
		end(assert.AnError)
	})
}

// allSpans flattens an export request. The SDK may split spans across several
// ResourceSpans/ScopeSpans entries, so a test that assumed one array would be
// reading an implementation detail.
func allSpans(req *coltrace.ExportTraceServiceRequest) []*tracepb.Span {
	var spans []*tracepb.Span
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			spans = append(spans, ss.GetSpans()...)
		}
	}
	return spans
}

// TestConcurrentSpansAreExported guards the tracer against a race detector
// failure under the concurrent teardown paths the executor actually has.
func TestConcurrentSpansAreExported(t *testing.T) {
	var mu sync.Mutex
	var spanCount int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req coltrace.ExportTraceServiceRequest
		require.NoError(t, proto.Unmarshal(body, &req))

		mu.Lock()
		spanCount += len(allSpans(&req))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, err := New(context.Background(), Config{Enabled: true, Endpoint: srv.URL})
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, end := p.Tracer().Start(context.Background(), "task.wait")
			end(nil)
		}()
	}
	wg.Wait()
	require.NoError(t, p.Shutdown(context.Background()))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 8, spanCount)
}
