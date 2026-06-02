package e2e

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
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

// hasSwarmCracker checks if swarmcracker binary is available
func hasSwarmCracker() bool {
	_, err := exec.LookPath("swarmcracker")
	return err == nil
}

// hasFirecracker checks if firecracker binary is available
func hasFirecracker() bool {
	_, err := exec.LookPath("firecracker")
	return err == nil
}

// hasDocker checks if docker is available
func hasDocker() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// hasPodman checks if podman is available
func hasPodman() bool {
	_, err := exec.LookPath("podman")
	return err == nil
}

// hasKVM checks if KVM device is available
func hasKVM() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

// hasKernel checks if Firecracker kernel is available
func hasKernel() bool {
	kernelPaths := []string{
		"/home/kali/.local/share/firecracker/vmlinux",
		"/usr/share/firecracker/vmlinux",
		"/boot/vmlinux",
		"/var/lib/firecracker/vmlinux",
	}
	for _, path := range kernelPaths {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// checkPrerequisites checks prerequisites and returns which are available
func checkPrerequisites(t *testing.T) (bool, bool, bool, bool) {
	swarmd := hasSwarmd()
	fc := hasFirecracker()
	kvm := hasKVM()
	kernel := hasKernel()

	if !swarmd {
		t.Log("swarmd not found, skipping test")
	}
	if !fc {
		t.Log("Firecracker not found, skipping test")
	}
	if !kvm {
		t.Log("KVM not available, skipping test")
	}
	if !kernel {
		t.Log("Firecracker kernel not found, skipping test")
	}

	return swarmd, fc, kvm, kernel
}

// requirePrerequisites fails the test if prerequisites are not met
func requirePrerequisites(t *testing.T) {
	swarmd, fc, kvm, kernel := checkPrerequisites(t)
	require.True(t, swarmd, "swarmd is required for E2E tests")
	require.True(t, fc, "Firecracker is required for E2E tests")
	require.True(t, kvm, "KVM is required for E2E tests")
	require.True(t, kernel, "Firecracker kernel is required for E2E tests")
}
