# Plan — #49 (part 1): placement gaps — settable node labels + actionable unschedulable errors

Branch: `feat/placement-labels` · Issue: [#49](https://github.com/restuhaqza/SwarmCracker/issues/49)
Program: `docs/plans/backlog-program.md` · Slice 2 of the sequential backlog.

## Current state (measured)

- **Constraint-based placement already exists and works.** `service create`/`update`
  expose `--constraint` and `--placement-pref`
  (`cmd/swarmcracker/cmd_service.go:321-322`, `:505-506`), built by the pure
  `buildPlacement` (`cmd/swarmcracker/cmd_service_spec.go:326-353`), applied at
  `:212-216`. The vendored SwarmKit scheduler honours them: `ConstraintFilter`
  (`…/manager/scheduler/filter.go:218-251`) resolves `node.hostname`, `node.role` and
  **`node.labels.*`** (`…/constraint.go:107,147,172-185`), and placement preferences
  spread through `scheduler.go:737-744` → `nodeset.go:50-116`. So the issue's premise
  ("placement is essentially untuned") is stale: what is missing is around it.
- **Gap 1 — labels cannot be set.** `cmd/swarmcracker/cmd_node.go:22-38` registers only
  `ls`, `inspect`, `drain`, `activate`, `promote`, `rm`. `node.labels.*` is schedulable
  but no CLI path writes `Node.Spec.Annotations.Labels`, so a user cannot satisfy a
  `node.labels.*` constraint.
- **Gap 2 — the reason is never shown.** SwarmKit does produce it:
  `scheduler.go:942-959` sets `Task.Status.Err` ("no suitable node (…)") and
  `constraint_enforcer.go:215` sets `Status.Message`. But `service ps`
  (`cmd/swarmcracker/cmd_service.go:1176`, table at `:1226-1244`) prints only
  `ID STATUS HEALTH NODE IMAGE`, and `task ls`
  (`cmd/swarmcracker/cmd_task.go:137`, table at `:171`) prints only
  `ID SERVICE STATUS HEALTH NODE IMAGE`. The operator sees a task that never starts and
  no reason.
- **Gap 3 — stale docs.** `docs-site/src/content/docs/guides/advanced.md:62-63` still
  says placement constraints are not exposed through the CLI.

## Design

1. `swarmcracker node update <node-id> --label-add k=v --label-rm k` (both repeatable),
   mirroring the existing `setNodeAvailability` flow
   (`cmd/swarmcracker/cmd_node.go:342-379`): `resolveNodeRef` → `GetNode` → mutate
   `Spec.Annotations.Labels` → `UpdateNode` with the current `NodeVersion`.
2. An `ERROR` column **appended** to `service ps` and `task ls`, carrying
   `Status.Err` only, empty when there is none — SwarmKit puts routine transition
   notes like "started" in `Status.Message`, so a fallback would print a note in an
   ERROR column. Appending at the end keeps every existing positional parse intact
   (this project's lab scripts read fields 3 and 4).
3. Docs: correct the stale statement and document the new flag and the new column.

### Interfaces (binding)

```go
// cmd/swarmcracker/cmd_node.go
func newNodeUpdateCommand() *cobra.Command   // "update <node-id>", flags --label-add/--label-rm
func updateNodeLabels(nodeID string, add, remove []string) error

// cmd/swarmcracker/labels.go (new, pure helpers — unit-tested without a client)
// parseLabelAssignments turns ["k=v","k2=v2"] into a map, rejecting a missing "=" or
// an empty key with an error naming the offending argument.
func parseLabelAssignments(pairs []string) (map[string]string, error)

// applyLabelChanges returns a new label map with remove applied first, then add, and
// never mutating the input map.
func applyLabelChanges(current map[string]string, add, remove []string) (map[string]string, error)

// taskErrorReason is the operator-facing reason for a task: Status.Err only, "" when
// there is none (Status.Message carries routine transition notes like "started", so it
// is not a reason).
func taskErrorReason(task *api.Task) string
```

## Global constraints

- Go 1.26. `gofmt -l pkg cmd` must print nothing.
- Errors: `fmt.Errorf("...: %w", err)`; reject invalid input with a clear error that
  names the offending value — never accept a flag and ignore it.
- Tests: table-driven + testify, host- and privilege-independent.
- Tests run on the Linux test server (`root@192.168.18.25`, checkout `/root/sc-verify`);
  this workstation is darwin and `cmd/...` does not build here.
- `pkg/...` coverage stays ≥ 85%; a touched package must not drop by more than 0.1pp.
- Docs live in `docs-site/src/content/docs/`.
- Pre-existing root-only failures exist in `pkg/image`, `pkg/swarmkit`, `pkg/network`;
  verify with `/root/sc-cmp.sh <base-sha> <pkgs...>` and require an **empty head-only**
  set rather than a green package.

## Task T1 — `node update --label-add / --label-rm`

Goal: a user can label a node so `node.labels.*` constraints can be satisfied, and can
remove a label again.

Files: `cmd/swarmcracker/cmd_node.go`, `cmd/swarmcracker/labels.go` (new),
`cmd/swarmcracker/cmd_node_test.go` (or a new `labels_test.go`).

Steps:
1. Add `newNodeUpdateCommand()` next to the other node subcommands
   (`cmd/swarmcracker/cmd_node.go:22-38`) with repeatable `--label-add` and
   `--label-rm`; `Args: cobra.ExactArgs(1)`; `PreRun` sets up logging like the others.
2. Implement `updateNodeLabels` modelled exactly on `setNodeAvailability`
   (`:342-379`): `resolveNodeRef` (so a short ID works), `GetNode`, mutate a **copy** of
   the label map via `applyLabelChanges`, `UpdateNode` with
   `NodeVersion: &node.Meta.Version`, then print the resulting labels.
3. `parseLabelAssignments` rejects a pair without `=` and a pair with an empty key;
   `applyLabelChanges` applies removals before additions and does not mutate its input.
   Both are pure and unit-tested.
4. Validate all arguments **before** opening the client (so a typo fails before any
   RPC), and reject an invocation with neither flag with a clear message.
5. Keep `--label-rm` of a non-existent key a no-op (not an error), and say so in the
   command's long help.

Verification: `go test -count=1 ./cmd/swarmcracker/` and the named helpers' tests on the
test server; `bash /root/sc-cmp.sh <base> ./cmd/swarmcracker/` must report an empty
head-only set. Tests must cover: parsing (valid, missing `=`, empty key), apply
(remove-then-add, no input mutation), the cobra wiring (flags registered, help text
mentions both), and that an invalid pair errors **before** any client call.

Depends on: —

## Task T2 — show the reason a task is not running

Goal: `service ps` and `task ls` tell the operator why a task is not running, instead of
silently showing a status that never advances.

Files: `cmd/swarmcracker/cmd_service.go`, `cmd/swarmcracker/cmd_task.go`,
`cmd/swarmcracker/labels.go` (or a small shared file for `taskErrorReason`),
plus their `_test.go` files.

Steps:
1. Add the pure `taskErrorReason(task *api.Task) string` (`Status.Err` only, `""`
   otherwise — not `Status.Message`, which carries routine transition notes). Unit-test
   it directly.
2. Append an `ERROR` column to the `service ps` table
   (`cmd_service.go:1226-1244`) and the `task ls` table (`cmd_task.go:171`), as the
   **last** column. Keep the existing columns and their order untouched (positional
   parsers depend on them). Truncate the reason to a fixed width so one long reason does
   not destroy the layout, and print nothing extra when it is empty.
3. Do not change the JSON paths' field set (they already marshal the whole task); confirm
   `writeTasksJSON` (`cmd/swarmcracker/task_health.go:316`) therefore already carries the
   reason, and say so in the report.
4. Adjust the header separator line if the width changes.

Verification: `go test -count=1 ./cmd/swarmcracker/` plus
`/root/sc-cmp.sh <base> ./cmd/swarmcracker/` with an empty head-only set. Tests must
cover: `taskErrorReason` for `Err` set / only `Message` set / neither; the table output
containing the reason for a task whose `Status.Err` is set; the existing columns still
present and in the same order; a task with no error printing a row whose trailing column
is empty.

Depends on: —

## Task T3 — documentation

Goal: the docs match the CLI: labels are settable, constraints are honoured, and a
blocked task shows why.

Files: `docs-site/src/content/docs/guides/advanced.md`,
`docs-site/src/content/docs/reference/cli.md`.

Steps:
1. `advanced.md:62-63`: replace the stale "placement constraints are not exposed through
   the CLI yet" claim with the truth — `--constraint`/`--placement-pref` are honoured by
   the scheduler; `node.labels.*` needs `node update --label-add`, with a worked example
   (`node update <id> --label-add disk=ssd` then
   `service create --constraint node.labels.disk==ssd`).
2. `reference/cli.md`: document `node update` (both flags, the `k=v` requirement, that
   removing a missing key is a no-op) and the new `ERROR` column in the `service ps` and
   `task ls` output descriptions.
3. Verify with the docs pipeline (`cd docs-site && npm run verify` on Node 22).

Verification: docs check passes; `grep` shows both flags and the column documented.

Depends on: T1, T2.

## Verification (program-level, by the controller)

- `go test` for the touched packages as root and as an unprivileged user.
- `/root/sc-cmp.sh` with an empty head-only set on the full suite, plus `verify.sh`.
- CI green (test / lint / build / coverage gate).
- **Lab e2e (2 nodes)**: label node2 with `disk=ssd`; `service create --constraint
  node.labels.disk==ssd` lands both replicas on node2 (and a negative control with
  `disk=hdd` lands them on node1); then an impossible constraint
  (`node.labels.nope==1`) shows a clear reason in `service ps` instead of a silent
  never-starting task. Restore the lab to `main` afterwards.
- Independent review before merge.

## Acceptance mapping (the #49 criteria this slice closes)

| Criterion | Where |
|---|---|
| `--constraint node.labels.disk==ssd` is honoured and reflected in where tasks land | T1 + lab e2e (the scheduler side already worked; the labels were unreachable) |
| Unschedulable constraints produce a clear, actionable error rather than a silent failure | T2 + lab e2e |
| Docs updated | T3 |

`--placement-pref spread=node.role` already works and is exercised by the same e2e
(node pinning); autoscaling is deliberately **not** in this slice (it needs the
per-service metric work, program slice 9).
