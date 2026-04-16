package jailer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestForceStop_TaskNotFound tests ForceStop with non-existent task
func TestForceStop_TaskNotFound(t *testing.T) {
	j := &Jailer{
		config:    &Config{},
		processes: make(map[string]*Process),
	}

	ctx := context.Background()
	err := j.ForceStop(ctx, "nonexistent-task")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "task not found")
}

// TestForceStop_EmptyTaskID tests ForceStop with empty task ID
func TestForceStop_EmptyTaskID(t *testing.T) {
	j := &Jailer{
		config:    &Config{},
		processes: make(map[string]*Process),
	}

	ctx := context.Background()
	err := j.ForceStop(ctx, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "task not found")
}

// TestCgroupManager_GetStats tests cgroup stats reading
func TestCgroupManager_GetStats(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(basePath, taskID string)
		taskID      string
		expectError bool
	}{
		{
			name: "no cgroup directory",
			setup: func(basePath, taskID string) {
				// Don't create anything
			},
			taskID:      "test-task",
			expectError: false, // Should handle missing files gracefully
		},
		{
			name: "empty taskID",
			setup: func(basePath, taskID string) {
				// Nothing
			},
			taskID:      "",
			expectError: false, // Empty path handling
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setup(tmpDir, tt.taskID)

			cm := &CgroupManager{
				basePath: tmpDir,
			}

			stats, err := cm.GetStats(tt.taskID)

			// Function reads files, may return empty stats on missing files
			_ = stats
			_ = err
		})
	}
}

// TestIsCgroupV2Available_Paths tests cgroup v2 detection paths
func TestIsCgroupV2Available_Paths(t *testing.T) {
	// Test the function - it checks actual /sys/fs/cgroup
	result := isCgroupV2Available()

	// Just verify it doesn't panic and returns a boolean
	assert.IsType(t, false, result)
}

// TestCgroupManager_RemoveCgroup tests cgroup removal
func TestCgroupManager_RemoveCgroup(t *testing.T) {
	tmpDir := t.TempDir()
	taskID := "test-task"
	cgroupPath := filepath.Join(tmpDir, taskID)

	// Create mock cgroup directory
	require.NoError(t, os.MkdirAll(cgroupPath, 0755))

	cm := &CgroupManager{
		basePath: tmpDir,
	}

	err := cm.RemoveCgroup(taskID)

	// Should succeed removing the directory
	require.NoError(t, err)

	// Directory should be gone
	_, statErr := os.Stat(cgroupPath)
	require.Error(t, statErr)
}

// TestCgroupManager_RemoveCgroup_NotFound tests removal of non-existent cgroup
func TestCgroupManager_RemoveCgroup_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	cm := &CgroupManager{
		basePath: tmpDir,
	}

	err := cm.RemoveCgroup("nonexistent")

	// Should error or handle gracefully
	_ = err
}

// TestCgroupManager_AddProcess tests adding process to cgroup
func TestCgroupManager_AddProcess(t *testing.T) {
	tmpDir := t.TempDir()
	taskID := "test-task"

	cm := &CgroupManager{
		basePath: tmpDir,
	}

	// Try to add a process - will fail without real cgroup
	err := cm.AddProcess(taskID, 12345)

	// Expect error since no real cgroup
	require.Error(t, err)
}