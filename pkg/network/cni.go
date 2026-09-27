// Package network provides CNI-compatible TAP device operations
// for Firecracker microVMs.
package network

import (
	"context"
	"fmt"
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
	if err := executor.Run(cleanupCmd); err != nil {
		log.Debug().Err(err).Msg("Pre-cleanup TAP delete (device may not exist)")
	}

	// Create TAP device
	createCmd := executor.Command("ip", "tuntap", "add", name, "mode", "tap")
	if err := executor.Run(createCmd); err != nil {
		return nil, fmt.Errorf("failed to create TAP device %s: %w", name, err)
	}

	// Bring TAP up
	upCmd := executor.Command("ip", "link", "set", name, "up")
	if err := executor.Run(upCmd); err != nil {
		cleanupCmd := executor.Command("ip", "link", "delete", name)
		if cerr := executor.Run(cleanupCmd); cerr != nil {
			log.Debug().Err(cerr).Msg("Cleanup TAP delete failed after bring-up error")
		}
		return nil, fmt.Errorf("failed to bring TAP up: %w", err)
	}

	// Connect to bridge
	if bridge != "" {
		masterCmd := executor.Command("ip", "link", "set", name, "master", bridge)
		if err := executor.Run(masterCmd); err != nil {
			cleanupCmd := executor.Command("ip", "link", "delete", name)
			if cerr := executor.Run(cleanupCmd); cerr != nil {
				log.Debug().Err(cerr).Msg("Cleanup TAP delete failed after bridge attach error")
			}
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
		cmd := exec.CommandContext(context.Background(), "bridge", "fdb", "add", "00:00:00:00:00:00", "dev", vxlanInterface, "dst", peer)
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
