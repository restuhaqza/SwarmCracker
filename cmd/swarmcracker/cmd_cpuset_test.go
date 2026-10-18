package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cpusetFlagHelpRequirement is the part of the --cpuset help text the tests
// pin: the accepted syntax and the host requirement. Both are user-facing
// contract, not incidental wording.
var cpusetFlagHelpRequirement = []string{"--cpuset", "0-3,8", "online"}

// assertCpusetFlag checks one command's registration of the flag: a string
// flag defaulting to empty (an unset flag must not pin anything), documented in
// --help with the syntax and the host requirement.
func assertCpusetFlag(t *testing.T, cmdName string, usage string, flagType, defValue string) {
	t.Helper()
	assert.Equal(t, "string", flagType, "%s: --cpuset must be a string flag", cmdName)
	assert.Equal(t, "", defValue, "%s: an absent --cpuset must default to empty", cmdName)
	for _, want := range cpusetFlagHelpRequirement {
		assert.Contains(t, usage, want, "%s: --help must document %q", cmdName, want)
	}
}

// TestCpusetFlagRegistered verifies --cpuset is registered on every command
// that can pin a VM — service create, service update and vm create — so the
// flag a user types is never an unknown flag and always shows in --help.
func TestCpusetFlagRegistered(t *testing.T) {
	create := newServiceCreateCommand()
	flag := create.Flags().Lookup("cpuset")
	require.NotNil(t, flag, "service create must register --cpuset")
	assertCpusetFlag(t, "service create", create.UsageString(), flag.Value.Type(), flag.DefValue)

	update := newServiceUpdateCommand()
	flag = update.Flags().Lookup("cpuset")
	require.NotNil(t, flag, "service update must register --cpuset")
	assertCpusetFlag(t, "service update", update.UsageString(), flag.Value.Type(), flag.DefValue)
	assert.Contains(t, update.UsageString(), "empty value removes",
		"service update --help must document how to clear the pin")

	vm := newVMCreateCommand()
	flag = vm.Flags().Lookup("cpuset")
	require.NotNil(t, flag, "vm create must register --cpuset")
	assertCpusetFlag(t, "vm create", vm.UsageString(), flag.Value.Type(), flag.DefValue)
}

// TestBuildServiceSpec_Cpuset verifies the service-create mapping: an unset
// flag adds nothing, a valid cpulist is stored on CpusetLabel in canonical
// form (so the CLI and the daemon never hold different strings), and a
// malformed list fails before the spec exists.
func TestBuildServiceSpec_Cpuset(t *testing.T) {
	base := func() serviceCreateOptions {
		return serviceCreateOptions{name: "web", image: "nginx", replicas: 1}
	}

	t.Run("unset adds no label", func(t *testing.T) {
		spec, err := buildServiceSpec(base())
		require.NoError(t, err)
		_, ok := spec.Annotations.Labels[types.CpusetLabel]
		assert.False(t, ok, "an unset --cpuset must not add a label")
	})

	valid := []struct {
		name string
		in   string
		want string
	}{
		{"single cpu", "0", "0"},
		{"range", "0-3", "0-3"},
		{"range and cpu", "0-3,8", "0-3,8"},
		{"unsorted merges", "3,1-2", "1-3"},
		{"adjacent merges", "1,2,5", "1-2,5"},
		{"overlapping merges", "0-3,2-5", "0-5"},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			o := base()
			o.cpuset, o.cpusetSet = tc.in, true
			spec, err := buildServiceSpec(o)
			require.NoError(t, err)
			assert.Equal(t, tc.want, spec.Annotations.Labels[types.CpusetLabel],
				"%q must be stored canonically as %q", tc.in, tc.want)
		})
	}

	invalid := []string{"", "a", "1-", "5-3", "1,,2", "01", "0 1", "-1"}
	for _, in := range invalid {
		t.Run(fmt.Sprintf("invalid/%q", in), func(t *testing.T) {
			o := base()
			o.cpuset, o.cpusetSet = in, true
			_, err := buildServiceSpec(o)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--cpuset", "the error must name the flag")
			assert.Contains(t, err.Error(), fmt.Sprintf("%q", in), "the error must name the offending value")
		})
	}

	t.Run("an agreeing --label is accepted", func(t *testing.T) {
		o := base()
		o.cpuset, o.cpusetSet = "3,1-2", true
		o.labels = []string{types.CpusetLabel + "=1-3"}
		spec, err := buildServiceSpec(o)
		require.NoError(t, err)
		assert.Equal(t, "1-3", spec.Annotations.Labels[types.CpusetLabel])
	})

	t.Run("a conflicting --label is rejected", func(t *testing.T) {
		o := base()
		o.cpuset, o.cpusetSet = "8", true
		o.labels = []string{types.CpusetLabel + "=0-3"}
		_, err := buildServiceSpec(o)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `--cpuset "8"`)
		assert.Contains(t, err.Error(), types.CpusetLabel)
	})
}

