package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// placementFlags are the two flags this task adds. They are checked together
// because they are registered, documented and tri-stated as a pair.
var placementFlags = []struct {
	name string
	// flagType and defValue pin the flag's shape: an absent --hugepages must be
	// false and an absent --numa-node empty, so neither pins or binds anything
	// by default.
	flagType string
	defValue string
	// usage must document the accepted syntax and the host requirement, which
	// are user-facing contract rather than incidental wording.
	usage []string
}{
	{
		name:     "hugepages",
		flagType: "bool",
		defValue: "false",
		usage:    []string{"2M huge pages", "hugepage pool"},
	},
	{
		name:     "numa-node",
		flagType: "string",
		defValue: "",
		usage:    []string{"NUMA nodes", "0-1", "online"},
	},
}

// TestPlacementFlagsRegistered verifies --hugepages and --numa-node exist on
// every command that can request them — service create, service update and vm
// create — so the flag a user types is never an unknown flag and always shows
// in --help.
func TestPlacementFlagsRegistered(t *testing.T) {
	commands := map[string]*cobra.Command{
		"service create": newServiceCreateCommand(),
		"service update": newServiceUpdateCommand(),
		"vm create":      newVMCreateCommand(),
	}

	for cmdName, cmd := range commands {
		for _, tc := range placementFlags {
			flag := cmd.Flags().Lookup(tc.name)
			require.NotNil(t, flag, "%s must register --%s", cmdName, tc.name)
			assert.Equal(t, tc.flagType, flag.Value.Type(), "%s: --%s flag type", cmdName, tc.name)
			assert.Equal(t, tc.defValue, flag.DefValue, "%s: --%s default", cmdName, tc.name)
			for _, want := range tc.usage {
				assert.Contains(t, flag.Usage, want, "%s: --%s help must document %q", cmdName, tc.name, want)
			}
		}
	}

	assert.Contains(t, newServiceUpdateCommand().UsageString(), "empty value removes",
		"service update --help must document how to clear the NUMA binding")
	assert.Contains(t, newServiceUpdateCommand().UsageString(), "--hugepages=false",
		"service update --help must document how to clear the hugepages request")
}

// TestBuildServiceSpec_Hugepages verifies the create mapping: --hugepages stores
// the boolean request as swarmcracker.hugepages=true, and an absent or explicitly
// false flag adds nothing (hugepages is opt-in, never a default).
func TestBuildServiceSpec_Hugepages(t *testing.T) {
	base := func() serviceCreateOptions {
		return serviceCreateOptions{name: "web", image: "nginx", replicas: 1}
	}

	t.Run("set stores true", func(t *testing.T) {
		o := base()
		o.hugepages, o.hugepagesSet = true, true
		spec, err := buildServiceSpec(o)
		require.NoError(t, err)
		assert.Equal(t, "true", spec.Annotations.Labels[types.HugepagesLabel])
	})

	t.Run("unset adds no label", func(t *testing.T) {
		spec, err := buildServiceSpec(base())
		require.NoError(t, err)
		_, ok := spec.Annotations.Labels[types.HugepagesLabel]
		assert.False(t, ok, "an unset --hugepages must not add a label")
	})

	t.Run("explicit false adds no label", func(t *testing.T) {
		o := base()
		o.hugepages, o.hugepagesSet = false, true
		spec, err := buildServiceSpec(o)
		require.NoError(t, err)
		_, ok := spec.Annotations.Labels[types.HugepagesLabel]
		assert.False(t, ok, "--hugepages=false is not a hugepages request")
	})
}

