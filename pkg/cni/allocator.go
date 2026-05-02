package cni

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/moby/swarmkit/v2/manager/allocator/networkallocator"
)

// CNINetworkAllocator implements networkallocator.NetworkAllocator using CNI
type CNINetworkAllocator struct {
	provider      *CNIProvider
	config        *networkallocator.Config
	allocatedNets map[string]*AllocatedNetwork
	mu            sync.RWMutex
}

// NewCNINetworkAllocator creates a new CNI network allocator
func NewCNINetworkAllocator(provider *CNIProvider, cfg *networkallocator.Config) (*CNINetworkAllocator, error) {
	if provider == nil {
		return nil, fmt.Errorf("provider cannot be nil")
	}

	allocator := &CNINetworkAllocator{
		provider:      provider,
		config:        cfg,
		allocatedNets: make(map[string]*AllocatedNetwork),
	}

	// Initialize with config defaults if provided
	if cfg != nil {
		if cfg.VXLANUDPPort > 0 {
			provider.SetDefaultVXLANUDPPort(cfg.VXLANUDPPort)
		}
	}

	return allocator, nil
}

// ===== Network Allocation =====

// IsAllocated returns if the network has been allocated
func (a *CNINetworkAllocator) IsAllocated(n *api.Network) bool {
	if n == nil {
		return false
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	_, exists := a.allocatedNets[n.ID]
	return exists
}

// Allocate allocates all necessary resources for a network
func (a *CNINetworkAllocator) Allocate(n *api.Network) error {
	if n == nil {
		return fmt.Errorf("network cannot be nil")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Check if already allocated
	if _, exists := a.allocatedNets[n.ID]; exists {
		return nil // Already allocated
	}

	// Get driver type
	driver := "bridge"
	if n.Spec.DriverConfig != nil && n.Spec.DriverConfig.Name != "" {
		driver = n.Spec.DriverConfig.Name
	}

	// Validate driver
	if err := a.provider.ValidateNetworkDriver(n.Spec.DriverConfig); err != nil {
		return err
	}

	// Get network name
	name := n.Spec.Annotations.Name
	if name == "" {
		name = n.ID
	}

	// Allocate network from provider
	allocatedNet, err := a.provider.AllocateNetwork(name, driver)
	if err != nil {
		return fmt.Errorf("failed to allocate network: %w", err)
	}

	// Update with SwarmKit network ID
	allocatedNet.ID = n.ID

	// Set ingress flag if applicable
	if n.Spec.Ingress {
		allocatedNet.Ingress = true
	}

	// Store allocation
	a.allocatedNets[n.ID] = allocatedNet

	// Update network spec with allocation info
	n.DriverState = &api.Driver{
		Name:    driver,
		Options: allocatedNet.DriverState(),
	}
	n.IPAM = allocatedNet.IPAMState()

	return nil
}

// Deallocate frees all resources assigned to a network
func (a *CNINetworkAllocator) Deallocate(n *api.Network) error {
	if n == nil {
		return fmt.Errorf("network cannot be nil")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	allocatedNet, exists := a.allocatedNets[n.ID]
	if !exists {
		return nil // Not allocated
	}

	// Remove CNI configuration
	if err := RemoveCNIConfig(a.provider.config.ConfigDir, allocatedNet.Name); err != nil {
		// Log error but continue
		fmt.Printf("warning: failed to remove CNI config: %v\n", err)
	}

	// Remove IP pool
	a.provider.ipamMgr.mu.Lock()
	delete(a.provider.ipamMgr.pools, allocatedNet.Subnet.String())
	a.provider.ipamMgr.mu.Unlock()

	// Remove from allocated networks
	delete(a.allocatedNets, n.ID)

	return nil
}

// ===== Service Allocation =====

// IsServiceAllocated returns if the service has network resources allocated
func (a *CNINetworkAllocator) IsServiceAllocated(s *api.Service, flags ...func(*networkallocator.ServiceAllocationOpts)) bool {
	if s == nil {
		return false
	}

	// Parse options
	opts := &networkallocator.ServiceAllocationOpts{}
	for _, flag := range flags {
		flag(opts)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	// Check if service has VIPs allocated in all its networks
	for _, networkSpec := range s.Spec.Networks {
		allocatedNet, exists := a.allocatedNets[networkSpec.Target]
		if !exists {
			return false
		}

		allocatedNet.mu.RLock()
		_, vipExists := allocatedNet.Services[s.ID]
		allocatedNet.mu.RUnlock()

		if !vipExists {
			return false
		}
	}

	return true
}

// AllocateService allocates VIPs and ports for a service
func (a *CNINetworkAllocator) AllocateService(s *api.Service) error {
	if s == nil {
		return fmt.Errorf("service cannot be nil")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Allocate VIPs for each network the service is attached to
	for _, networkSpec := range s.Spec.Networks {
		allocatedNet, exists := a.allocatedNets[networkSpec.Target]
		if !exists {
			return fmt.Errorf("network %s not allocated", networkSpec.Target)
		}

		// Allocate VIP
		vip, err := a.provider.ipamMgr.AllocateVIP(allocatedNet.Subnet.String(), s.ID)
		if err != nil {
			return fmt.Errorf("failed to allocate VIP: %w", err)
		}

		// Store VIP allocation
		allocatedNet.mu.Lock()
		allocatedNet.Services[s.ID] = &ServiceVIP{
			ServiceID:      s.ID,
			NetworkID:      allocatedNet.ID,
			VIP:            vip,
			PublishedPorts: parsePublishedPorts(s),
			AllocatedAt:    time.Now(),
		}
		allocatedNet.mu.Unlock()
	}

	// Allocate published ports (ingress network)
	if s.Spec.Endpoint != nil && len(s.Spec.Endpoint.Ports) > 0 {
		// Find ingress network
		for netID, allocatedNet := range a.allocatedNets {
			if allocatedNet.Ingress {
				// Store service in ingress network
				allocatedNet.mu.Lock()
				allocatedNet.Services[s.ID] = &ServiceVIP{
					ServiceID:      s.ID,
					NetworkID:      netID,
					PublishedPorts: parsePublishedPorts(s),
					AllocatedAt:    time.Now(),
				}
				allocatedNet.mu.Unlock()
				break
			}
		}
	}

	return nil
}

// DeallocateService frees VIPs and ports for a service
func (a *CNINetworkAllocator) DeallocateService(s *api.Service) error {
	if s == nil {
		return fmt.Errorf("service cannot be nil")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Remove VIPs from all networks
	for _, networkSpec := range s.Spec.Networks {
		allocatedNet, exists := a.allocatedNets[networkSpec.Target]
		if !exists {
			continue
		}

		allocatedNet.mu.Lock()
		if vipAlloc, vipExists := allocatedNet.Services[s.ID]; vipExists {
			// Release VIP
			a.provider.ipamMgr.ReleaseVIP(vipAlloc.VIP, allocatedNet.Subnet.String(), s.ID)
			delete(allocatedNet.Services, s.ID)
		}
		allocatedNet.mu.Unlock()
	}

	// Remove from ingress network
	for _, allocatedNet := range a.allocatedNets {
		if allocatedNet.Ingress {
			allocatedNet.mu.Lock()
			delete(allocatedNet.Services, s.ID)
			allocatedNet.mu.Unlock()
			break
		}
	}

	return nil
}

// ===== Task Allocation =====

// IsTaskAllocated returns if the task has network resources allocated
func (a *CNINetworkAllocator) IsTaskAllocated(t *api.Task) bool {
	if t == nil {
		return false
	}

	// Task is allocated if all its network attachments have IPs
	for _, attachment := range t.Networks {
		if len(attachment.Addresses) == 0 {
			return false
		}
	}

	return true
}

// AllocateTask allocates IPs for all networks a task is attached to
func (a *CNINetworkAllocator) AllocateTask(t *api.Task) error {
	if t == nil {
		return fmt.Errorf("task cannot be nil")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, attachment := range t.Networks {
		allocatedNet, exists := a.allocatedNets[attachment.Network.ID]
		if !exists {
			return fmt.Errorf("network %s not allocated", attachment.Network.ID)
		}

		// Allocate IP for task
		ip, err := a.provider.ipamMgr.AllocateIP(allocatedNet.Subnet.String(), t.ID)
		if err != nil {
			return fmt.Errorf("failed to allocate IP for task: %w", err)
		}

		// Update task attachment
		attachment.Addresses = []string{ip.String()}
	}

	return nil
}

// DeallocateTask releases IPs for all networks a task is attached to
func (a *CNINetworkAllocator) DeallocateTask(t *api.Task) error {
	if t == nil {
		return fmt.Errorf("task cannot be nil")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, attachment := range t.Networks {
		allocatedNet, exists := a.allocatedNets[attachment.Network.ID]
		if !exists {
			continue
		}

		// Parse IP from address
		if len(attachment.Addresses) == 0 {
			continue
		}
		ip := net.ParseIP(attachment.Addresses[0])
		if ip == nil {
			continue
		}

		// Release IP
		a.provider.ipamMgr.ReleaseIP(ip, allocatedNet.Subnet.String())
		attachment.Addresses = nil
	}

	return nil
}

// ===== Node Attachment Allocation =====

// AllocateAttachment allocates a load balancer endpoint for a node
// THIS IS THE KEY METHOD THAT FIXES THE "network support unavailable" ERROR
func (a *CNINetworkAllocator) AllocateAttachment(node *api.Node, na *api.NetworkAttachment) error {
	if node == nil || na == nil {
		return fmt.Errorf("node and attachment cannot be nil")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Find the allocated network
	allocatedNet, exists := a.allocatedNets[na.Network.ID]
	if !exists {
		return fmt.Errorf("network %s not allocated", na.Network.ID)
	}

	// Allocate IP for node attachment
	ip, err := a.provider.ipamMgr.AllocateIP(allocatedNet.Subnet.String(), node.ID)
	if err != nil {
		return fmt.Errorf("failed to allocate IP for node attachment: %w", err)
	}

	// Create node attachment record
	attachment := &NodeAttachment{
		NodeID:      node.ID,
		NetworkID:   allocatedNet.ID,
		IPAddress:   ip,
		VXLANVNI:    allocatedNet.VXLANID,
		AllocatedAt: time.Now(),
	}

	// Store in network
	allocatedNet.mu.Lock()
	allocatedNet.Attachments[node.ID] = attachment
	allocatedNet.mu.Unlock()

	// Update SwarmKit network attachment
	na.Addresses = []string{ip.String()}
	if allocatedNet.Driver == "vxlan" {
		// Set VXLAN VNI for overlay networks
		na.DriverAttachmentOpts = map[string]string{
			"vxlan_vni": fmt.Sprintf("%d", allocatedNet.VXLANID),
		}
	}

	return nil
}

// DeallocateAttachment frees a load balancer endpoint for a node
func (a *CNINetworkAllocator) DeallocateAttachment(node *api.Node, na *api.NetworkAttachment) error {
	if node == nil || na == nil {
		return fmt.Errorf("node and attachment cannot be nil")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	allocatedNet, exists := a.allocatedNets[na.Network.ID]
	if !exists {
		return nil // Network not allocated, nothing to deallocate
	}

	allocatedNet.mu.Lock()
	defer allocatedNet.mu.Unlock()

	attachment, exists := allocatedNet.Attachments[node.ID]
	if !exists {
		return nil // Not attached
	}

	// Release IP
	a.provider.ipamMgr.ReleaseIP(attachment.IPAddress, allocatedNet.Subnet.String())

	// Remove attachment
	delete(allocatedNet.Attachments, node.ID)

	// Clear SwarmKit attachment
	na.Addresses = nil
	na.DriverAttachmentOpts = nil

	return nil
}

// IsAttachmentAllocated returns if a node attachment is allocated
func (a *CNINetworkAllocator) IsAttachmentAllocated(node *api.Node, na *api.NetworkAttachment) bool {
	if node == nil || na == nil {
		return false
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	allocatedNet, exists := a.allocatedNets[na.Network.ID]
	if !exists {
		return false
	}

	allocatedNet.mu.RLock()
	defer allocatedNet.mu.RUnlock()

	_, attached := allocatedNet.Attachments[node.ID]
	return attached
}

// ===== Helper Methods =====

// GetAllocatedNetwork returns an allocated network by ID
func (a *CNINetworkAllocator) GetAllocatedNetwork(networkID string) (*AllocatedNetwork, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	net, exists := a.allocatedNets[networkID]
	if !exists {
		return nil, fmt.Errorf("network %s not found", networkID)
	}

	return net, nil
}

// ListAllocatedNetworks returns all allocated networks
func (a *CNINetworkAllocator) ListAllocatedNetworks() []*AllocatedNetwork {
	a.mu.RLock()
	defer a.mu.RUnlock()

	networks := make([]*AllocatedNetwork, 0, len(a.allocatedNets))
	for _, net := range a.allocatedNets {
		networks = append(networks, net)
	}

	return networks
}

// RunGC runs garbage collection for stale allocations
func (a *CNINetworkAllocator) RunGC(ctx context.Context) error {
	// Implementation for cleaning up stale allocations
	// This would be called periodically to clean up orphaned IPs
	return nil
}

// ===== DriverState and IPAMState helpers =====

// DriverState returns driver-specific state for SwarmKit
func (n *AllocatedNetwork) DriverState() map[string]string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	state := map[string]string{
		"bridge": n.BridgeName,
	}

	if n.Driver == "vxlan" {
		state["vxlan_vni"] = fmt.Sprintf("%d", n.VXLANID)
	}

	return state
}

// IPAMState returns IPAM state for SwarmKit
func (n *AllocatedNetwork) IPAMState() *api.IPAMOptions {
	n.mu.RLock()
	defer n.mu.RUnlock()

	return &api.IPAMOptions{
		Configs: []*api.IPAMConfig{
			{
				Subnet:  n.Subnet.String(),
				Gateway: n.Gateway.String(),
			},
		},
	}
}

// parsePublishedPorts extracts published ports from a service spec
func parsePublishedPorts(s *api.Service) []PublishedPort {
	if s.Spec.Endpoint == nil {
		return nil
	}

	ports := make([]PublishedPort, 0, len(s.Spec.Endpoint.Ports))
	for _, p := range s.Spec.Endpoint.Ports {
		ports = append(ports, PublishedPort{
			Port:          p.TargetPort,
			PublishedPort: p.PublishedPort,
			Protocol:      p.Protocol.String(),
			PublishMode:   p.PublishMode.String(),
		})
	}

	return ports
}

// RemoveCNIConfig removes a CNI configuration file
func RemoveCNIConfig(configDir, networkName string) error {
	// Implementation would remove the .conf or .conflist file
	// Placeholder for now
	return nil
}