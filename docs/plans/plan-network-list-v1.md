# Plan: `network ls` v1 envelope (phase 0, slice 7)

Spec: [`spec-network-list-v1.md`](./spec-network-list-v1.md)
Base: `32101a9` (config/secret lists merged).

## Global constraints

- Go 1.26; no new dependencies.
- User-defined networks only; built-ins match the legacy table exclusion.
- Legacy `--format json`, table and `--quiet` keep their behavior.
- Every task commits on the current branch and leaves the tree formatted.

## Task 1 — DTO and OpenAPI

**Files:** `pkg/api/v1/network.go`, `pkg/api/v1/network_test.go`,
`pkg/api/v1/testdata/network-list-empty.json`, `api/openapi.yaml`,
`pkg/api/v1/openapi_test.go`

1. Add `Network`, `NetworkIPAM`, `NetworkList` and the non-nil constructor.
2. Add the empty golden fixture and schema test.
3. Add `/v1/networks`, `Network`, `NetworkIPAM`, `NetworkList`.

**Verification:** `go test ./pkg/api/v1`

## Task 2 — client mapping

**Files:** `pkg/client/network_mapping.go`,
`pkg/client/network_mapping_test.go`, `pkg/client/mapping_test.go`,
`pkg/client/client.go`

1. Extend `controlClient` with `ListNetworks`.
2. Map identity, revision, creation time, driver, first IPAM pool, labels and
   attachability.
3. Exclude nil entries and built-in ingress/gateway networks; sort by creation
   time then ID.
4. Test mapping, filtering, ordering, empty and RPC failure.

**Verification:** `go test ./pkg/client`

## Task 3 — CLI and docs

**Files:** `cmd/swarmcracker/cmd_network_admin.go`,
`cmd/swarmcracker/network_contract_test.go`,
`docs-site/src/content/docs/reference/cli.md`, `CHANGELOG.md`

1. Add `-o json|name`; keep `-o table` legacy.
2. Reject `-o` + `--format`, `--quiet` with JSON, and unsupported formats.
3. Emit `NetworkList` or IDs.
4. Test JSON, empty, name mode, error mapping and flag combinations.
5. Document the user-defined-only rule and the legacy boundary.

**Verification:** focused Linux test binary, lint, docs build.
