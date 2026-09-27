package e2e

import (
	"os/exec"
	"testing"
)

// TestE2E_Prerequisites delegates to the unified testinfra package.
// All prerequisite checks live in test/testinfra/ — the single source of truth.
//
// Run standalone:
//
//	go run ./test/testinfra/cmd/...           # text output
//	go run ./test/testinfra/cmd/... --json    # JSON for CI/scripts
func TestE2E_Prerequisites(t *testing.T) {
	t.Log("Infrastructure prerequisites are validated by test/testinfra/")
	t.Log("Run: go test -v ./test/testinfra/...")
	t.Log("Or:   go run ./test/testinfra/cmd/... --json")
	t.Skip("Delegate to test/testinfra/ for full prerequisite report")
}

// TestE2E_PlannedScenarios documents planned E2E scenarios for future implementation.
// These tests require a running swarmd-firecracker executor on a multi-node cluster.
// See full_workflow_test.go for tests that use swarmctl (no Docker required).
func TestE2E_PlannedScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E in short mode")
	}

	scenarios := []struct {
		name     string
		status   string
		requires string
	}{
		{"ManagerOnly", "TODO", "swarmd with SwarmCracker executor"},
		{"ClusterFormation", "TODO", "3-node cluster via Ansible"},
		{"ServiceDeploy", "partially in full_workflow_test.go", "swarmd-firecracker executor"},
		{"ServiceScaling", "partially in full_workflow_test.go", "swarmd-firecracker executor"},
		{"FailureRecovery", "TODO", "multi-node cluster"},
		{"NetworkIsolation", "TODO", "VXLAN + multi-node"},
		{"SnapshotRestore", "TODO", "snapshot config deployed"},
	}

	t.Log("Planned E2E scenarios:")
	for _, s := range scenarios {
		t.Logf("  [%s] %s — %s", s.status, s.name, s.requires)
	}
}

// hasSwarmd checks if swarmd binary is available
func hasSwarmd() bool {
	_, err := exec.LookPath("swarmd")
	return err == nil
}
func hasFirecracker() bool {
	_, err := exec.LookPath("firecracker")
	return err == nil
}
