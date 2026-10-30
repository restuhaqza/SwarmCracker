package broker

// This file owns the node-side metrics handler: GET /v1/tasks/{taskID}/metrics
// on the internal broker.
//
// Metrics has no new source of truth. The daemon's per-VM poller already samples
// the guest's agent every interval and caches the reading in pkg/metrics, so the
// node handler answers from that cache. The manager therefore reads the last
// sample the node recorded without scraping Prometheus, dialing the guest, or
// holding a second credential.
//
// The two "no metrics" answers are deliberately distinct:
//   - 404 with details.reason "no_sample": the task is running but its guest has
//     not answered yet (still booting, or an image whose agent predates this
//     operation). This is not a failure: an autoscaler skips the replica for
//     this round.
//   - 404 with details.reason "task_not_running": the task has no live VM, so
//     there is no current reading. A sample the cache still holds (a crashed
//     VM's last one) describes a VM that is gone and is never served as if it
//     were current.
//
// Nothing here is audited or cached at this layer: a metrics read is one map
// lookup. The reading itself is bounded by the poller's interval, which is the
// guest's own sampling cadence.

import (
	"context"
	"net/http"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/metrics"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// MetricsOperation is the broker operation name for a metrics read. The manager
// resolves its route with it, so the node route and the manager call cannot
// drift apart.
const MetricsOperation = "metrics"

// NewMetricsHandler builds the metrics operation. It reads the in-process sample
// cache the daemon's poller fills, so it needs no dialer and no node-local
// configuration.
func NewMetricsHandler() Handler {
	return func(_ context.Context, task Task, w http.ResponseWriter, _ *http.Request) {
		serveMetrics(task, w)
	}
}

// serveMetrics answers one metrics read.
func serveMetrics(task Task, w http.ResponseWriter) {
	sample, status, readErr := readTaskMetrics(task)
	if readErr != nil {
		writeTypedError(w, status, readErr)
		return
	}

	body, err := encodeDocument(sample)
	if err != nil {
		writeError(w, http.StatusInternalServerError, output.CodeInternal,
			"task %q metrics could not be encoded", task.ID)
		return
	}

	w.Header().Set("Content-Type", publicMediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G705: the body is a JSON document served as the v1 media type.
	_, _ = w.Write(body)
}

// readTaskMetrics reads the task's last sample. It returns the response status it
// maps to: 200 with a document, or 404 for a task that is not running and for a
// running task that has not reported yet.
func readTaskMetrics(task Task) (v1.TaskMetrics, int, *output.Error) {
	if task.ID == "" || !task.Running {
		return v1.TaskMetrics{}, http.StatusNotFound, output.NewErrorf(output.CodeNotFound,
			"task %q is not running", task.ID).
			WithDetails(map[string]any{"reason": "task_not_running"})
	}

	sample, ok := metrics.LastTaskSample(task.ID)
	if !ok {
		return v1.TaskMetrics{}, http.StatusNotFound, output.NewErrorf(output.CodeNotFound,
			"task %q has no metrics sample yet", task.ID).
			WithDetails(map[string]any{"reason": "no_sample"})
	}

	return v1.NewTaskMetrics(v1.TaskMetrics{
		TaskID:      sample.TaskID,
		Service:     sample.Service,
		CPUSeconds:  sample.CPUSeconds,
		MemoryBytes: sample.MemoryBytes,
		NetRxBytes:  sample.NetRxBytes,
		NetTxBytes:  sample.NetTxBytes,
		ObservedAt:  sample.ObservedAt,
	}), http.StatusOK, nil
}
