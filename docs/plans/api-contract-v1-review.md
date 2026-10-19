# Independent review — API contract v1 proposal

- **Reviewer:** fresh-context subagent (adversarial review)
- **Date:** 2026-10-19
- **Scope:** `docs/plans/api-contract-v1-proposal.md` against
  `docs/plans/rfc-programmatic-api.md`, issue #102, and the actual repository /
  SwarmKit dependency.
- **Status:** findings only. No proposal or source file was changed.
- **Revision status:** the proposal was revised after this review; see §0 of
  `api-contract-v1-proposal.md` for the resolution list. This file records the
  review as delivered.
- **Severity:** **P0** = must resolve before freezing `pkg/api/v1`; **P1** =
  must resolve before phase 1; **P2** = can land during phase 0/1.

> The P0 claims below were spot-checked by the author of this file:
> `UpdateService` replaces the whole spec, secret/config updates are labels-only,
> and SwarmKit has no network update RPC.

---

## Verdict

Phase 0's CLI half (empty-list fix, error/exit mapping, output writer) can start
now. Freezing `pkg/api/v1` cannot: the write model is not implementable against
SwarmKit as written, and the document's error/kind contradictions would be
encoded permanently by OpenAPI.

**Biggest risk:** a lossy `Service` DTO plus full-replace `PUT` silently deletes
unmodeled SwarmKit fields and registry credentials, and the additive-only policy
then makes that permanent.

---

## Blocking issues

### P0 — Lossy DTO + full-replace `PUT`

§7.2 says omitted optional fields reset to defaults, while read-only fields are
rejected and `registryAuth` is write-only. A GET → PUT round-trip is therefore
impossible: stripping read-only fields deletes unmodeled SwarmKit fields, and
SwarmKit's `UpdateService` replaces the whole spec:

- `manager/controlapi/service.go:909-913` — `service.Spec = *request.Spec.Copy()`
- `api/specs.proto:63-88`, `:124-176` — fields the DTO does not model

**Fix:** define `PUT`/`PATCH` as read-modify-write overlays that preserve fields
not represented in the DTO, or use a separate exhaustive write type. If neither
is acceptable, drop `PUT` from v1.

### P0 — Secrets and configs cannot support `PUT`/`PATCH` data

SwarmKit allows only label updates for secrets and configs; changed data is
rejected:

- `manager/controlapi/secret.go:63-72`
- `manager/controlapi/config.go:65-83`
- `api/control.proto` — no data-update RPC

The endpoint table lists `POST/PUT/DELETE` and the proposal says `PUT` rotates
data. That cannot be implemented.

**Fix:** `POST` create, `PATCH` labels only, `DELETE`. Rotation is create-new then
repoint services. Remove `PUT` from those rows.

### P0 — Networks have no update RPC

The control API exposes Create/Get/List/Remove only; there is no UpdateNetwork:

- `api/control.proto:69-73`
- `manager/controlapi/network.go` — no update handler

The proposal lists `PUT`/`PATCH` for networks. Remove them; state that driver,
scope and labels are immutable in v1.

### P0 — Error / status / `kind` contradictions

A schema freeze would encode all of these:

- `428` is used in §7.1 but missing from the §4.2 table.
- `conflict` maps to both 409 and 412, with no rule for which body shape.
- `207` appears as both an error code and a `TaskList`; `partial[].error` lacks
  `requestId`/`details`.
- `Cluster` and `AuditEntry` are missing from the closed `kind` set despite
  `/v1/cluster` and `/v1/audit`.
- `dryRun` appears in responses but is not in the document shape.
- `Warning` is both a `kind` and a `warnings[]` element.
- `kind` is declared closed while axiom 8 promises additive-only evolution.
- `405`, `413`, `415`, and unknown routes are untyped.

**Fix:** enumerate every `kind`; add 428 and transport-level errors; define one
body per status and one error shape; add `dryRun`/`warnings` to the envelope;
make `kind` open (clients tolerate unknown values).

### P1 — `runner` is cluster-root-in-guests, not per-task authz

§8.2 claims authorization is "may exec this task", but `tasks:exec`,
`tasks:files` and `tasks:snapshots` are global scopes. A runner token can exec
into any production task and read mounted secrets, which makes
`secrets:read = metadata only` mostly cosmetic.

