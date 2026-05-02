# CNI NetworkProvider Implementation Spec

> SwarmKit NetworkAllocator implementation using CNI plugins

---

## Overview

### Problem Statement

swarmd-firecracker cannot allocate network resources for worker nodes because it lacks a `NetworkProvider` implementation. SwarmKit falls back to `Inert{}` which returns `"network support is unavailable"` for all allocation requests.

### Solution

Implement a `NetworkProvider` that delegates network allocation to standard CNI plugins, following the Kubernetes networking model.

---

## Architecture

### SwarmKit Interface Requirements

```go
// Provider interface (from networkallocator.go)
type Provider interface {
    DriverValidator
    PredefinedNetworks() []PredefinedNetworkData
    SetDefaultVXLANUDPPort(uint32) error
    NewAllocator(*Config) (NetworkAllocator, error)
}

// NetworkAllocator interface
type NetworkAllocator interface {
    IsAllocated(n *api.Network) bool
    Allocate(n *api.Network) error
    Deallocate(n *api.Network) error
    
    IsServiceAllocated(s *api.Service, flags ...func(*ServiceAllocationOpts)) bool
    AllocateService(s *api.Service) error
    DeallocateService(s *api.Service) error
    
    IsTaskAllocated(t *api.Task) bool
    AllocateTask(t *api.Task) error
    DeallocateTask(t *api.Task) error
    
    AllocateAttachment(node *api.Node, na *api.NetworkAttachment) error
    DeallocateAttachment(node *api.Node, na *api.NetworkAttachment) error
    IsAttachmentAllocated(node *api.Node, na *api.NetworkAttachment) bool
}
```

### CNI Integration Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     SwarmKit Node                                │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐         ┌──────────────────────────────────┐   │
│  │  Executor   │         │     CNI NetworkProvider          │   │
│  │  (Firecracker)│       │  ┌────────────────────────────┐  │   │
│  └─────────────┘         │  │   CNINetworkAllocator      │  │   │
│                          │  │  ┌──────────────────────┐  │  │   │
│                          │  │  │ CNI Plugin Manager   │  │  │   │
│                          │  │  │  ┌────────────────┐  │  │  │   │
│                          │  │  │  │ bridge         │  │  │  │   │
│                          │  │  │  │ vxlan          │  │  │  │   │
│                          │  │  │  │ host-local IPAM│  │  │  │   │
│                          │  │  │  └────────────────┘  │  │  │   │
│                          │  │  └──────────────────────┘  │  │   │
│                          │  └────────────────────────────┘  │   │
│                          └──────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

---

## Package Structure

```
pkg/cni/
├── provider.go           # CNIProvider implementation
├── allocator.go          # CNINetworkAllocator implementation  
├── plugin_manager.go     # CNI plugin execution wrapper
├── ipam.go               # IPAM integration (host-local)
├── config.go             # CNI configuration generation
├── validation.go         # Driver validation
├── predefined.go         # Predefined network handling
├── types.go              # Internal types and constants
└── cni_test.go           # Unit tests
```

---

## Implementation Details

### 1. CNIProvider (provider.go)

```go
package cni

import (
    "github.com/moby/swarmkit/v2/manager/allocator/networkallocator"
    "github.com/moby/swarmkit/v2/api"
)

// CNIProvider implements networkallocator.Provider using CNI plugins
type CNIProvider struct {
    config       *CNIConfig
    pluginDir    string
    configDir    string
    vxlanPort    uint32
}

type CNIConfig struct {
    BridgeName    string
    Subnet        string
    VXLANPort     uint32
    IPAMType      string  // "host-local"
}

// NewCNIProvider creates a new CNI-based network provider
func NewCNIProvider(cfg *CNIConfig) (*CNIProvider, error) {
    // Validate CNI plugins are available
    // Create default bridge network config
    // Setup VXLAN overlay config
}

// Provider interface implementation
func (p *CNIProvider) NewAllocator(cfg *networkallocator.Config) (networkallocator.NetworkAllocator, error) {
    return NewCNINetworkAllocator(p, cfg)
}

func (p *CNIProvider) PredefinedNetworks() []networkallocator.PredefinedNetworkData {
    return []networkallocator.PredefinedNetworkData{
        {Name: "ingress", Driver: "bridge"},
        {Name: "docker_gwbridge", Driver: "bridge"},
    }
}

func (p *CNIProvider) SetDefaultVXLANUDPPort(port uint32) error {
    p.vxlanPort = port
    return nil
}
```

