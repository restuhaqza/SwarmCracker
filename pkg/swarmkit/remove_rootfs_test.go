package swarmkit

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestController_Remove_KeepsSharedImageRootfs proves Remove must NOT delete the
// shared, per-image rootfs (the cache). Mounts are applied to an ephemeral
// per-task copy instead, so the image cache stays intact.
func TestController_Remove_KeepsSharedImageRootfs(t *testing.T) {
	tmpDir := t.TempDir()

	sharedRootfs := filepath.Join(tmpDir, "nginx-latest.ext4")
	require.NoError(t, os.WriteFile(sharedRootfs, []byte("shared-cache"), 0644))

	ctrl := &Controller{
		task:   &api.Task{ID: "task-123"},
		config: &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		internalTask: &types.Task{
			ID:          "task-123",
			Annotations: map[string]string{"rootfs": sharedRootfs},
		},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}

	require.NoError(t, ctrl.Remove(context.Background()))

	_, err := os.Stat(sharedRootfs)
	assert.NoError(t, err, "the shared image rootfs (cache) must be kept")
}

// TestController_Remove_DeletesEphemeralRootfs proves the per-task rootfs copy
// (created for mount-bearing tasks) is removed, while the shared image cache is
// left alone.
func TestController_Remove_DeletesEphemeralRootfs(t *testing.T) {
	tmpDir := t.TempDir()

	sharedRootfs := filepath.Join(tmpDir, "nginx-latest.ext4")
	require.NoError(t, os.WriteFile(sharedRootfs, []byte("shared-cache"), 0644))

	privateRootfs := filepath.Join(tmpDir, "task-123.ext4")
	require.NoError(t, os.WriteFile(privateRootfs, []byte("private"), 0644))

	ctrl := &Controller{
		task:   &api.Task{ID: "task-123"},
		config: &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		internalTask: &types.Task{
			ID: "task-123",
			Annotations: map[string]string{
				"rootfs":           privateRootfs,
				"rootfs_ephemeral": "true",
			},
		},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}

	require.NoError(t, ctrl.Remove(context.Background()))

	_, err := os.Stat(privateRootfs)
	assert.True(t, os.IsNotExist(err), "the ephemeral per-task rootfs should be deleted, got err=%v", err)

	_, err = os.Stat(sharedRootfs)
	assert.NoError(t, err, "the shared image rootfs (cache) must be kept")
}

// TestController_Remove_FallsBackToLegacyPath keeps the old behavior when no
// annotation is present (e.g. Remove called before a successful Prepare).
func TestController_Remove_FallsBackToLegacyPath(t *testing.T) {
	tmpDir := t.TempDir()
	legacyPath := filepath.Join(tmpDir, "task-456.ext4")
	require.NoError(t, os.WriteFile(legacyPath, []byte("legacy"), 0644))

	ctrl := &Controller{
		task:       &api.Task{ID: "task-456"},
		config:     &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
		// internalTask nil → fallback path
	}

	err := ctrl.Remove(context.Background())
	require.NoError(t, err)

	_, err = os.Stat(legacyPath)
	assert.True(t, os.IsNotExist(err), "legacy fallback path should be deleted")
}
