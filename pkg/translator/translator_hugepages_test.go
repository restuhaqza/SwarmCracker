package translator

import (
	"encoding/json"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHugepagesTask returns a task carrying an optional hugepages annotation and,
// when memoryMB is positive, a memory limit so the translator's own sizing is
// exercised.
func newHugepagesTask(hugepages string, memoryMB int) *types.Task {
	annotations := map[string]string{"rootfs": "/rootfs.ext4"}
	if hugepages != "" {
		annotations[types.HugepagesLabel] = hugepages
	}

	task := &types.Task{
		ID: "task-hugepages",
		Spec: types.TaskSpec{
			Runtime: &types.Container{Image: "nginx:latest"},
		},
		Annotations: annotations,
	}
	if memoryMB > 0 {
		task.Spec.Resources.Limits = &types.Resources{
			MemoryBytes: int64(memoryMB) * 1024 * 1024,
		}
	}
	return task
}

// TestTranslate_NoHugepagesWithoutAnnotation pins the no-op property: an
// unconfigured task's machine-config must not gain a huge_pages key, so the
// serialised config stays byte-identical to before hugepages existed.
func TestTranslate_NoHugepagesWithoutAnnotation(t *testing.T) {
	tt := NewTaskTranslator(&Config{KernelPath: "/test/kernel", DefaultVCPUs: 1, DefaultMemMB: 512})

	got, err := tt.Translate(newHugepagesTask("", 0))
	require.NoError(t, err)

	configMap, ok := got.(map[string]interface{})
	require.True(t, ok, "Translate must return map[string]interface{}")

	machine, ok := configMap["machine-config"].(map[string]interface{})
	require.True(t, ok)
	_, present := machine["huge_pages"]
	assert.False(t, present, "an unconfigured task must emit no huge_pages key")

	raw, err := json.Marshal(configMap)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "huge_pages")
}

// TestTranslate_HugepagesConfiguration covers the emitted value and every
// request the translator must reject before Firecracker sees it.
func TestTranslate_HugepagesConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		hugepages string
		memoryMB  int
		wantErr   string
	}{
		{
			name:      "true requests 2M pages",
			hugepages: "true",
			memoryMB:  512,
		},
		{
			name:      "2M is accepted as a synonym",
			hugepages: "2M",
			memoryMB:  1024,
		},
		{
			name:      "false leaves ordinary memory",
			hugepages: "false",
			memoryMB:  512,
		},
		{
			name:      "a malformed value is a hard error",
			hugepages: "yes-please",
			memoryMB:  512,
			wantErr:   `invalid hugepages value "yes-please"`,
		},
		{
			name:      "an odd memory size cannot be mapped with 2M pages",
			hugepages: "true",
			memoryMB:  513,
			wantErr:   "invalid hugepage-backed memory size 513 MiB",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := NewTaskTranslator(&Config{KernelPath: "/test/kernel", DefaultVCPUs: 1, DefaultMemMB: 512})

			got, err := tt.Translate(newHugepagesTask(tc.hugepages, tc.memoryMB))

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			configMap, ok := got.(map[string]interface{})
			require.True(t, ok, "Translate must return map[string]interface{}")

			machine, ok := configMap["machine-config"].(map[string]interface{})
			require.True(t, ok)

			want, _ := types.ParseHugepages(tc.hugepages)
			if want {
				assert.Equal(t, "2M", machine["huge_pages"])
			} else {
				_, present := machine["huge_pages"]
				assert.False(t, present, "a declining value must not add the key")
			}
		})
	}
}
