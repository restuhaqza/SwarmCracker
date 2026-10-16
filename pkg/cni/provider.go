package cni

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"

	"github.com/moby/swarmkit/v2/api"
	"github.com/moby/swarmkit/v2/manager/allocator/networkallocator"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CNIProvider implements networkallocator.Provider using CNI plugins
type CNIProvider struct {
	config       *CNIConfig
	pluginMgr    *PluginManager
	ipamMgr      *IPAMManager
	configGen    *NetworkConfigGenerator
	vxlanPort    uint32
	networks     map[string]*AllocatedNetwork
	networkIndex uint32
	mu           sync.RWMutex
}

// NewCNIProvider creates a new CNI-based network provider
func NewCNIProvider(cfg *CNIConfig) (*CNIProvider, error) {
	if cfg == nil {
		cfg = DefaultCNIConfig()
	}

	// Create plugin manager
	pluginMgr := NewPluginManager(cfg.PluginDir, cfg.ConfigDir)

	// Validate required plugins exist
	if err := pluginMgr.ValidatePlugins(); err != nil {
		return nil, fmt.Errorf("CNI plugin validation failed: %w", err)
	}

	// Create IPAM manager
	ipamMgr := NewIPAMManager(cfg)

	// Create config generator
	configGen := NewConfigGenerator()

	provider := &CNIProvider{
		config:    cfg,
		pluginMgr: pluginMgr,
		ipamMgr:   ipamMgr,
		configGen: configGen,
		vxlanPort: cfg.VXLANPort,
		networks:  make(map[string]*AllocatedNetwork),
	}

	// Initialize predefined networks
	if err := provider.initPredefinedNetworks(); err != nil {
		return nil, fmt.Errorf("failed to initialize predefined networks: %w", err)
	}

	return provider, nil
}

// NewAllocator returns a new NetworkAllocator instance
func (p *CNIProvider) NewAllocator(cfg *networkallocator.Config) (networkallocator.NetworkAllocator, error) {
	return NewCNINetworkAllocator(p, cfg)
}

// PredefinedNetworks returns predefined network data for SwarmKit
func (p *CNIProvider) PredefinedNetworks() []networkallocator.PredefinedNetworkData {
	return []networkallocator.PredefinedNetworkData{
		{
			Name:   IngressNetworkName,
			Driver: "bridge",
		},
		{
			Name:   GWBridgeNetworkName,
			Driver: "bridge",
		},
	}
}

// SetDefaultVXLANUDPPort sets the VXLAN UDP port for overlay networks
func (p *CNIProvider) SetDefaultVXLANUDPPort(port uint32) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.vxlanPort = port
	return nil
}

// ValidateNetworkDriver validates a network driver specification
func (p *CNIProvider) ValidateNetworkDriver(d *api.Driver) error {
	if d == nil {
		return nil
	}

	// Validate supported driver types
	driverName := d.Name
	switch driverName {
	case "bridge", "vxlan", "":
		return nil
	default:
		return status.Errorf(codes.InvalidArgument,
			"network driver %s not supported", driverName)
	}
}

// ValidateIngressNetworkDriver validates an ingress network driver
func (p *CNIProvider) ValidateIngressNetworkDriver(d *api.Driver) error {
	if d == nil {
		return nil
	}

	driverName := d.Name
	if driverName != "bridge" && driverName != "" {
		return status.Errorf(codes.InvalidArgument,
			"ingress network driver %s not supported, only bridge is allowed", driverName)
	}

	return nil
}

// ValidateIPAMDriver validates an IPAM driver specification
func (p *CNIProvider) ValidateIPAMDriver(d *api.Driver) error {
	if d == nil {
		return nil
	}

	driverName := d.Name
	switch driverName {
	case "host-local", "":
		return nil
	default:
		return status.Errorf(codes.InvalidArgument,
			"IPAM driver %s not supported, only host-local is allowed", driverName)
	}
}

