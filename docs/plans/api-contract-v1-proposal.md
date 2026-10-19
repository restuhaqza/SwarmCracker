# API contract v1 — language-agnostic programmatic surface

- **Status:** revised after independent review (`docs/plans/api-contract-v1-review.md`)
- **Issue:** [#102](https://github.com/restuhaqza/SwarmCracker/issues/102)
- **Companion:** `docs/plans/rfc-programmatic-api.md` (requirements R1–R31)
- **Scope:** the contract (wire format, semantics, errors, auth), not the full
  implementation plan. Phases are sized, not scheduled.

> This revision resolves the review's P0 findings and the P1 findings that
> change the contract. The main changes are: no full-replace `PUT`; secrets,
> configs and networks no longer pretend to support updates SwarmKit cannot do;
> the error/`kind` schema is closed and internally consistent; health, exec,
> files and events are limited to what the guest agent and Watch API actually
> provide; and v1 is explicitly single-manager, because tokens, idempotency and
> audit state are not replicated yet.

---

## 0. Revision summary

| Area | Previous proposal | This revision |
| --- | --- | --- |
| Update semantics | `PUT` full replace + `PATCH` | **No `PUT`.** `POST` create, `PATCH` merge/overlay, `DELETE`. Unmodeled SwarmKit fields and `registryAuth` are preserved. |
| Secrets / configs | `POST/PUT/DELETE`, data updates | `POST`, **labels-only `PATCH`**, `DELETE`. Rotation is create-new + repoint. |
| Networks | `POST/PUT/PATCH/DELETE` | **`POST` / `DELETE` only**; driver, IPAM and labels are immutable in v1. |
| Errors / kinds | 428 used but not listed; 207 two bodies; `Cluster`/`AuditEntry` missing; `Warning` kind | 428 added; 207 is a success document with `partial`; kinds enumerated and open; `warnings` is an array, not a kind. |
| Health | Included in phase-1 list reads | **Phase-3 capability** via the broker. Manager does not dial node loopback. |
| Exec / files | `user`, resize, signal, directory tar | Removed. `stdin` requires `tty`; TTY is stdin/stdout/stderr/exit only; regular files only. |
| Events | `ready` cursor, `changed`, `actor` guaranteed | `ready` cursor comes from a store view; `changed` is live-only; `actor` is optional. |
| Auth / HA | Any manager + VIP; tokens local | **Single-manager v1.** Tokens, idempotency and audit are manager-local; HA is phase 2. `runner` tokens require task selectors. |
| Config values | Readable with `configs:read` | **`configs:read-values`** scope. `configs:read` returns metadata only. R8 holds for default tokens; `configs:read-values` is an explicit operator grant (see open question 1). |
| Ports | 4244 public, 4245 broker, 4243 ingress | Same, plus: 4242 remote API goes **loopback by default** in phase 1; 4244 gets a leaf certificate with SANs. |
| Snapshots | Phase 3, cluster resource | **Phase 4, experimental**, disabled by default; fields aligned with `pkg/snapshot`. |
| Phase 0 | Claimed G3 | Phase 0 closes G1, G2, G8; phase 2 closes G3. Phase 3 is XL. |

---

## 1. Goals and non-goals

**Goal.** A Python, Node, Go or shell consumer can drive SwarmCracker over a
documented, versioned contract without root, without SSH, without protobuf, and
without scraping human output.

**Non-goals (v1).**

- Multi-manager high availability for the public API. v1 is single-manager;
  tokens, idempotency keys and audit records are manager-local.
- Cluster lifecycle (init/join/leave) over the API.
- Image build, registry push, volume drivers, host filesystem management.
- Multi-tenant isolation, public-internet exposure, L7 ingress.
- Browser JavaScript clients. The TS SDK targets Node 18+; a browser needs a
  token broker and CORS, which are out of scope.
- Replacing SwarmKit as the source of truth.
- A new domain model. The API is a **view** over SwarmKit objects, owned by this
  repo so it can be frozen.

---

## 2. Design axioms

1. **HTTP/JSON is the only public wire format.** No protobuf, no gRPC on the
   public surface. Protobuf oneofs and integer enums are not a contract.
2. **The CLI is a client of the same contract.** Local root access uses the same
   HTTP API over a Unix socket. There is no private CLI path.
3. **Resource-oriented.** Nouns in the path; the rare verb is an action
   sub-resource (`/rollback`). No RPC tunnel with a verb in the body.
4. **Spec/status split.** `spec` is writable; `status` is observed and
   read-only. This is the single biggest change from the raw SwarmKit JSON.
5. **One response answers the question.** Service status is in the read model;
   task health is an explicit, capability-gated add-on (phase 3).
6. **Errors are data.** Every failure has a stable `code`, typed `details`, and a
   `requestId`. Message text is not stable.
7. **Streaming is named and opt-in.** `events`, `logs`, `exec` each declare their
   content type. Everything else is one document.
8. **Additive only within v1.** Clients ignore unknown fields, unknown enum
   values and unknown `kind` values. A breaking change is `/v2` with an overlap
   window.
9. **Absent means absent.** Optional fields are omitted, never `null`. Arrays are
   `[]`; maps are `{}`. Exceptions: `next: null` at the end of a page, and
   `null` inside a JSON Merge Patch, where it means "delete/reset".
10. **The contract is the artifact.** OpenAPI 3.1 is normative and tested; every
    Go DTO is validated against it. SDKs are generated from it, not
    hand-maintained per language.

---

## 3. One document shape for HTTP and CLI

The CLI's `-o json` output is byte-identical to the HTTP response body. This is
what makes `pkg/client`, the OpenAPI schema, the golden fixtures, and every SDK
share one definition — and it lets a CI log be replayed as a fixture.

```json
{
  "schemaVersion": 1,
  "kind": "ServiceList",
  "items": [],
  "next": null
}
```

| Field | Rule |
| --- | --- |
| `schemaVersion` | Integer. `1` for all of v1. Increments only on a breaking payload change. |
| `kind` | Open within v1: clients must tolerate unknown values. Known values are listed below. |
| `items` | Always present on `*List` kinds, never `null`. |
| `next` | Opaque cursor or `null`. Always present on list kinds. |
| `warnings` | Optional array of `{code, message, details}`. Warnings are **not** documents and are not written to stderr. |
| `partial` | Present only when an aggregate read could not reach some nodes (§8.4). |
| `dryRun` | Present and `true` only when `?dryRun=true` was requested. |

**Known `kind` values (v1):**

`Service`, `ServiceList`, `Task`, `TaskList`, `Node`, `NodeList`, `Network`,
`NetworkList`, `Config`, `ConfigList`, `Secret`, `SecretList`, `Snapshot`,
`SnapshotList`, `Cluster`, `Event`, `AuditEntry`, `AuditList`, `VersionInfo`,
`Error`.

`Warning` is **not** a kind. A warning is an element of `warnings[]`. A stream
frame uses `type`, not `kind`; frames are described in §8.3.

A single-resource document carries the resource fields at the top level:

```json
{
  "schemaVersion": 1,
  "kind": "Service",
  "id": "q1w2e3r4t5y6u7i8o9p0asdfg",
  "name": "web",
  "labels": {"team": "payments"},
  "revision": "17",
  "createdAt": "2026-10-19T10:00:00Z",
  "updatedAt": "2026-10-19T10:04:12Z",
  "spec": { "...": "..." },
  "status": { "...": "..." }
}
```

### 3.1 Three version axes

These are independent and must not be conflated:

| Axis | Where | Current | Changes when |
| --- | --- | --- | --- |
| gRPC control protocol | `pkg/apiversion.Current` | `"1"` | The internal manager/CLI gRPC protocol breaks. |
| Public HTTP API | `/v1`, `X-SwarmCracker-Api-Version: 1` | `1` | The public route or semantics break. |
| Document schema | `schemaVersion` | `1` | The payload shape breaks. |

A public breaking change increments **both** the HTTP major and
`schemaVersion`. The gRPC version may move independently. `GET /v1/version`
reports all three so a client can log the exact combination.

R25 asks for schema semver. R3 asks for an integer `schemaVersion`. This
contract satisfies both by treating **the API major as the semver major**
(`/v1`) and the integer as the payload major. A breaking payload change
increments both; an additive field does not.

### 3.2 Version negotiation

- Path prefix `/v1`.
- Request: `Accept: application/vnd.swarmcracker.v1+json` (plain
  `application/json` is also accepted).
- Response: `Content-Type: application/vnd.swarmcracker.v1+json` and
  `X-SwarmCracker-Api-Version: 1`.
- A client asking for an unknown major gets `406` with
  `{"code":"unsupported_version","details":{"supported":["v1"]}}` — a typed
  refusal, not a parse failure (R26).
- `GET /v1/version` returns server capabilities so clients feature-detect
  instead of version-sniffing:

```json
{
  "schemaVersion": 1,
  "kind": "VersionInfo",
  "apiVersions": ["v1"],
  "serverVersion": "v0.12.0",
  "grpcProtocolVersion": "1",
  "clusterId": "…",
  "capabilities": ["read", "write", "events", "exec", "files", "snapshots", "health"]
}
```

`capabilities` describes **server features, not token grants**. A `reader` token
sees `write` in the list but still receives `403 forbidden` on a write. A
capability that is absent means the endpoint/parameter returns
`501 not_implemented` with `details.capability`, not a parse error. The example
above is a maximal phase-4 server; a phase-1 server omits `exec`, `files`,
`snapshots` and `health`.

---

## 4. Errors, status codes and exit codes

### 4.1 Error document

```json
{
  "schemaVersion": 1,
  "kind": "Error",
  "error": {
    "code": "conflict",
    "message": "service web is at revision 19; request expected 17",
    "requestId": "req_9f1c2d3e4a5b6c7d",
    "retryable": false,
    "details": {
      "expectedRevision": "17",
      "currentRevision": "19"
    }
  }
}
```

`message` is for humans and may change. `code`, `retryable`, `requestId` and the
shape of `details` are the contract.

### 4.2 Closed code set

| `code` | HTTP | CLI exit | Retryable | Meaning / `details` |
| --- | --- | --- | --- | --- |
| `invalid` | 400 | 2 | no | Body/query invalid. `details.fields:[{path,reason}]` |
| `ambiguous` | 400 | 3 | no | Ref matched >1. `details.candidates:[{id,name}]` |
| `not_found` | 404 | 3 | no | No such resource, or unknown route (`details.resource:"route"`) |
| `unauthenticated` | 401 | 5 | no | Missing/bad/expired token (with `WWW-Authenticate: Bearer`) |
| `forbidden` | 403 | 5 | no | Token lacks scope or task selector. `details.requiredScope` |
| `conflict` | 409 | 4 | no | Name exists, idempotency key reused, or request in flight |
| `conflict` | 412 | 4 | no | `If-Match` failed. `details.expectedRevision`, `currentRevision` |
| `conflict` | 428 | 4 | no | `If-Match` required on an existing resource. `details.required:"If-Match"` |
| `cursor_expired` | 410 | 1 | no | Event log compacted. `details.action:"relist"` |
| `rate_limited` | 429 | 6 | yes | `Retry-After` header |
| `unavailable` | 503 | 6 | yes | Not the leader, node down, shutting down. `details.leader?` |
| `timeout` | 504 | 6 | yes | Server-side deadline exceeded |
| `unsupported_version` | 406 | 2 | no | Unknown API major. `details.supported` |
| `not_implemented` | 501 | 1 | no | Capability absent. `details.capability` |
| `internal` | 500 | 1 | yes | Bug. `requestId` is the support handle |

**Client-synthesized only** (never sent by a server):

| `code` | CLI exit | Meaning |
| --- | --- | --- |
| `usage` | 2 | Bad flag or argument, caught before any call |
| `unreachable` | 6 | DNS, TCP, TLS, or connection reset |

`405`, `413` and `415` are returned with `code: invalid` and a
`details.reason` of `method_not_allowed`, `payload_too_large` or
`unsupported_media_type`. They are not separate codes, so the SDK's typed
exception set does not grow with HTTP plumbing.

### 4.3 `207` is a success, not an error

A `207` response is a normal list document with a `partial` array and the tasks
that were readable. It is **not** an `Error` document. The CLI exits `7` when
`partial` is present. SDKs must treat `2xx` as success and inspect `partial`
explicitly — `response.ok` alone would hide the missing nodes.

### 4.4 Exit codes and the guest-code collision

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1 | any unclassified error (incl. `internal`, `cursor_expired`) |
| 2 | usage / unsupported version / malformed HTTP-level request |
| 3 | not found / ambiguous |
| 4 | conflict / precondition |
| 5 | unauthenticated / forbidden |
| 6 | unreachable / timeout / rate limited |
| 7 | partial |
| 8 | guest exited non-zero under `--exit-code=api` |

`vm exec` is the one command that must return the guest's status. That collides
with the table above: a guest exiting `3` is indistinguishable from "not found".

- **Default:** passthrough is preserved for compatibility.
- **`--exit-code=api`:** the CLI never returns a guest code. A non-zero guest
  exit becomes `8`; a guest killed by signal `N` becomes `128 + N`.
- **Machine mode (`-o ndjson` / `-o json`):** the result always carries the
  guest's `exitCode` (and `signal`, when known), so a consumer never infers from
  `$?`.

---

## 5. Resource model

### 5.1 Common fields

| Field | Type | Notes |
| --- | --- | --- |
| `id` | string | Opaque, currently 25-char base36 for SwarmKit objects; snapshots use `snap-<hex>`. Never parsed. |
| `name` | string | Unique per resource type. Immutable after create. |
| `labels` | map[string]string | User metadata; also the filter key. |
| `revision` | string | Opaque optimistic-concurrency token. For SwarmKit resources this is `Meta.Version.Index`. |
| `createdAt`, `updatedAt` | string | RFC 3339 with nanoseconds, UTC, `Z` suffix. |

Conventions:

- camelCase field names.
- Units live in the name: `memoryBytes`, `timeoutMs`, `vcpus`.
- Enums are lowercase strings and **open**: clients tolerate unknown values.
- Durations are integer milliseconds.
- Secrets are a distinct type: there is no `data` field on any serialized
  `Secret`.
- `spec` is the only writable subtree. `id`, `name` (after create), `revision`,
  timestamps and `status` are rejected in a write body with `invalid`.

### 5.2 Write model: create, overlay, delete

**There is no `PUT` in v1.** A full replace is unsafe with a curated DTO: the
server would have to decide what to do with SwarmKit fields the DTO does not
represent, and "reset to default" silently deletes them. The contract is:

| Call | Semantics |
| --- | --- |
| `POST /v1/services` | Create. `409 conflict` if the name exists. `Idempotency-Key` for retries. `If-None-Match: *` is accepted and means create-only. |
| `PATCH /v1/services/{ref}` | Update. `application/merge-patch+json`, `If-Match` required. Overlay only the mapped fields present in the patch. |
| `DELETE /v1/services/{ref}` | `204`. Missing → `404`; `?ignoreMissing=true` → `204`. |

**Overlay semantics.** The server reads the current SwarmKit object, deep-copies
it, applies only the fields present in the patch, and writes the merged object
back. Fields the DTO does not model are preserved. `registryAuth` is preserved
when omitted; `null` clears it; a value replaces it. This is the only way a
curated DTO can round-trip safely against `UpdateService`, which replaces the
whole `ServiceSpec` (`manager/controlapi/service.go:909-913`).

**Merge Patch rules.** `null` resets an optional scalar to its default, `null`
on a map key deletes the key, and `[]` clears an array. This is the explicit
exception to axiom 9. A patch that attempts to change an immutable field
(`name`, `spec.mode`, network driver/scope) returns `invalid`.

**Declarative apply.** A reconciler does `GET`, merges its desired fields into
the current document, then `PATCH` with `If-Match`. A create path uses `POST`
with an idempotency key. No consumer needs a lossy `PUT`.

### 5.3 Service

```json
{
  "schemaVersion": 1,
  "kind": "Service",
  "id": "q1w2e3r4t5y6u7i8o9p0asdfg",
  "name": "web",
  "labels": {"team": "payments"},
  "revision": "17",
  "createdAt": "2026-10-19T10:00:00Z",
  "updatedAt": "2026-10-19T10:04:12Z",
  "spec": {
    "mode": "replicated",
    "replicas": 3,
    "task": {
      "image": "nginx:1.27-alpine",
      "command": ["nginx"],
      "args": ["-g", "daemon off;"],
      "env": {"APP_ENV": "prod"},
      "user": "101:101",
      "workdir": "/usr/share/nginx/html",
      "vcpus": 1,
      "memoryBytes": 536870912,
      "ports": [
        {"target": 80, "published": 8080, "protocol": "tcp", "mode": "ingress"}
      ],
      "mounts": [
        {"type": "volume", "source": "webdata", "target": "/data", "readOnly": false}
      ],
      "networks": [{"id": "n1…", "name": "app", "aliases": ["web"]}],
      "restartPolicy": {"condition": "any", "delayMs": 5000, "maxAttempts": 0},
      "healthcheck": {
        "test": ["CMD", "curl", "-f", "http://localhost/"],
        "intervalMs": 10000,
        "timeoutMs": 3000,
        "retries": 3,
        "startPeriodMs": 5000
      }
    },
    "update": {"parallelism": 1, "delayMs": 10000, "failureAction": "pause",
               "order": "stop-first", "monitorMs": 30000, "maxFailureRatio": 0},
    "rollback": {"parallelism": 1, "failureAction": "pause", "order": "stop-first"},
    "placement": {"constraints": ["node.labels.zone==a"]}
  },
  "status": {
    "desiredTasks": 3,
    "runningTasks": 3,
    "tasksInTransition": 0,
    "converged": true,
    "message": "",
    "endpoint": {
      "ports": [{"target": 80, "published": 8080, "protocol": "tcp"}]
    }
  }
}
```

Notes:

- `spec.replicas` is present only for `mode: replicated`; global modes omit it.
- `env` is a map, not SwarmKit's `["K=V"]` array. Duplicate keys are invalid.
- `mounts[].type` is `volume | bind | tmpfs`. Bind mounts require `admin`.
- `status.converged` is `runningTasks == desiredTasks && tasksInTransition == 0`.
  For `global` mode, `desiredTasks` is the observed eligible-node count; a
  service is converged when every eligible node has a running task and nothing
  is in transition. Jobs are out of scope for v1.
- Registry credentials are write-only: `registryAuth` on a write, never present
  on a read. This is enforced by separate request and response types, not by
  remembering to call `pkg/redact`.

### 5.4 Task

```json
{
  "schemaVersion": 1,
  "kind": "Task",
  "id": "t1…",
  "name": "web.1.abc123",
  "revision": "42",
  "serviceId": "q1…",
  "serviceName": "web",
  "nodeId": "n2…",
  "nodeName": "node-2",
  "slot": 1,
  "state": "running",
  "desiredState": "running",
  "createdAt": "2026-10-19T10:01:00Z",
  "updatedAt": "2026-10-19T10:01:04Z",
  "image": "nginx:1.27-alpine",
  "addresses": ["10.0.1.7"],
  "ports": [{"target": 80, "protocol": "tcp"}],
  "health": {
    "status": "healthy",
    "failingStreak": 0,
    "output": "ok",
    "command": "curl -f http://localhost/",
    "checkedAt": "2026-10-19T10:04:10Z"
  }
}
```

- `health.status` is `starting | healthy | failing | unhealthy`. It is omitted
  when the task declares no healthcheck.
- Health is **capability-gated**: `GET /v1/tasks/{ref}` includes it when the
  broker is available (phase 3). `?health=true` on a list requires the `health`
  capability and returns `501 not_implemented` otherwise. The manager never
  dials a node's loopback health port.
- `exitCode` is present only for `complete`/`failed`. A signal-terminated guest
  also carries `signal`; the exit code is `128 + signal` when known.
- `ports` is the task's view; `published` is a service-level concept and is not
  duplicated per task.

### 5.5 Node

```json
{
  "schemaVersion": 1,
  "kind": "Node",
  "id": "n2…",
  "hostname": "node-2",
  "revision": "9",
  "createdAt": "2026-10-18T08:00:00Z",
  "updatedAt": "2026-10-19T10:03:00Z",
  "role": "worker",
  "membership": "accepted",
  "availability": "active",
  "state": "ready",
  "address": "192.168.1.12",
  "labels": {"zone": "a"},
  "resources": {"cpus": 8, "memoryBytes": 17179869184}
}
```

`manager` is present only on managers and omitted on workers (no `null`).
`agentVersion` is optional and omitted when the executor has not reported an
`EngineDescription`. `state` is SwarmKit's node state.

### 5.6 Network, config, secret

- **Network:** `{id, name, revision, createdAt, driver, ipam:{subnet,gateway}, labels, attachable}`.
  There is no `scope` field (SwarmKit's network scope is a constant in this
  deployment) and **no update in v1**. Create and delete only; driver, IPAM and
  labels are immutable. A `PATCH` or `PUT` returns `405 invalid` with
  `details.reason: "method_not_allowed"`.
- **Config:** metadata `{id, name, revision, createdAt, updatedAt, labels}`.
  `data` is included only when the token has `configs:read-values`. That is a
  deliberate operator grant, not a default reader capability. If R8 is read as
  "config values must never appear even with a grant", configs must be
  write-only; that is open question 1. `data` is a UTF-8 string; binary configs
  are rejected on create with `400 invalid` and
  `details.reason: "binary_not_supported"`. There is no data update: `PATCH`
  accepts labels only.
- **Secret:** `{id, name, revision, createdAt, updatedAt, labels, driver}`.
  There is no `data` field on any read path, ever. Writes send `data` (base64)
  or an external `driver`, never both. `PATCH` accepts labels only. Rotation is
  create-new + repoint; the old object is deleted after the rollout.

### 5.7 Snapshot (phase 4, experimental)

```json
{
  "schemaVersion": 1,
  "kind": "Snapshot",
  "id": "snap-a1b2c3d4e5f67890",
  "taskId": "t1…",
  "serviceId": "q1…",
  "nodeId": "n2…",
  "createdAt": "2026-10-19T10:04:12Z",
  "sizeBytes": 268451840,
  "memoryMB": 256,
  "vcpuCount": 1,
  "rootfsPath": "/var/lib/firecracker/rootfs/…",
  "checksum": "sha256:…"
}
```

Snapshots are disabled by default. When `snapshot.enabled` is false, every
snapshot endpoint returns `501 not_implemented` with `details.capability:
"snapshots"`. `POST` may pause the VM briefly and uses a 30 s default timeout.
`restore` is experimental: it can start a Firecracker process that SwarmKit does
not track. The API documents this and does not claim it is a scheduler-managed
operation. Snapshots are listed oldest-first by `(createdAt, id)`; the CLI may
reverse for display.

### 5.8 References in URLs

`{ref}` accepts exact `id`, exact `name`, or a unique `id` prefix of at least 4
characters. Ambiguity returns `400 ambiguous` with candidates.

---

## 6. Collection semantics

### 6.1 Filters

| Query | Applies to | Repeatable | Semantics |
| --- | --- | --- | --- |
| `name` | all | yes | Exact name |
| `id` | all | yes | Exact ID |
| `label` | all | yes | `k` or `k=v`, AND across repeats |
| `service` | tasks | no | Service ID or name |
| `node` | tasks | no | Node ID or hostname |
| `state` | tasks, nodes | yes | Exact state, OR across repeats |
| `desiredState` | tasks | yes | Exact desired state |
| `role` | nodes | no | `manager` / `worker` |
| `membership` | nodes | no | `accepted` / `pending` / `rejected` |

Unknown query parameters are rejected with `invalid`. A typo'd filter that
silently widens a query is a correctness bug.

### 6.2 Pagination and ordering

- `limit` default `100`, maximum `1000`. Above the maximum is rejected
  (`invalid`), not clamped.
- `next` is an opaque cursor. Clients must not construct or parse it.
- **Order: `createdAt` ascending, then `id` ascending.** This amends R20
  ("sorted by ID"): random IDs mean a newly created object can sort *before*
  the cursor and be skipped forever. `createdAt` appends new objects at the end;
  `id` breaks ties. Updates never move an object because `createdAt` is
  immutable.
- The cursor binds **all** options that change item shape: filters, sort, and
  `health`. Reusing a cursor with a different option set returns `invalid`.
- Timestamps are RFC 3339 with nanoseconds. Ordering assumes managers have
  monotonic clocks. **Absent clock skew**, a page sequence neither duplicates
  nor skips objects; a clock step can violate that, so consumers that need
  exact change tracking use `/v1/events`.
- Lists are point-in-time per request. Concurrent updates are visible on later
  pages, but the page sequence stays stable for objects that keep their
  `createdAt`.
- Empty lists are `{"items":[]}` in every command and every mode. The legacy
  prose branch is removed from JSON mode (both `-o json` and `--format json`);
  table mode may still print `No services found`.

### 6.3 Partial results

A `207` response is a normal list document plus:

```json
{
  "schemaVersion": 1,
  "kind": "TaskList",
  "items": ["…"],
  "next": null,
  "partial": [
    {
      "nodeId": "n3…",
      "nodeName": "node-3",
      "error": {
        "code": "unavailable",
        "message": "node did not answer within 2000ms",
        "requestId": "req_…",
        "retryable": true,
        "details": {"nodeId": "n3…"}
      }
    }
  ]
}
```

Each `partial[].error` is a complete `Error.error` object, including
`requestId`, `retryable` and typed `details`. A single-task read that cannot
reach its node is `503 unavailable` with `details.nodeId` — "I can't see it" is
not "it isn't there".

---

## 7. Concurrency, idempotency, apply

### 7.1 Optimistic concurrency

- `GET /v1/services/web` returns `ETag: "17"`, where `17` is
  `revision = Meta.Version.Index`.
- Every `PATCH` that changes an existing resource requires `If-Match: "17"`.
- Missing `If-Match` on an existing resource → `428` with
  `code: conflict`, `details.required: "If-Match"`.
- Stale `If-Match` → `412` with `code: conflict` and both revisions.
- Weak ETags (`W/"17"`) are rejected with `412`; they are not equivalent to the
  strong revision.
- `If-Match: *` matches any current revision, but the resource must exist. It is
  allowed for `PATCH` and `DELETE`; audit records that an unconditional
  precondition was used.
- `If-None-Match: *` on `POST` means create-only. If the name exists, return
  `409 conflict`; the header does not turn `POST` into an upsert.
- `DELETE` accepts `If-Match` but it is not atomic: SwarmKit's
  `RemoveServiceRequest` carries no version, so a concurrent update can land
  between the check and the delete. The contract documents the race; delete
  wins. Callers that need strict ordering should `PATCH` first and then delete.
- Preconditions are evaluated before `ignoreMissing`. A missing resource with
  `If-Match` returns `412`; a missing resource without `If-Match` returns `404`,
  or `204` when `?ignoreMissing=true`.
- `rollback` requires `If-Match`, because it is an update to the current spec.

### 7.2 Idempotent create

- `Idempotency-Key: <uuid>` on `POST`.
- Scope: token + method + path. TTL 24 h. State is **manager-local in v1**
  (see §12.2).
- A replay returns the original status and body plus
  `Idempotency-Replayed: true`.
- The same key with a different body hash → `409 conflict`
  (`details.reason: "idempotency_key_reuse"`).
- The same key while the first request is still in flight → `409 conflict`
  (`details.reason: "in_progress"`, `retryable: true`,
  `retryAfterMs: 250`).
- The CLI generates a key for every create and reuses it across its own
  retries, so a network retry never double-creates.

### 7.3 Wait and dry run

- `?wait=converged&timeoutMs=600000` on `POST`/`PATCH` holds the request until
  `status.converged` or the timeout.
- Wait requests are exempt from the normal 30 s request timeout; they are
  bounded by `timeoutMs`, maximum 600000.
- A wait timeout is **not** an error: the mutation succeeded. Return `200/201`
  with `status.converged: false` and
  `warnings:[{"code":"wait_timeout","message":"…"}]`.
- `?dryRun=true` validates and resolves references without persisting. It still
  evaluates `If-Match`: a dry run against a stale revision returns `412`. The
  response is the document that *would* be written, with `dryRun: true`.

---

## 8. Endpoint surface

### 8.1 Managers (public, token auth)

| Method | Path | Phase |
| --- | --- | --- |
| GET | `/v1/version` | 1 |
| GET | `/v1/services`, `/v1/services/{ref}` | 1 |
| POST | `/v1/services` | 2 |
| PATCH / DELETE | `/v1/services/{ref}` | 2 |
| POST | `/v1/services/{ref}/rollback` | 2 |
| GET | `/v1/tasks`, `/v1/tasks/{ref}` | 1 (health: 3) |
| GET | `/v1/nodes`, `/v1/nodes/{ref}` | 1 |
| PATCH | `/v1/nodes/{ref}` (availability, labels) | 2 |
| GET | `/v1/networks`, `/v1/networks/{ref}` | 1 |
| POST | `/v1/networks` | 2 |
| DELETE | `/v1/networks/{ref}` | 2 |
| GET | `/v1/configs`, `/v1/configs/{ref}` | 1 |
| POST | `/v1/configs` | 2 |
| PATCH / DELETE | `/v1/configs/{ref}` (labels only) | 2 |
| GET | `/v1/secrets`, `/v1/secrets/{ref}` (metadata only) | 1 |
| POST | `/v1/secrets` | 2 |
| PATCH / DELETE | `/v1/secrets/{ref}` (labels only) | 2 |
| GET | `/v1/cluster` | 1 |
| GET | `/v1/events` (SSE) | 2 |
| POST | `/v1/tasks/{ref}/exec` | 3 |
| GET | `/v1/tasks/{ref}/logs` | 3 |
| GET / PUT | `/v1/tasks/{ref}/files` | 3 |
| GET / POST | `/v1/tasks/{ref}/snapshots`, `/v1/snapshots` | 4 |
| POST | `/v1/snapshots/{ref}/restore` | 4 |
| DELETE | `/v1/snapshots/{ref}` | 4 |
| GET | `/v1/audit` | 2 |

Health and metrics stay as they are: `/healthz` and `/metrics` on the daemon's
loopback port. They are not part of the authenticated API and do not move.

### 8.2 Why task-centric node operations

Public routes are **task-centric** (`/v1/tasks/{ref}/exec`), not node-centric:

- A CI runner knows the task it just created; it should not need node topology.
- The manager resolves task → node and proxies over the private broker, so the
  client needs one endpoint and one firewall rule.
- Task identity is stable across rescheduling; node identity is not.
- Authorization is naturally "may exec this task", enforced through token task
  selectors (§11.3).

### 8.3 Streaming formats

| Surface | Content type | Framing |
| --- | --- | --- |
| Events | `text/event-stream` | SSE, `id` = cursor |
| Logs | `application/x-ndjson` | one JSON object per line, final `eof` |
| Exec (non-TTY) | `application/x-ndjson` | one JSON object per line, final `exit` |
| Exec (TTY) | WebSocket | JSON control frames + base64 data |
| Files | `application/octet-stream` | raw bytes |

All streams are HTTP/1.1-friendly. Only interactive TTY exec needs a WebSocket.

### 8.4 Partial reads and capabilities

`GET /v1/tasks?health=true` fans out through the **broker**, never by dialing a
node's `:8080`. If the `health` capability is absent, the parameter returns
`501 not_implemented` with `details.capability: "health"`. If some nodes fail,
return `207` with the readable tasks plus `partial` (§6.3). A single-task read
that cannot reach the node is `503 unavailable`, not `404`.

### 8.5 Not in v1

Cluster lifecycle (`init`/`join`/`leave`), image build, volumes, host
filesystem management, browser clients, multi-manager HA for the public API.

---

## 9. Events (watch)

`GET /v1/events?types=service,task,node&since=<cursor>&label=team=payments`

- Transport: SSE. `id: <cursor>` on every event, so `EventSource` and HTTP
  clients resume with `Last-Event-ID`.
- The stream begins with `event: ready`. The gateway reads the store's current
  watch version **before** opening the watch; when it can, `ready` carries that
  cursor. When it cannot, `ready` omits `cursor` and the first event's cursor is
  the resume point. This is honest: SwarmKit's `Watch` sends an empty initial
  message with a nil version (`manager/watchapi/watch.go`).
- Heartbeat comment (`: ping`) every 15 s keeps LBs and proxies from idling out.
- Cursor is SwarmKit's Watch store index (`WatchMessage.version`), which
  supports `ResumeFrom`. Compaction maps to `410 cursor_expired`,
  `details.action: "relist"`.
- Delivery is **at-least-once across reconnects**. Duplicates at a resume
  boundary are allowed; consumers dedupe by cursor or revision.
- `changed` is a list of spec paths for **live** updates only. A replay from
  `since` does not have the old object, so `changed` is empty. `actor` is
  optional and gateway-observed for API-initiated changes; scheduler-driven
  changes omit it.

```json
{
  "schemaVersion": 1,
  "kind": "Event",
  "cursor": "18422",
  "at": "2026-10-19T10:04:12Z",
  "type": "service.updated",
  "resource": {"kind": "Service", "id": "q1…", "name": "web"},
  "revision": "18",
  "changed": ["spec.replicas"],
  "actor": {"type": "token", "id": "tok_7f3", "name": "ci-deploy"}
}
```

`type` is `<resource>.<created|updated|removed>`. `?include=object` embeds the
object for consumers that cannot afford a follow-up read.

---

## 10. Exec, logs, files, snapshots

### 10.1 Exec

`POST /v1/tasks/{ref}/exec`

```json
{
  "command": ["sh", "-c", "make test"],
  "env": {"CI": "true"},
  "workdir": "/src",
  "tty": false,
  "stdin": false,
  "timeoutMs": 600000
}
```

**Non-TTY (the CI case):** `200`, `application/x-ndjson`:

```json
{"type":"stdout","data":"aGVsbG8K"}
{"type":"stderr","data":"d2FybmluZwo="}
{"type":"exit","exitCode":0}
```

- `data` is base64 because the stream is binary-safe.
- A non-zero guest exit is **not** an HTTP error: the call succeeded. Only the
  final frame carries `exitCode` (and `signal`, when known).
- `stdin: true` requires `tty: true`. The guest agent has no stdin channel for
  non-TTY commands; sending it returns `400 invalid` with
  `details.reason: "stdin_requires_tty"`.
- **No exit frame means the API failed**, even if output arrived. Clients treat
  a truncated stream as `unreachable`/`partial`, never as success.
- A server-side timeout is `{"type":"error","code":"timeout"}`. It is not a
  fabricated guest exit code.
- A signal-terminated guest is `{"type":"exit","exitCode":143,"signal":15}`;
  `128 + signal` is the shell convention.

**TTY:** `tty: true` requires a WebSocket upgrade
(`Sec-WebSocket-Protocol: v1.swarmcracker.exec`). Client frames:
`{"type":"stdin","data":"…"}`. Closing the socket sends `SIGHUP`. Resize and
signal frames are **not** part of v1; the guest agent does not implement them.
`user` is not part of v1 either.

### 10.2 Logs

`GET /v1/tasks/{ref}/logs?follow=true&tail=200&since=2026-10-19T10:00:00Z`

`application/x-ndjson`:

```json
{"type":"log","stream":"stdout","time":"2026-10-19T10:04:12.123Z","data":"aGVsbG8K"}
{"type":"eof"}
```

`time` is always present; there is no separate `timestamps` parameter.
`follow=false` uses the same shape and ends with `eof`.

### 10.3 Files

- `GET /v1/tasks/{ref}/files?path=/etc/hosts` → raw bytes.
- `PUT /v1/tasks/{ref}/files?path=/tmp/app&mode=0644` with the body as bytes.
- **Regular files only.** Directories return `400 invalid` with
  `details.reason: "directory_not_supported"`; there is no tar mode in v1.
- Limits: 1 GiB per request; paths must be absolute; `..` is rejected after
  cleaning. Every call is audited under `tasks:files`.

### 10.4 Snapshots (phase 4, experimental)

- `POST /v1/tasks/{ref}/snapshots` → `201` `Snapshot` (§5.7).
  Pauses the VM briefly; default timeout 30 s. Returns `501 not_implemented`
  when snapshots are disabled.
- `GET /v1/snapshots?task=` lists; `POST /v1/snapshots/{id}/restore` restores
  and resumes; `DELETE` removes files.
- `restore` may start Firecracker outside SwarmKit's task tracker. The docs say
  so; the API does not claim scheduler ownership.

---

## 11. Authentication and authorization

### 11.1 Transport security

- TLS 1.2+ on the public API. The server certificate is a **leaf certificate
  issued by the cluster CA** with SANs for manager hostnames and advertise
  addresses. Existing node certificates are CN=node ID with no DNS SANs, so the
  leaf issuance path is new work in phase 1, not a reuse of the node cert.
- Clients receive the CA bundle via `swarmcracker cluster ca export` and verify
  the server. No `InsecureSkipVerify` on any TCP path. The existing Unix-socket
  dial may keep its no-hostname configuration in a separate code path; a lint
  rule forbids it in the HTTP client.
- Bind default `127.0.0.1:4244`; remote exposure requires
  `--api-listen 0.0.0.0:4244` and is documented with the firewall rule.

### 11.2 Tokens

- `Authorization: Bearer sc_<keyId>_<secret>`
- `keyId` is 8 hex characters for O(1) lookup; `secret` is 32 random bytes.
- The server stores `sha256(secret)` and compares in constant time. The token is
  shown once; `token list` shows only `keyId` plus metadata.
- Tokens carry scopes, a name, an optional expiry, last-used time, and optional
  **task selectors** (§11.3).
- **Manager-local in v1.** Tokens are minted and validated on the manager that
  serves the API. A leader change requires re-issuing tokens. This is the
  single-manager decision in §12.2, not an accident.
- Bootstrap: `cluster init` writes an admin token to
  `/var/lib/swarmcracker/tokens/admin.token` (0600) and prints the path, not the
  value. On a new leader, root can mint a new admin token locally.
- Rotation: create new, deploy, revoke old. Revocation is local and immediate
  (cache TTL ≤ 5 s).

### 11.3 Scopes, roles and task selectors

Scopes (server-enforced, deny by default):

```
services:read   services:write
tasks:read      tasks:exec   tasks:files   tasks:logs   tasks:snapshots
nodes:read      nodes:write
networks:read   networks:write
configs:read    configs:read-values   configs:write
secrets:read    secrets:write        # read = metadata only, always
events:read     cluster:read
tokens:admin    audit:read
```

| Role | Scopes | Use |
| --- | --- | --- |
| `reader` | all `*:read` (not `configs:read-values`) + `events:read` + `tasks:logs` + `cluster:read` | dashboards, monitoring, read-only CI |
| `runner` | `reader` + `tasks:exec` + `tasks:files` + `tasks:snapshots` | CI that builds/tests inside VMs but must not change services |
| `operator` | `runner` + `services:write`, `networks:write`, `configs:write`, `configs:read-values`, `secrets:write` | deploys |
| `admin` | everything, incl. `nodes:write`, `tokens:admin`, `audit:read` | cluster administration |

**Task selectors.** A token may carry:

```json
{
  "taskSelectors": {
    "serviceIds": ["q1…"],
    "taskIds": ["t1…"],
    "labels": {"team": "payments"}
  }
}
```

Selectors are ANDed. The manager resolves the task, then checks that the task's
service ID, task ID, or service labels match before proxying `exec`, `files` or
`snapshots`.

- `runner` tokens **must** have at least one selector. A runner token with an
  empty selector cannot be minted; an existing one fails closed with
  `403 forbidden` and `details.reason: "selector_required"`.
- `operator` and `admin` tokens may omit selectors, which means "all tasks".
- This is what makes "may exec this task" true; `tasks:exec` alone is
  cluster-wide root inside guests.

### 11.4 mTLS and the broker

Node → manager and manager → node traffic keeps mutual TLS with cluster node
certificates. External clients use bearer tokens. The node broker never sees a
user token; the manager authenticates the caller, checks scopes and selectors,
then proxies.

### 11.5 Audit

Every mutation and every `exec`/`files` call records:

```json
{"at":"…","requestId":"req_…","actor":{"type":"token","id":"tok_7f3","name":"ci"},
 "action":"service.update","resource":{"kind":"Service","id":"…","name":"web"},
 "result":"ok","sourceIp":"10.0.0.5"}
```

Audit records are manager-local in v1, append-only, retained 30 days by
default, and exposed at `GET /v1/audit?limit=&cursor=` with `audit:read`.
`sourceIp` is the TCP peer unless `api.trustedProxies` is configured; behind a
VIP it is the load balancer, and the docs say so. Secret values are never
recorded; the action is.

---

## 12. Topology, ports, limits

### 12.1 Ports

Keep the listeners separate. `4243` is already the ingress mTLS server; the
public API must not share it.

| Port | Owner | Auth | Exposure |
| --- | --- | --- | --- |
| 4242 | SwarmKit remote API | cluster mTLS | **loopback by default in phase 1**; existing `0.0.0.0` needs a firewall/migration note |
| 4243 | Ingress table/report | cluster mTLS | internal |
| **4244** | **Public API v1** | **bearer token + TLS** | loopback by default; opt-in remote |
| 4245 | Node broker | cluster mTLS | internal, manager→node only |
| 8080 | Health/metrics | none | loopback |
| Unix socket | Local API | file mode 0600 (uid 0) | manager node only |

`--listen-remote-api` currently defaults to `0.0.0.0:4242`, and any cluster
certificate is cluster-admin. Phase 1 changes the default to loopback and
documents the migration; operators who need it must firewall it explicitly.

### 12.2 One manager in v1

- The API is served by the **leader**. A request that reaches a non-leader
  returns `503 unavailable` with `details.leader` and an
  `X-SwarmCracker-Leader` hint.
- `GET /v1/cluster` returns `{leader, members:[{id,hostname,role,state,apiUrl}], apiVersion}`.
- Tokens, idempotency keys and audit records are manager-local. After a leader
  change, clients re-issue tokens against the new leader. **HA/VIP failover is
  phase 2**, and requires replicating this state; v1 does not claim it.
- The gateway dials the **local control socket**, not the SwarmKit remote API on
  4242. Only the local server is raft-proxied; 4242 is an admin surface.

### 12.3 Limits (defaults, documented)

| Limit | Value |
| --- | --- |
| JSON request body | 8 MiB |
| File transfer | 1 GiB |
| `limit` | 100 default, 1000 max |
| Concurrent exec per node | 10 (429 beyond) |
| Exec/log idle timeout | 15 min |
| Wait timeout | 600 s max; exempt from the normal request timeout |
| Normal request timeout | 30 s |
| Event stream buffer | 1024 events; a slow consumer is disconnected (`cursor_expired` on resume if compacted) |
| Rate limit | 10 req/s per token, burst 20 (matches the daemon; configurable) |
| `X-Request-ID` | 8–64 chars of `[A-Za-z0-9._-]`; invalid values are replaced, not echoed |

---

## 13. CLI and SDK shape

### 13.1 CLI

New global flags (replacing 17 per-command `--format` flags):

| Flag | Env | Purpose |
| --- | --- | --- |
| `-H, --host` | `SWARMCRACKER_HOST` | `https://mgr:4244` or `unix:///var/run/swarmcracker/api.sock` |
| `--ca` | `SWARMCRACKER_CA` | CA bundle for server verification |
| `--token-file` | `SWARMCRACKER_TOKEN_FILE` | 0600 file containing the bearer token |
| `-o, --output` | `SWARMCRACKER_OUTPUT` | `table` (default), `json`, `name`, `ndjson` |
| `--quiet` | — | same as `-o name` |
| `--limit`, `--cursor` | — | pagination |
| `--request-id` | — | caller-supplied correlation ID |
| `--timeout` | `SWARMCRACKER_TIMEOUT` | request timeout |
| `--wait`, `--dry-run` | — | map to the query parameters |
| `--exit-code=api\|guest` | — | guest passthrough vs API-only exit codes |

- The existing `volume export -o` file flag collides with global `-o`. Phase 0
  renames it to `--output-file` (deprecating `-o` for that command) and removes
  the per-command `-q` registrations in favour of the global `--quiet`.
- `--format json` stays as a deprecated alias for the **legacy shape**, not the
  new envelope. In JSON mode it now emits valid JSON on empty results (`[]`) —
  a compatible bug fix — while table mode may still print prose. Removal no
  earlier than N+2 minors.
- In `-o json`, stdout is one document; stderr carries exactly one `Error`
  document if the command fails. Warnings live in the payload's `warnings[]`,
  not on stderr. For streams, a warning is a `{type:"warning"}` NDJSON frame or
  an SSE `event: warning`.
- `-o name` / `--quiet` prints one ID per line.

### 13.2 Go

`pkg/client` is the only package allowed to import `api.ControlClient`; both the
CLI and the HTTP gateway use it. A test/lint gate enforces the rule.

```go
c, _ := client.New(client.Config{
    Host:      "https://mgr:4244",
    CAFile:    "ca.crt",
    TokenFile: "~/.config/swarmcracker/prod.token",
})
services, _ := c.Services().List(ctx, client.ListOptions{Label: "team=payments"})
for _, svc := range services.Items {
    fmt.Println(svc.Name, svc.Status.RunningTasks)
}
```

### 13.3 Python

Generated from OpenAPI, wrapped thinly:

```python
from swarmcracker import Client
sc = Client(host="https://mgr:4244", ca="ca.crt", token_file="~/.config/swarmcracker/prod.token")

for svc in sc.services.list(label="team=payments"):
    print(svc.name, svc.status.running_tasks)

sc.services.patch("web", {"spec": {"replicas": 5}}, if_match="17", wait="converged")

with sc.tasks.exec("t1…", ["sh", "-c", "make test"]) as run:
    for frame in run:
        ...
    print(run.exit_code)
```

### 13.4 Node / TypeScript

Generated fetch client, ESM, **Node 18+ only**. It is not browser-safe: browsers
cannot set `Authorization` on `EventSource`, cannot use a custom CA bundle, and
cannot read a token file. Browser support needs a token broker and CORS, which
are out of v1.

```ts
const sc = new Client({ host, ca, tokenFile });
for await (const ev of sc.events.watch({ types: ["service", "task"], since })) {
  console.log(ev.type, ev.resource.id);
}
```

### 13.5 SDK rules

- Retry idempotent calls (`GET`, and `POST` with an idempotency key) on
  `429`/`5xx`/network errors with jittered backoff; honor `Retry-After`.
- Surface typed exceptions carrying `code`, `details`, `requestId`.
- Expose streams as language-native iterators.
- Never retry a non-idempotent `POST` silently.
- Treat `207` as success and inspect `partial`; do not throw on a partial read.
- Read the token from env/file, never require it in argv.
- Validate every DTO against the OpenAPI schema in CI, and compile the generated
  Python/TS clients against that schema.
- `make api-check` runs against an in-process mock on hosted CI; a separate
  scheduled job runs against the three-node KVM lab.

---

## 14. Where this differs from RFC #102

| # | RFC | This proposal | Why |
| --- | --- | --- | --- |
| 1 | `-H https://mgr:4243` | `:4244`; 4243 stays ingress | 4243 is already bound by `pkg/ingress` with a different auth model. |
| 2 | D2 leaning mTLS certs | Bearer tokens; mTLS internal | Revocable, scopeable, one header in every language. |
| 3 | `/v1/nodes/{id}/exec` | `/v1/tasks/{ref}/exec`, manager-proxied | Clients know tasks, not topology; one endpoint; authz per task. |
| 4 | R20 sorted by ID | `(createdAt, id)` ascending | ID order skips newly created objects that sort before the cursor. |
| 5 | Error codes `usage`…`internal` | Server vs client codes split; add 428, `cursor_expired`, `rate_limited`, `unavailable`, `timeout`, `unsupported_version`, `not_implemented` | A server cannot return `usage`/`unreachable`; 207 is a success body. |
| 6 | Guest exit passthrough | Passthrough default, `--exit-code=api` + `exitCode`/`signal` | The RFC's passthrough collides with its own exit table. |
| 7 | `pkg/client` + CLI | CLI is a client of the HTTP API over a Unix socket | Otherwise "one implementation" is false. |
| 8 | Raw SwarmKit JSON for `--format` | New envelope + spec/status DTOs; legacy frozen | `--format` cannot change without breaking scripts. |
| 9 | Three roles | Four roles incl. `runner`, plus task selectors | `exec` is root-in-guest and must be separable from `services:write`. |
| 10 | 207 unmentioned | 207 + typed `partial` | R10 requires a machine-distinguishable partial failure. |
| 11 | `PUT` replace | **No `PUT`; `PATCH` overlay** | A curated DTO cannot full-replace a SwarmKit spec without data loss. |
| 12 | Any manager + VIP | **Single-manager v1; HA phase 2** | Token/idempotency/audit state is not replicated yet. |
| 13 | Config values readable | `configs:read-values` scope | R8 requires config values to be protected. |
| 14 | Health in one call | Health via broker, phase 3 | Managers cannot reach node loopback `:8080`. |
| 15 | Exec user/resize/signal/tar | Removed in v1 | The guest agent does not implement them. |
| 16 | Snapshots phase 3 | Phase 4 experimental | The current restore path starts an untracked VM. |

---

## 15. Decisions

| # | Decision | Call | Rationale |
| --- | --- | --- | --- |
| D1 | Transport | **A: HTTP/JSON gateway; C as phase 0** | Widest language reach; curl-testable; SSE/NDJSON for streaming. |
| D2 | Credential model | **Scoped bearer tokens; manager-local v1; mTLS internal** | Revocable, scopeable, one header. HA token replication is phase 2. |
| D3 | Node broker | **Manager-proxied, task-keyed; private mTLS broker** | One endpoint, one firewall rule, no node certs on laptops. |
| D4 | Schema ownership | **Hand-written DTOs; OpenAPI 3.1 normative; no `PUT`** | Protobuf leakage is avoided; full replace is unsafe. |
| D5 | Legacy JSON lifetime | **N+2 minors** | Migration time without freezing forever. |
| D6 | Write scope | **Curated: services, networks (create/delete), configs/secrets (create/labels/delete), nodes (availability/labels)** | Keeps blast radius small and matches SwarmKit RPCs. |
| D7 | Volumes & snapshots | **No volumes; snapshots phase 4 experimental** | Volumes are not scheduler objects; restore is untracked. |
| D8 | Public port | **4244 public, 4245 broker; 4242 loopback** | 4243 is ingress; 4242 is admin. |
| D9 | Authorization | **Scopes + 4 roles + task selectors** | `exec` is root-in-guest; selectors make per-task authz real. |
| D10 | SDK strategy | **OpenAPI-generated + thin wrappers; Node-only TS** | One schema, three languages, no browser auth fiction. |
| D11 | Availability | **Single-manager v1; HA phase 2** | Tokens, idempotency and audit are not replicated. |
| D12 | Health | **Broker capability, phase 3** | The manager cannot dial node loopback. |

---

## 16. Phases and acceptance

| Phase | Deliverable | Closes | Size |
| --- | --- | --- | --- |
| 0 | `pkg/api/v1` DTOs for core resources, `pkg/output`, global `-o`, empty-list fix, error/exit mapping, golden fixtures, `pkg/client` (gRPC transport), DTO↔OpenAPI test | G1, G2, G8 | M |
| 1 | HTTP server on Unix socket + 4244, reader tokens, read endpoints, pagination, ETag, `/v1/version`, 4244 leaf cert, 4242 loopback, OpenAPI | G4 | M |
| 2 | Writes, scopes/roles/selectors, `If-Match`, idempotency, wait, dry run, events, audit, rate limits | G3 | M–L |
| 3 | Broker + `exec`/`logs`/`files` + `health` capability | G5 | **XL** |
| 4 | Snapshots (experimental), generated SDKs, conformance suite, docs examples in CI, CI image | G6, G7 | M–L |

Acceptance criteria (additions to RFC §8):

1. `swarmcracker service ls -o json` on an empty cluster prints
   `{"schemaVersion":1,"kind":"ServiceList","items":[],"next":null}` and exits 0;
   a golden fixture asserts it for every **enveloped core** list command.
   `volume`, `image` and `metrics` remain legacy until their read models exist.
2. A `set -e` script distinguishes: `service inspect nope` → 3; daemon stopped →
   6; reader creating a service → 5; missing `If-Match` → 4 (428); stale
   `If-Match` → 4 (412); `limit=5000` → 2; node down during
   `tasks?health=true` → 7.
3. `cmd/swarmcracker` and `pkg/api` import no `api.ControlClient` outside
   `pkg/client` (test-enforced).
4. From a laptop with a `reader` token and an explicitly exposed
   `--api-listen 0.0.0.0:4244`:
   `swarmcracker -H https://mgr:4244 --ca ca.crt --token-file reader.token service ls`
   works with no root and no SSH.
5. A reader token cannot write (403 `forbidden`). A runner token without a
   selector cannot be minted, and one with a selector cannot exec a task outside
   it (403 with `details.reason`).
6. `vm exec web -- true` → 0; `-- false` → guest status; `--exit-code=api -- false`
   → 8; a signal-killed guest → `128 + signal`; a truncated stream is a failure.
7. Redaction property test: a service with `registryAuth` never emits the
   credential. A config value is absent unless the token has
   `configs:read-values`.
8. No `InsecureSkipVerify` on a TCP path. The Unix-socket exception is isolated
   and tested.
9. OpenAPI validates every DTO and golden fixture. `make api-check` runs against
   an in-process mock on hosted CI; a separate scheduled job runs against the
   three-node lab.
10. The Python and Node examples in `docs-site` are executed in CI.

---

## 17. Risks and mitigations

| Risk | Mitigation |
| --- | --- |
| Overlay `PATCH` mishandles an unmodeled field | Deep-copy the current SwarmKit object, overlay mapped fields, preserve the rest; property-test round-trips. |
| Manager-local tokens/idempotency/audit break on failover | v1 is explicitly single-manager; HA is phase 2 with replicated state. |
| Health fan-out through the broker adds latency | Broker is phase 3; `health` is capability-gated; 2 s per-node deadline; `207 partial`. |
| Guest agent gaps (user/stdin/resize/tar) | Cut from v1; schedule agent v2 only if a consumer requires it. |
| Clock skew breaks `createdAt` pagination | RFC3339Nano; document "absent clock skew"; events are the exact-change path. |
| Watch compaction | Map to `410 cursor_expired`; SDKs re-list and resume. |
| Snapshot restore starts an untracked VM | Mark experimental, disabled by default, phase 4; docs say it is not scheduler-managed. |
| 4242 remains exposed | Phase 1 changes the default to loopback with a migration note and firewall guidance. |
| Curated DTO drifts from SwarmKit | All mapping in `pkg/client`; DTO↔OpenAPI validation; contract tests pin the wire shape. |
| API becomes a second source of truth | No writes outside SwarmKit RPCs; no local database for resources; stateful control-plane data is explicitly manager-local. |

---

## 18. Open questions for the owner

1. **Config values:** is `configs:read-values` the right protection, or should
   configs never be readable over the API? This revision assumes a scope.
2. **Single-manager v1:** acceptable for the first release, with HA in phase 2?
   If not, token/idempotency/audit replication must be designed before phase 1.
3. **Snapshots:** is phase 4 experimental acceptable, given that restore starts
   an untracked VM? If not, remove restore from the API and keep snapshot
   creation only.
4. **Full replacement:** confirm no consumer needs a true `PUT`. If one does,
   the write type must become exhaustive.
5. **4242 default:** change to loopback in phase 1, or keep `0.0.0.0` with a
   documented firewall requirement?
6. **4244 certificate:** issue from the cluster CA, or use a separate CA with
   fingerprint pinning? The node certificates cannot be reused as-is.
7. **Browser clients:** confirm out of v1. If a dashboard must run in a browser,
   a token-broker endpoint and CORS design are needed.
8. **Task selectors:** should `operator` also be restricted to selectors in
   production, or is cluster-wide exec acceptable for that role?
