# Plan — #39: container healthchecks + health-gated rolling updates

Plan id: `issue-39-healthchecks`
Branch: `feat/healthchecks` (base `main` = `f2a709c`)
Repo: github.com/restuhaqza/swarmcracker

## Why

Nothing in the codebase runs a workload healthcheck: `HealthCheck` has zero
references outside tests. Consequences today: rolling updates only wait for a
process to *start*, an unhealthy-but-running workload is never restarted, and
`service ps` cannot show health. This is what makes SwarmKit rolling updates
safe, and #43 (`vm exec`, merged) removed the blocker by giving us in-guest
execution.

## What ships (acceptance criteria, from the issue)

1. `service create --health-cmd '...' --health-interval 5s ...` is accepted and
   stored in the task spec.
2. The executor runs the check inside the microVM and reflects health into
   SwarmKit's task status.
3. An unhealthy task triggers the service's restart policy and is visible in
   `service ps`.
4. A rolling update with `--update-order start-first` does not proceed until the
   new task is healthy.
5. Checks have a bounded timeout and never block the executor loop.
6. Unit tests for spec mapping + a guest-level e2e proving an unhealthy command
   prevents rollout completion.
7. Docs updated (`reference/cli.md`, `guides/operations.md`).

## Architecture (fixed decisions — derived from the reconnaissance)

- **Config side works as-is.** `api.ContainerSpec.Healthcheck *api.HealthConfig`
  exists (`swarmkit api/specs.proto:323`; Go field spelled `Healthcheck`).
  `--health-cmd` maps to `Test: ["CMD-SHELL", cmd]` (Docker parity).
- **The health gate is `Controller.Start`.** SwarmKit's agent transitions a task
  to `RUNNING` immediately after `Start` returns
  (`swarmkit agent/exec/controller.go:321`), and the manager's updater treats
  `State >= Running` as "up" (`manager/orchestrator/update/updater.go:429`).
  So holding `Start` until the check passes is the only way to gate a rollout.
  This is safe: `Start` runs in a per-task goroutine
  (`swarmkit agent/task.go:134`, `worker.go:634`) and its `opctx` has **no
  deadline** (`task.go:108`).
- **`Start` must not hold `c.mu` while waiting.** `Start` currently locks
  `c.mu` for its whole body (`pkg/swarmkit/executor.go:772-773`) and so do
  `Shutdown`/`Terminate`/`Remove`. Split `Start` into a locked `startVM` phase
  and an unlocked health-wait phase.
