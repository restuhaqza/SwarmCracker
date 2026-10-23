# Capability Map: Programmatic API v1

Status: approved for execution by the owner's "selesaikan semua tasks" request.
Source of truth: `docs/plans/api-contract-v1-proposal.md` (normative contract).

| Module id | Responsibility | Depends on | Phase |
| --- | --- | --- | --- |
| `contract` | `pkg/api/v1` DTOs, envelope, error codes, OpenAPI, golden fixtures | — | 0 |
| `client-boundary` | `pkg/client` is the only control-client boundary; legacy adapter is temporary | `contract` | 0 |
| `http-read` | HTTP/JSON server on Unix socket + `:4244`, `/v1/version`, read endpoints, pagination, ETag, TLS | `contract`, `client-boundary` | 1 |
| `auth` | Bearer tokens, scopes, roles, task selectors, audit | `http-read` | 2 |
| `http-write` | Create/overlay/delete, `If-Match`, idempotency, wait, dry run | `auth` | 2 |
| `events` | Watch cursor, SSE/NDJSON, `cursor_expired` | `http-write` | 2 |
| `broker` | Node broker, `exec`, `logs`, `files`, `health` capability | `auth` | 3 |
| `distribution` | Snapshots (experimental), generated SDKs, conformance suite, docs examples, CI image | `broker`, `events` | 4 |

Dependency direction is one-way. `contract` and `client-boundary` are done except
the final allowlist removal; later modules must not introduce a second source of
truth.

Build order: `contract` → `client-boundary` → `http-read` → `auth` →
`http-write` → `events` → `broker` → `distribution`.

Each module gets its own spec, plan and task list under `docs/plans/` and
`tasks/`. Implementation lands in small PRs, one module slice at a time.
