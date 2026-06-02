# Network Package Reference

> `pkg/network/` — NetworkManager, CNI, VXLAN, IPAM, and Discovery.

---

## Overview

The `pkg/network` package manages networking for Firecracker VMs, including bridge creation, TAP device setup, VXLAN overlay networks, and IP allocation.

**Package Structure:**

```
pkg/network/
├── manager.go           # NetworkManager (orchestration)
├── vxlan.go             # VXLAN overlay (cross-node)
├── cni.go               # CNI plugin integration
├── cni_client.go        # CNI client wrapper
├── netlink.go           # Netlink operations
├── discovery.go         # Consul peer discovery
├── tap_executor.go      # TAP device creation
└── testhelpers/         # Test utilities
```

---

## NetworkManager

**File:** `manager.go`

The `NetworkManager` orchestrates all network operations for VMs.

### Type Definition

```go
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
    nodeDiscovery types.NodeDiscovery
    cniClient     *CNIClient
    pendingPeers  []string
}
```

### Configuration

```go
type NetworkConfig struct {
    BridgeName       string   // e.g., "swarm-br0"
    Subnet           string   // e.g., "192.168.127.0/24"
    BridgeIP         string   // e.g., "192.168.127.1/24"
    IPMode           string   // "static" or "dhcp"
    NATEnabled       bool     // Enable masquerading
    VXLANEnabled     bool     // Enable cross-node overlay
    VXLANPeers       []string // Initial peer IPs
    EnableRateLimit  bool     // Packet rate limiting
    MaxPacketsPerSec int      // Rate limit threshold
}
```

### Constructor

```go
func NewNetworkManager(config types.NetworkConfig) *NetworkManager
```

**Example:**

```go
config := types.NetworkConfig{
    BridgeName:   "swarm-br0",
    Subnet:       "192.168.127.0/24",
    BridgeIP:     "192.168.127.1/24",
    IPMode:       "static",
    NATEnabled:   true,
    VXLANEnabled: true,
}

nm := network.NewNetworkManager(config)
```

---

### Methods

#### Init

```go
func (nm *NetworkManager) Init(ctx context.Context) error
```

**Purpose:** Initialize network infrastructure.

**Steps:**
1. Create Linux bridge (`swarm-br0`)
2. Assign bridge IP address
3. Enable IP forwarding
4. Setup NAT/masquerading (if enabled)
5. Create VXLAN device (if enabled)

**Example:**

```go
if err := nm.Init(ctx); err != nil {
    log.Fatal(err)
}
```

---

#### CreateTapDevice

```go
func (nm *NetworkManager) CreateTapDevice(ctx context.Context, taskID string) (*TapDevice, error)
```

**Purpose:** Create TAP device for VM and connect to bridge.

**Parameters:**
- `ctx` — Context for cancellation
- `taskID` — Unique task identifier

**Returns:**
- `TapDevice` — TAP device info with allocated IP

**Example:**

```go
tap, err := nm.CreateTapDevice(ctx, "task-abc123")
if err != nil {
    return err
}

fmt.Printf("TAP: %s, IP: %s\n", tap.Name, tap.IP)
// Output: TAP: tap-abc123, IP: 192.168.127.42
```

---

#### RemoveTapDevice

```go
func (nm *NetworkManager) RemoveTapDevice(ctx context.Context, taskID string) error
```

**Purpose:** Remove TAP device and release IP.

---

#### AllocateIP

```go
func (nm *NetworkManager) AllocateIP(taskID string) (string, error)
```

**Purpose:** Allocate IP address for task.

**Algorithm:**
1. SHA-256 hash of task ID
2. Convert hash to IP offset in subnet
3. Linear probing for collision resolution
4. Skip gateway, network, and broadcast addresses

---

#### ReleaseIP

```go
func (nm *NetworkManager) ReleaseIP(ip string) error
```

**Purpose:** Release IP back to pool.

---

#### UpdateVXLANPeers

```go
func (nm *NetworkManager) UpdateVXLANPeers(peers []string) error
```

**Purpose:** Update VXLAN forwarding database with new peers.

