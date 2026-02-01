package network

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// NetworkManagerInternal wraps NetworkManager with testable interfaces
type NetworkManagerInternal struct {
	*NetworkManager
	executor CommandExecutor
}

// NewNetworkManagerWithExecutor creates a NetworkManager with a custom command executor
func NewNetworkManagerWithExecutor(config types.NetworkConfig, executor CommandExecutor) *NetworkManagerInternal {
	nm := &NetworkManager{
		config:     config,
		bridges:    make(map[string]bool),
		tapDevices: make(map[string]*TapDevice),
	}

	// Initialize IP allocator if subnet and bridge IP are configured
	if config.Subnet != "" && config.BridgeIP != "" {
		gatewayStr := strings.Split(config.BridgeIP, "/")[0]
		allocator, err := NewIPAllocator(config.Subnet, gatewayStr)
		if err != nil {
			log.Error().Err(err).Msg("Failed to initialize IP allocator")
		} else {
			nm.ipAllocator = allocator
		}
	}

	return &NetworkManagerInternal{
		NetworkManager: nm,
		executor:       executor,
	}
}

// ensureBridgeWithExecutor ensures bridge exists using the configured executor
func (nm *NetworkManagerInternal) ensureBridgeWithExecutor(ctx context.Context) error {
	bridgeName := nm.config.BridgeName

	nm.mu.RLock()
	exists := nm.bridges[bridgeName]
	nm.mu.RUnlock()

	if exists {
		return nil
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()

	if nm.bridges[bridgeName] {
		return nil
	}

	// Check if bridge exists
	if err := nm.executor.Command("ip", "link", "show", bridgeName).Run(); err != nil {
		log.Info().Str("bridge", bridgeName).Msg("Creating bridge")

		cmd := nm.executor.Command("ip", "link", "add", bridgeName, "type", "bridge")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create bridge: %w", err)
		}

		if nm.config.BridgeIP != "" {
			if err := nm.setupBridgeIPWithExecutor(ctx); err != nil {
				log.Warn().Err(err).Msg("Failed to set bridge IP")
			}
		}

		if err := nm.executor.Command("ip", "link", "set", bridgeName, "up").Run(); err != nil {
			return fmt.Errorf("failed to bring bridge up: %w", err)
		}

		log.Info().
			Str("bridge", bridgeName).
			Str("ip", nm.config.BridgeIP).
			Msg("Bridge created and configured")
	}

	nm.bridges[bridgeName] = true
	return nil
}

// setupBridgeIPWithExecutor configures bridge IP using the configured executor
func (nm *NetworkManagerInternal) setupBridgeIPWithExecutor(ctx context.Context) error {
	bridgeName := nm.config.BridgeName

	// Check if IP is already assigned
	if err := nm.executor.Command("ip", "addr", "show", bridgeName).Run(); err == nil {
		cmd := nm.executor.Command("ip", "addr", "add", nm.config.BridgeIP, "dev", bridgeName)
		if err := cmd.Run(); err != nil {
			log.Debug().Str("bridge", bridgeName).Msg("Bridge IP might already be set")
		}
	} else {
		cmd := nm.executor.Command("ip", "addr", "add", nm.config.BridgeIP, "dev", bridgeName)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to set bridge IP: %w", err)
		}
	}

	return nil
}

// setupNATWithExecutor configures NAT using the configured executor
func (nm *NetworkManagerInternal) setupNATWithExecutor(ctx context.Context) error {
	if nm.config.Subnet == "" {
		return fmt.Errorf("subnet must be configured for NAT")
	}

	log.Info().Str("subnet", nm.config.Subnet).Msg("Setting up NAT masquerading")

	// Enable IP forwarding
	if err := nm.executor.Command("sysctl", "-w", "net.ipv4.ip_forward=1").Run(); err != nil {
		return fmt.Errorf("failed to enable IP forwarding: %w", err)
	}

	subnet := nm.config.Subnet
	cmd := nm.executor.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", subnet, "-j", "MASQUERADE")
	if err := cmd.Run(); err != nil {
		cmd = nm.executor.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", subnet, "-j", "MASQUERADE")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add NAT rule: %w", err)
		}
		log.Info().Msg("NAT masquerade rule added")
	}

	// Allow forwarding from bridge
	cmd = nm.executor.Command("iptables", "-C", "FORWARD", "-i", nm.config.BridgeName, "-j", "ACCEPT")
	if err := cmd.Run(); err != nil {
		cmd = nm.executor.Command("iptables", "-A", "FORWARD", "-i", nm.config.BridgeName, "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add forward rule: %w", err)
		}
	}

	// Allow forwarding to bridge
	cmd = nm.executor.Command("iptables", "-C", "FORWARD", "-o", nm.config.BridgeName, "-j", "ACCEPT")
	if err := cmd.Run(); err != nil {
		cmd = nm.executor.Command("iptables", "-A", "FORWARD", "-o", nm.config.BridgeName, "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add forward rule: %w", err)
		}
	}

	return nil
}

