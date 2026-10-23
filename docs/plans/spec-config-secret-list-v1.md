# Spec: config + secret list v1 envelopes (phase 0, slice 6)

Status: ready for implementation. Normative shapes:
[`api-contract-v1-proposal.md`](./api-contract-v1-proposal.md), §5.6.

## Objective

Add `swarmcracker config ls -o json|name` and
`swarmcracker secret ls -o json|name` using the v1 metadata envelopes, while
keeping the legacy `--format json` lists stable.

Success means:

- Empty and populated lists emit `ConfigList` / `SecretList` with `items: []`
  when empty and `next: null`.
- Config documents contain `id`, `name`, `revision`, timestamps and labels.
- Secret documents add `driver`. Neither document ever contains `data`.
- `-o name` prints one ID per line.
- RPC failures are typed error documents with the documented exit code.
- `--format json`, `--quiet` and the table output keep their legacy behavior.
- The new paths go through `pkg/client`; no new `api.ControlClient` use in
  `cmd/swarmcracker`.

## Assumptions

1. Lists are ordered by `createdAt` ascending, then ID ascending.
2. Config values are never included: phase 0 issues no `configs:read-values`
   capability, so the metadata envelope is the whole contract.
3. Secret payloads are never included on any read path.
4. `-o table` stays on the legacy path; `-o json|name` is the contract path.
5. `-o` and `--format` cannot be combined; `--quiet` is replaced by `-o name`.

## Commands

```bash
go test ./pkg/api/v1 ./pkg/client ./pkg/output
GOOS=linux GOARCH=amd64 go build ./cmd/swarmcracker
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c ./cmd/swarmcracker -o /tmp/swarmcracker.test
docker run --rm -v /tmp:/work -w /work alpine:3.20 /work/swarmcracker.test -test.run 'TestConfig|TestSecret' -test.v
cd docs-site && npm run verify
```

## Project structure

```text
pkg/api/v1/config.go, secret.go       metadata DTOs and list constructors
pkg/api/v1/config_test.go, secret_test.go
pkg/api/v1/testdata/config-list-empty.json, secret-list-empty.json
pkg/client/named_mapping.go           SwarmKit -> v1 metadata mapping + ordering
pkg/client/named_mapping_test.go
pkg/client/client.go                  ListConfigs / ListSecrets
cmd/swarmcracker/cmd_config.go        -o json|name path
cmd/swarmcracker/cmd_secret.go        -o json|name path
cmd/swarmcracker/config_secret_contract_test.go
api/openapi.yaml                      Config/Secret lists and paths
pkg/api/v1/openapi_test.go
docs-site/src/content/docs/reference/cli.md
CHANGELOG.md
```

## Testing strategy

- **v1**: empty constructors, golden fixtures and OpenAPI validation.
- **client**: mapping, ordering, nil entries, empty results and RPC failures.
- **CLI**: envelope kind, label/driver fields, no `data`, name mode, typed
  errors and rejected `-o` / `--quiet` combinations.
