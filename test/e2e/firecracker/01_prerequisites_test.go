//go:build e2e
// +build e2e

package firecracker

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/test/testinfra/checks"
	"github.com/stretchr/testify/require"
)

// TestE2EPrerequisites verifies all prerequisites for Firecracker E2E tests
func TestE2EPrerequisites(t *testing.T) {
	t.Run("Firecracker Binary", func(t *testing.T) {
		fcChecker := checks.NewFirecrackerChecker()
		err := fcChecker.CheckBinary()
		require.NoError(t, err, "Firecracker binary must be available")

		// Get binary info
		cmd := exec.Command(fcChecker.GetBinaryPath(), "--version")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "Firecracker --version must work")

		t.Logf("Firecracker version: %s", string(output))
	})

	t.Run("KVM Device", func(t *testing.T) {
		kernelChecker := checks.NewKernelChecker()

		// Check KVM device exists
		err := kernelChecker.CheckKVMDevice()
		require.NoError(t, err, "KVM device must exist at /dev/kvm")

		// Check KVM permissions
		err = kernelChecker.CheckKVMPermissions()
		if err != nil {
			t.Skipf("KVM permissions check failed: %v (run with sudo or add user to kvm group)", err)
		}

		// Check KVM module is loaded
		err = kernelChecker.CheckKernelModule()
		require.NoError(t, err, "KVM kernel module must be loaded")

		t.Log("KVM is available and accessible")
	})

	t.Run("Firecracker Kernel", func(t *testing.T) {
		fcChecker := checks.NewFirecrackerChecker()
		err := fcChecker.CheckKernel()
		require.NoError(t, err, "Firecracker kernel image must be available")

		kernelPath := fcChecker.GetKernelPath()
		require.NotEmpty(t, kernelPath, "Kernel path must not be empty")

		// Verify kernel file
		info, err := os.Stat(kernelPath)
		require.NoError(t, err, "Kernel file must exist")
		require.True(t, info.Mode().IsRegular(), "Kernel must be a regular file")
		require.Greater(t, info.Size(), int64(1024*1024), "Kernel must be at least 1MB")

		t.Logf("Firecracker kernel found at: %s (size: %d bytes)", kernelPath, info.Size())
	})

	t.Run("Docker Availability", func(t *testing.T) {
		path, err := exec.LookPath("docker")
		require.NoError(t, err, "Docker binary must be available for image extraction")

		cmd := exec.Command(path, "--version")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "Docker --version must work")

		t.Logf("Docker version: %s", string(output))
	})

	t.Run("Network Requirements", func(t *testing.T) {
		netChecker := checks.NewNetworkChecker()

		// Check ip command
		err := netChecker.CheckIPCommand()
		require.NoError(t, err, "ip command must be available")

		// Check bridge support
		err = netChecker.CheckBridgeSupport()
		require.NoError(t, err, "bridge kernel module must be available")

		// Check TAP support
		err = netChecker.CheckTAPSupport()
		require.NoError(t, err, "TAP device support must be available")

		// Note: IP forwarding check is informational
		err = netChecker.CheckIPForwarding()
		if err != nil {
			t.Logf("Warning: %v (IP forwarding should be enabled for VM networking)", err)
		} else {
			t.Log("IP forwarding is enabled")
		}

		t.Log("Network requirements verified")
	})

	t.Run("Directory Permissions", func(t *testing.T) {
		dirs := []string{
			"/var/run/firecracker",
			"/var/lib/firecracker/rootfs",
		}

		for _, dir := range dirs {
			// Try to create directory if it doesn't exist
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Logf("Warning: Cannot create %s: %v (may require sudo)", dir, err)
				continue
			}

			// Check if writable
			testFile := filepath.Join(dir, ".test-write")
			f, err := os.Create(testFile)
			if err != nil {
				t.Logf("Warning: Cannot write to %s: %v (may require sudo)", dir, err)
			} else {
				f.Close()
				os.Remove(testFile)
				t.Logf("Directory %s is writable", dir)
			}
		}
	})

	t.Run("User Permissions", func(t *testing.T) {
		// Check if user is in kvm group
		cmd := exec.Command("groups")
		output, err := cmd.CombinedOutput()
		if err == nil {
			groupsStr := string(output)
			t.Logf("Current user groups: %s", groupsStr)

			// Check for docker group
			cmd = exec.Command("groups")
			output, _ = cmd.CombinedOutput()
			if contains(string(output), "docker") {
				t.Log("User is in docker group")
			} else {
				t.Log("Warning: User is not in docker group (may affect Docker operations)")
			}
		}
	})

	t.Log("All prerequisites verified successfully!")
}

// TestE2EDownloadKernel downloads Firecracker kernel if not present
func TestE2EDownloadKernel(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping kernel download in short mode")
	}

	fcChecker := checks.NewFirecrackerChecker()
	err := fcChecker.CheckKernel()
	if err == nil {
		t.Log("Kernel already present, skipping download")
		return
	}

	t.Log("Kernel not found, attempting to download...")

	destDir := "/home/kali/.local/share/firecracker"
	err = fcChecker.DownloadKernel(destDir)
	if err != nil {
		t.Logf("Warning: Failed to download kernel: %v", err)
		t.Log("Please download manually from:")
		t.Log("  https://github.com/firecracker-microvm/firecracker/releases")
		t.SkipNow()
	}

	t.Log("Kernel downloaded successfully")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
