package main

// The uptime a VM's status view prints.
//
// `vm status` showed "Uptime: 7s ([33m 7s])" for a VM that had been up 33
// minutes: formatDuration subtracted each unit from the duration it then printed,
// so the part before the bracket was the seconds remainder. These tests pin the
// contract — the duration and its parts describe the same thing — and the
// user-visible line that was wrong.

import (
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFormatDuration pins the format: the standard duration string, then the same
// duration broken into its units in brackets.
func TestFormatDuration(t *testing.T) {
	cases := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{name: "zero", duration: 0, want: "0s ([0s])"},
		{name: "below a second rounds to zero", duration: 400 * time.Millisecond, want: "0s ([0s])"},
		{name: "seconds", duration: 7 * time.Second, want: "7s ([7s])"},
		// The shape the bug broke: the minutes were dropped from the leading
		// duration, leaving "7s ([33m 7s])".
		{name: "minutes and seconds", duration: 33*time.Minute + 7*time.Second, want: "33m7s ([33m 7s])"},
		{name: "a whole hour", duration: time.Hour, want: "1h0m0s ([1h])"},
		{name: "hours, minutes and seconds", duration: 2*time.Hour + 3*time.Minute + 4*time.Second, want: "2h3m4s ([2h 3m 4s])"},
		{name: "more than a day", duration: 25 * time.Hour, want: "25h0m0s ([1d 1h])"},
		{name: "two days", duration: 49*time.Hour + 2*time.Minute, want: "49h2m0s ([2d 1h 2m])"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, formatDuration(tc.duration))
		})
	}
}

// TestPrintVMStatusUptimeCarriesTheWholeDuration pins the line a user reads. It
// covers the shape the bug broke (hours and minutes dropped, seconds left) and a
// short uptime, so a future change cannot "fix" the line by swapping the two
// halves — both halves must describe the same duration.
func TestPrintVMStatusUptimeCarriesTheWholeDuration(t *testing.T) {
	cases := []struct {
		name string
		ago  time.Duration
		want string // the shape of the leading duration; seconds may drift by one
	}{
		{name: "minutes and seconds", ago: 33*time.Minute + 7*time.Second, want: `^33m\d+s \(\[33m \d+s\]\)$`},
		{name: "a few seconds", ago: 7 * time.Second, want: `^\d+s \(\[\d+s\]\)$`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := uptimeLine(t, tc.ago)
			assert.Regexp(t, tc.want, line)

			head, bracket, ok := strings.Cut(line, " (")
			require.True(t, ok, "uptime %q has no bracketed parts", line)

			// The two halves are the same duration said twice. The bug printed the
			// seconds remainder before a bracket holding the whole uptime.
			headDuration, err := time.ParseDuration(head)
			require.NoError(t, err, "leading duration %q does not parse", head)
			bracket = strings.TrimSuffix(bracket, ")")
			parts := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(bracket, "["), "]"), " ", "")
			bracketDuration, err := time.ParseDuration(parts)
			require.NoError(t, err, "bracketed parts %q do not parse", bracket)
			assert.Equal(t, headDuration, bracketDuration,
				"the duration and its parts must describe the same thing")
		})
	}
}

// uptimeLine runs the status view for a VM started ago and returns the value its
// Uptime line carries, e.g. "33m7s ([33m 7s])".
func uptimeLine(t *testing.T, ago time.Duration) string {
	t.Helper()

	vm := &runtime.VMState{ID: "task-1", Status: "running", StartTime: time.Now().Add(-ago)}
	out := captureStdout(t, func() { printVMStatus(vm) })

	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  Uptime:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "  Uptime:"))
		}
	}
	t.Fatalf("the status view printed no uptime:\n%s", out)
	return ""
}
