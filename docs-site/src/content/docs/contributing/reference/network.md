---
title: "Network Package Reference"
description: "`pkg/network/` — NetworkManager, CNI, VXLAN, IPAM, and Discovery."
---

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
└── tap_executor.go      # TAP device creation
```

> Test utilities live in `test/testhelpers/` (they were moved out of the
> `pkg/network` package).

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
    nodeDiscovery types.NodeDiscovery
    cniClient     *CNIClient
    pendingPeers  []string
    dnsmasqMu     sync.Mutex
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
    DHCPRangeStart   int      // DHCP range start offset (default 50)
    DHCPRangeEnd     int      // DHCP range end offset (default 200)
    NetnsPath        string   // CNI netns path (default /tmp/firecracker-ns)
    VXLANID          int      // VXLAN VNI (default 100)
    VXLANTunnelIP    string   // Overlay IP for this node
}
```

### Constructor

```go
func NewNetworkManager(config types.NetworkConfig) types.NetworkManager
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

#### PrepareNetwork

```go
func (nm *NetworkManager) PrepareNetwork(ctx context.Context, task *types.Task) error
```

**Purpose:** Create the TAP device(s), allocate IPs, and attach the VM's
network(s) to the bridge. TAP creation/removal is handled by the unexported
helpers `createTapDevice(ctx, network, index, taskID)` and
`removeTapDevice(tap)`.

**Parameters:**
- `ctx` — Context for cancellation
- `task` — Task whose `Networks` are configured (annotations are added in place)

---

#### CleanupNetwork

```go
func (nm *NetworkManager) CleanupNetwork(ctx context.Context, task *types.Task) error
```

**Purpose:** Tear down the task's TAP devices and release its IP allocations.

---

#### GetTapIP

```go
func (nm *NetworkManager) GetTapIP(taskID string) (string, error)
```

**Purpose:** Return the allocated IP for a task's TAP device.

**IP allocation** for a task is deterministic (SHA-256 of the task ID mapped
into the subnet, linear probing for collisions); releasing an IP is done via
`(*IPAllocator).Release` (see below).

---

#### UpdateVXLANPeers

```go
func (nm *NetworkManager) UpdateVXLANPeers(peers []string) error
```

**Purpose:** Update VXLAN forwarding database with new peers.

**Implementation:**

```bash
# For each peer IP:
bridge fdb append dev swarm-br0-vxlan dst <peer-ip> vni 100 port 4789
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

#### Release

```go
func (a *IPAllocator) Release(ip string)
```

**Purpose:** Release an IP back to the pool.

---

## VXLANManager

**File:** `vxlan.go`

The `VXLANManager` handles VXLAN overlay network setup and peer management.

### Type Definition

```go
type VXLANManager struct {
    BridgeName string    // Bridge to attach (e.g., "swarm-br0")
    VXLANID    int       // VXLAN Network Identifier (e.g., 100)
    OverlayIP  string    // Overlay IP of this node
    vxlanPort  int       // UDP port (default 4789)
    peerStore  PeerStore // Source of peer IPs
    mu         sync.RWMutex
    // + private context/netlink executor fields
}
```

Peers are supplied by a `PeerStore` (`GetPeers`, `AddPeer`, `RemovePeer`).
`NewStaticPeerStore(initialPeers []string) *StaticPeerStore` is the map-backed
implementation used for manually configured peers.

### Constructor

```go
func NewVXLANManager(bridgeName string, vxlanID int, overlayIP string, peerStore PeerStore) *VXLANManager
```

**Parameters:**
- `bridgeName` — bridge the VXLAN device is attached to
- `vxlanID` — VXLAN Network Identifier
- `overlayIP` — this node's overlay IP
- `peerStore` — peer source (a nil store is replaced by an empty `StaticPeerStore`)

---

### Methods

#### SetupVXLAN

```go
func (v *VXLANManager) SetupVXLAN(physInterface, localIP string) error
```

**Purpose:** Create the VXLAN device, attach it to the bridge, add the overlay
IP, and program initial peer forwarding entries.

**Implementation:**

```bash
# Create VXLAN device
ip link add swarm-br0-vxlan type vxlan id 100 dstport 4789 local <local-ip>

# Attach to bridge
ip link set swarm-br0-vxlan master swarm-br0

# Bring up
ip link set swarm-br0-vxlan up
```

---

#### AddRouteToSubnet

```go
func (v *VXLANManager) AddRouteToSubnet(remoteSubnet, remoteOverlayIP string) error
```

**Purpose:** Route a remote subnet through a peer's overlay IP.

---

#### UpdatePeers

```go
func (v *VXLANManager) UpdatePeers(newPeers []string) error
```

**Purpose:** Reconcile the VXLAN forwarding database with the given peer set
(adds and removes FDB entries as needed).

---

#### GetPeers

```go
func (v *VXLANManager) GetPeers() []string
```

