package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectTaskHealth(t *testing.T) {
	dir := t.TempDir()

	updated := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	// Written out of ID order to prove the output is sorted.
	for _, md := range []*VMMetadata{
		{ID: "task-c", Health: &VMHealth{State: HealthFailing, FailingStreak: 2, Output: "boom", Command: "true", UpdatedAt: updated}},
		{ID: "task-a", Health: &VMHealth{State: HealthHealthy, UpdatedAt: updated}},
		{ID: "task-b"}, // no healthcheck: never published
	} {
		if err := WriteVMMetadata(dir, md); err != nil {
			t.Fatalf("WriteVMMetadata(%s) failed: %v", md.ID, err)
		}
	}
	// A malformed sidecar must be skipped, not fail the collection.
	if err := os.WriteFile(filepath.Join(dir, "task-x"+VMMetadataSuffix), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("failed to seed malformed metadata: %v", err)
	}

	got := CollectTaskHealth(dir)
	requireTaskOrder(t, got, "task-a", "task-c")

	if got[0].State != HealthHealthy {
		t.Errorf("task-a state = %q, want %q", got[0].State, HealthHealthy)
	}
	if got[1].State != HealthFailing || got[1].FailingStreak != 2 || got[1].Output != "boom" {
		t.Errorf("task-c health = %+v, want failing/streak 2/output boom", got[1].VMHealth)
	}
	if !got[0].UpdatedAt.Equal(updated) {
		t.Errorf("task-a UpdatedAt = %v, want %v", got[0].UpdatedAt, updated)
	}
}

func TestCollectTaskHealth_Empty(t *testing.T) {
	// Both an empty directory and an unset one yield an empty, non-nil list so
	// the JSON body is `{"tasks":[]}` rather than `null`.
	for _, dir := range []string{t.TempDir(), ""} {
		got := CollectTaskHealth(dir)
		if got == nil {
			t.Fatalf("CollectTaskHealth(%q) = nil, want empty slice", dir)
		}
		if len(got) != 0 {
			t.Errorf("CollectTaskHealth(%q) = %v, want empty", dir, got)
		}
	}
}

// TestTaskHealthResponse_JSONShape pins the wire shape the CLI decodes: the
// health fields are flattened next to task_id.
func TestTaskHealthResponse_JSONShape(t *testing.T) {
	resp := TaskHealthResponse{Tasks: []TaskHealth{{
		TaskID:   "task-a",
		VMHealth: VMHealth{State: HealthUnhealthy, FailingStreak: 3, UpdatedAt: time.Unix(0, 0).UTC()},
	}}}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	tasks, ok := got["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("tasks = %v, want one entry", got["tasks"])
	}
	row, ok := tasks[0].(map[string]any)
	if !ok {
		t.Fatalf("task row = %v, want object", tasks[0])
	}
	if row["task_id"] != "task-a" {
		t.Errorf("task_id = %v, want task-a", row["task_id"])
	}
	if row["state"] != HealthUnhealthy {
		t.Errorf("state = %v, want %q", row["state"], HealthUnhealthy)
	}
	if row["failing_streak"] != float64(3) {
		t.Errorf("failing_streak = %v, want 3", row["failing_streak"])
	}
}

func requireTaskOrder(t *testing.T, got []TaskHealth, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("CollectTaskHealth = %d rows, want %d (%v)", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].TaskID != id {
			t.Errorf("row %d task_id = %q, want %q", i, got[i].TaskID, id)
		}
	}
}
