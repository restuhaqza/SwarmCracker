package snapshot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotConfigDefaults(t *testing.T) {
	cfg := DefaultSnapshotConfig()

	assert.True(t, strings.Contains(cfg.SnapshotDir, "firecracker"))
	assert.Equal(t, 3, cfg.MaxSnapshots)
	assert.Equal(t, 168*time.Hour, cfg.MaxAge)
	assert.False(t, cfg.AutoSnapshot)
	assert.False(t, cfg.Compress)
}

func TestSnapshotConfigSetDefaults(t *testing.T) {
	cfg := SnapshotConfig{}
	cfg.SetDefaults()

	assert.NotEmpty(t, cfg.SnapshotDir)
	assert.Equal(t, 3, cfg.MaxSnapshots)
	assert.Equal(t, 168*time.Hour, cfg.MaxAge)
}

func TestSnapshotConfigSetDefaultsPreservesSet(t *testing.T) {
	cfg := SnapshotConfig{
		SnapshotDir:  "/custom/path",
		MaxSnapshots: 10,
		MaxAge:       24 * time.Hour,
		AutoSnapshot: true,
	}
	cfg.SetDefaults()

	assert.Equal(t, "/custom/path", cfg.SnapshotDir)
	assert.Equal(t, 10, cfg.MaxSnapshots)
	assert.Equal(t, 24*time.Hour, cfg.MaxAge)
	assert.True(t, cfg.AutoSnapshot)
}

func TestNewManager(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := SnapshotConfig{SnapshotDir: tmpDir}

	mgr, err := NewManager(cfg)
	require.NoError(t, err)
	require.NotNil(t, mgr)
	assert.Equal(t, tmpDir, mgr.config.SnapshotDir)
}

func TestNewManagerCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := SnapshotConfig{SnapshotDir: filepath.Join(tmpDir, "nested", "dir")}

	mgr, err := NewManager(cfg)
	require.NoError(t, err)
	require.NotNil(t, mgr)

	info, err := os.Stat(cfg.SnapshotDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestSaveAndLoadMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	now := time.Now().UTC().Truncate(time.Second)
	original := &SnapshotInfo{
		ID:         "snap-abc123def4567890",
		TaskID:     "task-1",
		ServiceID:  "nginx",
		NodeID:     "worker-1",
		CreatedAt:  now,
		MemoryPath: "/var/lib/snapshots/snap-abc123def4567890/vm.mem",
		StatePath:  "/var/lib/snapshots/snap-abc123def4567890/vm.state",
		SizeBytes:  1024 * 1024 * 512,
		VCPUCount:  2,
		MemoryMB:   1024,
		RootfsPath: "/var/lib/firecracker/rootfs/task-1.ext4",
		Checksum:   "abc123",
		Metadata:   map[string]string{"image": "nginx:alpine", "version": "1.25"},
	}

	err := saveMetadata(tmpDir, original)
	require.NoError(t, err)

	// Verify file exists
	assert.FileExists(t, filepath.Join(tmpDir, metadataFile))

	// Load it back
	loaded, err := loadMetadata(tmpDir)
	require.NoError(t, err)

	assert.Equal(t, original.ID, loaded.ID)
	assert.Equal(t, original.TaskID, loaded.TaskID)
	assert.Equal(t, original.ServiceID, loaded.ServiceID)
	assert.Equal(t, original.NodeID, loaded.NodeID)
	assert.Equal(t, original.CreatedAt, loaded.CreatedAt)
	assert.Equal(t, original.MemoryPath, loaded.MemoryPath)
	assert.Equal(t, original.StatePath, loaded.StatePath)
	assert.Equal(t, original.SizeBytes, loaded.SizeBytes)
	assert.Equal(t, original.VCPUCount, loaded.VCPUCount)
	assert.Equal(t, original.MemoryMB, loaded.MemoryMB)
	assert.Equal(t, original.RootfsPath, loaded.RootfsPath)
	assert.Equal(t, original.Checksum, loaded.Checksum)
	assert.Equal(t, original.Metadata, loaded.Metadata)
}

