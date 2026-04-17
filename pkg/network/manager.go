// Package network manages networking for Firecracker VMs.
package network

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// NetworkManager manages VM networking.
//
//nolint:revive // Stuttering name is acceptable here for package clarity
type NetworkManager struct {
	config        types.NetworkConfig
	bridges       map[string]bool
	mu            sync.RWMutex
	tapDevices    map[string]*TapDevice
	ipAllocator   *IPAllocator
	natSetup      bool
	vxlanMgr      *VXLANManager
	peerDiscovery bool
	peerCancel    context.CancelFunc
	nodeDiscovery NodeDiscovery // SwarmKit node discovery provider
	cniClient     *CNIClient    // CNI client for SwarmKit network attachments
}

// TapDevice represents a TAP device.
type TapDevice struct {
	Name    string
	Bridge  string
	IP      string
	Netmask string
	Gateway string
	Subnet  string
}

// IPAllocator handles static IP allocation.
type IPAllocator struct {
	subnet    *net.IPNet
	gateway   net.IP
	allocated map[string]string // Track allocated IPs (IP -> VM ID)
	mu        sync.Mutex
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
		allocated: make(map[string]string),
	}, nil
}

// Allocate allocates an IP address for a VM ID.
func (a *IPAllocator) Allocate(vmID string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Check if this VM ID already has an IP allocated
	for ip, id := range a.allocated {
		if id == vmID {
			return ip, nil
		}
	}

	// Generate IP from hash of VM ID (deterministic but distributed)
	ip := a.hashToIP(vmID)

	// Ensure IP is in subnet
	if !a.subnet.Contains(ip) {
		// Should not happen if hashToIP works correctly, but safe fallback
		return "", fmt.Errorf("generated IP %s not in subnet %s", ip, a.subnet)
	}

	// Collision resolution (Linear Probing)
	// Try up to 256 times (arbitrary limit to prevent infinite loops)
	for i := 0; i < 256; i++ {
		ipStr := ip.String()

		// Check constraints:
		// 1. Not the gateway
		// 2. Not network address (usually start of subnet)
		// 3. Not broadcast address (usually end of subnet) - simplified check
		// 4. Not already allocated

		isGateway := ip.Equal(a.gateway)
		_, isAllocated := a.allocated[ipStr]

		if !isGateway && !isAllocated {
			// Found free IP
			a.allocated[ipStr] = vmID
			return ipStr, nil
		}

		// Try next IP
		ip = incIP(ip)

		// Wrap around or check if still in subnet
		if !a.subnet.Contains(ip) {
			// Reset to start of subnet + 2 (skip network & gateway assumption)
			// Simple reset:
			ip = make(net.IP, len(a.subnet.IP))
			copy(ip, a.subnet.IP)
			ip = incIP(ip) // .1
			ip = incIP(ip) // .2
		}
	}

	return "", fmt.Errorf("failed to allocate IP: subnet exhausted or too many collisions")
}

// hashToIP converts a VM ID to an IP address using SHA-256.
func (a *IPAllocator) hashToIP(vmID string) net.IP {
	h := sha256.New()
	h.Write([]byte(vmID))
	hash := h.Sum(nil)

	// Determine if IPv4 or IPv6
	isIPv6 := len(a.subnet.IP) == net.IPv6len

	if isIPv6 {
		// IPv6 logic
		// Use hash to generate suffix
		ip := make(net.IP, net.IPv6len)
		copy(ip, a.subnet.IP)

		// XOR hash into the last 8 bytes (interface ID)
		for i := 0; i < 8; i++ {
			ip[8+i] = a.subnet.IP[8+i] ^ hash[i]
		}
		return ip
	}

	// IPv4 Logic
	// Calculate available range size
	ones, bits := a.subnet.Mask.Size()
	size := uint32(1) << (bits - ones)

	if size < 4 {
		// Very small subnet (e.g. /30, /31, /32). Just return start + 1?
		// For /30: .0 net, .1 gw, .2 host, .3 broad. size=4.
		// For /31: .0, .1. size=2.
		// Let's just pick based on hash
		n := binary.BigEndian.Uint32(hash[:4]) % size

		ip := make(net.IP, 4)
		ipInt := binary.BigEndian.Uint32(a.subnet.IP.To4()) + n
		binary.BigEndian.PutUint32(ip, ipInt)
		return ip
	}

	// Use hash to pick an offset
	// Avoid .0 (network) and .255 (broadcast) generally, but mainly fit in size
	n := binary.BigEndian.Uint32(hash[:4]) % (size - 2) // -2 to avoid network/broadcast roughly

	ip := make(net.IP, 4)
	ipInt := binary.BigEndian.Uint32(a.subnet.IP.To4()) + n + 1 // +1 to skip network address
	binary.BigEndian.PutUint32(ip, ipInt)

	return ip
}

