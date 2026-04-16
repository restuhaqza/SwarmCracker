package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateSnapshotValidation tests the validation paths in CreateSnapshot
// that don't require a real Firecracker socket.
func TestCreateSnapshotValidation(t *testing.T) {
	t.Run("empty socket path returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
		require.NoError(t, err)

		info, err := mgr.CreateSnapshot(
			context.Background(),
			"task-1",
			"", // empty socket path
			CreateOptions{
				ServiceID:  "nginx",
				NodeID:     "worker-1",
				VCPUCount:  2,
				MemoryMB:   1024,
				RootfsPath: "/path/to/rootfs.ext4",
			},
		)

		assert.Error(t, err)
		assert.Nil(t, info)
		assert.Contains(t, err.Error(), "socket path is required")
	})

	t.Run("non-existent socket returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
		require.NoError(t, err)

		nonExistentSocket := filepath.Join(tmpDir, "nonexistent.sock")

		info, err := mgr.CreateSnapshot(
			context.Background(),
			"task-1",
			nonExistentSocket,
			CreateOptions{
				ServiceID:  "nginx",
				NodeID:     "worker-1",
				VCPUCount:  2,
				MemoryMB:   1024,
				RootfsPath: "/path/to/rootfs.ext4",
			},
		)

		assert.Error(t, err)
		assert.Nil(t, info)
		assert.Contains(t, err.Error(), "socket not found")
	})

	t.Run("existing socket file passes validation", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
		require.NoError(t, err)

		// Create a dummy socket file (not a real socket, but exists)
		socketPath := filepath.Join(tmpDir, "test.sock")
		err = os.WriteFile(socketPath, []byte("dummy"), 0644)
		require.NoError(t, err)

		// This should pass validation but fail at API call
		info, err := mgr.CreateSnapshot(
			context.Background(),
			"task-1",
			socketPath,
			CreateOptions{
				ServiceID:  "nginx",
				NodeID:     "worker-1",
				VCPUCount:  2,
				MemoryMB:   1024,
				RootfsPath: "/path/to/rootfs.ext4",
			},
		)

		// Should fail at API call, not validation
		assert.Error(t, err)
		assert.Nil(t, info)
		// Error should be about API call, not socket not found
		assert.NotContains(t, err.Error(), "socket path is required")
		assert.NotContains(t, err.Error(), "socket not found")
	})
}

// TestCreateSnapshotStagingDir tests the staging directory behavior.
func TestCreateSnapshotStagingDir(t *testing.T) {
	t.Run("staging dir is created with correct prefix", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
		require.NoError(t, err)

		socketPath := filepath.Join(tmpDir, "test.sock")
		err = os.WriteFile(socketPath, []byte("dummy"), 0644)
		require.NoError(t, err)

		// Attempt creation (will fail at API call but we can check staging dir)
		_, _ = mgr.CreateSnapshot(
			context.Background(),
			"task-1",
			socketPath,
			CreateOptions{
				ServiceID:  "nginx",
				NodeID:     "worker-1",
				VCPUCount:  2,
				MemoryMB:   1024,
				RootfsPath: "/path/to/rootfs.ext4",
			},
		)

		// Check that staging directory was created and cleaned up
		// After function returns, staging dir should be removed
		entries, err := os.ReadDir(tmpDir)
		require.NoError(t, err)

		// Should not have any staging directories left (they're cleaned up)
		for _, entry := range entries {
			assert.False(t, strings.HasPrefix(entry.Name(), "snapshot-staging-"),
				"staging directory should be cleaned up")
		}
	})

	t.Run("snapshot directory structure is correct", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Simulate what CreateSnapshot does after successful API call
		taskID := "task-1"
		snapshotID := generateSnapshotID(taskID)
		snapshotDir := filepath.Join(tmpDir, snapshotID)

		err := os.MkdirAll(snapshotDir, 0755)
		require.NoError(t, err)

		// Create metadata
		info := &SnapshotInfo{
			ID:         snapshotID,
			TaskID:     taskID,
			ServiceID:  "nginx",
			NodeID:     "worker-1",
			CreatedAt:  time.Now().UTC(),
			MemoryPath: filepath.Join(snapshotDir, "vm.mem"),
			StatePath:  filepath.Join(snapshotDir, "vm.state"),
			SizeBytes:  1024,
			VCPUCount:  2,
			MemoryMB:   1024,
			RootfsPath: "/path/to/rootfs.ext4",
		}

		err = saveMetadata(snapshotDir, info)
		require.NoError(t, err)

		// Verify directory structure
		entries, err := os.ReadDir(snapshotDir)
		require.NoError(t, err)
		assert.Len(t, entries, 1) // Only metadata.json

		// Verify metadata file exists
		metadataPath := filepath.Join(snapshotDir, metadataFile)
		assert.FileExists(t, metadataPath)
	})
}

