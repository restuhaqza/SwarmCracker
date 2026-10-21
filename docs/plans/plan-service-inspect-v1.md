# Plan: `service inspect` v1 envelope (phase 0, slice 4)

Spec: [`spec-service-inspect-v1.md`](./spec-service-inspect-v1.md)
Base: `b5a80c1` (phase 0 slices 1–3 merged).

## Global constraints

- Go 1.26; no new dependencies.
- `schemaVersion` stays `1`; `kind` is `Service`; camelCase fields; timestamps
  UTC RFC 3339; integers in milliseconds.
- Do not add `api.ControlClient` to `cmd/swarmcracker`; the new path uses
  `pkg/client`.
- Legacy `--format json` keeps the raw SwarmKit array/object shape and its
  credential redaction.
- Every task commits on the current branch and leaves the tree formatted.

## Task 1 — client inspection path

**Files:**
- `pkg/client/client.go`
- `pkg/client/mapping_test.go`

**Steps:**
1. Add `GetService` to the narrow `controlClient` interface.
2. Add `Client.GetService(ctx, id)` that calls `GetService`, lists tasks filtered
   by `ServiceIDs`, and maps the service with `serviceFromSwarmkit`.
3. Return a NotFound gRPC status when the response has no service.
4. Extend the fake control client and add tests for counts, RPC failure and the
   nil-service case.

**Verification:** `go test ./pkg/client`

## Task 2 — CLI contract path

**Files:**
- `cmd/swarmcracker/cmd_service.go`
- `cmd/swarmcracker/service_inspect_contract_test.go` (new)

**Steps:**
1. Add `-o json` dispatch to `newServiceInspectCommand`; reject `-o` combined
   with `--format`, and reject formats other than `json`.
2. Add `inspectServiceContract` and `inspectServiceContractWithClient`.
3. Resolve exact ID, exact name, then unique prefix. Return typed `not_found`
   and `ambiguous` errors.
4. Map client RPC errors through `output.FromGRPC`; write
   `v1.NewServiceDocument`.
5. Test the envelope, resolution order, error codes, task failure and unsupported
   output.

**Verification:** focused Go tests and the Linux `cmd/swarmcracker` test binary.

## Task 3 — OpenAPI, docs and changelog

**Files:**
- `api/openapi.yaml`
- `pkg/api/v1/openapi_test.go`
- `docs-site/src/content/docs/reference/cli.md`
- `CHANGELOG.md`

**Steps:**
1. Add `/v1/services/{id}` and `ServiceDocument` (`allOf` Service + envelope).
2. Validate a populated document in `openapi_test.go`.
3. Document `service inspect -o json`, the resolution order and the legacy
   boundary.
4. Add an Unreleased entry.

**Verification:** `go test ./pkg/api/v1`, `cd docs-site && npm run verify`.
