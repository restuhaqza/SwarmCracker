package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// runningTask is a task whose Firecracker socket lives in dir, so the health
// handler derives the metadata directory from it exactly as it does on a node.
func runningTask(dir string) Task {
	return Task{
		ID:         "task-1",
		SocketPath: filepath.Join(dir, "task-1.sock"),
		Running:    true,
	}
}

// publishHealth writes the sidecar the executor writes: <dir>/<taskID>.net.json.
func publishHealth(t *testing.T, dir, taskID string, health *scruntime.VMHealth) {
	t.Helper()

	require.NoError(t, scruntime.WriteVMMetadata(dir, &scruntime.VMMetadata{
		ID:     taskID,
		Health: health,
	}))
}

// serveHealthRequest runs the health handler once and returns the response.
func serveHealthRequest(task Task) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/"+task.ID+"/health", nil)
	NewHealthHandler()(context.Background(), task, rec, req)
	return rec
}

func TestHealthHandlerMapsVMHealth(t *testing.T) {
	checkedAt := time.Date(2026, 10, 24, 12, 0, 0, 123456789, time.UTC)

	cases := []struct {
		name   string
		health *scruntime.VMHealth
		want   v1.TaskHealth
	}{
		{
			name:   "healthy",
			health: &scruntime.VMHealth{State: scruntime.HealthHealthy, UpdatedAt: checkedAt},
			want:   v1.TaskHealth{Status: "healthy", CheckedAt: "2026-10-24T12:00:00.123456789Z"},
		},
		{
			name: "failing with a streak and output",
			health: &scruntime.VMHealth{
				State:         scruntime.HealthFailing,
				FailingStreak: 2,
				Output:        "connection refused",
				Command:       "curl -fsS http://127.0.0.1/",
				UpdatedAt:     checkedAt,
			},
			want: v1.TaskHealth{
				Status:        "failing",
				FailingStreak: 2,
				Output:        "connection refused",
				Command:       "curl -fsS http://127.0.0.1/",
				CheckedAt:     "2026-10-24T12:00:00.123456789Z",
			},
		},
		{
			name:   "unhealthy",
			health: &scruntime.VMHealth{State: scruntime.HealthUnhealthy, FailingStreak: 3, UpdatedAt: checkedAt},
			want:   v1.TaskHealth{Status: "unhealthy", FailingStreak: 3, CheckedAt: "2026-10-24T12:00:00.123456789Z"},
		},
		{
			name:   "starting",
			health: &scruntime.VMHealth{State: scruntime.HealthStarting, UpdatedAt: checkedAt},
			want:   v1.TaskHealth{Status: "starting", CheckedAt: "2026-10-24T12:00:00.123456789Z"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			publishHealth(t, dir, "task-1", tc.health)

			rec := serveHealthRequest(runningTask(dir))

			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, publicMediaType, rec.Header().Get("Content-Type"))
			require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

			var got v1.TaskHealth
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.Equal(t, tc.want, got)
		})
	}
}

func TestHealthHandlerNoMetadataIs204(t *testing.T) {
	dir := t.TempDir()

	rec := serveHealthRequest(runningTask(dir))

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Empty(t, rec.Body.String(), "a 204 carries no body")
}

func TestHealthHandlerNoHealthcheckIs204(t *testing.T) {
	dir := t.TempDir()
	publishHealth(t, dir, "task-1", nil)

	rec := serveHealthRequest(runningTask(dir))

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Empty(t, rec.Body.String())
}

// TestHealthHandlerHealthWithoutStateIs204 proves a sidecar that is not a real
// observation (no state yet) is "nothing published" rather than an empty
// TaskHealth the schema cannot accept.
func TestHealthHandlerHealthWithoutStateIs204(t *testing.T) {
	dir := t.TempDir()
	publishHealth(t, dir, "task-1", &scruntime.VMHealth{
		UpdatedAt: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	})

	rec := serveHealthRequest(runningTask(dir))

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Empty(t, rec.Body.String())
}

// TestHealthHandlerMalformedMetadataIs204 proves a half-written or ancient
// sidecar degrades to "no health published" instead of failing the read.
func TestHealthHandlerMalformedMetadataIs204(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task-1"+scruntime.VMMetadataSuffix)
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

	rec := serveHealthRequest(runningTask(dir))

	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestHealthHandlerWithoutSocketIs404(t *testing.T) {
	cases := []struct {
		name string
		task Task
	}{
		{name: "no socket path", task: Task{ID: "task-1"}},
		{name: "log-only task", task: Task{ID: "task-1", LogPath: "/var/log/task-1.log"}},
		{name: "empty ID", task: Task{SocketPath: "/var/run/firecracker/.sock"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveHealthRequest(tc.task)

			require.Equal(t, http.StatusNotFound, rec.Code)
			doc := decodeErrorDocument(t, rec.Body)
			require.Equal(t, string(codeNotFound), doc.Error.Code)
		})
	}
}

// TestHealthHandlerReadErrorIs503 proves an unreadable sidecar is a typed
// unavailable failure that names no path. A directory at the metadata path is a
// deterministic read failure (EISDIR) on every platform.
func TestHealthHandlerReadErrorIs503(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task-1"+scruntime.VMMetadataSuffix)
	require.NoError(t, os.Mkdir(path, 0o700))

	rec := serveHealthRequest(runningTask(dir))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	doc := decodeErrorDocument(t, rec.Body)
	require.Equal(t, string(output.CodeUnavailable), doc.Error.Code)
	require.True(t, doc.Error.Retryable)
	require.NotContains(t, rec.Body.String(), dir, "the node must not leak its metadata path")
	require.NotContains(t, rec.Body.String(), scruntime.VMMetadataSuffix,
		"the node must not leak its metadata file name")
}
