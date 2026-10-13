package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()

	fn()

	_ = w.Close()
	os.Stdout = orig
	out := <-done
	_ = r.Close()
	return out
}

// TestEnrichVMs_ReadsLiveHealth verifies the CLI picks up the daemon's live
// healthcheck verdict from the per-task metadata sidecar — the only place it can
// be read, since SwarmKit never polls a running task's status.
func TestEnrichVMs_ReadsLiveHealth(t *testing.T) {
	dir := t.TempDir()

	orig := vmSocketDir
	vmSocketDir = dir
	defer func() { vmSocketDir = orig }()

	// Force the SwarmKit index lookup to fail fast instead of reaching a real
	// cluster that may be running on the host.
	t.Setenv("SWARM_STATE_DIR", t.TempDir())

	if err := runtime.WriteVMMetadata(dir, &runtime.VMMetadata{
		ID: "svc-task-h",
		Health: &runtime.VMHealth{
			State:         runtime.HealthFailing,
			FailingStreak: 2,
			Output:        "boom",
			Command:       "test -f /tmp/ready",
			UpdatedAt:     time.Now(),
		},
	}); err != nil {
		t.Fatalf("WriteVMMetadata failed: %v", err)
	}

	vm := &runtime.VMState{ID: "svc-task-h", Status: "running"}
	enrichVMs([]*runtime.VMState{vm})

	if vm.Health == nil {
		t.Fatal("Health = nil, want the daemon's live observation")
	}
	if vm.Health.State != runtime.HealthFailing {
		t.Errorf("Health.State = %q, want %q", vm.Health.State, runtime.HealthFailing)
	}
	if vm.Health.FailingStreak != 2 {
		t.Errorf("Health.FailingStreak = %d, want 2", vm.Health.FailingStreak)
	}
	if vm.Health.Output != "boom" {
		t.Errorf("Health.Output = %q, want %q", vm.Health.Output, "boom")
	}
}

// TestEnrichVMs_ExistingHealthWins ensures metadata never overwrites health that
// is already known (mirrors the existing IP/network precedence).
func TestEnrichVMs_ExistingHealthWins(t *testing.T) {
	dir := t.TempDir()

	orig := vmSocketDir
	vmSocketDir = dir
	defer func() { vmSocketDir = orig }()

	t.Setenv("SWARM_STATE_DIR", t.TempDir())

	if err := runtime.WriteVMMetadata(dir, &runtime.VMMetadata{
		ID:     "vm-2",
		Health: &runtime.VMHealth{State: runtime.HealthUnhealthy},
	}); err != nil {
		t.Fatalf("WriteVMMetadata failed: %v", err)
	}

	vm := &runtime.VMState{
		ID:     "vm-2",
		Status: "running",
		Health: &runtime.VMHealth{State: runtime.HealthHealthy},
	}
	enrichVMs([]*runtime.VMState{vm})

	if vm.Health.State != runtime.HealthHealthy {
		t.Errorf("existing health should win, got %q", vm.Health.State)
	}
}

// TestFormatHealthColumn pins the HEALTH column: "-" when nothing is known, the
// published state otherwise.
func TestFormatHealthColumn(t *testing.T) {
	if got := formatHealthColumn(nil); got != "-" {
		t.Errorf("formatHealthColumn(nil) = %q, want %q", got, "-")
	}
	if got := formatHealthColumn(&runtime.VMHealth{}); got != "-" {
		t.Errorf("formatHealthColumn(empty) = %q, want %q", got, "-")
	}
	for _, state := range []string{
		runtime.HealthStarting, runtime.HealthHealthy, runtime.HealthFailing, runtime.HealthUnhealthy,
	} {
		if got := formatHealthColumn(&runtime.VMHealth{State: state}); got != state {
			t.Errorf("formatHealthColumn(%q) = %q", state, got)
		}
	}
}