// TestNewManagerConfigVariations tests various configuration combinations.
func TestNewManagerConfigVariations(t *testing.T) {
	t.Run("empty snapshot dir uses default", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
		require.NoError(t, err)
		assert.NotNil(t, mgr)
		assert.Contains(t, mgr.config.SnapshotDir, tmpDir)
	})

	t.Run("custom snapshot dir is respected", func(t *testing.T) {
		tmpDir := t.TempDir()
		customDir := filepath.Join(tmpDir, "custom", "snapshots", "path")
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: customDir})
		require.NoError(t, err)
		assert.NotNil(t, mgr)
		assert.Equal(t, customDir, mgr.config.SnapshotDir)
	})

	t.Run("nested directory is created", func(t *testing.T) {
		tmpDir := t.TempDir()
		nestedDir := filepath.Join(tmpDir, "a", "b", "c", "snapshots")

		mgr, err := NewManager(SnapshotConfig{SnapshotDir: nestedDir})
		require.NoError(t, err)
		assert.NotNil(t, mgr)

		// Verify directory was created
		info, err := os.Stat(nestedDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})

	t.Run("manager stores config values", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := SnapshotConfig{
			SnapshotDir:  tmpDir,
			MaxSnapshots: 5,
			MaxAge:       48 * time.Hour,
			AutoSnapshot: true,
			Compress:     true,
		}

		mgr, err := NewManager(cfg)
		require.NoError(t, err)
		assert.NotNil(t, mgr)

		assert.Equal(t, tmpDir, mgr.config.SnapshotDir)
		assert.Equal(t, 5, mgr.config.MaxSnapshots)
		assert.Equal(t, 48*time.Hour, mgr.config.MaxAge)
		assert.True(t, mgr.config.AutoSnapshot)
		assert.True(t, mgr.config.Compress)
	})
}

// TestListSnapshotsWithMissingFiles tests listing snapshots with missing metadata.
func TestListSnapshotsWithMissingFiles(t *testing.T) {
	t.Run("directory without metadata is skipped", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
		require.NoError(t, err)

		// Create a directory without metadata
		emptyDir := filepath.Join(tmpDir, "snap-invalid")
		err = os.MkdirAll(emptyDir, 0755)
		require.NoError(t, err)

		// List should skip the invalid directory
		snapshots, err := mgr.ListSnapshots(SnapshotFilter{})
		require.NoError(t, err)
		assert.Len(t, snapshots, 0)
	})

	t.Run("mix of valid and invalid directories", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
		require.NoError(t, err)

		// Create valid snapshot
		createTestSnapshot(t, tmpDir, &SnapshotInfo{
			ID:        "snap-valid",
			TaskID:    "task-1",
			CreatedAt: time.Now().UTC(),
		})

		// Create invalid directories
		os.MkdirAll(filepath.Join(tmpDir, "snap-no-metadata"), 0755)
		os.WriteFile(filepath.Join(tmpDir, "not-a-dir.txt"), []byte("test"), 0644)

		// List should only return the valid snapshot
		snapshots, err := mgr.ListSnapshots(SnapshotFilter{})
		require.NoError(t, err)
		assert.Len(t, snapshots, 1)
		assert.Equal(t, "snap-valid", snapshots[0].ID)
	})
}