**Implementation:**

```bash
# For each peer IP:
bridge fdb append dev vxlan100 dst <peer-ip> vni 100 port 4789
```

---

#### SetNodeDiscovery

```go
func (nm *NetworkManager) SetNodeDiscovery(discovery types.NodeDiscovery)
```

**Purpose:** Set discovery provider (Consul) for dynamic peer updates.

---

## IPAllocator

**File:** `manager.go`

### Type Definition

```go
type IPAllocator struct {
    subnet    *net.IPNet
    gateway   net.IP
    allocated map[string]string // IP → VM ID mapping
    mu        sync.Mutex
}
```

### Constructor

```go
func NewIPAllocator(subnetStr, gatewayStr string) (*IPAllocator, error)
```

---

### Methods

#### Allocate

```go
func (a *IPAllocator) Allocate(vmID string) (string, error)
```

**Purpose:** Allocate deterministic IP for VM.

**Algorithm:**

```go
func (a *IPAllocator) hashToIP(vmID string) net.IP {
    h := sha256.New()
    h.Write([]byte(vmID))
    hash := h.Sum(nil)
    
    // IPv4: use first 4 bytes of hash
    n := binary.BigEndian.Uint32(hash[:4]) % (size - 2)
    ipInt := subnetBase + n + 1
    
    return net.IP(ipInt)
}
```

**Collision Resolution:**

```go
// Linear probing - try next IP if collision
for i := 0; i < 256; i++ {
    if !isGateway && !isAllocated {
        allocated[ipStr] = vmID
        return ipStr, nil
    }
    ip = incIP(ip)
}
```

---

## VXLANManager

**File:** `vxlan.go`

The `VXLANManager` handles VXLAN overlay network setup and peer management.

### Type Definition

```go
type VXLANManager struct {
    vxlanDevice string    // e.g., "vxlan100"
    vni         int       // VXLAN Network Identifier (100)
    bridge      string    // Bridge to attach (swarm-br0)
    port        int       // UDP port (4789)
    localIP     string    // Local underlay IP
    peers       []string  // Remote peer IPs
    mu          sync.RWMutex
}
```

### Constructor

```go
func NewVXLANManager(config VXLANConfig) (*VXLANManager, error)
```

---

### Methods

#### Create

```go
func (vx *VXLANManager) Create(ctx context.Context) error
```

**Purpose:** Create VXLAN device and attach to bridge.

**Implementation:**

```bash
# Create VXLAN device
ip link add vxlan100 type vxlan id 100 dstport 4789 local <local-ip>

# Attach to bridge
ip link set vxlan100 master swarm-br0

# Bring up
ip link set vxlan100 up
```

---

#### AddPeer

```go
func (vx *VXLANManager) AddPeer(peerIP string) error
```

**Purpose:** Add remote peer to forwarding database.

**Implementation:**

```bash
bridge fdb append dev vxlan100 dst <peer-ip> vni 100 port 4789
```

**Note:** Uses `fdb append` to allow multiple peers per VNI.

---

#### RemovePeer

```go
func (vx *VXLANManager) RemovePeer(peerIP string) error
```

**Purpose:** Remove peer from forwarding database.

---

#### UpdatePeers

```go
func (vx *VXLANManager) UpdatePeers(peers []string) error
```

**Purpose:** Batch update all peers.

---

## CNIClient

**File:** `cni.go`, `cni_client.go`

CNI integration for SwarmKit network attachments.

### Type Definition

```go
type CNIClient struct {
    pluginDir string
    configDir string
    cacheDir  string
}

type CNIConfig struct {
    Name       string
    Type       string
    Bridge     string
    IPAM       IPAMConfig
    IsDefaultGateway bool
}
```

### Constructor

```go
func NewCNIClient(pluginDir, configDir string) *CNIClient
```

---

### Methods

#### Add

```go
func (c *CNIClient) Add(ctx context.Context, netName, ifaceName, containerID string, config *CNIConfig) (*CNIResult, error)
```

**Purpose:** Add network interface for container.

**Returns:**