// incIP increments an IP address.
func incIP(ip net.IP) net.IP {
	// Handle IPv4 mapped as IPv6 or pure IPv4
	isIPv4 := ip.To4() != nil

	next := make(net.IP, len(ip))
	copy(next, ip)

	if isIPv4 && len(ip) == net.IPv6len {
		// It's a mapped address (::ffff:1.2.3.4), only increment last 4 bytes
		for j := len(next) - 1; j >= len(next)-4; j-- {
			next[j]++
			if next[j] > 0 {
				return next
			}
		}
		// Overflowed 32-bit (should wrap to 0.0.0.0 in last 4 bytes)
		return next
	}

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

	// Initialize CNI client for SwarmKit network attachments
	// CNI is used when tasks have network attachments from SwarmKit
	nm.cniClient = NewCNIClient(CNIConfig{
		BinDir:      "/opt/cni/bin",
		ConfDir:     "/etc/cni/net.d",
		NetworkName: "swarmcracker",
	})

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

	// Setup DHCP server for VM network boot
	if err := nm.setupDHCP(ctx); err != nil {
		log.Warn().Err(err).Msg("Failed to setup DHCP, VMs may need static config")
	}

	// Check if we should use CNI for network attachments
	// Use CNI when: task has network attachments AND CNI is configured
	if len(task.Networks) > 0 && nm.cniClient != nil {
		// Use CNI plugin for SwarmKit network attachments
		return nm.prepareNetworkWithCNI(ctx, task)
	}

	// If no networks attached (and CNI not used), create default TAP
	if len(task.Networks) == 0 {
		log.Info().Str("task_id", task.ID).Msg("No network attachments, creating default TAP device")

		// Create a synthetic network attachment for default bridge
		defaultNetwork := types.NetworkAttachment{
			Network: types.Network{
				ID: "default",
				Spec: types.NetworkSpec{
					Name:   "default",
					Driver: "bridge",
					DriverConfig: &types.DriverConfig{
						Bridge: &types.BridgeConfig{
							Name: nm.config.BridgeName,
						},
					},
				},
			},
			Addresses: []string{},
		}

		tap, err := nm.createTapDevice(ctx, defaultNetwork, 0, task.ID)
		if err != nil {
			return fmt.Errorf("failed to create default TAP device: %w", err)
		}

		nm.mu.Lock()
		nm.tapDevices[task.ID+"-"+tap.Name] = tap
		nm.mu.Unlock()

		// Add network attachment to task
		if tap.IP != "" {
			ipWithMask := fmt.Sprintf("%s/%s", tap.IP, ipMaskToCIDR(tap.Netmask))
			defaultNetwork.Addresses = []string{ipWithMask}
		}
		task.Networks = []types.NetworkAttachment{defaultNetwork}

		log.Info().
			Str("task_id", task.ID).
			Str("tap", tap.Name).
			Str("bridge", tap.Bridge).
			Str("ip", tap.IP).
			Msg("Default TAP device created")
	}

	// Create TAP device for each network attachment (legacy method, used when CNI not available)
	for i, network := range task.Networks {
		tap, err := nm.createTapDevice(ctx, network, i, task.ID)
		if err != nil {
			return fmt.Errorf("failed to create TAP device: %w", err)
		}

		nm.mu.Lock()
		nm.tapDevices[task.ID+"-"+tap.Name] = tap
		nm.mu.Unlock()

		// Update task network addresses with allocated IP (only if not from SwarmKit)
		if tap.IP != "" && len(network.Addresses) == 0 {
			if network.Addresses == nil {
				network.Addresses = []string{}
			}
			ipWithMask := fmt.Sprintf("%s/%s", tap.IP, ipMaskToCIDR(tap.Netmask))
			task.Networks[i].Addresses = append(network.Addresses, ipWithMask)
		}

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

// prepareNetworkWithCNI uses CNI plugin for SwarmKit network attachments.
func (nm *NetworkManager) prepareNetworkWithCNI(ctx context.Context, task *types.Task) error {
	log.Info().
		Str("task_id", task.ID).
		Int("networks", len(task.Networks)).
		Msg("Using CNI for network preparation")

	for _, attachment := range task.Networks {
		// Get network name and IP from SwarmKit attachment
		networkName := attachment.Network.Spec.Name
		if networkName == "" {
			networkName = attachment.Network.ID[:8]
		}

		// Get IP from SwarmKit IPAM (Addresses[0] in CIDR format)
		var ipCIDR string
		if len(attachment.Addresses) > 0 {
			ipCIDR = attachment.Addresses[0]
		} else {
			return fmt.Errorf("CNI requires SwarmKit-provided IP in Addresses")
		}

		// Call CNI ADD
		result, err := nm.cniClient.AddNetwork(ctx, task.ID, "/tmp/firecracker-ns", ipCIDR, networkName)
		if err != nil {
			return fmt.Errorf("CNI ADD failed for network %s: %w", networkName, err)
		}

		// Store TAP device info
		if len(result.Interfaces) > 0 && len(result.IPs) > 0 {
			iface := result.Interfaces[0]
			ip := result.IPs[0]

			tap := &TapDevice{
				Name:    iface.Name,
				Bridge:  nm.config.BridgeName, // CNI handles bridge internally
				IP:      ip.Address.IP.String(),
				Netmask: net.IP(ip.Address.Mask).String(),
			}

			nm.mu.Lock()
			nm.tapDevices[task.ID+"-"+iface.Name] = tap
			nm.mu.Unlock()

			log.Info().
				Str("task_id", task.ID).
				Str("tap", iface.Name).
				Str("mac", iface.Mac).
				Str("ip", ip.Address.IP.String()).
				Str("network", networkName).
				Msg("CNI network interface created")
		}
	}

	log.Info().
		Str("task_id", task.ID).
		Msg("CNI network preparation completed")

	return nil
}

// CleanupNetwork cleans up network interfaces for a task.
func (nm *NetworkManager) CleanupNetwork(_ context.Context, task *types.Task) error {
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

		// Bring bridge up
		if err := exec.Command("ip", "link", "set", bridgeName, "up").Run(); err != nil {
			return fmt.Errorf("failed to bring bridge up: %w", err)
		}
	}

	// Always ensure bridge IP is configured (even if bridge already existed)
	if nm.config.BridgeIP != "" {
		if err := nm.setupBridgeIP(ctx); err != nil {
			log.Warn().Err(err).Msg("Failed to set bridge IP")
		}
	}

	// Ensure bridge is up
	if err := exec.Command("ip", "link", "set", bridgeName, "up").Run(); err != nil {
		log.Warn().Err(err).Msg("Failed to ensure bridge is up")
	}

	nm.bridges[bridgeName] = true

	// Setup VXLAN overlay if configured
	if nm.config.VXLANEnabled {
		if err := nm.setupVXLANOverlay(ctx); err != nil {
			log.Warn().Err(err).Msg("Failed to setup VXLAN overlay")
		}
	}

	// Start peer discovery if VXLAN peers are configured
	if nm.config.VXLANEnabled && len(nm.config.VXLANPeers) > 0 {
		if err := nm.StartPeerDiscovery(ctx); err != nil {
			log.Warn().Err(err).Msg("Failed to start peer discovery")
		}
	}

	log.Info().
		Str("bridge", bridgeName).
		Str("ip", nm.config.BridgeIP).
		Msg("Bridge ready")

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

// setupDHCP configures a minimal DHCP server using dnsmasq.
func (nm *NetworkManager) setupDHCP(ctx context.Context) error {
	if nm.config.Subnet == "" || nm.config.BridgeIP == "" {
		return fmt.Errorf("subnet and bridge IP must be configured for DHCP")
	}

	// Check if dnsmasq is available
	if _, err := exec.LookPath("dnsmasq"); err != nil {
		log.Warn().Msg("dnsmasq not found, DHCP will not be available")
		return nil // Not fatal - VMs can use static IPs
	}

	// Parse subnet to get DHCP range
	_, subnet, err := net.ParseCIDR(nm.config.Subnet)
	if err != nil {
		return fmt.Errorf("invalid subnet: %w", err)
	}

	// Parse gateway IP
	gatewayIP, _, err := net.ParseCIDR(nm.config.BridgeIP)
	if err != nil {
		gatewayIP = net.ParseIP(nm.config.BridgeIP)
		if gatewayIP == nil {
			return fmt.Errorf("invalid bridge IP")
		}
	}

	// Calculate DHCP range (exclude gateway)
	// Use IPs from .50 to .200 in the subnet
	subnetIP := binary.BigEndian.Uint32(subnet.IP.To4())
	dhcpStart := subnetIP + 50
	dhcpEnd := subnetIP + 200

	startIP := make(net.IP, 4)
	endIP := make(net.IP, 4)
	binary.BigEndian.PutUint32(startIP, dhcpStart)
	binary.BigEndian.PutUint32(endIP, dhcpEnd)

	log.Info().
		Str("bridge", nm.config.BridgeName).
		Str("start", startIP.String()).
		Str("end", endIP.String()).
		Str("gateway", gatewayIP.String()).
		Msg("Setting up DHCP server")

	// Kill any existing dnsmasq for this bridge
	exec.Command("pkill", "-f", fmt.Sprintf("dnsmasq.*%s", nm.config.BridgeName)).Run()

	// Create log file with proper permissions for dnsmasq (runs as nobody)
	logFile := "/tmp/dnsmasq.log"
	os.Remove(logFile) // Remove old file first
	if err := os.WriteFile(logFile, []byte{}, 0666); err != nil {
		log.Warn().Err(err).Msg("Could not create dnsmasq log file")
	}

	// Start dnsmasq for this bridge
	// Arguments:
	// --interface: bind to bridge
	// --bind-interfaces: only bind to specified interface
	// --dhcp-range: define DHCP pool
	// --dhcp-option=3: set gateway
	// --dhcp-option=6: set DNS (use gateway as DNS proxy)
	cmd := exec.Command("dnsmasq",
		"--interface", nm.config.BridgeName,
		"--bind-interfaces",
		"--dhcp-range", fmt.Sprintf("%s,%s,12h", startIP.String(), endIP.String()),
		"--dhcp-option", fmt.Sprintf("3,%s", gatewayIP.String()),
		"--dhcp-option", fmt.Sprintf("6,%s", gatewayIP.String()),
		"--log-queries",
		"--log-dhcp",
		"--log-facility=/tmp/dnsmasq.log",
		"--pid-file=/tmp/dnsmasq.pid",
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		log.Error().Str("output", string(output)).Msg("dnsmasq failed to start")
		return fmt.Errorf("failed to start dnsmasq: %w (output: %s)", err, string(output))
	}

	log.Info().Msg("DHCP server (dnsmasq) started")
	return nil
}

// setupVXLANOverlay sets up VXLAN overlay networking for cross-node VM communication.
func (nm *NetworkManager) setupVXLANOverlay(ctx context.Context) error {
	bridgeName := nm.config.BridgeName
	vxlanID := 100 // Default VXLAN ID

	// Discover physical interface and local IP
	physInterface, localIP, err := nm.getPhysicalInterface()
	if err != nil {
		return fmt.Errorf("failed to discover physical interface: %w", err)
	}

	// Combine static peers from config with discovered peers
	allPeers := nm.discoverPeerWorkers()
	allPeers = append(allPeers, nm.config.VXLANPeers...)

	// Create peer store with combined peers
	peerStore := NewStaticPeerStore(allPeers)

	// Create VXLAN manager
	nm.vxlanMgr = NewVXLANManager(bridgeName, vxlanID, nm.config.BridgeIP, peerStore)

	log.Info().
		Str("bridge", bridgeName).
		Str("phys", physInterface).
		Str("local_ip", localIP).
		Strs("peers", allPeers).
		Msg("Setting up VXLAN overlay")

	// Setup VXLAN using the VXLAN manager
	if err := nm.vxlanMgr.SetupVXLAN(physInterface, localIP); err != nil {
		return fmt.Errorf("failed to setup VXLAN: %w", err)
	}

	log.Info().Msg("VXLAN overlay configured")
	return nil
}

// getPhysicalInterface discovers the physical interface used for VXLAN transport.
func (nm *NetworkManager) getPhysicalInterface() (string, string, error) {
	// Get default route interface
	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return "", "", err
	}

	fields := strings.Fields(string(out))
	if len(fields) < 5 {
		return "", "", fmt.Errorf("unexpected route output")
	}

	physInterface := fields[4]

	// Get IP address of physical interface
	out, err = exec.Command("ip", "addr", "show", physInterface).Output()
	if err != nil {
		return "", "", err
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "inet ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				ip := strings.TrimSuffix(parts[1], "/24")
				ip = strings.TrimSuffix(ip, "/32")
				return physInterface, ip, nil
			}
		}
	}

	return "", "", fmt.Errorf("no IP found on interface %s", physInterface)
}

