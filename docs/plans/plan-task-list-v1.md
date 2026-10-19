# Plan: `task ls` v1 envelope (phase 0, slice 2)

Spec: [`spec-task-list-v1.md`](./spec-task-list-v1.md)
Base: `feat/api-contract-v1-phase0` after the slice-1 checkpoint.

## Global constraints

- Go 1.26; module `github.com/restuhaqza/swarmcracker`; no new dependencies.
- `schemaVersion` stays `1`; `kind` is `Task` / `TaskList`; `items` is never
  `null`; `next` is present and `null` until pagination exists.
- Lowercase enum strings, camelCase fields, UTC RFC 3339 nanosecond timestamps,
  integer milliseconds, `omitempty` for absent optionals.
- Do not import `api.ControlClient` in `cmd/swarmcracker` for the new path; use
  `pkg/client`.
- Legacy `--format json` keeps its array shape, redaction, `health` and
  `live_health`; the only legacy change is empty output `[]` instead of prose.
- Phase 0 `-o json` does not populate `health`; `--health-port` is a usage error
  in that mode.
- Preserve the current `--all` behavior exactly (it is currently a no-op).
- Every task commits on the current branch and leaves the tree formatted.

## Preflight scan

| Pair | Shared surface | Ruling |
| --- | --- | --- |
| T1 ↔ T2 | `pkg/api/v1.Task*` | T1 first; T2 only consumes the types. Serial. |
| T2 ↔ T3 | `pkg/client.Client.ListTasks`, `controlClient` | T2 first; T3 only consumes `ListTasks`. Serial. |
| T3 ↔ T4 | CLI behavior documented in T4 | T4 after T3 so docs describe what shipped. Serial. |
| T1 internal | `Task` and `TaskHealth` schema | `TaskHealth` is defined but unpopulated in phase 0; OpenAPI includes it as optional. |
| T2 internal | task ordering vs legacy order | v1 sorts by `createdAt`/`id`; legacy path is not touched. |
| T3 internal | `--all` no-op vs “show all” wording | Preserve legacy; note it in the report. Fixing is out of scope. |

No task contradicts another. No parallel execution: each task depends on the
previous one.

## Task 1 — v1 Task DTO, envelope, OpenAPI and fixtures

**Goal:** Define the pure wire types and make the schema test cover them.

**Files:**
- `pkg/api/v1/task.go` (new)
- `pkg/api/v1/task_test.go` (new)
- `pkg/api/v1/testdata/task-list-empty.json` (new)
- `pkg/api/v1/openapi_test.go` (extend)
- `api/openapi.yaml` (extend)

**Interfaces:**
```go
type Task struct {
    ID, Name, Revision               string
    ServiceID, ServiceName           string
    NodeID, NodeName                 string
    Slot                             uint64
    State, DesiredState              string
    Message, Error                   string
    CreatedAt, UpdatedAt             string
    Image                            string
    Addresses                        []string
    Ports                            []Port
    ExitCode                         *int32
    Health                           *TaskHealth
}

type TaskHealth struct {
    Status        string
    FailingStreak int
    Output        string
    Command       string
    CheckedAt     string
}

type TaskList struct {
    SchemaVersion int
    Kind          string
    Items         []Task
    Next          *string
    Warnings      []Warning
    Partial       []Partial
}

type TaskDocument struct { SchemaVersion int; Kind string; Task }

func NewTaskList(items []Task) TaskList
func NewTaskDocument(task Task) TaskDocument
```

**Steps:**
1. Add the types with the JSON tags from the spec/proposal (§5.4).
2. `NewTaskList(nil)` must produce `items: []`; `Next` stays `nil`.
3. Add the empty golden fixture.
4. Extend `api/openapi.yaml` with `TaskList`, `Task`, `TaskHealth` and a
   `/v1/tasks` GET skeleton reusing the `ServiceList` response pattern.
5. Extend `openapi_test.go` to validate the empty and a fully populated
   `TaskList`; assert a task with `Health == nil` omits the key.

**Verification:** `go test ./pkg/api/v1`

**Depends on:** none.

## Task 2 — `pkg/client.ListTasks`

**Goal:** Map SwarmKit tasks to `v1.Task`, including service/node names, and
return a contract-ordered list.

**Files:**
- `pkg/client/client.go` (extend `controlClient`, add `ListTasks`)
- `pkg/client/task_mapping.go` (new)
- `pkg/client/task_mapping_test.go` (new)
- `pkg/client/mapping_test.go` (fake gains `ListNodes`)
- `pkg/client/client_test.go` (only if the fake needs shared setup)

**Interfaces:**
```go
func (c *Client) ListTasks(ctx context.Context) ([]v1.Task, error)
```

`controlClient` gains:
```go
ListNodes(context.Context, *api.ListNodesRequest, ...grpc.CallOption) (*api.ListNodesResponse, error)
```

