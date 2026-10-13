package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteReadVMMetadata_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	md := &VMMetadata{
		ID:          "task-123",
		NetworkID:   "network-1",
		IPAddresses: []string{"192.168.127.42/24"},
	}

	if err := WriteVMMetadata(dir, md); err != nil {
		t.Fatalf("WriteVMMetadata failed: %v", err)
	}

	got, ok := ReadVMMetadata(dir, md.ID)
	if !ok {
		t.Fatalf("ReadVMMetadata reported no metadata for %q", md.ID)
	}
	if got.ID != md.ID {
		t.Errorf("ID = %q, want %q", got.ID, md.ID)
	}
	if got.NetworkID != md.NetworkID {
		t.Errorf("NetworkID = %q, want %q", got.NetworkID, md.NetworkID)
	}
	if len(got.IPAddresses) != 1 || got.IPAddresses[0] != "192.168.127.42/24" {
		t.Errorf("IPAddresses = %v, want [192.168.127.42/24]", got.IPAddresses)
	}
}

func TestReadVMMetadata_MissingAndInvalid(t *testing.T) {
	dir := t.TempDir()

	if _, ok := ReadVMMetadata(dir, "nope"); ok {
		t.Error("ReadVMMetadata reported metadata for a missing file")
	}
	if _, ok := ReadVMMetadata("", "nope"); ok {
		t.Error("ReadVMMetadata reported metadata for an empty dir")
	}
	if _, ok := ReadVMMetadata(dir, ""); ok {
		t.Error("ReadVMMetadata reported metadata for an empty task ID")
	}

	// A malformed file must be ignored, not surfaced as an error.
	if err := os.WriteFile(filepath.Join(dir, "broken"+VMMetadataSuffix), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("failed to seed malformed metadata: %v", err)
	}
	if _, ok := ReadVMMetadata(dir, "broken"); ok {
		t.Error("ReadVMMetadata accepted malformed metadata")
	}
}

func TestWriteVMMetadata_Validation(t *testing.T) {
	dir := t.TempDir()

	if err := WriteVMMetadata("", &VMMetadata{ID: "x"}); err == nil {
		t.Error("WriteVMMetadata accepted an empty directory")
	}
	if err := WriteVMMetadata(dir, nil); err == nil {
		t.Error("WriteVMMetadata accepted nil metadata")
	}
	if err := WriteVMMetadata(dir, &VMMetadata{}); err == nil {
		t.Error("WriteVMMetadata accepted metadata without an ID")
	}
}

func TestWriteVMMetadata_AtomicOverwrite(t *testing.T) {
	dir := t.TempDir()

	if err := WriteVMMetadata(dir, &VMMetadata{ID: "vm", IPAddresses: []string{"10.0.0.2/24"}}); err != nil {
		t.Fatalf("first write failed: %v", err)
	}
	if err := WriteVMMetadata(dir, &VMMetadata{ID: "vm", IPAddresses: []string{"10.0.0.3/24"}}); err != nil {
		t.Fatalf("second write failed: %v", err)
	}

	got, ok := ReadVMMetadata(dir, "vm")
	if !ok {
		t.Fatal("metadata missing after overwrite")
	}
	if got.IPAddresses[0] != "10.0.0.3/24" {
		t.Errorf("IPAddresses = %v, want [10.0.0.3/24]", got.IPAddresses)
	}

	// No temp file should be left behind.
	if _, err := os.Stat(filepath.Join(dir, "vm"+VMMetadataSuffix+".tmp")); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}

func TestRemoveVMMetadata(t *testing.T) {
	dir := t.TempDir()

	if err := WriteVMMetadata(dir, &VMMetadata{ID: "vm"}); err != nil {
		t.Fatalf("WriteVMMetadata failed: %v", err)
	}
	if err := RemoveVMMetadata(dir, "vm"); err != nil {
		t.Fatalf("RemoveVMMetadata failed: %v", err)
	}
	if _, ok := ReadVMMetadata(dir, "vm"); ok {
		t.Error("metadata still readable after removal")
	}

	// Removing again, or with empty arguments, must be a no-op.
	if err := RemoveVMMetadata(dir, "vm"); err != nil {
		t.Errorf("RemoveVMMetadata on a missing file returned %v", err)
	}
	if err := RemoveVMMetadata("", "vm"); err != nil {
		t.Errorf("RemoveVMMetadata with an empty dir returned %v", err)
	}
}

