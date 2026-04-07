package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// testTempDir creates a temporary directory for tests and returns it.
func testTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

// --- MetaStore Tests ---

func TestMetaStore_WriteAndRead(t *testing.T) {
	dir := testTempDir(t)
	store, err := NewMetaStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	m := &volumeMeta{
		Name:      "test-vol",
		Type:      VolumeTypeDir,
		SizeMB:    512,
		CreatedAt: nowUTC(),
		TaskID:    "task-123",
	}

	if err := store.Write(ctx, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := store.Read(ctx, "test-vol")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if got.Name != "test-vol" {
		t.Errorf("Name = %q, want %q", got.Name, "test-vol")
	}
	if got.Type != VolumeTypeDir {
		t.Errorf("Type = %q, want %q", got.Type, VolumeTypeDir)
	}
	if got.SizeMB != 512 {
		t.Errorf("SizeMB = %d, want 512", got.SizeMB)
	}
	if got.TaskID != "task-123" {
		t.Errorf("TaskID = %q, want %q", got.TaskID, "task-123")
	}

	// Verify file exists on disk
	if _, err := os.Stat(filepath.Join(dir, sanitizeVolumeName("test-vol"), metadataFileName)); err != nil {
		t.Errorf("metadata file not found on disk: %v", err)
	}
}

func TestMetaStore_ReadNotFound(t *testing.T) {
	dir := testTempDir(t)
	store, err := NewMetaStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.Read(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent volume")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

func TestMetaStore_Delete(t *testing.T) {
	dir := testTempDir(t)
	store, err := NewMetaStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	m := &volumeMeta{Name: "to-delete", Type: VolumeTypeDir, CreatedAt: nowUTC()}
	if err := store.Write(ctx, m); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, "to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = store.Read(ctx, "to-delete")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestMetaStore_List(t *testing.T) {
	dir := testTempDir(t)
	store, err := NewMetaStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for _, name := range []string{"vol-a", "vol-b", "vol-c"} {
		m := &volumeMeta{Name: name, Type: VolumeTypeDir, CreatedAt: nowUTC()}
		if err := store.Write(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	metas, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 3 {
		t.Errorf("len(metas) = %d, want 3", len(metas))
	}
}

func TestMetaStore_TouchLastUsed(t *testing.T) {
	dir := testTempDir(t)
	store, err := NewMetaStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	m := &volumeMeta{Name: "touch-me", Type: VolumeTypeDir, CreatedAt: nowUTC()}
	if err := store.Write(ctx, m); err != nil {
		t.Fatal(err)
	}

	got1, _ := store.Read(ctx, "touch-me")
	original := got1.LastUsedAt

	if err := store.TouchLastUsed(ctx, "touch-me"); err != nil {
		t.Fatal(err)
	}

	got2, _ := store.Read(ctx, "touch-me")
	if got2.LastUsedAt.Before(original) || got2.LastUsedAt.Equal(original) {
		t.Error("LastUsedAt should have been updated")
	}
}

func TestMetaStore_UpdateTaskID(t *testing.T) {
	dir := testTempDir(t)
	store, err := NewMetaStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	m := &volumeMeta{Name: "task-update", Type: VolumeTypeDir, CreatedAt: nowUTC(), TaskID: "old-task"}
	if err := store.Write(ctx, m); err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateTaskID(ctx, "task-update", "new-task"); err != nil {
		t.Fatal(err)
	}

	got, _ := store.Read(ctx, "task-update")
	if got.TaskID != "new-task" {
		t.Errorf("TaskID = %q, want %q", got.TaskID, "new-task")
	}
}

func TestMetaStore_RemoveVolumeDir(t *testing.T) {
	dir := testTempDir(t)
	store, err := NewMetaStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	m := &volumeMeta{Name: "nuke-me", Type: VolumeTypeDir, CreatedAt: nowUTC()}
	if err := store.Write(ctx, m); err != nil {
		t.Fatal(err)
	}

	if err := store.RemoveVolumeDir("nuke-me"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, sanitizeVolumeName("nuke-me"))); !os.IsNotExist(err) {
		t.Error("volume directory should have been removed")
	}
}
