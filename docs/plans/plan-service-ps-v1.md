# Plan: `service ps` v1 envelope (phase 0, slice 5)

Spec: [`spec-service-ps-v1.md`](./spec-service-ps-v1.md)
Base: `26c4c9f` (service inspect merged).

## Global constraints

- Go 1.26; no new dependencies.
- Reuse `pkg/api/v1.TaskList`; no new wire type.
- The new path goes through `pkg/client`; legacy `--format json` stays untouched.
- Every task commits on the current branch and leaves the tree formatted.

## Task 1 — client service-task list

**Files:** `pkg/client/client.go`, `pkg/client/task_mapping_test.go`

1. Factor the shared task mapping into `Client.listTasks`.
2. Add `Client.ListServiceTasks(ctx, serviceID)` with a `ServiceIDs` filter.
3. Test the request filter, mapping, empty result and node-lookup skip.

**Verification:** `go test ./pkg/client`

## Task 2 — CLI contract path

**Files:** `cmd/swarmcracker/cmd_service.go`,
`cmd/swarmcracker/service_ps_contract_test.go`

1. Add `-o json|name` dispatch; keep `-o table` on the legacy path.
2. Reject `-o` plus `--format`, `-o json` plus `--quiet`, and `--health-port`
   in the v1 modes.
3. Resolve the service with `resolveV1ServiceRef`, fetch tasks through
   `ListServiceTasks`, apply `filterV1Tasks`, and emit `TaskList` or IDs.
4. Test JSON, filtering, name mode, resolution errors, RPC errors and flag
   combinations.

**Verification:** focused Linux `cmd/swarmcracker` test binary.

## Task 3 — OpenAPI, docs and changelog

**Files:** `api/openapi.yaml`, `pkg/api/v1/openapi_test.go`,
`docs-site/src/content/docs/reference/cli.md`, `CHANGELOG.md`

1. Add `/v1/services/{id}/tasks` returning `TaskList`.
2. Assert the path exists in the OpenAPI test.
3. Document the v1 modes and the legacy boundary.
4. Add an Unreleased entry.

**Verification:** `go test ./pkg/api/v1`, `cd docs-site && npm run verify`.
