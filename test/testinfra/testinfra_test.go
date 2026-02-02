package testinfra

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestInfra_Prerequisites checks all infrastructure prerequisites
func TestInfra_Prerequisites(t *testing.T) {
	t.Log("Checking infrastructure prerequisites...")

	checks := []struct {
		name  string
		check func(*testing.T)
	}{
		{"GoVersion", checkGoVersion},
		{"Architecture", checkArchitecture},
		{"OperatingSystem", checkOS},
		{"KVM", checkKVM},
		{"Firecracker", checkFirecrackerBinary},
		{"FirecrackerKernel", checkFirecrackerKernel},
		{"ContainerRuntime", checkContainerRuntime},
		{"NetworkPermissions", checkNetworkPermissions},
		{"DiskSpace", checkDiskSpace},
		{"Memory", checkMemory},
	}

	passed := 0
	failed := 0
	skipped := 0

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			// Run check and recover from panics
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("Check panicked: %v", r)
						failed++
					}
				}()
				tc.check(t)
			}()

			// Count results
			if t.Skipped() {
				skipped++
			} else if t.Failed() {
				failed++
			} else {
				passed++
			}
		})
	}

	// Summary
	t.Log("")
	t.Log("=== Infrastructure Check Summary ===")
	t.Logf("Passed: %d", passed)
	t.Logf("Failed: %d", failed)
	t.Logf("Skipped: %d", skipped)
	t.Log("==================================")

	if failed > 0 {
		t.Fatalf("%d infrastructure check(s) failed", failed)
	}
}

// checkGoVersion verifies Go version is 1.21+
func checkGoVersion(t *testing.T) {
	cmd := exec.Command("go", "version")
	output, err := cmd.Output()
	require.NoError(t, err, "Failed to get Go version")

	versionStr := strings.TrimSpace(string(output))
	t.Logf("Go version: %s", versionStr)

	// Parse version (simplified check)
	if strings.Contains(versionStr, "go1.20") || strings.Contains(versionStr, "go1.19") || strings.Contains(versionStr, "go1.18") {
		t.Error("Go version 1.21 or higher is required")
	}
}

// checkArchitecture verifies the architecture is amd64 or arm64
func checkArchitecture(t *testing.T) {
	arch := runtime.GOARCH
	t.Logf("Architecture: %s", arch)

	if arch != "amd64" && arch != "arm64" {
		t.Skipf("Unsupported architecture: %s (amd64 or arm64 required)", arch)
	}
}

// checkOS verifies the operating system is Linux
func checkOS(t *testing.T) {
	os := runtime.GOOS
	t.Logf("Operating System: %s", os)

	if os != "linux" {
		t.Skipf("Unsupported OS: %s (Linux required)", os)
	}
}

// checkKVM verifies KVM device is available
func checkKVM(t *testing.T) {
	kvmPath := "/dev/kvm"

	// Check if device exists
	info, err := os.Stat(kvmPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("KVM device not found at %s", kvmPath)
		}
		t.Fatalf("Failed to check KVM device: %v", err)
	}

	// Check if it's a character device
	if info.Mode()&os.ModeCharDevice == 0 {
		t.Errorf("%s is not a character device", kvmPath)
	}

	// Check if user has read/write permissions
	if info.Mode().Perm()&0600 != 0600 {
		// Try to open it
		f, err := os.OpenFile(kvmPath, os.O_RDWR, 0)
		if err != nil {
			t.Logf("Warning: No read/write access to %s (try: sudo usermod -aG kvm $USER)", kvmPath)
		}
		f.Close()
	}

	t.Logf("KVM device found: %s", kvmPath)
}

// checkFirecrackerBinary verifies Firecracker binary is available
func checkFirecrackerBinary(t *testing.T) {
	cmd := exec.Command("firecracker", "--version")
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Skipf("Firecracker binary not found or not executable: %v", err)
	}

	version := strings.TrimSpace(string(output))
	t.Logf("Firecracker version: %s", version)
}

// checkFirecrackerKernel verifies Firecracker kernel image is available
func checkFirecrackerKernel(t *testing.T) {
	kernelPaths := []string{
		"/home/kali/.local/share/firecracker/vmlinux",
		"/usr/share/firecracker/vmlinux",
		"/boot/vmlinux",
		"/var/lib/firecracker/vmlinux",
	}

	found := false
	for _, path := range kernelPaths {
		if info, err := os.Stat(path); err == nil {
			size := info.Size()
			sizeMB := float64(size) / (1024 * 1024)
			t.Logf("Firecracker kernel found: %s (%.2f MB)", path, sizeMB)
			found = true
			break
		}
	}

	if !found {
		t.Skip("Firecracker kernel not found. Download from: https://github.com/firecracker-microvm/firecracker/releases")
	}
}