**Fix:** add token selectors (`serviceIds`, `labels`, `taskIds`) enforced at task
resolution, or document runner as cluster-wide root-in-guests and restrict it to
single-tenant clusters.

### P1 — Token, idempotency and audit state have no cluster-wide home

§12.2 supports any manager plus a VIP, while tokens live on the minting manager,
idempotency is a 24 h local cache, and audit is append-only at `/v1/audit`.
§17 says "no local database", and SwarmKit has no token/audit RPC.

Consequences: a token minted on A fails on B; failover can double-create;
revocation is per-manager.

**Fix:** use a replicated store object, or pin stateful admin features to the
leader and drop the VIP/any-manager claim.

### P1 — Health fan-out cannot work from a manager

The existing fan-out is plain HTTP to `Status.Addr:8080`
(`cmd/swarmcracker/task_health.go`), the daemon binds `127.0.0.1:8080`
(`cmd/swarmd-firecracker/flags.go`), and `/v1/tasks/health` is unauthenticated.
§8.4's manager-proxied `?health=true` has no reachable, authenticated path.

**Fix:** route health through the mTLS broker, or drop `?health=true` in v1 and
keep health node-local.

### P1 — Exec/files DTOs exceed the agent protocol

The guest agent protocol has no `user`, no resize/signal, and no directory tar;
non-TTY stdin is rejected; TTY consumes the connection:

- `pkg/agent/protocol.go:34-43` — request shape
- `pkg/agent/client.go` — non-TTY stdin rejection
- `pkg/agent/server.go` — TTY connection semantics; regular files only
- RFC non-goal: do not change the guest protocol

**Fix:** remove `user` and resize/signal from v1; document stdin requires `tty`;
make files regular-file-only; schedule a protocol v2 if needed.

### P1 — Events cannot deliver the claimed `ready` cursor, `changed`, or `actor`

`Watch` sends an empty `WatchMessage` with a nil version
(`manager/watchapi/watch.go`; the code even has a TODO asking whether to send
the current version). `IncludeOldObject` applies only to live changes, not
`ResumeFrom`. Watch events carry no actor.

**Fix:** source the initial cursor from a store view, mark `changed` as
live-only, and make `actor` optional / gateway-observed.

---

## Important issues

1. **`revision` ambiguity.** `UpdateService` checks `ServiceVersion` against
   `Meta.Version`, not `SpecVersion`. Define `revision = Meta.Version.Index`,
   amend R24, and document that scheduler bumps can also invalidate an `If-Match`
   (so 412 may be retried, not treated as a user conflict).
2. **`If-Match` gaps.** RemoveService carries no version, so delete is TOCTOU.
   Rollback has no precondition. Document the race and require `If-Match` on
   rollback.
3. **Legacy `--format json` still prints prose on empty lists**
   (`cmd_service.go:800`, `cmd_task.go:148`, `cmd_node.go:302`, `volume.go:156`),
   so G1 is only fixed for `-o json`. Fix both or narrow R1.
4. **Phase 0 "every list command"** includes volume/image/metrics, but `kind`
   omits `Volume`, `Image`, `Metric`.
5. **4244 TLS issuance is unspecified.** Node certificates are CN=node ID with
   no DNS SANs; there is no `cluster ca export` command. Specify leaf issuance or
   fingerprint pinning.
6. **Snapshot contract mismatch.** `SnapshotInfo` has `SizeBytes`/`MemoryMB`,
   not `stateBytes`/`memoryBytes`; restore starts an untracked Firecracker
   process; snapshots are disabled by default; the existing client timeout is
   30 s, not 120 s.
7. **4242 remains exposed.** `--listen-remote-api` defaults to `0.0.0.0:4242`,
   and any cluster certificate is admin. Change to loopback or add a migration
   and firewall note.
8. **`Node.version` cannot be populated.** `Describe` sets no
   `EngineDescription`. Remove the field or populate it.
9. **Health fields mismatch.** `runtime.VMHealth` has no `since` or `lastCheckAt`;
   map `UpdatedAt` or add fields.
10. **Config `data` is bytes**, so a UTF-8 string silently corrupts binary
    configs. Use base64 or a typed error.
11. **Merge Patch needs `null` to delete keys**, contradicting axiom 9. Document
    the exception.
12. **Idempotency in-flight/failover is undefined**, and `wait` at 600 s
    contradicts the 30 s request timeout.
13. **Rate-limit claim is false.** The daemon uses 10 req/s with burst 20, not
    100/200.
