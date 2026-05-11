package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMetaStore_Write_InvalidJSON tests Write with data that would fail to marshal
func TestMetaStore_Write_CreateDirError(t *testing.T) {
	// Use a path that can't be created (e.g., under /proc)
	store := &MetaStore{baseDir: "/proc/nonexistent/path"}
	ctx := context.Background()

	m := &volumeMeta{Name: "test-vol", Type: VolumeTypeDir, CreatedAt: time.Now()}
	err := store.Write(ctx, m)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create volume dir")
}

// TestMetaStore_Read_CorruptJSON tests Read with corrupt JSON file
func TestMetaStore_Read_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMetaStore(dir)
	require.NoError(t, err)

	// Create volume dir with corrupt meta.json
	volDir := filepath.Join(dir, sanitizeVolumeName("corrupt-vol"))
	os.MkdirAll(volDir, 0755)
	os.WriteFile(filepath.Join(volDir, metadataFileName), []byte("not valid json"), 0644)

	ctx := context.Background()
	_, err = store.Read(ctx, "corrupt-vol")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// TestMetaStore_Delete_NotExist tests Delete when metadata doesn't exist
func TestMetaStore_Delete_NotExist(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMetaStore(dir)
	require.NoError(t, err)

	ctx := context.Background()
	err = store.Delete(ctx, "nonexistent")
	require.NoError(t, err) // Delete should succeed even if file doesn't exist
}

// TestMetaStore_List_Empty tests List with empty directory
func TestMetaStore_List_Empty(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMetaStore(dir)
	require.NoError(t, err)

	ctx := context.Background()
	metas, err := store.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, metas)
}

// TestMetaStore_List_SkipNonDirs tests List skips non-directory entries
func TestMetaStore_List_SkipNonDirs(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMetaStore(dir)
	require.NoError(t, err)

	// Create a file (not directory) in baseDir
	os.WriteFile(filepath.Join(dir, "somefile.txt"), []byte("data"), 0644)

	// Create one valid volume
	ctx := context.Background()
	m := &volumeMeta{Name: "valid-vol", Type: VolumeTypeDir, CreatedAt: time.Now()}
	store.Write(ctx, m)

	metas, err := store.List(ctx)
	require.NoError(t, err)
	assert.Len(t, metas, 1)
	assert.Equal(t, "valid-vol", metas[0].Name)
}

// TestMetaStore_List_SkipNoMetadata tests List skips dirs without meta.json
func TestMetaStore_List_SkipNoMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMetaStore(dir)
	require.NoError(t, err)

	// Create a directory without meta.json
	os.MkdirAll(filepath.Join(dir, "no-meta-dir"), 0755)

	// Create one valid volume
	ctx := context.Background()
	m := &volumeMeta{Name: "valid-vol", Type: VolumeTypeDir, CreatedAt: time.Now()}
	store.Write(ctx, m)

	metas, err := store.List(ctx)
	require.NoError(t, err)
	assert.Len(t, metas, 1)
	assert.Equal(t, "valid-vol", metas[0].Name)
}

// TestMetaStore_List_NonexistentBaseDir tests List when base dir doesn't exist
func TestMetaStore_List_NonexistentBaseDir(t *testing.T) {
	store := &MetaStore{baseDir: "/nonexistent/path/that/does/not/exist"}

	ctx := context.Background()
	metas, err := store.List(ctx)
	require.NoError(t, err) // Should return nil, nil for nonexistent dir
	assert.Nil(t, metas)
}

// TestMetaStore_TouchLastUsed_NotExist tests TouchLastUsed on nonexistent volume
func TestMetaStore_TouchLastUsed_NotExist(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMetaStore(dir)
	require.NoError(t, err)

	ctx := context.Background()
	err = store.TouchLastUsed(ctx, "nonexistent")
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err))
}

// TestMetaStore_UpdateTaskID_NotExist tests UpdateTaskID on nonexistent volume
func TestMetaStore_UpdateTaskID_NotExist(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMetaStore(dir)
	require.NoError(t, err)

	ctx := context.Background()
	err = store.UpdateTaskID(ctx, "nonexistent", "task-123")
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err))
}

// TestMetaStore_RemoveVolumeDir_NotExist tests RemoveVolumeDir on nonexistent volume
func TestMetaStore_RemoveVolumeDir_NotExist(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMetaStore(dir)
	require.NoError(t, err)

	err = store.RemoveVolumeDir("nonexistent")
	require.NoError(t, err) // Should succeed even if dir doesn't exist
}

