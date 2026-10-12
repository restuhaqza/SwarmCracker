package translator

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTranslate_PopulatesVsock verifies the CLI translator emits a vsock
// section so the lifecycle manager can register the device before boot.
func TestTranslate_PopulatesVsock(t *testing.T) {
	tt := NewTaskTranslator(&Config{
		KernelPath:   "/test/kernel",
		DefaultVCPUs: 1,
		DefaultMemMB: 512,
	})

	task := &types.Task{
		ID: "task-vsock",
		Spec: types.TaskSpec{
			Runtime: &types.Container{Image: "nginx:latest"},
		},
		Annotations: map[string]string{"rootfs": "/rootfs.ext4"},
	}

	got, err := tt.Translate(task)
	require.NoError(t, err)

	configMap, ok := got.(map[string]interface{})
	require.True(t, ok, "Translate must return map[string]interface{}")

	vsock, ok := configMap["vsock"].(map[string]interface{})
	require.True(t, ok, "translated config must carry a vsock section")
	// configToMap round-trips through JSON, so numbers arrive as float64.
	assert.Equal(t, float64(agent.GuestCID), vsock["guest_cid"])
	assert.Equal(t, "vsock0", vsock["vsock_id"])
}