**Mapping rules:**
- `id`, `revision`, timestamps from `Meta`; `slot`, `serviceId`, `nodeId`,
  `state`, `desiredState`, `message`, `error`, `image` from the task.
- `serviceName` from `ServiceAnnotations.Name`.
- `nodeName` from `ListNodes` (`Description.Hostname`).
- `addresses` collects `task.Networks[].Addresses`.
- `ports` uses `Status.PortStatus.Ports`; fallback to `task.Endpoint.Ports`;
  target/protocol only, never `published`.
- `exitCode` only when the task is terminal and `Status.GetContainer()` exists.
- `health` is left nil in phase 0.
- `name` follows the spec’s derivation; omit when it cannot be derived.
- Sort by `createdAt`, then `id`.
- Empty service list returns `[]v1.Task{}` without a node call only if there
  are no tasks; otherwise `ListNodes` is required.

**Tests:** mapping of all fields, derived/supplied names, port fallback,
ordering, empty list, `ListNodes` failure propagation, health omitted.

**Verification:** `go test ./pkg/client`

**Depends on:** Task 1.

## Task 3 — CLI `task ls -o json|name`, legacy empty fix, tests

**Goal:** Wire the new client into the command without adding another
`api.ControlClient` path.

**Files:**
- `cmd/swarmcracker/cmd_task.go`
- `cmd/swarmcracker/task_contract_test.go` (new)
- `cmd/swarmcracker/api_contract_test.go` (only if a shared fake is cleaner)

**Interfaces:**
```go
type taskLister interface {
    ListTasks(context.Context) ([]v1.Task, error)
}

func listTasksContract(format, filter string, all bool, node, serviceID string) error
func listTasksContractWithClient(ctx context.Context, l taskLister, w io.Writer,
    format, filter string, all bool, node, serviceID string) error
func filterV1Tasks(tasks []v1.Task, filter, node, serviceID string, all bool) []v1.Task
```

**Command behavior:**
- `-o table` → existing `listTasks` path.
- `-o json` / `-o name`:
  - reject a changed `--format` (`usage`);
  - reject `--quiet` (`usage`, use `-o name`);
  - reject a changed `--health-port` (`usage`, phase 0 has no health);
  - build `pkg/client.New(client.DefaultConfig())`;
  - call `ListTasks`, filter the DTOs, then emit `v1.NewTaskList` or IDs.
- `-o ndjson` → `usage`.
- `filterV1Tasks` mirrors the legacy `state=`/`node=` behavior and the
  `--node`/`--service` flags; `all` is accepted but not used, matching the
  legacy no-op.
- Legacy empty branch: `--format json` prints `[]`; table still prints
  `No tasks found`.
- New-path RPC errors use `output.FromGRPC`.

**Tests:** empty JSON envelope, filtered JSON, name mode, typed error, legacy
empty `[]`, legacy table prose, health-port/quiet usage errors.

**Verification:**
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c ./cmd/swarmcracker -o cmd/swarmcracker/swarmcracker.test
docker run --rm -v "$PWD/cmd/swarmcracker":/work -w /work alpine:3.20 \
  /work/swarmcracker.test -test.run 'TestTask|TestLegacyTask' -test.v
rm -f cmd/swarmcracker/swarmcracker.test
```

**Depends on:** Task 2.

## Task 4 — Docs, changelog, minor polish and final verification

**Goal:** Document the new mode, record the slice, and close the file-local
user-facing minors promoted from Task 3.

**Files:**
- `docs-site/src/content/docs/reference/cli.md`
- `CHANGELOG.md`
- `cmd/swarmcracker/cmd_task.go` (help/examples and conflict ordering only)
- `cmd/swarmcracker/task_contract_test.go` (next-key assertion and one mode test)
- `docs/plans/api-contract-v1-proposal.md` only if a factual note is needed

**Steps:**
1. Extend the `task ls` reference with `-o json`/`-o name`, the health omission,
   service-name matching, and the empty-list guarantee.
2. Update the command help/examples: show `-o json` and `-o name`, say
   `--service` accepts an ID or name in the new modes.
3. Hoist the `--format` conflict check so `-o table --format json` is a usage
   error, matching `service ls`, and remove the duplicated legacy `listTasks`
   call in `RunE`.
4. Assert that `next` is present and null in the empty contract test, and add
   the `-o table --format json` usage case.
5. Add an Unreleased changelog line.
6. Run docs verification and the focused + command test suites.
7. Report any deferred minors for the final review.

**Verification:**
```bash
gofmt -w cmd/swarmcracker/cmd_task.go cmd/swarmcracker/task_contract_test.go
cd docs-site && npm run verify
go test ./pkg/api/v1 ./pkg/client ./pkg/output
GOOS=linux GOARCH=amd64 go build ./cmd/swarmcracker
# cmd tests in the Linux container, as in Task 3
```

**Depends on:** Task 3.
