// Package autoscaler decides how many replicas a service should run.
//
// The manager samples every running replica of an autoscaled service once per
// interval and hands the readings to Decide, which turns them into a replica
// count. Keeping the decision in one pure function means every hysteresis and
// cooldown rule can be exercised without a cluster, a clock or a network: the
// caller supplies `now`, and nothing here reads the wall clock, keeps package
// state or starts a goroutine. The same arguments always produce the same
// Decision and the same next State.
//
// Only the cpu metric exists in this cut, so Decide does not branch on
// types.AutoscalePolicy.Metric; the policy parser guarantees it is
// types.AutoscaleMetricCPU.
package autoscaler

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// Direction is the way a decision moved the replica count. The zero value means
// "no change", so a caller can test `Decision.Direction == DirectionNone` (or
// just `== ""`) to decide whether to write a new replica count at all.
type Direction string

const (
	// DirectionNone is a decision that leaves the replica count alone.
	DirectionNone Direction = ""
	// DirectionUp is a decision that added one replica.
	DirectionUp Direction = "up"
	// DirectionDown is a decision that removed one replica.
	DirectionDown Direction = "down"
)

const (
	// scaleDownSamples is how many consecutive samples below the lower band a
	// scale-down needs. One is deliberately not enough: a replica that has just
	// booted reads low while its workload warms up, and a single dip during a
	// scrape should not cost a boot cycle later.
	scaleDownSamples = 2

	// bandFraction is how far the average may sit from the target before the
	// engine reacts: strictly above target*(1+bandFraction) is hot, strictly
	// below target*(1-bandFraction) is cold, anything between is idle. Scale-up
	// reacts on the first hot sample, so the band is what absorbs the noise.
	bandFraction = 0.10
)

// Observation is one round of samples from a service's running replicas.
type Observation struct {
	// Replicas is the replica count the service runs right now, as the manager
	// last read it from the service spec.
	Replicas int

	// Cores holds one reading per running replica, already reduced to cores per
	// replica by the caller: CPU-seconds burned during the sampling window
	// divided by the window's wall-seconds. It is sentinel-free, so a reading of
	// 0.70 means 0.70 of one vCPU. A reading that is negative or not finite is
	// treated as "no data" rather than as an extreme value, because a broken
	// probe must never be able to scale a service on its own.
	Cores []float64

	// SampledAt is when the readings were taken. Decide does not use it — the
	// decision is made against the `now` it is given, so a caller replaying a
	// backlog of samples still reasons against the clock of its choosing. It is
	// carried so the caller can log the sample age next to the reason.
	SampledAt time.Time
}

// State is what the caller must carry from one tick to the next. It is returned
// by Decide and must be passed straight back on the next call; it holds the only
// memory the engine has.
type State struct {
	// LastChange is when the replica count last changed. The zero value means
	// "never changed", which is treated as "no cooldown is running" — a zero
	// timestamp must not be read as a cooldown that began at the epoch.
	LastChange time.Time

	// LastDirection is the direction of that last change. An empty or unknown
	// value names no cooldown, so it blocks nothing.
	LastDirection Direction

	// BelowTarget counts consecutive samples strictly below the lower band. It
	// is what makes scale-down need two samples instead of one, and it counts
	// every cold sample — including the ones a cooldown holds back — so the
	// reason can say how long the service has been cold and stays truthful.
	BelowTarget int
}

// Decision is the outcome of one tick.
type Decision struct {
	// Replicas is the replica count the service should run. A hold reports the
	// observed count unchanged; a change reports it moved by exactly one in
	// Direction. An out-of-range observed count is therefore echoed, not
	// corrected: bringing it back inside the policy is the caller's explicit
	// "initial alignment" step, never a load decision.
	Replicas int

	// Direction is DirectionNone when Replicas equals the observed count, so a
	// caller applies exactly the decisions that report a direction.
	Direction Direction

	// Reason is a lowercase sentence fragment with no trailing period, e.g.
	// "scaled up: 0.93 cores over target 0.70". It is logged verbatim, so the
	// wording is part of the contract.
	Reason string
}

