package runtime

import (
	"os"
	"path/filepath"
	"testing"
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
