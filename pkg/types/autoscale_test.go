package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutoscaleLabelConstants pins the label keys an operator writes on a
// service. They are part of the stored contract: a later manager loop reads
// exactly these keys, so renaming one silently disables every existing policy.
func TestAutoscaleLabelConstants(t *testing.T) {
	assert.Equal(t, "swarmcracker.autoscale.min", AutoscaleMinLabel)
	assert.Equal(t, "swarmcracker.autoscale.max", AutoscaleMaxLabel)
	assert.Equal(t, "swarmcracker.autoscale.metric", AutoscaleMetricLabel)
	assert.Equal(t, "swarmcracker.autoscale.target", AutoscaleTargetLabel)
	assert.Equal(t, "swarmcracker.autoscale.cooldown_up", AutoscaleCooldownUpLabel)
	assert.Equal(t, "swarmcracker.autoscale.cooldown_down", AutoscaleCooldownDownLabel)

	assert.ElementsMatch(t, []string{
		AutoscaleMinLabel, AutoscaleMaxLabel, AutoscaleMetricLabel,
		AutoscaleTargetLabel, AutoscaleCooldownUpLabel, AutoscaleCooldownDownLabel,
	}, AutoscaleLabelKeys(),
		"AutoscaleLabelKeys must list every label a policy is stored as, so a clear removes all of them")
}