### 2. CNINetworkAllocator (allocator.go)

```go
// CNINetworkAllocator implements networkallocator.NetworkAllocator
type CNINetworkAllocator struct {
    provider      *CNIProvider
    pluginManager *PluginManager
    ipam          *IPAMManager
    allocated     map[string]*AllocatedNetwork
    mu            sync.RWMutex
}

type AllocatedNetwork struct {
    ID          string
    Name        string
    Subnet      *net.IPNet
    Gateway     net.IP
    VXLANID     uint32
    BridgeName  string
    Attachments map[string]*NodeAttachment
}

type NodeAttachment struct {
    NodeID      string
    NetworkID   string
    IPAddress   net.IP
    MACAddress  string
    VXLANVNI    uint32
}

// Network allocation
func (a *CNINetworkAllocator) Allocate(n *api.Network) error {
    // 1. Parse network spec for driver type (bridge/vxlan)
    // 2. Generate subnet if not specified
    // 3. Create CNI network configuration JSON
    // 4. Execute CNI ADD command
    // 5. Store allocated state
}

func (a *CNINetworkAllocator) Deallocate(n *api.Network) error {
    // 1. Execute CNI DEL command
    // 2. Release IPAM allocations
    // 3. Remove from allocated map
}

// Node attachment allocation
func (a *CNINetworkAllocator) AllocateAttachment(node *api.Node, na *api.NetworkAttachment) error {
    // 1. Get allocated network
    // 2. Allocate IP from IPAM
    // 3. Execute CNI ADD for the attachment
    // 4. Store attachment info
}

// Service allocation
func (a *CNINetworkAllocator) AllocateService(s *api.Service) error {
    // 1. Allocate VIP if needed
    // 2. Allocate published ports
    // 3. Update service spec
}

// Task allocation
func (a *CNINetworkAllocator) AllocateTask(t *api.Task) error {
    // 1. Get task's network attachments
    // 2. Allocate IPs for each network
    // 3. Set network attachment specs
}
```

### 3. Plugin Manager (plugin_manager.go)

```go
// PluginManager handles CNI plugin execution
type PluginManager struct {
    pluginDir string
    configDir string
}

// CNIExecResult holds results from CNI plugin execution
type CNIExecResult struct {
    Interfaces []CNIInterface
    IPs        []CNIIPConfig
    Routes     []CNIRoute
    DNS        CNIDNS
}

// Add executes CNI ADD command
func (m *PluginManager) Add(netName, netID, ifName string, args map[string]string) (*CNIExecResult, error) {
    // Build CNI_ARGS
    // Load network config from configDir
    // Execute plugin binary: plugin ADD <config> <args>
    // Parse JSON result
}

// Del executes CNI DEL command
func (m *PluginManager) Del(netName, netID, ifName string, args map[string]string) error {
    // Execute plugin DEL command
    // Release IPAM allocation
}

// Check executes CNI CHECK command (optional)
func (m *PluginManager) Check(netName, netID, ifName string) error {
    // Verify network attachment is still valid
}
```

### 4. IPAM Integration (ipam.go)

```go
// IPAMManager manages IP allocation using host-local IPAM
type IPAMManager struct {
    allocations map[string]*IPPool
    mu          sync.RWMutex
}

type IPPool struct {
    Subnet    *net.IPNet
    Gateway   net.IP
    UsedIPs   map[string]string  // IP -> allocation ID
    NextIP    net.IP
}

// AllocateIP reserves an IP from the pool
func (i *IPAMManager) AllocateIP(subnet *net.IPNet, containerID string) (net.IP, error) {
    // Use host-local IPAM via CNI or manage internally
}

// ReleaseIP frees an allocated IP
func (i *IPAMManager) ReleaseIP(ip net.IP, subnet *net.IPNet) error {
    // Release IP back to pool
}

// GenerateSubnet creates a new subnet for overlay network
func (i *IPAMManager) GenerateSubnet() (*net.IPNet, error) {
    // Use default pool: 10.0.0.0/8 with /24 subnets
    // Or configurable pool from networkallocator.Config
}
```

