package autoscaler

import (
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// t0 is the clock every table case reasons from. Decide never reads the wall
// clock, so a fixed instant makes each expectation reproducible.
var t0 = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

// cpuPolicy is the policy most cases share: keep 2 to 10 replicas at 70% of one
// vCPU, using the shipped cooldown defaults (60s up, 300s down). With target=70,
// "hot" means above 0.77 cores and "cold" means below 0.63 cores.
func cpuPolicy() types.AutoscalePolicy {
	return types.AutoscalePolicy{
		Min:          2,
		Max:          10,
		Metric:       types.AutoscaleMetricCPU,
		Target:       70,
		CooldownUp:   types.DefaultAutoscaleCooldownUp,
		CooldownDown: types.DefaultAutoscaleCooldownDown,
	}
}

// policyBounds is cpuPolicy with different replica bounds.
func policyBounds(min, max int) types.AutoscalePolicy {
	p := cpuPolicy()
	p.Min, p.Max = min, max
	return p
}

// policyCooldowns is cpuPolicy with different cooldowns. A zero duration means
// "no wait in this direction".
func policyCooldowns(up, down time.Duration) types.AutoscalePolicy {
	p := cpuPolicy()
	p.CooldownUp, p.CooldownDown = up, down
	return p
}

// policyTarget is cpuPolicy with a different target percentage.
func policyTarget(target float64) types.AutoscalePolicy {
	p := cpuPolicy()
	p.Target = target
	return p
}

// settled is a state whose last change happened long enough ago that no cooldown
// is running, with the given below-target counter. It is used both as the state a
// row starts from and as the state a hold is expected to return: a hold must move
// neither timestamp.
func settled(belowTarget int) State {
	return State{
		LastChange:    t0.Add(-10 * time.Minute),
		LastDirection: DirectionUp,
		BelowTarget:   belowTarget,
	}
}

// TestDecide is the decision table: one row per rule the engine promises, with
// the returned State asserted alongside the Decision because the counter and the
// cooldown timestamps are what make the next tick correct. Every reason string is
// pinned verbatim: it is printed in the manager's log, so a wording change is a
// behaviour change for whoever reads the log.
func TestDecide(t *testing.T) {
	// cold and hot are the readings several rows share; their means are 0.43
	// and 0.93 cores, i.e. below 0.63 and above 0.77.
	cold := []float64{0.40, 0.42, 0.44, 0.46}
	hot := []float64{0.95, 0.90, 0.94}
	// idle averages 0.69 cores: inside the band, neither hot nor cold.
	idle := []float64{0.70, 0.66, 0.68, 0.72}

	cases := []struct {
		name      string
		policy    types.AutoscalePolicy
		obs       Observation
		state     State
		want      Decision
		wantState State
	}{
		{
			// The common case: a service inside its band must be left alone,
			// and any stale "below target" evidence must be forgotten.
			name:   "idle holds and resets the below-target counter",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 4, Cores: idle, SampledAt: t0.Add(-15 * time.Second)},
			state:  settled(1),
			want: Decision{
				Replicas:  4,
				Direction: DirectionNone,
				Reason:    "holding: 0.69 cores is within the band around target 0.70",
			},
			wantState: settled(0),
		},
		{
			// Scale-up reacts on the first sample: load spikes are short.
			name:   "hot scales up by one on the first sample",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 3, Cores: hot},
			state:  settled(0),
			want: Decision{
				Replicas:  4,
				Direction: DirectionUp,
				Reason:    "scaled up: 0.93 cores over target 0.70",
			},
			wantState: State{LastChange: t0, LastDirection: DirectionUp},
		},
		{
			// Hysteresis: one cold sample is not enough, it only arms the
			// counter. A freshly booted replica reads low while it warms up.
			name:   "one cold sample only counts",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 4, Cores: cold},
			state:  settled(0),
			want: Decision{
				Replicas:  4,
				Direction: DirectionNone,
				Reason:    "holding: 0.43 cores is under target 0.70 (1 of 2 samples)",
			},
			wantState: settled(1),
		},
		{
			// The second consecutive cold sample scales down by one, and the
			// counter restarts from zero.
			name:   "two consecutive cold samples scale down by one",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 4, Cores: cold},
			state:  settled(1),
			want: Decision{
				Replicas:  3,
				Direction: DirectionDown,
				Reason:    "scaled down: 0.43 cores under target 0.70 for 2 samples",
			},
			wantState: State{LastChange: t0, LastDirection: DirectionDown},
		},
		{
			name:   "hot inside the scale-up cooldown holds",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 3, Cores: hot},
			state: State{
				LastChange:    t0.Add(-30 * time.Second),
				LastDirection: DirectionUp,
			},
			want: Decision{
				Replicas:  3,
				Direction: DirectionNone,
				Reason:    "waiting out the 60s scale-up cooldown",
			},
			wantState: State{
				LastChange:    t0.Add(-30 * time.Second),
				LastDirection: DirectionUp,
			},
		},
		{
			// Hot but already at the ceiling: the clamp wins over the load, and
			// the reason says which bound stopped it.
			name:   "hot at the maximum holds",
			policy: cpuPolicy(),
			obs: Observation{
				Replicas: 10,
				Cores:    []float64{1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0},
			},
			state: settled(0),
			want: Decision{
				Replicas:  10,
				Direction: DirectionNone,
				Reason:    "holding: 10 is already the maximum",
			},
			wantState: settled(0),
		},
		{
			// A cold sample during the down cooldown still counts (the service
			// really is idle), but nothing moves until the cooldown expires.
			name:   "cold inside the scale-down cooldown counts but holds",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 4, Cores: cold},
			state: State{
				LastChange:    t0.Add(-60 * time.Second),
				LastDirection: DirectionDown,
				BelowTarget:   1,
			},
			want: Decision{
				Replicas:  4,
				Direction: DirectionNone,
				Reason:    "waiting out the 300s scale-down cooldown",
			},
			wantState: State{
				LastChange:    t0.Add(-60 * time.Second),
				LastDirection: DirectionDown,
				BelowTarget:   2,
			},
		},
		{
			// Flip-flop guard: a recent scale-down blocks the scale-up the load
			// would otherwise ask for.
			name:   "a recent scale-down blocks scaling up",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 4, Cores: hot},
			state: State{
				LastChange:    t0.Add(-60 * time.Second),
				LastDirection: DirectionDown,
				BelowTarget:   0,
			},
			want: Decision{
				Replicas:  4,
				Direction: DirectionNone,
				Reason:    "waiting out the 300s scale-down cooldown",
			},
			wantState: State{
				LastChange:    t0.Add(-60 * time.Second),
				LastDirection: DirectionDown,
			},
		},
		{
			// ... and the mirror image: a recent scale-up blocks a scale-down
			// even when two cold samples have already been seen.
			name:   "a recent scale-up blocks scaling down",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 4, Cores: cold},
			state: State{
				LastChange:    t0.Add(-30 * time.Second),
				LastDirection: DirectionUp,
				BelowTarget:   1,
			},
			want: Decision{
				Replicas:  4,
				Direction: DirectionNone,
				Reason:    "waiting out the 60s scale-up cooldown",
			},
			wantState: State{
				LastChange:    t0.Add(-30 * time.Second),
				LastDirection: DirectionUp,
				BelowTarget:   2,
			},
		},
		{
			// Clamping at the floor: the hysteresis is satisfied but there is
			// nowhere to go, and the reason names the floor.
			name:   "cold at the minimum holds",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 2, Cores: cold},
			state:  settled(1),
			want: Decision{
				Replicas:  2,
				Direction: DirectionNone,
				Reason:    "holding: 2 is already the minimum",
			},
			wantState: settled(2),
		},
		{
			// A count above the ceiling is not a load decision: it is corrected
			// immediately, even though this sample is hot, so the policy bounds
			// are never left behind by a policy update.
			name:   "a count above the maximum is clamped down",
			policy: cpuPolicy(),
			obs: Observation{
				Replicas: 12,
				Cores:    []float64{1.2, 1.2, 1.2, 1.2, 1.2, 1.2, 1.2, 1.2, 1.2, 1.2, 1.2, 1.2},
			},
			state: settled(1),
			want: Decision{
				Replicas:  10,
				Direction: DirectionDown,
				Reason:    "clamped down to the maximum 10 (was 12)",
			},
			wantState: State{LastChange: t0, LastDirection: DirectionDown},
		},
		{
			// The mirror image: a count below the floor is raised to the floor,
			// whatever the sample says. This is how a service created with the
			// default `--replicas 1` reaches `min=2` on the first pass.
			name:   "a count below the minimum is clamped up",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 1, Cores: idle},
			state:  settled(0),
			want: Decision{
				Replicas:  2,
				Direction: DirectionUp,
				Reason:    "clamped up to the minimum 2 (was 1)",
			},
			wantState: State{LastChange: t0, LastDirection: DirectionUp},
		},
		{
			name:   "no readings is no data",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 4},
			state:  settled(2),
			want: Decision{
				Replicas:  4,
				Direction: DirectionNone,
				Reason:    "no replicas reporting",
			},
			wantState: settled(0),
		},
		{
			name:   "a NaN reading is no data",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 2, Cores: []float64{0.70, math.NaN()}},
			state:  settled(1),
			want: Decision{
				Replicas:  2,
				Direction: DirectionNone,
				Reason:    "no data: ignoring an invalid cpu reading",
			},
			wantState: settled(0),
		},
		{
			name:   "a negative reading is no data",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 2, Cores: []float64{0.70, -0.40}},
			state:  settled(1),
			want: Decision{
				Replicas:  2,
				Direction: DirectionNone,
				Reason:    "no data: ignoring an invalid cpu reading",
			},
			wantState: settled(0),
		},
		{
			name:   "an infinite reading is no data",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 2, Cores: []float64{math.Inf(1), 0.70}},
			state:  settled(0),
			want: Decision{
				Replicas:  2,
				Direction: DirectionNone,
				Reason:    "no data: ignoring an invalid cpu reading",
			},
			wantState: settled(0),
		},
		{
			// The bands are strict: exactly target*1.1 is not "over" it.
			name:   "exactly the upper band edge is not hot",
			policy: cpuPolicy(),
			obs: Observation{
				Replicas: 2,
				Cores:    []float64{0.70 * 1.10, 0.70 * 1.10},
			},
			state: settled(0),
			want: Decision{
				Replicas:  2,
				Direction: DirectionNone,
				Reason:    "holding: 0.77 cores is within the band around target 0.70",
			},
			wantState: settled(0),
		},
		{
			// ... and exactly target*0.9 is not "under" it, so a stale cold
			// counter must be cleared rather than completed.
			name:   "exactly the lower band edge is not cold",
			policy: cpuPolicy(),
			obs: Observation{
				Replicas: 2,
				Cores:    []float64{0.70 * 0.90, 0.70 * 0.90},
			},
			state: settled(1),
			want: Decision{
				Replicas:  2,
				Direction: DirectionNone,
				Reason:    "holding: 0.63 cores is within the band around target 0.70",
			},
			wantState: settled(0),
		},
		{
			// A zero cooldown is a deliberate "no wait in this direction", not a
			// missing value.
			name:   "zero cooldowns never wait",
			policy: policyCooldowns(0, 0),
			obs:    Observation{Replicas: 3, Cores: hot},
			state: State{
				LastChange:    t0,
				LastDirection: DirectionUp,
			},
			want: Decision{
				Replicas:  4,
				Direction: DirectionUp,
				Reason:    "scaled up: 0.93 cores over target 0.70",
			},
			wantState: State{LastChange: t0, LastDirection: DirectionUp},
		},
		{
			// The zero LastChange is "never changed", so it cannot be read as a
			// cooldown that started at the beginning of time.
			name:   "a missing LastChange never waits",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 3, Cores: hot},
			state: State{
				LastDirection: DirectionUp,
			},
			want: Decision{
				Replicas:  4,
				Direction: DirectionUp,
				Reason:    "scaled up: 0.93 cores over target 0.70",
			},
			wantState: State{LastChange: t0, LastDirection: DirectionUp},
		},
		{
			// An unknown direction cannot name a cooldown, so it cannot block.
			name:   "an unknown last direction does not block",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 3, Cores: hot},
			state: State{
				LastChange:    t0,
				LastDirection: Direction("sideways"),
			},
			want: Decision{
				Replicas:  4,
				Direction: DirectionUp,
				Reason:    "scaled up: 0.93 cores over target 0.70",
			},
			wantState: State{LastChange: t0, LastDirection: DirectionUp},
		},
		{
			// The reason quotes the policy's own cooldown, not the default: an
			// operator who set 45s must not read "60s" in the log.
			name:   "the cooldown reason names the policy duration",
			policy: policyCooldowns(45*time.Second, 10*time.Minute),
			obs:    Observation{Replicas: 3, Cores: hot},
			state: State{
				LastChange:    t0.Add(-5 * time.Second),
				LastDirection: DirectionUp,
			},
			want: Decision{
				Replicas:  3,
				Direction: DirectionNone,
				Reason:    "waiting out the 45s scale-up cooldown",
			},
			wantState: State{
				LastChange:    t0.Add(-5 * time.Second),
				LastDirection: DirectionUp,
			},
		},
		{
			// A target the engine cannot compare against is a policy bug, and
			// guessing at it would scale a service on no evidence.
			name:   "an unusable target holds",
			policy: policyTarget(0),
			obs:    Observation{Replicas: 4, Cores: hot},
			state:  settled(1),
			want: Decision{
				Replicas:  4,
				Direction: DirectionNone,
				Reason:    "invalid policy: target is not a usable percentage",
			},
			wantState: settled(1),
		},
		{
			name:   "a NaN target holds",
			policy: policyTarget(math.NaN()),
			obs:    Observation{Replicas: 4, Cores: cold},
			state:  settled(0),
			want: Decision{
				Replicas:  4,
				Direction: DirectionNone,
				Reason:    "invalid policy: target is not a usable percentage",
			},
			wantState: settled(0),
		},
		{
			// min is documented as >= 1, but Decide is the last line of defence
			// and must never hand back zero replicas.
			name:   "a minimum below one is treated as one",
			policy: policyBounds(0, 4),
			obs:    Observation{Replicas: 1, Cores: cold},
			state:  settled(1),
			want: Decision{
				Replicas:  1,
				Direction: DirectionNone,
				Reason:    "holding: 1 is already the minimum",
			},
			wantState: settled(2),
		},
		{
			// The bounds hold even when the rest of the policy is unusable:
			// correcting a drifted count needs no sample, so it is not deferred
			// to a policy the operator has to fix first.
			name:   "an unusable target still corrects a count above the maximum",
			policy: policyTarget(0),
			obs:    Observation{Replicas: 12, Cores: idle},
			state:  settled(1),
			want: Decision{
				Replicas:  10,
				Direction: DirectionDown,
				Reason:    "clamped down to the maximum 10 (was 12)",
			},
			wantState: State{LastChange: t0, LastDirection: DirectionDown},
		},
		{
			// The cooldown is "at least CooldownUp between two scale-ups", so
			// exactly one cooldown later the next scale-up is allowed.
			name:   "the cooldown ends exactly at its duration",
			policy: cpuPolicy(),
			obs:    Observation{Replicas: 3, Cores: hot},
			state: State{
				LastChange:    t0.Add(-60 * time.Second),
				LastDirection: DirectionUp,
			},
			want: Decision{
				Replicas:  4,
				Direction: DirectionUp,
				Reason:    "scaled up: 0.93 cores over target 0.70",
			},
			wantState: State{LastChange: t0, LastDirection: DirectionUp},
		},
		{
			// A policy whose max is below its min still yields a usable range
			// instead of an unsatisfiable one.
			name:   "a maximum below the minimum is treated as the minimum",
			policy: policyBounds(5, 2),
			obs:    Observation{Replicas: 4, Cores: idle},
			state:  settled(0),
			want: Decision{
				Replicas:  5,
				Direction: DirectionUp,
				Reason:    "clamped up to the minimum 5 (was 4)",
			},
			wantState: State{LastChange: t0, LastDirection: DirectionUp},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, gotState := Decide(tc.policy, tc.obs, tc.state, t0)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantState, gotState)

			// Purity: the same inputs must produce the same decision and the
			// same next state, no matter how often they are evaluated.
			again, againState := Decide(tc.policy, tc.obs, tc.state, t0)
			assert.Equal(t, got, again, "Decide must be deterministic")
			assert.Equal(t, gotState, againState, "Decide must not mutate anything it reads")
		})
	}
}