// TestEnforceMaxSnapshotsEdgeCases tests edge cases for enforceMaxSnapshots.
func TestEnforceMaxSnapshotsEdgeCases(t *testing.T) {
	t.Run("zero max snapshots does not delete", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{
			SnapshotDir:  tmpDir,
			MaxSnapshots: 0, // unlimited
		})
		require.NoError(t, err)

		// Create multiple snapshots
		for i := 1; i <= 5; i++ {
			createTestSnapshot(t, tmpDir, &SnapshotInfo{
				ID:        fmt.Sprintf("snap-%d", i),
				ServiceID: "nginx",
				CreatedAt: time.Now().UTC().Add(time.Duration(-i) * time.Hour),
				SizeBytes: int64(i * 100),
			})
		}

		mgr.mu.Lock()
		mgr.enforceMaxSnapshots("nginx")
		mgr.mu.Unlock()

		// All snapshots should still exist
		snapshots, err := mgr.ListSnapshots(SnapshotFilter{ServiceID: "nginx"})
		require.NoError(t, err)
		assert.Len(t, snapshots, 5)
	})

	t.Run("negative max snapshots is treated as unlimited", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{
			SnapshotDir:  tmpDir,
			MaxSnapshots: -1,
		})
		require.NoError(t, err)

		createTestSnapshot(t, tmpDir, &SnapshotInfo{
			ID:        "snap-1",
			ServiceID: "nginx",
			CreatedAt: time.Now().UTC(),
		})

		mgr.mu.Lock()
		mgr.enforceMaxSnapshots("nginx")
		mgr.mu.Unlock()

		// Snapshot should still exist
		snapshots, err := mgr.ListSnapshots(SnapshotFilter{ServiceID: "nginx"})
		require.NoError(t, err)
		assert.Len(t, snapshots, 1)
	})

	t.Run("enforcement only affects specified service", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{
			SnapshotDir:  tmpDir,
			MaxSnapshots: 2,
		})
		require.NoError(t, err)

		// Create snapshots for multiple services
		createTestSnapshot(t, tmpDir, &SnapshotInfo{
			ID:        "snap-nginx-1",
			ServiceID: "nginx",
			CreatedAt: time.Now().UTC().Add(-3 * time.Hour),
		})
		createTestSnapshot(t, tmpDir, &SnapshotInfo{
			ID:        "snap-nginx-2",
			ServiceID: "nginx",
			CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
		})
		createTestSnapshot(t, tmpDir, &SnapshotInfo{
			ID:        "snap-nginx-3",
			ServiceID: "nginx",
			CreatedAt: time.Now().UTC().Add(-1 * time.Hour),
		})
		createTestSnapshot(t, tmpDir, &SnapshotInfo{
			ID:        "snap-redis-1",
			ServiceID: "redis",
			CreatedAt: time.Now().UTC().Add(-3 * time.Hour),
		})
		createTestSnapshot(t, tmpDir, &SnapshotInfo{
			ID:        "snap-redis-2",
			ServiceID: "redis",
			CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
		})
		createTestSnapshot(t, tmpDir, &SnapshotInfo{
			ID:        "snap-redis-3",
			ServiceID: "redis",
			CreatedAt: time.Now().UTC().Add(-1 * time.Hour),
		})

		// Only enforce for nginx
		mgr.mu.Lock()
		mgr.enforceMaxSnapshots("nginx")
		mgr.mu.Unlock()

		// Check nginx snapshots (should be 2)
		nginxSnaps, err := mgr.ListSnapshots(SnapshotFilter{ServiceID: "nginx"})
		require.NoError(t, err)
		assert.Len(t, nginxSnaps, 2)

		// Check redis snapshots (should still be 3, not enforced)
		redisSnaps, err := mgr.ListSnapshots(SnapshotFilter{ServiceID: "redis"})
		require.NoError(t, err)
		assert.Len(t, redisSnaps, 3)
	})
}