// TestCreateService_CpusetLabel verifies the create path submits the canonical
// pin as the service label, and that a malformed list sends no create RPC (the
// validation must happen before the spec is submitted, never after).
func TestCreateService_CpusetLabel(t *testing.T) {
	t.Run("value reaches the spec canonically", func(t *testing.T) {
		client := &fakeServiceCreateClient{}
		require.NoError(t, createServiceWithClient(context.Background(), client, serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			cpuset: "3,1-2", cpusetSet: true,
		}))

		require.Equal(t, 1, client.createCalls)
		require.NotNil(t, client.spec)
		assert.Equal(t, "1-3", client.spec.Annotations.Labels[types.CpusetLabel])
	})

	t.Run("no flag leaves no label", func(t *testing.T) {
		client := &fakeServiceCreateClient{}
		require.NoError(t, createServiceWithClient(context.Background(), client, serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
		}))

		require.Equal(t, 1, client.createCalls)
		_, ok := client.spec.Annotations.Labels[types.CpusetLabel]
		assert.False(t, ok, "an unset --cpuset must not add a label")
	})

	t.Run("invalid value sends no RPC", func(t *testing.T) {
		client := &fakeServiceCreateClient{}
		err := createServiceWithClient(context.Background(), client, serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			cpuset: "1,,2", cpusetSet: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "--cpuset")
		assert.Contains(t, err.Error(), `"1,,2"`)
		assert.Zero(t, client.createCalls, "a malformed cpuset must not send the create RPC")
		assert.Nil(t, client.spec)
	})
}

// serviceWithCpuset builds a one-container service carrying an optional
// swarmcracker.cpuset label.
func serviceWithCpuset(label string) *api.Service {
	labels := map[string]string{}
	if label != "" {
		labels[types.CpusetLabel] = label
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

// TestUpdateServiceCpuset_TriState verifies --cpuset is tri-state on `service
// update`: absent preserves the current pin, a value canonicalises and
// replaces it, and an explicit empty value removes it. A malformed list is
// rejected before any RPC — not even the service lookup — so the flag never
// reaches the manager partly applied.
func TestUpdateServiceCpuset_TriState(t *testing.T) {
	t.Run("absent preserves the current pin", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithCpuset("0-3")}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			replicas: 2, replicasSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, "0-3", client.updatedSpec.Annotations.Labels[types.CpusetLabel])
	})

	t.Run("absent on an unpinned service adds nothing", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithCpuset("")}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			replicas: 2, replicasSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		_, ok := client.updatedSpec.Annotations.Labels[types.CpusetLabel]
		assert.False(t, ok, "an absent --cpuset must not add a label")
	})

	t.Run("value canonicalises and replaces the pin", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithCpuset("0-3")}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			cpuset: "5,4", cpusetSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, "4-5", client.updatedSpec.Annotations.Labels[types.CpusetLabel])
	})

	t.Run("explicit empty removes the label", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithCpuset("0-3")}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			cpuset: "", cpusetSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		_, ok := client.updatedSpec.Annotations.Labels[types.CpusetLabel]
		assert.False(t, ok, "an explicit --cpuset \"\" must remove the label")
	})

	t.Run("invalid value is rejected before any RPC", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithCpuset("0-3")}
		err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			cpuset: "5-3", cpusetSet: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "--cpuset")
		assert.Contains(t, err.Error(), `"5-3"`)
		assert.Zero(t, client.getCalls, "the malformed list must fail before the service lookup")
		assert.Zero(t, client.listCalls)
		assert.Zero(t, client.updateCalls)
		assert.Nil(t, client.updatedSpec)
	})
}

