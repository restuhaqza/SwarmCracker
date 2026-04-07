package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryDriver_CreateAndStat(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	path, err := d.Create(ctx, "test-vol", CreateOptions{SizeMB: 100})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if path == "" {
		t.Fatal("path should not be empty")
	}

	// Verify data subdirectory was created
	dataDir := filepath.Join(path, dirDataSubdir)
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("data dir not created: %v", err)
	}

	// Verify metadata
	info, err := d.Stat(ctx, "test-vol")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Name != "test-vol" {
		t.Errorf("Name = %q", info.Name)
	}
	if info.Type != VolumeTypeDir {
		t.Errorf("Type = %q", info.Type)
	}
	if info.SizeMB != 100 {
		t.Errorf("SizeMB = %d, want 100", info.SizeMB)
	}
}

func TestDirectoryDriver_CreateEmptyName(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = d.Create(context.Background(), "", CreateOptions{})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestDirectoryDriver_Delete(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "del-me", CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := d.Delete(ctx, "del-me"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = d.Stat(ctx, "del-me")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestDirectoryDriver_MountAndUnmount(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "mount-vol", CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	// Write some data into the volume
	dataDir := filepath.Join(dir, sanitizeVolumeName("mount-vol"), dirDataSubdir)
	if err := os.WriteFile(filepath.Join(dataDir, "test.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a fake rootfs
	rootfsDir := t.TempDir()
	targetDir := filepath.Join(rootfsDir, "data")

	// Mount
	if err := d.Mount(ctx, "mount-vol", rootfsDir, "/data"); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// Verify data was copied
	content, err := os.ReadFile(filepath.Join(targetDir, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello" {
		t.Errorf("content = %q, want %q", string(content), "hello")
	}

	// Modify data in rootfs
	if err := os.WriteFile(filepath.Join(targetDir, "new.txt"), []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}

	// Unmount (sync back)
	if err := d.Unmount(ctx, "mount-vol", rootfsDir, "/data", false); err != nil {
		t.Fatalf("Unmount: %v", err)
	}

	// Verify new data was synced back
	newContent, err := os.ReadFile(filepath.Join(dataDir, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(newContent) != "world" {
		t.Errorf("synced content = %q, want %q", string(newContent), "world")
	}
}

func TestDirectoryDriver_MountReadOnly(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "ro-vol", CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	rootfsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootfsDir, "data"), 0755); err != nil {
		t.Fatal(err)
	}

	// Mount
	if err := d.Mount(ctx, "ro-vol", rootfsDir, "/data"); err != nil {
		t.Fatal(err)
	}

	// Unmount read-only — should skip sync
	if err := d.Unmount(ctx, "ro-vol", rootfsDir, "/data", true); err != nil {
		t.Fatalf("Unmount read-only: %v", err)
	}
}

func TestDirectoryDriver_Capacity(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "cap-vol", CreateOptions{SizeMB: 100}); err != nil {
		t.Fatal(err)
	}

	used, limit, err := d.Capacity(ctx, "cap-vol")
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if limit != 100*1024*1024 {
		t.Errorf("limit = %d, want %d", limit, 100*1024*1024)
	}
	if used < 0 {
		t.Errorf("used = %d, should be >= 0", used)
	}
}

func TestDirectoryDriver_SnapshotAndRestore(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "snap-vol", CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	// Write data
	dataDir := filepath.Join(dir, sanitizeVolumeName("snap-vol"), dirDataSubdir)
	if err := os.WriteFile(filepath.Join(dataDir, "original.txt"), []byte("original data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Snapshot
	snap, err := d.Snapshot(ctx, "snap-vol")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Volume != "snap-vol" {
		t.Errorf("snap.Volume = %q", snap.Volume)
	}
	if _, err := os.Stat(snap.Path); err != nil {
		t.Fatalf("snapshot file not found: %v", err)
	}

	// Modify volume data
	if err := os.WriteFile(filepath.Join(dataDir, "modified.txt"), []byte("new data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Restore
	if err := d.Restore(ctx, "snap-vol", snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Original data should be back
	content, err := os.ReadFile(filepath.Join(dataDir, "original.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original data" {
		t.Errorf("after restore: content = %q", string(content))
	}

	// Modified file should be gone
	if _, err := os.Stat(filepath.Join(dataDir, "modified.txt")); !os.IsNotExist(err) {
		t.Error("modified.txt should not exist after restore")
	}
}

func TestDirectoryDriver_ExportImport(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "export-vol", CreateOptions{SizeMB: 50}); err != nil {
		t.Fatal(err)
	}

	// Write data
	dataDir := filepath.Join(dir, sanitizeVolumeName("export-vol"), dirDataSubdir)
	if err := os.WriteFile(filepath.Join(dataDir, "export.txt"), []byte("export me"), 0644); err != nil {
		t.Fatal(err)
	}

	// Export
	var buf bytes.Buffer
	if err := d.Export(ctx, "export-vol", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("export buffer should not be empty")
	}

	// Import into new volume
	if err := d.Import(ctx, "import-vol", &buf, 50); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// Verify imported data
	importDir := filepath.Join(dir, sanitizeVolumeName("import-vol"), dirDataSubdir)
	content, err := os.ReadFile(filepath.Join(importDir, "export.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "export me" {
		t.Errorf("imported content = %q", string(content))
	}
}

func TestDirectoryDriver_MountNonexistentVolume(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	rootfsDir := t.TempDir()
	err = d.Mount(context.Background(), "nope", rootfsDir, "/data")
	if err == nil {
		t.Fatal("expected error for nonexistent volume")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, should mention not found", err)
	}
}
