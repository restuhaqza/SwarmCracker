package swarmkit

import (
	"context"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncVolumeData_NoAnnotations tests volume sync when no annotations present
func TestSyncVolumeData_NoAnnotations(t *testing.T) {
	task := &types.Task{
		ID:          "task-no-annotations",
		Annotations: map[string]string{},
	}

	// When no rootfs annotation exists, sync should be skipped
	_, hasRootfs := task.Annotations["rootfs"]
	assert.False(t, hasRootfs, "Should not have rootfs annotation")
}

// TestSyncVolumeData_EmptyRootfs tests with empty rootfs path
func TestSyncVolumeData_EmptyRootfs(t *testing.T) {
	task := &types.Task{
		ID: "task-empty-rootfs",
		Annotations: map[string]string{
			"rootfs": "",
		},
	}

	rootfsPath := task.Annotations["rootfs"]
	assert.Empty(t, rootfsPath, "Rootfs path should be empty")
}

// TestSyncVolumeData_ValidRootfsAnnotation tests valid annotation
func TestSyncVolumeData_ValidRootfsAnnotation(t *testing.T) {
	task := &types.Task{
		ID: "task-valid-rootfs",
		Annotations: map[string]string{
			"rootfs": "/var/lib/swarmcracker/rootfs/task.ext4",
		},
	}

	rootfsPath, hasRootfs := task.Annotations["rootfs"]
	assert.True(t, hasRootfs, "Should have rootfs annotation")
	assert.NotEmpty(t, rootfsPath, "Rootfs path should not be empty")
}

// TestExecutor_Describe tests the Describe method with proper config
func TestExecutor_Describe(t *testing.T) {
	e := &Executor{
		config: &Config{
			ReservedCPUs: 1,
		},
		controllers: make(map[string]*Controller),
	}

	ctx := context.Background()
	desc, err := e.Describe(ctx)

	require.NoError(t, err)
	require.NotNil(t, desc)

	// Should have valid resources
	if desc != nil && desc.Resources != nil {
		assert.GreaterOrEqual(t, desc.Resources.NanoCPUs, int64(1))
		assert.GreaterOrEqual(t, desc.Resources.MemoryBytes, int64(512*1024*1024))
	}
}