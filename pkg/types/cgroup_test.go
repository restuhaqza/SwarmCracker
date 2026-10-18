package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCpusetAnnotationConstants pins the label/annotation key: the service label
// (what an operator writes) and the task annotation (what the VMM paths read)
// must stay the same key, exactly like the balloon.
func TestCpusetAnnotationConstants(t *testing.T) {
	assert.Equal(t, "swarmcracker.cpuset", CpusetLabel)
	assert.Equal(t, CpusetLabel, AnnotationCpuset)
}

func TestResolveCgroupLimits(t *testing.T) {
	const mib = int64(1024 * 1024)

	tests := []struct {
		name    string
		task    *Task
		want    CgroupLimits
		wantErr string
	}{
		{
			name: "a nil task declares nothing",
			task: nil,
			want: CgroupLimits{},
		},
		{
			name: "declared nanoCPUs become a microsecond quota",
			task: &Task{
				ID:   "t1",
				Spec: TaskSpec{Resources: ResourceRequirements{Limits: &Resources{NanoCPUs: 2e9}}},
			},
			want: CgroupLimits{CPUQuotaUs: 2_000_000},
		},
		{
			name: "a fractional CPU is honoured as declared",
			task: &Task{
				ID:   "t1",
				Spec: TaskSpec{Resources: ResourceRequirements{Limits: &Resources{NanoCPUs: 1500000000}}},
			},
			want: CgroupLimits{CPUQuotaUs: 1_500_000},
		},
		{
			name: "memory carries the VMM headroom",
			task: &Task{
				ID:   "t1",
				Spec: TaskSpec{Resources: ResourceRequirements{Limits: &Resources{MemoryBytes: 512 * mib}}},
			},
			want: CgroupLimits{MemoryMaxB: 512*mib + CgroupMemoryHeadroom},
		},
		{
			name: "both declared limits are resolved together",
			task: &Task{
				ID: "t1",
				Spec: TaskSpec{Resources: ResourceRequirements{Limits: &Resources{
					NanoCPUs:    1e9,
					MemoryBytes: 256 * mib,
				}}},
			},
			want: CgroupLimits{CPUQuotaUs: 1_000_000, MemoryMaxB: 256*mib + CgroupMemoryHeadroom},
		},
		{
			name: "reservations are the source when no limit is declared",
			task: &Task{
				ID:   "t1",
				Spec: TaskSpec{Resources: ResourceRequirements{Reservations: &Resources{NanoCPUs: 4e9, MemoryBytes: 2048 * mib}}},
			},
			want: CgroupLimits{CPUQuotaUs: 4_000_000, MemoryMaxB: 2048*mib + CgroupMemoryHeadroom},
		},
		{
			name: "limits win over reservations and are never mixed",
			task: &Task{
				ID: "t1",
				Spec: TaskSpec{Resources: ResourceRequirements{
					Limits:       &Resources{NanoCPUs: 2e9},
					Reservations: &Resources{MemoryBytes: 4096 * mib},
				}},
			},
			want: CgroupLimits{CPUQuotaUs: 2_000_000},
		},
		{
			name: "negative values are treated as undeclared",
			task: &Task{
				ID:   "t1",
				Spec: TaskSpec{Resources: ResourceRequirements{Limits: &Resources{NanoCPUs: -2e9, MemoryBytes: -512 * mib}}},
			},
			want: CgroupLimits{},
		},
		{
			name: "a cpuset annotation is returned verbatim and trimmed",
			task: &Task{
				ID:          "t1",
				Annotations: map[string]string{CpusetLabel: " 3,1-2 "},
			},
			want: CgroupLimits{CpusetCPUs: "3,1-2"},
		},
		{
			name: "a whitespace-only annotation means no cpuset",
			task: &Task{
				ID:          "t1",
				Annotations: map[string]string{CpusetLabel: "   "},
			},
			want: CgroupLimits{},
		},
		{
			name: "a numa-node annotation is returned verbatim and trimmed",
			task: &Task{
				ID:          "t1",
				Annotations: map[string]string{NumaLabel: " 1,0 "},
			},
			want: CgroupLimits{NumaNodes: "1,0"},
		},
		{
			name: "cpuset and numa-node annotations are resolved together",
			task: &Task{
				ID:          "t1",
				Annotations: map[string]string{CpusetLabel: "0-1", NumaLabel: "0"},
			},
			want: CgroupLimits{CpusetCPUs: "0-1", NumaNodes: "0"},
		},
		{
			name: "a whitespace-only numa-node annotation means no placement",
			task: &Task{
				ID:          "t1",
				Annotations: map[string]string{NumaLabel: "   "},
			},
			want: CgroupLimits{},
		},
		{
			name: "a CPU request below the kernel minimum is rejected by value",
			task: &Task{
				ID:   "t1",
				Spec: TaskSpec{Resources: ResourceRequirements{Limits: &Resources{NanoCPUs: 1000}}},
			},
			wantErr: "1000 NanoCPUs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveCgroupLimits(tt.task)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
