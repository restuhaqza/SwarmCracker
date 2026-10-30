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

	// The replica bound check only applies to an explicit --replicas (the
	// flag's default is 1, and a policy-min of 2 must not silently reject a
	// create that never mentioned replicas), so the "given at all" bit has to
	// reach the options struct.
	t.Run("create records whether --replicas was given", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args []string
			want bool
		}{
			{"explicit", []string{"--name", "web", "--image", "nginx", "--replicas", "5"}, true},
			{"absent", []string{"--name", "web", "--image", "nginx"}, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var got serviceCreateOptions
				serviceCreateHook = func(opts serviceCreateOptions) error {
					got = opts
					return nil
				}
				t.Cleanup(func() { serviceCreateHook = nil })

				cmd := newServiceCreateCommand()
				cmd.SetArgs(tc.args)
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)

				require.NoError(t, cmd.Execute())
				assert.Equal(t, tc.want, got.replicasSet, "replicasSet must follow Flags().Changed(\"replicas\")")
			})
		}
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

// TestServiceScaleHelpMentionsAutoscaleBounds pins the warning that `service
// scale` is validated against a stored policy's bounds: the command refuses a
// count the policy cannot hold, so --help must say so rather than leave the
// operator to discover it as an error after the fact.
func TestServiceScaleHelpMentionsAutoscaleBounds(t *testing.T) {
	cmd := newServiceScaleCommand()
	assert.Contains(t, cmd.Long, "autoscale policy", "service scale --help must mention the autoscale bounds check")
	assert.Contains(t, cmd.Long, "min/max", "the help must name the bounds it is checked against")
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

// serviceWithAutoscaleMode is serviceWithAutoscale with an explicit mode, so a
// test can express "already global" (replicas are meaningless there) or a
// replicated service sitting at a particular count.
func serviceWithAutoscaleMode(labels map[string]string, global bool, replicas uint64) *api.Service {
	svc := serviceWithAutoscale(labels)
	if global {
		svc.Spec.Mode = &api.ServiceSpec_Global{Global: &api.GlobalService{}}
	} else {
		svc.Spec.Mode = &api.ServiceSpec_Replicated{
			Replicated: &api.ReplicatedService{Replicas: replicas},
		}
	}
	return svc
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

// autoscaleTestPolicy is a complete, valid flag-form policy used by the tests
// below; the bounds it declares are min=2, max=10.
const autoscaleTestPolicy = "min=2,max=10,metric=cpu,target=70"

// TestAutoscaleGlobalModeRejected verifies the combination is refused in every
// direction: a global service runs one task per node and ignores the replica
// count, so a policy on it could never be honoured. Silently storing the labels
// (and `service inspect` then showing them as configured) would be a promise
// the manager loop cannot keep.
func TestAutoscaleGlobalModeRejected(t *testing.T) {
	existing := autoscaleLabels("2", "10", "cpu", "70", "1m0s", "5m0s")

	t.Run("create with --mode global and --autoscale is rejected", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 2, replicasSet: true,
			mode: modeGlobal, autoscale: autoscaleTestPolicy,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "global", "the error must name the mode")
		assert.Contains(t, err.Error(), "replicas", "the error must explain that global ignores replicas")
		assert.Contains(t, err.Error(), "autoscal", "the error must name autoscaling as what cannot apply")
	})

	t.Run("create with --mode global and a label-declared policy is rejected", func(t *testing.T) {
		// --label is the lower-level way to declare the same policy, so it must
		// not be a way around the rejection.
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", mode: modeGlobal,
			labels: []string{
				types.AutoscaleMinLabel + "=2",
				types.AutoscaleMaxLabel + "=10",
				types.AutoscaleMetricLabel + "=cpu",
				types.AutoscaleTargetLabel + "=70",
			},
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "global")
	})

	t.Run("create with --mode global and no policy is allowed", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", mode: modeGlobal,
		})

		require.NoError(t, err)
		assert.NotNil(t, spec.GetGlobal())
	})

	t.Run("update --mode global on a service with a policy is rejected", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscaleMode(existing, false, 2)}

		err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			mode: modeGlobal, modeSet: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "global")
		assert.Contains(t, err.Error(), "replicas")
		assert.Contains(t, err.Error(), "autoscal")
		assert.Zero(t, client.updateCalls, "a rejected combination must not be submitted")
	})

	t.Run("update --autoscale on a global service is rejected", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscaleMode(nil, true, 0)}

		err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			autoscale: autoscaleTestPolicy, autoscaleSet: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "global")
		assert.Contains(t, err.Error(), "autoscal")
		assert.Zero(t, client.updateCalls, "a rejected combination must not be submitted")
	})

	t.Run("clearing the policy on a global service stays possible", func(t *testing.T) {
		// An explicit empty value is a clear, not a declaration: it is the way
		// out of a service that already carries a policy it cannot honour.
		client := &fakeServiceUpdateClient{service: serviceWithAutoscaleMode(existing, true, 0)}

		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			autoscale: "", autoscaleSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		for _, key := range types.AutoscaleLabelKeys() {
			_, ok := client.updatedSpec.Annotations.Labels[key]
			assert.False(t, ok, "clearing must remove %s", key)
		}
	})
}

