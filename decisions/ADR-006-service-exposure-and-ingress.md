# ADR-006: Service Exposure and Ingress Routing Mesh

- **Date:** 2026-10-08
- **Status:** 🟡 Proposed (pending ratification)
- **Tracking:** [#35](https://github.com/restuhaqza/SwarmCracker/issues/35) (publish), [#36](https://github.com/restuhaqza/SwarmCracker/issues/36) (routing mesh)

## Context

Today a SwarmCracker service is **unreachable except by raw microVM IP**. There is
no `--publish` flag on `service create` (`cmd/swarmcracker/cmd_service.go:190-200`),
and `Controller.PortStatus()` returns an empty struct
(`pkg/swarmkit/executor.go:1110-1112`). An operator must run `vm status`, copy the
guest IP, and connect directly.

This breaks the product's stated promise of a Docker Swarm-compatible interface,
where `docker service create -p 80:80` is the most basic operation.

The codebase already contains the **foundation** for a routing mesh but does not
use it: `GenerateIngressConfig` (`pkg/cni/config.go:60`), `IngressNetworkName`
(`pkg/cni/types.go:31`), VIP allocation (`pkg/cni/ipam.go:192-329`), and
`AllocateService`/`DeallocateService` (`pkg/cni/allocator.go:189-274`). No caller
outside `pkg/cni` exists, so the ingress network is never created and VIPs are
never allocated.

We need a decision on *how* services are exposed before implementing #35/#36.

## Decision

Expose services in **two layers**, matching Docker Swarm's model:

### Layer 1 — Host port publishing (per replica)

`service create --publish [host:]container[/tcp|udp]` forwards a host port to the
guest port through the VM's TAP device. This is implemented as host-side DNAT
(nftables) or Firecracker `hostfwd`, chosen during implementation and documented
in `guides/networking.md`. This unblocks single-replica and stateless access and
is tracked in #35.

### Layer 2 — Ingress routing mesh (cluster-wide)

For replicated services, `--publish` (default `--publish-mode ingress`) publishes
on **every node** and forwards to any healthy replica through a per-service
**VIP**. This reuses the existing `pkg/cni` scaffolding: the ingress network is
created, VIPs are allocated from the top of the subnet on service create/update
and released on `rm`. Balancing is **L4 (TCP/UDP)**. Tracked in #36.

`--publish-mode host` preserves the Layer-1 per-replica behavior.

### Explicitly rejected

- **L7 / TLS termination in the mesh.** Keep the core L4; TLS/HTTP routing belongs
  to a reverse proxy (Traefik/nginx), not the platform.
- **A flag that is accepted but ignored.** If a feature is not yet enforced, the
  CLI must reject it with a clear error.

## Alternatives considered

1. **Rely on an external load balancer / reverse proxy (HAProxy, Traefik).**
   Cheapest, but contradicts "Docker Swarm UX" — users expect `-p 80:80` to work
   out of the box, and it imposes an extra moving part on homelab users.

2. **Consul-based service registry + host-side proxy.** Consul is already present
   for VXLAN peer discovery (`pkg/discovery/consul.go`). But it adds a data-plane
   dependency and does not give a stable per-service VIP as cleanly as the
   existing CNI allocator.

3. **Per-replica host ports only (Layer 1 without the mesh).** Simple and useful as
   a first step, but with N replicas the operator must know every node's IP/port,
   and there is no single entry point. Kept as `--publish-mode host`.

4. **Full L7 mesh.** Highest capability, highest complexity, and out of scope for a
   platform whose value is kernel-level isolation.

## Consequences

### Positive

- `-p 80:80` behaves as users expect; multi-replica services get one stable VIP.
- Reuses already-written (and already partially tested) CNI/VIP code, so #36 is
  incremental rather than greenfield.
- Clear two-layer model maps 1:1 onto Docker's ingress/host publish modes.

### Negative

- Layer 1 has per-node port-collision semantics (two replicas on one node cannot
  bind the same host port) that must be documented.
- The ingress data path becomes platform-owned code that must be maintained and
  tested multi-node.
- L4 only: no path-based routing.

## Implementation plan

1. #35 — host port publishing (parsing, spec mapping, `PortStatus`, tests, docs).
2. #36 — wire the ingress network and VIP allocation; add `--publish-mode`.
3. Update `guides/networking.md` and `reference/cli.md`; add a multi-node e2e test.

## References

- Docker Swarm ingress routing mesh (conceptual parity target).
- `pkg/cni/{config,types,ipam,allocator}.go` — existing, unwired foundation.
- `docs/roadmap.md` — delivery order.
