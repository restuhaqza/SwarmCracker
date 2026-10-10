package image

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

func TestPrepare_NoSecretsUsesSharedCache(t *testing.T) {
	rootfsDir := t.TempDir()
	imageID := generateImageID("plain:v1")
	rootfsPath := filepath.Join(rootfsDir, imageID+".ext4")
	require.NoError(t, os.WriteFile(rootfsPath, []byte("rootfs"), 0644))

	ip := NewImagePreparer(&PreparerConfig{RootfsDir: rootfsDir}).(*ImagePreparer)

	task := &types.Task{
		ID:          "plain-task",
		Annotations: make(map[string]string),
		Spec:        types.TaskSpec{Runtime: &types.Container{Image: "plain:v1"}},
	}

	_ = ip.Prepare(context.Background(), task)

	assert.Equal(t, rootfsPath, task.Annotations["rootfs"], "no secrets/mounts must reuse the shared cache")
}
