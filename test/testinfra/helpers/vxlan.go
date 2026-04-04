package helpers

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// vxlanIPCmd returns the appropriate prefix for network commands.
func vxlanIPCmd() []string {
	if os.Geteuid() == 0 {
		return nil
	}
	return []string{"sudo"}
}

// VXLANHelper provides helper methods for VXLAN overlay testing
type VXLANHelper struct{}

// NewVXLANHelper creates a new VXLAN helper
func NewVXLANHelper() *VXLANHelper {
	return &VXLANHelper{}
}

// VXLANSetup holds configuration for a VXLAN overlay
type VXLANSetup struct {
	BridgeName     string
	VXLANName      string
	VXLANID        int
	OverlayIP      string
	PhysInterface  string
	LocalIP        string
	PeerIPs        []string
	RemoteSubnets  []RemoteSubnet
}

// RemoteSubnet defines a route to a remote worker's VM subnet
type RemoteSubnet struct {
	Subnet string // e.g., "192.168.128.0/24"
	Via    string // e.g., "10.30.0.2"
	Device string // e.g., "swarm-br0"
}

// cmd wraps exec.Command with sudo if needed
func (vh *VXLANHelper) cmd(args ...string) *exec.Cmd {
	if os.Geteuid() == 0 {
		return exec.Command(args[0], args[1:]...)
	}
	sudoArgs := append([]string{"sudo"}, args...)
	return exec.Command(sudoArgs[0], sudoArgs[1:]...)
}

// SetupVXLAN creates and configures a VXLAN overlay network
func (vh *VXLANHelper) SetupVXLAN(setup *VXLANSetup) error {
	if err := vh.cmd("modprobe", "vxlan").Run(); err != nil {
		return fmt.Errorf("failed to load vxlan module: %w", err)
	}

	cmd := vh.cmd("ip", "link", "add", setup.VXLANName,
		"type", "vxlan",
		"id", fmt.Sprintf("%d", setup.VXLANID),
		"dstport", "4789",
		"dev", setup.PhysInterface,
		"local", setup.LocalIP,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create VXLAN %s: %s (%w)", setup.VXLANName, string(output), err)
	}

	cmd = vh.cmd("ip", "link", "set", setup.VXLANName, "master", setup.BridgeName)
	if output, err := cmd.CombinedOutput(); err != nil {
		vh.TeardownVXLAN(setup)
		return fmt.Errorf("failed to attach VXLAN to bridge: %s (%w)", string(output), err)
	}

	cmd = vh.cmd("ip", "addr", "add", setup.OverlayIP, "dev", setup.BridgeName)
	if output, err := cmd.CombinedOutput(); err != nil {
		if !strings.Contains(string(output), "File exists") {
			vh.TeardownVXLAN(setup)
			return fmt.Errorf("failed to add overlay IP: %s (%w)", string(output), err)
		}
	}

	if err := vh.cmd("ip", "link", "set", setup.VXLANName, "up").Run(); err != nil {
		vh.TeardownVXLAN(setup)
		return fmt.Errorf("failed to bring VXLAN up: %w", err)
	}

	for _, peer := range setup.PeerIPs {
		cmd = vh.cmd("bridge", "fdb", "append",
			"to", "00:00:00:00:00:00",
			"dst", peer,
			"dev", setup.VXLANName,
		)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add FDB entry for peer %s: %w", peer, err)
		}
	}

	for _, rs := range setup.RemoteSubnets {
		cmd = vh.cmd("ip", "route", "add", rs.Subnet, "via", rs.Via, "dev", rs.Device)
		if output, err := cmd.CombinedOutput(); err != nil {
			if !strings.Contains(string(output), "File exists") {
				return fmt.Errorf("failed to add route %s via %s: %s (%w)", rs.Subnet, rs.Via, string(output), err)
			}
		}
	}

	vh.cmd("sysctl", "-w", fmt.Sprintf("net.ipv4.conf.%s.proxy_arp=1", setup.BridgeName)).Run()
	vh.cmd("sysctl", "-w", fmt.Sprintf("net.ipv4.conf.%s.forwarding=1", setup.BridgeName)).Run()
	vh.cmd("sysctl", "-w", "net.ipv4.ip_forward=1").Run()

	return nil
}

