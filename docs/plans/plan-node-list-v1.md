# Plan: `node ls` v1 envelope (phase 0, slice 3)

Spec: [`spec-node-list-v1.md`](./spec-node-list-v1.md)
Base: `feat/api-contract-v1-phase0` after the task-list final fix (`b432c06`).

## Global constraints

- Go 1.26; module `github.com/restuhaqza/swarmcracker`; no new dependencies.
- `schemaVersion` stays `1`; `kind` is `Node` / `NodeList`; `items` is never
  `null`; `next` is present and `null` until pagination exists.
- Lowercase enum strings, camelCase fields, UTC RFC 3339 nanosecond timestamps,
  integer milliseconds, `omitempty` for absent optionals.
- Do not import `api.ControlClient` in `cmd/swarmcracker` for the new path; use
  `pkg/client`.
- Legacy `--format json` keeps its array shape and redaction; the only legacy
  change is empty/filtered-empty output `[]` instead of prose or `null`.
- Preserve the current `--all` behavior where it exists (node ls has none).
- Every task commits on the current branch and leaves the tree formatted.

## Preflight scan

| Pair | Shared surface | Ruling |
| --- | --- | --- |
| T1 ↔ T2 | `pkg/api/v1.Node*` | T1 first; T2 consumes. Serial. |
| T2 ↔ T3 | `pkg/client.Client.ListNodes`, `controlClient` | T2 first; T3 consumes. Serial. |
| T3 ↔ T4 | CLI behavior documented by T4 | T3 first; T4 documents shipped behavior. Serial. |
| T1 internal | `NodeManager` shape | `leader`/`reachable` always present; `address`/`raftId` optional. |
| T2 internal | v1 order vs legacy order | v1 sorts by `createdAt`/`id`; legacy untouched. |
| T3 internal | unknown filter keys | Ignore, matching legacy `filterNodes`. |

No contradictions. No parallel execution: each task depends on the previous one.

## Task 1 — v1 Node DTO, envelope, OpenAPI and fixtures

**Goal:** Define the pure wire types and make the schema test cover them.

**Files:**
- `pkg/api/v1/node.go` (new)
- `pkg/api/v1/node_test.go` (new)
- `pkg/api/v1/testdata/node-list-empty.json` (new)
- `pkg/api/v1/openapi_test.go` (extend)
- `api/openapi.yaml` (extend)

**Interfaces:**
```go
type Node struct {
    ID, Hostname, Revision string
    CreatedAt, UpdatedAt   string
    Role, Membership       string
    Availability, State    string
    Address                string
    Labels                 map[string]string
    Resources              *NodeResources
    Manager                *NodeManager
    AgentVersion           string
}

type NodeResources struct {
    CPUs        float64 `json:"cpus,omitempty"`
    MemoryBytes int64   `json:"memoryBytes,omitempty"`
}

type NodeManager struct {
    Leader    bool   `json:"leader"`
    Reachable bool   `json:"reachable"`
    Address   string `json:"address,omitempty"`
    RaftID    uint64 `json:"raftId,omitempty"`
}

type NodeList struct {
    SchemaVersion int
    Kind          string
    Items         []Node
    Next          *string
    Warnings      []Warning
    Partial       []Partial
}

type NodeDocument struct { SchemaVersion int; Kind string; Node }

func NewNodeList(items []Node) NodeList
func NewNodeDocument(node Node) NodeDocument
```

**Steps:**
1. Add the types with JSON tags from §5.5.
2. `NewNodeList(nil)` must produce `items: []`; `Next` stays `nil`.
3. Add the empty golden fixture.
4. Extend `api/openapi.yaml` with `NodeList`, `Node`, `NodeResources`,
   `NodeManager` and a `/v1/nodes` GET skeleton. Parameters: `id`, `hostname`,
   `label`, `role`, `membership`, `state`, `limit`, `cursor`.
5. Extend `openapi_test.go` to validate the empty and a fully populated
   `NodeList`; assert a worker omits `manager`.

**Verification:** `go test ./pkg/api/v1`

**Depends on:** none.

## Task 2 — `pkg/client.ListNodes`

**Goal:** Map SwarmKit nodes to `v1.Node` and expose a contract-ordered list.

