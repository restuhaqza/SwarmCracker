// Package network manages networking for Firecracker VMs.
package network

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// NetworkManager manages VM networking.
type NetworkManager struct {
	config   types.NetworkConfig
	bridges  map[string]bool
	mu       sync.RWMutex
	tapDevices map[string]*TapDevice
}

// TapDevice represents a TAP device.
type TapDevice struct {
	Name    string
	Bridge  string
	IP      string
	Netmask string
}

// NewNetworkManager creates a new NetworkManager.
func NewNetworkManager(config types.NetworkConfig) types.NetworkManager {
	return &NetworkManager{
		config:      config,
		bridges:     make(map[string]bool),
		tapDevices: make(map[string]*TapDevice),
	}
}

// PrepareNetwork prepares network interfaces for a task.
func (nm *NetworkManager) PrepareNetwork(ctx context.Context, task *types.Task) error {
	log.Info().
		Str("task_id", task.ID).
		Int("networks", len(task.Networks)).
		Msg("Preparing network interfaces")

	// Ensure bridge exists
	if err := nm.ensureBridge(ctx); err != nil {
		return fmt.Errorf("failed to ensure bridge: %w", err)
	}

	// Create TAP device for each network attachment
	for i, network := range task.Networks {
		tap, err := nm.createTapDevice(ctx, network, i)
		if err != nil {
			return fmt.Errorf("failed to create TAP device: %w", err)
		}

		nm.mu.Lock()
		nm.tapDevices[task.ID+"-"+tap.Name] = tap
		nm.mu.Unlock()

		log.Info().
			Str("task_id", task.ID).
			Str("tap", tap.Name).
			Str("bridge", tap.Bridge).
			Msg("TAP device created")
	}

	log.Info().
		Str("task_id", task.ID).
		Msg("Network preparation completed")

	return nil
}

// CleanupNetwork cleans up network interfaces for a task.
func (nm *NetworkManager) CleanupNetwork(ctx context.Context, task *types.Task) error {
	log.Info().
		Str("task_id", task.ID).
		Msg("Cleaning up network interfaces")

	nm.mu.Lock()
	defer nm.mu.Unlock()

	// Find and remove all TAP devices for this task
	for key, tap := range nm.tapDevices {
		if strings.HasPrefix(key, task.ID+"-") {
			if err := nm.removeTapDevice(tap); err != nil {
				log.Error().Err(err).
					Str("tap", tap.Name).
					Msg("Failed to remove TAP device")
			}
			delete(nm.tapDevices, key)
		}
	}

	log.Info().
		Str("task_id", task.ID).
		Msg("Network cleanup completed")

	return nil
}

// ensureBridge ensures the bridge exists.
func (nm *NetworkManager) ensureBridge(ctx context.Context) error {
	bridgeName := nm.config.BridgeName

	nm.mu.RLock()
	exists := nm.bridges[bridgeName]
	nm.mu.RUnlock()

	if exists {
		return nil
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()

	// Double-check after acquiring write lock
	if nm.bridges[bridgeName] {
		return nil
	}

	// Check if bridge exists
	if err := exec.Command("ip", "link", "show", bridgeName).Run(); err != nil {
		// Create bridge
		log.Info().Str("bridge", bridgeName).Msg("Creating bridge")

		cmd := exec.Command("ip", "link", "add", "bridgeName", "type", "bridge")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create bridge: %w", err)
		}

		// Bring bridge up
		if err := exec.Command("ip", "link", "set", bridgeName, "up").Run(); err != nil {
			return fmt.Errorf("failed to bring bridge up: %w", err)
		}
	}

	nm.bridges[bridgeName] = true
	return nil
}

// createTapDevice creates a TAP device for a network attachment.
func (nm *NetworkManager) createTapDevice(ctx context.Context, network types.NetworkAttachment, index int) (*TapDevice, error) {
	// Generate interface ID
	ifaceID := fmt.Sprintf("eth%d", index)
	tapName := fmt.Sprintf("tap-%s", ifaceID)

	// Create TAP device
	if err := exec.Command("ip", "tuntap", "add", tapName, "mode", "tap").Run(); err != nil {
		return nil, fmt.Errorf("failed to create TAP device: %w", err)
	}

	// Bring TAP up
	if err := exec.Command("ip", "link", "set", tapName, "up").Run(); err != nil {
		// Cleanup on failure
		exec.Command("ip", "link", "delete", tapName).Run()
		return nil, fmt.Errorf("failed to bring TAP up: %w", err)
	}

	// Add to bridge
	bridgeName := nm.config.BridgeName
	if network.Network.Spec.DriverConfig != nil &&
	   network.Network.Spec.DriverConfig.Bridge != nil &&
	   network.Network.Spec.DriverConfig.Bridge.Name != "" {
		bridgeName = network.Network.Spec.DriverConfig.Bridge.Name
	}

	if err := exec.Command("ip", "link", "set", tapName, "master", bridgeName).Run(); err != nil {
		// Cleanup on failure
		exec.Command("ip", "link", "delete", tapName).Run()
		return nil, fmt.Errorf("failed to add TAP to bridge: %w", err)
	}

	// Parse IP from network addresses (if provided)
	var ip string
	if len(network.Addresses) > 0 {
		ip = strings.Split(network.Addresses[0], "/")[0]
	}

	tap := &TapDevice{
		Name:    tapName,
		Bridge:  bridgeName,
		IP:      ip,
		Netmask: "255.255.255.0",
	}

	return tap, nil
}

// removeTapDevice removes a TAP device.
func (nm *NetworkManager) removeTapDevice(tap *TapDevice) error {
	log.Debug().
		Str("tap", tap.Name).
		Msg("Removing TAP device")

	// Bring interface down first
	exec.Command("ip", "link", "set", tap.Name, "down").Run()

	// Delete TAP device
	if err := exec.Command("ip", "link", "delete", tap.Name).Run(); err != nil {
		return fmt.Errorf("failed to delete TAP device: %w", err)
	}

	return nil
}

// SetupBridgeIP configures IP address on the bridge (optional).
func (nm *NetworkManager) SetupBridgeIP(ctx context.Context, ip, netmask string) error {
	bridgeName := nm.config.BridgeName

	cmd := exec.Command("ip", "addr", "add", ip+netmask, "dev", bridgeName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set bridge IP: %w", err)
	}

	return nil
}

// ListTapDevices returns a list of active TAP devices.
func (nm *NetworkManager) ListTapDevices() []*TapDevice {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	devices := make([]*TapDevice, 0, len(nm.tapDevices))
	for _, tap := range nm.tapDevices {
		devices = append(devices, tap)
	}

	return devices
}
