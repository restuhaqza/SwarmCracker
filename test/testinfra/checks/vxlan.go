package checks

import (
	"fmt"
	"os/exec"
	"strings"
)

// VXLANChecker validates VXLAN overlay networking prerequisites
type VXLANChecker struct{}

// NewVXLANChecker creates a new VXLAN checker
func NewVXLANChecker() *VXLANChecker {
	return &VXLANChecker{}
}

// CheckVXLANModule verifies the VXLAN kernel module is available
func (vc *VXLANChecker) CheckVXLANModule() error {
	// Check if module is loaded
	cmd := exec.Command("lsmod")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list kernel modules: %w", err)
	}

	if !strings.Contains(string(output), "vxlan ") {
		// Try to load it
		cmd = exec.Command("modprobe", "vxlan")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("vxlan module not available: %w", err)
		}
	}

	return nil
}

// CheckVXLANDevice checks if a VXLAN device exists
func (vc *VXLANChecker) CheckVXLANDevice(deviceName string) error {
	cmd := exec.Command("ip", "link", "show", deviceName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vxlan device %s does not exist: %s", deviceName, string(output))
	}
	return nil
}

// CheckBridgeAttachment verifies a VXLAN device is attached to a bridge
func (vc *VXLANChecker) CheckBridgeAttachment(vxlanName, bridgeName string) error {
	cmd := exec.Command("ip", "link", "show", vxlanName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vxlan device %s not found: %w", vxlanName, err)
	}

	if !strings.Contains(string(output), fmt.Sprintf("master %s", bridgeName)) {
		return fmt.Errorf("vxlan device %s is not attached to bridge %s", vxlanName, bridgeName)
	}

	return nil
}

// CheckVXLANFDB checks if forwarding database entries exist for a peer
func (vc *VXLANChecker) CheckVXLANFDB(vxlanName, peerIP string) error {
	cmd := exec.Command("bridge", "fdb", "show", "dev", vxlanName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to show FDB for %s: %w", vxlanName, err)
	}

	if !strings.Contains(string(output), peerIP) {
		return fmt.Errorf("no FDB entry for peer %s on %s", peerIP, vxlanName)
	}

	return nil
}

// CheckProxyARP verifies proxy ARP is enabled on an interface
func (vc *VXLANChecker) CheckProxyARP(interfaceName string) error {
	cmd := exec.Command("sysctl", "-n", fmt.Sprintf("net.ipv4.conf.%s.proxy_arp", interfaceName))
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to read proxy_arp for %s: %w", interfaceName, err)
	}

	if strings.TrimSpace(string(output)) != "1" {
		return fmt.Errorf("proxy ARP is disabled on %s", interfaceName)
	}

	return nil
}

// CheckIPForwardingInterface verifies IP forwarding is enabled on an interface
func (vc *VXLANChecker) CheckIPForwardingInterface(interfaceName string) error {
	cmd := exec.Command("sysctl", "-n", fmt.Sprintf("net.ipv4.conf.%s.forwarding", interfaceName))
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to read forwarding for %s: %w", interfaceName, err)
	}

	if strings.TrimSpace(string(output)) != "1" {
		return fmt.Errorf("IP forwarding is disabled on %s", interfaceName)
	}

	return nil
}

// CheckRouteExists verifies a route exists
func (vc *VXLANChecker) CheckRouteExists(subnet, via, device string) error {
	cmd := exec.Command("ip", "route", "show", subnet)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to show routes: %w", err)
	}

	routeStr := string(output)
	if !strings.Contains(routeStr, "via "+via) {
		return fmt.Errorf("no route to %s via %s (got: %s)", subnet, via, strings.TrimSpace(routeStr))
	}

	if !strings.Contains(routeStr, "dev "+device) {
		return fmt.Errorf("route to %s via %s does not use device %s", subnet, via, device)
	}

	return nil
}

// GetVXLANInterfaces returns a list of VXLAN interfaces on the system
func (vc *VXLANChecker) GetVXLANInterfaces() ([]string, error) {
	cmd := exec.Command("ip", "-d", "link", "show", "type", "vxlan")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list VXLAN interfaces: %w", err)
	}

	var interfaces []string
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "vxlan") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				// Format: "2: vxlan0: <...>"
				name := strings.TrimSuffix(fields[1], ":")
				interfaces = append(interfaces, name)
			}
		}
	}

	return interfaces, nil
}

// Validate performs all VXLAN prerequisite checks
func (vc *VXLANChecker) Validate() []error {
	var errors []error

	if err := vc.CheckVXLANModule(); err != nil {
		errors = append(errors, err)
	}

	return errors
}
