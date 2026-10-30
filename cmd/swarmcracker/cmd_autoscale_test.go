package main

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutoscaleCommandWiring guards the flag-to-option path of both commands.
// service create hands the flag value to the options struct (the RunE seam that
// once dropped --args); service update must see an explicit empty value as a
// set flag, because that is how a policy is cleared.
func TestAutoscaleCommandWiring(t *testing.T) {
	t.Run("create passes the value through", func(t *testing.T) {
		var got serviceCreateOptions
		serviceCreateHook = func(opts serviceCreateOptions) error {
			got = opts
			return nil
		}
		t.Cleanup(func() { serviceCreateHook = nil })

		cmd := newServiceCreateCommand()
		cmd.SetArgs([]string{
			"--name", "web",
			"--image", "nginx",
			"--autoscale", "min=2,max=10,metric=cpu,target=70",
		})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)

		require.NoError(t, cmd.Execute())
		assert.Equal(t, "min=2,max=10,metric=cpu,target=70", got.autoscale)
	})

	t.Run("update sees an explicit empty value as set", func(t *testing.T) {
		cmd := newServiceUpdateCommand()
		require.NoError(t, cmd.ParseFlags([]string{"--autoscale", ""}))
		assert.True(t, anyFlagChanged(cmd, "autoscale"),
			"an explicit empty --autoscale must count as a request to clear the policy")
	})
}

// TestAutoscaleFlagRegistered verifies --autoscale exists on both commands that
// can declare a policy, that it defaults to empty (no policy), and that its
// help documents both the grammar and how to clear a policy. A flag a user
// cannot find in --help is a flag that does not exist.
func TestAutoscaleFlagRegistered(t *testing.T) {
	for name, tc := range map[string]struct {
		cmd   *cobra.Command
		usage []string
	}{
		"service create": {
			cmd:   newServiceCreateCommand(),
			usage: []string{"min=", "max=", "metric=cpu", "target="},
		},
		"service update": {
			cmd:   newServiceUpdateCommand(),
			usage: []string{"min=", "max=", "metric=cpu", "target=", "empty value"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			flag := tc.cmd.Flags().Lookup("autoscale")
			require.NotNil(t, flag, "%s must register --autoscale", name)
			assert.Equal(t, "string", flag.Value.Type(), "%s: --autoscale flag type", name)
			assert.Equal(t, "", flag.DefValue, "%s: an absent --autoscale must declare no policy", name)
			for _, want := range tc.usage {
				assert.Contains(t, flag.Usage, want, "%s: --autoscale help must document %q", name, want)
			}
		})
	}
}

// autoscaleLabels returns the six labels a policy is stored as, in the form
// the CLI is expected to write them.
func autoscaleLabels(min, max, metric, target, up, down string) map[string]string {
	return map[string]string{
		types.AutoscaleMinLabel:          min,
		types.AutoscaleMaxLabel:          max,
		types.AutoscaleMetricLabel:       metric,
		types.AutoscaleTargetLabel:       target,
		types.AutoscaleCooldownUpLabel:   up,
		types.AutoscaleCooldownDownLabel: down,
	}
}

