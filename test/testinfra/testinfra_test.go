package testinfra

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInfra_Prerequisites runs all checks via the unified Runner.
func TestInfra_Prerequisites(t *testing.T) {
	t.Log("Checking infrastructure prerequisites (via unified Runner)...")

	runner := NewRunner()
	report := runner.Run(context.Background())

	// Print results
	for _, c := range report.Results {
		icon := map[string]string{"pass": "✓", "fail": "✗", "skip": "⚠"}[c.Status]
		sev := string(c.Severity)
		t.Logf("  %s %s [%s] %s", icon, c.Name, sev, c.Message)

		if c.Status == "fail" && c.Severity == "required" {
			t.Errorf("%s FAILED: %s", c.Name, c.Message)
		}
	}

	t.Logf("Passed: %d  Failed: %d  Skipped: %d  Ready: %v",
		report.Passed, report.Failed, report.Skipped, report.Ready)

	if !report.Ready {
		t.Fatal("Some required checks failed")
	}
}

// TestInfra_JSONOutput verifies the runner produces valid JSON.
func TestInfra_JSONOutput(t *testing.T) {
	runner := NewRunner()
	report := runner.Run(context.Background())

	assert.NotEmpty(t, report.Timestamp)
	assert.NotEmpty(t, report.Hostname)
	assert.NotEmpty(t, report.Arch)
	assert.NotZero(t, len(report.Results))
	// All checks should have valid statuses
	for _, c := range report.Results {
		assert.Contains(t, []string{"pass", "fail", "skip"}, c.Status,
			"check %s has invalid status: %s", c.Name, c.Status)
	}
}

// TestInfra_RequiredOnly verifies Ready is false when a required check fails.
func TestInfra_RequiredOnly(t *testing.T) {
	runner := NewRunner()
	report := runner.Run(context.Background())

	// Ready should be true (we expect required checks to pass on dev machine)
	// This test just validates the logic: Ready = no required failures
	for _, c := range report.Results {
		if c.Severity == "required" && c.Status == "fail" {
			assert.False(t, report.Ready, "Ready should be false when required checks fail")
			return
		}
	}
	assert.True(t, report.Ready, "Ready should be true when all required checks pass")
}

// TestInfra_SwarmKitInstallation checks if SwarmKit is installed
func TestInfra_SwarmKitInstallation(t *testing.T) {
	t.Log("Checking SwarmKit installation...")

	cmd := exec.Command("which", "swarmd")
	output, err := cmd.Output()
	if err != nil {
		t.Skipf("swarmd not found in PATH. Install with: go install github.com/moby/swarmkit/cmd/swarmd@latest")
	}

	swarmdPath := filepath.Clean(strings.TrimSpace(string(output)))
	t.Logf("swarmd found: %s", swarmdPath)

	info, err := os.Stat(swarmdPath)
	require.NoError(t, err, "Failed to stat swarmd")

	if info.Mode().Perm()&0111 == 0 {
		t.Errorf("swarmd is not executable")
	}
}

// TestInfra_BuildSwarmCracker verifies SwarmCracker can be built
func TestInfra_BuildSwarmCracker(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping build test in short mode")
	}

	t.Log("Building SwarmCracker...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", "/tmp/swarmcracker-test", "./cmd/swarmcracker")
	cmd.Dir = getProjectRoot(t)
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Errorf("Build failed: %v", err)
		t.Logf("Build output:\n%s", string(output))
		return
	}

	t.Log("Build successful")
	os.Remove("/tmp/swarmcracker-test")
}

// REMOVED: TestInfra_RunUnitTests — unit tests are already run by CI's test job.
// The testinfra package checks infrastructure readiness, not code correctness.

// getProjectRoot finds the project root directory
func getProjectRoot(t *testing.T) string {
	dir, err := os.Getwd()
	require.NoError(t, err)

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("Could not find project root (go.mod not found)")
		}
		dir = parent
	}
}
