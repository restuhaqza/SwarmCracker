package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

func TestParseSince(t *testing.T) {
	if ts, err := parseSince(""); err != nil || !ts.IsZero() {
		t.Fatalf("parseSince(\"\") = %v, %v; want zero time, nil", ts, err)
	}

	now := time.Now()
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"2h", 2 * time.Hour},
		{"30m", 30 * time.Minute},
		{"45", 45 * time.Minute},
	} {
		ts, err := parseSince(tc.in)
		if err != nil {
			t.Fatalf("parseSince(%q) error = %v", tc.in, err)
		}
		got := now.Sub(ts)
		if got < tc.want-time.Minute || got > tc.want+time.Minute {
			t.Errorf("parseSince(%q) = %v ago, want ~%v", tc.in, got, tc.want)
		}
	}

	// Absolute timestamps must be parsed as-is.
	local, _ := time.ParseInLocation("2006-01-02T15:04:05", "2026-01-02T15:04:05", time.Local)
	ts, err := parseSince("2026-01-02T15:04:05")
	if err != nil {
		t.Fatalf("parseSince(absolute) error = %v", err)
	}
	if !ts.Equal(local) {
		t.Errorf("parseSince(absolute) = %v, want %v", ts, local)
	}

	if _, err := parseSince("not-a-time"); err == nil {
		t.Error("parseSince(bogus) expected error, got nil")
	}
}

func TestExtractTimestamp(t *testing.T) {
	tests := []struct {
		line string
		ok   bool
	}{
		{"2026-10-07T21:18:50.101735814 [anonymous-instance:main] Running Firecracker", true},
		{"2026-10-07T21:18:50 [anonymous-instance:main] booted", true},
		{"2026-10-07 21:18:50 hello world", true},
		{"[    0.000000] Linux version 6.18.36+", false},
		{"", false},
		{"plain message without time", false},
	}
	for _, tc := range tests {
		_, ok := extractTimestamp(tc.line)
		if ok != tc.ok {
			t.Errorf("extractTimestamp(%q) ok = %v, want %v", tc.line, ok, tc.ok)
		}
	}
}

func TestShouldDisplayLine(t *testing.T) {
	if !shouldDisplayLine("anything", time.Time{}) {
		t.Error("zero since should keep all lines")
	}

	cutoff := time.Date(2026, 10, 7, 22, 0, 0, 0, time.Local)
	before := "2026-10-07T21:00:00.000000000 [anon] old"
	after := "2026-10-07T23:00:00.000000000 [anon] new"
	kernel := "[    0.000000] Linux version"

	if shouldDisplayLine(before, cutoff) {
		t.Error("line before cutoff should be filtered out")
	}
	if !shouldDisplayLine(after, cutoff) {
		t.Error("line after cutoff should be kept")
	}
	if !shouldDisplayLine(kernel, cutoff) {
		t.Error("line without timestamp should be kept")
	}
}

func TestTailLines(t *testing.T) {
	input := "a\nb\nc\nd\n"

	lines, err := tailLines(strings.NewReader(input), 2, time.Time{})
	if err != nil {
		t.Fatalf("tailLines error = %v", err)
	}
	if got, want := strings.Join(lines, ","), "c,d"; got != want {
		t.Errorf("tailLines(2) = %q, want %q", got, want)
	}

	lines, err = tailLines(strings.NewReader(input), 0, time.Time{})
	if err != nil {
		t.Fatalf("tailLines(0) error = %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("tailLines(0) = %v, want empty", lines)
	}

	cutoff := time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local)
	filtered := "2026-10-07T11:00:00 old\n2026-10-07T13:00:00 new1\n2026-10-07T14:00:00 new2\n"
	lines, err = tailLines(strings.NewReader(filtered), 1, cutoff)
	if err != nil {
		t.Fatalf("tailLines(since) error = %v", err)
	}
	if len(lines) != 1 || lines[0] != "2026-10-07T14:00:00 new2" {
		t.Errorf("tailLines(since) = %v, want only the newest after cutoff", lines)
	}
}

func TestWriteBacklog(t *testing.T) {
	input := "one\ntwo\nthree\n"

	var all bytes.Buffer
	if err := writeBacklog(&all, strings.NewReader(input), -1, time.Time{}); err != nil {
		t.Fatalf("writeBacklog(-1) error = %v", err)
	}
	if all.String() != input {
		t.Errorf("writeBacklog(-1) = %q, want %q", all.String(), input)
	}

	var tailed bytes.Buffer
	if err := writeBacklog(&tailed, strings.NewReader(input), 1, time.Time{}); err != nil {
		t.Fatalf("writeBacklog(1) error = %v", err)
	}
	if tailed.String() != "three\n" {
		t.Errorf("writeBacklog(1) = %q, want %q", tailed.String(), "three\n")
	}

	var none bytes.Buffer
	if err := writeBacklog(&none, strings.NewReader(input), 0, time.Time{}); err != nil {
		t.Fatalf("writeBacklog(0) error = %v", err)
	}
	if none.Len() != 0 {
		t.Errorf("writeBacklog(0) = %q, want empty", none.String())
	}
}

func TestResolveLogPathDaemonFallback(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("state directory is fixed when running as root")
	}
	t.Setenv("HOME", t.TempDir())

	sm, err := runtime.NewStateManager("")
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	// A log file that exists on disk but is absent from CLI state represents a
	// daemon-managed service task; it must still be resolvable by ID.
	logPath := filepath.Join(sm.GetLogDir(), "service-task-1.log")
	if err := os.WriteFile(logPath, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("failed to write log file: %v", err)
	}

	got, err := resolveLogPath(sm, "service-task-1")
	if err != nil {
		t.Fatalf("resolveLogPath(daemon task) error = %v", err)
	}
	if got != logPath {
		t.Errorf("resolveLogPath(daemon task) = %q, want %q", got, logPath)
	}

	if _, err := resolveLogPath(sm, "does-not-exist"); err == nil ||
		!strings.Contains(err.Error(), "VM not found") {
		t.Errorf("resolveLogPath(missing) error = %v, want 'VM not found'", err)
	}
}

// syncBuffer is a goroutine-safe writer for exercising tailStream.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitForContains(t *testing.T, sb *syncBuffer, sub string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(sb.String(), sub) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for output to contain %q; got %q", sub, sb.String())
}

func TestTailStreamAppendsAndStops(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "vm.log")
	if err := os.WriteFile(logPath, []byte("start\n"), 0o644); err != nil {
		t.Fatalf("failed to write log file: %v", err)
	}

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("failed to open log file: %v", err)
	}
	defer f.Close()

	sb := &syncBuffer{}
	out := bufio.NewWriter(sb)
	ticks := make(chan time.Time)
	stop := make(chan os.Signal, 1)
	done := make(chan error, 1)

	go func() {
		done <- tailStream(f, logPath, out, int64(len("start\n")), time.Time{}, ticks, stop)
	}()

	// Append a new line and poke the loop.
	af, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("failed to reopen log file: %v", err)
	}
	if _, err := af.WriteString("appended-line\n"); err != nil {
		t.Fatalf("failed to append: %v", err)
	}
	_ = af.Close()

	ticks <- time.Now()
	waitForContains(t, sb, "appended-line")

	stop <- os.Interrupt
	if err := <-done; err != nil {
		t.Fatalf("tailStream returned error: %v", err)
	}
	if !strings.Contains(sb.String(), "Stopped following logs") {
		t.Errorf("expected stop notice, got %q", sb.String())
	}
}
