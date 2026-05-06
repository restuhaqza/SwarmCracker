package health

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

// HealthStatus represents the overall health check result.
type HealthStatus struct {
	Healthy bool                   `json:"healthy"`
	Checks  map[string]CheckResult `json:"checks"`
}

// CheckResult represents the result of a single health check.
type CheckResult struct {
	Status  string `json:"status"`  // "ok" or "error"
	Message string `json:"message"`
}

// Checker performs health checks for swarmd-firecracker.
type Checker struct {
	BridgeName      string
	FirecrackerPath string
}

// NewChecker creates a new health checker.
func NewChecker(bridgeName, fcPath string) *Checker {
	return &Checker{
		BridgeName:      bridgeName,
		FirecrackerPath: fcPath,
	}
}

// ServeHTTP implements http.Handler for the health check endpoint.
func (c *Checker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := c.Check()

	w.Header().Set("Content-Type", "application/json")
	if !status.Healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	json.NewEncoder(w).Encode(status)
}

// Check performs all health checks and returns the overall status.
func (c *Checker) Check() HealthStatus {
	checks := make(map[string]CheckResult)
	allHealthy := true

	// Check KVM
	kvmResult := c.checkKVM()
	checks["kvm"] = kvmResult
	if kvmResult.Status == "error" {
		allHealthy = false
	}

	// Check bridge
	bridgeResult := c.checkBridge()
	checks["bridge"] = bridgeResult
	if bridgeResult.Status == "error" {
		allHealthy = false
	}

	// Check firecracker
	fcResult := c.checkFirecracker()
	checks["firecracker"] = fcResult
	if fcResult.Status == "error" {
		allHealthy = false
	}

	return HealthStatus{
		Healthy: allHealthy,
		Checks:  checks,
	}
}

// checkKVM verifies that /dev/kvm exists and is readable.
func (c *Checker) checkKVM() CheckResult {
	kvmPath := "/dev/kvm"

	// Check if /dev/kvm exists
	info, err := os.Stat(kvmPath)
	if err != nil {
		if os.IsNotExist(err) {
			return CheckResult{
				Status:  "error",
				Message: "/dev/kvm does not exist",
			}
		}
		return CheckResult{
			Status:  "error",
			Message: fmt.Sprintf("failed to stat /dev/kvm: %v", err),
		}
	}

	// Check if it's a character device (KVM should be a char device)
	if info.Mode()&os.ModeCharDevice == 0 {
		return CheckResult{
			Status:  "error",
			Message: "/dev/kvm is not a character device",
		}
	}

	// Check if readable
	file, err := os.OpenFile(kvmPath, os.O_RDONLY, 0)
	if err != nil {
		return CheckResult{
			Status:  "error",
			Message: fmt.Sprintf("/dev/kvm is not readable: %v", err),
		}
	}
	file.Close()

	return CheckResult{
		Status:  "ok",
		Message: "KVM is accessible",
	}
}

// checkBridge verifies that the network bridge exists.
func (c *Checker) checkBridge() CheckResult {
	bridgePath := filepath.Join("/sys/class/net", c.BridgeName)

	// Check if the bridge exists in /sys/class/net
	info, err := os.Stat(bridgePath)
	if err != nil {
		if os.IsNotExist(err) {
			return CheckResult{
				Status:  "error",
				Message: fmt.Sprintf("bridge %s does not exist", c.BridgeName),
			}
		}
		return CheckResult{
			Status:  "error",
			Message: fmt.Sprintf("failed to check bridge: %v", err),
		}
	}

	// Check if it's a directory (network interfaces show as directories in sysfs)
	if !info.IsDir() {
		return CheckResult{
			Status:  "error",
			Message: fmt.Sprintf("bridge %s is not a valid network interface", c.BridgeName),
		}
	}

	return CheckResult{
		Status:  "ok",
		Message: fmt.Sprintf("bridge %s exists", c.BridgeName),
	}
}

// checkFirecracker verifies that the firecracker binary is in PATH and executable.
func (c *Checker) checkFirecracker() CheckResult {
	// If a specific path is provided, check that
	if c.FirecrackerPath != "" && c.FirecrackerPath != "firecracker" {
		info, err := os.Stat(c.FirecrackerPath)
		if err != nil {
			if os.IsNotExist(err) {
				return CheckResult{
					Status:  "error",
					Message: fmt.Sprintf("firecracker binary not found at %s", c.FirecrackerPath),
				}
			}
			return CheckResult{
				Status:  "error",
				Message: fmt.Sprintf("failed to stat firecracker: %v", err),
			}
		}

		// Check if executable
		if info.Mode()&0111 == 0 {
			return CheckResult{
				Status:  "error",
				Message: fmt.Sprintf("firecracker at %s is not executable", c.FirecrackerPath),
			}
		}

		return CheckResult{
			Status:  "ok",
			Message: fmt.Sprintf("firecracker found at %s", c.FirecrackerPath),
		}
	}

	// Look up firecracker in PATH
	path, err := exec.LookPath("firecracker")
	if err != nil {
		return CheckResult{
			Status:  "error",
			Message: "firecracker binary not found in PATH",
		}
	}

	return CheckResult{
		Status:  "ok",
		Message: fmt.Sprintf("firecracker found at %s", path),
	}
}