func TestLoadMetadataResolvesRelativePaths(t *testing.T) {
	tmpDir := t.TempDir()

	info := &SnapshotInfo{
		ID:         "snap-test123",
		TaskID:     "task-1",
		CreatedAt:  time.Now().UTC(),
		MemoryPath: "vm.mem",   // relative
		StatePath:  "vm.state", // relative
	}

	err := saveMetadata(tmpDir, info)
	require.NoError(t, err)

	loaded, err := loadMetadata(tmpDir)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(tmpDir, "vm.mem"), loaded.MemoryPath)
	assert.Equal(t, filepath.Join(tmpDir, "vm.state"), loaded.StatePath)
}

func TestLoadMetadataNonexistent(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := loadMetadata(tmpDir)
	assert.Error(t, err)
}

func TestMetadataJSONRoundtrip(t *testing.T) {
	info := &SnapshotInfo{
		ID:        "snap-test",
		TaskID:    "task-1",
		ServiceID: "web",
		CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
		SizeBytes: 2048,
		VCPUCount: 4,
		MemoryMB:  2048,
		Metadata:  map[string]string{"key": "value"},
	}

	data, err := json.MarshalIndent(info, "", "  ")
	require.NoError(t, err)

	var decoded SnapshotInfo
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, info.ID, decoded.ID)
	assert.Equal(t, info.TaskID, decoded.TaskID)
	assert.Equal(t, info.ServiceID, decoded.ServiceID)
	assert.Equal(t, info.CreatedAt, decoded.CreatedAt)
	assert.Equal(t, info.SizeBytes, decoded.SizeBytes)
	assert.Equal(t, info.VCPUCount, decoded.VCPUCount)
	assert.Equal(t, info.MemoryMB, decoded.MemoryMB)
	assert.Equal(t, info.Metadata, decoded.Metadata)
}

func TestMatchesFilter(t *testing.T) {
	now := time.Now().UTC()
	info := &SnapshotInfo{
		TaskID:    "task-1",
		ServiceID: "nginx",
		NodeID:    "worker-1",
		CreatedAt: now,
	}

	tests := []struct {
		name   string
		filter SnapshotFilter
		match  bool
	}{
		{
			name:   "empty filter matches all",
			filter: SnapshotFilter{},
			match:  true,
		},
		{
			name:   "matching task ID",
			filter: SnapshotFilter{TaskID: "task-1"},
			match:  true,
		},
		{
			name:   "non-matching task ID",
			filter: SnapshotFilter{TaskID: "task-2"},
			match:  false,
		},
		{
			name:   "matching service ID",
			filter: SnapshotFilter{ServiceID: "nginx"},
			match:  true,
		},
		{
			name:   "non-matching service ID",
			filter: SnapshotFilter{ServiceID: "redis"},
			match:  false,
		},
		{
			name:   "matching node ID",
			filter: SnapshotFilter{NodeID: "worker-1"},
			match:  true,
		},
		{
			name:   "since before creation",
			filter: SnapshotFilter{Since: now.Add(-1 * time.Hour)},
			match:  true,
		},
		{
			name:   "since after creation",
			filter: SnapshotFilter{Since: now.Add(1 * time.Hour)},
			match:  false,
		},
		{
			name:   "before after creation",
			filter: SnapshotFilter{Before: now.Add(1 * time.Hour)},
			match:  true,
		},
		{
			name:   "before before creation",
			filter: SnapshotFilter{Before: now.Add(-1 * time.Hour)},
			match:  false,
		},
		{
			name: "combined filters",
			filter: SnapshotFilter{
				TaskID:    "task-1",
				ServiceID: "nginx",
				NodeID:    "worker-1",
			},
			match: true,
		},
		{
			name: "combined filters partial mismatch",
			filter: SnapshotFilter{
				TaskID:    "task-1",
				ServiceID: "nginx",
				NodeID:    "worker-2",
			},
			match: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.match, matchesFilter(info, tt.filter))
		})
	}
}

func TestListSnapshotsEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
	require.NoError(t, err)

	snapshots, err := mgr.ListSnapshots(SnapshotFilter{})
	require.NoError(t, err)
	assert.Empty(t, snapshots)
}