```go
type CNIResult struct {
    Interfaces []CNIIface
    IPs        []CNIIP
    Routes     []CNIRoute
}
```

---

#### Del

```go
func (c *CNIClient) Del(ctx context.Context, netName, ifaceName, containerID string) error
```

**Purpose:** Remove network interface.

---

## Discovery

**File:** `discovery.go`

Consul-based peer discovery for VXLAN.

### Functions

```go
func getLocalIPFromInterface() string
```

**Purpose:** Auto-detect local IP for Consul registration.

**Algorithm:**
1. Get all network interfaces
2. Filter up, non-loopback interfaces
3. Prefer interfaces with gateway route
4. Return first valid IP

---

## Netlink Operations

**File:** `netlink.go`

Low-level network operations using netlink.

### Functions

#### CreateBridge

```go
func CreateBridge(name string) error
```

**Implementation:**

```bash
ip link add <name> type bridge
ip link set <name> up
```

---

#### SetBridgeIP

```go
func SetBridgeIP(name, ipCIDR string) error
```

**Implementation:**

```bash
ip addr add <ipCIDR> dev <name>
```

---

#### CreateTap

```go
func CreateTap(name string) error
```

**Implementation:**

```bash
ip tuntap add dev <name> mode tap
ip link set <name> up
```

---

#### ConnectTapToBridge

```go
func ConnectTapToBridge(tapName, bridgeName string) error
```

**Implementation:**

```bash
ip link set <tapName> master <bridgeName>
```

---

## TAP Device

**File:** `tap_executor.go`

### Type Definition

```go
type TapDevice struct {
    Name    string  // e.g., "tap-abc123"
    Bridge  string  // e.g., "swarm-br0"
    IP      string  // e.g., "192.168.127.42"
    Netmask string  // e.g., "255.255.255.0"
    Gateway string  // e.g., "192.168.127.1"
    Subnet  string  // e.g., "192.168.127.0/24"
}
```

---

## NAT/Masquerading

**Enabled by default** for VM internet access.

**Implementation:**

```bash
# Enable IP forwarding
sysctl -w net.ipv4.ip_forward=1

# Setup masquerading
iptables -t nat -A POSTROUTING -s 192.168.127.0/24 ! -o swarm-br0 -j MASQUERADE
```

**Configuration:**

```yaml
network:
  nat_enabled: true
```

---

## Rate Limiting

Optional packet rate limiting on TAP devices.

**Configuration:**

```yaml
network:
  enable_rate_limit: true
  max_packets_per_sec: 10000
```

**Implementation:**

```bash
# Using tc (traffic control)
tc qdisc add dev <tap> root handle 1: htb
tc class add dev <tap> parent 1: classid 1:1 htb rate <rate>
tc filter add dev <tap> parent 1: protocol ip prio 1 u32 match u32 0 0 flowid 1:1
```

---

## Testing

### Mock NetworkManager

```go
type MockNetworkManager struct {
    TapDevices map[string]*TapDevice
    IPs        map[string]string
}

func (m *MockNetworkManager) CreateTapDevice(ctx context.Context, taskID string) (*TapDevice, error) {
    tap := &TapDevice{
        Name:    "tap-" + taskID,
        IP:      "192.168.127.42",
        Gateway: "192.168.127.1",
    }
    m.TapDevices[taskID] = tap
    return tap, nil
}
```

---

## Error Handling

### Common Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `"invalid subnet"` | Bad CIDR notation | Use valid format (e.g., `192.168.127.0/24`) |
| `"bridge creation failed"` | Permission denied | Run with root/capabilities |
| `"failed to allocate IP"` | Subnet exhausted | Increase subnet size or cleanup VMs |
| `"vxlan device creation failed"` | VXLAN module missing | Load vxlan kernel module |

---

## Related Documentation

| Topic | Document |
|-------|----------|
| SwarmKit executor | [SwarmKit Reference](swarmkit.md) |
| User networking guide | [Networking Guide](../../user/guides/networking.md) |
| Architecture | [Architecture Overview](../../architecture/overview.md) |