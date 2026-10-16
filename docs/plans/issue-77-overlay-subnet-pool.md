# Plan — #77: auto-allocated user-defined networks are given the default network's subnet

Branch: `fix/overlay-subnet-pool`
Issue: [#77](https://github.com/restuhaqza/SwarmCracker/issues/77)

## Symptom

A user-defined network created without `--subnet` is allocated the **default
network's subnet**, so its guests get addresses that overlap the flat L2 range:

```
$ swarmcracker network create --name autonet --driver overlay      # no --subnet
$ swarmcracker network ls
NAME      DRIVER  SUBNET
autonet   vxlan   192.168.127.0/24        <- the default network's subnet
```

## What the recon established

1. **The alignment of the pool with `--subnet` is deliberate.** `3cdf44a` (#52,
   ingress routing mesh) derived the CNI/overlay pool from `--subnet` "so ingress
   task IPs land on the flat L2 and are reachable cluster-wide". The ingress
   network is a **predefined** network with no explicit subnet
   (`pkg/cni/provider.go:69-78`), so its subnet comes from that pool. Changing the
   pool globally would move the ingress network off the flat L2.
2. **SwarmCracker's own provider does the allocation**, not SwarmKit:
   `CNIProvider.AllocateNetwork` carves a subnet from `p.config.SubnetPool`
   (`pkg/cni/provider.go:190`). SwarmKit's `DefaultAddrPool` is only propagated to
   the cluster object (`manager.go:972`, `1028-1040`) and is never used for the
   decision here.
3. **The default network's task IPs do not come from this pool.** Measured on the
   lab with `--cni-subnet-pool 10.99.0.0/16` on both nodes: a service on the
   default network still got `192.168.127.36`, cross-node ping **0% loss**; a
   user-defined network created without `--subnet` got `10.99.3.0/24` and pinged
   cross-node at **0% loss**.
4. **Why it collides today:** the pool is `--subnet` (a single /24) and
   `GenerateSubnet` computes `count = poolSize / step = 1`, so
   `base + index%1*step` is always the pool itself (`pkg/cni/config.go:150-155`).

## Root cause

User-defined networks have no subnet of their own, so they are allocated from the
same pool that must stay aligned with the flat L2 for the ingress network. With
that pool being a single /24, every such network is handed the flat L2 subnet.

## Fix

- **A dedicated pool for user-defined networks.** `CNIConfig` gains
  `OverlaySubnetPool` (flag `--cni-overlay-subnet-pool`, default
  `cni.DefaultSubnetPool` = `10.0.0.0/8`, i.e. 65536 /24s). Predefined networks
  (`ingress`, `docker_gwbridge`) keep the flat-L2 pool, so #52's alignment and the
  ingress mesh are untouched.
- **Skip candidates that are not free.** When carving from the overlay pool, skip
  any subnet that overlaps the flat L2 subnet or an already-allocated network.
- **Reject an explicit overlap.** An explicitly requested `--subnet` that overlaps
  the flat L2 subnet or an existing network is refused with a clear error, so the
  guard covers every client (CLI, `swarmctl`, API) rather than only the CLI.

## Explicitly out of scope

- **#76**: distinct per-node bridge addresses for user-defined networks. Separate
  defect, separate scheme change.

## Tasks

| # | Task | Files | Verify |
|---|------|-------|--------|
| T1 | Recon: why the pool is aligned, who allocates, and whether the default path depends on it | — | lab (done) |
| T2 | Overlay pool for user-defined networks; predefined keep the flat-L2 pool; skip non-free candidates | `pkg/cni/`, `cmd/swarmd-firecracker/` | unit |
| T3 | Reject an explicit subnet overlapping the flat L2 or an existing network | `pkg/cni/` | unit |
| T4 | Tests + docs (configuration, networking) | `pkg/cni/*_test.go`, `docs-site` | `npm run verify` |
| T5 | Independent whole-branch review | — | — |
| T6 | Lab e2e, including the **ingress regression check** | — | see below |
| T7 | Merge once CI is green, then restore the lab | — | CI |

## Verification

- Unit: predefined networks take the flat-L2 pool and user-defined ones take the
  overlay pool; a candidate overlapping the flat L2 or an existing allocation is
  skipped; an explicit overlapping `--subnet` is rejected with a clear error;
  distinct networks get distinct subnets.
- Lab e2e: three networks created without `--subnet` get **distinct** subnets, none
  equal to the flat L2; guest-to-guest ping works cross-node on each; an explicit
  `--subnet 192.168.127.0/24` is rejected; a duplicate explicit `--subnet` is
  rejected; the default network still works; and a **published (ingress)** service
  still serves through the mesh, which is the #52 behaviour this change must not
  disturb.