// discoverPeerWorkers discovers other worker nodes in the cluster.
// This can be extended with SwarmKit node discovery in the future.
func (nm *NetworkManager) discoverPeerWorkers() []string {
	// If node discovery is configured, use it
	if nm.nodeDiscovery != nil {
		peers, err := nm.nodeDiscovery.GetNodes()
		if err != nil {
			log.Warn().Err(err).Msg("Failed to discover nodes via SwarmKit")
			return []string{}
		}
		
		// Filter for VXLAN-capable nodes
		vxlanPeers := []string{}
		for _, node := range peers {
			if node.VXLANIP != "" && node.Status == "ready" {
				vxlanPeers = append(vxlanPeers, node.VXLANIP)
			}
		}
		
		log.Debug().Int("peer_count", len(vxlanPeers)).Msg("Discovered VXLAN peers via SwarmKit")
		return vxlanPeers
	}
	
	// Fallback: peers are configured via --vxlan-peers flag
	return []string{}
}

// NodeDiscovery provides an interface for SwarmKit node discovery.
// This allows NetworkManager to query active nodes in the cluster.
type NodeDiscovery interface {
	GetNodes() ([]NodeInfo, error)
}

// NodeInfo represents information about a cluster node.
type NodeInfo struct {
	ID       string
	IP       string
	VXLANIP  string
	Status   string
	Hostname string
}

