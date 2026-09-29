package cni

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// IPAMManager manages IP address allocation across multiple pools
type IPAMManager struct {
	pools  map[string]*IPPool // subnet -> pool
	mu     sync.RWMutex
	config *CNIConfig
}

// NewIPAMManager creates a new IPAM manager
func NewIPAMManager(config *CNIConfig) *IPAMManager {
	if config == nil {
		config = DefaultCNIConfig()
	}

	return &IPAMManager{
		pools:  make(map[string]*IPPool),
		config: config,
	}
}

// CreatePool creates a new IP pool for a subnet
func (m *IPAMManager) CreatePool(subnetCIDR string, gatewayIP net.IP) (*IPPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if pool already exists
	if pool, exists := m.pools[subnetCIDR]; exists {
		return pool, nil
	}

	// Parse subnet
	subnet, ip, err := ParseCIDR(subnetCIDR)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet: %w", err)
	}

	// If no gateway provided, use first IP in subnet
	if gatewayIP == nil {
		gatewayIP = incrementIP(ip)
	}

	pool := &IPPool{
		Subnet:      subnet,
		Gateway:     gatewayIP,
		UsedIPs:     make(map[string]string),
		ReservedIPs: []net.IP{gatewayIP}, // Reserve gateway
		NextIP:      incrementIP(gatewayIP),
	}

	m.pools[subnetCIDR] = pool
	return pool, nil
}