**Files:**
- `pkg/client/client.go` (add public `ListNodes`)
- `pkg/client/node_mapping.go` (new)
- `pkg/client/node_mapping_test.go` (new)
- `pkg/client/mapping_test.go` (fake already has `ListNodes`; extend only if needed)

**Interfaces:**
```go
func (c *Client) ListNodes(ctx context.Context) ([]v1.Node, error)
```

**Mapping rules:**
- `id`, `revision`, timestamps from `Meta`.
- `hostname` from `Description.Hostname`; omit when empty.
- `role` from `Spec.DesiredRole`; `membership` from `Spec.Membership`;
  `availability` from `Spec.Availability`; all lowercased.
- `state` from `Status.State`, lowercased.
- `address` from `Status.Addr`.
- `labels` from `Spec.Annotations.Labels`.
- `resources` from `Description.Resources`: `cpus = NanoCPUs / 1e9`,
  `memoryBytes`; omit the object when both are zero.
- `manager` from `ManagerStatus` when non-nil: `leader`, `reachable`
  (`Reachability == api.RaftMemberStatus_REACHABLE`), `address`, `raftId`.
- `agentVersion` from `Description.Engine.EngineVersion` when present.
- Sort by `createdAt`, then `id`; nil timestamps sort first.
- Empty list returns `[]v1.Node{}`.

**Tests:** every field, worker without manager, manager with leader/reachable,
resources/agent version, ordering, empty list.

**Verification:** `go test ./pkg/client`

**Depends on:** Task 1.

## Task 3 — CLI `node ls -o json|name`, legacy empty/filtered fix, tests

**Goal:** Wire the new client into the command without adding another
`api.ControlClient` path.

**Files:**
- `cmd/swarmcracker/cmd_node.go`
- `cmd/swarmcracker/node_contract_test.go` (new)

**Interfaces:**
```go
type nodeLister interface {
    ListNodes(context.Context) ([]v1.Node, error)
}

func listNodesContract(format, filter string) error
func listNodesContractWithClient(ctx context.Context, l nodeLister, w io.Writer,
    format, filter string) error
func filterV1Nodes(nodes []v1.Node, filter string) []v1.Node
```

**Command behavior:**
- `-o table` → existing `listNodes` path.
- `-o json` / `-o name`:
  - reject a changed `--format` (`usage`);
  - reject `--quiet` (`usage`, use `-o name`);
  - build `pkg/client.New(client.DefaultConfig())`;
  - call `ListNodes`, filter the DTOs, then emit `v1.NewNodeList` or IDs.
- `-o ndjson` → `usage`.
- `filterV1Nodes` supports `role=`, `membership=`, `availability=`, `state=`,
  `id=`, `hostname=`; values case-insensitive; unknown keys ignored.
- Legacy empty branch: `--format json` prints `[]`; table still prints
  `No nodes found`.
- After legacy filtering, if the result is empty, `--format json` prints `[]`
  instead of `null`.
- New-path RPC errors use `output.FromGRPC`.

**Tests:** empty JSON envelope, filtered JSON (role/state), name mode, typed
error, legacy empty/filtered-empty `[]`, legacy table prose, mode validation
(ndjson, quiet, format conflict), filter table.

**Verification:** Linux compile + Docker run of `TestNode|TestLegacyNode` then
the full `cmd/swarmcracker` suite.

**Depends on:** Task 2.

## Task 4 — Docs, changelog, final verification

**Goal:** Document the new mode and record the slice.

**Files:**
- `docs-site/src/content/docs/reference/cli.md`
- `CHANGELOG.md`
- `AGENTS.md` (phase-0 row: add NodeList)
- `cmd/swarmcracker/cmd_node.go` (help/examples only, if needed)

**Steps:**
1. Extend the `node ls` reference with `-o json`/`-o name`, filters, empty-list
   guarantee and the legacy `null` fix.
2. Add an Unreleased changelog line.
3. Update the AGENTS.md API v1 row to `ServiceList, TaskList, NodeList`.
4. Run docs verification and the focused + command test suites.
5. Report deferred minors for the final review.

**Verification:**
```bash
gofmt -w <changed Go files>
cd docs-site && npm run verify
go test ./pkg/api/v1 ./pkg/client ./pkg/output
GOOS=linux GOARCH=amd64 go build ./cmd/swarmcracker
# cmd tests in the Linux container, as in Task 3
```

**Depends on:** Task 3.
