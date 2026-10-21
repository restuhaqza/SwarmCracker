# Spec: `service inspect` v1 envelope (phase 0, slice 4)

Status: ready for implementation. Normative document shape:
[`api-contract-v1-proposal.md`](./api-contract-v1-proposal.md), §5.3.

## Objective

Add `swarmcracker service inspect -o json` using the v1 `ServiceDocument`,
while keeping the existing `--format json` output stable for scripts.

Success means:

- A resolved service in `-o json` prints one document with `schemaVersion: 1`,
  `kind: "Service"`, identity, spec, and observed status.
- The document never contains `registryAuth`.
- The reference resolver accepts a full ID, an exact name, or a unique ID
  prefix, in that order.
- Not found is exit 3; an ambiguous prefix is exit 3; a bad flag is exit 2.
- An RPC failure is a typed error document on stderr in `-o json` mode.
- Plain `service inspect` and `--format json` keep the legacy SwarmKit shape,
  including the existing credential redaction.
- The new path goes through `pkg/client`; no new `api.ControlClient` use in
  `cmd/swarmcracker`.

## Assumptions

1. This is the local-manager CLI path (phase 0); no HTTP or remote transport.
2. Only `-o json` is defined for `service inspect` in this slice. `-o name`
   and `-o table` are usage errors; `-o` cannot be combined with `--format`.
3. `-o json` ignores `--pretty`; the v1 writer is always indented.
4. Status counts come from `ListTasks` filtered to the resolved service. A task
   read failure fails the document rather than emitting a partial status.
5. `status.message` remains empty: SwarmKit's `api.Service` has no top-level
   service-status message to copy.
6. Jobs remain out of scope for v1 (`mode` is still mapped, but no new job
   semantics are added in this slice).

## Commands

```bash
go test ./pkg/api/v1 ./pkg/client ./pkg/output
GOOS=linux GOARCH=amd64 go build ./cmd/swarmcracker
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c ./cmd/swarmcracker -o /tmp/swarmcracker.test
docker run --rm -v /tmp:/work -w /work alpine:3.20 /work/swarmcracker.test -test.run 'TestInspectService' -test.v
cd docs-site && npm run verify
```

## Project structure

```text
pkg/api/v1/service_test.go          ServiceDocument envelope/fixture coverage
pkg/api/v1/openapi_test.go          ServiceDocument schema validation
api/openapi.yaml                    /v1/services/{id} + ServiceDocument
pkg/client/client.go                controlClient.GetService + Client.GetService
pkg/client/mapping_test.go          fake ControlClient and GetService tests
cmd/swarmcracker/cmd_service.go     -o json path and resolver
cmd/swarmcracker/service_inspect_contract_test.go
docs-site/src/content/docs/reference/cli.md
CHANGELOG.md
```

## Testing strategy

- **`pkg/client`**: full mapping, filtered task counts, nil service and RPC
  failures.
- **`cmd/swarmcracker`**: envelope shape, exact/name/prefix resolution,
  not-found and ambiguous errors, task failure mapping, unsupported `-o`.
- **OpenAPI**: validate a populated `ServiceDocument` and assert the path
  parameter.
- **Regression**: legacy `--format json` still redacts credentials and keeps
  the raw SwarmKit shape.