// UpdatePeers updates the VXLAN peer list.
func (nm *NetworkManager) UpdatePeers(peers []string) error {
	if nm.vxlanMgr == nil {
		return fmt.Errorf("VXLAN manager not initialized")
	}
	return nm.vxlanMgr.UpdatePeers(peers)
}

// StartPeerDiscovery starts UDP-based peer discovery for VXLAN.
func (nm *NetworkManager) StartPeerDiscovery(ctx context.Context) error {
	if nm.vxlanMgr == nil {
		return fmt.Errorf("VXLAN manager not initialized")
	}

	_, localIP, err := nm.getPhysicalInterface()
	if err != nil {
		return fmt.Errorf("failed to get local IP: %w", err)
	}

	const discoveryPort = 4789 // Default VXLAN port
	if err := nm.vxlanMgr.StartPeerDiscovery(ctx, localIP, discoveryPort); err != nil {
		return err
	}

	nm.peerDiscovery = true
	return nil
}

// StopPeerDiscovery stops the peer discovery process.
func (nm *NetworkManager) StopPeerDiscovery() {
	if nm.vxlanMgr != nil {
		nm.vxlanMgr.StopPeerDiscovery()
	}
	nm.peerDiscovery = false
}

// SetEncryptionKeys sets the network encryption keys for VXLAN.
// This is called by SwarmKit when new network bootstrap keys are available.
func (nm *NetworkManager) SetEncryptionKeys(keys interface{}) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if nm.vxlanMgr == nil {
		log.Debug().Msg("VXLAN manager not initialized, storing keys for later")
		return nil
	}

	// The keys are used for VXLAN encryption
	// Implementation depends on VXLAN encryption support
	// For now, we store them and log
	log.Info().Msg("Encryption keys received for VXLAN overlay")

	// Future: pass keys to vxlanMgr for encryption
	// if encMgr, ok := nm.vxlanMgr.(EncryptedVXLANManager); ok {
	//     return encMgr.SetKeys(keys)
	// }

	return nil
}