// TestAutoscaleBuildServiceSpec verifies the create mapping: a valid --autoscale
// lands as the six service labels in canonical form, an absent (or empty) flag
// adds nothing, and a malformed or contradictory value fails instead of being
// stored in a shape the manager loop cannot read.
func TestAutoscaleBuildServiceSpec(t *testing.T) {
	base := func() serviceCreateOptions {
		return serviceCreateOptions{name: "web", image: "nginx", replicas: 1}
	}

	t.Run("policy reaches the labels", func(t *testing.T) {
		o := base()
		o.autoscale = "min=2,max=10,metric=cpu,target=70"
		spec, err := buildServiceSpec(o)
		require.NoError(t, err)
		assert.Equal(t, autoscaleLabels("2", "10", "cpu", "70", "1m0s", "5m0s"), spec.Annotations.Labels)
	})

	t.Run("cooldowns reach the labels", func(t *testing.T) {
		o := base()
		o.autoscale = "target=55.5,min=1,max=3,metric=cpu,cooldown-up=30s,cooldown-down=2m"
		spec, err := buildServiceSpec(o)
		require.NoError(t, err)
		assert.Equal(t, autoscaleLabels("1", "3", "cpu", "55.5", "30s", "2m0s"), spec.Annotations.Labels)
	})

	t.Run("unset adds no label", func(t *testing.T) {
		spec, err := buildServiceSpec(base())
		require.NoError(t, err)
		for _, key := range types.AutoscaleLabelKeys() {
			_, ok := spec.Annotations.Labels[key]
			assert.False(t, ok, "an unset --autoscale must not add %s", key)
		}
	})

	t.Run("empty on create adds no label", func(t *testing.T) {
		o := base()
		o.autoscale = ""
		spec, err := buildServiceSpec(o)
		require.NoError(t, err)
		for _, key := range types.AutoscaleLabelKeys() {
			_, ok := spec.Annotations.Labels[key]
			assert.False(t, ok, "an empty --autoscale on create must not add %s", key)
		}
	})

	t.Run("malformed policy is rejected", func(t *testing.T) {
		for _, tc := range []struct {
			value string
			want  []string
		}{
			{"min=2,max=10,metric=cpu", []string{"missing target"}},
			{"min=10,max=2,metric=cpu,target=70", []string{"greater than or equal to min"}},
			{"min=2,max=10,metric=memory,target=70", []string{`unsupported autoscale metric "memory" (supported: cpu)`}},
			{"min=2,max=10,metric=cpu,target=0", []string{"greater than 0 and at most 100"}},
			{"min=2,max=10,metric=cpu,target=70,cooldown-up=soon", []string{"must be a duration"}},
		} {
			o := base()
			o.autoscale = tc.value
			_, err := buildServiceSpec(o)
			require.Error(t, err, "%q must not build a spec", tc.value)
			assert.Contains(t, err.Error(), "--autoscale", "the error must name the flag")
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want, "the error must say what is wrong and the valid form")
			}
		}
	})

	t.Run("agreeing partial --label is merged", func(t *testing.T) {
		o := base()
		o.autoscale = "min=2,max=10,metric=cpu,target=70"
		// A single label as well: the flag supplies the rest, and what gets
		// stored is the complete canonical set, never a half-written policy.
		o.labels = []string{types.AutoscaleTargetLabel + "=70.0", types.AutoscaleMinLabel + "=2"}
		spec, err := buildServiceSpec(o)
		require.NoError(t, err)
		assert.Equal(t, autoscaleLabels("2", "10", "cpu", "70", "1m0s", "5m0s"), spec.Annotations.Labels)
	})

	t.Run("conflicting --label is rejected", func(t *testing.T) {
		o := base()
		o.autoscale = "min=2,max=10,metric=cpu,target=70"
		o.labels = []string{types.AutoscaleMaxLabel + "=4"}
		_, err := buildServiceSpec(o)
		require.Error(t, err, "--autoscale must not silently overwrite a --label")
		assert.Contains(t, err.Error(), "--autoscale")
		assert.Contains(t, err.Error(), types.AutoscaleMaxLabel)
	})

	t.Run("unreadable --label is rejected", func(t *testing.T) {
		o := base()
		o.autoscale = "min=2,max=10,metric=cpu,target=70"
		o.labels = []string{types.AutoscaleTargetLabel + "=high"}
		_, err := buildServiceSpec(o)
		require.Error(t, err, "a --label no policy could hold must not be overridden silently")
		assert.Contains(t, err.Error(), "--autoscale")
		assert.Contains(t, err.Error(), types.AutoscaleTargetLabel)
	})
}