### 5. CNI Configuration Generation (config.go)

```go
// GenerateBridgeConfig creates CNI bridge network config
func GenerateBridgeConfig(name, subnet, gateway string) ([]byte, error) {
    return json.Marshal(map[string]interface{}{
        "cniVersion": "1.0.0",
        "name":       name,
        "type":       "bridge",
        "bridge":     "br-" + name,
        "ipam": map[string]interface{}{
            "type":   "host-local",
            "subnet": subnet,
            "gateway": gateway,
        },
        "isGateway": true,
        "ipMasq":    true,
    })
}

// GenerateVXLANConfig creates CNI VXLAN overlay config
func GenerateVXLANConfig(name, subnet, gateway string, vxlanID uint32, vxlanPort uint32) ([]byte, error) {
    return json.Marshal(map[string]interface{}{
        "cniVersion": "1.0.0",
        "name":       name,
        "type":       "vxlan",
        "vxlanID":    vxlanID,
        "vxlanPort":  vxlanPort,
        "ipam": map[string]interface{}{
            "type":   "host-local",
            "subnet": subnet,
            "gateway": gateway,
        },
    })
}
```

---

## Integration Points

### 1. swarmd-firecracker Integration

Modify `cmd/swarmd-firecracker/main.go`:

```go
func runAgent(ctx *cli.Context) error {
    // ... existing setup ...
    
    // Create CNI network provider
    cniProvider, err := cni.NewCNIProvider(&cni.CNIConfig{
        BridgeName:  "cni0",
        Subnet:      "10.0.0.0/8",
        VXLANPort:   4789,  // Default VXLAN port
        IPAMType:    "host-local",
    })
    if err != nil {
        return fmt.Errorf("failed to create CNI provider: %w", err)
    }
    
    nodeConfig := &node.Config{
        // ... existing config ...
        NetworkProvider: cniProvider,        // ← NEW
        NetworkConfig: &networkallocator.Config{
            DefaultAddrPool: []string{"10.0.0.0/8"},
            SubnetSize:      24,
            VXLANUDPPort:    4789,
        },
        // ... rest of config ...
    }
    
    // ... rest of agent setup ...
}
```

### 2. Executor Integration

The executor (`pkg/swarmkit/executor.go`) already has a `networkMgr` for local networking. This remains unchanged — CNI handles cluster-level allocation, executor handles local VM networking.

```
┌──────────────────────────────────────────────────────────────────┐
│                      Allocation Flow                              │
├──────────────────────────────────────────────────────────────────┤
│                                                                   │
│  SwarmKit Manager                                                 │
│       │                                                           │
│       │ allocateNode(node)                                        │
│       ▼                                                           │
│  CNINetworkAllocator.AllocateAttachment()                        │
│       │                                                           │
│       │ Allocate IP from IPAM                                     │
│       │ Store attachment in node.Attachments                      │
│       ▼                                                           │
│  Raft Store (node object updated)                                │
│       │                                                           │       │ Dispatch task to worker                                          │
│       ▼                                                           │
│  SwarmKit Agent on Worker                                         │
│       │                                                           │
│       │ Controller.Create()                                       │
│       ▼                                                           │
│  Executor.PrepareNetwork(task.Networks)                          │
│       │                                                           │
│       │ Create TAP device                                         │
│       │ Attach to bridge/VXLAN                                    │
│       │ Configure VM network                                      │
│       ▼                                                           │
│  Firecracker VM running                                           │
│                                                                   │
└──────────────────────────────────────────────────────────────────┘
```

---

## CNI Plugins Required

| Plugin | Purpose | Source |
|--------|---------|--------|
| `bridge` | Local bridge network | containernetworking/plugins |
| `vxlan` | Overlay network | containernetworking/plugins |
| `host-local` | IPAM | containernetworking/plugins |
| `loopback` | Loopback interface | containernetworking/plugins |

### Installation