// TestAutoscaleLabelOnlyPolicyIsValidated verifies the label block is validated
// too: the six labels are the stored form, so a policy declared only through
// --label must be as complete and readable as one the flag builds. Otherwise
// `--label swarmcracker.autoscale.min=0` would store a policy the flag rejects.
func TestAutoscaleLabelOnlyPolicyIsValidated(t *testing.T) {
	complete := []string{
		types.AutoscaleMinLabel + "=2",
		types.AutoscaleMaxLabel + "=10",
		types.AutoscaleMetricLabel + "=cpu",
		types.AutoscaleTargetLabel + "=70",
	}

	t.Run("a complete label-declared policy is accepted", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 2, labels: complete,
		})

		require.NoError(t, err, "a complete policy declared purely through --label must keep working")
		policy, err := types.AutoscaleFromLabels(spec.Annotations.Labels)
		require.NoError(t, err)
		require.NotNil(t, policy)
		assert.Equal(t, 2, policy.Min)
		assert.Equal(t, 10, policy.Max)
		assert.Equal(t, types.AutoscaleMetricCPU, policy.Metric)
		assert.Equal(t, 70.0, policy.Target)
	})

	t.Run("an incomplete label-declared policy is rejected", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 2,
			labels: []string{types.AutoscaleMinLabel + "=2"},
		})

		require.Error(t, err, "a half-written policy must not be stored by --label alone")
		assert.Contains(t, err.Error(), "autoscal")
		assert.Contains(t, err.Error(), types.AutoscaleMaxLabel, "the error must name a missing label")
	})

	t.Run("an unreadable label-declared policy is rejected", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 2,
			labels: []string{
				types.AutoscaleMinLabel + "=0",
				types.AutoscaleMaxLabel + "=10",
				types.AutoscaleMetricLabel + "=cpu",
				types.AutoscaleTargetLabel + "=70",
			},
		})

		require.Error(t, err, "a value no policy could hold must not be stored by --label alone")
		assert.Contains(t, err.Error(), types.AutoscaleMinLabel)
		assert.Contains(t, err.Error(), ">= 1")
	})

	// The flag grammar rejects an unknown key, so the label form must too. A
	// misspelled key used to be skipped, which stored a service that looked
	// configured and silently never scaled.
	t.Run("a misspelled label key is rejected", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 2,
			labels: []string{
				types.AutoscaleMinLabel + "=2",
				types.AutoscaleMaxLabel + "=10",
				types.AutoscaleMetricLabel + "=cpu",
				types.AutoscaleTargetLabel + "=70",
				types.AutoscaleLabelPrefix + "minn=2",
			},
		})

		require.Error(t, err, "a typo key must be refused, not stored as a policy that never applies")
		assert.Contains(t, err.Error(), types.AutoscaleLabelPrefix+"minn", "the error must name the offending key")
		assert.Contains(t, err.Error(), "unknown autoscale label")
	})
}

// TestAutoscaleReplicasBounds verifies an explicit --replicas is reconciled
// with the policy's [min,max]: storing a service outside its own bounds would
// leave the manager loop either to "correct" the count on its first pass or to
// never reach min. --replicas 0 is the deliberate exception (it means
// "paused"), and a create that never mentioned replicas is not second-guessed.
func TestAutoscaleReplicasBounds(t *testing.T) {
	existing := autoscaleLabels("2", "10", "cpu", "70", "1m0s", "5m0s")

	t.Run("create above max is rejected", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 50, replicasSet: true,
			autoscale: autoscaleTestPolicy,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "replicas 50", "the error must name the value")
		assert.Contains(t, err.Error(), "min=2, max=10", "the error must name the bounds")
		assert.Contains(t, err.Error(), "--autoscale", "the error must point at the policy")
	})

	t.Run("create below min is rejected", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1, replicasSet: true,
			autoscale: autoscaleTestPolicy,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "replicas 1")
		assert.Contains(t, err.Error(), "min=2, max=10")
	})

	t.Run("create within the bounds is accepted", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 5, replicasSet: true,
			autoscale: autoscaleTestPolicy,
		})

		require.NoError(t, err)
		require.NotNil(t, spec.GetReplicated())
		assert.Equal(t, uint64(5), spec.GetReplicated().Replicas)
	})

	t.Run("create with --replicas 0 is accepted as paused", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 0, replicasSet: true,
			autoscale: autoscaleTestPolicy,
		})

		require.NoError(t, err, "--replicas 0 means paused, not scale to the floor")
		require.NotNil(t, spec.GetReplicated())
		assert.Equal(t, uint64(0), spec.GetReplicated().Replicas)
	})

	t.Run("create with a label-declared policy is checked too", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 50, replicasSet: true,
			labels: []string{
				types.AutoscaleMinLabel + "=2",
				types.AutoscaleMaxLabel + "=10",
				types.AutoscaleMetricLabel + "=cpu",
				types.AutoscaleTargetLabel + "=70",
			},
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "replicas 50")
	})

	t.Run("create without an explicit --replicas is not bounded", func(t *testing.T) {
		// The flag defaults to 1: rejecting a create that never mentioned
		// replicas would be surprising, and the policy's min still wins on the
		// manager loop's first pass.
		spec, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1, autoscale: autoscaleTestPolicy,
		})

		require.NoError(t, err)
		assert.Equal(t, uint64(1), spec.GetReplicated().Replicas)
	})

	t.Run("update above max is rejected", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscaleMode(existing, false, 5)}

		err := updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			replicas: 50, replicasSet: true,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "replicas 50")
		assert.Contains(t, err.Error(), "min=2, max=10")
		assert.NotContains(t, err.Error(), "--replicas",
			"the create and update paths share the neutral wording")
		assert.Zero(t, client.updateCalls, "an out-of-bounds count must not be stored")
	})

	t.Run("update within the bounds is accepted", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscaleMode(existing, false, 5)}

		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			replicas: 8, replicasSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, uint64(8), client.updatedSpec.GetReplicated().Replicas)
	})

	t.Run("update with --replicas 0 is accepted as paused", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscaleMode(existing, false, 5)}

		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			replicas: 0, replicasSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, uint64(0), client.updatedSpec.GetReplicated().Replicas)
		assert.Equal(t, existing, client.updatedSpec.Annotations.Labels,
			"a pause must not touch the policy")
	})
}

