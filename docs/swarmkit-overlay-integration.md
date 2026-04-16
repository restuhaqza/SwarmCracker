# SwarmKit Overlay Network Integration

## Problem

SwarmCracker currently implements custom VXLAN overlay, ignoring SwarmKit's built-in overlay networking.

**Current behavior:**
- Allocates IPs locally via hash(taskID)
- Creates custom VXLAN tunnels
- Requires manual peer configuration

**SwarmKit provides:**
- Centralized IPAM (IP allocation coordinated by manager)
- Automatic VXLAN tunnel setup
- Service discovery via DNS
- Automatic peer discovery via control plane

## Solution

Use SwarmKit overlay networks by default:

### 1. Respect SwarmKit-Allocated IPs

When `NetworkAttachment.Addresses` has IPs, use them instead of allocating locally:

```go
// In createTapDevice
if len(network.Addresses) > 0 {
    // Use SwarmKit-provided IP (from overlay network IPAM)
    ipAddr = parseIPFromAddress(network.Addresses[0])
} else if nm.ipAllocator != nil {
    // Local allocation only when no SwarmKit network
    ipAddr, err = nm.ipAllocator.Allocate(taskID)
}
```

### 2. Use SwarmKit Overlay Bridge

SwarmKit creates bridge `br-<network-id[:12]>` for overlay networks.
Use this bridge instead of custom VXLAN:

```go
if network.Network.Spec.Driver == "overlay" {
    bridgeName = "br-" + network.Network.ID[:12]
}
```

### 3. Remove Custom VXLAN for Overlay

When using SwarmKit overlay, skip custom VXLAN setup:
- SwarmKit handles VXLAN tunnel creation
- SwarmKit handles FDB entries
- SwarmKit handles peer discovery

## Implementation Plan

1. **Update `createTapDevice`**:
   - Check `network.Addresses` first
   - Use SwarmKit-provided IP if present
   - Fall back to local allocation only for bridge networks

2. **Update `PrepareNetwork`**:
   - Skip VXLAN setup for overlay networks
   - Let SwarmKit's infrastructure handle overlay

3. **Remove VXLAN flag dependency**:
   - VXLAN setup only for custom bridge networks
   - Overlay networks use SwarmKit's built-in VXLAN

## Testing

1. Create SwarmKit overlay network via API
2. Deploy services attached to overlay
3. Verify cross-node communication works
4. Verify IP allocation is coordinated by manager