// initPredefinedNetworks initializes predefined network configurations
func (p *CNIProvider) initPredefinedNetworks() error {
	// Create gateway bridge configuration
	gwBridgeConfig, err := p.configGen.GenerateGWBridgeConfig()
	if err != nil {
		return fmt.Errorf("failed to generate gateway bridge config: %w", err)
	}

	if err := WriteConfig(p.config.ConfigDir, GWBridgeNetworkName, gwBridgeConfig); err != nil {
		return fmt.Errorf("failed to write gateway bridge config: %w", err)
	}

	// Create loopback configuration (required by CNI)
	loopbackConfig, err := p.configGen.GenerateLoopbackConfig()
	if err != nil {
		return fmt.Errorf("failed to generate loopback config: %w", err)
	}

	if err := WriteConfig(p.config.ConfigDir, "lo", loopbackConfig); err != nil {
		return fmt.Errorf("failed to write loopback config: %w", err)
	}

	return nil
}

// GetNetwork returns an allocated network by ID
func (p *CNIProvider) GetNetwork(networkID string) (*AllocatedNetwork, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	network, exists := p.networks[networkID]
	if !exists {
		return nil, fmt.Errorf("network %s not found", networkID)
	}

	return network, nil
}

// maxSubnetAttempts bounds how many consecutive candidates are tried before a
// carve gives up. It can be reached by a pool that is genuinely full or by one
// whose next candidates are all reserved or taken, so the error says which.
const maxSubnetAttempts = 256

// isPredefinedNetworkName reports whether a name belongs to a network this
// provider defines itself (see PredefinedNetworks). Those are carved from the
// flat-L2 pool, because their task IPs have to be reachable cluster-wide.
func isPredefinedNetworkName(name string) bool {
	return name == IngressNetworkName || name == GWBridgeNetworkName
}

// cidrsOverlap reports whether two CIDRs share any address.
func cidrsOverlap(a, b *net.IPNet) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// subnetConflictLocked describes what a candidate subnet collides with, or "" when
// it is free. Naming the conflict (the reserved flat L2 subnet, or which network)
// matters because "no free subnet" is otherwise indistinguishable from a mistake
// in the pool configuration.
func (p *CNIProvider) subnetConflictLocked(candidate string) string {
	_, cidr, err := net.ParseCIDR(candidate)
	if err != nil {
		return "is not a valid CIDR"
	}
	if p.config.FlatSubnet != "" {
		if flat, _, err := ParseCIDR(p.config.FlatSubnet); err == nil && cidrsOverlap(cidr, flat) {
			return fmt.Sprintf("overlaps the flat L2 subnet %s", p.config.FlatSubnet)
		}
	}
	for _, n := range p.networks {
		if cidrsOverlap(cidr, n.Subnet) {
			return fmt.Sprintf("overlaps network %q (%s)", n.Name, n.Subnet.String())
		}
	}
	return ""
}

// ReleaseNetwork forgets a network's allocation, so its subnet becomes available
// again. Without this the ledger would keep a deleted network's subnet reserved for
// the lifetime of the daemon, and re-creating the network would be refused.
//
// It matches on the name and subnet rather than the map key: the allocator replaces
// the ID this provider generated with the SwarmKit network ID, so by the time a
// network is deallocated the ledger's key no longer identifies it.
func (p *CNIProvider) ReleaseNetwork(name, subnet string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, n := range p.networks {
		if n.Name == name && n.Subnet != nil && n.Subnet.String() == subnet {
			delete(p.networks, key)
			return
		}
	}
}

// maxVXLANAttempts bounds the search for a free VNI. The VNI space is 24-bit
// (1..maxVXLAN-1) and a cluster holds few networks, so the bound is reached only in
// pathological cases.
const (
	maxVXLANAttempts = 256
	maxVXLAN         = 16777215
)

