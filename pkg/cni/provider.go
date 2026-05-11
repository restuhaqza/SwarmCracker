package cni

import (
	"fmt"
	"sync"

	"github.com/moby/swarmkit/v2/api"
	"github.com/moby/swarmkit/v2/manager/allocator/networkallocator"
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

// AllocateNetwork creates a new network allocation
func (p *CNIProvider) AllocateNetwork(name, driver string) (*AllocatedNetwork, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Generate subnet
	p.networkIndex++
	subnet, err := GenerateSubnet(p.config.SubnetPool, p.config.SubnetSize, p.networkIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to generate subnet: %w", err)
	}

	// Parse subnet and get gateway
	subnetNet, gateway, err := ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("failed to parse subnet: %w", err)
	}

	// Generate bridge name
	bridgeName := fmt.Sprintf("br-%s", NetworkNameFromSwarmKit(name))

	// Create IP pool
	_, err = p.ipamMgr.CreatePool(subnet, gateway)
	if err != nil {
		return nil, fmt.Errorf("failed to create IP pool: %w", err)
	}

	// Generate VXLAN ID for overlay networks
	var vxlanID uint32
	if driver == "vxlan" {
		vxlanID = GenerateVXLANID(name, p.networkIndex)
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