// TestAutoscaleParseFlag is the flag-grammar table: a valid policy in any key
// order, and one rejection per way the value can be wrong. Every error must
// name the offending key and the valid form, because the value is a single
// comma-separated string where a silent fallback is impossible to spot.
func TestAutoscaleParseFlag(t *testing.T) {
	valid := []struct {
		name string
		in   string
		want AutoscalePolicy
	}{
		{
			name: "canonical",
			in:   "min=2,max=10,metric=cpu,target=70",
			want: AutoscalePolicy{Min: 2, Max: 10, Metric: "cpu", Target: 70, CooldownUp: time.Minute, CooldownDown: 5 * time.Minute},
		},
		{
			name: "any key order",
			in:   "target=70,metric=cpu,max=10,min=2",
			want: AutoscalePolicy{Min: 2, Max: 10, Metric: "cpu", Target: 70, CooldownUp: time.Minute, CooldownDown: 5 * time.Minute},
		},
		{
			name: "explicit cooldowns",
			in:   "min=1,max=4,metric=cpu,target=85,cooldown-up=30s,cooldown-down=10m",
			want: AutoscalePolicy{Min: 1, Max: 4, Metric: "cpu", Target: 85, CooldownUp: 30 * time.Second, CooldownDown: 10 * time.Minute},
		},
		{
			name: "spaces around components",
			in:   "min = 2, max = 10 , metric = cpu , target = 70",
			want: AutoscalePolicy{Min: 2, Max: 10, Metric: "cpu", Target: 70, CooldownUp: time.Minute, CooldownDown: 5 * time.Minute},
		},
		{
			name: "fractional target and single replica range",
			in:   "min=1,max=1,metric=cpu,target=72.5",
			want: AutoscalePolicy{Min: 1, Max: 1, Metric: "cpu", Target: 72.5, CooldownUp: time.Minute, CooldownDown: 5 * time.Minute},
		},
		{
			name: "upper target bound",
			in:   "min=1,max=2,metric=cpu,target=100",
			want: AutoscalePolicy{Min: 1, Max: 2, Metric: "cpu", Target: 100, CooldownUp: time.Minute, CooldownDown: 5 * time.Minute},
		},
		{
			name: "zero cooldown is allowed",
			in:   "min=1,max=2,metric=cpu,target=50,cooldown-up=0s",
			want: AutoscalePolicy{Min: 1, Max: 2, Metric: "cpu", Target: 50, CooldownUp: 0, CooldownDown: 5 * time.Minute},
		},
	}

	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			got, err := ParseAutoscaleFlag(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	invalid := []struct {
		name string
		in   string
		// want are substrings the error must carry: the offending key, and the
		// valid form or bound the value violated.
		want []string
	}{
		{name: "empty", in: "", want: []string{"empty autoscale policy", "min=<n>", "target=<percent>"}},
		{name: "whitespace only", in: "   ", want: []string{"empty autoscale policy"}},
		{name: "missing min", in: "max=10,metric=cpu,target=70", want: []string{"missing min", "min=<n>"}},
		{
			name: "missing target",
			in:   "min=2,max=10,metric=cpu",
			want: []string{"missing target", "target=<percent>"},
		},
		{
			name: "missing several names them all",
			in:   "min=2",
			want: []string{"missing max", "metric", "target"},
		},
		{name: "empty required value counts as missing", in: "min=,max=10,metric=cpu,target=70", want: []string{"missing min"}},
		{name: "min below one", in: "min=0,max=10,metric=cpu,target=70", want: []string{"invalid autoscale min", ">= 1"}},
		{name: "min not an integer", in: "min=1.5,max=10,metric=cpu,target=70", want: []string{"invalid autoscale min", "integer"}},
		{name: "max not an integer", in: "min=1,max=ten,metric=cpu,target=70", want: []string{"invalid autoscale max", "integer"}},
		{
			name: "max below min",
			in:   "min=10,max=2,metric=cpu,target=70",
			want: []string{"invalid autoscale max", "greater than or equal to min"},
		},
		{
			name: "target zero",
			in:   "min=2,max=10,metric=cpu,target=0",
			want: []string{"invalid autoscale target", "greater than 0 and at most 100"},
		},
		{
			name: "target above one hundred",
			in:   "min=2,max=10,metric=cpu,target=101",
			want: []string{"invalid autoscale target", "greater than 0 and at most 100"},
		},
		{
			name: "target is not a number",
			in:   "min=2,max=10,metric=cpu,target=high",
			want: []string{"invalid autoscale target", "greater than 0 and at most 100"},
		},
		{
			// ParseFloat accepts "NaN", and every comparison with NaN is false,
			// so the range check alone would let it through and store a target
			// the manager loop could never compare against a sample.
			name: "target NaN",
			in:   "min=2,max=10,metric=cpu,target=NaN",
			want: []string{"invalid autoscale target", "greater than 0 and at most 100"},
		},
		{
			name: "unsupported metric",
			in:   "min=2,max=10,metric=memory,target=70",
			want: []string{`unsupported autoscale metric "memory" (supported: cpu)`},
		},
		{
			name: "empty metric",
			in:   "min=2,max=10,metric=,target=70",
			want: []string{"missing metric", "metric=cpu"},
		},
		{
			name: "bad cooldown duration",
			in:   "min=2,max=10,metric=cpu,target=70,cooldown-up=soon",
			want: []string{"invalid autoscale cooldown-up", "duration", "60s"},
		},
		{
			name: "negative cooldown",
			in:   "min=2,max=10,metric=cpu,target=70,cooldown-down=-5s",
			want: []string{"invalid autoscale cooldown-down", "negative"},
		},
		{
			name: "unknown key",
			in:   "min=2,max=10,metric=cpu,target=70,cooldown=5m",
			want: []string{`unknown autoscale key "cooldown"`, "cooldown-up", "cooldown-down"},
		},
		{
			// The key has to be recognised before it can be a duplicate, or a
			// repeated typo is reported as "duplicate" and sends the operator
			// looking for a key that does not exist.
			name: "repeated unknown key stays unknown",
			in:   "min=2,max=10,metric=cpu,target=70,cooldown=5m,cooldown=6m",
			want: []string{`unknown autoscale key "cooldown"`, "cooldown-up", "cooldown-down"},
		},
		{
			name: "component without a value",
			in:   "min=2,max=10,metric=cpu,target=70,oops",
			want: []string{`invalid autoscale component "oops"`, "key=value"},
		},
		{
			// Like every other flag-form error, the duplicate reports the valid
			// form so the operator can see what a well-formed value looks like.
			name: "duplicate key",
			in:   "min=2,min=3,max=10,metric=cpu,target=70",
			want: []string{`duplicate autoscale key "min"`, "at most once", "min=<n>", "target=<percent>"},
		},
	}

	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			got, err := ParseAutoscaleFlag(tc.in)
			require.Error(t, err, "a malformed policy must never parse")
			assert.Equal(t, AutoscalePolicy{}, got, "a rejected policy must not be usable")
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want, "the error must name what is wrong and the valid form")
			}
		})
	}
}

