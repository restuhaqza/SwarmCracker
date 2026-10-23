# Tasks: API v1

Status legend: `[ ]` pending, `[~]` in progress, `[x]` done.
Each task is one focused session and must end with its verification command.

## Phase 0 — contract + client boundary

- [x] `contract`: v1 DTOs, envelope, error/exit mapping, OpenAPI, golden
  fixtures, `make api-check`.
- [x] `contract`: `service ls`, `task ls`, `node ls`, `service inspect`,
  `service ps`, config/secret/network list envelopes.
- [x] `client-boundary`: guard test and migration spec.
- [x] `client-boundary`: migrate `resolve.go`.
- [ ] `client-boundary`: add the `pkg/client/legacy` adapter and route all
  remaining raw-control command paths through it.
  - Acceptance: allowlist empty; guard passes; legacy output unchanged.
  - Verify: Linux `cmd/swarmcracker` suite, `make api-check`, lint.
  - Files: `pkg/client/legacy/client.go`, the seven allowlisted command files,
    `cmd/swarmcracker/client_boundary_test.go`, spec.
- [ ] `client-boundary`: delete the allowlist map and record the empty state.
  - Acceptance: guard enforces an empty set.
  - Verify: same as above.

## Phase 1 — `http-read`

- [ ] Spec: `docs/plans/spec-http-read-v1.md` (ports, TLS, routes, errors,
  pagination, ETag, `/v1/version`).
- [ ] `http-read`: server package with mux, auth middleware placeholder, version
  endpoint.
- [ ] `http-read`: read handlers backed by `pkg/client` (services, tasks, nodes,
  networks, configs, secrets).
- [ ] `http-read`: pagination, deterministic ordering, ETag/`If-None-Match`.
- [ ] `http-read`: Unix socket listener + `:4244` TLS leaf, `:4242` loopback
  default.
- [ ] `http-read`: CLI `-H`, `--ca`, `--token-file` path using the same client.
- [ ] `http-read`: acceptance script for remote read from a laptop-style client.

## Phase 2 — `auth`, `http-write`, `events`

- [ ] Spec: token file format, scopes, roles, selectors, audit, idempotency.
- [ ] `auth`: token mint/list/revoke, scoped middleware, 401/403 tests.
- [ ] `http-write`: POST/PATCH/DELETE handlers with overlay semantics.
- [ ] `http-write`: `If-Match` 428/412, `Idempotency-Key`, `wait`, `dryRun`.
- [ ] `events`: watch bridge, cursor, SSE + NDJSON, `cursor_expired`.
- [ ] `http-write`: audit records and rate limits.
- [ ] Phase 2 acceptance script (403/428/412/429 cases).

## Phase 3 — `broker`

- [ ] Spec: broker mTLS, task-keyed routes, streaming frames, limits.
- [ ] `broker`: manager→node channel and node registry.
- [ ] `broker`: `exec` (TTY rules, exit/signal mapping, `--exit-code=api`).
- [ ] `broker`: `logs` follow and `files` regular-file read/write.
- [ ] `broker`: `health` capability and `207 partial`.
- [ ] Phase 3 acceptance script (exec codes, truncated stream failure).

## Phase 4 — `distribution`

- [ ] Spec: snapshot experimental gate, SDK generation, conformance suite.
- [ ] `distribution`: snapshot create/list/restore/delete behind a capability.
- [ ] `distribution`: generated Go/Python/Node clients and thin wrappers.
- [ ] `distribution`: conformance suite against an in-process mock + lab job.
- [ ] `distribution`: executable docs examples in CI and CI image.