// Decide turns one round of samples into a replica count.
//
// The rules, in the order they are applied. The first four are "nothing to
// decide" ticks: they hold, and they return State exactly as it arrived, so a
// pause, a broken scrape or a count that drifted outside the policy cannot arm a
// cooldown or reset the hysteresis evidence.
//
//   - No data — no readings at all, or a negative/non-finite reading — holds.
//     This is checked first, before the count and the bounds: with nothing
//     measured there is nothing to decide, whatever the count says.
//   - A count of 0 replicas is the operator's pause. Decide never restarts a
//     paused service: un-pausing is an explicit action.
//   - An observed count outside [max(1, min), max] holds and names the bound it
//     violates. It is not corrected here: Decide only ever moves the count by
//     one, and an "initial alignment" is the caller's own step.
//   - A policy whose target is not a usable percentage (outside (0, 100], or
//     NaN/±Inf) holds: scaling on a target that cannot be compared against is
//     guessing.
//   - A running cooldown for the last direction blocks every change, including a
//     flip in the opposite direction. A sample still updates the below-target
//     counter while it waits, so the cooldown delays a scale-down rather than
//     discarding the evidence for it.
//   - An average strictly above target*1.1 scales up by one, unless the count is
//     already at the maximum.
//   - An average strictly below target*0.9 scales down by one once it has been
//     seen scaleDownSamples times in a row, unless the count is already at the
//     minimum.
//   - Anything else holds.
//
// Any change resets BelowTarget, records `now` as LastChange and the direction in
// LastDirection.
func Decide(p types.AutoscalePolicy, obs Observation, st State, now time.Time) (Decision, State) {
	// No data comes first: neither the count nor the policy can be acted on
	// without a measurement, and forgetting the hysteresis here would be a lie.
	if len(obs.Cores) == 0 {
		return hold(obs.Replicas, "no replicas reporting", st)
	}

	avg, ok := averageCores(obs.Cores)
	if !ok {
		return hold(obs.Replicas, "no data: ignoring an invalid cpu reading", st)
	}

	// 0 replicas is the operator's pause, not a count to be raised: the engine
	// has no way to tell "paused on purpose" from "scaled to nothing", so it
	// must assume the former and leave the service alone.
	if obs.Replicas == 0 {
		return hold(obs.Replicas, "holding: 0 replicas means the service is paused", st)
	}

	// An out-of-range count is a hold, not a correction: only the caller knows
	// whether it wants the count aligned to a policy it just changed.
	floor, ceiling := bounds(p)
	if obs.Replicas > ceiling {
		return hold(obs.Replicas, fmt.Sprintf("holding: %d is above the maximum %d", obs.Replicas, ceiling), st)
	}
	if obs.Replicas < floor {
		return hold(obs.Replicas, fmt.Sprintf("holding: %d is below the minimum %d", obs.Replicas, floor), st)
	}

	// target is in cores per replica: a policy target of 70 means 0.70 cores.
	// Anything outside (0, 100] cannot be compared against, so it is a policy bug
	// and not a load signal (the parser rejects it; this is the last defence).
	target := p.Target / 100
	if math.IsNaN(target) || math.IsInf(target, 0) || target <= 0 || target > 1 {
		return hold(obs.Replicas, "invalid policy: target is not a usable percentage", st)
	}

	hot := avg > target*(1+bandFraction)
	cold := avg < target*(1-bandFraction)

	// The counter moves on every cold sample, in or out of a cooldown, so a
	// scale-down is decided on consecutive observations rather than on the tick
	// the cooldown happens to expire — and the reason can quote the real run.
	if cold {
		st.BelowTarget++
	} else {
		st.BelowTarget = 0
	}

	if cooldown, waiting := cooldownFor(st, now, p); waiting {
		reason := fmt.Sprintf("waiting out the %s scale-%s cooldown", formatSeconds(cooldown), st.LastDirection)
		return hold(obs.Replicas, reason, st)
	}

	switch {
	case hot:
		if obs.Replicas >= ceiling {
			return hold(obs.Replicas, fmt.Sprintf("holding: %d is already the maximum", ceiling), st)
		}
		return changed(obs.Replicas+1, DirectionUp,
			fmt.Sprintf("scaled up: %.2f cores over target %.2f", avg, target), now)

	case cold && st.BelowTarget >= scaleDownSamples:
		if obs.Replicas <= floor {
			return hold(obs.Replicas, fmt.Sprintf("holding: %d is already the minimum", floor), st)
		}
		return changed(obs.Replicas-1, DirectionDown,
			fmt.Sprintf("scaled down: %.2f cores under target %.2f for %d samples", avg, target, st.BelowTarget), now)

	case cold:
		return hold(obs.Replicas,
			fmt.Sprintf("holding: %.2f cores is under target %.2f (%d of %d samples)", avg, target, st.BelowTarget, scaleDownSamples), st)

	default:
		return hold(obs.Replicas,
			fmt.Sprintf("holding: %.2f cores is within the band around target %.2f", avg, target), st)
	}
}