// createTapDeviceWithExecutor creates a TAP device using the configured executor
func (nm *NetworkManagerInternal) createTapDeviceWithExecutor(ctx context.Context, network types.NetworkAttachment, index int, taskID string) (*TapDevice, error) {
	ifaceID := fmt.Sprintf("eth%d", index)
	tapName := fmt.Sprintf("tap-%s", ifaceID)

	var ipAddr string
	if nm.ipAllocator != nil && nm.config.IPMode == "static" {
		var err error
		ipAddr, err = nm.ipAllocator.Allocate(taskID)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to allocate static IP, TAP will have no IP")
		}
	}

	// Create TAP device
	if err := nm.executor.Command("ip", "tuntap", "add", tapName, "mode", "tap").Run(); err != nil {
		return nil, fmt.Errorf("failed to create TAP device: %w", err)
	}

	// Bring TAP up
	if err := nm.executor.Command("ip", "link", "set", tapName, "up").Run(); err != nil {
		nm.executor.Command("ip", "link", "delete", tapName).Run()
		return nil, fmt.Errorf("failed to bring TAP up: %w", err)
	}

	bridgeName := nm.config.BridgeName
	if network.Network.Spec.DriverConfig != nil &&
		network.Network.Spec.DriverConfig.Bridge != nil &&
		network.Network.Spec.DriverConfig.Bridge.Name != "" {
		bridgeName = network.Network.Spec.DriverConfig.Bridge.Name
	}

	if err := nm.executor.Command("ip", "link", "set", tapName, "master", bridgeName).Run(); err != nil {
		nm.executor.Command("ip", "link", "delete", tapName).Run()
		return nil, fmt.Errorf("failed to add TAP to bridge: %w", err)
	}

	var subnet, gateway, netmask string
	if nm.config.Subnet != "" {
		subnet = nm.config.Subnet
		_, ipNet, err := net.ParseCIDR(subnet)
		if err == nil {
			mask := net.IP(ipNet.Mask).String()
			netmask = mask
		}
	}
	if nm.config.BridgeIP != "" {
		gateway = strings.Split(nm.config.BridgeIP, "/")[0]
	}

	tap := &TapDevice{
		Name:    tapName,
		Bridge:  bridgeName,
		IP:      ipAddr,
		Netmask: netmask,
		Gateway: gateway,
		Subnet:  subnet,
	}

	return tap, nil
}

// removeTapDeviceWithExecutor removes a TAP device using the configured executor
func (nm *NetworkManagerInternal) removeTapDeviceWithExecutor(tap *TapDevice) error {
	log.Debug().
		Str("tap", tap.Name).
		Msg("Removing TAP device")

	nm.executor.Command("ip", "link", "set", tap.Name, "down").Run()

	if err := nm.executor.Command("ip", "link", "delete", tap.Name).Run(); err != nil {
		return fmt.Errorf("failed to delete TAP device: %w", err)
	}

	return nil
}

// PrepareNetworkWithExecutor prepares network using the configured executor
func (nm *NetworkManagerInternal) PrepareNetworkWithExecutor(ctx context.Context, task *types.Task) error {
	log.Info().
		Str("task_id", task.ID).
		Int("networks", len(task.Networks)).
		Msg("Preparing network interfaces")

	if err := nm.ensureBridgeWithExecutor(ctx); err != nil {
		return fmt.Errorf("failed to ensure bridge: %w", err)
	}

	if nm.config.NATEnabled && !nm.natSetup {
		if err := nm.setupNATWithExecutor(ctx); err != nil {
			log.Warn().Err(err).Msg("Failed to setup NAT, VMs may not have internet access")
		} else {
			nm.natSetup = true
		}
	}

	for i, network := range task.Networks {
		tap, err := nm.createTapDeviceWithExecutor(ctx, network, i, task.ID)
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
			Str("ip", tap.IP).
			Msg("TAP device created")
	}

	log.Info().
		Str("task_id", task.ID).
		Msg("Network preparation completed")

	return nil
}

// CleanupNetworkWithExecutor cleans up network using the configured executor
func (nm *NetworkManagerInternal) CleanupNetworkWithExecutor(ctx context.Context, task *types.Task) error {
	if task == nil {
		return nil
	}

	log.Info().
		Str("task_id", task.ID).
		Msg("Cleaning up network interfaces")

	nm.mu.Lock()
	defer nm.mu.Unlock()

	for key, tap := range nm.tapDevices {
		if strings.HasPrefix(key, task.ID+"-") {
			if err := nm.removeTapDeviceWithExecutor(tap); err != nil {
				log.Error().Err(err).
					Str("tap", tap.Name).
					Msg("Failed to remove TAP device")
			}

			if nm.ipAllocator != nil && tap.IP != "" {
				nm.ipAllocator.Release(tap.IP)
			}

			delete(nm.tapDevices, key)
		}
	}

	log.Info().
		Str("task_id", task.ID).
		Msg("Network cleanup completed")

	return nil
}

// Helper for process operations in tests
func findProcess(pid int) (*os.Process, error) {
	return os.FindProcess(pid)
}

// Helper for signals in tests
func killProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

// Legacy interface compatibility
var (
	_ CommandExecutor = (*RealCommandExecutor)(nil)
	_ CommandExecutor = (*MockCommandExecutor)(nil)
)

// Default executor for production use
var defaultExecutor CommandExecutor = &RealCommandExecutor{}

// execCommand is a helper that uses the default executor
func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