// TestPrintVMStatus_IncludesHealthSection asserts the node-local status view
// explains a running-but-failing task, including the streak and the last probe
// output (collapsed to one line).
func TestPrintVMStatus_IncludesHealthSection(t *testing.T) {
	vm := &runtime.VMState{
		ID:        "task-1",
		Status:    "running",
		StartTime: time.Now(),
		Health: &runtime.VMHealth{
			State:         runtime.HealthFailing,
			FailingStreak: 2,
			Output:        "line1\nline2",
			Command:       "test -f /tmp/ready",
			UpdatedAt:     time.Now(),
		},
	}

	out := captureStdout(t, func() { printVMStatus(vm) })

	for _, want := range []string{"[Health]", "Failing", "2 consecutive", "test -f /tmp/ready", "line1 line2"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
}

// TestPrintVMStatus_OmitsHealthSection asserts a VM with no healthcheck (a
// plain `vm create` VM, or a task without one) shows no health section at all.
func TestPrintVMStatus_OmitsHealthSection(t *testing.T) {
	vm := &runtime.VMState{ID: "task-2", Status: "running", StartTime: time.Now()}

	out := captureStdout(t, func() { printVMStatus(vm) })

	if strings.Contains(out, "[Health]") {
		t.Errorf("status output has a health section without a healthcheck:\n%s", out)
	}
}

// TestOutputTable_IncludesHealthColumn asserts the list table gains a HEALTH
// column and fills it per VM.
func TestOutputTable_IncludesHealthColumn(t *testing.T) {
	vms := []*runtime.VMState{
		{ID: "task-a", Status: "running", StartTime: time.Now(), Health: &runtime.VMHealth{State: runtime.HealthHealthy}},
		{ID: "task-b", Status: "running", StartTime: time.Now()},
	}

	out := captureStdout(t, func() { _ = outputTable(vms) })

	if !strings.Contains(out, "HEALTH") {
		t.Errorf("table header is missing the HEALTH column:\n%s", out)
	}

	var rowA, rowB string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "task-a"):
			rowA = line
		case strings.HasPrefix(line, "task-b"):
			rowB = line
		}
	}
	if !strings.Contains(rowA, runtime.HealthHealthy) {
		t.Errorf("row for a healthchecked VM is missing its state: %q", rowA)
	}
	if strings.Contains(rowB, runtime.HealthHealthy) {
		t.Errorf("row for a VM without a healthcheck must not claim health: %q", rowB)
	}
}

// TestOutputJSON_IncludesHealth asserts the machine-readable output carries the
// observation, and omits the key entirely when there is none (so a consumer can
// tell "no healthcheck" from "healthy").
func TestOutputJSON_IncludesHealth(t *testing.T) {
	out := captureStdout(t, func() {
		_ = outputJSON([]*runtime.VMState{
			{ID: "with", Status: "running", Health: &runtime.VMHealth{State: runtime.HealthFailing, FailingStreak: 1}},
			{ID: "without", Status: "running"},
		})
	})

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(got) != 2 {
		t.Fatalf("decoded %d VMs, want 2", len(got))
	}

	health, ok := got[0]["health"].(map[string]any)
	if !ok {
		t.Fatalf("VM with a healthcheck has no health object: %v", got[0])
	}
	if health["state"] != runtime.HealthFailing {
		t.Errorf("health.state = %v, want %q", health["state"], runtime.HealthFailing)
	}
	if _, ok := got[1]["health"]; ok {
		t.Errorf("VM without a healthcheck emitted a health key: %v", got[1])
	}
}

// TestCollapseOutput covers the one-line rendering of probe output.
func TestCollapseOutput(t *testing.T) {
	if got := collapseOutput("  a\nb\t c  "); got != "a b c" {
		t.Errorf("collapseOutput = %q, want %q", got, "a b c")
	}

	long := strings.Repeat("x", 300)
	got := collapseOutput(long)
	if len(got) != 203 || !strings.HasSuffix(got, "...") {
		t.Errorf("collapseOutput did not bound the output: len=%d tail=%q", len(got), got[len(got)-3:])
	}
}
