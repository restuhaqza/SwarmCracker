# #76 — a user-defined network's bridge address is per-node

## Symptom

Every node gives a user-defined network's bridge the **same** host address, because it
is derived from the subnet alone:

```
node1: br-dnsnet3  10.10.0.1/24
node2: br-dnsnet3  10.10.0.1/24
```

Measured on the 2-node lab (`main` at `6e2d617`), network `recon76b`:

| | node1 | node2 |
|---|---|---|
| bridge address | `10.0.4.1/24` | `10.0.4.1/24` |
| guest default route | `via 10.0.4.1` | `via 10.0.4.1` |
| ARP for `10.0.4.1` from the node2 guest | — | answered by **node2's own bridge MAC** |

A network's bridge is fanned out over its own VXLAN, so both nodes sit on one L2 and
both answer ARP for one address. Two nodes advertising the same address on a single L2
is at best a race — whichever answer arrives first becomes that guest's gateway. The
default network does the opposite: its per-node addresses come from `--bridge-ip`
(`192.168.127.1/24` on node1, `.2/24` on node2).

## Root cause

`ensureManagedNetwork` (`pkg/network/managed.go`) derives the bridge address from the
subnet only — the recorded gateway, or `<subnet>.1` when none is recorded — and never
consults the node's slot, although the rest of the machinery is already per-node:

- `nodeAllocationParams(bridgeIP, peerCount)` derives this node's 1-based slot from its
  overlay bridge address, and `reservedInfraHosts = 16` keeps `.1`..`.16` out of the
  guest range;
- `NewIPAllocatorForNode(subnet, gateway, nm.nodeIndex, nm.nodeSlots)` already partitions
  **guest** addresses per node — which is why the lab's guests came out as `.17` and
  `.18`.

Only the bridge address — the address every guest sends off-subnet traffic to — was left
cluster-wide.

## Design

Per-node bridge address, with the node's own guests using it as their gateway: exactly
what the default network does with `--bridge-ip`.

- Node *k*'s bridge on a user-defined network holds `<subnet>.<k>`.
- The guests of node *k* use `<subnet>.<k>` as their gateway, so egress stays local, as
  it already does today when the local ARP answer happens to win.
- The cluster-wide address recorded in the network's IPAM (`.1`) is unchanged and stays
  reachable from every node over the VXLAN — it is node 1's address, and node 1 holds it.
- A node with no slot — `nodeSlots <= 1`, or an overlay bridge address outside the
  reserved range — keeps today's behaviour exactly: `<subnet>.1`.
- A subnet too small to hold `<subnet>.<k>` as a usable host (e.g. a /30 for the second
  node, where it would be the broadcast address) also keeps `<subnet>.1` rather than
  inventing an address outside the range.

Both sides derive the value from one exported helper —
`network.NodeLocalGateway(subnet, clusterGateway, bridgeIP, peerCount)` — so the bridge
the manager creates and the gateway the translator writes into the guest's kernel
command line cannot drift apart.

## Tasks

- [ ] T1 `network.NodeLocalGateway`, with the range checks above.
- [ ] T2 `ensureManagedNetwork` takes the bridge address from it.
- [ ] T3 `convertTask` takes the attachment's gateway from it (nil-config safe), so the
  guest's default route follows the node it runs on.
- [ ] T4 Docs: per-network addressing in the networking guide.
- [ ] T5 Tests: the helper (node 1, node 2, single node, tiny subnet, out-of-range
  address); the bridge address on node 2; the attachment gateway for a multi-node
  configuration.
- [ ] T6 Verify on the test server + lab e2e: distinct bridge addresses, the node2
  guest's default route pointing at node2's own address, cross-node ping still 0%, and
  egress from both guests.

## Out of scope

- #70's masquerade rules (already guarded).
- Removing the cluster-wide `.1` from the spec: `network inspect` keeps showing it, and
  node 1 keeps using it.