// TestBuildServiceSpec_NumaNode verifies the create mapping: an unset flag adds
// nothing, a valid nodelist is stored on NumaLabel in canonical form (so the
// CLI and the daemon never hold different strings), and a malformed list fails
// before the spec exists.
func TestBuildServiceSpec_NumaNode(t *testing.T) {
	base := func() serviceCreateOptions {
		return serviceCreateOptions{name: "web", image: "nginx", replicas: 1}
	}

	t.Run("unset adds no label", func(t *testing.T) {
		spec, err := buildServiceSpec(base())
		require.NoError(t, err)
		_, ok := spec.Annotations.Labels[types.NumaLabel]
		assert.False(t, ok, "an unset --numa-node must not add a label")
	})

	valid := []struct {
		name string
		in   string
		want string
	}{
		{"single node", "0", "0"},
		{"range", "0-1", "0-1"},
		{"unsorted merges", "1,0", "0-1"},
		{"adjacent merges", "1,2,5", "1-2,5"},
		{"overlapping merges", "0-3,2-5", "0-5"},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			o := base()
			o.numaNode, o.numaNodeSet = tc.in, true
			spec, err := buildServiceSpec(o)
			require.NoError(t, err)
			assert.Equal(t, tc.want, spec.Annotations.Labels[types.NumaLabel],
				"%q must be stored canonically as %q", tc.in, tc.want)
		})
	}

	invalid := []string{"", "a", "1-", "5-3", "1,,2", "01", "0 1", "-1"}
	for _, in := range invalid {
		t.Run(fmt.Sprintf("invalid/%q", in), func(t *testing.T) {
			o := base()
			o.numaNode, o.numaNodeSet = in, true
			_, err := buildServiceSpec(o)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--numa-node", "the error must name the flag")
			assert.Contains(t, err.Error(), fmt.Sprintf("%q", in), "the error must name the offending value")
		})
	}

	t.Run("an agreeing --label is accepted", func(t *testing.T) {
		o := base()
		o.numaNode, o.numaNodeSet = "1,0", true
		o.labels = []string{types.NumaLabel + "=0-1"}
		spec, err := buildServiceSpec(o)
		require.NoError(t, err)
		assert.Equal(t, "0-1", spec.Annotations.Labels[types.NumaLabel])
	})

	t.Run("a conflicting --label is rejected", func(t *testing.T) {
		o := base()
		o.numaNode, o.numaNodeSet = "1", true
		o.labels = []string{types.NumaLabel + "=0"}
		_, err := buildServiceSpec(o)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `--numa-node "1"`)
		assert.Contains(t, err.Error(), types.NumaLabel)
	})
}

// TestCreateService_PlacementLabels verifies the create path submits both
// labels in their canonical form, and that a malformed nodelist sends no create
// RPC (validation must happen before the spec is submitted, never after).
func TestCreateService_PlacementLabels(t *testing.T) {
	t.Run("values reach the spec canonically", func(t *testing.T) {
		client := &fakeServiceCreateClient{}
		require.NoError(t, createServiceWithClient(context.Background(), client, serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			hugepages: true, hugepagesSet: true,
			numaNode: "3,1-2", numaNodeSet: true,
		}))

		require.Equal(t, 1, client.createCalls)
		require.NotNil(t, client.spec)
		assert.Equal(t, "true", client.spec.Annotations.Labels[types.HugepagesLabel])
		assert.Equal(t, "1-3", client.spec.Annotations.Labels[types.NumaLabel])
	})

	t.Run("no flags leave no labels", func(t *testing.T) {
		client := &fakeServiceCreateClient{}
		require.NoError(t, createServiceWithClient(context.Background(), client, serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
		}))

		require.Equal(t, 1, client.createCalls)
		_, huge := client.spec.Annotations.Labels[types.HugepagesLabel]
		_, numa := client.spec.Annotations.Labels[types.NumaLabel]
		assert.False(t, huge, "an unset --hugepages must not add a label")
		assert.False(t, numa, "an unset --numa-node must not add a label")
	})

	t.Run("invalid nodelist sends no RPC", func(t *testing.T) {
		client := &fakeServiceCreateClient{}
		err := createServiceWithClient(context.Background(), client, serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			numaNode: "1,,2", numaNodeSet: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "--numa-node")
		assert.Contains(t, err.Error(), `"1,,2"`)
		assert.Zero(t, client.createCalls, "a malformed nodelist must not send the create RPC")
		assert.Nil(t, client.spec)
	})
}

// serviceWithPlacement builds a one-container service carrying the given
// hugepages and NUMA labels (either may be empty to omit it).
func serviceWithPlacement(hugepages, numaNode string) *api.Service {
	labels := map[string]string{}
	if hugepages != "" {
		labels[types.HugepagesLabel] = hugepages
	}
	if numaNode != "" {
		labels[types.NumaLabel] = numaNode
	}
	return &api.Service{
		ID: "svc-1",
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{Name: "web", Labels: labels},
			Task: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{Container: &api.ContainerSpec{Image: "nginx"}},
			},
		},
	}
}

// TestUpdateServiceHugepages_TriState verifies --hugepages is tri-state on
// `service update`: absent preserves the current request, --hugepages sets it,
// and an explicit --hugepages=false removes it.
func TestUpdateServiceHugepages_TriState(t *testing.T) {
	t.Run("absent preserves the request", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithPlacement("true", "")}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			replicas: 2, replicasSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, "true", client.updatedSpec.Annotations.Labels[types.HugepagesLabel])
	})

	t.Run("absent on a non-hugepage service adds nothing", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithPlacement("", "")}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			replicas: 2, replicasSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		_, ok := client.updatedSpec.Annotations.Labels[types.HugepagesLabel]
		assert.False(t, ok, "an absent --hugepages must not add a label")
	})

	t.Run("set adds the request", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithPlacement("", "")}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			hugepages: true, hugepagesSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, "true", client.updatedSpec.Annotations.Labels[types.HugepagesLabel])
	})

	t.Run("explicit false removes the label", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithPlacement("true", "")}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			hugepages: false, hugepagesSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		_, ok := client.updatedSpec.Annotations.Labels[types.HugepagesLabel]
		assert.False(t, ok, "an explicit --hugepages=false must remove the label")
	})
}

