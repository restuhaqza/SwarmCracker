// Package network manages networking for Firecracker VMs.
package network

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// NetworkManager manages VM networking.
type NetworkManager struct {
	config      types.NetworkConfig
	bridges     map[string]bool
	mu          sync.RWMutex
	tapDevices  map[string]*TapDevice
	ipAllocator *IPAllocator
	natSetup    bool
}

// TapDevice represents a TAP device.
type TapDevice struct {
	Name      string
	Bridge    string
	IP        string
	Netmask   string
	Gateway   string
	Subnet    string
}

// IPAllocator handles static IP allocation.
type IPAllocator struct {
	subnet     *net.IPNet
	gateway    net.IP
	allocated  map[string]bool // Track allocated IPs
	mu         sync.Mutex
}

// NewIPAllocator creates a new IP allocator.
func NewIPAllocator(subnetStr, gatewayStr string) (*IPAllocator, error) {
	_, subnet, err := net.ParseCIDR(subnetStr)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet %s: %w", subnetStr, err)
	}

	gateway := net.ParseIP(gatewayStr)
	if gateway == nil {
		return nil, fmt.Errorf("invalid gateway %s", gatewayStr)
	}

	return &IPAllocator{
		subnet:    subnet,
		gateway:   gateway,
		allocated: make(map[string]bool),
	}, nil
}

// Allocate allocates an IP address for a VM ID.
func (a *IPAllocator) Allocate(vmID string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Generate IP from hash of VM ID (deterministic but distributed)
	ip := a.hashToIP(vmID)

	// Check if IP is in subnet
	if !a.subnet.Contains(ip) {
		return "", fmt.Errorf("generated IP %s not in subnet %s", ip, a.subnet)
	}

	// Don't allocate gateway address
	if ip.Equal(a.gateway) {
		// Try next IP
		ip = incIP(ip)
		if !a.subnet.Contains(ip) {
			return "", fmt.Errorf("cannot allocate IP: subnet exhausted")
		}
	}

	a.allocated[ip.String()] = true
	return ip.String(), nil
}

// hashToIP converts a VM ID to an IP address using SHA-256.
func (a *IPAllocator) hashToIP(vmID string) net.IP {
	h := sha256.New()
	h.Write([]byte(vmID))
	hash := h.Sum(nil)

	// Use first 4 bytes of hash to generate IP in subnet
	// Ensure IP is in the usable range (not network or broadcast)
	n := binary.BigEndian.Uint32(hash[:4]) % 250

	// Add offset to skip network address (x.x.x.0) and gateway (x.x.x.1)
	// Start from x.x.x.2
	ip := make(net.IP, 4)
	copy(ip, a.gateway.To4())
	ip[3] = byte(2 + uint8(n))

	return ip
}

// incIP increments an IP address.
func incIP(ip net.IP) net.IP {
	next := make(net.IP, len(ip))
	copy(next, ip)
	for j := len(next) - 1; j >= 0; j-- {
		next[j]++
		if next[j] > 0 {
			break
		}
	}
	return next
}

// Release releases an allocated IP.
func (a *IPAllocator) Release(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.allocated, ip)
}

// NewNetworkManager creates a new NetworkManager.
func NewNetworkManager(config types.NetworkConfig) types.NetworkManager {
	nm := &NetworkManager{
		config:     config,
		bridges:    make(map[string]bool),
		tapDevices: make(map[string]*TapDevice),
	}

	// Initialize IP allocator if subnet and bridge IP are configured
	if config.Subnet != "" && config.BridgeIP != "" {
		// Extract gateway IP from bridge IP (remove CIDR)
		gatewayStr := strings.Split(config.BridgeIP, "/")[0]
		allocator, err := NewIPAllocator(config.Subnet, gatewayStr)
		if err != nil {
			log.Error().Err(err).Msg("Failed to initialize IP allocator")
		} else {
			nm.ipAllocator = allocator
		}
	}

	return nm
}

