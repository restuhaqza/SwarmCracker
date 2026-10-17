package main

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMCreateBalloonFlag verifies `vm create` registers --balloon-mib.
func TestVMCreateBalloonFlag(t *testing.T) {
	cmd := newVMCreateCommand()

	flag := cmd.Flags().Lookup("balloon-mib")
	require.NotNil(t, flag, "vm create must register --balloon-mib")
	assert.Equal(t, "int", flag.Value.Type())
	assert.Equal(t, "0", flag.DefValue)
	assert.Contains(t, cmd.UsageString(), "--balloon-mib")
}

// TestVMCreateBalloonRejectedBeforeAnythingIsCreated verifies the two validation
// errors fire from the command itself, before any VM is created: a non-positive
// target, and a target that is not smaller than the VM's memory (both numbers
// named).
func TestVMCreateBalloonRejectedBeforeAnythingIsCreated(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "zero",
			args: []string{"--balloon-mib", "0", "alpine:latest"},
			want: []string{"--balloon-mib 0", "positive"},
		},
		{
			name: "negative",
			args: []string{"--balloon-mib", "-8", "alpine:latest"},
			want: []string{"--balloon-mib -8", "positive"},
		},
		{
			name: "equal to the default memory",
			args: []string{"--balloon-mib", "512", "alpine:latest"},
			want: []string{"--balloon-mib 512", "memory size 512"},
		},
		{
			name: "larger than --memory",
			args: []string{"--memory", "1024", "--balloon-mib", "2048", "alpine:latest"},
			want: []string{"--balloon-mib 2048", "memory size 1024"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newVMCreateCommand()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs(tc.args)

			err := cmd.Execute()
			require.Error(t, err)
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestWithBalloonAnnotation verifies the target is merged into a task's
// annotations rather than replacing them: a `vm create --golden` task already
// carries the golden annotations, which an assignment would silently drop. An
// empty target must leave the map untouched, so an unconfigured VM is identical
// to before.
func TestWithBalloonAnnotation(t *testing.T) {
	t.Run("adds the target to a nil map", func(t *testing.T) {
		got := withBalloonAnnotation(nil, "256")
		require.NotNil(t, got)
		assert.Equal(t, "256", got[types.BalloonLabel])
		assert.Len(t, got, 1)
	})

	t.Run("preserves existing annotations", func(t *testing.T) {
		golden := map[string]string{
			types.AnnotationRootfs: "/var/lib/firecracker/golden/ubuntu.ext4",
			types.AnnotationGolden: "ubuntu-24.04-docker@1.0.0",
		}

		got := withBalloonAnnotation(golden, "256")
		assert.Equal(t, "256", got[types.BalloonLabel])
		assert.Equal(t, "/var/lib/firecracker/golden/ubuntu.ext4", got[types.AnnotationRootfs],
			"the golden annotations must survive the merge")
		assert.Equal(t, "ubuntu-24.04-docker@1.0.0", got[types.AnnotationGolden])
	})

	t.Run("unset adds nothing", func(t *testing.T) {
		assert.Nil(t, withBalloonAnnotation(nil, ""), "an unset flag must not create a map")

		existing := map[string]string{"rootfs": "/tmp/rootfs.ext4"}
		got := withBalloonAnnotation(existing, "")
		assert.Equal(t, existing, got)
		_, ok := got[types.BalloonLabel]
		assert.False(t, ok, "an unset flag must not add an annotation")
	})
}
