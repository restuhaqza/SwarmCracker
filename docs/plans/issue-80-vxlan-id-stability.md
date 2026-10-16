# Plan — #80: VXLAN IDs come from the allocation index, so a restart can collide two networks

Branch: `fix/vxlan-id-stability`
Issue: [#80](https://github.com/restuhaqza/SwarmCracker/issues/80)

## Symptom

After a daemon restart, one user-defined network silently loses cross-node
connectivity while its siblings are fine. Two networks end up with the **same VNI**,
and the second one's VXLAN interface is never created on any node:

```
$ swarmcracker network ls
NAME    DRIVER  SUBNET        VNI
net-c   vxlan   10.0.5.0/24   4045883
net-a   vxlan   10.0.3.0/24   4045883     <- same VNI as net-c
net-b   vxlan   10.0.4.0/24   4045885

node1: swarm-br0-vxlan, br-net_c-vxlan, br-net_b-vxlan     (no br-net_a-vxlan)
node2: swarm-br0-vxlan, br-net_c-vxlan, br-net_b-vxlan     (no br-net_a-vxlan)
```

| Network | VNI | guest-to-guest ping across nodes |
|---------|-----|----------------------------------|
| net-b | 4045885 | 0% loss |
| net-c | 4045883 | 0% loss |
| net-a | 4045883 (collides) | **100% loss** |

Recreating the network gives it a fresh VNI and the traffic returns
(`vni=4045888 -> 0% loss`), which is what pinned the mechanism down.

## Root cause

```go
// pkg/cni/config.go:168
func GenerateVXLANID(networkName string, baseID uint32) uint32 {
	hash := uint32(0)
	for _, c := range networkName {
		hash = hash*31 + uint32(c)
	}
	vxlanID := (hash + baseID) % 16777215
	...
}
```

called as `GenerateVXLANID(name, p.networkIndex)` (`pkg/cni/provider.go:342`), where
`networkIndex` is the provider's allocation counter:

1. **It is recomputed on every re-allocation.** The manager re-allocates existing
   networks on a restart or an upgrade, each allocation consumes an index, and the
   recorded VNI (`DriverState["vxlan_vni"]`, read by the executor at
   `pkg/swarmkit/executor.go:1470`) is overwritten. A live network is renumbered, and
   if the store order differs between nodes they can disagree.
2. **Nothing checks for collisions.** `hash(a) + i == hash(b) + j` is easy to hit. Two
   networks with one VNI cannot both exist on a node — the second VXLAN interface is
   never created (`pkg/network/vxlan.go:157` treats the VNI as the interface's
   identity) — so that network has no cross-node L2.

This is the same class of defect #77 fixed for the subnet: a per-network value derived
from a mutable counter instead of being kept with the network.

## Fix

- **Keep the recorded VNI.** The allocator reads the network's
  `DriverState["vxlan_vni"]` and passes it with the recorded subnet; the provider uses
  it verbatim. A restart or an upgrade no longer renumbers a live network.
- **Never hand out a VNI that is taken.** When a network needs a new VNI, take the
  first candidate from `GenerateVXLANID(name, index+salt)` that no allocated network
  holds, bounded like the subnet carve. This covers the hash-collision case that a
  stable index alone does not.
- **One request struct instead of seven positional arguments** on
  `AllocateNetwork`, so what a network already holds is explicit at the call site
  (`NetworkAllocationRequest{... ExistingSubnet, RecordedVXLAN ...}`).

## Out of scope

- #76 — distinct per-node bridge addresses for user-defined networks.

## Tasks

| # | Task | Files | Verify |
|---|------|-------|--------|
| T1 | Recon: how the VNI is derived, recorded and read | — | done |
| T2 | Keep the recorded VNI; avoid taken candidates; request struct | `pkg/cni/` | unit |
| T3 | Tests: recorded VNI kept on re-allocation, taken VNI skipped, distinct networks distinct VNIs | `pkg/cni/*_test.go` | unit |
| T4 | Docs: the VNI is assigned per network and preserved | `docs-site` | `npm run verify` |
| T5 | Independent whole-branch review | — | — |
| T6 | Lab e2e: restart leaves every VNI unchanged and every network reachable | — | see below |
| T7 | Merge once CI is green, then restore the lab | — | CI |

## Verification

- Unit: an existing allocation keeps its VNI across a re-allocation (the regression
  test re-allocates under a *fresh* provider, which is what a restart does); a
  candidate VNI already held by an allocated network is skipped and the next one is
  used; distinct networks get distinct VNIs; a network with a recorded VNI that would
  collide is still kept (stability wins over uniqueness for a live network, and the
  collision is reported).
- Lab e2e: create several user-defined networks, record their VNIs, restart both
  daemons, and check that every VNI is unchanged, no two networks share one, every
  network's VXLAN interface exists on both nodes, and guest-to-guest ping works on all
  of them.