// TestAutoscalePolicyLabels verifies the stored form round-trips: what
// Labels produces is exactly what AutoscaleFromLabels reads back, with no
// lossy formatting (percent, durations) between the two.
func TestAutoscalePolicyLabels(t *testing.T) {
	tests := []struct {
		name string
		in   AutoscalePolicy
		want map[string]string
	}{
		{
			name: "canonical",
			in:   AutoscalePolicy{Min: 2, Max: 10, Metric: "cpu", Target: 70, CooldownUp: time.Minute, CooldownDown: 5 * time.Minute},
			want: map[string]string{
				AutoscaleMinLabel:          "2",
				AutoscaleMaxLabel:          "10",
				AutoscaleMetricLabel:       "cpu",
				AutoscaleTargetLabel:       "70",
				AutoscaleCooldownUpLabel:   "1m0s",
				AutoscaleCooldownDownLabel: "5m0s",
			},
		},
		{
			name: "fractional target",
			in:   AutoscalePolicy{Min: 1, Max: 3, Metric: "cpu", Target: 72.5, CooldownUp: 30 * time.Second, CooldownDown: 0},
			want: map[string]string{
				AutoscaleMinLabel:          "1",
				AutoscaleMaxLabel:          "3",
				AutoscaleMetricLabel:       "cpu",
				AutoscaleTargetLabel:       "72.5",
				AutoscaleCooldownUpLabel:   "30s",
				AutoscaleCooldownDownLabel: "0s",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.in.Labels())

			back, err := AutoscaleFromLabels(tc.in.Labels())
			require.NoError(t, err)
			require.NotNil(t, back)
			assert.Equal(t, tc.in, *back, "the label round-trip must preserve the policy")
			assert.Equal(t,
				"min="+tc.want[AutoscaleMinLabel]+",max="+tc.want[AutoscaleMaxLabel]+
					",metric=cpu,target="+tc.want[AutoscaleTargetLabel]+
					",cooldown-up="+tc.want[AutoscaleCooldownUpLabel]+
					",cooldown-down="+tc.want[AutoscaleCooldownDownLabel],
				tc.in.String(), "String must render the --autoscale grammar")
		})
	}
}