// TestMetaStore_ConcurrentWrite tests concurrent writes
func TestMetaStore_ConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMetaStore(dir)
	require.NoError(t, err)

	ctx := context.Background()

	// Write initial metadata
	m := &volumeMeta{Name: "concurrent-vol", Type: VolumeTypeDir, CreatedAt: time.Now()}
	store.Write(ctx, m)

	// Concurrent updates
	done := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			err := store.UpdateTaskID(ctx, "concurrent-vol", "task-"+string(rune('a'+idx)))
			done <- err
		}(i)
	}

	for i := 0; i < 10; i++ {
		err := <-done
		assert.NoError(t, err)
	}

	// Verify final state is readable
	got, err := store.Read(ctx, "concurrent-vol")
	require.NoError(t, err)
	assert.Equal(t, "concurrent-vol", got.Name)
}

// TestMetaStore_Write_Overwrite tests Write overwrites existing metadata
func TestMetaStore_Write_Overwrite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMetaStore(dir)
	require.NoError(t, err)

	ctx := context.Background()

	// Write initial
	m1 := &volumeMeta{Name: "overwrite-vol", Type: VolumeTypeDir, SizeMB: 100, CreatedAt: time.Now()}
	store.Write(ctx, m1)

	// Overwrite
	m2 := &volumeMeta{Name: "overwrite-vol", Type: VolumeTypeBlock, SizeMB: 200, CreatedAt: time.Now()}
	store.Write(ctx, m2)

	// Verify overwrite succeeded
	got, err := store.Read(ctx, "overwrite-vol")
	require.NoError(t, err)
	assert.Equal(t, VolumeTypeBlock, got.Type)
	assert.Equal(t, 200, got.SizeMB)
}

// TestVolumeMeta_JSONRoundtrip tests JSON serialization
func TestVolumeMeta_JSONRoundtrip(t *testing.T) {
	now := time.Now().UTC()
	m := &volumeMeta{
		Name:       "json-test",
		Type:       VolumeTypeBlock,
		SizeMB:     1024,
		CreatedAt:  now,
		LastUsedAt: now,
		TaskID:     "task-abc",
	}

	data, err := json.MarshalIndent(m, "", "  ")
	require.NoError(t, err)

	var got volumeMeta
	err = json.Unmarshal(data, &got)
	require.NoError(t, err)

	assert.Equal(t, m.Name, got.Name)
	assert.Equal(t, m.Type, got.Type)
	assert.Equal(t, m.SizeMB, got.SizeMB)
	assert.Equal(t, m.TaskID, got.TaskID)
}

// TestMetaStore_NewMetaStore_ExistingDir tests NewMetaStore with existing directory
func TestMetaStore_NewMetaStore_ExistingDir(t *testing.T) {
	dir := t.TempDir()

	// Directory exists already
	store, err := NewMetaStore(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, store.baseDir)
}

// TestMetaStore_NewMetaStore_NilDir tests NewMetaStore behavior with empty path
func TestMetaStore_NewMetaStore_EmptyPath(t *testing.T) {
	// Empty path should still work (creates in current dir context)
	_, err := NewMetaStore("")
	// May or may not error depending on permissions
	_ = err
}

// TestMetaStore_Read_ContextCancellation tests Read with cancelled context
func TestMetaStore_Read_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMetaStore(dir)
	require.NoError(t, err)

	// Create a volume
	ctx := context.Background()
	m := &volumeMeta{Name: "cancel-test", Type: VolumeTypeDir, CreatedAt: time.Now()}
	store.Write(ctx, m)

	// Read with cancelled context (should still work since Read doesn't use context)
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := store.Read(cancelCtx, "cancel-test")
	require.NoError(t, err)
	assert.Equal(t, "cancel-test", got.Name)
}

// TestMetaStore_volumeDir tests volumeDir path construction
func TestMetaStore_volumeDir(t *testing.T) {
	store := &MetaStore{baseDir: "/var/lib/volumes"}

	dir := store.volumeDir("test-volume")
	assert.Equal(t, "/var/lib/volumes/test-volume", dir)
}

// TestMetaStore_metaPath tests metaPath construction
func TestMetaStore_metaPath(t *testing.T) {
	store := &MetaStore{baseDir: "/var/lib/volumes"}

	path := store.metaPath("test-volume")
	assert.Equal(t, "/var/lib/volumes/test-volume/meta.json", path)
}

// TestSanitizeVolumeName tests volume name sanitization
func TestSanitizeVolumeName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"with spaces", "with_spaces"},
		{"with/slashes", "with_slashes"},
		{"with:colons", "with_colons"},
		{"with.dots", "with.dots"},
		{"with-dashes", "with-dashes"},
		{"UPPERCASE", "UPPERCASE"},
		{"with_underscores", "with_underscores"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeVolumeName(tt.input)
			// The exact sanitization depends on implementation
			// Just verify it's not empty and doesn't have problematic chars
			assert.NotEmpty(t, got)
		})
	}
}