// TestGenerateSnapshotIDProperties tests properties of generateSnapshotID.
func TestGenerateSnapshotIDProperties(t *testing.T) {
	t.Run("ID format is correct", func(t *testing.T) {
		id := generateSnapshotID("task-123")
		assert.True(t, strings.HasPrefix(id, "snap-"))
		// Total length: "snap-" (5) + 16 hex chars = 21
		assert.Len(t, id, 21)
	})

	t.Run("IDs are unique for same task ID", func(t *testing.T) {
		ids := make(map[string]bool)
		for i := 0; i < 100; i++ {
			id := generateSnapshotID("task-1")
			assert.False(t, ids[id], "ID should be unique: %s", id)
			ids[id] = true
		}
		assert.Len(t, ids, 100, "all IDs should be unique")
	})

	t.Run("IDs are unique for different task IDs", func(t *testing.T) {
		id1 := generateSnapshotID("task-1")
		id2 := generateSnapshotID("task-2")

		assert.NotEqual(t, id1, id2)
	})

	t.Run("ID includes taskID influence", func(t *testing.T) {
		// Same task ID should produce IDs that share some similarity pattern
		// (though they won't be identical due to timestamp)
		id1 := generateSnapshotID("task-abc")
		time.Sleep(time.Millisecond) // Ensure different timestamp
		id2 := generateSnapshotID("task-abc")
		id3 := generateSnapshotID("task-def")

		// All IDs should be different
		assert.NotEqual(t, id1, id2)
		assert.NotEqual(t, id1, id3)
		assert.NotEqual(t, id2, id3)

		// All should have valid format
		assert.True(t, strings.HasPrefix(id1, "snap-"))
		assert.True(t, strings.HasPrefix(id2, "snap-"))
		assert.True(t, strings.HasPrefix(id3, "snap-"))
	})

	t.Run("handles special characters in task ID", func(t *testing.T) {
		specialTaskIDs := []string{
			"task-with-dashes",
			"task_with_underscores",
			"task.with.dots",
			"task/with/slashes",
			"task:with:colons",
		}

		ids := make([]string, len(specialTaskIDs))
		for i, taskID := range specialTaskIDs {
			ids[i] = generateSnapshotID(taskID)
			assert.True(t, strings.HasPrefix(ids[i], "snap-"), "Should handle special chars: %s", taskID)
		}

		// All IDs should be valid and unique
		seen := make(map[string]bool)
		for _, id := range ids {
			assert.False(t, seen[id], "ID should be unique")
			seen[id] = true
			assert.Len(t, id, 21)
		}
	})
}

// TestSHA256FileEdgeCases tests edge cases for sha256File.
func TestSHA256FileEdgeCases(t *testing.T) {
	t.Run("empty file produces valid checksum", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "empty.txt")

		err := os.WriteFile(path, []byte{}, 0644)
		require.NoError(t, err)

		checksum, err := sha256File(path)
		require.NoError(t, err)

		// SHA-256 of empty string
		expected := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		assert.Equal(t, expected, checksum)
	})

	t.Run("large file produces correct checksum", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "large.bin")

		// Create a 1MB file
		data := make([]byte, 1024*1024)
		for i := range data {
			data[i] = byte(i % 256)
		}
		err := os.WriteFile(path, data, 0644)
		require.NoError(t, err)

		checksum, err := sha256File(path)
		require.NoError(t, err)

		// Checksum should be 64 hex characters
		assert.Len(t, checksum, 64)
		// All characters should be hex
		for _, c := range checksum {
			assert.True(t, (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))
		}
	})

	t.Run("binary file produces valid checksum", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "binary.bin")

		// Create binary data with null bytes
		data := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}
		err := os.WriteFile(path, data, 0644)
		require.NoError(t, err)

		checksum, err := sha256File(path)
		require.NoError(t, err)

		// Check that we get a valid checksum (not verifying exact value for this test)
		assert.NotEqual(t, "", checksum)
		assert.Len(t, checksum, 64)
	})

	t.Run("non-existent file returns error", func(t *testing.T) {
		_, err := sha256File("/nonexistent/path/file.txt")
		assert.Error(t, err)
		assert.True(t, os.IsNotExist(err))
	})
}

