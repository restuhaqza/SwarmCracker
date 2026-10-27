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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// Executable command functions - overridable for testing
var (
	execCommand  = exec.Command
	execLookPath = exec.LookPath
	osRemoveAll  = os.RemoveAll
	osRemove     = os.Remove
)

// NetworkManager manages VM networking.
//
//nolint:revive // Stuttering name is acceptable here for package clarity
type NetworkManager struct {
	config      types.NetworkConfig
	bridges     map[string]bool
	mu          sync.RWMutex
	tapDevices  map[string]*TapDevice
	ipAllocator *IPAllocator
	natSetup    bool

	// runtimeDir holds the per-bridge dnsmasq files (pid, log, leases and the
	// service-name records). It defaults to dnsmasqRuntimeDirDefault; a test
	// points a manager at a temporary directory instead.
	runtimeDir string

	// nodeIndex/nodeSlots partition the shared overlay into disjoint per-node
	// IP blocks (see IPAllocator). nodeSlots <= 1 means single-node.
	nodeIndex uint32
	nodeSlots uint32

	vxlanMgr      *VXLANManager
	peerDiscovery bool
	nodeDiscovery types.NodeDiscovery // SwarmKit node discovery provider
	cniClient     *CNIClient          // CNI client for SwarmKit network attachments
	pendingPeers  []string            // Peers queued before VXLAN init

	// managedNetworks holds user-defined networks materialized on this node
	// (each with its own bridge + optional VXLAN), keyed by SwarmKit network ID.
	// Guarded by mu.
	managedNetworks map[string]*managedNetwork

	// publishedPorts tracks host ports currently forwarded, keyed by
	// "<proto>:<hostPort>", to detect collisions between tasks. It is guarded
	// by mu.
	publishedPorts map[string]string

	// ingressRules holds the canonical form of the last applied ingress load
	// balancing rule set, so a reconcile that changes nothing is a no-op. It is
	// guarded by mu.
	ingressRules string

	// ingressProgrammed is set once the first ingress reconcile has run, so
	// stale rules from a previous daemon run are swept even when the desired
	// set is empty.
	ingressProgrammed bool

	// dnsRecords holds the canonical form of the last written service-name
	// records file, so a reconcile that changes nothing does not rewrite it or
	// reload dnsmasq. dnsProgrammed is set once the first DNS reconcile has
	// run, so stale records from a previous daemon run are cleared even when
	// the desired set is empty. Both are guarded by mu.
	dnsRecords    string
	dnsProgrammed bool

	// dnsmasqMu serializes the dnsmasq lifecycle. Without it, concurrent VM
	// creations race to kill and restart the DHCP server, leaking orphaned
	// instances and logging "Address already in use".
	dnsmasqMu sync.Mutex
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

	// nodeIndex and nodeSlots partition the subnet into disjoint per-node
	// blocks so that two nodes sharing the flat L2 overlay can never hand out
	// the same address. nodeIndex is 1-based; nodeSlots <= 1 disables
	// partitioning (single-node behaviour, which uses the whole subnet).
	nodeIndex uint32
	nodeSlots uint32
}

// reservedInfraHosts is the number of leading host addresses in a subnet that
// are never handed to VMs. In a multi-node cluster the overlay is a single L2
// subnet shared by every node, and each node's bridge takes a low address
// (e.g. 192.168.127.1, .2, ...). Without this reservation a guest on one node
// can be assigned another node's bridge IP, which collides on the overlay and
// breaks cross-node traffic (and DNAT targets for published ports).
const reservedInfraHosts = 16

// NewIPAllocator creates a new IP allocator that may use the whole subnet.
// Callers that run on a shared multi-node overlay should use
// NewIPAllocatorForNode instead.
func NewIPAllocator(subnetStr, gatewayStr string) (*IPAllocator, error) {
	return newIPAllocator(subnetStr, gatewayStr, 0, 1)
}

// NewIPAllocatorForNode creates an IP allocator restricted to block nodeIndex
// (1-based) of nodeSlots disjoint blocks within the subnet. The overlay is a
// single L2 segment shared by every node, so each node must only hand out
// addresses from its own block; otherwise two nodes can assign the same IP.
//
// nodeSlots <= 1 or nodeIndex == 0 disables partitioning (the whole subnet is
// used), which preserves single-node behaviour.
func NewIPAllocatorForNode(subnetStr, gatewayStr string, nodeIndex, nodeSlots uint32) (*IPAllocator, error) {
	return newIPAllocator(subnetStr, gatewayStr, nodeIndex, nodeSlots)
}

func newIPAllocator(subnetStr, gatewayStr string, nodeIndex, nodeSlots uint32) (*IPAllocator, error) {
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
		nodeIndex: nodeIndex,
		nodeSlots: nodeSlots,
	}, nil
}

// nodeAllocationParams derives this node's 1-based slot from its overlay bridge
// address and the number of slots (reservedInfraHosts, matching the reserved
// bridge range). It returns (0, 1) — disabling partitioning — when the node is
// not part of a multi-node overlay, so a single node keeps the whole subnet.
//
// The bridge address on the shared overlay is <subnet>.k for node k (the
// cluster commands assign .1, .2, ...), so its last octet is the node slot.
func nodeAllocationParams(bridgeIP string, peerCount int) (uint32, uint32) {
	if peerCount <= 0 {
		return 0, 1
	}
	ip := net.ParseIP(strings.Split(bridgeIP, "/")[0])
	if ip == nil {
		return 0, 1
	}
	v4 := ip.To4()
	if v4 == nil {
		return 0, 1
	}
	idx := uint32(v4[3])
	if idx < 1 || idx > reservedInfraHosts {
		// Not a per-node bridge address (see reservedInfraHosts); fall back to
		// unpartitioned allocation rather than risk an out-of-range slot.
		log.Warn().
			Str("bridge_ip", bridgeIP).
			Uint32("slots", reservedInfraHosts).
			Msg("Bridge address is not in the per-node range; overlay IPs will not be partitioned")
		return 0, 1
	}
	return idx, reservedInfraHosts
}