// TestUpdateServiceNumaNode_TriState verifies --numa-node is tri-state on
// `service update`: absent preserves the binding, a value canonicalises and
// replaces it, and an explicit empty value removes it.
func TestUpdateServiceNumaNode_TriState(t *testing.T) {
	t.Run("absent preserves the binding", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithPlacement("", "0-3")}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			replicas: 2, replicasSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, "0-3", client.updatedSpec.Annotations.Labels[types.NumaLabel])
	})

	t.Run("absent on an unbound service adds nothing", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithPlacement("", "")}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			replicas: 2, replicasSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		_, ok := client.updatedSpec.Annotations.Labels[types.NumaLabel]
		assert.False(t, ok, "an absent --numa-node must not add a label")
	})

	t.Run("value canonicalises and replaces the binding", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithPlacement("", "0-3")}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			numaNode: "5,4", numaNodeSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, "4-5", client.updatedSpec.Annotations.Labels[types.NumaLabel])
	})

	t.Run("explicit empty removes the label", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithPlacement("", "0-3")}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			numaNode: "", numaNodeSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		_, ok := client.updatedSpec.Annotations.Labels[types.NumaLabel]
		assert.False(t, ok, "an explicit --numa-node \"\" must remove the label")
	})

	t.Run("invalid value is rejected before any RPC", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithPlacement("", "0-3")}
		err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			numaNode: "5-3", numaNodeSet: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "--numa-node")
		assert.Contains(t, err.Error(), `"5-3"`)
		assert.Zero(t, client.getCalls, "the malformed list must fail before the service lookup")
		assert.Zero(t, client.listCalls)
		assert.Zero(t, client.updateCalls)
		assert.Nil(t, client.updatedSpec)
	})
}

// TestUpdateServicePlacement_RejectsRollback verifies both flags cannot be
// combined with --rollback. The rollback branch submits PreviousSpec and never
// consults them, so accepting either would validate it and then silently drop
// it. PreviousSpec is populated so that, with the guard removed, the rollback
// would actually be applied — asserting updateCalls stays zero is what pins
// "no RPC".
func TestUpdateServicePlacement_RejectsRollback(t *testing.T) {
	tests := []struct {
		name string
		opts serviceUpdateOptions
		want []string
	}{
		{
			name: "hugepages",
			opts: serviceUpdateOptions{hugepages: true, hugepagesSet: true, rollbackAction: true},
			want: []string{"--hugepages"},
		},
		{
			name: "explicit hugepages false",
			opts: serviceUpdateOptions{hugepages: false, hugepagesSet: true, rollbackAction: true},
			want: []string{"--hugepages"},
		},
		{
			name: "numa node",
			opts: serviceUpdateOptions{numaNode: "0-1", numaNodeSet: true, rollbackAction: true},
			want: []string{"--numa-node"},
		},
		{
			name: "explicit empty numa node",
			opts: serviceUpdateOptions{numaNode: "", numaNodeSet: true, rollbackAction: true},
			want: []string{"--numa-node"},
		},
		{
			name: "both flags name both flags",
			opts: serviceUpdateOptions{hugepages: true, hugepagesSet: true, numaNode: "5-3", numaNodeSet: true, rollbackAction: true},
			want: []string{"--hugepages", "--numa-node"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := serviceWithPlacement("true", "0")
			svc.PreviousSpec = serviceWithPlacement("", "").Spec.Copy()

			client := &fakeServiceUpdateClient{service: svc}
			err := updateServiceWithClient(context.Background(), client, "web", tc.opts)

			require.Error(t, err)
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want, "the error must name the offending flag")
			}
			assert.Contains(t, err.Error(), "--rollback", "the error must name the conflicting flag")
			assert.Contains(t, err.Error(), "previous spec",
				"the error must explain why the combination is rejected")
			assert.Zero(t, client.getCalls, "the combination must be rejected before the service lookup")
			assert.Zero(t, client.listCalls)
			assert.Zero(t, client.updateCalls, "a rejected combination must not apply the rollback")
			assert.Nil(t, client.updatedSpec)
		})
	}
}

