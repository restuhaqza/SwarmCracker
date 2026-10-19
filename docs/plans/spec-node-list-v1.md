# Spec: `node ls` v1 envelope (phase 0, slice 3)

Status: proposed for implementation. Normative source for the document shape:
[`api-contract-v1-proposal.md`](./api-contract-v1-proposal.md), §5.5 and §6.

## Objective

Add `swarmcracker node ls -o json` and `-o name` using the v1 contract, while
keeping the existing `--format json` output stable for scripts.

Success means:

- An empty cluster in `-o json` prints a valid `NodeList` envelope with
  `items: []` and `next: null`, exit 0.
- A populated list carries identity, hostname, role, membership, availability,
  state, address, labels, resources, manager status and agent version when the
  source has them.
- `-o name` prints one node ID per line and honors the same filters as `-o json`.
- A failed RPC is a typed error document on stderr in `-o json` mode and uses
  the documented exit code.
- `--format json` still emits the legacy node array, except that empty and
  filtered-empty results are now `[]` instead of prose or `null`.
- No new `api.ControlClient` use is introduced in `cmd/`; the new path goes
  through `pkg/client`.

## Assumptions I am making

1. This is the local-manager CLI path (phase 0). No HTTP, reader tokens or
   remote transport are added in this slice.
2. `role` is `spec.desiredRole` (the only role SwarmKit exposes on the node
   spec), lowercased; `membership` and `availability` are the spec values.
3. `address` is the manager-observed `status.addr`, not the node's advertised
   raft address.
4. `resources.cpus` is a JSON number (`NanoCPUs / 1e9`) so fractional CPUs are
   not truncated; `memoryBytes` is an integer.
5. `manager` is present only when `ManagerStatus != nil`; its `leader` and
   `reachable` fields are always present, `address` and `raftId` are optional.
6. The v1 list is ordered by `createdAt` ascending, then `id` ascending. The
   legacy `--format` path keeps SwarmKit's order.
7. `-o name` replaces `--quiet`; combining `-o json`/`-o name` with `--quiet`
   is a usage error.
8. `-o ndjson` is not supported for `node ls`; it is a usage error.
9. The new path supports `role=`, `membership=`, `availability=`, `state=`,
   `id=` and `hostname=` filters. Unknown filter keys are ignored, matching the
   legacy `filterNodes` behavior. Values are case-insensitive.
10. `--format json` with a filter that matches nothing emits `[]`, not `null`.
    This is a bug fix covered by the empty-list requirement.

## Tech stack

- Go 1.26, module `github.com/restuhaqza/swarmcracker`
- Cobra for the command surface
- SwarmKit v2 `api` types for the source data
- `testify` for assertions
- `kin-openapi` for the schema test

## Commands

```bash
# Focused unit tests
go test ./pkg/api/v1 ./pkg/client ./pkg/output

# Linux build (the host is macOS; several packages are Linux-only)
GOOS=linux GOARCH=amd64 go build ./cmd/swarmcracker

# Command tests on a macOS host: compile for Linux, run in a container
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c ./cmd/swarmcracker -o cmd/swarmcracker/swarmcracker.test
docker run --rm -v "$PWD/cmd/swarmcracker":/work -w /work alpine:3.20 \
  /work/swarmcracker.test -test.run 'TestNode|TestLegacyNode' -test.v

# Formatting and docs
gofmt -w <changed files>
cd docs-site && npm run verify
```

## Project structure

```text
pkg/api/v1/node.go                 v1 Node, NodeList, NodeDocument, NodeManager types
pkg/api/v1/node_test.go            envelope, fixture and manager-omission tests
pkg/api/v1/testdata/node-list-empty.json
pkg/api/v1/openapi_test.go         NodeList/Node schema validation
api/openapi.yaml                   NodeList/Node/NodeResources/NodeManager schemas + /v1/nodes
pkg/client/node_mapping.go         SwarmKit Node -> v1 Node mapping
pkg/client/client.go               public Client.ListNodes
pkg/client/node_mapping_test.go    mapping, ordering, manager/resources tests
cmd/swarmcracker/cmd_node.go       -o json|name path, empty/filtered-empty fix
cmd/swarmcracker/node_contract_test.go
docs-site/src/content/docs/reference/cli.md
CHANGELOG.md
```

## Code style

Follow the existing v1/task and output packages. A list constructor always
materializes an empty slice:

```go
func NewNodeList(items []Node) NodeList {
	if items == nil {
		items = []Node{}
	}
	return NodeList{
		SchemaVersion: SchemaVersion,
		Kind:          KindNodeList,
		Items:         items,
		Next:          nil,
	}
}
```

Mapping stays in `pkg/client`; `pkg/api/v1` must not import SwarmKit. Enums are
lowercase strings; timestamps are UTC RFC 3339 with nanoseconds; absent optional
values are omitted, never `null` except the document's `next`.

## Testing strategy

- **`pkg/api/v1`**: table/envelope tests, empty golden fixture, manager omission,
  OpenAPI validation of empty and full lists.
- **`pkg/client`**: fake control client covering `ListNodes`; assert all mapped
  fields, manager/resources/agent version, ordering, empty list.
- **`cmd/swarmcracker`**: fake `nodeLister`; assert empty `-o json`, filtered
  JSON, `-o name`, typed error mapping, legacy empty/filtered-empty `[]`, legacy
  table prose. Run in the Linux container because the package imports Linux-only
  code.
- **Regression**: existing `node ls` tests and the full `cmd/swarmcracker` suite
  must stay green.

## Boundaries

- **Always**: run `gofmt`, run the focused tests, preserve the legacy shape
  except the empty/filtered-empty fix, keep the v1 envelope non-null.
- **Ask first**: changing any other legacy field, adding a dependency, changing
  node write commands, touching `node inspect`.
- **Never**: add HTTP/write endpoints, change `schemaVersion`, import
  `api.ControlClient` into the new command path, expose credentials or secrets.

## Success criteria

1. `node ls -o json` on a fake empty list emits exactly
   `{"schemaVersion":1,"kind":"NodeList","items":[],"next":null}`.
2. A non-empty list validates against `api/openapi.yaml` and includes `id`,
   `hostname`, `revision`, `role`, `membership`, `availability`, `state`,
   `address`, `labels`, `resources` and `manager` when present.
3. `node ls -o name --filter 'role=manager'` prints only manager IDs.
4. `node ls -o json` with a not-found/unavailable RPC emits one `Error` document
   and exits 3/6; the legacy path is unchanged.
5. `node ls --format json` on an empty or filtered-empty result prints `[]`, not
   `No nodes found` or `null`.
6. `go test ./pkg/api/v1 ./pkg/client ./pkg/output`, the Linux build, the
   container-run command tests and `docs-site npm run verify` pass.

## Open questions

None blocking. The assumptions above are the rulings for this slice.