// PrepareNetwork prepares network interfaces for a task.
func (nm *NetworkManager) PrepareNetwork(ctx context.Context, task *types.Task) error {
	log.Info().
		Str("task_id", task.ID).
		Int("networks", len(task.Networks)).
		Msg("Preparing network interfaces")

	// Ensure bridge exists and is configured
	if err := nm.ensureBridge(ctx); err != nil {
		return fmt.Errorf("failed to ensure bridge: %w", err)
	}

	// Setup NAT if enabled
	if nm.config.NATEnabled && !nm.natSetup {
		if err := nm.setupNAT(ctx); err != nil {
			log.Warn().Err(err).Msg("Failed to setup NAT, VMs may not have internet access")
		} else {
			nm.natSetup = true
		}
	}

	// Create TAP device for each network attachment
	for i, network := range task.Networks {
		tap, err := nm.createTapDevice(ctx, network, i, task.ID)
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

// CleanupNetwork cleans up network interfaces for a task.
func (nm *NetworkManager) CleanupNetwork(ctx context.Context, task *types.Task) error {
	if task == nil {
		return nil
	}

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

			// Release allocated IP
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

// GetTapIP returns the allocated IP for a task.
func (nm *NetworkManager) GetTapIP(taskID string) (string, error) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	// Find TAP device for this task
	for key, tap := range nm.tapDevices {
		if strings.HasPrefix(key, taskID+"-") {
			if tap.IP == "" {
				return "", fmt.Errorf("no IP allocated for task %s", taskID)
			}
			return tap.IP, nil
		}
	}

	return "", fmt.Errorf("no TAP device found for task %s", taskID)
}

// ensureBridge ensures the bridge exists and is properly configured.
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

		cmd := exec.Command("ip", "link", "add", bridgeName, "type", "bridge")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create bridge: %w", err)
		}

		// Assign IP to bridge if configured
		if nm.config.BridgeIP != "" {
			if err := nm.setupBridgeIP(ctx); err != nil {
				log.Warn().Err(err).Msg("Failed to set bridge IP")
			}
		}

		// Bring bridge up
		if err := exec.Command("ip", "link", "set", bridgeName, "up").Run(); err != nil {
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

// setupBridgeIP configures the IP address on the bridge.
func (nm *NetworkManager) setupBridgeIP(ctx context.Context) error {
	bridgeName := nm.config.BridgeName

	// Check if IP is already assigned
	if err := exec.Command("ip", "addr", "show", bridgeName).Run(); err == nil {
		// IP might already be set, try to add it (will fail if exists)
		cmd := exec.Command("ip", "addr", "add", nm.config.BridgeIP, "dev", bridgeName)
		if err := cmd.Run(); err != nil {
			// IP might already be assigned, that's ok
			log.Debug().Str("bridge", bridgeName).Msg("Bridge IP might already be set")
		}
	} else {
		cmd := exec.Command("ip", "addr", "add", nm.config.BridgeIP, "dev", bridgeName)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to set bridge IP: %w", err)
		}
	}

	return nil
}

// setupNAT configures NAT/masquerading for internet access.
func (nm *NetworkManager) setupNAT(ctx context.Context) error {
	if nm.config.Subnet == "" {
		return fmt.Errorf("subnet must be configured for NAT")
	}

	log.Info().Str("subnet", nm.config.Subnet).Msg("Setting up NAT masquerading")

	// Enable IP forwarding
	if err := exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").Run(); err != nil {
		return fmt.Errorf("failed to enable IP forwarding: %w", err)
	}

	// Setup iptables masquerade rule
	subnet := nm.config.Subnet
	cmd := exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", subnet, "-j", "MASQUERADE")
	if err := cmd.Run(); err != nil {
		// Rule doesn't exist, add it
		cmd = exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", subnet, "-j", "MASQUERADE")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add NAT rule: %w", err)
		}
		log.Info().Msg("NAT masquerade rule added")
	}

	// Allow forwarding from bridge
	cmd = exec.Command("iptables", "-C", "FORWARD", "-i", nm.config.BridgeName, "-j", "ACCEPT")
	if err := cmd.Run(); err != nil {
		cmd = exec.Command("iptables", "-A", "FORWARD", "-i", nm.config.BridgeName, "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add forward rule: %w", err)
		}
	}

	// Allow forwarding to bridge
	cmd = exec.Command("iptables", "-C", "FORWARD", "-o", nm.config.BridgeName, "-j", "ACCEPT")
	if err := cmd.Run(); err != nil {
		cmd = exec.Command("iptables", "-A", "FORWARD", "-o", nm.config.BridgeName, "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add forward rule: %w", err)
		}
	}

	return nil
}

// createTapDevice creates a TAP device for a network attachment.
func (nm *NetworkManager) createTapDevice(ctx context.Context, network types.NetworkAttachment, index int, taskID string) (*TapDevice, error) {
	// Generate interface ID
	ifaceID := fmt.Sprintf("eth%d", index)
	tapName := fmt.Sprintf("tap-%s", ifaceID)

	// Allocate IP address for this TAP
	var ipAddr string
	if nm.ipAllocator != nil && nm.config.IPMode == "static" {
		var err error
		ipAddr, err = nm.ipAllocator.Allocate(taskID)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to allocate static IP, TAP will have no IP")
		}
	}

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

	// Parse subnet and gateway
	var subnet, gateway, netmask string
	if nm.config.Subnet != "" {
		subnet = nm.config.Subnet
		// Extract netmask from CIDR
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
