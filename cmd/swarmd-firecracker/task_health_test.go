package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

func TestTaskHealthHandler_ServesLiveHealth(t *testing.T) {
	dir := t.TempDir()
	if err := runtime.WriteVMMetadata(dir, &runtime.VMMetadata{
		ID:     "task-1",
		Health: &runtime.VMHealth{State: runtime.HealthFailing, FailingStreak: 1, UpdatedAt: time.Unix(0, 0).UTC()},
	}); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	// A task with no healthcheck is absent from the response.
	if err := runtime.WriteVMMetadata(dir, &runtime.VMMetadata{ID: "task-2"}); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}

	rec := httptest.NewRecorder()
	taskHealthHandler(dir).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, runtime.TaskHealthPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp runtime.TaskHealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Tasks) != 1 || resp.Tasks[0].TaskID != "task-1" {
		t.Fatalf("tasks = %+v, want just task-1", resp.Tasks)
	}
	if resp.Tasks[0].State != runtime.HealthFailing {
		t.Errorf("state = %q, want %q", resp.Tasks[0].State, runtime.HealthFailing)
	}
}

func TestTaskHealthHandler_EmptyDirIsEmptyList(t *testing.T) {
	rec := httptest.NewRecorder()
	taskHealthHandler(t.TempDir()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, runtime.TaskHealthPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "{\"tasks\":[]}\n" {
		t.Errorf("body = %q, want an empty JSON list", got)
	}
}

func TestTaskHealthHandler_RejectsNonGET(t *testing.T) {
	rec := httptest.NewRecorder()
	taskHealthHandler(t.TempDir()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, runtime.TaskHealthPath, nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", allow, "GET, HEAD")
	}
}

func TestTaskHealthHandler_AllowsHEAD(t *testing.T) {
	rec := httptest.NewRecorder()
	taskHealthHandler(t.TempDir()).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, runtime.TaskHealthPath, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
