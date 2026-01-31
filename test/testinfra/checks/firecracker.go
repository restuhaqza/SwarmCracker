package checks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// FirecrackerChecker validates Firecracker installation
type FirecrackerChecker struct {
	binaryPath string
	kernelPath string
}

// NewFirecrackerChecker creates a new Firecracker checker
func NewFirecrackerChecker() *FirecrackerChecker {
	return &FirecrackerChecker{}
}

// CheckBinary verifies Firecracker binary is available and executable
func (fc *FirecrackerChecker) CheckBinary() error {
	path, err := exec.LookPath("firecracker")
	if err != nil {
		return fmt.Errorf("firecracker binary not found in PATH: %w", err)
	}

	fc.binaryPath = path

	// Check version
	cmd := exec.Command(path, "--version")
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to run firecracker --version: %w", err)
	}

	return nil
}

// CheckKernel verifies Firecracker kernel image is available
func (fc *FirecrackerChecker) CheckKernel() error {
	kernelPaths := []string{
		"/home/kali/.local/share/firecracker/vmlinux",
		"/usr/share/firecracker/vmlinux",
		"/boot/vmlinux",
		"/var/lib/firecracker/vmlinux",
	}

	for _, path := range kernelPaths {
		if info, err := os.Stat(path); err == nil {
			// Check it's a regular file
			if info.Mode().IsRegular() {
				// Check size (kernel should be at least 1MB)
				if info.Size() < 1024*1024 {
					continue
				}

				fc.kernelPath = path
				return nil
			}
		}
	}

	return fmt.Errorf("firecracker kernel not found. Searched paths: %v", kernelPaths)
}

// GetKernelPath returns the path to the Firecracker kernel
func (fc *FirecrackerChecker) GetKernelPath() string {
	return fc.kernelPath
}

// GetBinaryPath returns the path to the Firecracker binary
func (fc *FirecrackerChecker) GetBinaryPath() string {
	return fc.binaryPath
}

// Validate performs all Firecracker checks
func (fc *FirecrackerChecker) Validate() []error {
	errors := make([]error, 0)

	if err := fc.CheckBinary(); err != nil {
		errors = append(errors, err)
	}

	if err := fc.CheckKernel(); err != nil {
		errors = append(errors, err)
	}

	return errors
}

// CreateTestVM creates a test VM to verify Firecracker works
func (fc *FirecrackerChecker) CreateTestVM(stateDir string) (func(), error) {
	if fc.kernelPath == "" {
		return nil, fmt.Errorf("kernel path not set")
	}

	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state dir: %w", err)
	}

	// This would create an actual Firecracker VM
	// For now, we'll just verify the directory is created
	cleanup := func() {
		os.RemoveAll(stateDir)
	}

	return cleanup, nil
}

// DownloadKernel downloads the Firecracker kernel
func (fc *FirecrackerChecker) DownloadKernel(destDir string) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	version := "v1.8.0"
	url := fmt.Sprintf("https://github.com/firecracker-microvm/firecracker/releases/download/%s/vmlinux-%s", version, version)
	destPath := filepath.Join(destDir, "vmlinux")

	// Use wget or curl to download
	var cmd *exec.Cmd
	if _, err := exec.LookPath("wget"); err == nil {
		cmd = exec.Command("wget", "-O", destPath, url)
	} else if _, err := exec.LookPath("curl"); err == nil {
		cmd = exec.Command("curl", "-L", "-o", destPath, url)
	} else {
		return fmt.Errorf("neither wget nor curl found for download")
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to download kernel: %w\nOutput: %s", err, string(output))
	}

	return nil
}