// TestAverageCores pins the reduction Decide relies on: the mean of the usable
// readings, and "unusable" for the whole round as soon as one reading is. The
// empty case is unreachable through Decide (it reports no readings separately,
// because the two cases need different reasons) but is part of the helper's
// contract, so it is asserted here rather than left untested.
func TestAverageCores(t *testing.T) {
	cases := []struct {
		name  string
		cores []float64
		want  float64
		ok    bool
	}{
		{name: "empty is unusable", cores: nil, ok: false},
		{name: "empty slice is unusable", cores: []float64{}, ok: false},
		{name: "one reading", cores: []float64{0.70}, want: 0.70, ok: true},
		{name: "mean of several", cores: []float64{0.2, 0.4, 0.6}, want: 0.4, ok: true},
		{name: "zero is a real reading", cores: []float64{0, 0}, want: 0, ok: true},
		{name: "NaN poisons the round", cores: []float64{0.5, math.NaN()}, ok: false},
		{name: "positive infinity poisons the round", cores: []float64{math.Inf(1)}, ok: false},
		{name: "negative infinity poisons the round", cores: []float64{math.Inf(-1)}, ok: false},
		{name: "a negative reading poisons the round", cores: []float64{0.5, -0.001}, ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := averageCores(tc.cores)
			assert.Equal(t, tc.ok, ok)
			if tc.ok {
				assert.InDelta(t, tc.want, got, 1e-9)
			}
		})
	}
}

