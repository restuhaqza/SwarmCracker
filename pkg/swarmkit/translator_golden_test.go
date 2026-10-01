package swarmkit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// goldenTask builds a prebuilt-golden task, optionally declaring a kernel profile.
func goldenTask(profile string) *types.Task {
	annotations := map[string]string{
		types.AnnotationRootfs:         "/var/lib/firecracker/golden/golden-ubuntu-24.04-docker-1.0.0.ext4",
		types.AnnotationPrebuiltRootfs: "true",
		types.AnnotationGolden:         "ubuntu-24.04-docker@1.0.0",
	}
	if profile != "" {
		annotations[types.AnnotationKernelProfile] = profile
	}
	return &types.Task{
		ID:          "golden-task",
		Spec:        types.TaskSpec{Runtime: &types.Container{Image: "golden:ubuntu-24.04-docker"}},
		Annotations: annotations,
		Networks: []types.NetworkAttachment{
			{Addresses: []string{"192.168.127.5/24"}},
		},
	}
}

func bootSource(t *testing.T, out interface{}) map[string]interface{} {
	t.Helper()
	m, ok := out.(map[string]interface{})
	require.True(t, ok)
	bs, ok := m["boot-source"].(map[string]interface{})
	require.True(t, ok)
	return bs
}

func TestTranslate_PrebuiltGolden_BootArgsAndKernel(t *testing.T) {
	kernel := filepath.Join(t.TempDir(), "vmlinux-runtime")
	require.NoError(t, os.WriteFile(kernel, []byte("kernel"), 0o644))

	tr, err := NewTaskTranslatorWithProfiles(
		"/usr/share/firecracker/vmlinux",
		"192.168.127.1/24",
		map[string]string{"guest-runtime-6.1": kernel},
	)
	require.NoError(t, err)

	task := goldenTask("guest-runtime-6.1")
	task.Annotations[types.AnnotationBootArgs] = "quiet splash"

	out, err := tr.Translate(task)
	require.NoError(t, err)

	bs := bootSource(t, out)
	assert.Equal(t, kernel, bs["kernel_image_path"], "kernel profile must override the default kernel")

	args, _ := bs["boot_args"].(string)
	assert.Contains(t, args, "init=/sbin/init", "golden guest boots its own init")
	assert.NotContains(t, args, "--", "golden VM must not append a container command")
	assert.Contains(t, args, "quiet splash", "recipe boot args must be appended")
	assert.Contains(t, args, "ip=192.168.127.5::192.168.127.1:255.255.255.0::eth0:off")

	m := out.(map[string]interface{})
	drives, ok := m["drives"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, drives, 1)
	assert.Equal(t, task.Annotations[types.AnnotationRootfs], drives[0]["path_on_host"])
}

func TestTranslate_UnknownKernelProfileFallsBack(t *testing.T) {
	tr, err := NewTaskTranslatorWithProfiles(
		"/usr/share/firecracker/vmlinux", "192.168.127.1/24", map[string]string{"known": "/x"},
	)
	require.NoError(t, err)

	bs := bootSource(t, mustTranslate(t, tr, goldenTask("mystery")))
	assert.Equal(t, "/usr/share/firecracker/vmlinux", bs["kernel_image_path"])
}

func TestTranslate_MappedKernelProfileMissingFileIsError(t *testing.T) {
	tr, err := NewTaskTranslatorWithProfiles(
		"/usr/share/firecracker/vmlinux", "192.168.127.1/24", map[string]string{"p": "/nope/vmlinux"},
	)
	require.NoError(t, err)

	_, err = tr.Translate(goldenTask("p"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not available")
}

func TestTranslate_NoProfileUsesDefaultKernel(t *testing.T) {
	tr, err := NewTaskTranslatorWithProfiles(
		"/usr/share/firecracker/vmlinux", "192.168.127.1/24", map[string]string{"p": "/x"},
	)
	require.NoError(t, err)

	bs := bootSource(t, mustTranslate(t, tr, goldenTask("")))
	assert.Equal(t, "/usr/share/firecracker/vmlinux", bs["kernel_image_path"])
}

// mustTranslate fails the test if Translate returns an error.
func mustTranslate(t *testing.T, tr types.TaskTranslator, task *types.Task) interface{} {
	t.Helper()
	out, err := tr.Translate(task)
	require.NoError(t, err)
	return out
}
