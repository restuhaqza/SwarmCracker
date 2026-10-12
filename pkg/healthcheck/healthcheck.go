// Package healthcheck implements the pure container-health state machine that
// turns a stream of probe results into a verdict. It performs no I/O and reads
// no clock, so every transition is table-testable.
package healthcheck

import (
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// Verdict is the health state derived from the probes observed so far.
type Verdict int

const (
	// VerdictStarting means the container is not healthy yet but is still
	// inside its start period (or has not exhausted its retries).
	VerdictStarting Verdict = iota
	// VerdictHealthy means the most recent probe passed.
	VerdictHealthy
	// VerdictUnhealthy means Retries consecutive probes failed outside the
	// start period.
	VerdictUnhealthy
)

// Tracker is the retry / start-period state machine. It is deliberately pure:
// the caller supplies every timestamp, so there is no hidden clock and no
// locking to reason about.
type Tracker struct {
	start       time.Time
	startPeriod time.Duration
	retries     int
	failures    int
}

// NewTracker builds a tracker for the given healthcheck, with start being the
// instant the container began running. A nil healthcheck is treated as a single
// retry and no start period, so a caller that slips through still fails closed.
func NewTracker(h *types.Healthcheck, start time.Time) *Tracker {
	t := &Tracker{start: start}
	if h != nil {
		t.startPeriod = h.StartPeriod
		t.retries = h.Retries
	}
	if t.retries < 1 {
		// Defensive: the CLI already rejects this, but a zero or negative
		// value must still mean "fail on the first failure" rather than
		// "never fail".
		t.retries = 1
	}
	return t
}

// Observe records one probe result at time now and returns the current verdict.
//
// A healthy probe always yields VerdictHealthy and resets the failure counter.
// A failing probe inside the start period yields VerdictStarting without
// incrementing the counter (Docker semantics: start-period failures do not
// count toward retries). Outside the start period each failure increments the
// counter, and the verdict becomes VerdictUnhealthy once it reaches Retries.
func (t *Tracker) Observe(now time.Time, healthy bool) Verdict {
	if healthy {
		t.failures = 0
		return VerdictHealthy
	}
	if now.Before(t.start.Add(t.startPeriod)) {
		return VerdictStarting
	}
	t.failures++
	if t.failures >= t.retries {
		return VerdictUnhealthy
	}
	return VerdictStarting
}
