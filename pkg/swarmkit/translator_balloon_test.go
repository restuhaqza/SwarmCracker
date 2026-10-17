package swarmkit

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newBalloonTask returns a task carrying an optional memory-balloon target and,
// when memoryMB is positive, a memory reservation so the translator's own
// sizing is exercised.
func newBalloonTask(balloon string, memoryMB int) *types.Task {
	annotations := map[string]string{"rootfs": "/rootfs.ext4"}
	if balloon != "" {
		annotations[types.BalloonLabel] = balloon
	}

	task := &types.Task{
		ID:          "task-balloon",
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

// TestTranslate_NoBalloonWithoutAnnotation pins the no-op property: a task that
// does not request a balloon must emit no "balloon" key at all, not a nil or
// zero-valued one, so the produced config is unchanged.
func TestTranslate_NoBalloonWithoutAnnotation(t *testing.T) {
	translator, err := NewTaskTranslator("/test/kernel", "192.168.127.1/24")
	require.NoError(t, err)

	result, err := translator.Translate(newBalloonTask("", 0))
	require.NoError(t, err)

	config, ok := result.(map[string]interface{})
	require.True(t, ok, "Translate must return map[string]interface{}")

	_, present := config["balloon"]
	assert.False(t, present, "an unconfigured task must emit no balloon key")
}

// TestTranslate_BalloonConfiguration covers the emitted device and every target
// the translator must reject.
func TestTranslate_BalloonConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		balloon    string
		memoryMB   int
		wantErr    string
		wantAmount int64
	}{
		{
			name:       "valid target below the VM memory size",
			balloon:    "256",
			memoryMB:   512,
			wantAmount: 256,
		},
		{
			name:       "valid target below the default memory size",
			balloon:    "128",
			memoryMB:   0,
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
			translator, err := NewTaskTranslator("/test/kernel", "192.168.127.1/24")
			require.NoError(t, err)

			result, err := translator.Translate(newBalloonTask(tc.balloon, tc.memoryMB))

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			config, ok := result.(map[string]interface{})
			require.True(t, ok, "Translate must return map[string]interface{}")

			device, ok := config["balloon"].(map[string]interface{})
			require.True(t, ok, "a configured task must emit a balloon section")
			assert.Equal(t, tc.wantAmount, device["amount_mib"])
			assert.Equal(t, true, device["deflate_on_oom"])
			assert.Equal(t, 0, device["stats_polling_interval_s"])
			assert.Len(t, device, 3, "the balloon section must carry exactly the three API keys")
		})
	}
}