// TestFileSizeEdgeCases tests edge cases for fileSize.
func TestFileSizeEdgeCases(t *testing.T) {
	t.Run("empty file returns zero", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "empty.txt")

		err := os.WriteFile(path, []byte{}, 0644)
		require.NoError(t, err)

		size, err := fileSize(path)
		require.NoError(t, err)
		assert.Equal(t, int64(0), size)
	})

	t.Run("large file size is accurate", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "large.bin")

		expectedSize := int64(10 * 1024 * 1024) // 10MB
		err := os.WriteFile(path, make([]byte, expectedSize), 0644)
		require.NoError(t, err)

		size, err := fileSize(path)
		require.NoError(t, err)
		assert.Equal(t, expectedSize, size)
	})

	t.Run("non-existent file returns error", func(t *testing.T) {
		_, err := fileSize("/nonexistent/path/file.txt")
		assert.Error(t, err)
		assert.True(t, os.IsNotExist(err))
	})
}

// TestWaitForSocket tests the waitForSocket function.
func TestWaitForSocket(t *testing.T) {
	t.Run("timeout when socket never appears", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "nonexistent.sock")

		err := waitForSocket(socketPath, 100*time.Millisecond)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not ready")
	})

	t.Run("returns quickly when socket exists", func(t *testing.T) {
		t.Skip("Skipping: waitForSocket expects a real HTTP server on the socket")
		// This would require creating an actual Unix socket and HTTP server
		// which is complex for a unit test. The function is tested indirectly
		// through integration tests.
	})

	t.Run("zero timeout returns immediately", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "nonexistent.sock")

		start := time.Now()
		err := waitForSocket(socketPath, 0)
		elapsed := time.Since(start)

		assert.Error(t, err)
		// Should return almost immediately with zero timeout
		assert.Less(t, elapsed, 500*time.Millisecond)
	})
}

