---
title: "Internal (Private) Endpoints"
description: "The TCP listeners SwarmCracker opens for cluster-internal traffic — none of them is part of the v1 API contract."
---

> The TCP listeners SwarmCracker opens for cluster-internal traffic — none of them is part of the v1 API contract.

SwarmCracker opens several TCP ports that are meant for the daemons to talk to
each other, or for the local host to inspect a node. They are **private**: they
carry no version negotiation, no compatibility promise and no stable schema.
This page documents them so operators can reason about their firewall and their
threat model — *not* as an integration surface.

:::caution[Private — not part of the v1 contract]
Every endpoint on this page is **private**. It is **not part of the v1 API
contract**, it **may change or disappear in any minor release** (including
fields, paths, status codes and default ports), and you should **not build
anything on it**. There is no OpenAPI schema, no deprecation window and no
support commitment for these listeners. The supported, versioned surface is the
[v1 HTTP API](/reference/api/#public-http-api-v1).
:::

## Summary

| Port | Where | Listener | Purpose | Transport / authentication |
|------|-------|----------|---------|----------------------------|
| `4243` | manager | `GET /v1/ingress` | Serve the ingress routing table to the other nodes | TLS with **cluster mutual TLS** (node certificate required) |
| `4243` | every node (manager: itself, workers: toward the manager) | `POST /v1/ingress/report` | Accept a node's report of the guest addresses it runs | TLS with **cluster mutual TLS**; node identity comes from the client certificate |
| `4242` | every node | SwarmKit remote API (gRPC) | Raft replication between managers, agent registration and control-API traffic | TLS with **cluster mutual TLS**; a joining node also presents the **join token** |
| `4245` | every node | Node broker (mTLS HTTP) | Relay `exec`, `attach`, `logs`, `files` and `health` from the manager to the node that runs the task | TLS with **cluster mutual TLS**; the manager's node certificate, never a caller's bearer token |
| `8080` | every node | Health and metrics HTTP | `GET /healthz`, `GET /metrics`, `GET /v1/tasks/health` | **Loopback only** (`127.0.0.1:8080` by default), **no authentication** — the bind address is the boundary |

The defaults above are what a stock `swarmd-firecracker` uses. Each has a flag;
see [Changing or disabling a listener](#changing-or-disabling-a-listener).

## `:4243` — ingress routing table

**Purpose.** The ingress mesh keeps published ports load-balanced on every
node. A manager computes the routing table (from the control API plus the task
reports below) and serves it; every node fetches it and programs its own load
balancer and resolver from the result. `GET /v1/ingress` returns that table as
a JSON document.

**Transport and authentication.** The server is started with
`tls.RequireAndVerifyClientCert` against the cluster CA, so only a holder of a
valid cluster node certificate can read the table. There is no bearer token, no
basic auth and no unauthenticated mode. A client that cannot present a node
certificate is rejected during the handshake.

**Private — not part of the v1 contract.** The response body is an internal
struct with no `schemaVersion` and no stability guarantee; it may gain, lose or
rename fields in any minor release. Do not poll it, do not parse it, and do not
expose the port outside the cluster network. It is a manager-to-node channel,
nothing else.

## `:4243` — `/v1/ingress/report`

**Purpose.** A task's guest address on the default bridge is allocated by the
node's daemon and never written back into the SwarmKit task store, so the
manager cannot read it from the control API. Each node therefore posts the
addresses of the tasks it runs (`{"tasks":[{"task_id":"…","ips":["…"]}]}`) every
few seconds; the manager keeps the latest report per node with a short TTL and
joins it with the control API view to answer service DNS.

**Transport and authentication.** Same mutual-TLS listener as the table, with a
stricter rule: the manager takes the reporting node's identity from the **client
certificate's common name**, not from the body. A node cannot report addresses
on another node's behalf, and a request without a peer certificate is answered
`401` before the body is read. Reports are capped at 1 MiB.

**Private — not part of the v1 contract.** The path, the body shape, the report
interval and the TTL are internal details of the mesh. They may change in any
minor release, and a build that speaks a different version of this endpoint
simply loses service DNS and load balancing until the nodes match versions —
which is why an operator upgrades the whole cluster together.

## `:4242` — SwarmKit remote API

**Purpose.** The SwarmKit remote API. Managers use it for Raft replication and
for the control API that `swarmcracker` and `swarmctl` reach over the local
control socket; workers use it to register and to receive task assignments.
Operators also point the CLI's cluster bootstrap commands
(`swarmcracker cluster init --advertise-addr host:4242`,
`swarmcracker cluster join host:4242 --token …`) at it.

**Transport and authentication.** TLS with the cluster CA. Node-to-node and
CLI-to-manager traffic is mutually authenticated with the cluster's certificates;
a node joining a cluster additionally proves possession of the **join token**
(`SWMTKN-…`), which is the only credential that is not a certificate. There is
no unauthenticated or plaintext mode.

**Private — not part of the v1 contract.** This is SwarmKit's protocol, not
SwarmCracker's: its messages are defined by the upstream `moby/swarmkit` API and
change with it. Nothing about the port, the wire format or the RPC set is
covered by the v1 contract, and the `x-swarmcracker-version` metadata the CLI
adds is an internal hint, not a negotiated version. Use the v1 HTTP API for
programmatic access; use the CLI for cluster lifecycle.

## `:4245` — node broker

**Purpose.** The node broker is how the manager relays task-scoped operations to
the node that actually runs the task: exec, attach, log streaming, file
transfer and per-task health. The manager always dials the fixed port `4245` and
ignores any port in a node's advertised address, so both sides must agree on it.

**Transport and authentication.** TLS with the **cluster's mutual-TLS identity**.
The manager presents its node certificate; the caller's bearer token is never
forwarded and never accepted by a node broker. A node broker therefore has no
concept of API roles or scopes — the manager has already authorized the request
before it relays it.

**Private — not part of the v1 contract.** The broker's routes mirror the v1
task routes by convention, not by contract; they take a task id and no resource
envelope. Their paths, their payloads and the fixed port may change in any
minor. Do not dial a node broker directly: the supported way to reach these
capabilities is `/v1/tasks/{id}/exec`, `/attach`, `/logs`, `/files` and
`?health=true` on the [v1 API](/reference/api/#task-exec-attach-logs-files-and-health).

## `:8080` — node health and metrics

**Purpose.** A small HTTP server on every node, separate from the API, that
serves:

| Path | Contents |
|------|----------|
| `GET /healthz` | node health — KVM, bridge and Firecracker checks; `200` when all pass, `503` otherwise |
| `GET /metrics` | Prometheus metrics for the node |
| `GET /v1/tasks/health` | live healthcheck state of every task the node hosts, which `service ps` / `task ls` fan out to |

**Transport and authentication.** Plain HTTP on `127.0.0.1:8080` by default, with
**no authentication** and no TLS. The bind address is the entire boundary: the
server trusts whoever can reach the socket. It is rate limited and every request
has a five-second timeout, but those are availability guards, not access
control.

**Private — not part of the v1 contract.** The paths, the JSON shape and the
metric names are internal. `/healthz` and `/metrics` are for a local monitor or
a sidecar scraper; `/v1/tasks/health` is read by the lifecycle code, not by
clients — its payload can carry whatever a check printed. Nothing here is
covered by the v1 contract and any of it may change in a minor release.

Binding this port on a cluster network is therefore an operational decision:
it exposes the same information as `/healthz` and `/metrics`, unauthenticated,
to everyone who can reach the address. Keep the default loopback bind unless a
scraper on another host forces otherwise, and firewall it if you do.

## Other private surfaces (Unix sockets)

These are not TCP ports but they follow the same rule: private, internal, and
free to change.

| Socket | Purpose |
|--------|---------|
| `/var/run/swarmkit/swarm.sock` | SwarmKit control API; the CLI's local transport (`pkg/client`) |
| `<socket-dir>/<task-id>.sock` (default `/var/run/firecracker`) | the Firecracker API socket for one microVM |
| `<socket-dir>/<task-id>.console.sock` | the guest console, used by `vm attach` without `-H` |
| `<state-dir>/api.sock` | **not private** — this one is the trusted arm of the v1 API, listed below |

## What *is* public

Exactly one machine surface is versioned and supported: the **v1 HTTP API**.

| Listener | Address | Authentication |
|----------|---------|----------------|
| TLS/TCP | `127.0.0.1:4244` by default (`--api-listen`) | `Authorization: Bearer <token>` on every route |
| Unix socket | `<state-dir>/api.sock` | none — the socket's file mode (`0600`) is the credential |

`api/openapi.yaml` is the normative schema for every route, DTO, header and
error code, and every response carries `X-SwarmCracker-Api-Version: 1`. See the
[v1 API reference](/reference/api/#public-http-api-v1) for the routes, the
scopes, the error codes and the CLI mapping, and the
[architecture overview](/architecture/) for how the internal listeners fit into
the cluster.

## Changing or disabling a listener

Every private listener has a flag on `swarmd-firecracker`; the defaults are what
this page documents.

| Listener | Flag | Default | To disable |
|----------|------|---------|------------|
| `:4243` ingress mesh | `--ingress-mesh`, `--ingress-port` | enabled, `4243` | `--ingress-mesh=false` |
| `:4242` SwarmKit remote API | `--listen-remote-api` | `0.0.0.0:4242` | bind `127.0.0.1:4242` on worker-only nodes |
| `:4245` node broker | `--broker-listen` | `0.0.0.0:4245` | empty string disables it (the manager's relayed routes then answer `501`) |
| `:8080` health/metrics | `--health-addr` | `127.0.0.1:8080` | rebind to loopback, or firewall it |

Changing a port is a cluster-wide decision: the manager dials the ingress table
and the node broker on fixed ports, so a node that moved one is not reachable
for that capability until the manager is configured to match.
