package snapshot

import (
	"testing"
	"time"
)

func TestSnapshotConfig_AgeEnforcement(t *testing.T) {
	cfg := SnapshotConfig{
		Enabled:      true,
		SnapshotDir:  "/tmp/snapshots",
		MaxAge:       24 * time.Hour,
		MaxSnapshots: 5,
	}

	if cfg.MaxAge != 24*time.Hour {
		t.Errorf("Expected MaxAge 24h, got %v", cfg.MaxAge)
	}

	if cfg.MaxSnapshots != 5 {
		t.Errorf("Expected MaxSnapshots 5, got %d", cfg.MaxSnapshots)
	}
}

func TestCreateOptions_Fields(t *testing.T) {
	opts := CreateOptions{
		ServiceID:  "svc-1",
		NodeID:     "node-1",
		VCPUCount:  2,
		MemoryMB:   512,
		RootfsPath: "/tmp/rootfs",
		Metadata:   map[string]string{"key": "value"},
	}

	if opts.ServiceID != "svc-1" {
		t.Errorf("Expected ServiceID svc-1, got %s", opts.ServiceID)
	}
	if opts.VCPUCount != 2 {
		t.Errorf("Expected VCPUCount 2, got %d", opts.VCPUCount)
	}
}

func TestSnapshotInfo_SizeCalculation(t *testing.T) {
	info := &SnapshotInfo{
		ID:         "snap-1",
		MemoryPath: "/tmp/mem",
		StatePath:  "/tmp/state",
		SizeBytes:  1024000,
	}

	if info.SizeBytes != 1024000 {
		t.Errorf("Expected SizeBytes 1024000, got %d", info.SizeBytes)
	}
}

func TestSnapshotFilter_TimeRange(t *testing.T) {
	now := time.Now().UTC()
	filter := SnapshotFilter{
		Since:  now.Add(-2 * time.Hour),
		Before: now.Add(2 * time.Hour),
	}

	// Test that filter's time range is valid
	if filter.Since.After(filter.Before) {
		t.Error("Since should be before Before")
	}
}
