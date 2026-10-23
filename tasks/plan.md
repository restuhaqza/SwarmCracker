# Plan: API v1 delivery

Source: `docs/plans/api-v1-capability-map.md`,
`docs/plans/api-contract-v1-proposal.md`.

## Verification checkpoints

1. **After every slice:** focused package tests, Linux `cmd/swarmcracker` tests
   when the command surface changed, `golangci-lint v2.13.2`.
2. **After contract changes:** `make api-check` plus OpenAPI golden fixtures.
3. **Before merge:** CI green (`test`, `lint`, `build`), issue #102 updated.
4. **Before phase close:** the acceptance script for that phase passes; docs
   under `docs-site/src/content/docs/` describe the shipped surface.

## Sequencing

- **Phase 0 (`contract`, `client-boundary`)** — finish now. The only remaining
  item is the allowlist; it must reach empty without changing legacy output.
- **Phase 1 (`http-read`)** — add the server behind `pkg/client`, then wire the
  CLI `-H/--token` path. Read-only first; writes stay on the local socket.
- **Phase 2 (`auth`, `http-write`, `events`)** — tokens/roles, precondition and
  idempotency semantics, then watch.
- **Phase 3 (`broker`)** — streaming exec/logs/files and health through the
  manager; this is the XL slice and lands feature by feature.
- **Phase 4 (`distribution`)** — snapshots, SDK generation, conformance suite,
  docs examples in CI, CI image.

## Risks

| Risk | Mitigation |
| --- | --- |
| Legacy output changes during migration | Golden/frozen tests; adapter only changes the type boundary. |
| HTTP server becomes a second source of truth | All reads/writes go through `pkg/client` → SwarmKit. |
| Auth state is manager-local | v1 is single-manager; HA remains phase 2. |
| Broker expands blast radius | Capability-gated, task selectors, mTLS, 207 partial. |
| Phase 3 delays everything | Ship phases 0–2 independently; broker is additive. |
