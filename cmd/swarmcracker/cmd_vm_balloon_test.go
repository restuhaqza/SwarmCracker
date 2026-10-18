package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/spf13/cobra"
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

// writeBalloonTestConfig writes a minimal, valid configuration to a fresh temp
// directory and returns its path. The command test passes it explicitly through
// the global --config flag so it never depends on the ambient
// /etc/swarmcracker/config.yaml: where that file is absent (CI), `vm create`
// otherwise fails while loading configuration and never reaches the balloon
// validation under test.
//
// The contents are the smallest a config needs to load and validate: the
// version marker, the two paths Validate requires, and nothing else — the
// loader fills the remaining fields from SetDefaults. It is not a real host
// config, and it is only ever read.
func writeBalloonTestConfig(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "version: 1\nexecutor:\n  kernel_path: /var/lib/swarmcracker/vmlinux\n  rootfs_dir: /var/lib/swarmcracker/rootfs\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// newRootWithConfig mirrors the slice of the real CLI's root that `vm create`
// depends on: the global persistent --config flag (registered on the root in
// main.go) plus the vm command group. Driving the flag — rather than letting the
// command fall back to the host's default path — is what makes the test below
// host-independent.
func newRootWithConfig() *cobra.Command {
	root := &cobra.Command{Use: "swarmcracker"}
	root.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "Path to configuration file (default: /etc/swarmcracker/config.yaml)")
	root.AddCommand(newVMCommand())
	return root
}

// TestVMCreateBalloonRejectedBeforeAnythingIsCreated verifies the two validation
// errors fire from the command itself, before any VM is created: a non-positive
// target, and a target that is not smaller than the VM's memory (both numbers
// named). The configuration is supplied explicitly via --config so the test
// exercises the same code path on any host, whether or not the default config
// file exists.
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
			cmd := newRootWithConfig()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs(append([]string{"vm", "create", "--config", writeBalloonTestConfig(t)}, tc.args...))

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

// TestBuildVMCreateTask_CarriesGoldenAndBalloonAnnotations drives the assembly
// the `vm create` RunE performs: `buildVMCreateTask` is the function that sets
// the task annotations, and RunE calls it with exactly these inputs (the
// resolved golden annotations and the validated --balloon-mib target). It pins
// the regression the task brief named: a `vm create --golden --balloon-mib N`
// task must carry both the golden annotations and the balloon target on the task
// handed downstream; reverting the merge inside that function to an assignment
// would silently drop one of them and this test would fail.
func TestBuildVMCreateTask_CarriesGoldenAndBalloonAnnotations(t *testing.T) {
	golden := func() map[string]string {
		return map[string]string{
			types.AnnotationRootfs:         "/var/lib/firecracker/golden/ubuntu-24.04-docker.ext4",
			types.AnnotationPrebuiltRootfs: "true",
			types.AnnotationGolden:         "ubuntu-24.04-docker@1.0.0",
		}
	}

	t.Run("golden annotations and the target both survive", func(t *testing.T) {
		task := buildVMCreateTask("golden:ubuntu-24.04-docker@1.0.0", 2, 2048, nil, golden(), "256", "", "", false)

		assert.Equal(t, "256", task.Annotations[types.BalloonLabel])
		assert.Equal(t, "/var/lib/firecracker/golden/ubuntu-24.04-docker.ext4", task.Annotations[types.AnnotationRootfs],
			"the golden rootfs must survive the balloon merge")
		assert.Equal(t, "true", task.Annotations[types.AnnotationPrebuiltRootfs])
		assert.Equal(t, "ubuntu-24.04-docker@1.0.0", task.Annotations[types.AnnotationGolden])
		assert.Len(t, task.Annotations, 4)
	})

	t.Run("no flag leaves the golden annotations unchanged", func(t *testing.T) {
		want := golden()
		task := buildVMCreateTask("golden:ubuntu-24.04-docker@1.0.0", 2, 2048, nil, want, "", "", "", false)

		assert.Equal(t, want, task.Annotations)
		_, ok := task.Annotations[types.BalloonLabel]
		assert.False(t, ok, "an unset --balloon-mib must not add an annotation")
	})

	t.Run("no golden and no flag keeps the mock task's annotations", func(t *testing.T) {
		task := buildVMCreateTask("alpine:latest", 1, 512, []string{"A=B"}, nil, "", "", "", false)

		assert.Equal(t, createMockTask("alpine:latest", 1, 512, []string{"A=B"}).Annotations, task.Annotations)
		assert.Nil(t, task.Annotations, "an unconfigured vm create must not gain an annotation map")
	})

	t.Run("no golden with the flag sets only the target", func(t *testing.T) {
		task := buildVMCreateTask("alpine:latest", 1, 512, nil, nil, "128", "", "", false)

		require.Len(t, task.Annotations, 1)
		assert.Equal(t, "128", task.Annotations[types.BalloonLabel])
	})

	t.Run("the requested resources still reach the task", func(t *testing.T) {
		task := buildVMCreateTask("alpine:latest", 3, 2048, nil, nil, "256", "", "", false)

		require.NotNil(t, task.Spec.Resources.Limits)
		assert.Equal(t, int64(2048*1024*1024), task.Spec.Resources.Limits.MemoryBytes)
		assert.Equal(t, int64(3*1e9), task.Spec.Resources.Limits.NanoCPUs)
	})
}