func TestListSnapshots(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
	require.NoError(t, err)

	// Create test snapshots directly
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID:        "snap-0001",
		TaskID:    "task-1",
		ServiceID: "nginx",
		NodeID:    "worker-1",
		CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
		SizeBytes: 100,
	})
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID:        "snap-0002",
		TaskID:    "task-2",
		ServiceID: "nginx",
		NodeID:    "worker-1",
		CreatedAt: time.Now().UTC().Add(-1 * time.Hour),
		SizeBytes: 200,
	})
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID:        "snap-0003",
		TaskID:    "task-3",
		ServiceID: "redis",
		NodeID:    "worker-2",
		CreatedAt: time.Now().UTC(),
		SizeBytes: 300,
	})

	// List all
	snapshots, err := mgr.ListSnapshots(SnapshotFilter{})
	require.NoError(t, err)
	assert.Len(t, snapshots, 3)

	// Filter by service
	snapshots, err = mgr.ListSnapshots(SnapshotFilter{ServiceID: "nginx"})
	require.NoError(t, err)
	assert.Len(t, snapshots, 2)

	// Filter by task
	snapshots, err = mgr.ListSnapshots(SnapshotFilter{TaskID: "task-1"})
	require.NoError(t, err)
	assert.Len(t, snapshots, 1)
	assert.Equal(t, "snap-0001", snapshots[0].ID)

	// Filter by node
	snapshots, err = mgr.ListSnapshots(SnapshotFilter{NodeID: "worker-2"})
	require.NoError(t, err)
	assert.Len(t, snapshots, 1)
	assert.Equal(t, "snap-0003", snapshots[0].ID)
}

func TestListSnapshotsSortedNewestFirst(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
	require.NoError(t, err)

	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID: "snap-old", CreatedAt: time.Now().UTC().Add(-3 * time.Hour),
	})
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID: "snap-new", CreatedAt: time.Now().UTC(),
	})
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID: "snap-mid", CreatedAt: time.Now().UTC().Add(-1 * time.Hour),
	})

	snapshots, err := mgr.ListSnapshots(SnapshotFilter{})
	require.NoError(t, err)
	require.Len(t, snapshots, 3)
	assert.Equal(t, "snap-new", snapshots[0].ID)
	assert.Equal(t, "snap-mid", snapshots[1].ID)
	assert.Equal(t, "snap-old", snapshots[2].ID)
}

func TestDeleteSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
	require.NoError(t, err)

	createTestSnapshot(t, tmpDir, &SnapshotInfo{ID: "snap-del"})

	err = mgr.DeleteSnapshot(context.Background(), "snap-del")
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(tmpDir, "snap-del"))
	assert.True(t, os.IsNotExist(err))

	// Delete again should fail
	err = mgr.DeleteSnapshot(context.Background(), "snap-del")
	assert.Error(t, err)
}

func TestDeleteSnapshotNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
	require.NoError(t, err)

	err = mgr.DeleteSnapshot(context.Background(), "snap-nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCleanupOldSnapshots(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{
		SnapshotDir: tmpDir,
		MaxAge:      24 * time.Hour,
	})
	require.NoError(t, err)

	// Create old and new snapshots
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID: "snap-old1", CreatedAt: time.Now().UTC().Add(-48 * time.Hour), SizeBytes: 100,
	})
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID: "snap-old2", CreatedAt: time.Now().UTC().Add(-25 * time.Hour), SizeBytes: 200,
	})
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID: "snap-new", CreatedAt: time.Now().UTC().Add(-1 * time.Hour), SizeBytes: 300,
	})

	removed, freed, err := mgr.CleanupOldSnapshots(context.Background(), 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 2, removed)
	assert.Equal(t, int64(300), freed)

	// Only new snapshot should remain
	snapshots, err := mgr.ListSnapshots(SnapshotFilter{})
	require.NoError(t, err)
	assert.Len(t, snapshots, 1)
	assert.Equal(t, "snap-new", snapshots[0].ID)
}

