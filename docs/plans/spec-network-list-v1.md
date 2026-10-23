# Spec: `network ls` v1 envelope (phase 0, slice 7)

Status: ready for implementation. Normative shape:
[`api-contract-v1-proposal.md`](./api-contract-v1-proposal.md), §5.6.

## Objective

Add `swarmcracker network ls -o json|name` using the v1 `NetworkList`,
while keeping the legacy `--format json` raw array stable.

Success means:

- Empty and populated lists emit `NetworkList` with `items: []` when empty and
  `next: null`.
- Each item carries identity, revision, creation time, driver, the first IPAM
  pool (`subnet`/`gateway`), labels and `attachable`.
- The built-in `ingress` and `docker_gwbridge` networks are excluded, matching
  the legacy table.
- `-o name` prints one network ID per line; `--quiet` is replaced by `-o name`.
- RPC failures are typed error documents with the documented exit code.
- Legacy `--format json` still emits the raw network array.
- The new path goes through `pkg/client`.

## Assumptions

1. `scope` is intentionally absent: SwarmKit's network scope is a constant in
   this deployment.
2. Only the first non-empty IPAM pool is emitted; additional pools are not part
   of the v1 shape.
3. Lists are ordered by `createdAt` ascending, then ID ascending.
4. `-o table` stays on the legacy path; `-o` and `--format` cannot be combined.

## Commands

```bash
go test ./pkg/api/v1 ./pkg/client ./pkg/output
GOOS=linux GOARCH=amd64 go build ./cmd/swarmcracker
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c ./cmd/swarmcracker -o /tmp/swarmcracker.test
docker run --rm -v /tmp:/work -w /work alpine:3.20 /work/swarmcracker.test -test.run 'TestNetwork' -test.v
cd docs-site && npm run verify
```

## Project structure

```text
pkg/api/v1/network.go, network_test.go
pkg/api/v1/testdata/network-list-empty.json
pkg/client/network_mapping.go, network_mapping_test.go
pkg/client/client.go                 ListNetworks
cmd/swarmcracker/cmd_network_admin.go
cmd/swarmcracker/network_contract_test.go
api/openapi.yaml                     /v1/networks, Network, NetworkList
pkg/api/v1/openapi_test.go
docs-site/src/content/docs/reference/cli.md
CHANGELOG.md
```