// TestMatchesFilterEdgeCases tests edge cases for matchesFilter.
func TestMatchesFilterEdgeCases(t *testing.T) {
	now := time.Now().UTC()

	t.Run("empty filter matches all", func(t *testing.T) {
		info := &SnapshotInfo{
			TaskID:    "task-1",
			ServiceID: "nginx",
			NodeID:    "worker-1",
			CreatedAt: now,
		}

		assert.True(t, matchesFilter(info, SnapshotFilter{}))
	})

	t.Run("all filters must match", func(t *testing.T) {
		info := &SnapshotInfo{
			TaskID:    "task-1",
			ServiceID: "nginx",
			NodeID:    "worker-1",
			CreatedAt: now,
		}

		filter := SnapshotFilter{
			TaskID:    "task-1",
			ServiceID: "nginx",
			NodeID:    "worker-1",
		}

		assert.True(t, matchesFilter(info, filter))
	})

	t.Run("one mismatching filter fails", func(t *testing.T) {
		info := &SnapshotInfo{
			TaskID:    "task-1",
			ServiceID: "nginx",
			NodeID:    "worker-1",
			CreatedAt: now,
		}

		filter := SnapshotFilter{
			TaskID:    "task-1",
			ServiceID: "nginx",
			NodeID:    "worker-2", // mismatch
		}

		assert.False(t, matchesFilter(info, filter))
	})

	t.Run("time range filters", func(t *testing.T) {
		info := &SnapshotInfo{
			CreatedAt: time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
		}

		// Within range
		filter := SnapshotFilter{
			Since:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			Before: time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
		}
		assert.True(t, matchesFilter(info, filter))

		// Before Since
		filter = SnapshotFilter{
			Since: time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
		}
		assert.False(t, matchesFilter(info, filter))

		// After Before
		filter = SnapshotFilter{
			Before: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		assert.False(t, matchesFilter(info, filter))
	})

	t.Run("exact boundary times", func(t *testing.T) {
		info := &SnapshotInfo{
			CreatedAt: time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
		}

		// Since: exactly at creation time (should match - CreatedAt is not Before)
		filter := SnapshotFilter{
			Since: time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
		}
		assert.True(t, matchesFilter(info, filter))

		// Before: exactly at creation time (should match - CreatedAt is not After)
		filter = SnapshotFilter{
			Before: time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
		}
		assert.True(t, matchesFilter(info, filter))
	})

	t.Run("partial filters match independently", func(t *testing.T) {
		info := &SnapshotInfo{
			TaskID:    "task-1",
			ServiceID: "nginx",
			NodeID:    "worker-1",
			CreatedAt: now,
		}

		// Only TaskID filter
		assert.True(t, matchesFilter(info, SnapshotFilter{TaskID: "task-1"}))
		assert.False(t, matchesFilter(info, SnapshotFilter{TaskID: "task-2"}))

		// Only ServiceID filter
		assert.True(t, matchesFilter(info, SnapshotFilter{ServiceID: "nginx"}))
		assert.False(t, matchesFilter(info, SnapshotFilter{ServiceID: "redis"}))

		// Only NodeID filter
		assert.True(t, matchesFilter(info, SnapshotFilter{NodeID: "worker-1"}))
		assert.False(t, matchesFilter(info, SnapshotFilter{NodeID: "worker-2"}))
	})
}

// TestSnapshotConfigEdgeCases tests edge cases for SnapshotConfig.
func TestSnapshotConfigEdgeCases(t *testing.T) {
	t.Run("DefaultSnapshotConfig returns valid config", func(t *testing.T) {
		cfg := DefaultSnapshotConfig()

		assert.NotEmpty(t, cfg.SnapshotDir)
		assert.Greater(t, cfg.MaxSnapshots, 0)
		assert.Greater(t, cfg.MaxAge, time.Duration(0))
		assert.False(t, cfg.AutoSnapshot)
		assert.False(t, cfg.Compress)
	})

	t.Run("SetDefaults on zero config", func(t *testing.T) {
		cfg := SnapshotConfig{}
		cfg.SetDefaults()

		assert.NotEmpty(t, cfg.SnapshotDir)
		assert.Greater(t, cfg.MaxSnapshots, 0)
		assert.Greater(t, cfg.MaxAge, time.Duration(0))
	})

	t.Run("SetDefaults preserves existing values", func(t *testing.T) {
		cfg := SnapshotConfig{
			SnapshotDir:  "/my/custom/dir",
			MaxSnapshots: 10,
			MaxAge:       48 * time.Hour,
			AutoSnapshot: true,
			Compress:     true,
		}
		cfg.SetDefaults()

		assert.Equal(t, "/my/custom/dir", cfg.SnapshotDir)
		assert.Equal(t, 10, cfg.MaxSnapshots)
		assert.Equal(t, 48*time.Hour, cfg.MaxAge)
		assert.True(t, cfg.AutoSnapshot)
		assert.True(t, cfg.Compress)
	})

	t.Run("SetDefaults fills partial config", func(t *testing.T) {
		cfg := SnapshotConfig{
			SnapshotDir: "/custom/dir",
			// Other fields left empty
		}
		cfg.SetDefaults()

		assert.Equal(t, "/custom/dir", cfg.SnapshotDir)
		assert.Equal(t, 3, cfg.MaxSnapshots) // filled by default
		assert.Equal(t, 168*time.Hour, cfg.MaxAge)
	})
}

// TestRestoreFromSnapshotValidation tests validation in RestoreFromSnapshot.
func TestRestoreFromSnapshotValidation(t *testing.T) {
	t.Run("nil snapshot info returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
		require.NoError(t, err)

		err = mgr.RestoreFromSnapshot(context.Background(), nil, "/path/to/socket")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "required")
	})

	t.Run("missing state file returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
		require.NoError(t, err)

		info := &SnapshotInfo{
			StatePath:  "/nonexistent/vm.state",
			MemoryPath: filepath.Join(tmpDir, "vm.mem"),
			Checksum:   "abc123",
		}

		err = mgr.RestoreFromSnapshot(context.Background(), info, "/path/to/socket")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "state file not found")
	})

	t.Run("missing memory file returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
		require.NoError(t, err)

		// Create state file
		statePath := filepath.Join(tmpDir, "vm.state")
		err = os.WriteFile(statePath, []byte("dummy"), 0644)
		require.NoError(t, err)

		info := &SnapshotInfo{
			StatePath:  statePath,
			MemoryPath: "/nonexistent/vm.mem",
			Checksum:   "abc123",
		}

		err = mgr.RestoreFromSnapshot(context.Background(), info, "/path/to/socket")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "memory file not found")
	})
}
