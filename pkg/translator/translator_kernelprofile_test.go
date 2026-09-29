package translator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goldenTaskWithProfile builds a prebuilt-golden task, optionally declaring a
// kernel profile annotation.
func goldenTaskWithProfile(profile string) *types.Task {
	task := &types.Task{
		ID:   "golden-task",
		Spec: types.TaskSpec{Runtime: &types.Container{Image: "golden:ubuntu-24.04-docker"}},
		Annotations: map[string]string{
			types.AnnotationRootfs:         "/var/lib/firecracker/golden/golden-ubuntu-24.04-docker-1.0.0.ext4",
			types.AnnotationPrebuiltRootfs: "true",
			types.AnnotationGolden:         "ubuntu-24.04-docker@1.0.0",
		},
		Networks: []types.NetworkAttachment{
			{Network: types.Network{ID: "net-1"}, Addresses: []string{"192.168.127.50/24"}},
		},
	}
	if profile != "" {
		task.Annotations[types.AnnotationKernelProfile] = profile
	}
	return task
}

func TestResolveKernelPath(t *testing.T) {
	exists := filepath.Join(t.TempDir(), "vmlinux-runtime")
	require.NoError(t, os.WriteFile(exists, []byte("kernel"), 0o644))
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	tr := NewTaskTranslator(&Config{
		KernelPath: "/default/vmlinux",
		KernelProfiles: map[string]string{
			"guest-runtime-6.1": exists,
			"broken":            missing,
		},
	})

	tests := []struct {
		name    string
		profile string
		want    string
		wantErr bool
	}{
		{name: "no profile uses default", profile: "", want: "/default/vmlinux"},
		{name: "mapped profile uses mapped path", profile: "guest-runtime-6.1", want: exists},
		{name: "unknown profile falls back to default", profile: "unknown-9.9", want: "/default/vmlinux"},
		{name: "mapped profile with missing file errors", profile: "broken", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tr.resolveKernelPath(goldenTaskWithProfile(tt.profile))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveKernelPath_NilTask(t *testing.T) {
	tr := NewTaskTranslator(&Config{KernelPath: "/default/vmlinux"})
	got, err := tr.resolveKernelPath(nil)
	require.NoError(t, err)
	assert.Equal(t, "/default/vmlinux", got)
}

func TestResolveKernelPath_NoProfilesConfigured(t *testing.T) {
	tr := NewTaskTranslator(&Config{KernelPath: "/default/vmlinux"})
	got, err := tr.resolveKernelPath(goldenTaskWithProfile("guest-runtime-6.1"))
	require.NoError(t, err)
	assert.Equal(t, "/default/vmlinux", got, "an unconfigured profile must not fail the task")
}

func TestTranslate_UsesKernelProfile(t *testing.T) {
	kernel := filepath.Join(t.TempDir(), "vmlinux-runtime")
	require.NoError(t, os.WriteFile(kernel, []byte("kernel"), 0o644))

	tr := NewTaskTranslator(&Config{
		KernelPath:     "/default/vmlinux",
		KernelProfiles: map[string]string{"guest-runtime-6.1": kernel},
	})

	got, err := tr.Translate(goldenTaskWithProfile("guest-runtime-6.1"))
	require.NoError(t, err)

	configMap, ok := got.(map[string]interface{})
	require.True(t, ok, "result should be a map")

	raw, err := json.Marshal(configMap)
	require.NoError(t, err)

	var cfg VMMConfig
	require.NoError(t, json.Unmarshal(raw, &cfg))

	assert.Equal(t, kernel, cfg.BootSource.KernelImagePath)
	assert.Contains(t, cfg.BootSource.BootArgs, "init=/sbin/init")
}

func TestTranslate_UnknownProfileFallsBackToDefault(t *testing.T) {
	tr := NewTaskTranslator(&Config{
		KernelPath:     "/default/vmlinux",
		KernelProfiles: map[string]string{"guest-runtime-6.1": "/some/where"},
	})

	got, err := tr.Translate(goldenTaskWithProfile("newer-profile"))
	require.NoError(t, err)

	configMap, ok := got.(map[string]interface{})
	require.True(t, ok)
	raw, err := json.Marshal(configMap)
	require.NoError(t, err)

	var cfg VMMConfig
	require.NoError(t, json.Unmarshal(raw, &cfg))
	assert.Equal(t, "/default/vmlinux", cfg.BootSource.KernelImagePath)
}