// NodeLocalGateway returns the address this node holds on a network with the given
// subnet — the address its bridge takes, and the one its own guests use as their
// default gateway.
//
// It mirrors the default overlay, where each node's bridge takes <subnet>.<slot>
// (--bridge-ip gives .1 to the first node, .2 to the second, ...). Handing every node
// the same address instead puts two ARP respondents for one address on a single L2: a
// user-defined network's bridge is fanned out over its own VXLAN, so all nodes share
// that segment, and whichever node answers first becomes the guest's gateway (#76).
//
// clusterGateway is the cluster-wide address recorded in the network's IPAM — its
// first usable host, unless the network was created with an explicit gateway. It is
// returned unchanged when this node has no slot (a single node, or an overlay bridge
// address outside the reserved range), for node 1 (which holds whatever the network
// records, so a network created with --gateway keeps that gateway — and the guests'
// default route with it) and for a subnet too small for the per-node guest blocks
// (a /28 or smaller, where the allocator partitions nothing either, so a per-node
// bridge address would just take an address a guest could be handed). Otherwise it
// returns <subnet>.<slot>, which the reserved range keeps out of the guest pool.
//
// The value is IPv4 only: an IPv6 subnet keeps the cluster-wide address. The
// cluster-wide address stays reachable from every node over the VXLAN either way.
func NodeLocalGateway(subnet, clusterGateway, bridgeIP string, peerCount int) string {
	slot, slots := nodeAllocationParams(bridgeIP, peerCount)
	if slot == 0 || slots <= 1 {
		return clusterGateway
	}
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return clusterGateway
	}
	v4 := ipNet.IP.To4()
	if v4 == nil {
		return clusterGateway
	}
	if slot == 1 {
		return clusterGateway
	}
	if slot > reservedInfraHosts {
		return clusterGateway
	}
	ones, bits := ipNet.Mask.Size()
	if bits != 32 {
		return clusterGateway
	}
	// Both bounds mirror the allocator's: below reservedInfraHosts+4 addresses it
	// reserves nothing, and slot > size-2 would be the broadcast address.
	size := uint32(1) << (bits - ones)
	if size < uint32(reservedInfraHosts)+4 || slot > size-2 {
		return clusterGateway
	}
	candidate := make(net.IP, len(v4))
	copy(candidate, v4)
	candidate[3] += byte(slot)
	if !ipNet.Contains(candidate) {
		return clusterGateway
	}
	return candidate.String()
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

	// Partitioned allocation (shared multi-node overlay): pick deterministically
	// within this node's block and linear-probe inside it, so two nodes can
	// never hand out the same address.
	if start, end, ok := a.nodeBlock(); ok {
		count := end - start
		base := binary.BigEndian.Uint32(a.subnet.IP.To4())
		reserved := a.reservedCount()
		sum := sha256.Sum256([]byte(vmID))
		off := binary.BigEndian.Uint32(sum[:4]) % count
		for i := uint32(0); i < count; i++ {
			host := start + (off+i)%count
			ip := make(net.IP, 4)
			binary.BigEndian.PutUint32(ip, base+1+reserved+host)
			ipStr := ip.String()
			if _, taken := a.allocated[ipStr]; !taken {
				a.allocated[ipStr] = vmID
				return ipStr, nil
			}
		}
		return "", fmt.Errorf("failed to allocate IP: node block exhausted (%d addresses in slot %d/%d)",
			count, a.nodeIndex, a.nodeSlots)
	}

	// Legacy allocation: whole subnet, hash + linear probing.
	return a.allocateHashed(vmID)
}

// nodeBlock returns the [start, end) host offsets (relative to the first usable
// address) this allocator may use, and whether partitioning applies. When it
// returns ok == false the allocator uses the whole subnet (legacy behaviour).
func (a *IPAllocator) nodeBlock() (uint32, uint32, bool) {
	if a.nodeSlots <= 1 || a.nodeIndex == 0 {
		return 0, 0, false
	}
	ones, bits := a.subnet.Mask.Size()
	if bits != 32 {
		return 0, 0, false
	}
	reserved := a.reservedCount()
	if reserved == 0 {
		// Subnet too small to reserve infrastructure; keep legacy behaviour.
		return 0, 0, false
	}
	size := uint32(1) << (bits - ones)
	if size <= 2+reserved {
		return 0, 0, false
	}
	usable := size - 2 - reserved
	blockSize := usable / a.nodeSlots
	if blockSize == 0 {
		return 0, 0, false
	}
	idx := a.nodeIndex
	if idx > a.nodeSlots {
		idx = a.nodeSlots
	}
	start := (idx - 1) * blockSize
	end := start + blockSize
	if idx == a.nodeSlots {
		// Last slot absorbs any remainder so no addresses are wasted.
		end = usable
	}
	return start, end, true
}

// allocateHashed is the legacy whole-subnet allocator: hash the VM ID to a
// starting address and linear-probe until a free address is found.
func (a *IPAllocator) allocateHashed(vmID string) (string, error) {
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
		isReserved := a.isReserved(ip)
		_, isAllocated := a.allocated[ipStr]

		if !isGateway && !isReserved && !isAllocated {
			// Found free IP
			a.allocated[ipStr] = vmID
			return ipStr, nil
		}

		// Try next IP
		ip = incIP(ip)

		// Wrap around or check if still in subnet
		if !a.subnet.Contains(ip) {
			// Reset to the first usable address (skips the network address
			// and the reserved infrastructure block).
			ip = a.firstUsable()
		}
	}

	return "", fmt.Errorf("failed to allocate IP: subnet exhausted or too many collisions")
}

// reservedCount returns how many leading host addresses are reserved for
// infrastructure on this subnet. It is zero for IPv6 or subnets too small to
// spare the block.
func (a *IPAllocator) reservedCount() uint32 {
	ones, bits := a.subnet.Mask.Size()
	if bits != 32 {
		return 0
	}
	size := uint32(1) << (bits - ones)
	if size < uint32(reservedInfraHosts)+4 {
		return 0
	}
	return uint32(reservedInfraHosts)
}

// isReserved reports whether an IP falls in the reserved infrastructure range.
func (a *IPAllocator) isReserved(ip net.IP) bool {
	r := a.reservedCount()
	if r == 0 {
		return false
	}
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	host := uint32(v4[3])
	return host >= 1 && host <= r
}

