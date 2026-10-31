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
	return createTAPDeviceWithExecutor(name, bridge, true, executor)
}

// CreateTAPDeviceDown creates a TAP device attached to the bridge and leaves it
// DOWN. It is the creator a restore uses.
//
// A VM restored from a snapshot resumes with the source node's address already
// configured on its guest interface, so an interface that is up before the guest
// is re-addressed would put a stale address on this node's bridge — the one
// instant two nodes could answer for the service. The caller brings the device up
// (BringTAPUp) once the guest has its new address.
//
// It is the same code as CreateTAPDevice with the bring-up step left out; there is
// one `ip tuntap add` in this package, not two.
func CreateTAPDeviceDown(name, bridge string) error {
	return CreateTAPDeviceDownWithExecutor(name, bridge, NewDefaultTAPExecutor())
}

// CreateTAPDeviceDownWithExecutor is CreateTAPDeviceDown with an injectable
// executor, so the "created but not up" contract can be asserted without root.
func CreateTAPDeviceDownWithExecutor(name, bridge string, executor TAPExecutor) error {
	_, err := createTAPDeviceWithExecutor(name, bridge, false, executor)
	return err
}

// createTAPDeviceWithExecutor is the one TAP creation sequence: remove any leftover
// device, add the TAP, optionally bring it up, and attach it to the bridge. The
// device's MAC is read last because it only exists once the device does.
//
// up is a parameter rather than a second function because the order of the steps is
// what a boot and a restore disagree about, and nothing else: a boot wants the
// interface up before the guest starts, a restore wants it down until the guest has
// been re-addressed.
func createTAPDeviceWithExecutor(name, bridge string, up bool, executor TAPExecutor) (*TAPDevice, error) {
	log.Info().
		Str("tap", name).
		Str("bridge", bridge).
		Bool("up", up).
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

	// Bring TAP up, unless the caller asked for a device that stays down. A boot
	// has nothing to configure in the guest, so it brings the interface up before
	// the VM starts; a restore brings it up only after the guest is re-addressed.
	if up {
		upCmd := executor.Command("ip", "link", "set", name, "up")
		if err := executor.Run(upCmd); err != nil {
			cleanupCmd := executor.Command("ip", "link", "delete", name)
			if cerr := executor.Run(cleanupCmd); cerr != nil {
				log.Debug().Err(cerr).Msg("Cleanup TAP delete failed after bring-up error")
			}
			return nil, fmt.Errorf("failed to bring TAP up: %w", err)
		}
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

// BringTAPUp brings an existing TAP device up. It is the second half of the
// restore's two-step TAP lifecycle: the device is created down, the guest is
// re-addressed, then the device is brought up.
func BringTAPUp(name string) error {
	return BringTAPUpWithExecutor(name, NewDefaultTAPExecutor())
}

// BringTAPUpWithExecutor is BringTAPUp with an injectable executor.
func BringTAPUpWithExecutor(name string, executor TAPExecutor) error {
	if err := executor.Run(executor.Command("ip", "link", "set", name, "up")); err != nil {
		return fmt.Errorf("failed to bring TAP %s up: %w", name, err)
	}
	return nil
}

// RemoveTAPDevice removes a TAP device and treats one that is already gone as
// success.
//
// Unlike DeleteTAPDevice it is idempotent, which is what a restore's undo needs: a
// restore that failed after creating the device removes it, and a restore that
// failed before that has nothing to remove — and must not report a cleanup failure
// for a device that never existed.
func RemoveTAPDevice(name string) error {
	return RemoveTAPDeviceWithExecutor(name, NewDefaultTAPExecutor())
}

// RemoveTAPDeviceWithExecutor is RemoveTAPDevice with an injectable executor.
func RemoveTAPDeviceWithExecutor(name string, executor TAPExecutor) error {
	// A device that no longer exists is what removal wanted. The existence check is
	// the answer: `ip link delete` fails for a missing device with the same kind of
	// error a permissions or driver failure produces, so only a probe can tell them
	// apart.
	stillThere := func() bool {
		return executor.Run(executor.Command("ip", "link", "show", name)) == nil
	}
	if deleteErr := DeleteTAPDeviceWithExecutor(name, executor); deleteErr != nil && stillThere() {
		return deleteErr
	}
	log.Debug().Str("tap", name).Msg("TAP device is removed or was already gone")
	return nil
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
