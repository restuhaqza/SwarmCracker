# Plan — #67: live cluster-wide `HEALTH` via a per-node health endpoint

Branch: `feat/cluster-live-health`
Issue: [#67](https://github.com/restuhaqza/SwarmCracker/issues/67)

## Problem

`service ps` / `task ls` show a **state-derived** `HEALTH` column (#39). A
`RUNNING` task that is currently failing its checks keeps reading `RUNNING`
until the retries are exhausted and the task becomes `FAILED`.

The root cause is not the missing field. Verified against SwarmKit v2.1.1: while
a task is `RUNNING` the agent blocks in `Controller.Wait`, `ContainerStatus` /
`PortStatus` are collected only when `Do` returns, and `reportAllStatuses` runs
only on session (re)establishment. So SwarmKit never polls a running task's
status, and **any** wire field would be frozen until the task dies.

The live verdict already exists — #66 publishes it into the per-task metadata
file next to the VM socket (`<socket-dir>/<task-id>.net.json`,
`runtime.VMMetadata.Health`) so node-local `vm status` / `vm list` can show it.
This issue closes the last gap: aggregate that per-node data in the CLI.

## Design

Chosen approach (the issue's proposal): **fan-out in the CLI**.

```
service ps / task ls
  ├─ ListTasks            (SwarmKit control API — unchanged)
  ├─ ListNodes            (one call; node ID → node.Status.Addr)
  └─ for each node used by the filtered tasks:
        GET http://<addr>:<health-port>/v1/tasks/health     (daemon, new)
          └─ daemon reads its own sidecars and returns live health
```

### T1 — daemon endpoint

The daemon already serves `/healthz` and `/metrics` on `--health-addr`
(default `127.0.0.1:8080`). Add `GET /v1/tasks/health` to the same mux.

- New exported helpers in `pkg/runtime` (the package that owns the sidecar):
  - `TaskHealthPath = "/v1/tasks/health"` — shared by both sides so the path
    cannot drift.
  - `TaskHealth{TaskID string; VMHealth}` — one row.
  - `TaskHealthResponse{Tasks []TaskHealth}` — the response body.
  - `CollectTaskHealth(dir string) []TaskHealth` — `ListVMMetadata(dir)`, keep
    the entries with a health object, sort by task ID (deterministic output).
- `cmd/swarmd-firecracker/task_health.go`: `taskHealthHandler(socketDir)`
  returns a small `http.Handler` that JSON-encodes `CollectTaskHealth`. Wired
  into the existing mux as `mux.Handle(runtime.TaskHealthPath, ...)`.

A task with no healthcheck never publishes, so it is absent from the response —
the CLI falls back to the derived value for it. The response is a read of files
the daemon already writes; no new state, no new privilege.

### T2 — CLI fan-out

New file `cmd/swarmcracker/task_health.go`:

```go
const (
    defaultHealthPort     = 8080
    healthRequestTimeout  = 3 * time.Second
)

type clusterHealthOptions struct {
    Disabled   bool          // --no-health
    Port       int           // --health-port (default 8080)
    HTTPClient *http.Client  // tests inject a client; nil → default
}

type liveHealth struct {
    enabled   bool
    byTask    map[string]runtime.VMHealth
    reachable map[string]bool
}

func fetchLiveHealth(ctx, client api.ControlClient, tasks []*api.Task, opts healthOptions) liveHealth
func taskHealthDisplay(task *api.Task, lh liveHealth) string
```

`fetchLiveHealth`:

1. `opts.Disabled` → return `liveHealth{}` (not enabled): the column stays
   derived, exactly as today.
2. Collect the distinct node IDs of the tasks **that declare a healthcheck**
   (no other task needs a lookup).
3. `client.ListNodes` once. On error, degrade: nothing is reachable.
4. For each node: `node.Status.Addr` empty → unreachable. Otherwise one
   `GET http://<addr>:<port><runtime.TaskHealthPath>` with
   `healthRequestTimeout`, decode `runtime.TaskHealthResponse`. Any transport,
   status or decode error → that node is unreachable.
5. `reachable[nodeID] = true` on success; copy the rows into `byTask`.

`taskHealthDisplay` (the rule the acceptance criteria describe):

| Task | Node reachable | Result |
|------|----------------|--------|
| no healthcheck | — | `-` |
| healthcheck, live row present | yes | live state (`starting`/`healthy`/`failing`/`unhealthy`) |
| healthcheck, no live row (not started, or already stopped) | yes | derived `taskHealth(task)` (unchanged behaviour) |
| healthcheck | no (unreachable / unknown address / `--no-health`) | `-` for unreachable; derived for `--no-health` |

The unreachable case is `-`, never a wrong value — and the command still
succeeds. `--no-health` keeps today's purely derived output (opt-out, and the
escape hatch when the endpoint is not exposed).

Wiring:

- `listServiceTasksWithClient` / `listTasksWithClient` gain a `healthOptions`
  last parameter (the tests pass it explicitly, so nothing is implicit).
- The table prints `taskHealthDisplay(task, lh)`; JSON adds `health` (the
  display value, live-overlaid) and `live_health` (the raw `VMHealth`, omitted
  when unknown) to `taskWithHealth`.
- `--health-port` (int, default `8080`) and `--no-health` (bool) on both
  `service ps` and `task ls`.

### Flags contract

Both flags are honored or rejected, never silently ignored (project rule):

- `--no-health` skips the fan-out entirely (no `ListNodes`, no HTTP).
- `--health-port 0` or negative is **rejected** with a clear error before any
  RPC (`--health-port must be between 1 and 65535`), even alongside
  `--no-health`: an invalid value is never silently ignored.

### T3 — docs

- `guides/operations.md`: rewrite "Live health on a node" into "Live health
  (cluster-wide)" — the fan-out, the `HEALTH` values now including
  `failing`/`healthy`, the trust model, and the `--no-health` / `--health-port`
  flags. Update "The API limitation" to point at the endpoint instead of
  "would need a per-node endpoint".
- `reference/api.md`: document `GET /v1/tasks/health` (bind, response, trust).
- `reference/cli.md`: the two new flags and the live-overlay semantics.
- `guides/configuration.md`: note the endpoint on the health server next to the
  `metrics` note.
- `pkg/runtime/metadata.go`: the `VMHealth` doc comment currently claims health
  "cannot travel cluster-wide" — it can now, so fix it.

### Trust model (documented, not enforced)

The endpoint returns **health only** — state, failing streak, the probe command
and its last bounded output. No secrets, no task spec, no auth. The bind address
is the boundary: the default `127.0.0.1:8080` is node-local and unreachable from
other nodes; an operator who wants cluster-wide `HEALTH` must bind it on the
cluster network (e.g. `--health-addr 0.0.0.0:8080`). Same exposure as the
existing `/healthz` and `/metrics`, which are already on that address.

## Tasks

| # | Task | Files | Verify |
|---|------|-------|--------|
| T1 | Endpoint + shared types + handler | `pkg/runtime/taskhealth.go`, `metadata.go`, `cmd/swarmd-firecracker/task_health.go`, `run.go` | `go test ./pkg/runtime/ ./cmd/swarmd-firecracker/` |
| T2 | CLI fan-out, overlay, flags | `cmd/swarmcracker/task_health.go`, `helpers.go`, `cmd_service.go`, `cmd_task.go`, tests | `go test ./cmd/swarmcracker/` |
| T3 | Docs | `guides/operations.md`, `reference/api.md`, `reference/cli.md`, `guides/configuration.md` | `cd docs-site && npm run verify` |
| T4 | Verification + lab e2e | test server | `verify.sh <branch>`, 2-node lab |

T1 → T2 → T3 → T4.

## Verification

- Unit tests per task (above): populated/empty/malformed sidecar dir; handler
  via `httptest`; fan-out with a fake HTTP server + fake client (live overlay,
  unreachable node → `-`, `--no-health`, `--health-port` validation).
- CI (authoritative gate).
- Lab e2e on the 2-node nested-KVM lab: bind both daemons' `--health-addr` to
  the cluster network; a service with a healthcheck that passes then starts
  failing; `service ps` **from the manager** shows `failing` for a task on the
  worker while it is still `RUNNING`; an unreachable node → `-`; `--no-health`
  shows the derived value only. Then restore the lab to `main`.

## Risks

- **Fan-out cost.** One `ListNodes` plus one small HTTP GET per node used by
  the (already filtered) tasks, issued concurrently (bounded to 8) under a 10s
  total budget, each bounded by `healthRequestTimeout`. A node that hangs costs
  one timeout, not one per task. `--no-health` opts out.
- **`failing` is short-lived.** The restart fires after
  `(retries - 1) × interval`; the e2e observes it inside that window (same as
  #66's node-local test).
- **Old nodes.** A node running a daemon without the endpoint answers 404, so it
  counts as reachable and its tasks keep the derived value — mixed-version
  clusters degrade per node instead of failing the command.
- **Default bind.** Each daemon binds `127.0.0.1:8080` by default, so a remote
  node's endpoint is not reachable and its tasks show `-`. To keep the common
  case working, a node whose own address does not answer is retried on the
  loopback of the machine running the CLI, accepted only if that endpoint
  reports a task the node owns (so another node's daemon is never
  misattributed).
- **Path drift.** Avoided by exporting `runtime.TaskHealthPath` from the package
  both sides import.

## Review round (2026-10-16)

An independent review of the first version found no blockers but flagged:

1. **Docs/`--help` vs code.** The help text and `cli.md` said an unreachable
   node falls back to the derived value, while the code showed `-` for any
   non-200 (so an old daemon 404ing lost the column). Fixed by treating *any*
   HTTP response as "the node is up": `getNodeTaskHealth` now returns
   `answered`, and only a transport failure yields `-`. Help/docs rewritten to
   state the three cases exactly.
2. **Fan-out could eat the command budget.** Per-node requests were serial over
   a randomly-ordered map with a shared 30s context. Now sorted node order,
   concurrent (bounded to 8) under a 10s fan-out budget.
3. **Default-config degradation.** Documented, and mitigated by the loopback
   fallback above plus a `--no-health` escape hatch.
4. Nits addressed: terminal tasks no longer report a stale live row; the daemon
   handler allows `HEAD` and encodes before writing the header;
   `writeTasksJSON` is nil-tolerant; "no secrets" softened in the docs; tests
   added for 404/malformed bodies, loopback attribution and non-misattribution,
   `ListNodes` failure, the quiet path, terminal states, invalid-port rejection,
   and flag wiring on both commands. The mixed-case column (live lowercase vs
   derived uppercase) is kept and documented, matching the issue's vocabulary.

A second pass on the fixed diff found one more real bug and two documentation
slips:

- **Unscheduled tasks.** A healthchecked task with no node yet (`NodeID == ""`,
  i.e. NEW/PENDING/ASSIGNED right after `service create`) fell into the
  "unreachable" branch and showed `-` instead of the derived `STARTING`. Fixed:
  no node yet → derived.
- **Docs.** The `service ps`/`task ls` intro still said "live, otherwise derived"
  (omitting `-`), and the fan-out description still said "one request per node".
- The semaphore now honours the fan-out context, so a goroutine waiting for a
  slot past the deadline exits instead of starting a doomed request.
- Still open (accepted, documented): the concurrency cap / 10s budget are not
  asserted by a test; if the budget is hit, which nodes degrade to `-` depends on
  timing; and a local sidecar not yet swept after a reassignment can be
  attributed to the node SwarmKit now reports (very narrow window, commented in
  the code). A `ListNodes` failure blanks the column to `-` by design.