// AllocateIP allocates an IP address from a subnet pool
func (m *IPAMManager) AllocateIP(subnetCIDR string, ownerID string) (net.IP, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool, exists := m.pools[subnetCIDR]
	if !exists {
		return nil, fmt.Errorf("pool not found for subnet %s", subnetCIDR)
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	// Bound the search by the number of usable host addresses in the subnet.
	// The network and broadcast addresses are excluded, and a fixed attempt
	// count is avoided so that small subnets (e.g. /30) terminate with an
	// exhaustion error instead of looping or handing out the broadcast.
	ones, bits := pool.Subnet.Mask.Size()
	hostBits := bits - ones
	if hostBits < 2 {
		// /31 and /32 have no usable host addresses.
		return nil, fmt.Errorf("IP exhaustion in subnet %s", subnetCIDR)
	}
	maxAttempts := 256 // fallback for very large subnets
	if hostBits < 31 {
		maxAttempts = (1 << uint(hostBits)) - 2
	}

	// Find next available IP
	ip := pool.NextIP
	attempts := 0

	for attempts < maxAttempts {
		// Never hand out the network or broadcast address.
		if !isNetworkOrBroadcast(pool.Subnet, ip) {
			ipStr := ip.String()

			// Check if IP is already used or reserved
			if _, used := pool.UsedIPs[ipStr]; !used && !isReserved(pool, ip) {
				// IP is available
				pool.UsedIPs[ipStr] = ownerID
				pool.NextIP = incrementIP(ip)
				return ip, nil
			}
		}

		// Try next IP
		ip = incrementIP(ip)
		attempts++

		// Check if we've gone past the subnet
		if !pool.Subnet.Contains(ip) {
			// Reset to start of subnet (after gateway)
			ip = incrementIP(pool.Gateway)
		}
	}

	return nil, fmt.Errorf("IP exhaustion in subnet %s", subnetCIDR)
}

// ReleaseIP releases an allocated IP address
func (m *IPAMManager) ReleaseIP(ip net.IP, subnetCIDR string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool, exists := m.pools[subnetCIDR]
	if !exists {
		return fmt.Errorf("pool not found for subnet %s", subnetCIDR)
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	ipStr := ip.String()
	if _, used := pool.UsedIPs[ipStr]; !used {
		return fmt.Errorf("IP %s not allocated", ipStr)
	}

	delete(pool.UsedIPs, ipStr)
	return nil
}

// GetPoolStats returns statistics for an IP pool
func (m *IPAMManager) GetPoolStats(subnetCIDR string) (used, total int, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	pool, exists := m.pools[subnetCIDR]
	if !exists {
		return 0, 0, fmt.Errorf("pool not found for subnet %s", subnetCIDR)
	}

	pool.mu.RLock()
	defer pool.mu.RUnlock()

	// Calculate total IPs in subnet
	ones, bits := pool.Subnet.Mask.Size()
	total = 1 << (bits - ones)
	total -= 2                     // Subtract network and broadcast addresses
	total -= len(pool.ReservedIPs) // Subtract reserved IPs

	used = len(pool.UsedIPs)
	return used, total, nil
}

// ListPools returns all managed pools
func (m *IPAMManager) ListPools() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	pools := make([]string, 0, len(m.pools))
	for subnet := range m.pools {
		pools = append(pools, subnet)
	}
	return pools
}

// AllocateVIP allocates a VIP for a service
func (m *IPAMManager) AllocateVIP(subnetCIDR string, serviceID string) (net.IP, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool, exists := m.pools[subnetCIDR]
	if !exists {
		return nil, fmt.Errorf("pool not found for subnet %s", subnetCIDR)
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	// VIPs are allocated from the higher end of the subnet
	// to avoid conflicts with node attachments
	vipStart := getVIPRangeStart(pool.Subnet)
	if vipStart == nil {
		return nil, fmt.Errorf("VIPs are not supported for subnet %s", subnetCIDR)
	}

	ip := vipStart
	attempts := 0
	maxAttempts := 16 // Reserve last 16 IPs for VIPs

	for attempts < maxAttempts {
		// Stop at the subnet boundary and never hand out the network or
		// broadcast address (matters for small subnets).
		if !pool.Subnet.Contains(ip) || isNetworkOrBroadcast(pool.Subnet, ip) {
			break
		}

		ipStr := ip.String()
		if _, used := pool.UsedIPs[ipStr]; !used && !isReserved(pool, ip) {
			pool.UsedIPs[ipStr] = "vip:" + serviceID
			return ip, nil
		}

		ip = decrementIP(ip)
		attempts++
	}

	return nil, fmt.Errorf("VIP exhaustion in subnet %s", subnetCIDR)
}

// ReleaseVIP releases a service VIP
func (m *IPAMManager) ReleaseVIP(vip net.IP, subnetCIDR string, serviceID string) error {
	return m.ReleaseIP(vip, subnetCIDR)
}

// GetAllocationOwner returns the owner of an allocated IP
func (m *IPAMManager) GetAllocationOwner(ip net.IP, subnetCIDR string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	pool, exists := m.pools[subnetCIDR]
	if !exists {
		return "", fmt.Errorf("pool not found for subnet %s", subnetCIDR)
	}

	pool.mu.RLock()
	defer pool.mu.RUnlock()

	ipStr := ip.String()
	owner, exists := pool.UsedIPs[ipStr]
	if !exists {
		return "", fmt.Errorf("IP %s not allocated", ipStr)
	}

	return owner, nil
}

// PrunePools removes IP pools whose subnet is not in the active set.
func (m *IPAMManager) PrunePools(activeSubnets map[string]struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for subnet := range m.pools {
		if _, ok := activeSubnets[subnet]; !ok {
			delete(m.pools, subnet)
		}
	}
}

// CleanupStaleAllocations removes allocations older than a threshold
func (m *IPAMManager) CleanupStaleAllocations(subnetCIDR string, olderThan time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool, exists := m.pools[subnetCIDR]
	if !exists {
		return 0
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	// Note: In a real implementation, we'd track allocation timestamps
	// For now, this is a placeholder that returns 0
	return 0
}

// Helper functions

// incrementIP returns the next IP address
func incrementIP(ip net.IP) net.IP {
	next := make([]byte, len(ip))
	copy(next, ip)

	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}

	return net.IP(next)
}

// decrementIP returns the previous IP address
func decrementIP(ip net.IP) net.IP {
	prev := make([]byte, len(ip))
	copy(prev, ip)

	for i := len(prev) - 1; i >= 0; i-- {
		prev[i]--
		if prev[i] != 255 {
			break
		}
	}

	return net.IP(prev)
}

// isReserved checks if an IP is in the reserved list
func isReserved(pool *IPPool, ip net.IP) bool {
	for _, reserved := range pool.ReservedIPs {
		if ip.Equal(reserved) {
			return true
		}
	}
	return false
}

// getVIPRangeStart returns the first VIP candidate for a subnet.
//
// For IPv4, VIPs are allocated from the top of the subnet to avoid conflicts
// with normal node/container attachments, so the start is the highest usable
// host address: the broadcast address minus one (e.g. 10.0.0.254 for a /24).
// AllocateVIP decrements from here, skipping used and reserved addresses.
//
// Non-IPv4 (or unparseable) subnets return nil; IPv6 VIPs are not supported.
func getVIPRangeStart(subnet *net.IPNet) net.IP {
	baseIP := subnet.IP.To4()
	if baseIP == nil {
		return nil
	}

	// Highest usable host address = broadcast (all host bits set) - 1.
	return decrementIP(broadcastIP(subnet))
}

// broadcastIP returns the broadcast address of an IPv4/IPv6 subnet.
func broadcastIP(subnet *net.IPNet) net.IP {
	base := subnet.IP
	if v4 := base.To4(); v4 != nil {
		base = v4
	}

	broadcast := make(net.IP, len(base))
	copy(broadcast, base)
	for i := range broadcast {
		broadcast[i] |= ^subnet.Mask[i]
	}
	return broadcast
}

// isNetworkOrBroadcast reports whether ip is the network or broadcast address
// of the given subnet. Neither may ever be allocated to a workload.
func isNetworkOrBroadcast(subnet *net.IPNet, ip net.IP) bool {
	if ip.Equal(subnet.IP) {
		return true
	}
	return ip.Equal(broadcastIP(subnet))
}