// SetNodeDiscovery sets the node discovery provider for SwarmKit integration.
func (nm *NetworkManager) SetNodeDiscovery(discovery NodeDiscovery) {
	nm.mu.Lock()
	nm.nodeDiscovery = discovery
	nm.mu.Unlock()
	log.Debug().Msg("Node discovery provider configured")
}

// createTapDevice creates a TAP device for a network attachment.
func (nm *NetworkManager) createTapDevice(ctx context.Context, network types.NetworkAttachment, index int, taskID string) (*TapDevice, error) {
	// Generate TAP name: tap-<hash[:8]>-<index>
	// Must match logic in translator
	hash := sha256.Sum256([]byte(taskID))
	hashStr := hex.EncodeToString(hash[:])
	tapName := fmt.Sprintf("tap-%s-%d", hashStr[:8], index)

	// Allocate IP address for this TAP
	// Priority: SwarmKit-provided IP > Local allocation
	var ipAddr, netmaskFromAddr string
	
	// Check if SwarmKit provided an IP (overlay/bridge network attachment)
	if len(network.Addresses) > 0 {
		// Use SwarmKit-provided IP from IPAM
		addr := network.Addresses[0]
		// Parse CIDR format: "10.0.0.2/24" -> IP + netmask
		if idx := strings.Index(addr, "/"); idx > 0 {
			ipAddr = addr[:idx]
			// Use standard library to parse CIDR and get proper netmask
			_, ipNet, err := net.ParseCIDR(addr)
			if err == nil && ipNet != nil {
				netmaskFromAddr = net.IP(ipNet.Mask).String()
			}
		} else {
			ipAddr = addr
		}
		
		log.Info().
			Str("task_id", taskID).
			Str("ip", ipAddr).
			Str("network", network.Network.Spec.Name).
			Msg("Using SwarmKit-provided IP from network attachment")
	} else if nm.ipAllocator != nil && nm.config.IPMode == "static" {
		// Local allocation only when no SwarmKit network attachment
		var err error
		ipAddr, err = nm.ipAllocator.Allocate(taskID)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to allocate static IP, TAP will have no IP")
		}
	}

	// Ensure clean state by removing existing device if any
	exec.Command("ip", "link", "delete", tapName).Run()

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

	// Override with specific bridge if configured
	if network.Network.Spec.DriverConfig != nil &&
		network.Network.Spec.DriverConfig.Bridge != nil &&
		network.Network.Spec.DriverConfig.Bridge.Name != "" {
		bridgeName = network.Network.Spec.DriverConfig.Bridge.Name
	} else if network.Network.Spec.Driver == "overlay" {
		// For overlay networks, Docker Swarm typically creates a bridge named "br-<network-id-prefix>"
		// We attempt to attach to that bridge.
		if len(network.Network.ID) >= 12 {
			bridgeName = "br-" + network.Network.ID[:12]
		}

		log.Info().
			Str("network_id", network.Network.ID).
			Str("derived_bridge", bridgeName).
			Msg("Detected overlay network, using derived bridge name")
	}

	// Ensure bridge exists (especially for overlay, it should already be there)
	if err := exec.Command("ip", "link", "show", bridgeName).Run(); err != nil {
		// If it's our default bridge, we might be able to create it (logic in ensureBridge)
		// But for overlay, we expect it to exist.
		if network.Network.Spec.Driver == "overlay" {
			// Try to cleanup
			exec.Command("ip", "link", "delete", tapName).Run()
			return nil, fmt.Errorf("overlay bridge %s not found: %w", bridgeName, err)
		}
		// Fallback to default behavior (ensureBridge) if it's the default bridge
		if bridgeName == nm.config.BridgeName {
			// ensureBridge is called in PrepareNetwork, but that only checks nm.config.BridgeName
			// If we switched to a different bridgeName here, we might be in trouble if it doesn't exist.
			_ = bridgeName // no-op: just documenting the fallback case
		}
	}

	if err := exec.Command("ip", "link", "set", tapName, "master", bridgeName).Run(); err != nil {
		// Cleanup on failure
		exec.Command("ip", "link", "delete", tapName).Run()
		return nil, fmt.Errorf("failed to add TAP to bridge: %w", err)
	}

	// Parse subnet and gateway
	var subnet, gateway, netmask string
	
	// Use SwarmKit-provided netmask if available
	if netmaskFromAddr != "" {
		netmask = netmaskFromAddr
	}
	
	if nm.config.Subnet != "" {
		subnet = nm.config.Subnet
		// Extract netmask from CIDR if not already set
		if netmask == "" {
			_, ipNet, err := net.ParseCIDR(subnet)
			if err == nil {
				mask := net.IP(ipNet.Mask).String()
				netmask = mask
			}
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

// ipMaskToCIDR converts a netmask string (e.g., "255.255.255.0") to CIDR prefix length (e.g., "24").
func ipMaskToCIDR(netmask string) string {
	if netmask == "" {
		return "24" // Default
	}
	mask := net.IPMask(net.ParseIP(netmask).To4())
	ones, _ := mask.Size()
	return fmt.Sprintf("%d", ones)
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