- **The check runs in-guest over the #43 agent**: `agent.Dial(ctx,
  vsockUDSPath(c.socketPath), agent.DefaultPort)` then `Client.Exec` with
  `/bin/sh -c <cmd>`, bounded by `--health-timeout`.
- **Restart-on-unhealthy is ours to implement.** The proto comment says an
  unhealthy container "will be destroyed, its creating task will exit and a new
  task will be rescheduled" (`swarmkit api/specs.pb.go:846-850`), but that
  behaviour belongs to the executor here. A per-controller monitor goroutine
  stops the VM after `Retries` consecutive failures, so `Wait` returns a failure
  and SwarmKit applies the restart policy.
- **The health column is state-derived (user's decision).** `api.ContainerStatus`
  is only `{ContainerID, PID, ExitCode}` and `TaskStatus` has no health field —
  unchanged in v2.1.2 and upstream master. So `service ps` derives HEALTH from
  the health-gated task state, and `task inspect` surfaces the failure reason via
  `Status.Err`. No fork, no node-local sidecar. Documented as an API limitation
  with an upstream recommendation.

## Global constraints (verbatim; binding on every task and reviewer)

- Go 1.26. Linux-only runtime. Cross-compile with
  `GOOS=linux GOARCH=amd64 CGO_ENABLED=0`.
- Module path `github.com/restuhaqza/swarmcracker`.
- **No new third-party dependencies.**
- **Do not change `pkg/agent`'s wire protocol** (merged in #43 and verified
  end-to-end). Consume it as-is.
- `vsockUDSPath(socketPath)` (`pkg/swarmkit/vmm.go:1052`) is the single source of
  truth for the vsock UDS path inside `pkg/swarmkit`.
- Docker-compatible defaults, applied only when a healthcheck is declared:
  `interval 30s`, `timeout 30s`, `retries 3`, `start-period 0`.
- Every check is bounded by `--health-timeout`; no health path may block the
  executor's shared loops.
- Errors wrapped: `fmt.Errorf("<context>: %w", err)`. Never swallow.
- Tests: every test asserts behaviour; `go test -short -race` must pass.
- Coverage: total `./pkg/...` must not drop more than 0.1% vs `main`.
- `make fmt`; golangci-lint (incl. `unused`) clean; `go mod tidy -diff` clean.
- Never touch `.impeccable/review/`.

## Tasks

### T1 — CLI health flags → task spec

**Goal.** `service create` / `service update` accept and store a healthcheck.

**Files.** `cmd/swarmcracker/cmd_service.go` (flag registration in
`newServiceCreateCommand` ~:273-315 and `newServiceUpdateCommand` ~:449-487),
`cmd/swarmcracker/cmd_service_spec.go` (`serviceCreateOptions` ~:19-81,
`serviceUpdateOptions` ~:235-297, `buildServiceSpec` ~:86-226), `updateService`
(`cmd_service.go` ~:1248), plus tests.

**Behaviour.** Flags `--health-cmd` (string), `--health-interval`,
`--health-timeout`, `--health-start-period` (durations), `--health-retries`
(int). When `--health-cmd` is set, build
`api.HealthConfig{Test: []string{"CMD-SHELL", cmd}, Interval, Timeout, Retries,
StartPeriod}` with the defaults above for anything unset. Reject with a clear
error: any other health flag without `--health-cmd`; a non-positive
interval/timeout/start-period; `--health-retries < 1`. Update path mutates
`spec.Task.GetContainer()` like the other flags.

**Verification.** `go test -short -race ./cmd/swarmcracker/`

**Depends on.** none.

### T2 — Health policy + in-guest runner

**Goal.** Pure, testable health policy plus the runner that executes the check
inside the guest.

**Files.** `pkg/healthcheck/healthcheck.go` + `healthcheck_test.go` (new);
`pkg/swarmkit/healthcheck.go` + `healthcheck_test.go` (new);
`pkg/types/task.go` (extend `types.Container` with a healthcheck field ~:91-106);
`pkg/swarmkit/executor.go` `convertTask` (~:1256-1375) to carry it.

**Interfaces (exact).**

```go
// pkg/healthcheck
type Policy struct {
    Interval    time.Duration
    Timeout     time.Duration
    Retries     int
    StartPeriod time.Duration
}
// Tracker is the retry / start-period state machine. Pure and table-testable.
type Tracker struct { /* ... */ }
func NewTracker(p Policy, start time.Time) *Tracker
// Observe records one probe result and reports the verdict.
//   VerdictHealthy   - the container is healthy
//   VerdictStarting  - not healthy yet, still inside the start period
//   VerdictUnhealthy - Retries consecutive failures outside the start period
func (t *Tracker) Observe(now time.Time, healthy bool) Verdict