// vxlanIDLocked returns the VNI for a network: the one it already holds when it has
// one and that value is still free, otherwise the first free candidate from the
// name's hash.
//
// The VNI has to be both stable and unique. Stable, because every node builds the
// network's VXLAN interface from it, so renumbering a live network on a restart
// leaves its replicas without an L2. Unique, because two networks with one VNI cannot
// both exist on a node: the kernel refuses the second interface and the lookup is by
// VNI, so that network silently loses cross-node connectivity (#80).
//
// A *recorded* value that another network already holds is the #80 incident itself,
// and keeping it would leave the network broken for good. It is renumbered instead:
// this path only runs for a re-allocation (a restart or an upgrade), where the
// network's tasks are gone, and the new value is persisted, so the repair sticks.
func (p *CNIProvider) vxlanIDLocked(name string, recorded uint32) uint32 {
	if recorded != 0 {
		if holder := p.vxlanIDHolderLocked(recorded); holder != "" {
			log.Warn().Uint32("vni", recorded).Str("network", name).Str("held_by", holder).
				Msg("Renumbering a network whose recorded VXLAN ID is held by another network")
		} else {
			return recorded
		}
	}

	start := GenerateVXLANID(name, p.networkIndex)
	for offset := uint32(0); offset < maxVXLANAttempts; offset++ {
		candidate := (start + offset) % maxVXLAN
		if candidate == 0 {
			candidate = maxVXLAN - 1
		}
		if p.vxlanIDHolderLocked(candidate) == "" {
			return candidate
		}
	}
	log.Warn().Str("network", name).Uint32("vni", start).
		Msg("No free VXLAN ID found; reusing the hashed one, which another network may hold")
	return start
}

// vxlanIDHolderLocked returns the name of the allocated network that holds a VNI, or
// "" when it is free.
func (p *CNIProvider) vxlanIDHolderLocked(vni uint32) string {
	if vni == 0 {
		return ""
	}
	for _, n := range p.networks {
		if n.VXLANID == vni {
			return n.Name
		}
	}
	return ""
}

// carveSubnetLocked hands the network a subnet from its pool, skipping candidates
// that are not free.
//
// A predefined network (ingress, docker_gwbridge) is carved from the flat-L2 pool
// and is exempt from the free check: its subnet *is* the flat L2 subnet, which is
// what keeps ingress task IPs reachable cluster-wide. A user-defined network is
// carved from the overlay pool instead, so it is never handed the flat L2 subnet
// (#77).
func (p *CNIProvider) carveSubnetLocked(predefined bool) (string, error) {
	pool := p.config.OverlaySubnetPool
	if predefined || pool == "" {
		pool = p.config.SubnetPool
	}
	for attempt := 0; attempt < maxSubnetAttempts; attempt++ {
		candidate, err := GenerateSubnet(pool, p.config.SubnetSize, p.networkIndex)
		if err != nil {
			return "", fmt.Errorf("failed to generate subnet: %w", err)
		}
		if predefined || p.subnetConflictLocked(candidate) == "" {
			return candidate, nil
		}
		p.networkIndex++
	}
	return "", fmt.Errorf("no free /%d subnet in pool %s after %d candidates: the flat L2 subnet %s is reserved and every other candidate was taken",
		p.config.SubnetSize, pool, maxSubnetAttempts, p.config.FlatSubnet)
}

// NetworkAllocationRequest is what the allocator asks for when it allocates a
// network, including whatever the network already holds: a re-allocation (a restart
// or an upgrade) has to keep that rather than renumber a live network (#77, #80).
type NetworkAllocationRequest struct {
	// Name and Driver identify the network.
	Name   string
	Driver string

	// RequestedSubnet and RequestedGateway are what the user asked for (empty when
	// the network is auto-allocated).
	RequestedSubnet  string
	RequestedGateway string

	// Ingress marks a predefined ingress network, which stays on the flat-L2 pool.
	Ingress bool

	// ExistingSubnet marks a subnet the network already holds. It is kept without an
	// overlap check, because moving it would break the tasks carrying addresses from
	// it.
	ExistingSubnet bool

	// RecordedVXLAN is the VNI the network already holds (0 when it has none).
	RecordedVXLAN uint32
}

