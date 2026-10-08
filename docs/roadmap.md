# SwarmCracker Roadmap

- **Status:** Active
- **Last updated:** 2026-10-08
- **Owner:** Restu Muzakir
- **Tracking epic:** [#40](https://github.com/restuhaqza/SwarmCracker/issues/40)

> Internal document (not published to docs.swarmcracker.com). It records the
> gap analysis and delivery order behind the "Docker Swarm UX with microVMs"
> positioning. The GitHub epic is the live tracker; this file is the rationale.

## Purpose

SwarmCracker already delivers what no container runtime can: each SwarmKit task
boots its own Linux kernel under KVM. But "hardware isolation with the Docker
Swarm interface" only holds if the *interface* is actually as capable as Docker
Swarm. This roadmap closes that gap before adding differentiation.

## Where we are today (strengths to keep)

Per-VM kernel isolation, SwarmKit executor, cross-node VXLAN, snapshot/restore,
golden images (`recipes/*.yaml`), volume drivers (dir/block/quota), jailer,
Prometheus metrics, `vm attach` console, node drain/activate, secret/config
*injection* into the guest, and a sysvinit/systemd-aware boot path.

## The gap

An audit of `pkg/` and `cmd/` found several table-stakes capabilities missing or
only half-built. Evidence (non-test code):

| Gap | Evidence |
|-----|----------|
| No port publishing | no `--publish` flag (`cmd/swarmcracker/cmd_service.go:190-200`); `Controller.PortStatus()` returns `{}` (`pkg/swarmkit/executor.go:1110-1112`) |
| No routing mesh / VIP LB | ingress CNI code exists (`pkg/cni/config.go:60`, `types.go:31`, `ipam.go:192-329`, `allocator.go:189-274`) but has **no caller outside `pkg/cni`**; `RoutingMesh`/`DNSRR`/`LoadBalancer` = 0 references |
| No secrets/configs CLI | injection exists (`pkg/swarmkit/executor.go:661-668`); the only `config` command manages the config *file*; `pkg/swarmkit/secrets/` & `configs/` are empty |
| No healthchecks | `HealthCheck` = 0 references outside tests |
| Thin `service create` | only `name/image/replicas/cpu/memory/disk/env/command/args/label` (`cmd_swarmcracker/cmd_service.go:190-200`); `UpdateConfig` = 0 references |
| No in-guest service DNS | `pkg/discovery/consul.go` is used only for node-to-node VXLAN peer discovery (`executor.go:203`), not per-service name resolution |
| Registry auth is config-only | `RegistryAuth` in `pkg/image/preparer.go:48`; no CLI/`--with-registry-auth` |

## Tier 1 — close the "Swarm just works" gaps

| # | Feature | Depends on |
|---|---------|------------|
| [#35](https://github.com/restuhaqza/SwarmCracker/issues/35) | `service create --publish`: expose microVM ports on the host | — |
| [#36](https://github.com/restuhaqza/SwarmCracker/issues/36) | Ingress routing mesh: cluster-wide VIP load balancing | #35 |
| [#37](https://github.com/restuhaqza/SwarmCracker/issues/37) | `service create` parity: networks, mounts, restart, global mode, constraints, update/rollback | — |
| [#38](https://github.com/restuhaqza/SwarmCracker/issues/38) | Secrets & configs end-to-end: CLI + service attachment | #37 (flags) |
| [#39](https://github.com/restuhaqza/SwarmCracker/issues/39) | Container healthchecks + health-gated rolling updates | guest exec/agent |

Each issue carries its own acceptance criteria. Tier 1 is "done" when a fresh
three-tier app (web + api + db) can be deployed with published ports, secrets,
healthchecked rollouts, and no manual IP lookups.

The architectural decision behind #35/#36 is recorded in
[ADR-006](../decisions/ADR-006-service-exposure-and-ingress.md).

## Tier 2 — runtime & operations depth

| # | Feature |
|---|---------|
| [#42](https://github.com/restuhaqza/SwarmCracker/issues/42) | In-guest service DNS — resolve `web`, `tasks.web` inside VMs, independent of Consul |
| [#43](https://github.com/restuhaqza/SwarmCracker/issues/43) | `vm exec` + file copy via a guest agent (vsock) — also the foundation for healthchecks and in-guest metrics |
| [#44](https://github.com/restuhaqza/SwarmCracker/issues/44) | Registry auth UX — `--with-registry-auth` and docker-config integration |
| [#45](https://github.com/restuhaqza/SwarmCracker/issues/45) | Observability depth — in-guest metrics, cross-node log aggregation, alerting, tracing |

## Tier 3 — differentiation & scale

| # | Feature |
|---|---------|
| [#46](https://github.com/restuhaqza/SwarmCracker/issues/46) | Live migration of running microVMs across nodes (Firecracker snapshot + UFFD) — the standout feature versus firecracker-containerd |
| [#47](https://github.com/restuhaqza/SwarmCracker/issues/47) | Resource QoS — memory balloon, CPU pinning/NUMA, hugepages, cgroup v2 |
| [#48](https://github.com/restuhaqza/SwarmCracker/issues/48) | User-defined overlay networks and segmentation (`network create/rm`, service → network attach) |
| [#49](https://github.com/restuhaqza/SwarmCracker/issues/49) | Autoscaling and placement intelligence |

## Non-goals

- Kubernetes API compatibility.
- L7/TLS ingress — that is a reverse-proxy concern; the core deliverable is L4.

## Sequencing

1. **#35** first (unblocks the most use cases; groundwork already exists).
2. **#36** next (builds directly on #35).
3. **#37 → #38** in parallel with the above.
4. **#39** once a guest exec path exists.

Tier 2/3 issues (#42–#49) exist as a backlog but are not yet sequenced; revisit
them once Tier 1 lands.