// bounds returns the replica range a change may land in. min is documented as
// at least 1, but Decide is the last line of defence before a patch reaches the
// service, so a malformed policy is narrowed instead of trusted: the floor is
// never below 1 and the range is never empty.
func bounds(p types.AutoscalePolicy) (floor, ceiling int) {
	floor = p.Min
	if floor < 1 {
		floor = 1
	}
	ceiling = p.Max
	if ceiling < floor {
		ceiling = floor
	}
	return floor, ceiling
}

// averageCores reduces the readings to the mean cores per replica, and reports
// false when any reading is unusable, or when the readings are individually
// fine but their sum is not. One bad reading poisons the whole round: averaging
// around it would let a failed probe, which typically reports 0 or NaN, drag the
// service into a scale-down. The sum is checked as well because a set of finite
// readings can still overflow to +Inf, which would read as an infinitely hot
// service.
func averageCores(cores []float64) (float64, bool) {
	if len(cores) == 0 {
		return 0, false
	}
	sum := 0.0
	for _, reading := range cores {
		if math.IsNaN(reading) || math.IsInf(reading, 0) || reading < 0 {
			return 0, false
		}
		sum += reading
	}
	if math.IsInf(sum, 0) {
		return 0, false
	}
	return sum / float64(len(cores)), true
}

// cooldownFor reports the cooldown that currently blocks changes, if any. Only
// the direction of the last change has a cooldown: while it runs, no change at
// all happens, which is what stops a service from flip-flopping when a hot and a
// cold sample alternate.
func cooldownFor(st State, now time.Time, p types.AutoscalePolicy) (time.Duration, bool) {
	var cooldown time.Duration
	switch st.LastDirection {
	case DirectionUp:
		cooldown = p.CooldownUp
	case DirectionDown:
		cooldown = p.CooldownDown
	default:
		return 0, false
	}
	// A negative cooldown can never mean anything (the parser rejects it, and
	// Decide must not assume it did) and a zero one means "never wait".
	if cooldown <= 0 || st.LastChange.IsZero() {
		return 0, false
	}
	if now.Sub(st.LastChange) < cooldown {
		return cooldown, true
	}
	return 0, false
}

// changed builds a decision that moves the count by exactly one replica, with the
// state a change implies: the cooldown clock starts now and the hysteresis
// restarts. Every caller passes obs.Replicas ± 1, which is what makes
// DirectionUp/DirectionDown mean "one more"/"one fewer" without qualification.
func changed(replicas int, direction Direction, reason string, now time.Time) (Decision, State) {
	return Decision{Replicas: replicas, Direction: direction, Reason: reason},
		State{LastChange: now, LastDirection: direction}
}

// hold builds a decision that keeps the count, carrying the state forward
// unchanged apart from the below-target counter Decide has already moved.
func hold(replicas int, reason string, st State) (Decision, State) {
	return Decision{Replicas: replicas, Direction: DirectionNone, Reason: reason}, st
}

// formatSeconds renders a cooldown as a whole-second figure ("60s", "300s") so a
// log line quotes the policy the operator wrote rather than the normalised
// duration ("1m0s", "5m0s").
func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}
