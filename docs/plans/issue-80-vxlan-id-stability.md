# Plan — #80: VXLAN IDs come from the allocation index, so a restart can collide two networks

Branch: `fix/vxlan-id-stability`
Issue: [#80](https://github.com/restuhaqza/SwarmCracker/issues/80)

## Symptom

After a daemon restart, one user-defined network silently loses cross-node
connectivity while its siblings are fine. Two networks end up with the **same VNI**,
and the second one's VXLAN interface is never created on any node (the numbers below
are the ones the issue recorded, from the pre-fix binary):

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
  `DriverState["vxlan_vni"]` and passes it with the recorded subnet, and the provider
  keeps it. A restart or an upgrade no longer renumbers a live network.
- **Repair an existing collision.** A recorded VNI that another allocated network
  already holds *is* the #80 incident, and keeping both would leave that network broken
  for good. It is renumbered once, with a warning naming both networks. The repair
  persists — the new value is written back to the network's driver state — so it cannot
  flip-flop on the next restart.
- **Never hand out a VNI that is taken.** A network that needs a new VNI takes the
  first free candidate from the name's hash, walking forward on a clash (bounded like
  the subnet carve). This covers the hash-collision case that a stable index alone does
  not.
- **One request struct instead of six positional arguments** on `AllocateNetwork`, so
  what a network already holds is explicit at the call site
  (`NetworkAllocationRequest{... ExistingSubnet, RecordedVXLAN ...}`).
- A recorded value that cannot be read, or is out of range, is ignored with a warning
  rather than silently renumbering the network.

## Review round (findings folded in)

An independent review of the first revision returned MERGE WITH FIXES:

- **Important — an already-colliding cluster was left broken.** Keeping a recorded VNI
  unconditionally meant the exact incident #80 describes survived the upgrade with no
  signal (the provider had no logger at all, so the plan's "the collision is reported"
  was false). The kernel refuses a second interface with the same VNI — the reviewer
  verified that on the test server — so the affected network stayed dead. A colliding
  recorded VNI is now renumbered once, with a warning naming both networks; because the
  new value is persisted, the repair sticks.
- **Important — the guide claimed a guarantee the code did not make.** Reworded: a
  *newly* assigned VNI is never given to a network that already holds one, and a
  recorded collision is repaired rather than kept.
- **Minor — the distinct-VNI test was not a regression test** (two ordinary names never
  collide). It now searches for a genuine hash collision and asserts that the second
  network skips the taken candidate, and the recorded-collision case has its own test.
- **Minor — plan inaccuracies**: "seven positional arguments" (the old signature had
  six), a salt the code does not use, and the reporting claim above. Corrected, and the
  lab results are recorded here.
- **Minor — stale or imprecise comments**: `GenerateVXLANID`'s "generates a unique
  VXLAN ID", an off-by-one on the VNI bound, an unreadable or out-of-range recorded
  value being dropped silently, and the exhausted-search fallback returning a
  possibly-taken value without a word. All addressed.

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
  candidate VNI already held by an allocated network is skipped; a recorded VNI that
  another network holds is renumbered rather than kept; two names whose hashes collide
  get distinct VNIs; a released network frees its VNI; a bridge network gets none.
- Lab e2e (done on the test server, `ecb86b5`): four user-defined networks with a
  replica pair each, both daemons restarted — every VNI unchanged across the restart,
  zero duplicates, all four VXLAN interfaces present on both nodes, and all four
  networks reach their peer across nodes at **0% loss**.
- Lab e2e: create several user-defined networks, record their VNIs, restart both
  daemons, and check that every VNI is unchanged, no two networks share one, every
  network's VXLAN interface exists on both nodes, and guest-to-guest ping works on all
  of them.
