package broker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/metrics"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// recordMetricsSample publishes one in-guest sample through the same accessor
// the daemon's poller uses, and removes it again when the test ends so one test
// cannot answer for another through the package-level cache.
func recordMetricsSample(t *testing.T, taskID, service string) metrics.TaskSample {
	t.Helper()

	metrics.RecordVMTaskMetrics("metrics-test-instance", taskID, service, 12.5, 64<<20, 1234, 5678)
	t.Cleanup(func() { metrics.ClearVMTaskMetrics("metrics-test-instance", taskID) })

	sample, ok := metrics.LastTaskSample(taskID)
	require.True(t, ok, "the sample must be readable after it is recorded")
	return sample
}

// serveMetricsRequest runs the metrics handler once for task and returns the
// response. The handler reads the node's in-process sample cache, so it needs no
// socket directory or sidecar.
func serveMetricsRequest(task Task) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/"+task.ID+"/metrics", nil)
	NewMetricsHandler()(context.Background(), task, rec, req)
	return rec
}

// TestMetricsHandlerServesTheLastSample pins the happy path: the node answers
// with the reading its poller recorded last, in the v1 envelope.
func TestMetricsHandlerServesTheLastSample(t *testing.T) {
	sample := recordMetricsSample(t, "task-metrics-ok", "web")

	rec := serveMetricsRequest(Task{
		ID:         "task-metrics-ok",
		SocketPath: "/var/run/firecracker/task-metrics-ok.sock",
		Running:    true,
	})

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, publicMediaType, rec.Header().Get("Content-Type"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	var got v1.TaskMetrics
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	assert.Equal(t, v1.SchemaVersion, got.SchemaVersion)
	assert.Equal(t, v1.KindTaskMetrics, got.Kind)
	assert.Equal(t, "task-metrics-ok", got.TaskID)
	assert.Equal(t, "web", got.Service)
	assert.InDelta(t, 12.5, got.CPUSeconds, 1e-9)
	assert.Equal(t, uint64(64<<20), got.MemoryBytes)
	assert.Equal(t, uint64(1234), got.NetRxBytes)
	assert.Equal(t, uint64(5678), got.NetTxBytes)
	assert.True(t, sample.ObservedAt.Equal(got.ObservedAt),
		"the node must publish the instant it recorded, %s, not %s", sample.ObservedAt, got.ObservedAt)
}

// TestMetricsHandlerNoSampleIs404 pins "the task is running but has not answered
// yet": a typed not_found with reason no_sample, which the manager reads as
// "skip this replica" rather than as a failure.
func TestMetricsHandlerNoSampleIs404(t *testing.T) {
	rec := serveMetricsRequest(Task{
		ID:         "task-metrics-never-sampled",
		SocketPath: "/var/run/firecracker/task-metrics-never-sampled.sock",
		Running:    true,
	})

	require.Equal(t, http.StatusNotFound, rec.Code)
	doc := decodeErrorDocument(t, rec.Body)
	require.Equal(t, string(codeNotFound), doc.Error.Code)
	assert.Equal(t, "no_sample", doc.Error.Details["reason"])
}

// TestMetricsOperationServesGETOnTheNodeServer pins the server wiring rather than
// the handler: the operation is known to the node's method table, so a GET
// reaches the registered handler and any other method is a typed 405 with Allow
// instead of a 501 that would claim the capability is missing.
func TestMetricsOperationServesGETOnTheNodeServer(t *testing.T) {
	recordMetricsSample(t, "task-1", "web")
	srv := newRouteServer(t, &fakeTaskSource{task: Task{ID: "task-1", Running: true}, found: true})
	require.NoError(t, srv.Handle(MetricsOperation, NewMetricsHandler()))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/tasks/task-1/metrics", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	assert.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
	assert.Equal(t, "method_not_allowed", decodeErrorDocument(t, rec.Body).Error.Details["reason"])
}

// TestMetricsHandlerNotRunningIs404 pins that a task with no live VM answers
// not_running, not no_sample: the two are different answers, and a sample the
// cache still holds (a crashed VM's last reading) must not be served as if the
// task were still reporting.
func TestMetricsHandlerNotRunningIs404(t *testing.T) {
	// A leftover sample: whatever the cache holds, a task with no live VM has
	// nothing current to report.
	recordMetricsSample(t, "task-metrics-stopped", "web")

	cases := []struct {
		name string
		task Task
	}{
		{
			name: "stopped task with a leftover sample",
			task: Task{ID: "task-metrics-stopped", LogPath: "/var/log/task-metrics-stopped.log"},
		},
		{
			name: "log-only task",
			task: Task{ID: "task-metrics-logonly", LogPath: "/var/log/task-metrics-logonly.log"},
		},
		{
			name: "empty task ID",
			task: Task{Running: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveMetricsRequest(tc.task)

			require.Equal(t, http.StatusNotFound, rec.Code)
			doc := decodeErrorDocument(t, rec.Body)
			require.Equal(t, string(codeNotFound), doc.Error.Code)
			assert.Equal(t, "task_not_running", doc.Error.Details["reason"])
			assert.NotContains(t, rec.Body.String(), "/var/",
				"the node must not leak a host path")
		})
	}
}

// metricsTestManager builds a manager that dials the test broker at baseURL. The
// request-ID source is explicit so a test can pin it.
func metricsTestManager(t *testing.T, pki testPKI, baseURL string,
	requestIDFrom func(context.Context) string,
) *Manager {
	t.Helper()

	host, port := brokerTarget(t, baseURL)
	mgr, err := NewManager(ManagerConfig{
		Tasks:         &stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		Nodes:         &stubNodeReader{node: v1.Node{ID: "node-1", Address: host}},
		Port:          port,
		Client:        pki.httpClient(),
		RequestIDFrom: requestIDFrom,
	})
	require.NoError(t, err)
	return mgr
}

// TestMetricsManagerDecodesTheNodeDocument pins the happy path: one GET to the
// node's metrics operation, decoded into the public TaskMetrics DTO.
func TestMetricsManagerDecodesTheNodeDocument(t *testing.T) {
	pki := newTestPKI(t)
	observedAt := time.Date(2026, 10, 30, 12, 0, 0, 123456789, time.UTC)

	var gotPath, gotMethod, gotRequestID string
	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		MetricsOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			gotPath, gotMethod = r.URL.Path, r.Method
			gotRequestID = r.Header.Get(requestIDHeader)
			body, err := encodeDocument(v1.NewTaskMetrics(v1.TaskMetrics{
				TaskID:      "task-1",
				Service:     "web",
				CPUSeconds:  12.5,
				MemoryBytes: 64 << 20,
				NetRxBytes:  1234,
				NetTxBytes:  5678,
				ObservedAt:  observedAt,
			}))
			require.NoError(t, err)
			w.Header().Set("Content-Type", publicMediaType)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		},
	})

	mgr := metricsTestManager(t, pki, baseURL, func(context.Context) string {
		return "req_metrics_abc123"
	})
	got, err := mgr.Metrics(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"})

	require.NoError(t, err)
	require.Equal(t, "/v1/tasks/task-1/metrics", gotPath)
	require.Equal(t, http.MethodGet, gotMethod)
	require.Equal(t, "req_metrics_abc123", gotRequestID, "the caller's request ID must reach the node")
	require.Equal(t, v1.TaskMetrics{
		// The relayed document keeps its envelope: the node's answer is a v1
		// document, and a caller that re-serializes it gets a valid one.
		SchemaVersion: v1.SchemaVersion,
		Kind:          v1.KindTaskMetrics,
		TaskID:        "task-1",
		Service:       "web",
		CPUSeconds:    12.5,
		MemoryBytes:   64 << 20,
		NetRxBytes:    1234,
		NetTxBytes:    5678,
		ObservedAt:    observedAt,
	}, got)
}

