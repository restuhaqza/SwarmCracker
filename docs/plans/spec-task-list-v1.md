# Spec: `task ls` v1 envelope (phase 0, slice 2)

Status: proposed for implementation. Normative source for the document shape:
[`api-contract-v1-proposal.md`](./api-contract-v1-proposal.md), §5.4 and §6.

## Objective

Add `swarmcracker task ls -o json` and `-o name` so the task list is available
to the same language-agnostic contract as `service ls`, while keeping the
existing `--format json` output stable for scripts.

Success means:

- An empty cluster in `-o json` prints a valid `TaskList` envelope with
  `items: []` and `next: null`, exit 0.
- A populated list carries the fields in §5.4 that phase 0 can source locally:
  identity, service/node names, state, desired state, image, addresses, ports
  and exit code.
- `-o name` prints one task ID per line and honors the same filters as `-o json`.
- A failed RPC is a typed error document on stderr in `-o json` mode and uses
  the documented exit code.
- `--format json` still emits the legacy redacted task array, except that an
  empty result is now `[]` instead of prose.
- No new `api.ControlClient` use is introduced in `cmd/`; the new path goes
  through `pkg/client`.

## Assumptions I am making

1. This is the local-manager CLI path (phase 0). No HTTP, reader tokens or
   remote transport are added in this slice.
2. `-o json` omits `health` in phase 0. Health is a phase-3 broker capability.
   The legacy `--format json` path keeps its existing `health` and
   `live_health` fields.
3. The existing `--all` behavior is preserved exactly, including its current
   no-op effect. Fixing that filter is a separate change; this slice must not
   silently change what scripts see.
4. Task `name` is `task.Annotations.Name` when set; otherwise
   `<serviceName>.<slot>.<first 12 chars of id>` when a slot exists, or
   `<serviceName>.<first 12 chars of id>` for slotless tasks. It is omitted when
   no service name is available.
5. `serviceName` comes from `Task.ServiceAnnotations.Name`. `nodeName` comes from
   one `ListNodes` call in `pkg/client`, not one call per task.
6. The v1 list is ordered by `createdAt` ascending, then `id` ascending. The
   legacy `--format` path keeps SwarmKit's order.
7. `-o name` replaces `--quiet`; combining `-o json`/`-o name` with `--quiet`
   is a usage error.
8. `-o ndjson` is not supported for `task ls` (the command has no stream); it is
   a usage error.
9. `--health-port` with `-o json`/`-o name` is a usage error, because phase 0
   never fetches live health. `--no-health` is accepted and has no effect.
10. A `ListNodes` failure fails the task list. The contract includes
    `nodeName`, so a caller that cannot read nodes cannot produce the document.

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

# Whole repo test (Linux CI target)
make test

# Linux build (the host is macOS; several packages are Linux-only)
GOOS=linux GOARCH=amd64 go build ./cmd/swarmcracker

# Command tests on a macOS host: compile for Linux, run in a container
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c ./cmd/swarmcracker -o cmd/swarmcracker/swarmcracker.test
docker run --rm -v "$PWD/cmd/swarmcracker":/work -w /work alpine:3.20 \
  /work/swarmcracker.test -test.run 'TestTask|TestLegacyTask' -test.v

# Formatting and docs
gofmt -w <changed files>
cd docs-site && npm run verify
```

## Project structure

```text
pkg/api/v1/task.go                 v1 Task, TaskList, TaskDocument types
pkg/api/v1/task_test.go            envelope, fixture and omission tests
pkg/api/v1/testdata/task-list-empty.json
pkg/api/v1/openapi_test.go         TaskList/Task schema validation
api/openapi.yaml                   TaskList/Task/TaskHealth schemas + /v1/tasks
pkg/client/task_mapping.go         SwarmKit Task -> v1 Task mapping
pkg/client/client.go               ListTasks + ListNodes in the narrow control interface
pkg/client/task_mapping_test.go    mapping, ordering, name derivation tests
cmd/swarmcracker/cmd_task.go       -o json|name path, empty-list fix
cmd/swarmcracker/task_contract_test.go
docs-site/src/content/docs/reference/cli.md
CHANGELOG.md
```

## Code style

Follow the existing v1/service and output packages. A list constructor always
materializes an empty slice:

```go
func NewTaskList(items []Task) TaskList {
	if items == nil {
		items = []Task{}
	}
	return TaskList{
		SchemaVersion: SchemaVersion,
		Kind:          KindTaskList,
		Items:         items,
		Next:          nil,
	}
}
```

Mapping stays in `pkg/client`; `pkg/api/v1` must not import SwarmKit. Enums are
lowercase strings; timestamps are UTC RFC 3339 with nanoseconds; durations are
milliseconds; absent optional values are omitted, never `null` except the
document's `next`.

## Testing strategy

- **`pkg/api/v1`**: table/envelope tests, empty golden fixture, health omission,
  `TaskDocument` kind, OpenAPI validation of empty and full lists.
- **`pkg/client`**: fake control client covering `ListTasks` and `ListNodes`;
  assert names, addresses, ports, exit code, ordering, empty list and node
  failure. No live cluster.
- **`cmd/swarmcracker`**: fake `taskLister`; assert empty `-o json`, filtered
  JSON, `-o name`, typed error mapping, legacy empty `[]`, legacy table prose.
  Run in the Linux container because the package imports Linux-only code.
- **Regression**: existing `task ls` tests and the full `cmd/swarmcracker` suite
  must stay green.

## Boundaries

- **Always**: run `gofmt`, run the focused tests, preserve the legacy shape
  except the empty-list fix, keep the v1 envelope non-null, redact credentials.
- **Ask first**: changing any other legacy field, adding a new dependency,
  changing filter semantics, touching `task inspect`.
- **Never**: add HTTP/write endpoints, populate `health` in phase 0, change
  `schemaVersion`, import `api.ControlClient` into the new command path, expose
  `registryAuth` or secret material.

## Success criteria

1. `task ls -o json` on a fake empty list emits exactly
   `{"schemaVersion":1,"kind":"TaskList","items":[],"next":null}`.
2. A non-empty list validates against `api/openapi.yaml` and includes `id`,
   `serviceId`, `serviceName`, `nodeId`, `nodeName`, `slot`, `state`,
   `desiredState`, `image`, `addresses`, `ports`, `revision`, `createdAt` and
   `updatedAt` when present.
3. `task ls -o name --filter 'state=running'` prints only matching IDs.
4. `task ls -o json` with a not-found/unavailable RPC emits one `Error` document
   and exits 3/6; the legacy path is unchanged.
5. `task ls --format json` on an empty result prints `[]`, not `No tasks found`.
6. `go test ./pkg/api/v1 ./pkg/client ./pkg/output`, the Linux build, the
   container-run command tests and `docs-site npm run verify` pass.

## Open questions

None blocking. The assumptions above are the rulings for this slice; if one is
wrong, it is cheaper to correct before the next command group.