// checkContainerRuntime verifies at least one container runtime is available
func checkContainerRuntime(t *testing.T) {
	// Check for Docker
	if _, err := exec.LookPath("docker"); err == nil {
		cmd := exec.Command("docker", "--version")
		output, _ := cmd.Output()
		t.Logf("Docker: %s", strings.TrimSpace(string(output)))
		return
	}

	// Check for Podman
	if _, err := exec.LookPath("podman"); err == nil {
		cmd := exec.Command("podman", "--version")
		output, _ := cmd.Output()
		t.Logf("Podman: %s", strings.TrimSpace(string(output)))
		return
	}

	t.Skip("No container runtime found (docker or podman required)")
}

// checkNetworkPermissions verifies network creation permissions
func checkNetworkPermissions(t *testing.T) {
	// Try to create a dummy bridge
	bridgeName := fmt.Sprintf("test-br-%d", time.Now().Unix())

	// Create bridge
	cmd := exec.Command("ip", "link", "add", bridgeName, "type", "bridge")
	if err := cmd.Run(); err != nil {
		t.Logf("Warning: Cannot create network bridge (may require privileges): %v", err)
		t.Skip("Network permissions check skipped")
		return
	}

	// Clean up bridge
	defer func() {
		cmd := exec.Command("ip", "link", "delete", bridgeName)
		_ = cmd.Run()
	}()

	t.Logf("Network permissions OK (created test bridge: %s)", bridgeName)
}

// checkDiskSpace verifies sufficient disk space
func checkDiskSpace(t *testing.T) {
	// Get current directory
	pwd, err := os.Getwd()
	require.NoError(t, err)

	// Stat filesystem
	var stat syscall.Statfs_t
	err = syscall.Statfs(pwd, &stat)
	if err != nil {
		t.Skipf("Failed to get disk space: %v", err)
		return
	}

	// Calculate available space (in bytes)
	available := stat.Bavail * uint64(stat.Bsize)
	availableGB := float64(available) / (1024 * 1024 * 1024)

	t.Logf("Available disk space: %.2f GB", availableGB)

	// Require at least 5 GB
	if availableGB < 5.0 {
		t.Errorf("Insufficient disk space: %.2f GB (5 GB required)", availableGB)
	}
}

// checkMemory verifies sufficient system memory
func checkMemory(t *testing.T) {
	// Read meminfo
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		t.Skipf("Failed to read memory info: %v", err)
		return
	}

	// Parse MemTotal
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				totalKB, _ := strconv.ParseInt(fields[1], 10, 64)
				totalGB := float64(totalKB) / (1024 * 1024)
				t.Logf("Total memory: %.2f GB", totalGB)

				// Require at least 4 GB
				if totalGB < 4.0 {
					t.Errorf("Insufficient memory: %.2f GB (4 GB recommended)", totalGB)
				}
				return
			}
		}
	}

	t.Skip("Could not determine memory size")
}

// TestInfra_SwarmKitInstallation checks if SwarmKit is installed
func TestInfra_SwarmKitInstallation(t *testing.T) {
	t.Log("Checking SwarmKit installation...")

	// Check for swarmd binary
	cmd := exec.Command("which", "swarmd")
	output, err := cmd.Output()
	if err != nil {
		t.Skipf("swarmd not found in PATH. Install with: go install github.com/moby/swarmkit/cmd/swarmd@latest")
	}

	swarmdPath := strings.TrimSpace(string(output))
	t.Logf("swarmd found: %s", swarmdPath)

	// Check if it's executable
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

	// Run go build
	cmd := exec.CommandContext(ctx, "go", "build", "-o", "/tmp/swarmcracker-test", "./cmd/swarmcracker")
	cmd.Dir = getProjectRoot(t)
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Errorf("Build failed: %v", err)
		t.Logf("Build output:\n%s", string(output))
		return
	}

	t.Log("Build successful")

	// Clean up
	os.Remove("/tmp/swarmcracker-test")
}

// TestInfra_RunUnitTests verifies unit tests pass
func TestInfra_RunUnitTests(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping unit tests in short mode")
	}

	t.Log("Running unit tests...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Run unit tests
	cmd := exec.CommandContext(ctx, "go", "test", "-short", "./pkg/...")
	cmd.Dir = getProjectRoot(t)
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Errorf("Unit tests failed: %v", err)
		t.Logf("Test output:\n%s", string(output))
		return
	}

	t.Log("Unit tests passed")
}

// getProjectRoot finds the project root directory
func getProjectRoot(t *testing.T) string {
	// Start from current directory and look for go.mod
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
