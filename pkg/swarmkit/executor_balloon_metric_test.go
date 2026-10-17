package swarmkit

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

// TestBalloonMibFromTask pin the decision behind the balloon metric: the writer
// records a target only when the task actually carries a usable balloon
// annotation, so a VM without a balloon emits no series at all (a zero-valued
// series for every VM would carry no signal — the dead-metric failure this task
// exists to avoid).
func TestBalloonMibFromTask(t *testing.T) {
	tests := []struct {
		name  string
		task  *types.Task
		want  int64
		wantK bool
	}{
		{
			name:  "no balloon annotation",
			task:  &types.Task{ID: "t", Annotations: map[string]string{"rootfs": "/r"}},
			want:  0,
			wantK: false,
		},
		{
			name:  "nil annotations",
			task:  &types.Task{ID: "t"},
			want:  0,
			wantK: false,
		},
		{
			name:  "blank annotation",
			task:  &types.Task{ID: "t", Annotations: map[string]string{types.BalloonLabel: "  "}},
			want:  0,
			wantK: false,
		},
		{
			name:  "configured target",
			task:  &types.Task{ID: "t", Annotations: map[string]string{types.BalloonLabel: "256"}},
			want:  256,
			wantK: true,
		},
		{
			name:  "configured target with surrounding space",
			task:  &types.Task{ID: "t", Annotations: map[string]string{types.BalloonLabel: " 512 "}},
			want:  512,
			wantK: true,
		},
		{
			name:  "unparseable annotation yields no series",
			task:  &types.Task{ID: "t", Annotations: map[string]string{types.BalloonLabel: "256m"}},
			want:  0,
			wantK: false,
		},
		{
			name:  "nil task",
			task:  nil,
			want:  0,
			wantK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := balloonMibFromTask(tc.task)
			assert.Equal(t, tc.wantK, ok, "configured decision")
			assert.Equal(t, tc.want, got, "target in MiB")
		})
	}
}
