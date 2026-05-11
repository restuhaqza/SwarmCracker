// Package network provides CNI-compatible TAP device operations
// for Firecracker microVMs.
package network

import (
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/rs/zerolog/log"
)

// TAPDevice represents a TAP network interface
type TAPDevice struct {
	Name    string
	MAC     string
	Bridge  string
	IP      string
	Netmask string
}

// CreateTAPDevice creates a TAP device and connects it to a bridge.
// This is a standalone function usable by the CNI plugin.
func CreateTAPDevice(name, bridge string) (*TAPDevice, error) {
	return CreateTAPDeviceWithExecutor(name, bridge, NewDefaultTAPExecutor())
}

// CreateTAPDeviceWithExecutor creates a TAP device using injectable executor.
func CreateTAPDeviceWithExecutor(name, bridge string, executor TAPExecutor) (*TAPDevice, error) {
	log.Info().
		Str("tap", name).
		Str("bridge", bridge).
		Msg("Creating TAP device")

	// Ensure clean state by removing existing device if any
	cleanupCmd := executor.Command("ip", "link", "delete", name)
	executor.Run(cleanupCmd) // Ignore error, device may not exist

	// Create TAP device
	createCmd := executor.Command("ip", "tuntap", "add", name, "mode", "tap")
	if err := executor.Run(createCmd); err != nil {
		return nil, fmt.Errorf("failed to create TAP device %s: %w", name, err)
	}

	// Bring TAP up
	upCmd := executor.Command("ip", "link", "set", name, "up")
	if err := executor.Run(upCmd); err != nil {
		cleanupCmd := executor.Command("ip", "link", "delete", name)
		executor.Run(cleanupCmd)
		return nil, fmt.Errorf("failed to bring TAP up: %w", err)
	}

	// Connect to bridge
	if bridge != "" {
		masterCmd := executor.Command("ip", "link", "set", name, "master", bridge)
		if err := executor.Run(masterCmd); err != nil {
			cleanupCmd := executor.Command("ip", "link", "delete", name)
			executor.Run(cleanupCmd)
			return nil, fmt.Errorf("failed to connect TAP to bridge %s: %w", bridge, err)
		}
	}

	// Get MAC address
	mac, err := getTAPMACWithExecutor(name, executor)
	if err != nil {
		// Non-critical, use placeholder
		mac = "00:00:00:00:00:00"
	}

	return &TAPDevice{
		Name:   name,
		MAC:    mac,
		Bridge: bridge,
	}, nil
}

// DeleteTAPDevice removes a TAP device.
func DeleteTAPDevice(name string) error {
	return DeleteTAPDeviceWithExecutor(name, NewDefaultTAPExecutor())
}

// DeleteTAPDeviceWithExecutor removes a TAP device using injectable executor.
func DeleteTAPDeviceWithExecutor(name string, executor TAPExecutor) error {
	log.Info().Str("tap", name).Msg("Deleting TAP device")

	// Remove from bridge first (if attached)
	nomasterCmd := executor.Command("ip", "link", "set", name, "nomaster")
	if output, err := executor.CombinedOutput(nomasterCmd); err != nil {
		log.Debug().Err(err).Str("output", string(output)).Msg("Failed to detach from bridge")
	}

	// Delete TAP device
	deleteCmd := executor.Command("ip", "link", "delete", name)
	if output, err := executor.CombinedOutput(deleteCmd); err != nil {
		log.Error().Err(err).Str("output", string(output)).Msg("Failed to delete TAP")
		return fmt.Errorf("failed to delete TAP device %s: %w (output: %s)", name, err, string(output))
	}

	log.Info().Str("tap", name).Msg("TAP device deleted successfully")
	return nil
}

// TAPDeviceExists checks if a TAP device exists.
func TAPDeviceExists(name string) (bool, error) {
	return TAPDeviceExistsWithExecutor(name, NewDefaultTAPExecutor())
}

// TAPDeviceExistsWithExecutor checks if a TAP device exists using injectable executor.
func TAPDeviceExistsWithExecutor(name string, executor TAPExecutor) (bool, error) {
	showCmd := executor.Command("ip", "link", "show", name)
	err := executor.Run(showCmd)
	if err != nil {
		// Device doesn't exist
		//nolint:nilerr
		return false, nil
	}
	return true, nil
}