// TeardownVXLAN removes VXLAN overlay configuration
func (vh *VXLANHelper) TeardownVXLAN(setup *VXLANSetup) error {
	for _, rs := range setup.RemoteSubnets {
		vh.cmd("ip", "route", "del", rs.Subnet, "via", rs.Via, "dev", rs.Device).Run()
	}
	vh.cmd("ip", "addr", "del", setup.OverlayIP, "dev", setup.BridgeName).Run()
	vh.cmd("ip", "link", "delete", setup.VXLANName).Run()
	return nil
}

// PingVXLANPeer tests connectivity to a VXLAN peer
func (vh *VXLANHelper) PingVXLANPeer(targetIP string, count int, timeout time.Duration) (bool, string, error) {
	cmd := exec.Command("ping", "-c", fmt.Sprintf("%d", count), "-W", fmt.Sprintf("%.0f", timeout.Seconds()), targetIP)
	output, err := cmd.CombinedOutput()
	result := string(output)

	if err != nil {
		if strings.Contains(result, "packets received") {
			return false, result, nil
		}
		return false, result, fmt.Errorf("ping failed: %w", err)
	}

	if strings.Contains(result, "0% packet loss") {
		return true, result, nil
	}

	return false, result, nil
}

// ParsePacketLoss extracts packet loss percentage from ping output
func (vh *VXLANHelper) ParsePacketLoss(pingOutput string) string {
	lines := strings.Split(pingOutput, "\n")
	for _, line := range lines {
		if strings.Contains(line, "packet loss") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "loss" && i > 0 {
					return parts[i-1] + " loss"
				}
			}
		}
	}
	return "unknown"
}

// ParseLatency extracts avg latency from ping output
func (vh *VXLANHelper) ParseLatency(pingOutput string) string {
	lines := strings.Split(pingOutput, "\n")
	for _, line := range lines {
		if strings.Contains(line, "rtt min/avg/max") {
			parts := strings.Split(line, "=")
			if len(parts) >= 2 {
				stats := strings.Fields(strings.TrimSpace(parts[1]))
				if len(stats) >= 2 {
					return "avg " + stats[1] + " " + stats[3]
				}
			}
		}
	}
	return "unknown"
}

// GetInterfaceIPs returns all IP addresses on an interface
func (vh *VXLANHelper) GetInterfaceIPs(interfaceName string) ([]string, error) {
	cmd := vh.cmd("ip", "addr", "show", interfaceName)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get IPs for %s: %w", interfaceName, err)
	}

	var ips []string
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "inet ") {
			parts := strings.Fields(line)
			for _, p := range parts {
				if strings.HasPrefix(p, "inet ") {
					ip := strings.TrimPrefix(p, "inet ")
					ips = append(ips, ip)
				}
			}
		}
	}

	return ips, nil
}

// GetForwardingDB returns the VXLAN forwarding database entries
func (vh *VXLANHelper) GetForwardingDB(vxlanName string) (string, error) {
	cmd := vh.cmd("bridge", "fdb", "show", "dev", vxlanName)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get FDB: %w", err)
	}
	return string(output), nil
}

// GetBridgePorts returns TAP devices attached to a bridge
func (vh *VXLANHelper) GetBridgePorts(bridgeName string) ([]string, error) {
	cmd := vh.cmd("bridge", "link", "show", "master", bridgeName)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get bridge ports: %w", err)
	}

	var ports []string
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 && !strings.Contains(line, bridgeName) {
			ports = append(ports, fields[1])
		}
	}

	return ports, nil
}
