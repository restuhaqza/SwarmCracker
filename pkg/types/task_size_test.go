package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveVMSize(t *testing.T) {
	tests := []struct {
		name            string
		res             *ResourceRequirements
		defaultVCPUs    int
		defaultMemoryMB int
		wantVCPUs       int
		wantMemoryMB    int
	}{
		{
			name:            "nil requirements keep the defaults",
			res:             nil,
			defaultVCPUs:    1,
			defaultMemoryMB: 512,
			wantVCPUs:       1,
			wantMemoryMB:    512,
		},
		{
			name:            "empty requirements keep the defaults",
			res:             &ResourceRequirements{},
			defaultVCPUs:    1,
			defaultMemoryMB: 512,
			wantVCPUs:       1,
			wantMemoryMB:    512,
		},
		{
			name:            "limits are used when declared",
			res:             &ResourceRequirements{Limits: &Resources{NanoCPUs: 2e9, MemoryBytes: 1024 * 1024 * 1024}},
			defaultVCPUs:    1,
			defaultMemoryMB: 512,
			wantVCPUs:       2,
			wantMemoryMB:    1024,
		},
		{
			name:            "reservations are used when no limits are declared",
			res:             &ResourceRequirements{Reservations: &Resources{NanoCPUs: 4e9, MemoryBytes: 2048 * 1024 * 1024}},
			defaultVCPUs:    1,
			defaultMemoryMB: 512,
			wantVCPUs:       4,
			wantMemoryMB:    2048,
		},
		{
			name:            "limits win over reservations",
			res:             &ResourceRequirements{Limits: &Resources{NanoCPUs: 2e9, MemoryBytes: 1024 * 1024 * 1024}, Reservations: &Resources{NanoCPUs: 8e9, MemoryBytes: 4096 * 1024 * 1024}},
			defaultVCPUs:    1,
			defaultMemoryMB: 512,
			wantVCPUs:       2,
			wantMemoryMB:    1024,
		},
		{
			name:            "an explicit 256 MiB is honoured without a floor",
			res:             &ResourceRequirements{Limits: &Resources{NanoCPUs: 1e9, MemoryBytes: 256 * 1024 * 1024}},
			defaultVCPUs:    1,
			defaultMemoryMB: 512,
			wantVCPUs:       1,
			wantMemoryMB:    256,
		},
		{
			name:            "sub-vCPU values floor to one vCPU",
			res:             &ResourceRequirements{Limits: &Resources{NanoCPUs: 5e8, MemoryBytes: 1024 * 1024 * 1024}},
			defaultVCPUs:    1,
			defaultMemoryMB: 512,
			wantVCPUs:       1,
			wantMemoryMB:    1024,
		},
		{
			name:            "sub-MiB memory keeps the default",
			res:             &ResourceRequirements{Limits: &Resources{MemoryBytes: 1024}},
			defaultVCPUs:    1,
			defaultMemoryMB: 512,
			wantVCPUs:       1,
			wantMemoryMB:    512,
		},
		{
			name:            "negative values keep the defaults",
			res:             &ResourceRequirements{Limits: &Resources{NanoCPUs: -2e9, MemoryBytes: -1024 * 1024 * 1024}},
			defaultVCPUs:    1,
			defaultMemoryMB: 512,
			wantVCPUs:       1,
			wantMemoryMB:    512,
		},
		{
			name:            "the caller's defaults are respected",
			res:             &ResourceRequirements{},
			defaultVCPUs:    2,
			defaultMemoryMB: 2048,
			wantVCPUs:       2,
			wantMemoryMB:    2048,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vcpus, memoryMB := ResolveVMSize(tt.res, tt.defaultVCPUs, tt.defaultMemoryMB)

			assert.Equal(t, tt.wantVCPUs, vcpus)
			assert.Equal(t, tt.wantMemoryMB, memoryMB)
		})
	}
}