// TestWriteReadVMMetadata_HealthRoundTrip covers the live healthcheck
// observation the daemon publishes for node-local `vm status`/`vm list`.
func TestWriteReadVMMetadata_HealthRoundTrip(t *testing.T) {
	dir := t.TempDir()
	updated := time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)
	md := &VMMetadata{
		ID:          "task-health",
		NetworkID:   "network-1",
		IPAddresses: []string{"192.168.127.42/24"},
		Health: &VMHealth{
			State:         HealthUnhealthy,
			FailingStreak: 2,
			Output:        "boom",
			Command:       "/bin/sh -c test ! -f /tmp/break",
			UpdatedAt:     updated,
		},
	}

	if err := WriteVMMetadata(dir, md); err != nil {
		t.Fatalf("WriteVMMetadata failed: %v", err)
	}

	got, ok := ReadVMMetadata(dir, md.ID)
	if !ok {
		t.Fatalf("ReadVMMetadata reported no metadata for %q", md.ID)
	}
	if got.Health == nil {
		t.Fatal("Health = nil, want the written observation")
	}
	if got.Health.State != HealthUnhealthy {
		t.Errorf("Health.State = %q, want %q", got.Health.State, HealthUnhealthy)
	}
	if got.Health.FailingStreak != 2 {
		t.Errorf("Health.FailingStreak = %d, want 2", got.Health.FailingStreak)
	}
	if got.Health.Output != "boom" {
		t.Errorf("Health.Output = %q, want %q", got.Health.Output, "boom")
	}
	if got.Health.Command != "/bin/sh -c test ! -f /tmp/break" {
		t.Errorf("Health.Command = %q", got.Health.Command)
	}
	if !got.Health.UpdatedAt.Equal(updated) {
		t.Errorf("Health.UpdatedAt = %s, want %s", got.Health.UpdatedAt, updated)
	}
	// The network fields must survive the health write.
	if got.NetworkID != "network-1" || len(got.IPAddresses) != 1 {
		t.Errorf("network fields lost: NetworkID=%q IPs=%v", got.NetworkID, got.IPAddresses)
	}
}

// TestReadVMMetadata_HealthAbsentIsBackwardCompatible pins the migration path:
// a sidecar written before health existed (or by a task with no healthcheck)
// must still decode, with Health simply absent.
func TestReadVMMetadata_HealthAbsentIsBackwardCompatible(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"id":"task-old","network_id":"net-1","ip_addresses":["10.0.0.2/24"]}`
	if err := os.WriteFile(filepath.Join(dir, "task-old"+VMMetadataSuffix), []byte(legacy), 0o600); err != nil {
		t.Fatalf("failed to seed legacy metadata: %v", err)
	}

	got, ok := ReadVMMetadata(dir, "task-old")
	if !ok {
		t.Fatal("ReadVMMetadata rejected a sidecar without a health object")
	}
	if got.Health != nil {
		t.Errorf("Health = %+v, want nil for a sidecar without health", got.Health)
	}
	if got.NetworkID != "net-1" || len(got.IPAddresses) != 1 {
		t.Errorf("legacy fields not decoded: NetworkID=%q IPs=%v", got.NetworkID, got.IPAddresses)
	}
}

// TestVMMetadata_HealthOmittedWhenNil asserts the field is additive on the wire:
// a metadata without health must not emit a "health" key at all, so an old
// reader sees exactly the file it always saw.
func TestVMMetadata_HealthOmittedWhenNil(t *testing.T) {
	data, err := json.Marshal(&VMMetadata{ID: "vm-1"})
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if strings.Contains(string(data), "health") {
		t.Errorf("metadata without health emitted a health key: %s", data)
	}
}