// TestMetricsManagerNoSampleIsATypedNotFound pins the contract an autoscaler
// branches on: "no sample yet" travels to the caller as a typed not_found error
// that still carries its reason, so the decision is made on the code rather than
// on a message string.
func TestMetricsManagerNoSampleIsATypedNotFound(t *testing.T) {
	pki := newTestPKI(t)

	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		MetricsOperation: func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
			writeTypedError(w, http.StatusNotFound, output.NewErrorf(codeNotFound,
				"task %q has no metrics sample yet", "task-1").
				WithDetails(map[string]any{"reason": "no_sample"}))
		},
	})

	_, err := metricsTestManager(t, pki, baseURL, nil).
		Metrics(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"})

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, output.CodeNotFound, typed.Code)
	assert.Equal(t, "no_sample", typed.Details["reason"])
	assert.False(t, typed.Retryable)
	require.ErrorContains(t, err, "task-1")
}

// TestMetricsManagerNotRunningIsATypedNotFound pins the other 404: a task with no
// live VM is not running, and its reason reaches the caller unchanged.
func TestMetricsManagerNotRunningIsATypedNotFound(t *testing.T) {
	pki := newTestPKI(t)

	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		MetricsOperation: func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
			writeTypedError(w, http.StatusNotFound, output.NewErrorf(codeNotFound,
				"task %q is not running", "task-1").
				WithDetails(map[string]any{"reason": "task_not_running"}))
		},
	})

	_, err := metricsTestManager(t, pki, baseURL, nil).
		Metrics(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"})

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, output.CodeNotFound, typed.Code)
	assert.Equal(t, "task_not_running", typed.Details["reason"])
}