// TestAutoscaleCreate_RejectedBeforeRPC verifies a malformed --autoscale sends
// no create request: the value is validated while the spec is built, ahead of
// the CreateService RPC.
func TestAutoscaleCreate_RejectedBeforeRPC(t *testing.T) {
	client := &fakeServiceCreateClient{}
	err := createServiceWithClient(context.Background(), client, serviceCreateOptions{
		name: "web", image: "nginx", replicas: 1,
		autoscale: "min=2,max=10,metric=cpu,target=101",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--autoscale")
	assert.Contains(t, err.Error(), "at most 100")
	assert.Zero(t, client.createCalls, "a malformed policy must not send the create RPC")
	assert.Nil(t, client.spec)
}

// serviceWithAutoscale builds a one-container service carrying the given
// autoscale labels (nil for a service with no policy).
func serviceWithAutoscale(labels map[string]string) *api.Service {
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

// TestAutoscaleUpdate_TriState verifies --autoscale on `service update`:
// absent preserves the declared policy, a value replaces it, an explicit empty
// value removes all six labels (clearing it), and a malformed value fails
// before the service lookup.
func TestAutoscaleUpdate_TriState(t *testing.T) {
	existing := autoscaleLabels("1", "3", "cpu", "50", "30s", "2m0s")

	t.Run("absent preserves the policy", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscale(existing)}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			replicas: 2, replicasSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, existing, client.updatedSpec.Annotations.Labels)
	})

	t.Run("absent on a service without a policy adds none", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscale(nil)}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			replicas: 2, replicasSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		for _, key := range types.AutoscaleLabelKeys() {
			_, ok := client.updatedSpec.Annotations.Labels[key]
			assert.False(t, ok, "an absent --autoscale must not add %s", key)
		}
	})

	t.Run("value replaces the policy", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscale(existing)}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			autoscale: "min=4,max=8,metric=cpu,target=75", autoscaleSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, autoscaleLabels("4", "8", "cpu", "75", "1m0s", "5m0s"),
			client.updatedSpec.Annotations.Labels,
			"a new policy must not keep the old cooldowns")
	})

	t.Run("explicit empty clears every label", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscale(existing)}
		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			autoscale: "", autoscaleSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		for _, key := range types.AutoscaleLabelKeys() {
			_, ok := client.updatedSpec.Annotations.Labels[key]
			assert.False(t, ok, "--autoscale \"\" must remove %s", key)
		}
	})

	t.Run("malformed value fails before any RPC", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscale(existing)}
		err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			autoscale: "min=2,max=10,metric=disk,target=70", autoscaleSet: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "--autoscale")
		assert.Contains(t, err.Error(), `unsupported autoscale metric "disk" (supported: cpu)`)
		assert.Zero(t, client.getCalls, "the malformed policy must fail before the service lookup")
		assert.Zero(t, client.listCalls)
		assert.Zero(t, client.updateCalls)
		assert.Nil(t, client.updatedSpec)
	})

	t.Run("rejected with --rollback", func(t *testing.T) {
		svc := serviceWithAutoscale(existing)
		svc.PreviousSpec = serviceWithAutoscale(nil).Spec.Copy()
		client := &fakeServiceUpdateClient{service: svc}
		err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			autoscale: "", autoscaleSet: true, rollbackAction: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "--autoscale")
		assert.Contains(t, err.Error(), "--rollback")
		assert.Zero(t, client.updateCalls, "a rejected combination must not apply the rollback")
	})
}

// TestServiceInspectAutoscale verifies `service inspect` renders a declared
// policy in the operator's terms, and that a half-written one is reported as
// invalid rather than shown as if it were configured.
func TestServiceInspectAutoscale(t *testing.T) {
	t.Run("policy is rendered", func(t *testing.T) {
		var buf bytes.Buffer
		writeServiceText(&buf, serviceWithAutoscale(
			autoscaleLabels("2", "10", "cpu", "70", "1m0s", "5m0s")))
		out := buf.String()

		assert.Contains(t, out, "Autoscale:\n")
		assert.Contains(t, out, "metric: cpu")
		assert.Contains(t, out, "target: 70% of one vCPU")
		assert.Contains(t, out, "replicas: 2-10")
		assert.Contains(t, out, "cooldown: up 1m0s, down 5m0s")
	})

	t.Run("no policy renders nothing", func(t *testing.T) {
		var buf bytes.Buffer
		writeServiceText(&buf, serviceWithAutoscale(nil))
		assert.NotContains(t, buf.String(), "Autoscale",
			"a service without a policy must not grow an empty Autoscale block")
	})

	t.Run("incomplete policy is reported as invalid", func(t *testing.T) {
		var buf bytes.Buffer
		writeServiceText(&buf, serviceWithAutoscale(map[string]string{
			types.AutoscaleMinLabel: "2",
			types.AutoscaleMaxLabel: "10",
		}))
		out := buf.String()

		assert.Contains(t, out, "Autoscale: invalid")
		assert.Contains(t, out, types.AutoscaleMetricLabel,
			"the invalid policy must say which labels are missing")
	})
}
