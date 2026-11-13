package sandbox

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEveryStateIsReachable pins the honest part of the state machine: every state a
// reader can find has a transition that produces it, so none of them is a label
// nothing can reach. A state that no path produces is a lie about what the node can
// say, which is the failure the four states exist to avoid.
func TestEveryStateIsReachable(t *testing.T) {
	reachable := map[State]bool{StateCreated: true} // the state a record is written in
	for from, tos := range transitions {
		for _, to := range tos {
			reachable[to] = true
		}
		// A state that nothing leaves is an end, and only stopped is one.
		if len(tos) == 0 {
			assert.Equal(t, StateStopped, from, "only stopped may be terminal")
		}
	}

	for _, state := range []State{StateCreated, StateReady, StateFailed, StateStopped} {
		assert.True(t, reachable[state], "state %s is not reachable from any other state", state)
		assert.True(t, state.Valid(), "state %s is not valid", state)
	}
}

func TestCanTransitionRefusesWhatTheNodeCannotDo(t *testing.T) {
	cases := []struct {
		from, to State
		want     bool
	}{
		{StateCreated, StateReady, true},
		{StateCreated, StateFailed, true},
		{StateCreated, StateStopped, true},
		{StateReady, StateFailed, true},
		{StateReady, StateStopped, true},
		{StateFailed, StateStopped, true},
		// A stopped sandbox is the end of the road: its record is deleted next.
		{StateStopped, StateReady, false},
		{StateStopped, StateFailed, false},
		{StateStopped, StateStopped, false},
		// Nothing goes back to created, and a write that changes nothing is not a
		// transition.
		{StateReady, StateCreated, false},
		{StateReady, StateReady, false},
		{StateFailed, StateReady, false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, CanTransition(tc.from, tc.to), "%s -> %s", tc.from, tc.to)
	}
}

// TestIdleIsMeasuredFromTheLastUse pins the reaper's clock, including both sides of
// the boundary: a sandbox exactly at its TTL is idle (the TTL is "after this long
// unused"), and one a moment short of it is not.
func TestIdleIsMeasuredFromTheLastUse(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	rec := Record{ID: "sbx", State: StateReady, Created: now, Updated: now, LastActive: now, IdleTTLSeconds: 60}

	assert.Equal(t, 59*time.Second, rec.IdleFor(now.Add(59*time.Second)))
	assert.False(t, rec.Idle(now.Add(59*time.Second)), "one second short of the TTL is not idle")
	assert.True(t, rec.Idle(now.Add(60*time.Second)), "the TTL is the moment the sandbox becomes idle")
	assert.True(t, rec.Idle(now.Add(10*time.Minute)))

	// Never reaped is a TTL of zero, and it is not the same as "idle immediately".
	never := rec
	never.IdleTTLSeconds = 0
	assert.False(t, never.Idle(now.Add(time.Hour)))
	assert.Zero(t, never.TTL())

	// A clock reading that predates the last activity is zero idle, not a negative
	// age: a node whose clock stepped backwards must not reap everything at once.
	assert.Equal(t, time.Duration(0), rec.IdleFor(now.Add(-time.Hour)))
	assert.False(t, rec.Idle(now.Add(-time.Hour)))
}

func TestRecordValidateRefusesWhatAReaderCannotUse(t *testing.T) {
	base := func() Record {
		now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
		return Record{
			ID: "sbx-1", State: StateReady,
			Created: now, Updated: now, LastActive: now,
			Disk: "/var/lib/firecracker/sandboxes/sbx-1.ext4",
		}
	}

	require.NoError(t, base().validate())

	cases := []struct {
		name  string
		mutil func(*Record)
	}{
		{"empty ID", func(r *Record) { r.ID = "" }},
		{"ID with a path separator", func(r *Record) { r.ID = "a/b" }},
		{"ID with a dot", func(r *Record) { r.ID = "a.b" }},
		{"unknown state", func(r *Record) { r.State = "starting" }},
		{"no creation time", func(r *Record) { r.Created = time.Time{} }},
		{"updated before created", func(r *Record) { r.Updated = r.Created.Add(-time.Second) }},
		{"ready before created", func(r *Record) { r.ReadyAt = r.Created.Add(-time.Second) }},
		{"negative TTL", func(r *Record) { r.IdleTTLSeconds = -1 }},
		{"relative disk path", func(r *Record) { r.Disk = "sandboxes/sbx-1.ext4" }},
		{"relative token path", func(r *Record) { r.AgentTokenFile = "token" }},
		{"failed without a reason", func(r *Record) { r.State = StateFailed; r.Reason = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := base()
			tc.mutil(&rec)
			assert.Error(t, rec.validate())
		})
	}

	// A failed record that says why is usable, and a stopped one is too.
	rec := base()
	rec.State, rec.Reason = StateFailed, "the template's pin does not match"
	require.NoError(t, rec.validate())
	rec = base()
	rec.State = StateStopped
	require.NoError(t, rec.validate())
}

// TestReadyWindowIsAMeasurement pins that "ready in" is computed from the two
// timestamps a record carries rather than from anything a caller reports.
func TestReadyWindowIsAMeasurement(t *testing.T) {
	created := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	rec := Record{ID: "sbx", State: StateReady, Created: created, Updated: created, LastActive: created}
	assert.Zero(t, rec.ReadyAtDuration(), "a sandbox that never became ready has no window")

	rec.ReadyAt = created.Add(1500 * time.Millisecond)
	assert.Equal(t, 1500*time.Millisecond, rec.ReadyAtDuration())
}