// pkg/swarmkit
// runHealthcheck executes cmd inside the guest via the #43 agent.
func runHealthcheck(ctx context.Context, udsPath string, p Policy, cmd string) (bool, string, error)
```

**Behaviour.** `runHealthcheck` dials `agent.Dial(ctx, udsPath,
agent.DefaultPort)`, runs `[]string{"/bin/sh", "-c", cmd}` with a ctx bounded by
`p.Timeout`, and returns healthy iff the exit code is 0; the output (bounded to
a few KiB) is returned for the failure message. A dial/exec error is reported as
an error, and the caller treats it as an unhealthy probe.

**Verification.** `go test -short -race ./pkg/healthcheck/ ./pkg/swarmkit/`

**Depends on.** none.

### T3 — Health-gated start (criterion #4)

**Goal.** A task with a healthcheck does not report RUNNING until it is healthy.

**Files.** `pkg/swarmkit/executor.go` (`Start` ~:769-838 split into a locked
`startVM` phase and an unlocked health wait), tests.

**Behaviour.** When the task's container declares no healthcheck, behaviour is
unchanged. Otherwise, after the VM starts, poll `runHealthcheck` every
`Interval`, feeding a `healthcheck.Tracker`: return `nil` as soon as the verdict
is healthy (the agent then reports RUNNING); return a wrapped error naming the
command and the last output when the verdict is unhealthy (task → FAILED).
Honour `ctx.Done()` and return promptly. Must not hold `c.mu` while waiting.

**Verification.** `go test -short -race ./pkg/swarmkit/`

**Depends on.** T2.

### T4 — Restart-on-unhealthy monitor (criterion #3)

**Goal.** A task that goes unhealthy while running is restarted.

**Files.** `pkg/swarmkit/health_monitor.go` + tests (new); lifecycle wiring in
`Start`/`Shutdown`/`Terminate`/`Remove`/`Close` (`pkg/swarmkit/executor.go`);
`pkg/swarmkit/vmm.go` `Wait` (~:790-833) so the failure message carries the
health reason.

**Behaviour.** After a healthchecked task is running, one goroutine polls every
`Interval` with a `Tracker`. When the verdict becomes unhealthy, stop the VM via
the VMM manager so `Wait` returns a failure whose message includes
`healthcheck failed: <cmd>: <output>`; SwarmKit then applies the restart policy.
The goroutine stops on shutdown/terminate/remove/close and must not leak (prove
with `-race`). No healthcheck → no goroutine.

**Verification.** `go test -short -race ./pkg/swarmkit/`

**Depends on.** T3.

### T5 — CLI surface for health

**Goal.** Health is visible.

**Files.** `cmd/swarmcracker/cmd_service.go` (`listServiceTasks` ~:1063-1139),
`cmd/swarmcracker/cmd_task.go` (`listTasks` ~:110-178, `inspectTask` ~:180-218),
tests.

**Behaviour.** `service ps` and `task ls` gain a HEALTH column, populated only
when the task's container declares a healthcheck, derived from the health-gated
state: `STARTING` (state STARTING/PREPARING with a healthcheck), `RUNNING`
(RUNNING), `FAILED` (FAILED/REJECTED), `-` otherwise. `task inspect` prints the
healthcheck config and, when set, the health failure reason from `Status.Err`.
JSON output carries the derived health.

**Verification.** `go test -short -race ./cmd/swarmcracker/`

**Depends on.** T1 (spec) and T3 (state semantics).

### T6 — Docs

**Files.** `docs-site/src/content/docs/reference/cli.md`,
`docs-site/src/content/docs/guides/operations.md` (healthchecks + gated
rollouts), plus `guides/configuration.md` if it lists service flags.

**Behaviour.** Document the five flags + defaults, the health gate (a task is
not RUNNING until healthy, so `start-first` rollouts wait), restart-on-unhealthy,
the state-derived HEALTH column, and the SwarmKit API limitation (no health field
in `ContainerStatus`/`TaskStatus`) with the recommendation to upstream one.

**Verification.** `cd docs-site && npm run verify`

**Depends on.** T5.

### Controller e2e (not a subagent task)

On the 2-node lab: a service with a healthcheck that fails until a marker file
appears (`--health-cmd 'test -f /tmp/ready'`); prove (a) the task stays STARTING
and a `--update-order start-first` rollout does not complete while unhealthy,
(b) it completes once the marker exists, (c) sustained unhealth restarts the
task, (d) `service ps` reflects the states. Then a final whole-branch review.

## Serialisation

T1 and T2 are independent; T3 depends on T2; T4 on T3; T5 on T1+T3; T6 on T5.
T1 ∥ T2 → T3 → T4 → T5 → T6.

## Risks / rulings

- **Blocking `Start`.** Safe because it runs in a per-task goroutine with a
  deadline-free opctx, and `tm.shutdown` cancels it. Ruling: release `c.mu`
  before waiting and honour ctx cancellation, or `Terminate` is stalled for the
  whole start period. — Cost if wrong: a `service rm` during a long start period
  hangs until the wait ends.
- **Health column is state-derived.** Ruling: no fork, no node-local sidecar
  (user's decision). — Cost if wrong: a RUNNING task that is currently failing
  checks shows `RUNNING` until retries are exhausted.
- **The start-time race.** `agent.Dial` retries the muxer handshake for 5s
  (`pkg/agent/client.go:23`) while the wrapper starts the agent in the
  background. A dial error counts as an unhealthy probe, and `--health-retries`
  plus `--health-start-period` absorb it. — Cost if wrong: a slow-booting guest
  fails its healthcheck spuriously.
- **Default when `--health-cmd` is absent.** Ruling: no healthcheck, no monitor,
  no gate, unchanged behaviour. — Cost if wrong: none; this preserves the
  existing path exactly.
