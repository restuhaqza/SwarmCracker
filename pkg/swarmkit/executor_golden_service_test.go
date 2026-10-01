package swarmkit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/golden"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/restuhaqza/swarmcracker/test/mocks"
)

// recordingImagePreparer records whether the OCI image path was taken.
type recordingImagePreparer struct {
	prepared bool
}

func (p *recordingImagePreparer) Prepare(_ context.Context, task *types.Task) error {
	p.prepared = true
	if task.Annotations == nil {
		task.Annotations = make(map[string]string)
	}
	task.Annotations["rootfs"] = "/mock/rootfs.ext4"
	return nil
}

func (p *recordingImagePreparer) Cleanup(context.Context, int) (int, int64, error) { return 0, 0, nil }

// writeGoldenStore creates a golden store directory holding one artifact and
// its metadata sidecar, and returns the store dir and the rootfs path.
func writeGoldenStore(t *testing.T, name, version, kernelProfile string, bootArgs []string) (dir, rootfsPath string) {
	t.Helper()
	dir = t.TempDir()
	rootfsPath = filepath.Join(dir, "golden-"+name+"-"+version+".ext4")
	require.NoError(t, os.WriteFile(rootfsPath, []byte("rootfs"), 0o644))

	md := golden.ArtifactMetadata{
		APIVersion:    "swarmcracker/v1",
		Kind:          golden.KindGoldenImage,
		Name:          name,
		Version:       version,
		KernelProfile: kernelProfile,
		Init:          golden.Init{BootArgs: bootArgs},
		Artifact:      filepath.Base(rootfsPath),
	}
	data, err := json.Marshal(md)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "golden-"+name+"-"+version+".json"), data, 0o644))
	return dir, rootfsPath
}

func goldenServiceTask(id, ref string) *api.Task {
	labels := map[string]string{}
	if ref != "" {
		labels[types.GoldenLabel] = ref
	}
	image := "alpine:latest"
	if ref != "" {
		image = "golden:" + ref
	}
	return &api.Task{
		ID:        id,
		ServiceID: "svc-" + id,
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{Image: image},
			},
		},
		ServiceAnnotations: api.Annotations{Labels: labels},
	}
}

func newGoldenController(task *api.Task, goldenDir, rootfsDir string, imgPrep types.ImagePreparer) *Controller {
	return &Controller{
		task:       task,
		config:     &Config{GoldenDir: goldenDir, RootfsDir: rootfsDir},
		imagePrep:  imgPrep,
		networkMgr: mocks.NewMockNetworkManager(),
		logger:     log.With().Str("task_id", task.ID).Logger(),
	}
}

func TestControllerPrepare_GoldenService_SkipsImagePreparation(t *testing.T) {
	dir, template := writeGoldenStore(t, "ubuntu-24.04-docker", "1.0.0", "guest-runtime-6.1", []string{"quiet"})
	rootfsDir := t.TempDir()

	imgPrep := &recordingImagePreparer{}
	ctrl := newGoldenController(
		goldenServiceTask("task-golden", "ubuntu-24.04-docker@1.0.0"), dir, rootfsDir, imgPrep,
	)

	require.NoError(t, ctrl.Prepare(context.Background()))

	assert.False(t, imgPrep.prepared, "a golden service must skip image preparation")
	require.NotNil(t, ctrl.internalTask)
	a := ctrl.internalTask.Annotations

	// The VM writes to its own copy, never the shared template.
	wantRootfs := filepath.Join(rootfsDir, "task-golden.ext4")
	assert.Equal(t, wantRootfs, a[types.AnnotationRootfs])
	copied, err := os.ReadFile(wantRootfs)
	require.NoError(t, err)
	assert.Equal(t, "rootfs", string(copied))
	untouched, err := os.ReadFile(template)
	require.NoError(t, err)
	assert.Equal(t, "rootfs", string(untouched), "the golden template must stay intact")

	assert.Equal(t, "true", a[types.AnnotationPrebuiltRootfs])
	assert.Equal(t, "ubuntu-24.04-docker@1.0.0", a[types.AnnotationGolden])
	assert.Equal(t, "guest-runtime-6.1", a[types.AnnotationKernelProfile])
	assert.Equal(t, "quiet", a[types.AnnotationBootArgs])
}

func TestControllerPrepare_GoldenService_UnknownImageFails(t *testing.T) {
	imgPrep := &recordingImagePreparer{}
	ctrl := newGoldenController(
		goldenServiceTask("task-missing", "does-not-exist@9.9.9"), t.TempDir(), t.TempDir(), imgPrep,
	)

	err := ctrl.Prepare(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "golden image")
	assert.False(t, ctrl.prepared, "a failed golden resolution must not mark the task prepared")
	assert.False(t, imgPrep.prepared, "a failed golden resolution must not fall back to OCI prep")
}

func TestControllerPrepare_GoldenService_MaterializeFailureFails(t *testing.T) {
	dir, _ := writeGoldenStore(t, "ubuntu-24.04-docker", "1.0.0", "", nil)

	// A regular file where the rootfs dir should be makes materialization fail.
	rootfsBlocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(rootfsBlocker, []byte("x"), 0o644))

	ctrl := newGoldenController(
		goldenServiceTask("task-blocked", "ubuntu-24.04-docker@1.0.0"), dir, rootfsBlocker, &recordingImagePreparer{},
	)

	err := ctrl.Prepare(context.Background())
	require.Error(t, err)
	assert.False(t, ctrl.prepared, "a failed materialization must not mark the task prepared")
}

func TestControllerPrepare_NoGoldenLabelPrepsImage(t *testing.T) {
	imgPrep := &recordingImagePreparer{}
	ctrl := newGoldenController(goldenServiceTask("task-oci", ""), t.TempDir(), t.TempDir(), imgPrep)

	require.NoError(t, ctrl.Prepare(context.Background()))
	assert.True(t, imgPrep.prepared, "a plain OCI service must still prepare an image")
}