// TestVMCreateCpusetRejectedBeforeAnythingIsCreated verifies `vm create`
// rejects a malformed --cpuset from the command itself. The configuration is
// supplied explicitly via --config, the same way the other vm create command
// tests do, so the test does not depend on the host's default config file.
func TestVMCreateCpusetRejectedBeforeAnythingIsCreated(t *testing.T) {
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
				"--cpuset", tc.value,
				"alpine:latest",
			})

			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--cpuset", "the error must name the flag")
			assert.Contains(t, err.Error(), fmt.Sprintf("%q", tc.value), "the error must name the offending value")
		})
	}
}

// TestUpdateServiceCpuset_RejectsRollback verifies --cpuset cannot be combined
// with --rollback. The rollback branch submits PreviousSpec and never consults
// the pin, so accepting the flag would validate it and then silently drop it.
// PreviousSpec is populated so that, with the guard removed, the rollback would
// actually be applied — asserting updateCalls stays zero is what pins "no RPC".
func TestUpdateServiceCpuset_RejectsRollback(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "a pin", value: "0-3"},
		{name: "an unsorted pin", value: "3,1-2"},
		{name: "an explicit empty pin", value: ""},
		{name: "a malformed pin", value: "5-3"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := serviceWithCpuset("0-3")
			svc.PreviousSpec = serviceWithCpuset("0").Spec.Copy()

			client := &fakeServiceUpdateClient{service: svc}
			err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
				cpuset:         tc.value,
				cpusetSet:      true,
				rollbackAction: true,
			})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "--cpuset", "the error must name the flag")
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

// TestVMCreateCpusetAnnotationThroughCommand drives a valid --cpuset through
// `vm create`'s own RunE and asserts the task assembled there carries the
// canonical pin. Testing buildVMCreateTask alone cannot catch the regression
// this covers: RunE passing the raw flag value, or "" instead of the canonical
// cpusetValue, would leave every other test green. The task is observed through
// vmCreateTaskHook, so no VM is booted, and the configuration is a temp file
// supplied via --config, as in the other command-level vm create tests.
func TestVMCreateCpusetAnnotationThroughCommand(t *testing.T) {
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
		"--cpuset", "3,1-2",
		"alpine:latest",
	})

	require.NoError(t, cmd.Execute())
	require.NotNil(t, assembled, "vm create must hand an assembled task downstream")
	assert.Equal(t, "1-3", assembled.Annotations[types.AnnotationCpuset],
		"RunE must hand buildVMCreateTask the canonical --cpuset, not the value as typed")
}

// TestBuildVMCreateTask_CarriesCpusetAnnotation verifies the canonical pin is
// merged onto the task as types.AnnotationCpuset, alongside — not instead of —
// the golden-image annotations and the balloon target. Reverting the merge to
// an assignment would silently drop one of the three, and this test fails.
func TestBuildVMCreateTask_CarriesCpusetAnnotation(t *testing.T) {
	golden := map[string]string{
		types.AnnotationRootfs:         "/var/lib/firecracker/golden/ubuntu-24.04-docker.ext4",
		types.AnnotationPrebuiltRootfs: "true",
		types.AnnotationGolden:         "ubuntu-24.04-docker@1.0.0",
	}

	t.Run("pin merges with the golden annotations and the balloon", func(t *testing.T) {
		task := buildVMCreateTask("golden:ubuntu-24.04-docker@1.0.0", 2, 2048, nil, golden, "256", "1-3", "", false)

		assert.Equal(t, "1-3", task.Annotations[types.AnnotationCpuset])
		assert.Equal(t, "256", task.Annotations[types.BalloonLabel])
		assert.Equal(t, golden[types.AnnotationRootfs], task.Annotations[types.AnnotationRootfs],
			"the golden rootfs must survive the merge")
		assert.Equal(t, golden[types.AnnotationGolden], task.Annotations[types.AnnotationGolden])
	})

	t.Run("no pin leaves the map untouched", func(t *testing.T) {
		want := map[string]string{types.AnnotationGolden: "ubuntu-24.04-docker@1.0.0"}
		task := buildVMCreateTask("golden:ubuntu-24.04-docker@1.0.0", 2, 2048, nil, want, "", "", "", false)

		assert.Equal(t, want, task.Annotations)
		_, ok := task.Annotations[types.AnnotationCpuset]
		assert.False(t, ok, "an unset --cpuset must not add an annotation")
	})

	t.Run("pin alone creates the annotation map", func(t *testing.T) {
		task := buildVMCreateTask("alpine:latest", 1, 512, nil, nil, "", "0-3", "", false)

		require.Len(t, task.Annotations, 1)
		assert.Equal(t, "0-3", task.Annotations[types.AnnotationCpuset])
	})
}
