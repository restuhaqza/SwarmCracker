# ADR-007: Cluster-wide VM IP allocation on the shared overlay

- **Date:** 2026-10-11
- **Status:** 🟢 Accepted (implemented)
- **Tracking:** [#51](https://github.com/restuhaqza/SwarmCracker/issues/51)

## Context

The multi-node overlay is a **single flat L2 subnet** shared by every node
(default `192.168.127.0/24`, one VXLAN ID). Every address on it — each node's
bridge **and** every guest — must therefore be unique cluster-wide.

Guest IPs were allocated **node-locally** by hashing the task ID
(`pkg/network/manager.go`, `IPAllocator`), with no knowledge of other nodes.
PR #50 reserved `.1`–`.16` for node bridges (which removed the systematic
guest↔bridge collision), but two guests on different nodes could still hash to
the same address. On the shared L2 that produces ARP ambiguity: the wrong host
answers, cross-node traffic breaks, and host-mode DNAT for `--publish` targets
the wrong VM.

The `pkg/cni` SwarmKit network allocator can assign cluster-unique task IPs, but
it only runs for tasks with **SwarmKit network attachments**; the default bridge
is SwarmCracker-specific and never goes through it.

## Decision

Give each node a **disjoint block** of the guest range, derived from its own
bridge address, without any coordination:

- The node's **slot** is the last octet of its overlay bridge address
  (`bridge_ip: 192.168.127.2/24` → slot `2`). The `cluster init` / `cluster
  join` commands assign `<subnet>.1`, `.2`, … so the slots line up.
- The guest range (`usable = size − 2 − 16`, i.e. `.17` upward on a `/24`) is
  split into `16` blocks (`reservedInfraHosts`), one per slot. Slot `k` owns
  `[(k−1)·blockSize, k·blockSize)`, with the last slot absorbing the remainder.
- The node allocates only within its block (deterministic hash into the block,
  then linear probing **inside** the block). When a block is full, allocation
  fails loudly instead of spilling into another node's block.
- **Partitioning is enabled only for multi-node overlays** (VXLAN peers
  configured). A single node keeps the whole subnet, so single-node behaviour
  and the `/30` (and other small-subnet) fallbacks are unchanged.

The same node slot is applied to user-defined networks (`#48`), whose subnets
are also shared L2: their gateway is `.1` on every node, so the slot is taken
from the node's *overlay* bridge, not from the per-network gateway.

## Consequences

- **Guaranteed uniqueness.** Two nodes can never assign the same guest IP, and
  no guest IP can equal a node bridge IP (the reserved block already covers the
  latter).
- **Capacity is capped per node** on small overlays: a `/24` gives each node
  `14` addresses (`238 / 16`). This is the price of uniqueness without
  coordination on a flat L2. Dense clusters should use a larger overlay subnet
  (e.g. `192.168.0.0/16` → ~4096 per node).
- **Single-node is unaffected** (no peers → no partition).
- The design is node-local and needs no manager round-trip, so it works on
  workers that have no control-API access.
- Bridge addresses must be sequential (`.1`, `.2`, …). If a bridge address
  falls outside `.1`–`.16`, the node logs a warning and falls back to
  unpartitioned allocation (the operator must fix the addressing).

## Alternatives considered

1. **Coordinated IPAM** — have the manager assign every task IP (SwarmKit
   network allocator / CNI IPAM). Guarantees uniqueness and needs no capacity
   cap, but requires the default overlay to become a SwarmKit network (or a new
   manager endpoint) and a round-trip on every task start. Deferred; the CNI
   allocator scaffolding remains the basis for a future move to this model.
2. **Per-node subnets + routing** — give each node its own guest subnet and
   route between them instead of a flat L2. Largest change (redefines the VXLAN
   design) and out of scope here.
3. **Disjoint per-node ranges (chosen)** — no coordination, minimal change,
   at the cost of per-node capacity on a small subnet.

## References

- [#51](https://github.com/restuhaqza/SwarmCracker/issues/51) — the issue.
- PR #50 (`--publish`) — the mitigation and the evidence that motivated this.
- `pkg/network/manager.go` — `IPAllocator`, `NewIPAllocatorForNode`,
  `nodeAllocationParams`.
- `docs-site/src/content/docs/guides/networking.md` — operator-facing docs.