// SetupVXLANFDB adds FDB entries for VXLAN peers.
// This enables cross-node communication via VXLAN overlay.
func SetupVXLANFDB(tapName string, peers []string) error {
	if len(peers) == 0 {
		return nil
	}

	log.Info().
		Str("tap", tapName).
		Strs("peers", peers).
		Msg("Setting up VXLAN FDB entries")

	// Get VXLAN interface name (typically swarm-br0-vxlan or br-<net>-vxlan)
	// The FDB entry forwards broadcast/multicast to the VXLAN tunnel
	vxlanInterface := "swarm-br0-vxlan"

	// Add FDB entry for each peer
	for _, peer := range peers {
		peer = strings.TrimSpace(peer)
		if peer == "" {
			continue
		}

		// Add all-zeros MAC forwarding to peer (for broadcast/unknown destinations)
		cmd := exec.Command("bridge", "fdb", "add", "00:00:00:00:00:00", "dev", vxlanInterface, "dst", peer)
		if err := cmd.Run(); err != nil {
			log.Warn().
				Err(err).
				Str("peer", peer).
				Msg("Failed to add VXLAN FDB entry")
			// Continue with other peers
		}
	}

	return nil
}

// getTAPMAC retrieves the MAC address of a TAP device.
func getTAPMAC(name string) (string, error) {
	return getTAPMACWithExecutor(name, NewDefaultTAPExecutor())
}

// getTAPMACWithExecutor retrieves the MAC address using injectable executor.
func getTAPMACWithExecutor(name string, executor TAPExecutor) (string, error) {
	// Use ip link show to get MAC
	cmd := executor.Command("ip", "-br", "link", "show", name)
	output, err := executor.Output(cmd)
	if err != nil {
		return "", err
	}

	// Parse output: "tap-xxx: <STATE> ff:ff:ff:ff:ff:ff ..."
	fields := strings.Fields(string(output))
	if len(fields) >= 3 {
		return fields[2], nil
	}

	return "", fmt.Errorf("could not parse MAC from output: %s", output)
}

// ConfigureTAPIP sets the IP address on a TAP device.
// This is typically used for the bridge side, not the VM side.
func ConfigureTAPIP(name, cidr string) error {
	return ConfigureTAPIPWithExecutor(name, cidr, NewDefaultTAPExecutor())
}

// ConfigureTAPIPWithExecutor sets the IP address using injectable executor.
func ConfigureTAPIPWithExecutor(name, cidr string, executor TAPExecutor) error {
	log.Info().
		Str("tap", name).
		Str("cidr", cidr).
		Msg("Configuring TAP IP address")

	cmd := executor.Command("ip", "addr", "add", cidr, "dev", name)
	if err := executor.Run(cmd); err != nil {
		return fmt.Errorf("failed to set IP on TAP %s: %w", name, err)
	}

	return nil
}

// CreateBridge creates a Linux bridge if it doesn't exist.
func CreateBridge(name, subnet string) error {
	return CreateBridgeWithExecutor(name, subnet, NewDefaultTAPExecutor())
}

// CreateBridgeWithExecutor creates a Linux bridge using injectable executor.
func CreateBridgeWithExecutor(name, subnet string, executor TAPExecutor) error {
	log.Info().
		Str("bridge", name).
		Str("subnet", subnet).
		Msg("Creating bridge")

	// Check if bridge exists
	showCmd := executor.Command("ip", "link", "show", name)
	if err := executor.Run(showCmd); err == nil {
		log.Info().Str("bridge", name).Msg("Bridge already exists")
		return nil
	}

	// Create bridge
	addCmd := executor.Command("ip", "link", "add", name, "type", "bridge")
	if err := executor.Run(addCmd); err != nil {
		return fmt.Errorf("failed to create bridge %s: %w", name, err)
	}

	// Bring bridge up
	upCmd := executor.Command("ip", "link", "set", name, "up")
	if err := executor.Run(upCmd); err != nil {
		cleanupCmd := executor.Command("ip", "link", "delete", name)
		executor.Run(cleanupCmd)
		return fmt.Errorf("failed to bring bridge up: %w", err)
	}

	// Set IP on bridge if subnet provided
	if subnet != "" {
		// Parse subnet to get gateway IP (first address)
		ip, ipNet, err := net.ParseCIDR(subnet)
		if err != nil {
			return fmt.Errorf("invalid subnet %s: %w", subnet, err)
		}

		// Gateway is typically the first IP in the subnet
		gatewayIP := make([]byte, 4)
		copy(gatewayIP, ip.To4())
		gatewayIP[3] = 1 // Last octet = 1 for gateway
		gatewayCIDR := fmt.Sprintf("%d.%d.%d.%d/%d", gatewayIP[0], gatewayIP[1], gatewayIP[2], gatewayIP[3], maskToPrefix(ipNet.Mask))

		if err := ConfigureTAPIPWithExecutor(name, gatewayCIDR, executor); err != nil {
			cleanupCmd := executor.Command("ip", "link", "delete", name)
			executor.Run(cleanupCmd)
			return fmt.Errorf("failed to set bridge IP: %w", err)
		}
	}

	return nil
}

// maskToPrefix converts a netmask to CIDR prefix length.
func maskToPrefix(mask net.IPMask) int {
	prefix, _ := mask.Size()
	return prefix
}
