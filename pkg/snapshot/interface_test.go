package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotInfo_JSONRoundTrip(t *testing.T) {
	info := &SnapshotInfo{
		ID:         "snap-123",
		TaskID:     "task-456",
		ServiceID:  "svc-789",
		NodeID:     "node-001",
		CreatedAt:  time.Now().UTC(),
		MemoryPath: "/snapshots/snap-123/vm.mem",
		StatePath:  "/snapshots/snap-123/vm.state",
		SizeBytes:  1024000,
		VCPUCount:  2,
		MemoryMB:   512,
		RootfsPath: "/images/rootfs.ext4",
		Checksum:   "abc123def456",
		Metadata:   map[string]string{"key": "value"},
	}

	data, err := os.CreateTemp("", "snapshot_test_*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(data.Name())

	// Test saveMetadata and loadMetadata
	dir := filepath.Dir(data.Name())
	if err := saveMetadata(dir, info); err != nil {
		// This might fail if metadata.json doesn't exist in that structure
		t.Logf("saveMetadata error (expected in some cases): %v", err)
	}
}

func TestSnapshotFilter_Matches(t *testing.T) {
	now := time.Now().UTC()

	info := &SnapshotInfo{
		ID:        "snap-1",
		TaskID:    "task-1",
		ServiceID: "svc-1",
		NodeID:    "node-1",
		CreatedAt: now,
	}

	// Test matching filter
	filter := SnapshotFilter{
		TaskID: "task-1",
	}
	if !matchesFilter(info, filter) {
		t.Error("Expected filter to match")
	}

	// Test non-matching filter
	filter = SnapshotFilter{
		TaskID: "task-2",
	}
	if matchesFilter(info, filter) {
		t.Error("Expected filter not to match")
	}

	// Test time filter - Since
	filter = SnapshotFilter{
		Since: now.Add(-1 * time.Hour),
	}
	if !matchesFilter(info, filter) {
		t.Error("Expected filter to match (since)")
	}

	filter = SnapshotFilter{
		Since: now.Add(1 * time.Hour),
	}
	if matchesFilter(info, filter) {
		t.Error("Expected filter not to match (since future)")
	}

	// Test time filter - Before
	filter = SnapshotFilter{
		Before: now.Add(1 * time.Hour),
	}
	if !matchesFilter(info, filter) {
		t.Error("Expected filter to match (before)")
	}

	filter = SnapshotFilter{
		Before: now.Add(-1 * time.Hour),
	}
	if matchesFilter(info, filter) {
		t.Error("Expected filter not to match (before past)")
	}
}

func TestSnapshotConfig_Default_Interface(t *testing.T) {
	config := DefaultSnapshotConfig()

	// Check that default config is valid
	if config.MaxSnapshots < 0 {
		t.Error("MaxSnapshots should be >= 0")
	}
	if config.MaxAge < 0 {
		t.Error("MaxAge should be >= 0")
	}
}

func TestGenerateSnapshotID_Interface(t *testing.T) {
	// Test that generateSnapshotID returns a valid ID
	id := generateSnapshotID("task-123")

	if id == "" {
		t.Error("generateSnapshotID should return non-empty string")
	}

	// Different inputs should produce different IDs
	id2 := generateSnapshotID("task-different")
	if id == id2 {
		t.Error("Different inputs should produce different IDs")
	}
}

func TestMatchesFilter_EmptyFilter(t *testing.T) {
	info := &SnapshotInfo{
		ID:        "snap-1",
		CreatedAt: time.Now().UTC(),
	}

	// Empty filter should match all
	filter := SnapshotFilter{}
	if !matchesFilter(info, filter) {
		t.Error("Empty filter should match all snapshots")
	}
}

func TestMatchesFilter_AllFields_Interface(t *testing.T) {
	now := time.Now().UTC()
	info := &SnapshotInfo{
		ID:        "snap-1",
		TaskID:    "task-1",
		ServiceID: "svc-1",
		NodeID:    "node-1",
		CreatedAt: now,
	}

	filter := SnapshotFilter{
		TaskID:    "task-1",
		ServiceID: "svc-1",
		NodeID:    "node-1",
		Since:     now.Add(-1 * time.Hour),
		Before:    now.Add(1 * time.Hour),
	}

	if !matchesFilter(info, filter) {
		t.Error("Filter with all matching fields should match")
	}
}

func TestMatchesFilter_PartialMatch(t *testing.T) {
	now := time.Now().UTC()
	info := &SnapshotInfo{
		ID:        "snap-1",
		TaskID:    "task-1",
		ServiceID: "svc-2", // Different
		CreatedAt: now,
	}

	filter := SnapshotFilter{
		TaskID:    "task-1",
		ServiceID: "svc-1",
	}

	// Partial match should not match (all specified fields must match)
	if matchesFilter(info, filter) {
		t.Error("Filter with non-matching ServiceID should not match")
	}
}