14. **The gateway must use the local control socket, not 4242.** Only the local
    server is raft-proxied.
15. **Browser/EventSource claims fail** with bearer auth and a custom CA.
    Either drop "browser-safe" or add a short-lived stream ticket and CORS.
16. **Hosted CI cannot run the three-node lab**; `make api-check` does not
    exist. Use mocks in CI and a separate KVM job for the lab.
17. **OpenAPI/DTO drift is untested.** Generate one from the other, or validate
    every marshaled DTO against the schema.
18. **Global `-o`/`-q` collide** with `volume export -o` and per-command `-q`.
    Pick non-conflicting names.
19. **Phase 0 claims G3 but has no watch**; phase 3 is XL, not L, given the agent
    gaps.
20. **Acceptance inconsistencies:** remote `mgr:4244` is tested while the default
    is loopback; `limit=5000` has no CLI flag; `--exit-code=api` maps guest 8 to
    8 and leaves signal `-1` undefined.

---

## Nits

- Citation drift: `watch.proto:96-110` should be `:136-139`; `cmd_service.go:799`
  is `:800`; `token_helper.go:52` is `:53`.
- `"manager": null` violates the no-null rule.
- "25-char base36" excludes `snap-<16 hex>` IDs.
- Snapshot list is newest-first, not `(createdAt, id)` ascending.
- `timestamps=true` is redundant with a mandatory `time`.
- 207 is WebDAV; document client handling.
- Validate `X-Request-ID` length/charset before echoing it.
- Define weak ETags (`W/"17"` → 412) and `If-Match: *`.
- `If-None-Match` on POST is undefined; `?ignoreMissing` + stale `If-Match` is
  undefined.
- `dryRun` precondition behavior is unspecified.
- `Network.scope` does not exist in `NetworkSpec`.
- `status.converged` is circular for global mode and undefined for jobs.
- Capabilities include `write`/`exec` for reader tokens; clarify features vs
  grants.
- `/v1/audit` lacks pagination/retention; `sourceIp` behind a VIP is the LB.
- `createdAt` ordering assumes clock monotonicity; say "absent clock skew."
- Cursor binds filters/sort but not `health`.
- `schemaVersion` is an integer, not semver as RFC R25 says.
- `health.status: unknown` is not emitted by the runtime.

---

## Requirements coverage

| ID | Status | Note |
|---|---|---|
| R1 | Partial | `-o json` meets it; frozen legacy `--format json` still prints prose on empty lists. |
| R2 | Partial | Payload on stdout, but warnings appear both in the body and on stderr; legacy prose remains. |
| R3 | Partial | Envelope exists; `Cluster`/`AuditEntry` missing from `kind`; stream frames use `type`. |
| R4 | Met | One document per call; named opt-in streams. |
| R5 | Partial | Code set exists, but 428 missing, `conflict` has two statuses, 207 has two shapes, `details` has no per-code schema. |
| R6 | Met | `-o name`/`-q` gives bare IDs. |
| R7 | Partial | OpenAPI normative; no test that every DTO field is represented. |
| R8 | **Not met** | Config values are readable with `configs:read`, contradicting R8's "config values can never appear". |
| R9 | Met | Exit table + guest passthrough + `--exit-code=api`. |
| R10 | Met | Transport/auth/not-found/conflict/partial are distinguishable. |
| R11 | Partial | Remote read exists; CA distribution and 4244 cert issuance unspecified. |
| R12 | Met | Server-enforced scopes; reader-create test planned. |
| R13 | Partial | Token commands are prose only; no endpoints or store design. |
| R14 | Partial | Public path verifies TLS; the local socket path still sets `InsecureSkipVerify`. |
| R15 | Partial | Audit design exists; storage, replication, pagination and source IP undefined. |
| R16 | Met | One manager API and one broker. |
| R17 | Met | HTTP+JSON read surface; no protobuf for consumers. |
| R18 | Partial | Node ops are phase 3; agent lacks user/stdin/resize/tar. |
| R19 | Partial | Health/metrics documented; ingress is neither documented nor declared private. |
| R20 | Partial | Ordering deliberately changed to `(createdAt, id)`; amend R20. |
| R21 | Met | Default 100, max 1000, opaque cursor, hard rejection. |
| R22 | Partial | Watch resume exists; initial cursor, `changed`, and actor claims do not hold. |
| R23 | Met | Idempotency key + name upsert; in-flight behavior undefined. |
| R24 | Partial | Conditional update exists, but uses `Meta.Version`, not `SpecVersion`. |
| R25 | Partial | Additive-only stated; `kind` closed and `schemaVersion` is an integer. |
| R26 | Met | Typed 406 with supported list. |
| R27 | Partial | Legacy shape frozen, but empty-list prose remains. |
| R28 | Partial | `pkg/client` exists; phase 0 is gRPC, phase 1 adds HTTP. |
| R29 | Partial | OpenAPI/`make api-check` planned; target absent; hosted CI cannot run the lab. |
| R30 | Partial | Python/Node examples planned for phase 4. |
| R31 | Partial | CI image planned; contents unspecified. |

