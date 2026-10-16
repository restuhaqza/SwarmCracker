# Plan — #70: cross-node guest-to-guest traffic dies on user-defined overlay networks

Branch: `fix/overlay-stale-nat-rules`
Issue: [#70](https://github.com/restuhaqza/SwarmCracker/issues/70)

## Symptom

Two replicas of one service on a user-defined overlay network, landing on
different nodes, cannot reach each other: 100% ICMP loss. The same test on the
default network works. ARP resolves and the remote VTEP is programmed, so the L2
control plane looks healthy.

## What the lab established

Packet captures of the *same* echo request (id 628, seq 0) at the two guest TAP
ports:

| Point | Frame |
|-------|-------|
| node1, guest A's TAP | `10.10.0.18 > 10.10.0.17`, MAC `aa:fc:ae:88:a9:b1 > aa:fc:1c:67:50:dd` |
| node2, guest B's TAP | **`10.10.0.1 > 10.10.0.17`**, same MACs |

The frame is delivered, but its **source IP has been rewritten to the bridge
address**. Guest B replies to `10.10.0.1` — which on node2 is *also* a local bridge
address — so the reply is consumed by node2's own stack and never returns.

The rewrite is done by a `nat POSTROUTING` MASQUERADE rule. Which rule, and whether
the product's own rule is to blame, was settled by experiment:

| Setup | Result |
|-------|--------|
| every `10.10.0.0/24` masquerade rule removed | ping **0% loss** |
| exactly the shape the product installs for this network (`-s 10.10.0.0/24 ! -o br-dnsnet3 -j MASQUERADE`) | ping **0% loss**, rule counter **0** |
| that rule plus `-m physdev ! --physdev-is-bridged` | ping **0% loss**, rule counter **0** |

So the network's *own* rule never matches its own bridged traffic. Under
`br_netfilter` (`bridge-nf-call-iptables=1`, the lab and Docker/CNI default) iptables
sees bridged traffic with the **bridge device** as in/out interface — `! -o
br-<this network's bridge>` is therefore false for its own traffic. The rule that
did the damage was a **leftover rule from a network that had been torn down**:

```
-A POSTROUTING -s 10.10.0.0/24 ! -o br-backend -j MASQUERADE     # 15 packets
-A POSTROUTING -s 10.10.0.0/24 ! -o br-dnsnet3  -j MASQUERADE    #  0 packets
```

Its `! -o br-backend` is *true* for traffic bridged on `br-dnsnet3`, so it
masqueraded a different network's frames.

## Root cause

1. **`teardownManagedNetwork` never removes the network's MASQUERADE rule**, so
   rules accumulate for networks that no longer exist.
2. **Auto-allocation reuses subnets** (the issue's own observation #1), so a later
   network can be given the same subnet as a torn-down one — and the stale rule,
   keyed only by subnet, hijacks its traffic.
3. **The per-node bridge address is duplicated** for user-defined networks (the
   issue's observation #2): both nodes claim `10.10.0.1`, so the masqueraded reply
   lands on the peer's own bridge instead of reaching the node whose conntrack
   could reverse the mapping. (The default network escapes this because its
   per-node addresses differ, which is why the same bug is invisible there.)

## Fix

- **Delete the rule on teardown**, so a network that is removed leaves no rule
  behind.
- **Sweep stale managed rules at startup**: any POSTROUTING MASQUERADE rule of the
  managed shape (`-s <subnet> ! -o <bridge>`) whose bridge no longer exists is
  removed. Networks still in use are re-materialized on demand and re-add their
  rule, so this is safe and it repairs nodes that already accumulated rules.
- **Exclude bridged traffic explicitly** with `-m physdev ! --physdev-is-bridged`:
  MASQUERADE exists for *routed* outbound traffic, and guest-to-guest frames must
  never be translated. This makes cross-node guest traffic independent of gateway
  addressing and of conntrack repair, rather than relying on the per-node address
  being unique.

## Explicitly out of scope

- **Distinct per-node bridge addresses for user-defined networks.** It is a real
  defect (two nodes advertising the same address on one L2) and it deserves its own
  issue: the guests' gateway is a cluster-wide address today, and changing the host
  address is not a one-line change.
- The isolation chain (`SC-ISOLATION`) is **not** affected: cross-bridge traffic is
  *routed*, not bridged, so `-i br-A -o br-B` matches as intended.

## Tasks

| # | Task | Files | Verify |
|---|------|-------|--------|
| T1 | Managed MASQUERADE rule carries the physdev guard; a single helper owns its shape | `pkg/network/managed.go` | unit |
| T2 | Teardown deletes the rule; startup sweep removes rules for bridges that no longer exist | `pkg/network/managed.go`, `pkg/network/manager.go` | unit |
| T3 | Tests: rule shape, teardown deletion, stale-rule sweep (including "not my rule" cases) | `pkg/network/*_test.go` | unit |
| T4 | Docs: the datapath note in the networking guide | `docs-site` | `npm run verify` |
| T5 | Independent whole-branch review | — | — |
| T6 | Lab e2e: reproduce the stale-rule failure, deploy the fix, confirm traffic and rule hygiene | — | see below |

## Verification

- Unit: the installed rule contains the physdev guard; teardown issues the delete;
  the sweep removes managed-shape rules for absent bridges and leaves the default
  network's rule, live networks' rules and unrelated rules alone.
- Lab e2e: (a) with a stale rule planted for a deleted bridge and the same subnet,
  cross-node ping fails before the fix and succeeds after; (b) `network rm` leaves
  no POSTROUTING rule behind; (c) two networks sharing a subnet do not break each
  other; (d) default-network cross-node traffic still works.