// firstUsable returns the first assignable address (skipping the network
// address and the reserved infrastructure block).
func (a *IPAllocator) firstUsable() net.IP {
	ip := make(net.IP, len(a.subnet.IP))
	copy(ip, a.subnet.IP)
	steps := 1 + int(a.reservedCount())
	for k := 0; k < steps; k++ {
		ip = incIP(ip)
	}
	return ip
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

	// Use hash to pick an offset within the usable range. Skip the network and
	// broadcast addresses, and the low addresses reserved for node bridges.
	reserved := a.reservedCount()
	count := size - 2 - reserved
	n := binary.BigEndian.Uint32(hash[:4]) % count

	ip := make(net.IP, 4)
	ipInt := binary.BigEndian.Uint32(a.subnet.IP.To4()) + 1 + reserved + n
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
	// Partition the shared overlay into disjoint per-node blocks when this node
	// is part of a multi-node cluster (VXLAN peers configured), so two nodes can
	// never assign the same guest IP. Single-node keeps the whole subnet.
	nodeIndex, nodeSlots := nodeAllocationParams(config.BridgeIP, len(config.VXLANPeers))

	nm := &NetworkManager{
		config:          config,
		bridges:         make(map[string]bool),
		tapDevices:      make(map[string]*TapDevice),
		publishedPorts:  make(map[string]string),
		managedNetworks: make(map[string]*managedNetwork),
		nodeIndex:       nodeIndex,
		nodeSlots:       nodeSlots,
		runtimeDir:      dnsmasqRuntimeDirDefault,
	}

	// Initialize IP allocator if subnet and bridge IP are configured
	if config.Subnet != "" && config.BridgeIP != "" {
		// Extract gateway IP from bridge IP (remove CIDR)
		gatewayStr := strings.Split(config.BridgeIP, "/")[0]
		allocator, err := NewIPAllocatorForNode(config.Subnet, gatewayStr, nodeIndex, nodeSlots)
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

// Init initializes the network infrastructure (bridge, VXLAN).
func (nm *NetworkManager) Init(ctx context.Context) error {
	// Validate bridge name to prevent IFNAMSIZ overflow and injection
	if err := validateBridgeName(nm.config.BridgeName); err != nil {
		return fmt.Errorf("invalid bridge name: %w", err)
	}

	// Ensure bridge exists (this also sets up VXLAN if enabled)
	if err := nm.ensureBridge(ctx); err != nil {
		return fmt.Errorf("failed to setup bridge: %w", err)
	}

	// Setup bridge IP if configured
	if nm.config.BridgeIP != "" {
		if err := nm.setupBridgeIP(ctx); err != nil {
			log.Warn().Err(err).Msg("Failed to setup bridge IP")
		}
	}

	// VXLAN is already set up in ensureBridge() if enabled
	// Just log status
	if nm.config.VXLANEnabled && nm.vxlanMgr != nil {
		log.Info().Str("bridge", nm.config.BridgeName).Msg("VXLAN overlay initialized")
	} else if nm.config.VXLANEnabled {
		log.Warn().Msg("VXLAN enabled but manager not initialized")
	}

	// Masquerade rules left by an earlier daemon run for networks that no longer
	// exist are removed here, so a node does not keep a stale rule that could
	// hijack a later network reusing the same subnet (#70).
	nm.sweepStaleManagedNAT()

	return nil
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

	// dnsmasq also serves DNS on the bridge gateway, which is where the
	// node-local service records live, so point a task that stays on the
	// default bridge at it. Tasks on a user-defined network are excluded: their
	// bridge has its own gateway and no resolver, so the image's own resolver
	// is a better answer than an address that answers nothing.
	if !nm.hasManagedAttachment(task.Networks) && nm.dnsmasqServing() {
		if gw := nm.bridgeGatewayIP(); gw != "" {
			if task.Annotations == nil {
				task.Annotations = make(map[string]string, 1)
			}
			task.Annotations[types.AnnotationResolver] = gw
		}
	}

	// User-defined networks are materialized locally (their own bridge + VXLAN),
	// so they bypass the CNI path entirely.
	if nm.hasManagedAttachment(task.Networks) {
		return nm.prepareManagedNetworks(ctx, task)
	}

	// Check if we should use CNI for network attachments.
	//
	// Use the CNI plugin only when an attachment sits on a network we do not
	// already terminate locally. When the SwarmKit-assigned address is inside
	// the flat L2 bridge subnet, the guest is reachable cluster-wide as-is, so
	// create a plain TAP on that bridge instead of invoking the CNI plugin.
	if len(task.Networks) > 0 && nm.cniClient != nil && !nm.attachmentsOnBridgeSubnet(task.Networks) {
		// Use CNI plugin for SwarmKit network attachments
		return nm.prepareNetworkWithCNI(ctx, task)
	}

	// If no networks attached (and CNI not used), create default TAP
	if len(task.Networks) == 0 {
		log.Info().Str("task_id", task.ID).Msg("No network attachments, creating default TAP device")

		// Create a synthetic network attachment for default bridge
		defaultNetwork := types.NetworkAttachment{
			Network: types.Network{
				ID: types.DefaultNetworkID,
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

// attachmentsOnBridgeSubnet reports whether every attachment that carries a
// SwarmKit-assigned address falls inside the configured bridge subnet. When it
// does, no overlay setup is needed: the guest boots directly on the shared L2
// bridge and is reachable cluster-wide.
// attachmentsOnBridgeSubnet reports whether every SwarmKit-assigned address is
// inside the flat L2 bridge subnet, i.e. the guest is already reachable
// cluster-wide and needs no CNI plumbing.
func (nm *NetworkManager) attachmentsOnBridgeSubnet(networks []types.NetworkAttachment) bool {
	if nm.config.Subnet == "" {
		return false
	}
	_, subnet, err := net.ParseCIDR(nm.config.Subnet)
	if err != nil || subnet == nil {
		return false
	}
	sawAddr := false
	for _, n := range networks {
		for _, addr := range n.Addresses {
			ipStr := strings.SplitN(addr, "/", 2)[0]
			ip := net.ParseIP(ipStr)
			if ip == nil || !subnet.Contains(ip) {
				return false
			}
			sawAddr = true
		}
	}
	return sawAddr
}

// prepareNetworkWithCNI uses CNI plugin for SwarmKit network attachments.
func (nm *NetworkManager) prepareNetworkWithCNI(ctx context.Context, task *types.Task) error {
	log.Info().
		Str("task_id", task.ID).
		Int("networks", len(task.Networks)).
		Msg("Using CNI for network preparation")

	for i, attachment := range task.Networks {
		// Get network name and IP from SwarmKit attachment
		networkName := attachment.Network.Spec.Name
		if networkName == "" {
			// An attachment can arrive without a name, and its ID can be absent or
			// shorter than the prefix taken here: slice only what is there. The value
			// labels the CNI config lookup, so it keeps the ID-prefix shape it has
			// always had, and a malformed attachment must not panic the daemon.
			networkName = attachment.Network.ID
			if len(networkName) > 8 {
				networkName = networkName[:8]
			}
		}

		// If no SwarmKit-assigned IP, fall back to DHCP/TAP allocation
		if len(attachment.Addresses) == 0 {
			log.Info().
				Str("task_id", task.ID).
				Str("network", networkName).
				Msg("No SwarmKit-assigned IP, falling back to TAP/DHCP allocation")

			tap, err := nm.createTapDevice(ctx, attachment, i, task.ID)
			if err != nil {
				return fmt.Errorf("failed to create TAP device for network %s: %w", networkName, err)
			}

			nm.mu.Lock()
			nm.tapDevices[task.ID+"-"+tap.Name] = tap
			nm.mu.Unlock()

			if tap.IP != "" {
				ipWithMask := fmt.Sprintf("%s/%s", tap.IP, ipMaskToCIDR(tap.Netmask))
				task.Networks[i].Addresses = []string{ipWithMask}
			}

			log.Info().
				Str("task_id", task.ID).
				Str("tap", tap.Name).
				Str("ip", tap.IP).
				Msg("TAP device created (CNI fallback)")
			continue
		}

		ipCIDR := attachment.Addresses[0]

		// Call CNI ADD
		result, err := nm.cniClient.AddNetwork(ctx, task.ID, nm.netnsPath(), ipCIDR, networkName)
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

// netnsPath returns the network namespace path used for CNI ADD/DEL.
// The path is configurable via config.NetnsPath and defaults to
// /tmp/firecracker-ns for backward compatibility.
func (nm *NetworkManager) netnsPath() string {
	if nm.config.NetnsPath != "" {
		return nm.config.NetnsPath
	}
	return "/tmp/firecracker-ns"
}

// CleanupNetwork cleans up network interfaces for a task.
func (nm *NetworkManager) CleanupNetwork(ctx context.Context, task *types.Task) error {
	if task == nil {
		return nil
	}

	log.Info().
		Str("task_id", task.ID).
		Msg("Cleaning up network interfaces")

	// Collect TAP devices to remove for this task
	var tapsToRemove []struct {
		key string
		tap *TapDevice
	}
	nm.mu.Lock()
	for key, tap := range nm.tapDevices {
		if strings.HasPrefix(key, task.ID+"-") {
			tapsToRemove = append(tapsToRemove, struct {
				key string
				tap *TapDevice
			}{key: key, tap: tap})
		}
	}
	nm.mu.Unlock()

	// Remove devices outside the global lock (exec may block)
	for _, entry := range tapsToRemove {
		if err := nm.removeTapDevice(entry.tap); err != nil {
			log.Error().Err(err).
				Str("tap", entry.tap.Name).
				Msg("Failed to remove TAP device")
		}

		// Release the address to the allocator that handed it out.
		if entry.tap.IP != "" {
			nm.releaseIP(entry.tap)
		}

		nm.mu.Lock()
		delete(nm.tapDevices, entry.key)
		nm.mu.Unlock()
	}

	// Tear down any user-defined network that no local task uses anymore, so
	// `network rm` does not leave orphaned bridges and VXLAN interfaces behind.
	nm.reconcileManagedNetworks()

	// Clean up CNI network attachments if any (prevents IPAM leaks)
	if nm.cniClient != nil && len(task.Networks) > 0 {
		netnsPath := nm.netnsPath()
		for _, netAttach := range task.Networks {
			if err := nm.cniClient.DelNetwork(ctx, task.ID, netnsPath, netAttach.Network.ID); err != nil {
				log.Warn().Err(err).
					Str("task_id", task.ID).
					Str("network_id", netAttach.Network.ID).
					Msg("Failed to clean up CNI network")
			}
		}
	}

	log.Info().
		Str("task_id", task.ID).
		Msg("Network cleanup completed")

	return nil
}

// releaseIP returns a task TAP's address to the allocator that handed it out:
// the user-defined network's allocator for a managed bridge, otherwise the
// default bridge allocator. Without this, addresses from a network's block are
// never reused and the block leaks as tasks churn.
func (nm *NetworkManager) releaseIP(tap *TapDevice) {
	nm.mu.RLock()
	var managedAlloc *IPAllocator
	for _, m := range nm.managedNetworks {
		if m.bridge == tap.Bridge {
			managedAlloc = m.allocator
			break
		}
	}
	nm.mu.RUnlock()

	if managedAlloc != nil {
		managedAlloc.Release(tap.IP)
		return
	}
	if nm.ipAllocator != nil {
		nm.ipAllocator.Release(tap.IP)
	}
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
// It uses a two-phase lock: first creates the bridge under write lock,
// then sets up VXLAN (I/O heavy) outside the lock to avoid blocking
// concurrent network operations.
func (nm *NetworkManager) ensureBridge(ctx context.Context) error {
	bridgeName := nm.config.BridgeName

	nm.mu.RLock()
	exists := nm.bridges[bridgeName]
	nm.mu.RUnlock()

	if exists {
		return nil
	}

	nm.mu.Lock()

	// Double-check after acquiring write lock
	if nm.bridges[bridgeName] {
		nm.mu.Unlock()
		return nil
	}

	// Check if bridge exists
	if err := execCommand("ip", "link", "show", bridgeName).Run(); err != nil {
		// Create bridge
		log.Info().Str("bridge", bridgeName).Msg("Creating bridge")

		cmd := execCommand("ip", "link", "add", bridgeName, "type", "bridge")
		if err := cmd.Run(); err != nil {
			nm.mu.Unlock()
			return fmt.Errorf("failed to create bridge: %w", err)
		}

		// Bring bridge up
		if err := execCommand("ip", "link", "set", bridgeName, "up").Run(); err != nil {
			nm.mu.Unlock()
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
	if err := execCommand("ip", "link", "set", bridgeName, "up").Run(); err != nil {
		log.Warn().Err(err).Msg("Failed to ensure bridge is up")
	}

	// Release lock before slow VXLAN/peer discovery setup to avoid blocking
	// concurrent network operations (e.g., Consul peer updates, task preparation).
	// The bridge is fully created at this point; only VXLAN overlay remains.
	nm.mu.Unlock()

	// Setup VXLAN overlay if configured (I/O heavy — outside critical section)
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

	// Mark bridge as ready after all initialization is complete
	nm.mu.Lock()
	nm.bridges[bridgeName] = true
	nm.mu.Unlock()

	return nil
}

// setupBridgeIP configures the IP address on the bridge.
func (nm *NetworkManager) setupBridgeIP(ctx context.Context) error {
	bridgeName := nm.config.BridgeName

	// Check if IP is already assigned
	if err := execCommand("ip", "addr", "show", bridgeName).Run(); err == nil {
		// IP might already be set, try to add it (will fail if exists)
		cmd := execCommand("ip", "addr", "add", nm.config.BridgeIP, "dev", bridgeName)
		if err := cmd.Run(); err != nil {
			// IP might already be assigned, that's ok
			log.Debug().Str("bridge", bridgeName).Msg("Bridge IP might already be set")
		}
	} else {
		cmd := execCommand("ip", "addr", "add", nm.config.BridgeIP, "dev", bridgeName)
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
	if err := execCommand("sysctl", "-w", "net.ipv4.ip_forward=1").Run(); err != nil {
		return fmt.Errorf("failed to enable IP forwarding: %w", err)
	}

	// Setup the iptables masquerade rule. It carries the physdev guard so it matches
	// only traffic the node routes: under br_netfilter bridged frames also traverse
	// POSTROUTING, and without the guard this rule masquerades guest-to-guest traffic
	// on any user-defined network that was auto-allocated this same subnet, which
	// destroys its return path (#70).
	subnet := nm.config.Subnet
	if err := nm.deleteNatRule(masqueradeSpec(subnet, "", "", false)); err == nil {
		log.Info().Str("subnet", subnet).Msg("Replaced an unguarded default NAT rule")
	}
	if err := nm.ensureRule(masqueradeSpec(subnet, "", "", true)); err != nil {
		return fmt.Errorf("failed to add NAT rule: %w", err)
	}
	log.Info().Str("subnet", subnet).Msg("NAT masquerade rule in place")

	// Allow forwarding from bridge
	cmd := execCommand("iptables", "-C", "FORWARD", "-i", nm.config.BridgeName, "-j", "ACCEPT")
	if err := cmd.Run(); err != nil {
		cmd = execCommand("iptables", "-A", "FORWARD", "-i", nm.config.BridgeName, "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add forward rule: %w", err)
		}
	}

	// Allow forwarding to bridge
	cmd = execCommand("iptables", "-C", "FORWARD", "-o", nm.config.BridgeName, "-j", "ACCEPT")
	if err := cmd.Run(); err != nil {
		cmd = execCommand("iptables", "-A", "FORWARD", "-o", nm.config.BridgeName, "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add forward rule: %w", err)
		}
	}

	return nil
}

// setupDHCP configures a minimal DHCP server using dnsmasq.
func (nm *NetworkManager) setupDHCP(ctx context.Context) error {
	// Serialize the dnsmasq lifecycle: concurrent VM creations would otherwise
	// race to kill and restart the server, leaking orphaned instances.
	nm.dnsmasqMu.Lock()
	defer nm.dnsmasqMu.Unlock()

	if nm.config.Subnet == "" || nm.config.BridgeIP == "" {
		return fmt.Errorf("subnet and bridge IP must be configured for DHCP")
	}

	// Check if dnsmasq is available
	if _, err := execLookPath("dnsmasq"); err != nil {
		log.Warn().Msg("dnsmasq not found, DHCP will not be available")
		return nil //nolint:nilerr
		// Not fatal - VMs can use static IPs
	}

	// The per-bridge files (pid, log, leases, records) live in a directory that
	// only root and dnsmasq's own user can write, rather than in world-writable
	// /tmp: at predictable /tmp names a local user could plant a symlink the
	// daemon would write through as root, or a pid file it would signal as root.
	if err := nm.ensureDnsmasqRuntimeDir(); err != nil {
		return fmt.Errorf("dnsmasq runtime directory: %w", err)
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
	// Configurable range, defaults to .50-.200 of the subnet
	subnetIP := binary.BigEndian.Uint32(subnet.IP.To4())
	dhcpRangeStart := nm.config.DHCPRangeStart
	dhcpRangeEnd := nm.config.DHCPRangeEnd
	if dhcpRangeStart == 0 {
		dhcpRangeStart = 50
	}
	if dhcpRangeEnd == 0 {
		dhcpRangeEnd = 200
	}
	dhcpStart := subnetIP + uint32(dhcpRangeStart)
	dhcpEnd := subnetIP + uint32(dhcpRangeEnd)

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

	// Reuse a healthy server instead of bouncing it on every VM creation — but
	// only one that can serve service records. An instance started before this
	// feature (or by an older daemon) has no --addn-hosts and would serve no
	// records at all, so it is replaced rather than silently reused.
	if pid := nm.liveDnsmasqPID(nm.config.BridgeName); pid != "" && dnsmasqServesRecords([]string{pid}) {
		log.Debug().
			Str("bridge", nm.config.BridgeName).
			Str("pid", pid).
			Msg("DHCP server (dnsmasq) already running; reusing")
		return nil
	}

	// The pid file lives in a directory both the daemon and a standalone CLI run
	// share, so it is normally visible to whoever started the server. Keep the
	// /proc scan as the global cross-check anyway, and reuse that server too
	// instead of colliding on 192.168.127.1:53.
	if pids := findDnsmasqPIDs(nm.config.BridgeName); len(pids) > 0 && dnsmasqServesRecords(pids) {
		log.Debug().
			Str("bridge", nm.config.BridgeName).
			Strs("pids", pids).
			Msg("DHCP server (dnsmasq) already running (detected via /proc); reusing")
		return nil
	}

	// Kill any existing dnsmasq for this bridge: via the pid file, and via the
	// /proc scan for an instance no pid file tracks (one an earlier run left
	// behind). Both go through killDnsmasqPID, which signals nothing that is not
	// a dnsmasq bound to this bridge.
	nm.killDnsmasqByPID(nm.config.BridgeName)
	for _, pid := range findDnsmasqPIDs(nm.config.BridgeName) {
		log.Info().
			Str("bridge", nm.config.BridgeName).
			Str("pid", pid).
			Msg("Replacing dnsmasq instance that cannot serve service records")
		nm.killDnsmasqPID(pid, nm.config.BridgeName)
	}
	time.Sleep(100 * time.Millisecond) // Brief pause to ensure process is killed

	// Create the log file with permissions for dnsmasq, which opens it as root
	// at startup (before dropping to nobody) and keeps the descriptor. 0640
	// keeps it readable by the daemon but not world-writable.
	logFile := nm.dnsmasqLogPath()
	_ = osRemove(logFile) // Remove old file first
	if err := writeDnsmasqFile(logFile, []byte{}, 0640); err != nil {
		log.Warn().Err(err).Str("path", logFile).Msg("Could not create dnsmasq log file")
	}

	// dnsmasq only reads its addn-hosts file at startup and on SIGHUP, so the
	// file must exist before it starts; ProgramDNS rewrites it in place.
	recordsFile := nm.dnsRecordsPath()
	if err := writeDnsmasqFile(recordsFile, []byte{}, 0644); err != nil {
		log.Warn().Err(err).Str("path", recordsFile).Msg("Could not create dnsmasq records file")
	}

	// Start dnsmasq for this bridge
	// Arguments:
	// --interface: bind to bridge
	// --bind-interfaces: only bind to specified interface
	// --listen-address: also restrict DNS to the bridge gateway. Without it
	//   dnsmasq still binds loopback (127.0.0.1:53 / [::1]:53), which collides
	//   with the host resolver and with a second instance on another bridge
	//   ("failed to create listening socket ... Address already in use").
	// --except-interface=lo: never touch loopback.
	// --dhcp-range: define DHCP pool
	// --dhcp-option=3: set gateway
	// --dhcp-option=6: set DNS (use gateway as DNS proxy)
	// --addn-hosts: service-name records the reconciler rewrites and reloads
	//   with SIGHUP, so a guest can resolve service names through this bridge.
	args := []string{
		"--interface", nm.config.BridgeName,
		"--bind-interfaces",
		"--listen-address", gatewayIP.String(),
		"--except-interface=lo",
		"--dhcp-range", fmt.Sprintf("%s,%s,12h", startIP.String(), endIP.String()),
		"--dhcp-option", fmt.Sprintf("3,%s", gatewayIP.String()),
		"--dhcp-option", fmt.Sprintf("6,%s", gatewayIP.String()),
		"--addn-hosts", recordsFile,
		"--log-queries",
		"--log-dhcp",
		"--log-facility=" + logFile,
		"--pid-file=" + nm.dnsmasqPIDPath(),
		// dnsmasq's compiled-in default lease file is /var/lib/misc/dnsmasq.leases,
		// which is read-only under the hardened systemd unit (ProtectSystem=strict).
		// Point it at a writable path so the DHCP fallback can start.
		"--dhcp-leasefile=" + nm.dnsmasqLeasePath(),
	}

	// Name the user and group dnsmasq drops to explicitly, because its defaults are
	// compile-time settings and the runtime directory is handed to exactly this
	// group. Leaving them implicit is not safe: dnsmasq can end up in a group that
	// cannot even traverse the directory, and then it silently serves nothing. When
	// the user cannot be resolved, dnsmasq's own default stands and the directory
	// falls back too (see ensureDnsmasqRuntimeDir).
	if userName, groupName, _, _, err := dnsmasqDropUser(); err == nil {
		args = append(args, "--user="+userName, "--group="+groupName)
	}

	cmd := execCommand("dnsmasq", args...)

	if output, err := cmd.CombinedOutput(); err != nil {
		log.Error().Str("output", string(output)).Msg("dnsmasq failed to start")
		return fmt.Errorf("failed to start dnsmasq: %w (output: %s)", err, string(output))
	}

	log.Info().Msg("DHCP server (dnsmasq) started")
	return nil
}

// Shutdown cleans up all network resources including dnsmasq.
func (nm *NetworkManager) Shutdown() error {
	log.Info().Msg("Shutting down network manager")

	// Kill all dnsmasq instances started by swarmcracker
	if err := nm.cleanupDnsmasq(); err != nil {
		log.Warn().Err(err).Msg("Failed to cleanup dnsmasq")
	}

	// Stop VXLAN peer discovery if running
	nm.StopPeerDiscovery()

	// Remove iptables NAT and forwarding rules added by setupNAT
	if err := nm.teardownNAT(); err != nil {
		log.Warn().Err(err).Msg("Failed to teardown NAT rules")
	}

	log.Info().Msg("Network manager shutdown complete")
	return nil
}

// teardownNAT removes the iptables rules added by setupNAT (MASQUERADE and
// FORWARD rules) so they don't accumulate across restarts.
func (nm *NetworkManager) teardownNAT() error {
	if nm.config.Subnet == "" {
		return nil
	}

	rules := [][]string{
		append([]string{"-t", "nat", "-D"}, masqueradeSpec(nm.config.Subnet, "", "", true)...),
		append([]string{"-t", "nat", "-D"}, masqueradeSpec(nm.config.Subnet, "", "", false)...),
		{"-D", "FORWARD", "-i", nm.config.BridgeName, "-j", "ACCEPT"},
		{"-D", "FORWARD", "-o", nm.config.BridgeName, "-j", "ACCEPT"},
	}

	for _, args := range rules {
		cmd := execCommand("iptables", args...)
		if err := cmd.Run(); err != nil {
			// Rule may not exist — not an error worth surfacing.
			log.Debug().Err(err).Strs("rule", args).Msg("iptables rule not present during teardown")
		}
	}

	return nil
}

// dnatChains are the nat-table chains a published port is inserted into.
// PREROUTING covers traffic arriving from outside the host; OUTPUT covers
// traffic originating on the host itself (e.g. curl localhost:8080).
var dnatChains = []string{"PREROUTING", "OUTPUT"}

// sweepChains are all nat-table chains scanned when removing a task's rules,
// including POSTROUTING (which holds the local-traffic masquerade rule).
var sweepChains = []string{"PREROUTING", "OUTPUT", "POSTROUTING"}

// portComment is the iptables comment used to associate DNAT rules with a task
// so they can be swept on removal. The "swarmcracker:" prefix namespaces us.
func portComment(taskID string) string { return "swarmcracker:" + taskID }

func commentTaskID(comment string) string {
	return strings.TrimPrefix(comment, "swarmcracker:")
}

func hostPortKey(p types.PublishedPort) string {
	return p.Protocol + ":" + strconv.FormatUint(uint64(p.PublishedPort), 10)
}

// PublishPorts programs host-side DNAT so each host port reaches guestIP at the
// target port inside the microVM. All mappings are validated against existing
// rules first so a collision never leaves partial state, and any rule added
// before a later failure is rolled back.
func (nm *NetworkManager) PublishPorts(taskID, guestIP string, ports []types.PublishedPort) error {
	if len(ports) == 0 {
		return nil
	}
	if guestIP == "" {
		return fmt.Errorf("cannot publish ports for task %s: guest IP is unknown", taskID)
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()

	// Allow locally-originated loopback traffic to reach the guest, so
	// `curl 127.0.0.1:<port>` works as it does with Docker.
	nm.enableRouteLocalnet()

	// Validate collisions before mutating anything.
	for _, p := range ports {
		if owner, ok := nm.publishedPorts[hostPortKey(p)]; ok && owner != taskID {
			return fmt.Errorf("host port %d/%s is already published by task %s", p.PublishedPort, p.Protocol, owner)
		}
		if owner := nm.ruleOwner(p); owner != "" && owner != taskID {
			return fmt.Errorf("host port %d/%s is already forwarded by task %s", p.PublishedPort, p.Protocol, owner)
		}
	}

	added := make([]types.PublishedPort, 0, len(ports))
	for _, p := range ports {
		if err := nm.addDNATRule(taskID, guestIP, p); err != nil {
			nm.removeTaskRules(taskID)
			for _, a := range added {
				delete(nm.publishedPorts, hostPortKey(a))
			}
			return err
		}
		nm.publishedPorts[hostPortKey(p)] = taskID
		added = append(added, p)
	}

	log.Info().
		Str("task_id", taskID).
		Str("guest_ip", guestIP).
		Int("ports", len(ports)).
		Msg("Published host ports")
	return nil
}

// UnpublishPorts removes host-side forwarding for a task. It sweeps by the
// per-task iptables comment, so it also cleans up rules left behind by a
// previous daemon instance. It is safe to call when nothing was published.
func (nm *NetworkManager) UnpublishPorts(taskID string, ports []types.PublishedPort) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	nm.removeTaskRules(taskID)
	for _, p := range ports {
		delete(nm.publishedPorts, hostPortKey(p))
	}
	return nil
}

// addDNATRule adds the PREROUTING and OUTPUT DNAT rules for one mapping plus a
// POSTROUTING masquerade for locally-originated traffic, skipping rules that
// already exist.
func (nm *NetworkManager) addDNATRule(taskID, guestIP string, p types.PublishedPort) error {
	for _, chain := range dnatChains {
		if err := nm.ensureRule(nm.dnatRule(chain, taskID, guestIP, p)); err != nil {
			return fmt.Errorf("failed to add DNAT rule for %d/%s: %w", p.PublishedPort, p.Protocol, err)
		}
	}

	// Masquerade traffic that originates on the host itself (e.g.
	// `curl 127.0.0.1:<port>`). Without this the guest would try to reply to
	// 127.0.0.1 on its own loopback and the connection would never return.
	snatRule := []string{
		"POSTROUTING",
		"-s", "127.0.0.0/8",
		"-d", guestIP + "/32",
		"-m", "comment", "--comment", portComment(taskID),
		"-j", "MASQUERADE",
	}
	if err := nm.ensureRule(snatRule); err != nil {
		return fmt.Errorf("failed to add masquerade rule for %d/%s: %w", p.PublishedPort, p.Protocol, err)
	}
	return nil
}

// ensureRule adds a nat-table rule unless an identical one already exists.
func (nm *NetworkManager) ensureRule(rule []string) error {
	if err := execCommand("iptables", append([]string{"-t", "nat", "-C"}, rule...)...).Run(); err == nil {
		return nil
	}
	return execCommand("iptables", append([]string{"-t", "nat", "-A"}, rule...)...).Run()
}

// enableRouteLocalnet lets locally-generated traffic with a loopback source
// (e.g. `curl 127.0.0.1:<port>`) be routed out of the VM bridge, which the
// kernel rejects by default. Best-effort: a failure is logged, not fatal.
func (nm *NetworkManager) enableRouteLocalnet() {
	if nm.config.BridgeName == "" {
		return
	}
	key := "net.ipv4.conf." + nm.config.BridgeName + ".route_localnet=1"
	if err := execCommand("sysctl", "-w", key).Run(); err != nil {
		log.Warn().Err(err).Str("bridge", nm.config.BridgeName).Msg("Failed to enable route_localnet for published ports")
	}
}

// dnatRule returns the iptables rule body (chain and everything after it) for
// one mapping, including the task comment used for cleanup.
func (nm *NetworkManager) dnatRule(chain, taskID, guestIP string, p types.PublishedPort) []string {
	return []string{
		chain,
		"-p", p.Protocol,
		"--dport", strconv.FormatUint(uint64(p.PublishedPort), 10),
		"-m", "comment", "--comment", portComment(taskID),
		"-j", "DNAT",
		"--to-destination", guestIP + ":" + strconv.FormatUint(uint64(p.TargetPort), 10),
	}
}

// removeTaskRules deletes every nat-table rule tagged with the task's comment.
func (nm *NetworkManager) removeTaskRules(taskID string) {
	comment := portComment(taskID)
	for _, chain := range sweepChains {
		out, err := execCommand("iptables", "-t", "nat", "-S", chain).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 || fields[0] != "-A" || fields[1] != chain {
				continue
			}
			if !containsComment(fields, comment) {
				continue
			}
			// Rebuild the rule spec for -D, unquoting the comment value: -S
			// renders comments containing ":" in quotes, but iptables compares
			// the comment literally, so the quotes must be removed.
			rest := append([]string(nil), fields[2:]...)
			for i := 0; i < len(rest)-1; i++ {
				if rest[i] == "--comment" {
					rest[i+1] = unquote(rest[i+1])
				}
			}
			delArgs := append([]string{"-t", "nat", "-D", chain}, rest...)
			if err := execCommand("iptables", delArgs...).Run(); err != nil {
				log.Debug().Err(err).Str("task_id", taskID).Msg("Failed to delete port forwarding rule")
			}
		}
	}
}

// ruleOwner returns the task ID of any existing DNAT rule for the given host
// port, or "" when the port is free. It consults the kernel rather than only
// in-memory state, so ports leaked by a previous daemon run are detected too.
func (nm *NetworkManager) ruleOwner(p types.PublishedPort) string {
	out, err := execCommand("iptables", "-t", "nat", "-S", "PREROUTING").Output()
	if err != nil {
		return ""
	}
	port := strconv.FormatUint(uint64(p.PublishedPort), 10)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "-A" {
			continue
		}
		if !containsComment(fields, "") || !hasFlagValue(fields, "--dport", port) || !hasFlagValue(fields, "-p", p.Protocol) || !hasFlagValue(fields, "-j", "DNAT") {
			continue
		}
		if comment := flagValue(fields, "--comment"); comment != "" {
			return commentTaskID(comment)
		}
	}
	return ""
}

// containsComment reports whether a rule contains a --comment flag. When want
// is non-empty, the comment value must match it exactly. iptables -S renders
// comments that contain special characters (such as ":") in quotes, so values
// are unquoted before comparison.
func containsComment(fields []string, want string) bool {
	for i, f := range fields {
		if f == "--comment" && i+1 < len(fields) {
			if want == "" || unquote(fields[i+1]) == want {
				return true
			}
		}
	}
	return false
}

func flagValue(fields []string, flag string) string {
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			return unquote(fields[i+1])
		}
	}
	return ""
}

// unquote removes a single pair of surrounding double quotes, if present.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func hasFlagValue(fields []string, flag, value string) bool {
	return flagValue(fields, flag) == value
}

// cleanupDnsmasq kills the dnsmasq instances this host tracks through pid files,
// and reaps those files, so orphans from earlier runs or a mid-startup
// replacement are removed too. Every signal goes through killDnsmasqPID: a pid
// file is untrusted input, so nothing is signalled that is not a dnsmasq bound to
// that bridge.
func (nm *NetworkManager) cleanupDnsmasq() error {
	for _, pattern := range []string{
		filepath.Join(nm.dnsmasqRuntimeDir(), "dnsmasq-*.pid"),
		// Instances started before these files moved out of world-writable /tmp.
		"/tmp/dnsmasq-*.pid",
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, pidFile := range matches {
			// A pid file is untrusted input: the legacy /tmp sweep runs in a
			// world-writable directory, so only a file this host owns is read at
			// all.
			if !trustedPidFile(pidFile) {
				log.Warn().Str("file", pidFile).Msg("Ignoring a pid file this host does not own")
				continue
			}
			if data, readErr := os.ReadFile(pidFile); readErr == nil {
				pid := strings.TrimSpace(string(data))
				if pid != "" {
					nm.killDnsmasqPID(pid, dnsmasqBridgeFromPIDFile(pidFile))
				}
			}
			_ = osRemove(pidFile)
		}
	}

	// Note: do NOT sweep /proc and kill every dnsmasq bound to our bridges.
	// A client that reuses the daemon's server would otherwise tear it down on
	// exit. Only instances tracked through pid files are removed here.

	log.Info().Msg("dnsmasq cleaned up")
	return nil
}

// dnsmasqBridgeFromPIDFile extracts the bridge name from a
// dnsmasq-<bridge>.pid path.
func dnsmasqBridgeFromPIDFile(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".pid")
	return strings.TrimPrefix(name, "dnsmasq-")
}

// validPID reports whether s is a plain, non-zero PID. A pid file is untrusted
// input: a value like "-1" would otherwise turn `kill <pid>` into a signal for
// every process this user may signal, and as root that is the whole system.
func validPID(s string) bool {
	if s == "" || len(s) > 7 || s == "0" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// killDnsmasqPID signals pid only when it is a dnsmasq bound to bridgeName.
// PIDs reach this function from pid files, which are untrusted input: without the
// check the daemon would signal an arbitrary process as root.
func (nm *NetworkManager) killDnsmasqPID(pid, bridgeName string) bool {
	if !validPID(pid) {
		log.Warn().Str("pid", pid).Msg("Refusing to signal a PID that is not a plain process ID")
		return false
	}
	if !dnsmasqArgvMatches(dnsmasqArgvForPID(pid), bridgeName) {
		log.Warn().
			Str("pid", pid).
			Str("bridge", bridgeName).
			Msg("Refusing to signal a PID that is not this bridge's dnsmasq")
		return false
	}
	nm.killByPID(pid)
	return true
}

// liveDnsmasqPID returns the PID of a running dnsmasq bound to bridgeName, or
// "" when none is tracked and alive.
func (nm *NetworkManager) liveDnsmasqPID(bridgeName string) string {
	data, err := os.ReadFile(nm.dnsmasqFileFor(bridgeName, "pid"))
	if err != nil {
		return ""
	}
	pid := strings.TrimSpace(string(data))
	// The pid file is untrusted input: a planted PID must not make the daemon
	// believe a server it did not start is healthy, or it would skip starting its
	// own. Only a live dnsmasq bound to this bridge counts.
	if !validPID(pid) || !dnsmasqArgvMatches(dnsmasqArgvForPID(pid), bridgeName) {
		return ""
	}
	// Signal 0 probes for liveness without affecting the process.
	if err := execCommand("kill", "-0", pid).Run(); err != nil {
		return ""
	}
	return pid
}

// findDnsmasqPIDs returns the PIDs of running dnsmasq processes bound to the
// given bridge, found by scanning /proc.
// findDnsmasqPIDs returns the PIDs of running dnsmasq processes bound to the
// given bridge, found by scanning /proc. It is a variable so a test can decide
// whether a resolver is "running" without a real dnsmasq process.
var findDnsmasqPIDs = func(bridgeName string) []string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if dnsmasqArgvMatches(dnsmasqArgvForPID(entry.Name()), bridgeName) {
			pids = append(pids, entry.Name())
		}
	}
	return pids
}

// dnsmasqArgvMatches reports whether argv is a dnsmasq invocation bound to
// bridgeName via --interface. The spelling is strict (a binary whose name
// contains "dnsmasq", then `--interface` and the bridge as two arguments): a
// dnsmasq started another way is invisible to both the reuse check and the kill
// guard, which is the safe direction — it is never signalled — but it is also not
// reused, and a second instance then fails to bind :53. SwarmCracker always starts
// dnsmasq with this spelling.
func dnsmasqArgvMatches(argv []string, bridgeName string) bool {
	if len(argv) == 0 || !strings.Contains(filepath.Base(argv[0]), "dnsmasq") {
		return false
	}
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "--interface" && argv[i+1] == bridgeName {
			return true
		}
	}
	return false
}

// killDnsmasqByPID reads the per-bridge dnsmasq PID file and sends SIGTERM. A pid
// file is untrusted input, so the signal is validated first (see killDnsmasqPID).
func (nm *NetworkManager) killDnsmasqByPID(bridgeName string) {
	pidFile := nm.dnsmasqFileFor(bridgeName, "pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid := strings.TrimSpace(string(data))
	if pid == "" {
		return
	}
	nm.killDnsmasqPID(pid, bridgeName)
	_ = osRemove(pidFile)
}

// killByPID sends SIGTERM to a process by its PID string.
func (nm *NetworkManager) killByPID(pid string) {
	cmd := exec.CommandContext(context.Background(), "kill", pid)
	if err := cmd.Run(); err != nil {
		log.Warn().Str("pid", pid).Err(err).Msg("Failed to kill process by PID")
	}
}

// validateBridgeName validates that a bridge name is safe and within IFNAMSIZ limits.
func validateBridgeName(name string) error {
	if name == "" {
		return fmt.Errorf("bridge name cannot be empty")
	}
	if len(name) > 15 {
		return fmt.Errorf("bridge name %q exceeds 15 character limit (IFNAMSIZ)", name)
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return fmt.Errorf("bridge name %q contains invalid character: %c", name, c)
	}
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
	allPeers := nm.allVXLANPeers()

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

	// Apply any pending peers that were queued before VXLAN init
	// Note: caller (ensureBridge) already holds nm.mu.Lock(), so we don't lock here
	pending := nm.pendingPeers
	nm.pendingPeers = nil

	if len(pending) > 0 {
		log.Info().Strs("peers", pending).Msg("Applying queued VXLAN peers")
		if err := nm.vxlanMgr.UpdatePeers(pending); err != nil {
			log.Warn().Err(err).Msg("Failed to apply queued VXLAN peers")
		}
	}

	log.Info().Msg("VXLAN overlay configured")
	return nil
}

// getPhysicalInterface discovers the physical interface used for VXLAN transport.
func (nm *NetworkManager) getPhysicalInterface() (string, string, error) {
	// Get default route interface
	out, err := execCommand("ip", "route", "show", "default").Output()
	if err != nil {
		return "", "", err
	}

	fields := strings.Fields(string(out))
	if len(fields) < 5 {
		return "", "", fmt.Errorf("unexpected route output")
	}

	physInterface := fields[4]

	// Get IP address of physical interface
	out, err = execCommand("ip", "addr", "show", physInterface).Output()
	if err != nil {
		return "", "", err
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "inet ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				// Parse CIDR properly instead of assuming /24 or /32.
				if ip, _, err := net.ParseCIDR(parts[1]); err == nil {
					return physInterface, ip.String(), nil
				}
				// Fallback: bare IP without prefix.
				if ip := net.ParseIP(parts[1]); ip != nil {
					return physInterface, ip.String(), nil
				}
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

// allVXLANPeers returns the union of discovered and statically configured VXLAN
// peer addresses (deduplicated, order-stable). These are the transport
// addresses of the other nodes and are shared by the default overlay and every
// user-defined network.
func (nm *NetworkManager) allVXLANPeers() []string {
	peers := nm.discoverPeerWorkers()
	peers = append(peers, nm.config.VXLANPeers...)

	seen := make(map[string]bool, len(peers))
	out := make([]string, 0, len(peers))
	for _, p := range peers {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// UpdatePeers updates the VXLAN peer list for the default overlay and every
// user-defined network materialized on this node, so all overlays keep the same
// set of reachable peers.
func (nm *NetworkManager) UpdatePeers(peers []string) error {
	if nm.vxlanMgr == nil {
		// Queue peers for later when VXLAN is initialized
		nm.mu.Lock()
		nm.pendingPeers = peers
		nm.mu.Unlock()
		log.Info().Strs("peers", peers).Msg("VXLAN peers queued (manager not initialized)")
		return nil
	}
	err := nm.vxlanMgr.UpdatePeers(peers)
	// Fan out regardless of the default overlay's result so user-defined
	// networks still learn about the peers.
	nm.updateManagedNetworkPeers(peers)
	return err
}

// updateManagedNetworkPeers fans the peer list out to every user-defined
// network overlay so their L2 domains reach the same peers as the default one.
func (nm *NetworkManager) updateManagedNetworkPeers(peers []string) {
	nm.mu.RLock()
	managers := make([]*VXLANManager, 0, len(nm.managedNetworks))
	for _, m := range nm.managedNetworks {
		if m.vxlanMgr != nil {
			managers = append(managers, m.vxlanMgr)
		}
	}
	nm.mu.RUnlock()
	for _, vxm := range managers {
		if err := vxm.UpdatePeers(peers); err != nil {
			log.Warn().Err(err).Str("bridge", vxm.BridgeName).Msg("Failed to update user-defined network VXLAN peers")
		}
	}
}

// UpdateVXLANPeers implements NetworkManager interface for VXLAN peer updates.
func (nm *NetworkManager) UpdateVXLANPeers(peers []string) error {
	return nm.UpdatePeers(peers)
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

	// Use a dedicated discovery port, NOT the VXLAN data port (4789).
	// The VXLAN device already owns a kernel UDP socket on 4789; binding a
	// userspace listener on the same port fails with EADDRINUSE, which
	// silently kills peer discovery.
	const discoveryPort = 47890
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
//
// The signature matches the NetworkKeySetter interface in pkg/swarmkit so
// the type assertion in SetNetworkBootstrapKeys succeeds (previously the
// interface{} parameter silently failed the assertion and keys were never
// delivered to the network manager).
func (nm *NetworkManager) SetEncryptionKeys(keys []*api.EncryptionKey) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if nm.vxlanMgr == nil {
		log.Debug().Msg("VXLAN manager not initialized, storing keys for later")
		return nil
	}

	// The keys are used for VXLAN encryption
	// Implementation depends on VXLAN encryption support
	// For now, we store them and log
	log.Info().Int("key_count", len(keys)).Msg("Encryption keys received for VXLAN overlay")

	// Future: pass keys to vxlanMgr for encryption
	// if encMgr, ok := nm.vxlanMgr.(EncryptedVXLANManager); ok {
	//     return encMgr.SetKeys(keys)
	// }

	return nil
}

// SetNodeDiscovery sets the node discovery provider for SwarmKit integration.
func (nm *NetworkManager) SetNodeDiscovery(discovery types.NodeDiscovery) {
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
	if err := execCommand("ip", "link", "delete", tapName).Run(); err != nil {
		log.Debug().Err(err).Msg("Pre-cleanup TAP delete (device may not exist)")
	}

	// Create TAP device
	if err := execCommand("ip", "tuntap", "add", tapName, "mode", "tap").Run(); err != nil {
		return nil, fmt.Errorf("failed to create TAP device: %w", err)
	}

	// Bring TAP up
	if err := execCommand("ip", "link", "set", tapName, "up").Run(); err != nil {
		// Cleanup on failure
		if cerr := execCommand("ip", "link", "delete", tapName).Run(); cerr != nil {
			log.Debug().Err(cerr).Msg("Cleanup TAP delete failed after bring-up error")
		}
		return nil, fmt.Errorf("failed to bring TAP up: %w", err)
	}

	// Add to bridge
	bridgeName := nm.attachmentBridgeName(&network.Network)

	// Ensure bridge exists (especially for overlay, it should already be there)
	if err := execCommand("ip", "link", "show", bridgeName).Run(); err != nil {
		// If it's our default bridge, we might be able to create it (logic in ensureBridge)
		// But for overlay, we expect it to exist.
		if network.Network.Spec.Driver == "overlay" {
			// Try to cleanup
			if cerr := execCommand("ip", "link", "delete", tapName).Run(); cerr != nil {
				log.Debug().Err(cerr).Msg("Cleanup TAP delete failed for overlay bridge")
			}
			return nil, fmt.Errorf("overlay bridge %s not found: %w", bridgeName, err)
		}
		// Fallback to default behavior (ensureBridge) if it's the default bridge
		if bridgeName == nm.config.BridgeName {
			// ensureBridge is called in PrepareNetwork, but that only checks nm.config.BridgeName
			// If we switched to a different bridgeName here, we might be in trouble if it doesn't exist.
			_ = bridgeName // no-op: just documenting the fallback case
		}
	}

	if err := execCommand("ip", "link", "set", tapName, "master", bridgeName).Run(); err != nil {
		// Cleanup on failure
		if cerr := execCommand("ip", "link", "delete", tapName).Run(); cerr != nil {
			log.Debug().Err(cerr).Msg("Cleanup TAP delete failed after bridge attach error")
		}
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
	if err := execCommand("ip", "link", "set", tap.Name, "down").Run(); err != nil {
		log.Debug().Err(err).Msg("Failed to set TAP down during removal")
	}

	// Delete TAP device
	if err := execCommand("ip", "link", "delete", tap.Name).Run(); err != nil {
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
	if mask == nil {
		return "24" // Invalid mask, fall back to default
	}
	if ones, bits := mask.Size(); ones > 0 && bits > 0 {
		return fmt.Sprintf("%d", ones)
	}
	return "24" // nil/zero mask, fall back to default
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
