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
  behind (in either the current or the earlier unguarded shape).
- **Sweep at startup**: rules this package tagged are removed whatever bridge they
  name (a network still in use re-materializes and re-adds its rule), and untagged
  rules from an earlier release are removed when the bridge they name is gone and the
  name is one of ours. This also repairs nodes that already accumulated rules.
- **Exclude bridged traffic explicitly** with `-m physdev ! --physdev-is-bridged`:
  MASQUERADE exists for *routed* outbound traffic, and guest-to-guest frames must
  never be translated.
- **Tag the rules** with `--comment swarmcracker:managed-nat` so the sweep recognises
  its own rules instead of inferring from the shape — Docker's per-network rule is
  the same shape.
- **Guard the default network's rule too**, and replace an unguarded one an earlier
  release installed. This is the case the issue's own reproduction hits: an
  auto-allocated network is given the default subnet, so the default rule — not the
  managed one — is what masquerades its guest-to-guest frames.

## Review round (findings folded in)

An independent review of the first revision found the following, all addressed:

- **Critical — the default rule was left unguarded.** `setupNAT` installs
  `-s <subnet> -j MASQUERADE` with no guard, and `GenerateSubnet` returns the CNI
  pool itself when the pool equals the subnet size, so **every** auto-allocated
  network gets the default subnet. The first revision guarded only the managed rule,
  so the reported symptom survived on the documented `network create` path. Verified
  on the lab before fixing: the unguarded default rule matched 17 packets while the
  managed guarded rule matched 0, and the ping still lost 100%.
- **Important — the sweep could delete a foreign rule.** Docker's per-network
  masquerade rule is byte-for-byte the same shape, so deleting by shape could remove
  it while its bridge was momentarily absent. Rules are now tagged with a comment and
  only untagged ones whose device name is ours (`br-<name>`/`sc-<id>`, never
  Docker's `br-<12 hex>`) are considered.
- **Important — an upgrade did not heal a surviving bridge.** The sweep kept rules
  whose bridge existed, so an old unguarded rule survived a restart and
  `ensureRule` added a second, guarded one next to it. The sweep now removes every
  tagged rule, and `setupManagedNAT`/`setupNAT` replace the unguarded shape.
- **Important — delete-by-spec with a quoted comment.** `iptables -S` quotes a
  comment containing `:`, and `-D` compares it literally; the sweep now unquotes it,
  as `removeTaskRules` already did for port rules.
- **Minor — the CLI does not run `Init`.** The plan claimed the standalone CLI path
  sweeps too; it does not (`Init` is called from the swarmkit agent only), so there
  is no cross-process sweep risk. The plan is corrected rather than the claim kept.
- **Minor — test gaps.** The "not my rule" fixture is now a genuine foreign
  masquerade rule (Docker-shaped, absent bridge), the install/delete shapes are
  pinned by a table, the comment-unquoting path is asserted, and the default rule's
  guard has its own test.

## Explicitly out of scope

- **Distinct per-node bridge addresses for user-defined networks.** It is a real
  defect (two nodes advertising the same address on one L2) and it deserves its own
  issue: the guests' gateway is a cluster-wide address today, and changing the host
  address is not a one-line change.
- **Preventing a user-defined network from being auto-allocated the default
  subnet.** The guard makes such a network work, but the address overlap itself
  (issue observation #1) is a separate allocator problem.
- The isolation chain (`SC-ISOLATION`) is **not** affected: cross-bridge traffic is
  *routed*, not bridged, so `-i br-A -o br-B` matches as intended.

## Tasks

| # | Task | Files | Verify |
|---|------|-------|--------|
| T1 | One builder for every masquerade shape; managed rules guarded and tagged | `pkg/network/managed.go` | unit |
| T2 | Teardown deletes both shapes; startup sweep removes tagged rules, stale untagged ones and replaces the default rule's unguarded shape | `pkg/network/managed.go`, `pkg/network/manager.go` | unit |
| T3 | Tests: shape table, parser, bridge-name check, install, teardown, sweep matrix, default-rule guard | `pkg/network/managed_nat_test.go` | unit |
| T4 | Docs: the datapath note, including the default rule | `docs-site` | `npm run verify` |
| T5 | Independent whole-branch review, findings folded in | — | — |
| T6 | Lab e2e: reproduce the stale-rule failure and the auto-allocated-subnet failure, deploy the fix, confirm traffic and rule hygiene | — | see below |

## Verification

- Unit: each shape is pinned by a table; the parser accepts only managed shapes; the
  bridge-name check rejects Docker's `br-<12 hex>`; install carries the guard and the
  comment and replaces the unguarded shape; teardown deletes both shapes; the sweep
  deletes exactly the tagged rule (with the comment unquoted), the stale untagged one
  and the default rule's unguarded shape, while keeping a live bridge's rule, a
  Docker-shaped rule, a rule for a real interface and a non-masquerade; `setupNAT`
  installs the guarded default rule.
- Lab e2e: (a) a stale rule planted for a deleted bridge breaks a fresh subnet, and
  removing it repairs the subnet; (b) a network on a subnet whose stale rule is still
  present fails before the fix and works after it; (c) a network created **without**
  `--subnet` (auto-allocated the default subnet) fails before the fix and works after
  it; (d) `network rm` leaves no POSTROUTING rule behind; (e) default-network
  cross-node traffic still works.
