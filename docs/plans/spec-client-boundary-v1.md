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

These files still use the raw SwarmKit control client for legacy paths:

```text
cmd_config.go
cmd_network_admin.go
cmd_node.go
cmd_secret.go
cmd_service.go
cmd_task.go
task_health.go
```

`pkg/client` itself is the allowed implementation boundary. `pkg/api` has no
control-client reference and is guarded separately.

## Migration order

1. **Read-only helpers first.** `resolve.go`, `task_health.go`, then the list
   and inspect paths. These need client wrappers that keep raw SwarmKit fields
   for the legacy renderers.
2. **Write paths.** Service create/update/scale/remove, node availability,
   config/secret create/remove and network create/remove.
3. **Delete the allowlist.** When the last file moves, remove the map from
   `client_boundary_test.go`; the test then enforces an empty set.

Each step must keep `make api-check` and the full `cmd/swarmcracker` suite green.
The legacy JSON shapes are frozen; wrappers may expose raw results internally,
but no new command may import `api.ControlClient`.

## Verification

```bash
GOOS=linux GOARCH=amd64 go test -c ./cmd/swarmcracker -o /tmp/swarmcracker.test
docker run --rm -v "$PWD":/src -v /tmp:/test -w /src/cmd/swarmcracker \
  alpine:3.20 /test/swarmcracker.test -test.run 'Test.*ControlClientBoundary' -test.v
make api-check
```
