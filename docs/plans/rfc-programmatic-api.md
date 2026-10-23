# RFC — Programmatic API surface: a stable machine contract for automation and integration

- **Status:** draft — decisions D1–D7 open
- **Issue:** [#102](https://github.com/restuhaqza/SwarmCracker/issues/102) (the live tracker)
- **Owner:** Restu Muzakir
- **Related:** [#21](https://github.com/restuhaqza/SwarmCracker/issues/21) (TUI, needs a read model), [#89](https://github.com/restuhaqza/SwarmCracker/issues/89) (log aggregation), [#45](https://github.com/restuhaqza/SwarmCracker/issues/45) (observability), `docs/plans/backlog-program.md` slice 13 (`pkg/control` extraction)

> Internal proposal (not published to docs.swarmcracker.com). It is the plan of
> record for issue #102; the issue is the live tracker.

---

## TL;DR

SwarmCracker is operated today by a human with `sudo` on a manager node. It is
consumed by machines through nothing in particular: integrations either SSH into
a manager and scrape `--format json`, or import the vendored SwarmKit gRPC package
and read root-owned certificates off disk.

This RFC proposes a **supported programmatic surface** in four increments:

1. freeze a machine contract for the CLI (JSON envelope, error codes, exit codes);
2. add a `pkg/client` Go library that the CLI itself uses;
3. expose an authenticated remote API so a non-root client off-node can read and write;
4. broker node-local operations (`exec`, `logs`, `cp`, `snapshot`) through the same API.

The contract is the deliverable. The transport is a decision (see §9).

---

## 1. Who needs this

| Consumer | What it does today | What it needs |
|---|---|---|
| CI/CD pipeline | `ssh mgr 'sudo swarmcracker service update …'`, greps table output | non-root remote call, `--wait`, meaningful exit codes |
| GitOps / reconciler | polls `service ls --format json`, diffs text | stable resource IDs, watch/cursor, optimistic concurrency |
| Dashboard / TUI (#21) | shells out to 4–5 commands and joins IDs by hand | one read model, one call, streaming updates |
| Test harness | `vm exec` / `vm cp` as root on the node running the VM | node-local ops reachable from a CI runner |
| Non-Go SDK (Python/TS/…) | nothing — SwarmKit protobufs are unreasonable | HTTP+JSON, or a generated client |
| Monitoring | scrapes `/metrics` | already fine; only the labels need a contract |

---

## 2. What exists today (verified inventory)

| # | Surface | Where | Auth | Integration-ready? |
|---|---|---|---|---|
| 1 | CLI, 13 command groups | `cmd/swarmcracker` | local root | partial (§2.1–2.3) |
| 2 | SwarmKit gRPC control API | `/var/run/swarmkit/swarm.sock` | mTLS with node certs, root-only | no |
| 3 | SwarmKit remote API | `--listen-remote-api`, default `0.0.0.0:4242` (`cmd/swarmd-firecracker/flags.go:25-29`) | cluster CA | no: no client path, no issuance, no docs |
| 4 | Daemon HTTP | `127.0.0.1:8080` → `/healthz`, `/metrics`, `/v1/tasks/health` (`cmd/swarmd-firecracker/run.go:112-134`) | none; loopback by default; 10 req/s burst 20 (`run.go:122`) | partial |
| 5 | Ingress mTLS HTTP | `:4243` → `/v1/ingress`, `/v1/ingress/report` (`pkg/ingress/server.go:23-70`, `cmd/swarmd-firecracker/ingress.go:53-56`) | node client certs | no: internal, absent from `reference/api.md` |
| 6 | Guest agent wire protocol | vsock, 4-byte-length-prefixed JSON, version `"1"` (`pkg/agent/protocol.go:21-33`) | node-local socket access | no |
| 7 | Per-VM Firecracker API | `<socket-dir>/<task-id>.sock` (`reference/api.md:273-285`) | root on the node | no |
| 8 | `swarmctl` debug client | `cmd/swarmctl/main.go:1` ("for testing") | same as #2 | no: not in `make all` (`Makefile:35`) or the release workflow |
| 9 | Prometheus metrics | `/metrics` | none | yes, monitoring only |

### 2.1 The "machine-readable" claim is partial

- **17** `--format` flags are registered, one per command, none global:
  `cmd_service.go:70,99,137`, `cmd_task.go:79,113`, `cmd_node.go:70,98`,
  `cmd_network.go:73,120,157`, `cmd_network_admin.go:169,253`,
  `cmd_config.go:99`, `cmd_secret.go:115`, `cmd_list.go:45`,
  `cmd_metrics.go:45`, `cmd_health.go:45`. There is no `-o`/`--output` anywhere.
- Defaults are inconsistent: 4 commands default to `json`, 13 to `table`.
- **Empty results behave differently per command.** Five commands that accept
  `--format json` print prose on stdout *before* reaching the JSON branch, so a
  parser breaks on a healthy empty cluster: `service ls`
  (`cmd_service.go:799-801`), `service ps` (`cmd_service.go:1233-1235`),
  `task ls` (`cmd_task.go:147-149`), `node ls` (`cmd_node.go:301-303`),
  `volume ls` (`volume.go:155-157`). `secret ls`, `config ls`, `network ls` and
  `vm list` put the JSON branch first — so the same situation returns an empty
  array in one command and prose in another.
- `service ls` / `service inspect` JSON is the **raw SwarmKit struct** through
  `encoding/json` (`cmd/swarmcracker/redact.go:22-38`). Field names, oneof
  shapes and enum encoding are owned by generated dependency code
  (`github.com/moby/swarmkit/v2/api`, `objects.pb.go:147-157`), not by a
  contract this repo controls.
- `task ls --format json` silently adds a derived `live_health` field from the
  node fan-out (`cmd/swarmcracker/task_health.go:328`) — machine-visible, undocumented.
- Errors are free-form text on stderr. Nothing is parseable.

### 2.2 Exit codes carry no information

`cliExitCode` returns `0` or `1` (`cmd_vm_exec.go:30-39`). Only `vm exec`
propagates a guest status. "not found", "already exists", "permission denied",
"cluster unreachable" and "bug" are all `1`.

### 2.3 Everything assumes root on a manager

- The CLI loads the node's own certificates and dials a **Unix socket**
  (`token_helper.go:23-76`: `swarm-node.crt` / `swarm-node.key` /
  `swarm-root-ca.crt` under `/var/lib/swarmkit/certificates`, then
  `net.Dial("unix", socketPath)`). The same block is repeated in
  `cmd_service.go:638`, `cmd_node.go:236`, `cmd_task.go:284`.
- `SWARM_SOCKET` / `SWARM_STATE_DIR` only relocate files; they do not reach
  another host (`token_helper.go:23-30`).
- Node-local operations read the node's socket directory: `vm exec`
  (`cmd_vm_exec.go:44-54`), `vm cp`, `vm attach`, `vm logs`, `vm snapshot`.
- `InsecureSkipVerify: true` is set on the client TLS config
  (`token_helper.go:52`), justified by "no hostname on a Unix socket". Any
  remote client built on this path must not inherit it.
- The remote API listens on `0.0.0.0:4242` by default, but no command dials it,
  no certificate is issued for it, and it is documented only inside the
  disaster-recovery guide. Any cluster cert is cluster-admin.

### 2.4 No client library, no events

- No `pkg/client`. CLI logic lives in `package main`; there is no supported way
  to call SwarmCracker from another Go program except re-implementing the dial +
  TLS block above.
- No watch/stream except `vm logs --follow`, which is local.
- No pagination, no idempotency keys, no request IDs, no concurrency control on
  the CLI surface (`UpdateService` takes a version, but nothing exposes it as a
  precondition).

---

## 3. What breaks in practice

| # | Failure | Root cause |
|---|---|---|
| G1 | A pipeline's `jq` gets `No services found` and fails on a healthy empty cluster | §2.1 empty-list prose |
| G2 | A pipeline cannot tell "service missing" from "manager down" | §2.2 exit codes |
| G3 | A controller polls the CLI every N seconds and diffs text | no watch, no cursor |
| G4 | A developer cannot reach the cluster from a laptop without SSH + sudo | §2.3 |
| G5 | A CI runner cannot exec in a VM it just created | node-local only |
| G6 | A Python consumer must speak SwarmKit protobuf | no language-agnostic surface |
| G7 | The ingress report protocol is de facto public but undocumented | §2.1, table row 5 |
| G8 | Any JSON shape can change with a SwarmKit bump | no owned schema |

---

## 4. Requirements

`MUST` = contract; `SHOULD` = expected this program; `MAY` = later.

### A. Output contract

| ID | Level | Requirement |
|---|---|---|
| R1 | MUST | Every read command accepts `-o json` / `--format json` and emits exactly **one** JSON document on stdout — including empty results (`{"items":[]}`), never prose. |
| R2 | MUST | stdout carries only the payload; every diagnostic, warning and progress line goes to stderr. |
| R3 | MUST | Every document carries `schemaVersion` (integer) and `kind` (string). |
| R4 | MUST | One document per invocation. Streaming is opt-in and named (`watch`, `logs`). |
| R5 | MUST | In JSON mode, errors are `{"error":{"code","message","details"}}` on stderr with a closed code enum (§6.2). |
| R6 | SHOULD | `-q` returns bare identifiers, one per line, for shell use. |
| R7 | MUST | Every field name and type is documented and covered by a golden fixture. |
| R8 | MUST | Secrets, config values and registry credentials can never appear, enforced by type rather than by remembering to call a redactor (current approach: `cmd/swarmcracker/redact.go`, `pkg/redact`). |

### B. Exit codes

| ID | Level | Requirement |
|---|---|---|
| R9 | MUST | A documented exit-code table (§6.3). `vm exec` keeps guest-status passthrough. |
| R10 | MUST | A machine can distinguish transport failure, auth failure, not-found, conflict and partial failure without parsing text. |

### C. Access

| ID | Level | Requirement |
|---|---|---|
| R11 | MUST | A non-root client on another host can read cluster state with an explicit credential file — no SSH, no root, no node certificate. |
| R12 | MUST | Mutating calls require an explicit write role; a read-only credential cannot mutate, enforced server-side and tested. |
| R13 | SHOULD | Credential issue / list / revoke are documented commands with tests. |
| R14 | MUST | TLS is verified by default on every remote path; no `InsecureSkipVerify` outside tests. |
| R15 | SHOULD | Every mutating call emits an audit record (identity, action, target, result). |

### D. Transport and topology

| ID | Level | Requirement |
|---|---|---|
| R16 | MUST | One documented endpoint per role: the manager API, and a node-local broker. |
| R17 | SHOULD | The read surface is language-agnostic (HTTP+JSON). gRPC may remain an advanced/internal path. |
| R18 | MUST | Node-local operations (`exec`, `cp`, `logs`, `attach`, `snapshot`) are reachable from off-node through a documented, authenticated path. |
| R19 | MUST | Every daemon endpoint is either documented + versioned or explicitly declared private and not relied on across minors. |

### E. State and concurrency

| ID | Level | Requirement |
|---|---|---|
| R20 | MUST | Stable resource identity and deterministic ordering (sorted by ID) in every list response. |
| R21 | MUST | Pagination (`limit`, `cursor`) with a documented default and a hard maximum. |
| R22 | SHOULD | Watch/resume via a monotonic cursor: no lost events, no full re-list. |
| R23 | SHOULD | Idempotent create (client token or name-based upsert). |
| R24 | SHOULD | Conditional update with a typed conflict error (SwarmKit `SpecVersion` surfaced as a precondition). |

### F. Compatibility

| ID | Level | Requirement |
|---|---|---|
| R25 | MUST | Schema semver. Additive only within a major. A breaking change needs a new major plus a deprecation window of ≥2 minor releases. |
| R26 | MUST | Version negotiation: an unknown major is refused with a typed error, not a parse failure. |
| R27 | MUST | Today's `--format json` shapes keep working until deprecated per R25; no silent breakage of existing scripts. |

### G. Developer experience

| ID | Level | Requirement |
|---|---|---|
| R28 | MUST | `pkg/client` (Go) that the CLI itself uses. One implementation, not two. |
| R29 | SHOULD | A published schema (JSON Schema or OpenAPI) and a conformance suite runnable against a live cluster. |
| R30 | SHOULD | At least one worked non-Go example in docs, exercised in CI. |
| R31 | MAY | A container image with the CLI + CA bundle for CI runners. |

---

## 5. Proposed design

### Option A (recommended): resource-oriented HTTP/JSON API + `pkg/client`

```
                 ┌──────────────────────────────┐
  CLI / SDK ────►│  manager: /v1/* (mTLS)       │──► SwarmKit control socket
  curl/python    │  roles: reader|operator|admin│    (write path of record)
  CI runner      └──────────┬───────────────────┘
                            │ node mTLS channel
                            ▼
                 ┌──────────────────────────────┐
                 │ node broker: /v1/nodes/{id}/ │──► vsock guest agent,
                 │ exec|logs|cp|snapshot        │    Firecracker socket
                 └──────────────────────────────┘
```

Endpoints (shape, not final):

| Method | Path | Notes |
|---|---|---|
| GET | `/v1/services`, `/v1/services/{id}` | list/inspect, paginated |
| PUT / DELETE | `/v1/services/{id}` | requires `specVersion` precondition (R24) |
| GET | `/v1/tasks`, `/v1/tasks/{id}` | includes live health, as one call |
| GET | `/v1/nodes`, `/v1/networks`, `/v1/secrets`, `/v1/configs` | secret **metadata only**, never values |
| GET | `/v1/events` | SSE, resumable cursor |
| POST | `/v1/nodes/{id}/exec`, `…/copy`, `…/snapshot` | broker; streams stdout/stderr |
| GET | `/v1/nodes/{id}/logs` | broker; supports follow |

Why: one implementation serves CLI, SDK and third parties; roles are enforceable
at one place; SwarmKit stays the source of truth; no protobuf dependency for
consumers.

### Option B (cheaper): harden and document the SwarmKit remote API

Use `--listen-remote-api`, publish the protos, add `auth issue` and
`--host/--cert/--key/--ca` to the CLI.

Trade-off: any cluster certificate is cluster-admin, so read/write separation
needs a proxy anyway; Go/protobuf-centric; oneof and enum encoding stay awkward.

### Option C (minimum): CLI contract only

Freeze JSON + exit codes + `pkg/client`. Cheapest, ships in one or two slices,
but closes none of G4–G6.

**Recommendation:** C as phase 0–1, A after. B only if a Go-only consumer is the
priority and the admin-cert caveat is acceptable.

### Phases

| Phase | Deliverable | Closes |
|---|---|---|
| 0 | Contract freeze: `pkg/output` envelope + error type, global `-o`, empty-list fix, exit-code table, `pkg/client`, golden fixtures | G1–G3, G8 |
| 1 | Remote read: `--host/--cert/--key/--ca`, verified TLS, reader credential, `SWARMCRACKER_HOST` | G4 |
| 2 | Remote write + `--wait`/`--timeout`, idempotency, conditional update, roles, audit | G2, G3 |
| 3 | Node broker for `exec`/`cp`/`logs`/`attach`/`snapshot` | G5 |
| 4 | Events/watch, generated schema, non-Go example, CI image | G6, G7 |

Phase 0 touches every command, so it lands command-group by command-group
(`service`, `task`, `node`, …), each with golden tests — not as one big diff.

---

## 6. Contract details (phase 0, normative)

### 6.1 Envelope

```json
{
  "schemaVersion": 1,
  "kind": "ServiceList",
  "generatedAt": "2026-10-19T10:00:00Z",
  "items": [],
  "next": null
}
```

- `items` is `[]` (never `null`) when empty.
- Single-resource kinds (`Service`, `Task`, …) carry the object at the top level
  plus `schemaVersion`/`kind`, not inside `items`.
- `next` is a cursor, `null` at the end.

### 6.2 Error codes

| `code` | Meaning |
|---|---|
| `usage` | bad flag/argument |
| `not_found` | no such resource (after name/prefix resolution) |
| `ambiguous` | prefix matched more than one resource |
| `conflict` | version precondition failed / already exists |
| `unauthorized` | missing or bad credential |
| `forbidden` | credential lacks the role |
| `unreachable` | transport failure or timeout |
| `partial` | some nodes answered, some did not |
| `internal` | bug; includes a request ID |

### 6.3 Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 2 | usage error |
| 3 | not found / ambiguous |
| 4 | conflict |
| 5 | authn/authz |
| 6 | unreachable / timeout |
| 7 | partial failure |
| 1 | any other error |
| *guest* | `vm exec` only: the guest's own status, unchanged |

### 6.4 Compatibility

- `schemaVersion` increments only on a breaking change; additive fields are
  allowed silently (consumers must ignore unknown fields — stated in docs).
- `--format json` (legacy shapes) is frozen at its current output and marked
  deprecated when `-o json` ships; removal no earlier than N+2 minors.

---

## 7. Non-goals

- Kubernetes API compatibility (existing roadmap non-goal).
- Exposing this to the public internet or multi-tenant isolation.
- L7/TLS ingress — L4 stays the core (ADR-006).
- Replacing SwarmKit types with a new domain model.
- Changing the guest-agent protocol (v1 stays; it is private, R19).

---

## 8. Acceptance criteria

1. `swarmcracker service ls -o json` on an empty cluster prints
   `{"schemaVersion":1,"kind":"ServiceList","items":[]}` and exits 0; a golden
   fixture asserts it for **every** list command.
2. A `set -e` script distinguishes: `service inspect nope` → 3, stopped daemon →
   6, read-only credential creating a service → 5, stale `specVersion` → 4.
3. `cmd/swarmcracker` contains no direct `api.ControlClient` call outside
   `pkg/client` (enforced by a lint check or test).
4. From a laptop, with a `reader` credential and no root: `swarmcracker -H
   https://mgr:4243 service ls` works.
5. The same credential cannot `service create` (403/`forbidden`) — server-side test.
6. `swarmcracker -H … vm exec web -- true` from a CI runner returns 0; `… -- false`
   returns the guest status (passthrough preserved).
7. Redaction property test: a service carrying `PullOptions.AuthConfig` never
   emits the credential in any JSON document.
8. No `InsecureSkipVerify` outside `_test.go`.
9. A conformance suite (`make api-check`) runs green against the three-node lab
   in CI.

---

## 9. Open decisions (need a call before phase 0 lands)

| # | Decision | Options | Leaning |
|---|---|---|---|
| D1 | Transport | A (HTTP/JSON gateway) / B (SwarmKit remote API) / C-first | C now, A next |
| D2 | Credential model | mTLS cert with role / signed bearer token | certs — no new CA |
| D3 | Node broker topology | manager-proxied / client→node direct | proxied: one endpoint, one firewall rule |
| D4 | Schema ownership | hand-written structs / generated from protos | hand-written; protos leak oneofs |
| D5 | Legacy JSON lifetime | N+2 / N+4 / forever | N+2 |
| D6 | Write scope | every SwarmKit RPC / curated subset | curated: services, scale, networks, secrets, nodes |
| D7 | Volumes & snapshots | first-class resources / node-local only | node-local; revisit with the broker |

---

## 10. Sizing (rough)

| Phase | Size | Slices |
|---|---|---|
| 0 | M | per command group + `pkg/client` + fixtures |
| 1 | M | dial path, TLS, credential issuance |
| 2 | M–L | roles, wait, idempotency, audit |
| 3 | L | broker protocol + streaming |
| 4 | M | watch, schema, examples |

---

## 11. Evidence index

| Claim | Evidence |
|---|---|
| Per-command `--format`, no global output flag | 17 registrations listed in §2.1; `cmd/swarmcracker/main.go:36-42` has no output flag |
| Empty list prints prose | five JSON-mode sites listed in §2.1; the other commands put JSON first |
| Raw SwarmKit structs in JSON | `cmd/swarmcracker/redact.go:22-38`; `github.com/moby/swarmkit/v2/api` `objects.pb.go:147-157` |
| Exit codes 0/1 only | `cmd/swarmcracker/cmd_vm_exec.go:30-39` |
| Local socket + node certs | `cmd/swarmcracker/token_helper.go:23-76` |
| `InsecureSkipVerify` | `cmd/swarmcracker/token_helper.go:52` |
| Remote API default exposure | `cmd/swarmd-firecracker/flags.go:25-29` |
| Daemon endpoints + rate limit | `cmd/swarmd-firecracker/run.go:112-134` (rate limit at `:122`) |
| Ingress mTLS API | `pkg/ingress/server.go:23-70`, `cmd/swarmd-firecracker/ingress.go:53-56` |
| Guest agent protocol v1 | `pkg/agent/protocol.go:21-33` |
| `swarmctl` is a test client, not shipped | `cmd/swarmctl/main.go:1`; `Makefile:35`; `.github/workflows/release.yml` |
| Ingress API absent from reference docs | no `ingress` match in `docs-site/src/content/docs/reference/api.md` |
