# Plan — #42: in-guest service DNS

Branch: `feat/service-dns`
Issue: [#42](https://github.com/restuhaqza/SwarmCracker/issues/42)

## Problem

MicroVMs get IPs but nothing resolves service names inside the guest. Today a
guest's `/etc/resolv.conf` is `8.8.8.8`/`8.8.4.4` (injected at image build) —
which is unreachable from a guest on a NAT-less bridge, so DNS is broken inside
VMs entirely. So one VM cannot reach another as `web` or `tasks.web`.

## What the recon established

| Fact | Evidence |
|------|----------|
| The per-bridge `dnsmasq` already **serves DNS** on the bridge gateway and forwards to the host's upstream | lab: `nslookup github.com <gw>` resolves, unknown names → SERVFAIL |
| Nothing ever points a guest at it | `pkg/image/essentials.go:45-65` writes Google DNS; the wrapper only rewrites `resolv.conf` when `sc.dns=` is set (`pkg/image/wrapper.go:114-120`) |
| The ingress table already carries `service → healthy replica IPs`, computed on a manager and distributed to every node over mutual TLS (3 s poll) | `pkg/ingress/{source,controller,table,server,client}.go` |
| …but it carries **no service name** (only the service ID) | `pkg/ingress/source.go:53`, `table.go:19-30` |
| The translator already computes the guest's **gateway** (the bridge IP, i.e. where dnsmasq listens) | `pkg/swarmkit/translator.go:206-218` |
| `PrepareNetwork` runs **before** `Translate` | `pkg/swarmkit/executor.go:713` vs `:833` |
| `dnsmasq` is part of the standard install | `infrastructure/ansible/group_vars/all.yml:53` |
| `dnsmasq` is launched **only for the default bridge** | `setupDHCP` has one caller, `manager.go:530`, before the managed-network branch |
| The allocated VIP is not a routable address (the ingress datapath is host-port DNAT) | `pkg/cni` only stores `ServiceVIP`; no `VIP` reference in `cmd/` or `pkg/swarmkit` |
| Cross-node traffic works on the **default** network but is broken on user-defined overlays | lab: `ping -c 3` 0% vs 100% loss → filed as #70 |
| **The control API cannot see a default-bridge task address.** `api.TaskStatus` has no address field, the control API has no `UpdateTask`, and `Task.Networks[].Addresses` is written only by the manager's own network allocator (the ingress network and user-defined networks) | verified on the lab: a `RUNNING` plain task reports `IPs: null` while a published service's task carries an address (its DNAT backend). This is the same fact #66 recorded for the metadata sidecar |

## Design

Reuse the ingress distribution channel rather than adding a transport: the same
manager→node table, the same mutual TLS, the same 3 s poll.

### Records (data)

- `ServiceSpec` and the wire `TableRoute` gain `Name` (additive), populated from
  `svc.Spec.Annotations.Name`.
- `ControlClientSource.Services` stops filtering to ingress-published services: it
  returns **every** service (with its ingress ports when it has them). Ingress
  behaviour is unchanged because `BuildRoutes` already ignores portless routes.
- New `BuildDNSRecords(services, tasks, reported)` produces `[]network.DNSRecord` — for
  each service with a name, `<name>` and `tasks.<name>` → the IPs of its **running**
  tasks, preferring the address the node running the task reported and falling back
  to the control API's. A service with no running replica produces no record, so
  records follow tasks starting and stopping with no stale entries.
- The wire `Table` gains `Records []TableRecord{Name, IPs}`; the revision hash
  covers them, so workers detect record changes.

### Addresses (the report channel)

The control API only knows the addresses of tasks attached to a network its own
allocator manages, so a plain service's replicas are invisible to the manager.
Each node therefore reports the addresses it has recorded for its own VMs
(`SidecarAddressSource`, reading the per-task metadata sidecar — the same file
`vm status` uses):

- `POST /v1/ingress/report` on the manager's existing mutual-TLS table server;
  the reporting identity comes from the client certificate, never the body.
- `ReportStore` keeps the latest report per node with a TTL (20s), so a node that
  crashes, partitions or drains stops contributing addresses instead of leaving a
  record pointing at something unreachable.
- `Reporter` publishes every 5s from both managers and workers (a manager reports
  to itself over the same endpoint), and the reconciler joins reports with the
  control API by task ID — a task is only resolved while it is `RUNNING`.

### Serving (dnsmasq)

- `dnsmasq` is launched with `--addn-hosts=<records file>` (an `/etc/hosts`-format
  file: multiple lines for one name yield multiple A records, i.e. DNS-RR).
- New `NetworkManager.ProgramDNS(records)`: render the records deterministically,
  write the file when the content changed, and `SIGHUP` the bridge's dnsmasq
  (which re-reads addn-hosts and clears its cache). Idempotent, so the reconciler
  can call it every tick — same shape as `ProgramIngress`.
- A live dnsmasq that was started **without** `--addn-hosts` (an instance from
  before this change, reused by the existing liveness check) is treated as stale
  and restarted, so an upgrade does not silently serve no records.

### Guest side

- The network manager, which owns the bridge and knows whether a resolver is
  running on it, publishes the resolver address on the task
  (`types.AnnotationResolver`) in `PrepareNetwork`.
- The translator emits `sc.dns=<resolver>` when the task carries it and the
  container declares no `--dns`; the existing wrapper then writes
  `/etc/resolv.conf` with that nameserver. An explicit `--dns` always wins.
- dnsmasq forwards anything it does not know, so external names keep resolving.

### Enablement

Service DNS follows the ingress mesh (already default-on, `--ingress-mesh`): the
reconciler that computes the table also computes records, and the same two call
sites (manager controller, worker table client) program them.

### Explicitly out of scope (documented, not silently missing)

- **User-defined networks**: a service attached to one does resolve — its
  addresses are reported like any other — but that overlay's cross-node datapath
  is broken (#70), so such a replica is reachable only from the same node. Those
  bridges also get no dnsmasq of their own, so a guest attached *only* to one
  keeps the image's resolver.
- **VIP-based resolution**: the VIP is not a routable address today, so
  `<service>` resolves to replica IPs (DNS-RR) — the issue's documented fallback.
- **Golden images**: they boot their own init and never read `sc.dns`; the image's
  own resolver applies.
- Search domains and per-network name scoping.

## Tasks

| # | Task | Files | Verify |
|---|------|-------|--------|
| T1 | `DNSRecord`, `ProgramDNS` (records file + SIGHUP), `--addn-hosts`, stale-instance restart | `pkg/network/dns.go`, `manager.go` | `go test ./pkg/network/` |
| T2 | `Name` on the spec/table, `TableRecord`s, `BuildDNSRecords`, controller + client apply, source relaxed | `pkg/ingress/*.go` | `go test ./pkg/ingress/` |
| T2b | Node→manager task report channel: `ReportStore` with TTL, `POST /v1/ingress/report` (identity from the client certificate), `Reporter`, sidecar address source, `ListVMMetadata` | `pkg/ingress/report.go`, `server.go`, `pkg/runtime/metadata.go`, `cmd/swarmd-firecracker/*.go` | `go test ./pkg/ingress/ ./pkg/runtime/` |
| T3 | Resolver annotation + `sc.dns` default | `pkg/types/annotations.go`, `pkg/network/manager.go`, `pkg/swarmkit/translator.go` | `go test ./pkg/swarmkit/ ./pkg/network/` |
| T4 | Wiring (manager controller + worker client) | `cmd/swarmd-firecracker/ingress.go` | build |
| T5 | Docs | `guides/networking.md`, `guides/operations.md` | `npm run verify` |
| T6 | Independent whole-branch review at the frozen tip | — | — |
| T7 | Lab e2e (2 nodes) | — | see below |

## Verification

- Unit tests per task.
- Lab e2e: two services on the default network, one replica each on different
  nodes; from inside one guest, `getent hosts`/`nslookup` resolves `<service>` and
  `tasks.<service>` to the replica IPs **including the remote node's**; killing a
  replica removes its record; an external name still resolves through the gateway.
- The known-broken user-defined-network path (#70) is documented, not papered over.
