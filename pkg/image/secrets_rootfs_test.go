package image

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrepare_SecretsForcePrivateRootfs verifies that a task with secrets or
// configs gets a private, per-task rootfs copy instead of the shared image
// cache, so payloads can never leak between tasks.
func TestPrepare_SecretsForcePrivateRootfs(t *testing.T) {
	rootfsDir := t.TempDir()
	imageID := generateImageID("secrets-private:v1")
	rootfsPath := filepath.Join(rootfsDir, imageID+".ext4")
	require.NoError(t, os.WriteFile(rootfsPath, []byte("rootfs"), 0644))
	markCachedRootfsCurrent(t, rootfsPath)

	ip := NewImagePreparer(&PreparerConfig{RootfsDir: rootfsDir}).(*ImagePreparer)

	task := &types.Task{
		ID:          "secret-private",
		Annotations: make(map[string]string),
		Spec:        types.TaskSpec{Runtime: &types.Container{Image: "secrets-private:v1"}},
		Secrets:     []types.SecretRef{{ID: "s1", Name: "s", Data: []byte("x")}},
	}

	_ = ip.Prepare(context.Background(), task)

	private := filepath.Join(rootfsDir, task.ID+".ext4")
	assert.Equal(t, private, task.Annotations["rootfs"], "secrets must use a per-task rootfs")
	assert.Equal(t, "true", task.Annotations["rootfs_ephemeral"])
}

// TestCleanup_ReapsAbandonedStagingFiles proves a copy interrupted by a crash does
// not leave its staging file behind forever, while a fresh one — a copy still in
// progress — is left alone.
func TestCleanup_ReapsAbandonedStagingFiles(t *testing.T) {
	rootfsDir := t.TempDir()
	ip := NewImagePreparer(&PreparerConfig{RootfsDir: rootfsDir}).(*ImagePreparer)

	abandoned := filepath.Join(rootfsDir, "task-1.ext4"+rootfsStagingMarker+"123-456")
	require.NoError(t, os.WriteFile(abandoned, []byte("half a copy"), 0644))
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(abandoned, old, old))

	inFlight := filepath.Join(rootfsDir, "task-2.ext4"+rootfsStagingMarker+"789-012")
	require.NoError(t, os.WriteFile(inFlight, []byte("a copy in progress"), 0644))

	removed, freed, err := ip.Cleanup(context.Background(), 7)
	require.NoError(t, err)

	assert.Equal(t, 1, removed, "only the abandoned staging file is reaped")
	assert.EqualValues(t, len("half a copy"), freed)
	assert.NoFileExists(t, abandoned)
	assert.FileExists(t, inFlight, "a staging file younger than the threshold is left alone")
}

// perTaskRootfs is where a task's own writable copy of the image rootfs lives.
func perTaskRootfs(rootfsDir, taskID string) string {
	return filepath.Join(rootfsDir, taskID+".ext4")
}

// TestPrepare_NoSecretsStillGetsOwnRootfs is the #69 invariant: a task with no
// mounts, secrets or configs still boots from its own writable copy, so nothing a
// guest writes can reach the file every later task on the node shares.
func TestPrepare_NoSecretsStillGetsOwnRootfs(t *testing.T) {
	rootfsDir := t.TempDir()
	imageID := generateImageID("plain:v1")
	cache := filepath.Join(rootfsDir, imageID+".ext4")
	makeCachedRootfs(t, cache)
	markCachedRootfsCurrent(t, cache)
	before, err := os.ReadFile(cache)
	require.NoError(t, err)

	ip := NewImagePreparer(&PreparerConfig{RootfsDir: rootfsDir}).(*ImagePreparer)

	task := &types.Task{
		ID:          "plain-task",
		Annotations: make(map[string]string),
		Spec:        types.TaskSpec{Runtime: &types.Container{Image: "plain:v1"}},
	}

	_ = ip.Prepare(context.Background(), task)

	want := perTaskRootfs(rootfsDir, task.ID)
	assert.Equal(t, want, task.Annotations["rootfs"], "a plain task must not boot from the shared cache")
	assert.NotEqual(t, cache, task.Annotations["rootfs"])
	assert.Equal(t, "true", task.Annotations[types.AnnotationRootfsEphemeral],
		"teardown must delete the copy, never the cache")
	assert.FileExists(t, want, "the task's own copy must exist")

	after, err := os.ReadFile(cache)
	require.NoError(t, err)
	assert.Equal(t, before, after, "preparing a task must not modify the shared cache")

	// The copy is staged and renamed into place, so nothing is left behind.
	staging, err := filepath.Glob(filepath.Join(rootfsDir, "*"+rootfsStagingMarker+"*"))
	require.NoError(t, err)
	assert.Empty(t, staging, "a successful copy must not leave a staging file")
}
