# Plan: config + secret list v1 envelopes (phase 0, slice 6)

Spec: [`spec-config-secret-list-v1.md`](./spec-config-secret-list-v1.md)
Base: `569597b` (service ps merged).

## Global constraints

- Go 1.26; no new dependencies.
- Metadata only: config and secret payloads never enter a v1 document.
- Legacy `--format json`, table and `--quiet` keep their behavior.
- Every task commits on the current branch and leaves the tree formatted.

## Task 1 — DTOs and OpenAPI

**Files:** `pkg/api/v1/config.go`, `pkg/api/v1/secret.go`,
`pkg/api/v1/config_test.go`, `pkg/api/v1/secret_test.go`,
`pkg/api/v1/testdata/config-list-empty.json`,
`pkg/api/v1/testdata/secret-list-empty.json`, `api/openapi.yaml`,
`pkg/api/v1/openapi_test.go`

1. Add `Config`, `ConfigList`, `Secret`, `SecretList` and non-nil constructors.
2. Add golden fixtures and schema tests.
3. Add `/v1/configs`, `/v1/secrets`, `ConfigList` and `SecretList`.

**Verification:** `go test ./pkg/api/v1`

## Task 2 — client mapping

**Files:** `pkg/client/client.go`, `pkg/client/named_mapping.go`,
`pkg/client/named_mapping_test.go`, `pkg/client/mapping_test.go`

1. Extend `controlClient` with `ListConfigs` and `ListSecrets`.
2. Map metadata, labels, timestamps, revision and secret driver.
3. Sort by creation time then ID; skip nil entries; return non-nil empty slices.
4. Test mapping, ordering, empty and RPC failures.

**Verification:** `go test ./pkg/client`

## Task 3 — CLI paths and docs

**Files:** `cmd/swarmcracker/cmd_config.go`,
`cmd/swarmcracker/cmd_secret.go`,
`cmd/swarmcracker/config_secret_contract_test.go`,
`docs-site/src/content/docs/reference/cli.md`, `CHANGELOG.md`

1. Add `-o json|name` dispatch; keep `-o table` legacy.
2. Reject `-o` + `--format`, `--quiet` with JSON, and unsupported formats.
3. Emit `ConfigList` / `SecretList` or IDs.
4. Test documents, name mode, errors and flag combinations.
5. Document the metadata-only contract and changelog entry.

**Verification:** focused Linux test binary, lint, docs build.
