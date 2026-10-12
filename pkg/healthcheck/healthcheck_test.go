package healthcheck

import (
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

// start is an arbitrary fixed instant; Tracker never reads the clock itself, so
// every observation is supplied explicitly.
var start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// step is one Observe call: at is the offset from start, healthy the probe
// result, and want the expected verdict.
type step struct {
	at      time.Duration
	healthy bool
	want    Verdict
}

func TestTrackerObserve(t *testing.T) {
	tests := []struct {
		name string
		h    *types.Healthcheck
		// steps are observed in order against a single tracker.
		steps []step
	}{
		{
			name: "healthy first try",
			h:    &types.Healthcheck{Retries: 3},
			steps: []step{
				{0, true, VerdictHealthy},
			},
		},
		{
			name: "failures inside the start period stay starting and do not count",
			h:    &types.Healthcheck{StartPeriod: 10 * time.Second, Retries: 2},
			steps: []step{
				{0, false, VerdictStarting},
				{1 * time.Second, false, VerdictStarting},
				{2 * time.Second, false, VerdictStarting},
				// At the boundary the failure counts: the counter is now 1, one
				// short of the 2 needed, so the verdict is still Starting. If
				// the earlier three failures had counted it would already be
				// Unhealthy.
				{10 * time.Second, false, VerdictStarting},
				{11 * time.Second, false, VerdictUnhealthy},
			},
		},
		{
			name: "exactly retries failures outside the start period is unhealthy",
			h:    &types.Healthcheck{Retries: 3},
			steps: []step{
				{0, false, VerdictStarting},
				{1 * time.Second, false, VerdictStarting},
				{2 * time.Second, false, VerdictUnhealthy},
			},
		},
		{
			name: "one fewer than retries stays starting",
			h:    &types.Healthcheck{Retries: 3},
			steps: []step{
				{0, false, VerdictStarting},
				{1 * time.Second, false, VerdictStarting},
			},
		},
		{
			name: "a success after failures resets the counter",
			h:    &types.Healthcheck{Retries: 3},
			steps: []step{
				{0, false, VerdictStarting},
				{1 * time.Second, false, VerdictStarting},
				{2 * time.Second, true, VerdictHealthy},
				// The two prior failures were reset, so two more are still one
				// short of Unhealthy.
				{3 * time.Second, false, VerdictStarting},
				{4 * time.Second, false, VerdictStarting},
				{5 * time.Second, false, VerdictUnhealthy},
			},
		},
		{
			name: "retries 0 is treated as 1",
			h:    &types.Healthcheck{Retries: 0},
			steps: []step{
				{0, false, VerdictUnhealthy},
			},
		},
		{
			name: "negative retries is treated as 1",
			h:    &types.Healthcheck{Retries: -5},
			steps: []step{
				{0, false, VerdictUnhealthy},
			},
		},
		{
			name: "just inside the start-period boundary stays starting",
			h:    &types.Healthcheck{StartPeriod: 10 * time.Second, Retries: 1},
			steps: []step{
				{10*time.Second - 1, false, VerdictStarting},
				// The boundary failure now counts and reaches Retries=1.
				{10 * time.Second, false, VerdictUnhealthy},
			},
		},
		{
			name: "a healthy probe inside the start period is healthy",
			h:    &types.Healthcheck{StartPeriod: 10 * time.Second, Retries: 3},
			steps: []step{
				{1 * time.Second, true, VerdictHealthy},
			},
		},
		{
			name: "a nil healthcheck defaults to one retry and no start period",
			h:    nil,
			steps: []step{
				{0, false, VerdictUnhealthy},
				{1 * time.Second, true, VerdictHealthy},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewTracker(tt.h, start)
			for i, s := range tt.steps {
				got := tracker.Observe(start.Add(s.at), s.healthy)
				assert.Equalf(t, s.want, got, "step %d at %s (healthy=%v)", i, s.at, s.healthy)
			}
		})
	}
}

// TestVerdictValues pins the verdict enumeration so a reordering cannot silently
// change the meaning of a persisted or logged value.
func TestVerdictValues(t *testing.T) {
	assert.Equal(t, Verdict(0), VerdictStarting)
	assert.Equal(t, Verdict(1), VerdictHealthy)
	assert.Equal(t, Verdict(2), VerdictUnhealthy)
}
