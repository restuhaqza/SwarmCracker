package sandbox

import (
	"strings"
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

// TestExposedPortsAreValidated is the record's own check on an exposure: an exposed port is
// a listener on the host and an opening in the sandbox's table, so a record that names one
// must name it exactly. A record a reader could act on and the node could not honour is the
// failure this refuses.
func TestExposedPortsAreValidated(t *testing.T) {
	created := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	digest := strings.Repeat("ab", 32)
	good := ExposedPort{GuestPort: 8080, Address: "127.9.9.9", TokenHash: digest, Created: created}

	rec := testRecord("exposed")
	rec.Ports = []ExposedPort{good}
	require.NoError(t, rec.validate(), "a well-formed exposure is usable")

	another := ExposedPort{GuestPort: 9090, Address: "127.9.9.9", TokenHash: digest, Created: created}

	for _, tc := range []struct {
		name  string
		ports []ExposedPort
		want  string
	}{
		{"port zero", []ExposedPort{{Address: "127.9.9.9", TokenHash: digest, Created: created}}, "exposes port 0"},
		{"a public address", []ExposedPort{{GuestPort: 8080, Address: "10.63.0.2", TokenHash: digest, Created: created}}, "loopback IPv4 address"},
		{"a name", []ExposedPort{{GuestPort: 8080, Address: "sandbox.example", TokenHash: digest, Created: created}}, "loopback IPv4 address"},
		{"a digest that is not one", []ExposedPort{{GuestPort: 8080, Address: "127.9.9.9", TokenHash: "short", Created: created}}, "not a SHA-256 digest"},
		{"no creation time", []ExposedPort{{GuestPort: 8080, Address: "127.9.9.9", TokenHash: digest}}, "no creation time"},
		{"expiry before creation", []ExposedPort{{GuestPort: 8080, Address: "127.9.9.9", TokenHash: digest, Created: created, ExpiresAt: created.Add(-time.Minute)}}, "before it was exposed"},
		{"the same port twice", []ExposedPort{good, good}, "twice"},
		// A second, well-formed exposure beside a bad one is still refused: an exposure
		// somewhere in the list is what matters, not only the first.
		{"a bad one beside a good one", []ExposedPort{another, {GuestPort: 8080, Address: "127.0.0.1", TokenHash: digest}}, "no creation time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := testRecord("exposed")
			rec.Ports = tc.ports
			err := rec.validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	// An exposure with no digest is one this node cannot check, and the record may still
	// carry it: the proxy is what refuses to serve it, not the store.
	rec = testRecord("exposed")
	rec.Ports = []ExposedPort{{GuestPort: 8080, Address: "127.9.9.9", Created: created}}
	require.NoError(t, rec.validate())
}

// TestReapDueIsWhicheverLimitComesFirst pins the rule the two deadlines share: a sandbox
// is taken back by whichever it reaches first, and one past both is reported against the
// limit whose deadline came first — the limit that actually took it. The matrix is
// asymmetric on purpose: the same sandbox at the same instant is an idle reap or a
// maximum-lifetime reap depending only on which deadline is earlier, so a rule that
// always answered one of them would pass a symmetric case and fail here.
func TestReapDueIsWhicheverLimitComesFirst(t *testing.T) {
	created := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

	// Every case names the instant the reaper looks and the last time the sandbox was
	// used, both as offsets from its creation, so each one is a state and not a duration
	// of elapsed test time.
	cases := []struct {
		name            string
		maxTTL, idleTTL time.Duration
		lastActive, now time.Duration
		want            string
		wantDue         bool
	}{
		{"inside both", 2 * time.Hour, 30 * time.Minute, 0, 10 * time.Minute, "", false},
		// The idle deadline came first: unused for forty-five minutes with a thirty
		// minute idle TTL, well inside the four hour maximum.
		{"past the idle deadline only", 4 * time.Hour, 30 * time.Minute, 0, 45 * time.Minute, ReapLimitIdle, true},
		// The maximum deadline came first, and the sandbox was used a minute ago: an exec
		// postpones the idle TTL and does nothing for this one, which is exactly the
		// difference between the two limits.
		{"past the maximum deadline only", 2 * time.Hour, 30 * time.Minute, 100 * time.Minute, 121 * time.Minute, ReapLimitMax, true},
		// Both are past. The idle deadline (last used at creation, plus thirty minutes)
		// came before the maximum one (two hours), so idle is the limit that took it.
		{"both, idle deadline first", 2 * time.Hour, 30 * time.Minute, 0, 24 * time.Hour, ReapLimitIdle, true},
		// Both are past, and this time the maximum deadline (an hour) came first, three
		// quarters of an hour before the idle deadline did.
		{"both, maximum deadline first", time.Hour, time.Hour, 90 * time.Minute, 151 * time.Minute, ReapLimitMax, true},
		{"no limit at all", 0, 0, 0, 24 * time.Hour, "", false},
		{"no maximum, idle past", 0, time.Minute, 0, time.Hour, ReapLimitIdle, true},
		{"no idle TTL, maximum past", time.Hour, 0, 0, 2 * time.Hour, ReapLimitMax, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := testRecord("sbx-1")
			rec.Created = created
			rec.LastActive = created.Add(tc.lastActive)
			rec.IdleTTLSeconds = int64(tc.idleTTL / time.Second)
			rec.MaxTTLSeconds = int64(tc.maxTTL / time.Second)

			limit, due := rec.ReapDue(created.Add(tc.now))
			assert.Equal(t, tc.wantDue, due)
			assert.Equal(t, tc.want, limit)
		})
	}
}

// TestMaxDeadlineIsTheRecordsOwnInstant pins the property the record field exists for: the
// deadline is a function of the record alone, so a node that restarts reads the same
// deadline back rather than starting the clock again.
func TestMaxDeadlineIsTheRecordsOwnInstant(t *testing.T) {
	created := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

	rec := testRecord("sbx-1")
	rec.Created = created
	rec.MaxTTLSeconds = int64(90 * time.Minute / time.Second)
	assert.Equal(t, created.Add(90*time.Minute), rec.MaxDeadline())
	assert.Equal(t, 90*time.Minute, rec.MaxTTL())
	assert.False(t, rec.MaxExpired(created.Add(89*time.Minute)), "a second before the deadline it is alive")
	assert.True(t, rec.MaxExpired(created.Add(90*time.Minute)), "the deadline itself is the end")

	// A sandbox this node sets no maximum for has no deadline to report, which is what a
	// reader prints as "none" rather than as the zero time.
	rec.MaxTTLSeconds = 0
	assert.True(t, rec.MaxDeadline().IsZero())
	assert.Zero(t, rec.MaxTTL())
	assert.False(t, rec.MaxExpired(created.Add(1000*time.Hour)))

	// A record with no creation time has no anchor for a maximum, so it has no deadline
	// rather than one measured from the zero time.
	rec.MaxTTLSeconds = 3600
	rec.Created = time.Time{}
	assert.True(t, rec.MaxDeadline().IsZero())
	assert.False(t, rec.MaxExpired(created.Add(1000*time.Hour)))
}

// TestRecordValidateRefusesANegativeMaximum is the store's side of the ceiling: a record
// whose maximum lifetime is negative is one no reader can act on, and the store refuses
// to serve it rather than treating it as unlimited.
func TestRecordValidateRefusesANegativeMaximum(t *testing.T) {
	rec := testRecord("sbx-1")
	rec.MaxTTLSeconds = -1
	err := rec.validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "negative maximum lifetime")
}

// TestDeadlineIsTheEarlierOfTheTwoLimits pins the instant a reader that has to bound
// something else by this sandbox's life gets — a token bound to a sandbox, above all: the
// earlier of the idle deadline and the maximum lifetime, and the zero time when the sandbox
// has neither.
func TestDeadlineIsTheEarlierOfTheTwoLimits(t *testing.T) {
	created := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		maxTTL     time.Duration
		idleTTL    time.Duration
		lastActive time.Duration
		want       time.Time
	}{
		{"neither", 0, 0, 0, time.Time{}},
		{"the idle TTL only", 0, 30 * time.Minute, 5 * time.Minute, created.Add(35 * time.Minute)},
		{"the maximum lifetime only", 2 * time.Hour, 0, 0, created.Add(2 * time.Hour)},
		{"the maximum lifetime is earlier", time.Hour, 4 * time.Hour, 0, created.Add(time.Hour)},
		{"the idle deadline is earlier", 4 * time.Hour, 30 * time.Minute, 0, created.Add(30 * time.Minute)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := testRecord("sbx-1")
			rec.Created = created
			rec.LastActive = created.Add(tc.lastActive)
			rec.IdleTTLSeconds = int64(tc.idleTTL / time.Second)
			rec.MaxTTLSeconds = int64(tc.maxTTL / time.Second)
			assert.Equal(t, tc.want, rec.Deadline())
		})
	}
}