func TestCleanupOldSnapshotsUsesConfigDefault(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{
		SnapshotDir: tmpDir,
		MaxAge:      1 * time.Hour,
	})
	require.NoError(t, err)

	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID: "snap-old", CreatedAt: time.Now().UTC().Add(-2 * time.Hour), SizeBytes: 500,
	})
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID: "snap-new", CreatedAt: time.Now().UTC().Add(-30 * time.Minute), SizeBytes: 100,
	})

	// Pass 0 to use config default
	removed, freed, err := mgr.CleanupOldSnapshots(context.Background(), 0)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.Equal(t, int64(500), freed)
}

func TestEnforceMaxSnapshots(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{
		SnapshotDir:  tmpDir,
		MaxSnapshots: 2,
	})
	require.NoError(t, err)

	// Create 3 snapshots for the same service
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID: "snap-1", ServiceID: "nginx", CreatedAt: time.Now().UTC().Add(-3 * time.Hour), SizeBytes: 100,
	})
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID: "snap-2", ServiceID: "nginx", CreatedAt: time.Now().UTC().Add(-2 * time.Hour), SizeBytes: 200,
	})
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID: "snap-3", ServiceID: "nginx", CreatedAt: time.Now().UTC().Add(-1 * time.Hour), SizeBytes: 300,
	})
	// Different service should not be affected
	createTestSnapshot(t, tmpDir, &SnapshotInfo{
		ID: "snap-4", ServiceID: "redis", CreatedAt: time.Now().UTC().Add(-1 * time.Hour), SizeBytes: 400,
	})

	// Trigger enforcement
	mgr.mu.Lock()
	mgr.enforceMaxSnapshots("nginx")
	mgr.mu.Unlock()

	// Should have 2 nginx snapshots (newest) + 1 redis
	snapshots, err := mgr.ListSnapshots(SnapshotFilter{})
	require.NoError(t, err)
	assert.Len(t, snapshots, 3)

	nginxSnaps, err := mgr.ListSnapshots(SnapshotFilter{ServiceID: "nginx"})
	require.NoError(t, err)
	assert.Len(t, nginxSnaps, 2)
	assert.Equal(t, "snap-3", nginxSnaps[0].ID)
	assert.Equal(t, "snap-2", nginxSnaps[1].ID)
}

func TestSHA256File(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")

	err := os.WriteFile(path, []byte("hello world"), 0644)
	require.NoError(t, err)

	checksum, err := sha256File(path)
	require.NoError(t, err)

	// Expected SHA-256 of "hello world"
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	assert.Equal(t, expected, checksum)
}

func TestSHA256FileNonexistent(t *testing.T) {
	_, err := sha256File("/nonexistent/path")
	assert.Error(t, err)
}

func TestGenerateSnapshotID(t *testing.T) {
	id1 := generateSnapshotID("task-1")
	id2 := generateSnapshotID("task-1")
	id3 := generateSnapshotID("task-2")

	// IDs should start with "snap-"
	assert.True(t, strings.HasPrefix(id1, "snap-"))
	assert.True(t, strings.HasPrefix(id2, "snap-"))
	assert.True(t, strings.HasPrefix(id3, "snap-"))

	// Same task ID with different timestamps should produce different IDs
	assert.NotEqual(t, id1, id2)

	// Different task IDs should produce different IDs
	assert.NotEqual(t, id1, id3)
}

func TestFileSize(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")

	err := os.WriteFile(path, []byte("hello"), 0644)
	require.NoError(t, err)

	size, err := fileSize(path)
	require.NoError(t, err)
	assert.Equal(t, int64(5), size)
}

func TestFileSizeNonexistent(t *testing.T) {
	_, err := fileSize("/nonexistent/file")
	assert.Error(t, err)
}

// --- Helpers ---

func createTestSnapshot(t *testing.T, baseDir string, info *SnapshotInfo) {
	t.Helper()
	snapshotDir := filepath.Join(baseDir, info.ID)
	require.NoError(t, os.MkdirAll(snapshotDir, 0755))
	require.NoError(t, saveMetadata(snapshotDir, info))

	// Create dummy state and memory files so size info is realistic
	if info.SizeBytes > 0 {
		require.NoError(t, os.WriteFile(filepath.Join(snapshotDir, "vm.state"), make([]byte, info.SizeBytes/2), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(snapshotDir, "vm.mem"), make([]byte, info.SizeBytes/2), 0644))
	}
}
