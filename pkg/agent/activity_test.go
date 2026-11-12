//go:build !integration

package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTrackerReportsWhatIsInFlight is the operation's whole point: an operation
// that has not finished is visible from somewhere else, while it runs.
func TestTrackerReportsWhatIsInFlight(t *testing.T) {
	trk := newTracker()
	proc := procFixtureWithProcesses(t, 7)

	done := trk.begin(CapTar)
	time.Sleep(2 * time.Millisecond)

	busy := trk.snapshot(proc)
	require.Len(t, busy.Inflight, 1)
	assert.Equal(t, CapTar, busy.Inflight[0].Op)
	assert.Greater(t, busy.Inflight[0].Seconds, 0.0)
	assert.Equal(t, 7, busy.Processes)
	assert.Greater(t, busy.UptimeSeconds, 0.0)
	assert.Empty(t, busy.LastOp, "nothing has finished yet")

	done()
	idle := trk.snapshot(proc)
	assert.Empty(t, idle.Inflight)
	assert.Equal(t, CapTar, idle.LastOp)
	assert.GreaterOrEqual(t, idle.IdleSeconds, 0.0)
}

// TestTrackerDoesNotCountTheActivityRequest is the claim that makes the operation
// answerable at all: asking a sandbox what it is doing must not make it look busy,
// or a second question would always see the first one as work.
func TestTrackerDoesNotCountTheActivityRequest(t *testing.T) {
	trk := newTracker()
	proc := procFixtureWithProcesses(t, 3)

	done := trk.begin(CapActivity)
	before := trk.snapshot(proc)
	assert.Empty(t, before.Inflight)

	time.Sleep(2 * time.Millisecond)
	after := trk.snapshot(proc)
	assert.Empty(t, after.Inflight)
	assert.Empty(t, after.LastOp)
	done()

	// The one operation that ended was the observation, so the agent is still
	// exactly as idle as it was.
	assert.Empty(t, trk.snapshot(proc).Inflight)
	assert.Empty(t, trk.snapshot(proc).LastOp)
}

// TestTrackerIdleGrowsWhileNothingRuns proves the idle time is measured and not a
// constant: it grows while the agent does nothing and is reset by work.
func TestTrackerIdleGrowsWhileNothingRuns(t *testing.T) {
	trk := newTracker()
	proc := procFixtureWithProcesses(t, 1)

	done := trk.begin(CapInfo)
	done()
	assert.Less(t, trk.snapshot(proc).IdleSeconds, 0.5, "just finished")

	time.Sleep(1100 * time.Millisecond)
	assert.Greater(t, trk.snapshot(proc).IdleSeconds, 1.0, "a second later, still idle")
	assert.Equal(t, CapInfo, trk.snapshot(proc).LastOp)
}

// TestTrackerEndsAnOperationOnce covers the end function being safe to call twice:
// a handler that defers it and also calls it must not corrupt the count.
func TestTrackerEndsAnOperationOnce(t *testing.T) {
	trk := newTracker()
	proc := procFixtureWithProcesses(t, 1)

	done := trk.begin(CapList)
	done()
	done()

	assert.Empty(t, trk.snapshot(proc).Inflight)
}

// TestCountProcessesCountsNumericEntries proves the guest's process count comes
// from the process directories and not from everything /proc holds.
func TestCountProcessesCountsNumericEntries(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"1", "42", "9999"} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0o555))
	}
	for _, name := range []string{"self", "net", "sys", "uptime", "12x"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o444))
	}

	assert.Equal(t, 3, countProcesses(dir))
	assert.Equal(t, 0, countProcesses(filepath.Join(dir, "absent")), "an unreadable /proc reports no process rather than failing")
}

// procFixtureWithProcesses writes a /proc that holds n process directories, which
// is what the activity report counts.
func procFixtureWithProcesses(t *testing.T, n int) string {
	t.Helper()

	dir := t.TempDir()
	for i := 0; i < n; i++ {
		require.NoError(t, os.Mkdir(filepath.Join(dir, leftPad(i+1)), 0o555))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "uptime"), []byte("1.0 1.0\n"), 0o444))
	return dir
}