// TestVMCreatePlacementRejectedBeforeAnythingIsCreated verifies `vm create`
// rejects a malformed --numa-node from the command itself. The configuration is
// supplied explicitly via --config, as in the other command-level vm create
// tests, so the test does not depend on the host's default config file.
func TestVMCreatePlacementRejectedBeforeAnythingIsCreated(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "not a number", value: "a"},
		{name: "trailing dash", value: "1-"},
		{name: "reversed range", value: "5-3"},
		{name: "empty element", value: "1,,2"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newRootWithConfig()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs([]string{
				"vm", "create",
				"--config", writeBalloonTestConfig(t),
				"--numa-node", tc.value,
				"alpine:latest",
			})

			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--numa-node", "the error must name the flag")
			assert.Contains(t, err.Error(), fmt.Sprintf("%q", tc.value), "the error must name the offending value")
		})
	}
}

// TestVMCreatePlacementAnnotationsThroughCommand drives valid --hugepages and
// --numa-node values through `vm create`'s own RunE and asserts the task
// assembled there carries both. Testing buildVMCreateTask alone cannot catch
// the regression this covers: RunE passing the raw flag values, or dropping one
// of them, would leave every other test green. The task is observed through
// vmCreateTaskHook so no VM is booted, and the configuration is a temp file
// supplied via --config.
func TestVMCreatePlacementAnnotationsThroughCommand(t *testing.T) {
	var assembled *types.Task
	vmCreateTaskHook = func(task *types.Task) error {
		assembled = task
		return nil
	}
	t.Cleanup(func() { vmCreateTaskHook = nil })

	cmd := newRootWithConfig()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"vm", "create",
		"--config", writeBalloonTestConfig(t),
		"--hugepages",
		"--numa-node", "3,1-2",
		"alpine:latest",
	})

	require.NoError(t, cmd.Execute())
	require.NotNil(t, assembled, "vm create must hand an assembled task downstream")
	assert.Equal(t, "true", assembled.Annotations[types.AnnotationHugepages])
	assert.Equal(t, "1-3", assembled.Annotations[types.AnnotationNumaNode],
		"RunE must hand buildVMCreateTask the canonical --numa-node, not the value as typed")
}

// TestBuildVMCreateTask_CarriesPlacementAnnotations verifies both requests are
// merged onto the task alongside — not instead of — the golden-image
// annotations and the balloon target.
func TestBuildVMCreateTask_CarriesPlacementAnnotations(t *testing.T) {
	golden := map[string]string{
		types.AnnotationRootfs:         "/var/lib/firecracker/golden/ubuntu-24.04-docker.ext4",
		types.AnnotationPrebuiltRootfs: "true",
		types.AnnotationGolden:         "ubuntu-24.04-docker@1.0.0",
	}

	t.Run("requests merge with the golden annotations and the balloon", func(t *testing.T) {
		task := buildVMCreateTask("golden:ubuntu-24.04-docker@1.0.0", 2, 2048, nil, golden, "256", "1-3", "0-1", true)

		assert.Equal(t, "true", task.Annotations[types.AnnotationHugepages])
		assert.Equal(t, "0-1", task.Annotations[types.AnnotationNumaNode])
		assert.Equal(t, "1-3", task.Annotations[types.AnnotationCpuset])
		assert.Equal(t, "256", task.Annotations[types.BalloonLabel])
		assert.Equal(t, golden[types.AnnotationRootfs], task.Annotations[types.AnnotationRootfs],
			"the golden rootfs must survive the merge")
		assert.Equal(t, golden[types.AnnotationGolden], task.Annotations[types.AnnotationGolden])
	})

	t.Run("no requests leave the map untouched", func(t *testing.T) {
		want := map[string]string{types.AnnotationGolden: "ubuntu-24.04-docker@1.0.0"}
		task := buildVMCreateTask("golden:ubuntu-24.04-docker@1.0.0", 2, 2048, nil, want, "", "", "", false)

		assert.Equal(t, want, task.Annotations)
		_, huge := task.Annotations[types.AnnotationHugepages]
		_, numa := task.Annotations[types.AnnotationNumaNode]
		assert.False(t, huge, "an unset --hugepages must not add an annotation")
		assert.False(t, numa, "an unset --numa-node must not add an annotation")
	})

	t.Run("requests alone create the annotation map", func(t *testing.T) {
		task := buildVMCreateTask("alpine:latest", 1, 512, nil, nil, "", "", "0-1", true)

		require.Len(t, task.Annotations, 2)
		assert.Equal(t, "true", task.Annotations[types.AnnotationHugepages])
		assert.Equal(t, "0-1", task.Annotations[types.AnnotationNumaNode])
	})
}
