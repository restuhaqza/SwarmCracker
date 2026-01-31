package checks

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// NetworkChecker validates network configuration
type NetworkChecker struct{}

// NewNetworkChecker creates a new network checker
func NewNetworkChecker() *NetworkChecker {
	return &NetworkChecker{}
}

// CheckIPCommand verifies ip command is available
func (nc *NetworkChecker) CheckIPCommand() error {
	_, err := exec.LookPath("ip")
	if err != nil {
		return fmt.Errorf("ip command not found (install iproute2 package)")
	}
	return nil
}

// CheckBridgeSupport verifies bridge support is available
func (nc *NetworkChecker) CheckBridgeSupport() error {
	// Check if bridge module is loaded
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return fmt.Errorf("failed to read /proc/modules: %w", err)
	}

	if !strings.Contains(string(data), "bridge ") {
		// Try to load it
		cmd := exec.Command("modprobe", "bridge")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("bridge module not available and failed to load: %w", err)
		}
	}

	return nil
}

// CheckTAPSupport verifies TAP device support
func (nc *NetworkChecker) CheckTAPSupport() error {
	// Check if tun module is loaded
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return fmt.Errorf("failed to read /proc/modules: %w", err)
	}

	if !strings.Contains(string(data), "tun ") {
		// Try to load it
		cmd := exec.Command("modprobe", "tun")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("tun module not available and failed to load: %w", err)
		}
	}

	// Check /dev/net/tun
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return fmt.Errorf("/dev/net/tun not found: %w", err)
	}

	return nil
}

// CheckIPForwarding verifies IP forwarding is enabled
func (nc *NetworkChecker) CheckIPForwarding() error {
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return fmt.Errorf("failed to check IP forwarding: %w", err)
	}

	value := strings.TrimSpace(string(data))
	if value != "1" {
		return fmt.Errorf("IP forwarding is disabled (current: %s). Enable with: sudo sysctl -w net.ipv4.ip_forward=1", value)
	}

	return nil
}

// CheckBridgeExists checks if a bridge exists
func (nc *NetworkChecker) CheckBridgeExists(bridgeName string) error {
	// Use ip command to check
	cmd := exec.Command("ip", "link", "show", bridgeName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bridge %s does not exist: %w", bridgeName, err)
	}

	if !strings.Contains(string(output), bridgeName) {
		return fmt.Errorf("bridge %s not found", bridgeName)
	}

	return nil
}

// CreateTestBridge creates a test bridge for validation
func (nc *NetworkChecker) CreateTestBridge(bridgeName string) (func(), error) {
	// Create bridge
	cmd := exec.Command("ip", "link", "add", bridgeName, "type", "bridge")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to create bridge %s: %w", bridgeName, err)
	}

	// Bring it up
	cmd = exec.Command("ip", "link", "set", bridgeName, "up")
	if err := cmd.Run(); err != nil {
		// Cleanup on failure
		nc.DeleteTestBridge(bridgeName)
		return nil, fmt.Errorf("failed to bring up bridge %s: %w", bridgeName, err)
	}

	cleanup := func() {
		nc.DeleteTestBridge(bridgeName)
	}

	return cleanup, nil
}

// DeleteTestBridge deletes a test bridge
func (nc *NetworkChecker) DeleteTestBridge(bridgeName string) error {
	// Bring it down
	cmd := exec.Command("ip", "link", "set", bridgeName, "down")
	_ = cmd.Run() // Ignore errors

	// Delete it
	cmd = exec.Command("ip", "link", "delete", bridgeName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete bridge %s: %w", bridgeName, err)
	}

	return nil
}

// CheckNetworkPermissions checks if we can create network devices
func (nc *NetworkChecker) CheckNetworkPermissions() error {
	bridgeName := fmt.Sprintf("test-swarmbr-%d", os.Getpid())

	cleanup, err := nc.CreateTestBridge(bridgeName)
	if err != nil {
		return fmt.Errorf("no permission to create network devices (try running with sudo/CAP_NET_ADMIN): %w", err)
	}

	if cleanup != nil {
		cleanup()
	}

	return nil
}

// Validate performs all network checks
func (nc *NetworkChecker) Validate() []error {
	errors := make([]error, 0)

	if err := nc.CheckIPCommand(); err != nil {
		errors = append(errors, err)
	}

	if err := nc.CheckBridgeSupport(); err != nil {
		errors = append(errors, err)
	}

	if err := nc.CheckTAPSupport(); err != nil {
		errors = append(errors, err)
	}

	if err := nc.CheckIPForwarding(); err != nil {
		errors = append(errors, err)
	}

	return errors
}
