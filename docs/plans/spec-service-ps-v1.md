# Spec: `service ps` v1 envelope (phase 0, slice 5)

Status: ready for implementation. Normative task shape:
[`api-contract-v1-proposal.md`](./api-contract-v1-proposal.md), §5.4.

## Objective

Add `swarmcracker service ps -o json` and `-o name` using the v1 `TaskList`
envelope scoped to one service, while keeping the legacy `--format json` path
stable.

Success means:

- A service with tasks in `-o json` prints one `TaskList` document whose items
  are the tasks belonging to that service.
- `-o name` prints one task ID per line and honors `--filter`.
- References resolve as full ID, exact name, then unique ID prefix.
- Not-found and ambiguous references are exit 3; bad flag combinations are
  exit 2.
- Phase 0 omits live health from the v1 modes; `--health-port` is rejected and
  `--quiet` is replaced by `-o name`.
- The new path goes through `pkg/client`; no new `api.ControlClient` use in
  `cmd/swarmcracker`.
- `--format json` keeps the legacy array, live-health fields and redaction.

## Assumptions

1. The v1 document is `TaskList`, not a new service-specific document type;
   `service ps` is the task list filtered by one resolved service.
2. The client asks the manager to filter tasks with
   `ListTasksRequest_Filters.ServiceIDs`.
3. `--filter` keeps the existing v1 task filter semantics (`state=`, `node=`,
   `service=`), with unknown keys matching everything.
4. The v1 modes never call the health fan-out.

## Commands

```bash
go test ./pkg/api/v1 ./pkg/client ./pkg/output
GOOS=linux GOARCH=amd64 go build ./cmd/swarmcracker
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c ./cmd/swarmcracker -o /tmp/swarmcracker.test
docker run --rm -v /tmp:/work -w /work alpine:3.20 /work/swarmcracker.test -test.run 'TestServicePS' -test.v
cd docs-site && npm run verify
```

## Project structure

```text
pkg/client/client.go                 ListServiceTasks + shared list helper
pkg/client/task_mapping_test.go      filter request and empty-list tests
cmd/swarmcracker/cmd_service.go      -o json|name path
cmd/swarmcracker/service_ps_contract_test.go
api/openapi.yaml                     /v1/services/{id}/tasks
pkg/api/v1/openapi_test.go           path check
docs-site/src/content/docs/reference/cli.md
CHANGELOG.md
```

## Testing strategy

- **`pkg/client`**: ServiceIDs filter is sent, result is mapped, empty skips the
  node lookup.
- **`cmd/swarmcracker`**: envelope, filter, `-o name`, not-found, RPC error and
  rejected flag combinations.
- **OpenAPI**: the service-tasks path exists and the TaskList schema still
  validates.
