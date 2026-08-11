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

// TestController_Remove_UsesAnnotationRootfs proves C3: Controller.Remove must
// delete the ACTUAL rootfs path recorded in internalTask.Annotations["rootfs"]
// (named by image ID), not the legacy task.ID+".ext4" path which never exists.
//
// Before the fix: Remove computed rootfsDir/task.ID+".ext4", so the real
// rootfs file was never deleted (disk leak). After the fix: the annotation
// path is removed.
func TestController_Remove_UsesAnnotationRootfs(t *testing.T) {
	tmpDir := t.TempDir()

	// The real rootfs as recorded by image preparation (image-ID based).
	realRootfs := filepath.Join(tmpDir, "nginx-latest.ext4")
	require.NoError(t, os.WriteFile(realRootfs, []byte("fake-rootfs"), 0644))

	// A decoy at the legacy task-ID path — the old code deleted THIS
	// (or tried to) and left the real file behind.
	legacyPath := filepath.Join(tmpDir, "task-123.ext4")
	require.NoError(t, os.WriteFile(legacyPath, []byte("decoy"), 0644))

	ctrl := &Controller{
		task:   &api.Task{ID: "task-123"},
		config: &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		internalTask: &types.Task{
			ID: "task-123",
			Annotations: map[string]string{
				"rootfs": realRootfs,
			},
		},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}

	err := ctrl.Remove(context.Background())
	require.NoError(t, err)

	// The real rootfs (annotation path) must be gone.
	_, err = os.Stat(realRootfs)
	assert.True(t, os.IsNotExist(err), "real rootfs should be deleted, got err=%v", err)

	// The legacy decoy path must NOT be deleted by this Remove (it is not
	// this task's rootfs; removing it would be wrong).
	_, err = os.Stat(legacyPath)
	assert.NoError(t, err, "legacy decoy path should still exist")
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