// AllocateNetwork creates a new network allocation.
func (p *CNIProvider) AllocateNetwork(req NetworkAllocationRequest) (*AllocatedNetwork, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.networkIndex++

	name, driver := req.Name, req.Driver
	predefined := req.Ingress || isPredefinedNetworkName(name)

	// Use the requested subnet when given, otherwise carve one from the pool.
	subnet := req.RequestedSubnet
	switch {
	case subnet == "":
		generated, err := p.carveSubnetLocked(predefined)
		if err != nil {
			return nil, err
		}
		subnet = generated
	case !predefined && !req.ExistingSubnet:
		// Two networks on the same addresses cannot be told apart by route, NAT
		// rule or IPAM pool, so a subnet the user is asking for now has to be free
		// (#77). A recorded allocation is exempt: it is already in use.
		if reason := p.subnetConflictLocked(subnet); reason != "" {
			return nil, fmt.Errorf("subnet %s %s", subnet, reason)
		}
	}

	// Parse subnet and derive the gateway: the first usable host address. The
	// CIDR's own address is the network address, which must never be handed to
	// a gateway or a task.
	subnetNet, _, err := ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("failed to parse subnet: %w", err)
	}
	gateway := incrementIP(subnetNet.IP.To4())
	if req.RequestedGateway != "" {
		parsed := net.ParseIP(req.RequestedGateway)
		if parsed == nil {
			return nil, fmt.Errorf("invalid gateway %s", req.RequestedGateway)
		}
		gateway = parsed
	}

	// Generate bridge name
	bridgeName := fmt.Sprintf("br-%s", NetworkNameFromSwarmKit(name))

	// Create IP pool
	pool, err := p.ipamMgr.CreatePool(subnet, gateway)
	if err != nil {
		return nil, fmt.Errorf("failed to create IP pool: %w", err)
	}

	// Reserve the low infrastructure addresses on the shared L2 overlay. Each
	// node's bridge takes a low address (.1, .2, ...), so task IPs must not be
	// handed out there or they would collide with a sibling node's bridge.
	if pool != nil {
		if base := subnetNet.IP.To4(); base != nil {
			baseInt := binary.BigEndian.Uint32(base)
			for h := uint32(1); h <= infraReservedHosts; h++ {
				rip := make(net.IP, 4)
				binary.BigEndian.PutUint32(rip, baseInt+h)
				if subnetNet.Contains(rip) {
					pool.ReservedIPs = append(pool.ReservedIPs, rip)
				}
			}
		}
	}

	// VXLAN ID for overlay networks. A network that already has one keeps it; a new
	// one never takes a VNI another network holds.
	var vxlanID uint32
	if driver == "vxlan" {
		vxlanID = p.vxlanIDLocked(name, req.RecordedVXLAN)
	}

	network := &AllocatedNetwork{
		ID:          fmt.Sprintf("net-%d", p.networkIndex),
		Name:        name,
		Driver:      driver,
		Subnet:      subnetNet,
		Gateway:     gateway,
		VXLANID:     vxlanID,
		BridgeName:  bridgeName,
		Attachments: make(map[string]*NodeAttachment),
		Services:    make(map[string]*ServiceVIP),
	}

	p.networks[network.ID] = network

	// Generate CNI configuration
	var configBytes []byte
	if driver == "vxlan" {
		configBytes, err = p.configGen.GenerateVXLANConfig(
			name, subnet, gateway, vxlanID, p.vxlanPort)
	} else {
		configBytes, err = p.configGen.GenerateBridgeConfig(
			name, bridgeName, subnet, gateway)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to generate CNI config: %w", err)
	}

	// Write CNI configuration
	if err := WriteConfig(p.config.ConfigDir, name, configBytes); err != nil {
		return nil, fmt.Errorf("failed to write CNI config: %w", err)
	}

	return network, nil
}

// GetVXLANPort returns the configured VXLAN port
func (p *CNIProvider) GetVXLANPort() uint32 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.vxlanPort
}

// GetPluginManager returns the plugin manager
func (p *CNIProvider) GetPluginManager() *PluginManager {
	return p.pluginMgr
}

// GetIPAMManager returns the IPAM manager
func (p *CNIProvider) GetIPAMManager() *IPAMManager {
	return p.ipamMgr
}

// GetConfig returns the CNI configuration
func (p *CNIProvider) GetConfig() *CNIConfig {
	return p.config
}