// TestDecideProperty is the property test: whatever policy, observation and
// state the engine is handed, the replica count it returns is inside the
// policy's bounds (and never below 1), the direction agrees with the count it
// reports, the reason is a lowercase sentence fragment, and no input makes it
// panic. The bounds are recomputed here from the policy rather than read from
// the package, so a bug in the clamp cannot hide behind the same helper.
func TestDecideProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(20260701))

	// directions is the closed set a Decision may report.
	directions := map[Direction]bool{DirectionNone: true, DirectionUp: true, DirectionDown: true}

	for i := 0; i < 2000; i++ {
		// target is sometimes unusable on purpose: an out-of-range count must
		// still be clamped in that case, so the invalid-target path belongs in
		// the property too.
		target := 0.5 + rng.Float64()*120 // sometimes outside (0,100]
		switch rng.Intn(8) {
		case 0:
			target = 0
		case 1:
			target = -rng.Float64() * 50
		case 2:
			target = math.NaN()
		}

		policy := types.AutoscalePolicy{
			Metric:       types.AutoscaleMetricCPU,
			Min:          rng.Intn(10) - 2, // -2..7, so also the malformed min < 1
			Max:          rng.Intn(14) - 1, // -1..12, sometimes below Min
			Target:       target,
			CooldownUp:   time.Duration(rng.Intn(120)) * time.Second,
			CooldownDown: time.Duration(rng.Intn(600)) * time.Second,
		}
		replicas := rng.Intn(20) - 1 // -1..18, so also out-of-range counts and 0

		cores := make([]float64, rng.Intn(6))
		for j := range cores {
			switch rng.Intn(10) {
			case 0:
				cores[j] = math.NaN()
			case 1:
				cores[j] = -rng.Float64()
			case 2:
				cores[j] = math.Inf(1)
			default:
				cores[j] = rng.Float64() * 2 // 0..2 cores
			}
		}

		state := State{
			LastDirection: Direction([]string{"", "up", "down", "sideways"}[rng.Intn(4)]),
			BelowTarget:   rng.Intn(4),
		}
		if rng.Intn(2) == 0 {
			state.LastChange = t0.Add(-time.Duration(rng.Intn(900)) * time.Second)
		}

		obs := Observation{Replicas: replicas, Cores: cores, SampledAt: t0.Add(-15 * time.Second)}

		floor := 1
		if policy.Min > floor {
			floor = policy.Min
		}
		ceiling := policy.Max
		if ceiling < floor {
			ceiling = floor
		}

		var decision Decision
		var next State
		require.NotPanics(t, func() {
			decision, next = Decide(policy, obs, state, t0)
		}, "case %d: policy %+v obs %+v state %+v", i, policy, obs, state)

		assert.GreaterOrEqual(t, decision.Replicas, floor,
			"case %d: %+v must not scale below max(1, min)", i, decision)
		assert.LessOrEqual(t, decision.Replicas, ceiling,
			"case %d: %+v must not scale above max", i, decision)
		assert.True(t, directions[decision.Direction],
			"case %d: unexpected direction %q", i, decision.Direction)
		assert.NotEmpty(t, decision.Reason, "case %d: every decision needs a reason", i)
		assert.Equal(t, strings.ToLower(decision.Reason), decision.Reason,
			"case %d: %q must stay a lowercase sentence fragment", i, decision.Reason)
		assert.False(t, strings.HasSuffix(decision.Reason, "."),
			"case %d: %q must not end with a period", i, decision.Reason)

		// The direction must describe the count that was reported, so a caller
		// can trust "up means more replicas than the observation".
		switch decision.Direction {
		case DirectionNone:
			assert.Equal(t, obs.Replicas, decision.Replicas,
				"case %d: no direction must mean no change in the count", i)
			assert.Equal(t, state.LastChange, next.LastChange,
				"case %d: a hold must not move the last-change time", i)
			assert.Equal(t, state.LastDirection, next.LastDirection,
				"case %d: a hold must not change the last direction", i)
			assert.GreaterOrEqual(t, next.BelowTarget, 0,
				"case %d: the below-target counter is a count, never negative", i)
		case DirectionUp:
			assert.Greater(t, decision.Replicas, obs.Replicas, "case %d: up must add a replica", i)
			assert.Equal(t, t0, next.LastChange, "case %d: a change records its time", i)
			assert.Equal(t, DirectionUp, next.LastDirection, "case %d: a change records its direction", i)
			assert.Zero(t, next.BelowTarget, "case %d: a change restarts the hysteresis", i)
		case DirectionDown:
			assert.Less(t, decision.Replicas, obs.Replicas, "case %d: down must remove a replica", i)
			assert.Equal(t, t0, next.LastChange, "case %d: a change records its time", i)
			assert.Equal(t, DirectionDown, next.LastDirection, "case %d: a change records its direction", i)
			assert.Zero(t, next.BelowTarget, "case %d: a change restarts the hysteresis", i)
		}

		again, againState := Decide(policy, obs, state, t0)
		assert.Equal(t, decision, again, "case %d: Decide must be deterministic", i)
		assert.Equal(t, next, againState, "case %d: Decide must be pure", i)
	}
}