```bash
# Install CNI plugins
curl -LO https://github.com/containernetworking/plugins/releases/download/v1.5.0/cni-plugins-linux-amd64-v1.5.0.tgz
mkdir -p /opt/cni/bin
tar -xzf cni-plugins-linux-amd64-v1.5.0.tgz -C /opt/cni/bin

# Create CNI config directory
mkdir -p /etc/cni/net.d
```

---

## Testing Strategy

### Unit Tests

```go
// Test CNIProvider interface compliance
func TestCNIProviderInterface(t *testing.T) {
    var _ networkallocator.Provider = (*CNIProvider)(nil)
}

// Test network allocation
func TestAllocateNetwork(t *testing.T) {
    allocator := NewTestCNINetworkAllocator()
    network := &api.Network{
        ID: "test-net",
        Spec: api.NetworkSpec{
            DriverConfig: &api.Driver{Name: "bridge"},
        },
    }
    err := allocator.Allocate(network)
    assert.NoError(t, err)
    assert.True(t, allocator.IsAllocated(network))
}

// Test IPAM allocation
func TestIPAMAllocation(t *testing.T) {
    ipam := NewIPAMManager()
    subnet := mustParseCIDR("10.0.1.0/24")
    ip, err := ipam.AllocateIP(subnet, "test-container")
    assert.NoError(t, err)
    assert.NotNil(t, ip)
}
```

### Integration Tests

```go
// Test node attachment allocation
func TestAllocateAttachment(t *testing.T) {
    // Requires real CNI plugins installed
    allocator := NewCNINetworkAllocator(testProvider())
    
    node := &api.Node{ID: "test-node"}
    na := &api.NetworkAttachment{
        Network: &api.Network{ID: "test-net"},
    }
    
    err := allocator.AllocateAttachment(node, na)
    assert.NoError(t, err)
    assert.True(t, allocator.IsAttachmentAllocated(node, na))
}
```

### E2E Test

```bash
# Test with real cluster
ansible-playbook -i inventory/libvirt/hosts site.yml

# Verify worker nodes join successfully
ssh swarm-manager "swarmctl node ls"

# Should show all nodes with status "READY"
```

---

## Migration Path

### Phase 1: Core Implementation (Week 1-2)

1. Implement `CNIProvider` and `CNINetworkAllocator`
2. Implement `PluginManager` for CNI plugin execution
3. Implement `IPAMManager` for IP allocation
4. Unit tests for core functionality

### Phase 2: Integration (Week 3)

1. Integrate with `swarmd-firecracker`
2. Update Ansible playbooks for CNI plugin installation
3. Integration tests with mock CNI plugins

### Phase 3: VXLAN Overlay (Week 4)

1. Implement VXLAN network support
2. Configure VXLAN encryption keys (from `SetNetworkBootstrapKeys`)
3. Test cross-node communication

### Phase 4: Service VIP & Routing Mesh (Week 5-6)

1. Implement `AllocateService` for VIP allocation
2. Implement port publishing
3. Ingress network support

---

## Risks & Mitigation

| Risk | Mitigation |
|------|------------|
| CNI plugins not installed | Ansible role to install plugins |
| IP exhaustion | Implement IP pool management with garbage collection |
| VXLAN encryption keys | Use SwarmKit's encryption key infrastructure |
| Plugin execution errors | Retry logic with backoff, error logging |
| Network config drift | Periodic reconciliation, CNI CHECK command |

---

## Success Criteria

1. ✅ Worker nodes join cluster without "network support unavailable" error
2. ✅ Nodes have valid IP attachments in `node.Attachments`
3. ✅ Tasks can be scheduled across multiple nodes
4. ✅ Cross-node communication works via VXLAN overlay
5. ✅ Service VIP allocation works
6. ✅ Published ports accessible via ingress network

---

## References

- [CNI Spec](https://github.com/containernetworking/cni/blob/master/SPEC.md)
- [CNI Plugins](https://github.com/containernetworking/plugins)
- [SwarmKit NetworkAllocator](https://github.com/moby/swarmkit/blob/master/manager/allocator/networkallocator/networkallocator.go)
- [SwarmKit Inert Provider](https://github.com/moby/swarmkit/blob/master/manager/allocator/networkallocator/inert.go)

---

## Changelog

| Date | Author | Change |
|------|--------|--------|
| 2026-05-02 | hermione | Initial spec created |