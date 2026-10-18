package swarmkit

import (
	"encoding/json"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHugepagesTask returns a daemon-shaped task carrying an optional hugepages
// request and, when memoryMB is positive, a memory reservation so the
// translator's own sizing (the value the machine-config boots) is exercised.
func newHugepagesTask(hugepages string, memoryMB int) *types.Task {
	annotations := map[string]string{"rootfs": "/rootfs.ext4"}
	if hugepages != "" {
		annotations[types.HugepagesLabel] = hugepages
	}

	task := &types.Task{
		ID:          "task-translator-hugepages",
		Annotations: annotations,
		Spec: types.TaskSpec{
			Runtime: &types.Container{Image: "nginx:latest"},
		},
	}
	if memoryMB > 0 {
		task.Spec.Resources.Reservations = &types.Resources{
			MemoryBytes: int64(memoryMB) * 1024 * 1024,
		}
	}
	return task
}

// TestTranslateHugepages_NoRequestKeepsMachineConfigIdentical pins the
// no-behaviour-change property on the path the daemon actually uses
// (pkg/swarmkit, not pkg/translator): a task that asks for no hugepages must
// produce exactly the machine-config it produced before hugepages existed, with
// no huge_pages key at all.
func TestTranslateHugepages_NoRequestKeepsMachineConfigIdentical(t *testing.T) {
	translator, err := NewTaskTranslator("/test/kernel", "192.168.127.1/24")
	require.NoError(t, err)

	result, err := translator.Translate(newHugepagesTask("", 512))
	require.NoError(t, err)

	config, ok := result.(map[string]interface{})
	require.True(t, ok, "Translate must return map[string]interface{}")

	machine, ok := config["machine-config"].(map[string]interface{})
	require.True(t, ok, "Translate must emit a machine-config section")

	assert.Equal(t, map[string]interface{}{
		"vcpu_count":   1,
		"mem_size_mib": 512,
		"smt":          false,
	}, machine, "an unconfigured task must emit no huge_pages key")

	raw, err := json.Marshal(machine)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "huge_pages")
}

// TestTranslateHugepages_RequestReachesMachineConfig pins the daemon path fix:
// a hugepages request must come out of pkg/swarmkit's translator as the
// Firecracker machine-config value, because the daemon never calls
// pkg/translator. Accepting spellings and strict rejections are covered too.
func TestTranslateHugepages_RequestReachesMachineConfig(t *testing.T) {
	tests := []struct {
		name      string
		hugepages string
		memoryMB  int
		wantValue string
		wantErr   string
	}{
		{name: "true", hugepages: "true", memoryMB: 512, wantValue: "2M"},
		{name: "2M synonym", hugepages: "2M", memoryMB: 512, wantValue: "2M"},
		{name: "padded and uppercase", hugepages: " TRUE ", memoryMB: 512, wantValue: "2M"},
		{name: "default memory size", hugepages: "true", memoryMB: 0, wantValue: "2M"},
		{name: "false", hugepages: "false", memoryMB: 512},
		{name: "none", hugepages: "none", memoryMB: 512},
		{
			name:      "malformed value",
			hugepages: "yes",
			memoryMB:  512,
			wantErr:   `invalid hugepages value "yes"`,
		},
		{
			name:      "odd memory size",
			hugepages: "true",
			memoryMB:  513,
			wantErr:   "invalid hugepage-backed memory size 513 MiB",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translator, err := NewTaskTranslator("/test/kernel", "192.168.127.1/24")
			require.NoError(t, err)

			result, err := translator.Translate(newHugepagesTask(tc.hugepages, tc.memoryMB))

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			config, ok := result.(map[string]interface{})
			require.True(t, ok, "Translate must return map[string]interface{}")

			machine, ok := config["machine-config"].(map[string]interface{})
			require.True(t, ok, "Translate must emit a machine-config section")

			raw, present := machine["huge_pages"]
			if tc.wantValue == "" {
				assert.False(t, present, "a task without a request must emit no huge_pages key")
				return
			}
			require.True(t, present, "a hugepage request must reach the machine-config the daemon pushes")
			assert.Equal(t, tc.wantValue, raw)
		})
	}
}
