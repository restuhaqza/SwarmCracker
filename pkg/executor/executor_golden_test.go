package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/restuhaqza/swarmcracker/test/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newGoldenExecutor(t *testing.T, imgPrep *mocks.MockImagePreparer, netMgr *mocks.MockNetworkManager) *FirecrackerExecutor {
	t.Helper()
	exec, err := NewFirecrackerExecutor(
		&Config{RootfsDir: t.TempDir()},
		mocks.NewMockVMMManager(),
		mocks.NewMockTaskTranslator(),
		imgPrep,
		netMgr,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = exec.Close() })
	return exec
}

func TestPrepare_PrebuiltRootfs_SkipsImagePreparer(t *testing.T) {
	rootfs := filepath.Join(t.TempDir(), "golden.ext4")
	require.NoError(t, os.WriteFile(rootfs, []byte("img"), 0644))

	imgPrep := mocks.NewMockImagePreparer()
	netMgr := mocks.NewMockNetworkManager()
	exec := newGoldenExecutor(t, imgPrep, netMgr)

	task := &types.Task{
		ID:   "golden-task",
		Spec: types.TaskSpec{Runtime: &types.Container{Image: "golden:ubuntu-24.04-docker"}},
		Annotations: map[string]string{
			types.AnnotationRootfs:         rootfs,
			types.AnnotationPrebuiltRootfs: "true",
			types.AnnotationGolden:         "ubuntu-24.04-docker@1.0.0",
		},
	}

	require.NoError(t, exec.Prepare(context.Background(), task))
	assert.False(t, imgPrep.PrepareCalled, "image preparer must be skipped for a golden rootfs")
	assert.True(t, netMgr.PrepareCalled, "network preparation must still run")
}

func TestPrepare_PrebuiltRootfs_MissingArtifact(t *testing.T) {
	imgPrep := mocks.NewMockImagePreparer()
	exec := newGoldenExecutor(t, imgPrep, mocks.NewMockNetworkManager())

	task := &types.Task{
		ID:   "golden-task",
		Spec: types.TaskSpec{Runtime: &types.Container{Image: "golden:ubuntu-24.04-docker"}},
		Annotations: map[string]string{
			types.AnnotationRootfs:         filepath.Join(t.TempDir(), "does-not-exist.ext4"),
			types.AnnotationPrebuiltRootfs: "true",
		},
	}

	err := exec.Prepare(context.Background(), task)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not accessible")
	assert.False(t, imgPrep.PrepareCalled)
}

func TestPrepare_PrebuiltRootfs_MissingAnnotation(t *testing.T) {
	imgPrep := mocks.NewMockImagePreparer()
	exec := newGoldenExecutor(t, imgPrep, mocks.NewMockNetworkManager())

	task := &types.Task{
		ID:          "golden-task",
		Spec:        types.TaskSpec{Runtime: &types.Container{Image: "golden:x"}},
		Annotations: map[string]string{types.AnnotationPrebuiltRootfs: "true"},
	}

	err := exec.Prepare(context.Background(), task)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rootfs")
	assert.False(t, imgPrep.PrepareCalled)
}
