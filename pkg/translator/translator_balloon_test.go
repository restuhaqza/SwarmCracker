package translator

import (
	"encoding/json"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newBalloonTask returns a task carrying an optional memory-balloon target and,
// when memoryMB is positive, a memory limit so the translator's own sizing is
// exercised.
func newBalloonTask(balloon string, memoryMB int) *types.Task {
	annotations := map[string]string{"rootfs": "/rootfs.ext4"}
	if balloon != "" {
		annotations[types.BalloonLabel] = balloon
	}

	task := &types.Task{
		ID: "task-balloon",
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

// TestTranslate_NoBalloonWithoutAnnotation pins the no-op property: a task that
// does not request a balloon must emit no "balloon" key at all, not a nil or
// zero-valued one, so the produced config is unchanged.
func TestTranslate_NoBalloonWithoutAnnotation(t *testing.T) {
	tt := NewTaskTranslator(&Config{KernelPath: "/test/kernel", DefaultVCPUs: 1, DefaultMemMB: 512})

	got, err := tt.Translate(newBalloonTask("", 0))
	require.NoError(t, err)

	configMap, ok := got.(map[string]interface{})
	require.True(t, ok, "Translate must return map[string]interface{}")

	_, present := configMap["balloon"]
	assert.False(t, present, "an unconfigured task must emit no balloon key")

	raw, err := json.Marshal(configMap)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "balloon")
}

// TestTranslate_BalloonConfiguration covers the emitted device and every target
// the translator must reject.
func TestTranslate_BalloonConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		balloon    string
		memoryMB   int
		wantErr    string
		wantAmount float64
	}{
		{
			name:       "valid target below the VM memory size",
			balloon:    "256",
			memoryMB:   512,
			wantAmount: 256,
		},
		{
			name:       "valid target with surrounding whitespace",
			balloon:    " 128 ",
			memoryMB:   512,
			wantAmount: 128,
		},
		{
			name:     "non-integer value",
			balloon:  "256m",
			memoryMB: 512,
			wantErr:  `invalid memory balloon target "256m"`,
		},
		{
			name:     "zero",
			balloon:  "0",
			memoryMB: 512,
			wantErr:  `invalid memory balloon target "0"`,
		},
		{
			name:     "negative",
			balloon:  "-1",
			memoryMB: 512,
			wantErr:  `invalid memory balloon target "-1"`,
		},
		{
			name:     "equal to the VM memory size",
			balloon:  "512",
			memoryMB: 512,
			wantErr:  "must be less than the VM memory size 512 MiB",
		},
		{
			name:     "larger than the VM memory size",
			balloon:  "1024",
			memoryMB: 512,
			wantErr:  "must be less than the VM memory size 512 MiB",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := NewTaskTranslator(&Config{KernelPath: "/test/kernel", DefaultVCPUs: 1, DefaultMemMB: 512})

			got, err := tt.Translate(newBalloonTask(tc.balloon, tc.memoryMB))

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			configMap, ok := got.(map[string]interface{})
			require.True(t, ok, "Translate must return map[string]interface{}")

			device, ok := configMap["balloon"].(map[string]interface{})
			require.True(t, ok, "a configured task must emit a balloon section")
			// configToMap round-trips through JSON, so numbers arrive as float64.
			assert.Equal(t, tc.wantAmount, device["amount_mib"])
			assert.Equal(t, true, device["deflate_on_oom"])
			assert.Equal(t, float64(0), device["stats_polling_interval_s"])
			assert.Len(t, device, 3, "the balloon section must carry exactly the three API keys")
		})
	}
}
