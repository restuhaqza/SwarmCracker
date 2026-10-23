package broker

// This file owns the node-side health handler: GET /v1/tasks/{taskID}/health on
// the internal broker.
//
// Health has no new source of truth. The executor already publishes every task's
// live healthcheck state in the sidecar it writes next to the VM socket
// (<socketDir>/<taskID>.net.json), so the node handler reads that file. The
// manager therefore never dials a node's loopback health endpoint and needs no
// second credential or port.
//
// The two "no health" answers are deliberately distinct:
//   - 204: the task declares no healthcheck, or the daemon has not probed yet.
//     This is not an error, and a list fan-out must not turn it into a partial.
//   - 404: the task has no VM on this node (log-only, or already exited), so
//     there is no sidecar to read and no health to report.
//
// Nothing here is audited or cached: a health read is one local file read.

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// healthOperation is the broker operation name for a health read.
const healthOperation = "health"

// NewHealthHandler builds the health operation. It reads the per-task sidecar the
// executor writes, so it needs no dialer and no node-local configuration beyond
// the socket path the task source already resolved.
func NewHealthHandler() Handler {
	return func(_ context.Context, task Task, w http.ResponseWriter, _ *http.Request) {
		serveHealth(task, w)
	}
}

// serveHealth answers one health read.
func serveHealth(task Task, w http.ResponseWriter) {
	health, status, readErr := readTaskHealth(task)
	if readErr != nil {
		writeTypedError(w, status, readErr)
		return
	}
	// 204 deliberately carries no body: there is no health object to publish,
	// and an empty {} would claim one exists.
	if status == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	body, err := encodeDocument(health)
	if err != nil {
		writeError(w, http.StatusInternalServerError, output.CodeInternal,
			"task %q health could not be encoded", task.ID)
		return
	}

	w.Header().Set("Content-Type", publicMediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G705: the body is a JSON document served as the v1 media type.
	_, _ = w.Write(body)
}

// readTaskHealth reads the task's published health. It returns the response
// status it maps to: 200 with a document, 204 for "nothing published", or 404
// for a task with no VM.
func readTaskHealth(task Task) (*v1.TaskHealth, int, *output.Error) {
	if task.ID == "" || task.SocketPath == "" {
		// Log-only or exited: the sidecar lives next to the VM socket, so there
		// is no directory to read and no health to serve.
		return nil, http.StatusNotFound, output.NewErrorf(output.CodeNotFound,
			"task %q has no health", task.ID)
	}

	dir := filepath.Dir(task.SocketPath)
	metadata, ok := scruntime.ReadVMMetadata(dir, task.ID)
	if !ok {
		// ReadVMMetadata folds "missing" and "unreadable" into one answer. A node
		// must tell them apart: a malformed or absent sidecar is 204, while an
		// unreadable one is a retryable failure.
		if err := sidecarReadError(dir, task.ID); err != nil {
			return nil, http.StatusServiceUnavailable, output.NewErrorf(output.CodeUnavailable,
				"task %q health is unavailable", task.ID)
		}
		return nil, http.StatusNoContent, nil
	}
	// A sidecar without a state carries no observation: publishing an empty
	// status would break the schema and look like a node failure to the manager.
	if metadata.Health == nil || metadata.Health.State == "" {
		return nil, http.StatusNoContent, nil
	}

	return taskHealthFromVM(metadata.Health), http.StatusOK, nil
}

// sidecarReadError reports whether the task's sidecar exists but cannot be read.
// An absent file is not an error; a readable-but-malformed one is not either
// (the runtime reader degrades it to "no health yet" on purpose).
func sidecarReadError(dir, taskID string) error {
	//nolint:gosec // G304: dir comes from the task source's socket path and the
	// task ID was validated by SocketDirSource before the handler ran.
	_, err := os.ReadFile(filepath.Join(dir, taskID+scruntime.VMMetadataSuffix))
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// taskHealthFromVM maps the daemon's observation to the public DTO. The states
// are the runtime strings (starting|healthy|failing|unhealthy), which the public
// schema enumerates. An unset observation time is left off the wire rather than
// published as the zero instant.
func taskHealthFromVM(health *scruntime.VMHealth) *v1.TaskHealth {
	out := &v1.TaskHealth{
		Status:        health.State,
		FailingStreak: health.FailingStreak,
		Output:        health.Output,
		Command:       health.Command,
	}
	if !health.UpdatedAt.IsZero() {
		out.CheckedAt = health.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}