**Count:** 10 met, 1 not met, 20 partial.

---

## What the proposal gets right

- **The 4243 collision is real and correctly avoided.** Ingress already owns 4243
  and identifies peers by client certificate; multiplexing bearer tokens onto it
  would be an auth bug. 4244 is the right call.
- **The error taxonomy split is a real improvement.** Server codes vs
  client-synthesized `usage`/`unreachable` closes G2 without string matching, and
  the guest-exit collision is handled explicitly.
- **Task-centric node operations are the right abstraction.** They match the CI
  mental model and keep one firewall rule.
- **`(createdAt, id)` ordering is the correct amendment to R20.** Random IDs
  cannot support append-only cursor pagination; the rationale is sound.
- **The streaming formats are language-agnostic.** SSE + NDJSON + base64 + an
  explicit exit frame are implementable from Python/Node without a Go dependency.
- **Phase 0's empty-list fix is the highest-leverage change.** G1 is reproducible
  and the fix is small and testable.
- **Separate request/response types for `registryAuth` are stronger than the
  current redactor**, which still emits a `"***"` placeholder.

---

## Recommended changes (priority order)

1. Replace full-replace `PUT` with read-modify-write overlays, or drop `PUT`.
2. Remove `PUT`/`PATCH` data updates for secrets/configs; remove network updates.
3. Fix the envelope/error/`kind` table before freezing `pkg/api/v1`:
   add 428/405/413/415; enumerate kinds; one body per status; define `dryRun` and
   `warnings`; make `kind` open.
4. Decide where tokens, idempotency keys and audit records live (replicated
   store or leader-only), and reconcile the VIP claim.
5. Scope `runner` with selectors, or document it as cluster-wide root-in-guests.
6. Route health through the broker, or drop `?health=true` in v1.
7. Cut `user`, resize/signal and directory tar from exec/files v1.
8. Fix the event contract: initial cursor source, live-only `changed`, optional
   actor.
9. Define `revision = Meta.Version.Index`; amend R24; require `If-Match` on
   rollback; document the delete race.
10. Fix compatibility: either repair legacy empty-list prose or narrow R1.
11. Make `make api-check` runnable on hosted CI (mocks); keep the lab job separate.
12. Prove OpenAPI/DTO alignment; compile generated clients in CI.
13. Drop "browser-safe" TS or add a browser auth story.
14. Rename global `-o`/`-q` to avoid collisions.
15. Default 4242 to loopback; specify 4244 certificate issuance.

---

## Open questions for the owner

1. Is the DTO lossless for all SwarmKit fields, or a curated subset? If curated,
   what happens to unmodeled fields on write — preserve, reject, or reset? This
   decides whether v1 can be frozen.
2. Where do tokens, idempotency keys and audit records live across managers?
3. Is `runner` allowed to exec into production tasks? If not, what binds it to a
   service/task?
4. Are configs secret or not? R8 says values never appear; §5.5 makes them
   readable.
5. Is snapshot restore a cluster-managed operation or a node-local debug action?
   The current restore starts an untracked Firecracker process.
6. Is browser support a real goal? If yes, how do browsers send bearer tokens to
   `EventSource`/WebSocket and trust the cluster CA?
7. Is the 4244 certificate issued by the cluster CA or a separate CA with
   fingerprint pinning?
8. Should legacy `--format json` keep the empty-list prose, or is fixing it a
   compatible bug fix?
9. Is true full-replace `PUT` required by any consumer? If not, v1 can be
   `PATCH`-only.
10. Does the public API need multi-manager/VIP in v1, or can it be single-manager
    first? If multi-manager, resolve question 2 first.