// TestMetricsManagerRejectsAMalformedDocument proves a node that breaks the wire
// contract is a typed internal failure, not a half-filled TaskMetrics: a zero
// sample is a valid reading, so it cannot stand in for "unreadable".
func TestMetricsManagerRejectsAMalformedDocument(t *testing.T) {
	pki := newTestPKI(t)

	cases := []struct {
		name string
		body string
	}{
		{name: "truncated JSON", body: `{"taskId":`},
		{name: "missing task ID", body: `{"schemaVersion":1,"kind":"TaskMetrics","cpuSeconds":1}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
			_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
				MetricsOperation: func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", publicMediaType)
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, tc.body)
				},
			})

			_, err := metricsTestManager(t, pki, baseURL, nil).
				Metrics(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"})

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			require.Equal(t, output.CodeInternal, typed.Code)
			assert.Equal(t, "node-1", typed.Details["nodeId"])
		})
	}
}

// TestMetricsManagerUnreachableNodeIsTyped proves a transport failure is a
// retryable, stable message that names no internal address, exactly as a health
// read already is.
func TestMetricsManagerUnreachableNodeIsTyped(t *testing.T) {
	port := freePort(t)

	mgr, err := NewManager(ManagerConfig{
		Tasks:  &stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		Nodes:  &stubNodeReader{node: v1.Node{ID: "node-1", Address: "127.0.0.1"}},
		Port:   port,
		Client: &http.Client{Transport: &http.Transport{}},
	})
	require.NoError(t, err)

	_, err = mgr.Metrics(context.Background(), v1.Task{ID: "task-1", NodeID: "node-1"})

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, output.CodeUnavailable, typed.Code)
	require.Equal(t, "node-1", typed.Details["nodeId"])
	require.True(t, typed.Retryable)
	require.NotContains(t, typed.Message, "127.0.0.1",
		"the message must not expose the internal broker address")
}

// TestMetricsManagerTaskWithoutNodeIsUnavailable pins the resolution rule: a task
// the manager cannot place is not dialed anywhere.
func TestMetricsManagerTaskWithoutNodeIsUnavailable(t *testing.T) {
	mgr := newTestManager(t,
		&stubTaskReader{task: v1.Task{ID: "task-1"}},
		&stubNodeReader{node: v1.Node{ID: "node-1", Address: "192.168.1.10"}},
		0,
		&http.Client{},
	)

	_, err := mgr.Metrics(context.Background(), v1.Task{ID: "task-1"})

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, output.CodeUnavailable, typed.Code)
}
