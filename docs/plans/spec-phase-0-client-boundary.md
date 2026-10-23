# Spec: phase 0 client-boundary completion

Module id: `client-boundary`. Parent: `docs/plans/api-v1-capability-map.md`.

## Objective

`pkg/client` becomes the only package that owns `api.ControlClient`. Command
code may keep raw SwarmKit values for frozen legacy output through the
`pkg/client/legacy` adapter, but no command file references the raw control
client type. Success means the guard allowlist is empty, all existing command
output is unchanged, and `make api-check` passes.

## Commands

```bash
make test
make api-check
GOOS=linux GOARCH=amd64 go test -c ./cmd/swarmcracker -o /tmp/swarmcracker.test
docker run --rm -v "$PWD":/src -v /tmp:/test -w /src/cmd/swarmcracker \
  alpine:3.20 /test/swarmcracker.test
docker run --rm -v "$PWD":/src -w /src golangci/golangci-lint:v2.13.2 \
  golangci-lint run ./...
```

## Project structure

- `pkg/client` — typed v1 client and SwarmKit mapping.
- `pkg/client/legacy` — transitional adapter for raw legacy paths; deleted when
  legacy JSON reaches N+2.
- `cmd/swarmcracker` — command surface; no `api.ControlClient` reference.
- `pkg/api/v1` — DTOs and OpenAPI fixtures.

## Code style

The adapter exposes the narrowest type that keeps legacy code compiling:

```go
// pkg/client/legacy/client.go
type ControlClient = api.ControlClient

func NewControlClient(conn *grpc.ClientConn) ControlClient {
    return api.NewControlClient(conn)
}
```

Command signatures use `legacy.ControlClient`; request/response structs remain
`api.*` until the legacy renderer is removed.

## Testing strategy

- Guard test: `cmd/swarmcracker/client_boundary_test.go` fails on any
  `api.ControlClient` reference outside `pkg/client`, and fails if the allowlist
  is stale.
- Full Linux `cmd/swarmcracker` suite, because Darwin skips Linux-only tests.
- `make api-check` for the v1 contract.
- `golangci-lint v2.13.2`.

## Boundaries

- Always: preserve legacy `--format json` bytes; keep redaction; run boundary +
  full Linux command tests.
- Ask first: changing a legacy JSON shape, deleting the legacy adapter, or
  changing public CLI flags.
- Never: add new `api.ControlClient` use to a command, expose registry
  credentials, or weaken the guard.

## Success criteria

1. `legacyControlClientFiles` is empty and the guard passes.
2. No non-test file under `cmd/swarmcracker` or `pkg/api` references
   `api.ControlClient`.
3. Legacy command tests and `make api-check` pass on Linux.
4. `resolve.go`, `task_health.go`, and all read/write command paths use
   `pkg/client/legacy` for raw control access.
5. The client-boundary spec records the allowlist as empty.

## Open questions

None. The owner approved the capability map and the phase order.
