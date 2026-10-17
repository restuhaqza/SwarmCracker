package swarmkit

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vmSizeFixture is the canonical sizing matrix. The identical fixture lives in
// pkg/translator/translator_size_test.go and must pin the same answers, so the
// two translators cannot drift apart.
type vmSizeFixtureCase struct {
	name         string
	limits       *types.Resources
	reservations *types.Resources
	wantVCPUs    int
	wantMemMB    int
}

var vmSizeFixture = []vmSizeFixtureCase{
	{
		name:      "neither declared uses the 1 vCPU / 512 MiB defaults",
		wantVCPUs: 1,
		wantMemMB: 512,
	},
	{
		name:      "limits alone size the VM",
		limits:    &types.Resources{NanoCPUs: 2e9, MemoryBytes: 1024 * 1024 * 1024},
		wantVCPUs: 2,
		wantMemMB: 1024,
	},
	{
		name:         "reservations alone size the VM",
		reservations: &types.Resources{NanoCPUs: 4e9, MemoryBytes: 2048 * 1024 * 1024},
		wantVCPUs:    4,
		wantMemMB:    2048,
	},
	{
		name:         "limits win when both are set",
		limits:       &types.Resources{NanoCPUs: 2e9, MemoryBytes: 1024 * 1024 * 1024},
		reservations: &types.Resources{NanoCPUs: 8e9, MemoryBytes: 4096 * 1024 * 1024},
		wantVCPUs:    2,
		wantMemMB:    1024,
	},
	{
		name:      "an explicit 256 MiB limit is honoured, not floored",
		limits:    &types.Resources{NanoCPUs: 1e9, MemoryBytes: 256 * 1024 * 1024},
		wantVCPUs: 1,
		wantMemMB: 256,
	},
	{
		name:         "an explicit 256 MiB reservation is honoured too",
		reservations: &types.Resources{NanoCPUs: 1e9, MemoryBytes: 256 * 1024 * 1024},
		wantVCPUs:    1,
		wantMemMB:    256,
	},
	{
		name:      "vCPUs come from the same source as memory",
		limits:    &types.Resources{NanoCPUs: 3e9, MemoryBytes: 3 * 1024 * 1024 * 1024},
		wantVCPUs: 3,
		wantMemMB: 3072,
	},
	{
		name:      "sub-vCPU nanoseconds keep 1 vCPU but memory is still sized",
		limits:    &types.Resources{NanoCPUs: 5e8, MemoryBytes: 256 * 1024 * 1024},
		wantVCPUs: 1,
		wantMemMB: 256,
	},
	{
		name:      "sub-MiB memory keeps the default",
		limits:    &types.Resources{MemoryBytes: 1024},
		wantVCPUs: 1,
		wantMemMB: 512,
	},
	{
		name:      "zero values leave the defaults in place",
		limits:    &types.Resources{},
		wantVCPUs: 1,
		wantMemMB: 512,
	},
	{
		name:      "negative values leave the defaults in place",
		limits:    &types.Resources{NanoCPUs: -2e9, MemoryBytes: -1024 * 1024 * 1024},
		wantVCPUs: 1,
		wantMemMB: 512,
	},
}

func TestResolveSize(t *testing.T) {
	for _, tc := range vmSizeFixture {
		t.Run(tc.name, func(t *testing.T) {
			res := &types.ResourceRequirements{Limits: tc.limits, Reservations: tc.reservations}

			vcpus, memoryMB := resolveSize(res)

			assert.Equal(t, tc.wantVCPUs, vcpus)
			assert.Equal(t, tc.wantMemMB, memoryMB)
		})
	}
}

// TestResolveSize_NilRequirements guards the nil arm: a task with no resource
// requirements at all keeps the historical 1 vCPU / 512 MiB behaviour.
func TestResolveSize_NilRequirements(t *testing.T) {
	vcpus, memoryMB := resolveSize(nil)

	assert.Equal(t, 1, vcpus)
	assert.Equal(t, 512, memoryMB)
}

// TestTranslate_ResourcePrecedence pins the fixture end to end: the
// machine-config the daemon sends must carry the size resolveSize resolved.
func TestTranslate_ResourcePrecedence(t *testing.T) {
	for _, tc := range vmSizeFixture {
		t.Run(tc.name, func(t *testing.T) {
			translator, err := NewTaskTranslator("/test/kernel", "192.168.127.1/24")
			require.NoError(t, err)

			task := &types.Task{
				ID: "task-size",
				Spec: types.TaskSpec{
					Runtime: &types.Container{Image: "test:latest"},
					Resources: types.ResourceRequirements{
						Limits:       tc.limits,
						Reservations: tc.reservations,
					},
				},
			}

			result, err := translator.Translate(task)
			require.NoError(t, err)

			config, ok := result.(map[string]interface{})
			require.True(t, ok, "Translate must return map[string]interface{}")

			machineConfig, ok := config["machine-config"].(map[string]interface{})
			require.True(t, ok, "machine-config is required")

			assert.Equal(t, tc.wantVCPUs, machineConfig["vcpu_count"])
			assert.Equal(t, tc.wantMemMB, machineConfig["mem_size_mib"])
		})
	}
}
