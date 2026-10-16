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
- Node 1 keeps whatever address the network records, so a network created with an
  explicit `--gateway` keeps that gateway held (and the guests' default route with it).
- The per-node address applies where the per-node guest blocks do: from a `/27` up.
  Below that the allocator partitions nothing, so taking `<subnet>.<slot>` would put the
  bridge on an address a guest could be handed.

Both sides derive the value from one exported helper —
`network.NodeLocalGateway(subnet, clusterGateway, bridgeIP, peerCount)` — so the bridge
the manager creates and the gateway the translator writes into the guest's kernel
command line cannot drift apart.

## Tasks

- [x] T1 `network.NodeLocalGateway`, with the gates above.
- [x] T2 `ensureManagedNetwork` takes the bridge address from it, and reconciles the
  bridge's addresses so an address an earlier release left there is removed (T7).
- [x] T3 `convertTask` takes the attachment's gateway from it (nil-config safe), so the
  guest's default route follows the node it runs on.
- [x] T4 Docs: per-network addressing in the networking guide.
- [x] T5 Tests: the helper's table; the bridge address on node 2; the attachment gateway
  for a multi-node configuration.
- [x] T6 Verified on the test server and the 2-node lab.
- [x] T7 `reconcileBridgeAddr` removes a stale address from a managed bridge (from the
  review), with a test that materializes a network on node 2 whose bridge still holds the
  old cluster-wide address.

## Verification

- Test server (`the-root`, Go 1.26.8) against `main`: gofmt, vet and build clean; short
  `-race` `./pkg/...` and non-short `./pkg/... ./cmd/...` failure sets identical to the
  base; golangci-lint clean.
- Lab e2e: `br-*` `10.0.3.1/24` on node 1 and `10.0.3.2/24` on node 2; each guest's
  default route points at its own node's address; `10.0.3.1` still reachable from the
  node 2 guest; cross-node guest-to-guest 0% loss; egress from both nodes 0% loss.
- Upgrade measurement: with the old build both nodes held `10.0.3.1/24`; after deploying
  the new build and restarting both daemons node 2 held only `10.0.3.2/24`.
- Predefined networks: on `main` node 2's `br-ingress` held `192.168.127.1/24` (node 1's
  address); with the branch it holds `192.168.127.2/24`, i.e. that node's own address on
  the flat L2, which is the only address a node has on that segment. The ingress DNAT
  rules (to the replica IPs, 50/50) are present on both nodes, unchanged.

## Review round (findings folded in)

An independent review of the first revision returned MERGE WITH FIXES:

- **Critical — the new test was red, and invisible.** `TestNodeLocalGateway` had two
  wrong rows (see T7 below for the *other* reason it was invisible). Both fixed, and the
  case split into what the helper actually contracts for.
- **Important — an upgraded node kept the duplicate.** `setBridgeAddr` only adds, so a
  bridge an earlier release created would have held both the cluster-wide address and
  the node's own. `reconcileBridgeAddr` now removes any other IPv4 address first (T7).
- **Important — node 1 did not always hold the recorded gateway.** Now it does, so a
  network created with `--gateway` keeps it held.
- **Important — the per-node address also needs a usable guest pool.** The helper's gate
  now matches the allocator's (a `/27` up), so it cannot take an address a guest could be
  handed on a small subnet.
- **Important — the `ingress`/`docker_gwbridge` bridges.** Checked on the lab (above):
  the cross-node duplicate is gone, and each node's ingress bridge sharing its default
  bridge's address is inherent to one address per node per L2, with the ingress datapath
  reading replica IPs rather than the bridge address.
- **Important — the fix is conditional.** It needs each node's own `bridge_ip` and
  configured peers, which the docs now state, as they do for the per-node guest blocks.
- **Minor — stale doc**: the networking guide still called user-defined overlays
  cross-node broken (#70, fixed). Removed.

Also filed [#83](https://github.com/restuhaqza/SwarmCracker/issues/83): an empty network
ID panics in `networkBridgeName` and aborts the whole `./pkg/network/` test binary, which
is how the red row above stayed invisible in a full package run.

## Out of scope

- #70's masquerade rules (already guarded).
- Removing the cluster-wide `.1` from the spec: `network inspect` keeps showing it, and
  node 1 keeps using it.