// TestAutoscaleFromLabels covers what a service's labels mean. A service with
// no autoscale labels has no policy; a complete set is the policy; and a
// half-written set is an error naming the missing keys — a partially
// configured policy must never quietly do nothing.
func TestAutoscaleFromLabels(t *testing.T) {
	t.Run("no labels at all", func(t *testing.T) {
		got, err := AutoscaleFromLabels(nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("unrelated labels only", func(t *testing.T) {
		got, err := AutoscaleFromLabels(map[string]string{
			"com.example.team": "platform",
			DiskSizeLabel:      "10G",
		})
		require.NoError(t, err)
		assert.Nil(t, got, "a service without autoscale labels has no policy")
	})

	t.Run("complete labels", func(t *testing.T) {
		got, err := AutoscaleFromLabels(map[string]string{
			AutoscaleMinLabel:          "2",
			AutoscaleMaxLabel:          "10",
			AutoscaleMetricLabel:       "cpu",
			AutoscaleTargetLabel:       "70",
			AutoscaleCooldownUpLabel:   "30s",
			AutoscaleCooldownDownLabel: "2m",
		})
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, AutoscalePolicy{Min: 2, Max: 10, Metric: "cpu", Target: 70, CooldownUp: 30 * time.Second, CooldownDown: 2 * time.Minute}, *got)
	})

	t.Run("cooldowns are optional and default", func(t *testing.T) {
		got, err := AutoscaleFromLabels(map[string]string{
			AutoscaleMinLabel:    "1",
			AutoscaleMaxLabel:    "3",
			AutoscaleMetricLabel: "cpu",
			AutoscaleTargetLabel: "60",
		})
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, time.Minute, got.CooldownUp)
		assert.Equal(t, 5*time.Minute, got.CooldownDown)
	})

	// Each of these is a half-written policy: at least one autoscale label is
	// present, so the operator meant to declare one, but the required set is
	// incomplete. Silently returning nil would leave autoscaling off while
	// `service inspect` showed labels that look configured.
	incomplete := []struct {
		name   string
		labels map[string]string
		miss   []string
	}{
		{
			name: "missing target",
			labels: map[string]string{
				AutoscaleMinLabel:    "2",
				AutoscaleMaxLabel:    "10",
				AutoscaleMetricLabel: "cpu",
			},
			miss: []string{AutoscaleTargetLabel},
		},
		{
			name:   "only a cooldown",
			labels: map[string]string{AutoscaleCooldownUpLabel: "30s"},
			miss:   []string{AutoscaleMinLabel, AutoscaleMaxLabel, AutoscaleMetricLabel, AutoscaleTargetLabel},
		},
		{
			name: "empty values are missing",
			labels: map[string]string{
				AutoscaleMinLabel:    "",
				AutoscaleMaxLabel:    "10",
				AutoscaleMetricLabel: "cpu",
				AutoscaleTargetLabel: "70",
			},
			miss: []string{AutoscaleMinLabel},
		},
	}

	for _, tc := range incomplete {
		t.Run("incomplete/"+tc.name, func(t *testing.T) {
			got, err := AutoscaleFromLabels(tc.labels)
			require.Error(t, err, "a half-configured policy must not be ignored")
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), "incomplete autoscale policy")
			for _, key := range tc.miss {
				assert.Contains(t, err.Error(), key, "the error must name every missing key")
			}
		})
	}

	// A complete set can still hold values no autoscaler can honour; reading it
	// back must fail rather than hand the loop a policy it cannot apply.
	invalid := []struct {
		name   string
		labels map[string]string
		want   []string
	}{
		{
			name: "min is not a number",
			labels: map[string]string{
				AutoscaleMinLabel:    "two",
				AutoscaleMaxLabel:    "10",
				AutoscaleMetricLabel: "cpu",
				AutoscaleTargetLabel: "70",
			},
			want: []string{AutoscaleMinLabel, "integer"},
		},
		{
			name: "max below min",
			labels: map[string]string{
				AutoscaleMinLabel:    "10",
				AutoscaleMaxLabel:    "2",
				AutoscaleMetricLabel: "cpu",
				AutoscaleTargetLabel: "70",
			},
			want: []string{AutoscaleMaxLabel, "greater than or equal to min"},
		},
		{
			name: "unsupported metric",
			labels: map[string]string{
				AutoscaleMinLabel:    "1",
				AutoscaleMaxLabel:    "2",
				AutoscaleMetricLabel: "memory",
				AutoscaleTargetLabel: "70",
			},
			want: []string{AutoscaleMetricLabel, `unsupported autoscale metric "memory" (supported: cpu)`},
		},
		{
			name: "target out of range",
			labels: map[string]string{
				AutoscaleMinLabel:    "1",
				AutoscaleMaxLabel:    "2",
				AutoscaleMetricLabel: "cpu",
				AutoscaleTargetLabel: "101",
			},
			want: []string{AutoscaleTargetLabel, "greater than 0 and at most 100"},
		},
		{
			name: "bad cooldown",
			labels: map[string]string{
				AutoscaleMinLabel:          "1",
				AutoscaleMaxLabel:          "2",
				AutoscaleMetricLabel:       "cpu",
				AutoscaleTargetLabel:       "70",
				AutoscaleCooldownDownLabel: "5 minutes",
			},
			want: []string{AutoscaleCooldownDownLabel, "duration"},
		},
	}

	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			got, err := AutoscaleFromLabels(tc.labels)
			require.Error(t, err)
			assert.Nil(t, got)
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestAutoscaleFromLabelsUnknownKey closes the gap between the two ways a
// policy is declared: the flag grammar rejects an unknown key, so the labels
// must too. A typo like "minn" otherwise leaves a service that looks configured
// in `service inspect` and never scales, with nothing said about why.
func TestAutoscaleFromLabelsUnknownKey(t *testing.T) {
	// A typo key alongside a complete, otherwise-valid policy: this is the shape
	// an operator produces by mistyping one label of a working policy, and it is
	// the one that must not be read as "configured and fine".
	t.Run("typo key beside a complete policy", func(t *testing.T) {
		got, err := AutoscaleFromLabels(map[string]string{
			AutoscaleMinLabel:             "2",
			AutoscaleMaxLabel:             "10",
			AutoscaleMetricLabel:          "cpu",
			AutoscaleTargetLabel:          "70",
			"swarmcracker.autoscale.minn": "2",
		})
		require.Error(t, err, "an unknown autoscale label must not be ignored")
		assert.Nil(t, got)
		assert.Contains(t, err.Error(), "swarmcracker.autoscale.minn",
			"the error must name the offending key, so the typo is findable")
		assert.Contains(t, err.Error(), "unknown autoscale label")
		for _, key := range AutoscaleLabelKeys() {
			assert.Contains(t, err.Error(), key, "the error must name the valid label set")
		}
	})

	// A typo key on its own declares nothing the loop can read. Returning nil
	// would report "no policy" for a service that plainly means to have one.
	t.Run("typo key alone", func(t *testing.T) {
		got, err := AutoscaleFromLabels(map[string]string{
			"swarmcracker.autoscale.targett": "70",
		})
		require.Error(t, err)
		assert.Nil(t, got)
		assert.Contains(t, err.Error(), "swarmcracker.autoscale.targett")
	})

	// Every unknown key is named at once, so a policy with two typos is one
	// round trip to fix rather than two.
	t.Run("every unknown key is named", func(t *testing.T) {
		got, err := AutoscaleFromLabels(map[string]string{
			"swarmcracker.autoscale.minn": "2",
			"swarmcracker.autoscale.maxx": "10",
		})
		require.Error(t, err)
		assert.Nil(t, got)
		assert.Contains(t, err.Error(), "swarmcracker.autoscale.minn")
		assert.Contains(t, err.Error(), "swarmcracker.autoscale.maxx")
	})

	// The control cases: the six known keys stay accepted, and a service with no
	// autoscale labels at all is still "no policy" rather than an error.
	t.Run("every known key is accepted", func(t *testing.T) {
		got, err := AutoscaleFromLabels(map[string]string{
			AutoscaleMinLabel:          "1",
			AutoscaleMaxLabel:          "3",
			AutoscaleMetricLabel:       "cpu",
			AutoscaleTargetLabel:       "60",
			AutoscaleCooldownUpLabel:   "30s",
			AutoscaleCooldownDownLabel: "2m",
			"com.example.team":         "platform",
		})
		require.NoError(t, err)
		require.NotNil(t, got)
	})

	t.Run("no autoscale labels at all", func(t *testing.T) {
		got, err := AutoscaleFromLabels(map[string]string{"com.example.team": "platform"})
		require.NoError(t, err)
		assert.Nil(t, got, "a service without autoscale labels has no policy")
	})
}