**Purpose:** Return the current peer list.

---

#### StartPeerDiscovery / StopPeerDiscovery

```go
func (v *VXLANManager) StartPeerDiscovery(ctx context.Context, localIP string, port int) error
func (v *VXLANManager) StopPeerDiscovery()
```

**Purpose:** Start/stop UDP announcement-based peer discovery.

---

## CNIClient

**File:** `cni.go`, `cni_client.go`

CNI integration for SwarmKit network attachments.

### Type Definition

```go
type CNIClient struct {
    config CNIConfig
}

type CNIConfig struct {
    BinDir      string // Path to CNI binaries (default: /opt/cni/bin)
    ConfDir     string // Path to CNI configs (default: /etc/cni/net.d)
    CacheDir    string // Path to CNI cache (default: /var/lib/cni)
    NetworkName string // Default network name (default: swarmcracker)
}
```

### Constructor

```go
func NewCNIClient(config CNIConfig) *CNIClient
```

Empty `CNIConfig` fields are filled with the defaults shown above.

---

### Methods

#### AddNetwork

```go
func (c *CNIClient) AddNetwork(ctx context.Context, containerID, netns, ipCIDR, networkName string) (*CNIResult, error)
```

**Purpose:** Invoke the CNI plugin's ADD operation for a container's netns.

**Returns:**

```go
type CNIResult struct {
    CNIVersion string
    Interfaces []CNIInterface
    IPs        []CNIIP
    Routes     []CNIRoute
}
```

---

#### DelNetwork

```go
func (c *CNIClient) DelNetwork(ctx context.Context, containerID, netns, networkName string) error
```

**Purpose:** Invoke the CNI plugin's DEL operation.

---

## Discovery

Consul-based peer discovery for VXLAN lives in the separate `pkg/discovery`
package (`consul.go`), not in `pkg/network`. `NetworkManager.SetNodeDiscovery`
accepts a `types.NodeDiscovery` provider, which the SwarmKit integration wires
to the Consul client so that peer updates are programmed into the VXLAN
forwarding database.

---

## Netlink Operations

**File:** `netlink.go`

Low-level link/address/route programming goes through a `NetlinkExecutor`
interface; `NewDefaultNetlinkExecutor()` returns the real
`vishvananda/netlink`-backed implementation. The interface exposes
`LinkByName`, `LinkAdd`, `LinkDel`, `LinkSetUp`, `LinkSetDown`,
`LinkSetMaster`, `LinkSetMTU`, `LinkSetNsFd`, `AddrAdd`, `AddrDel`, `AddrList`,
`RouteAdd`, `RouteDel`, `RouteList`, `BridgeVlanAdd`, `BridgeVlanDel`,
`NeighAdd`, `NeighDel`, and `NeighList`. Tests substitute a mock implementation.

### TAP helpers

**File:** `cni.go`

```go
func CreateTAPDevice(name, bridge string) (*TAPDevice, error)
func DeleteTAPDevice(name string) error
func SetupVXLANFDB(tapName string, peers []string) error
```

These standalone helpers (usable by the CNI plugin) create/delete a TAP device,
attach it to a bridge, and program VXLAN FDB entries through a `TAPExecutor`
(`NewDefaultTAPExecutor()`), which has injectable `*WithExecutor` variants for
testing.

---

## TAP Device

**File:** `manager.go`, `cni.go`

`pkg/network` has two TAP representations:

- `TapDevice` (`manager.go`) — the per-VM device tracked by the manager, with
  full network details.
- `TAPDevice` (`cni.go`) — the result of the standalone CNI/`CreateTAPDevice`
  helper.

```go
type TapDevice struct {
    Name    string // e.g., "tap-abc123"
    Bridge  string // e.g., "swarm-br0"
    IP      string // e.g., "192.168.127.42"
    Netmask string // e.g., "255.255.255.0"
    Gateway string // e.g., "192.168.127.1"
    Subnet  string // e.g., "192.168.127.0/24"
}

type TAPDevice struct {
    Name    string
    MAC     string
    Bridge  string
    IP      string
    Netmask string
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

`pkg/network` tests use `mocks_test.go`. A minimal stand-in for the
`types.NetworkManager` interface looks like:

```go
type MockNetworkManager struct {
    Prepared map[string]bool
}

func (m *MockNetworkManager) PrepareNetwork(ctx context.Context, task *types.Task) error {
    m.Prepared[task.ID] = true
    return nil
}

func (m *MockNetworkManager) CleanupNetwork(ctx context.Context, task *types.Task) error {
    delete(m.Prepared, task.ID)
    return nil
}

func (m *MockNetworkManager) GetTapIP(taskID string) (string, error) {
    return "192.168.127.42", nil
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
| User networking guide | [Networking Guide](/guides/networking/) |
| Architecture | [Architecture Overview](/architecture/) |