// TestAutoscaleServiceScaleBounds covers `service scale`, the entry point an
// operator reaches for once a policy already exists. It shares the update path's
// bounds check but has no --replicas flag, so the refusal must name the count
// and the bounds and point at the policy instead of at a flag this command does
// not have. Scale to 0 stays the pause; a count within the bounds is applied.
func TestAutoscaleServiceScaleBounds(t *testing.T) {
	existing := autoscaleLabels("2", "10", "cpu", "70", "1m0s", "5m0s")

	t.Run("scale within the bounds is applied", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscaleMode(existing, false, 4)}

		require.NoError(t, scaleServiceWithClient(context.Background(), client, "web", 5))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, uint64(5), client.updatedSpec.GetReplicated().Replicas)
		assert.Equal(t, existing, client.updatedSpec.Annotations.Labels,
			"a scale must not touch the policy")
	})

	t.Run("scale below min is refused without naming --replicas", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscaleMode(existing, false, 4)}

		err := scaleServiceWithClient(context.Background(), client, "web", 1)

		require.Error(t, err, "an explicit count below min contradicts the policy")
		assert.Contains(t, err.Error(), "replicas 1", "the error must name the count")
		assert.Contains(t, err.Error(), "min=2, max=10", "the error must name the bounds")
		assert.Contains(t, err.Error(), "--autoscale", "the error must point at the policy")
		assert.NotContains(t, err.Error(), "--replicas",
			"service scale has no --replicas flag, so the refusal must not name one")
		assert.Zero(t, client.updateCalls, "a refused scale must not be stored")
	})

	t.Run("scale to 0 still pauses", func(t *testing.T) {
		client := &fakeServiceUpdateClient{service: serviceWithAutoscaleMode(existing, false, 4)}

		require.NoError(t, scaleServiceWithClient(context.Background(), client, "web", 0))

		require.Equal(t, 1, client.updateCalls)
		assert.Equal(t, uint64(0), client.updatedSpec.GetReplicated().Replicas)
		assert.Equal(t, existing, client.updatedSpec.Annotations.Labels,
			"a pause must not touch the policy")
	})
}

// TestAutoscaleWhitespaceBehavesLikeEmpty verifies a whitespace-only
// --autoscale is the same request as "": on create it declares no policy, on
// update it clears the declared one. A value of " " must not reach the parser,
// which would reject it as an empty policy and make the two spellings differ.
func TestAutoscaleWhitespaceBehavesLikeEmpty(t *testing.T) {
	t.Run("create adds no label", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1, autoscale: "  \t ",
		})

		require.NoError(t, err)
		for _, key := range types.AutoscaleLabelKeys() {
			_, ok := spec.Annotations.Labels[key]
			assert.False(t, ok, "a whitespace-only --autoscale on create must not add %s", key)
		}
	})

	t.Run("update clears the policy", func(t *testing.T) {
		client := &fakeServiceUpdateClient{
			service: serviceWithAutoscaleMode(autoscaleLabels("2", "10", "cpu", "70", "1m0s", "5m0s"), false, 5),
		}

		require.NoError(t, updateServiceWithClient(context.Background(), client, "web", serviceUpdateOptions{
			autoscale: "   ", autoscaleSet: true,
		}))

		require.Equal(t, 1, client.updateCalls)
		for _, key := range types.AutoscaleLabelKeys() {
			_, ok := client.updatedSpec.Annotations.Labels[key]
			assert.False(t, ok, "a whitespace-only --autoscale must clear %s", key)
		}
	})
}
