# Spec: client-boundary migration (phase 0, final item)

Status: in progress. Guard landed; migration remains.

## Objective

Make `pkg/client` the only package that imports `api.ControlClient`, while
preserving the legacy command output until its N+2 removal window.

Success means:

- A non-test file under `cmd/swarmcracker` or `pkg/api` that references
  `api.ControlClient` fails CI.
- The allowlist below shrinks to empty as each legacy path is migrated.
- Contract paths continue to use the v1 `pkg/client` methods.
- Legacy `--format json` shapes and redaction are unchanged during migration.

## Current allowlist

The allowlist is **empty**. Legacy command paths reach the raw control surface
through `pkg/client/legacy`; `pkg/client` and `pkg/client/legacy` are the only
packages that may reference `api.ControlClient`. `pkg/api` has no
control-client reference and is guarded separately.

## Migration result

1. **Read-only helpers.** `resolve.go` moved to `pkg/client/legacy`; the
   remaining raw read paths use the `legacy.ControlClient` adapter.
2. **Write paths.** Service create/update/scale/remove, node availability,
   config/secret create/remove and network create/remove all use the same
   adapter.
3. **Allowlist deleted.** `client_boundary_test.go` now enforces an empty set.

The legacy JSON shapes are frozen; the adapter exposes raw results internally,
but no command file names `api.ControlClient`. The adapter is deleted when the
legacy JSON output reaches its N+2 removal window.

## Verification

```bash
GOOS=linux GOARCH=amd64 go test -c ./cmd/swarmcracker -o /tmp/swarmcracker.test
docker run --rm -v "$PWD":/src -v /tmp:/test -w /src/cmd/swarmcracker \
  alpine:3.20 /test/swarmcracker.test -test.run 'Test.*ControlClientBoundary' -test.v
make api-check
```
