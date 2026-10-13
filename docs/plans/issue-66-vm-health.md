# Plan — #66: live healthcheck state in `vm status` / `vm list` (node-local)

Branch: `feat/vm-status-live-health`
Issue: [#66](https://github.com/restuhaqza/SwarmCracker/issues/66)

## Problem

#39 shipped a **state-derived** `HEALTH` column. A `RUNNING` task that is
currently failing its checks keeps reading `RUNNING` until retries are
exhausted. That is the honest cluster-wide value — SwarmKit has no health field
and (verified) never polls a RUNNING task's status, so nothing pushed through
SwarmKit could be live without repurposing `Wait`/`Status.Err`.

But the daemon *does* hold the live verdict: the gate (`awaitHealthy`) and the
restart monitor (`monitorHealth`) probe every interval. We only need to expose
it. `vm status`/`vm list` are already node-local commands (they discover VMs from
the local Firecracker socket dir), so that is the natural place — and the sidecar
they already read for the guest IP is the natural carrier.

## Design

### Carrier: the existing per-task sidecar

`pkg/runtime.VMMetadata` is written by the daemon to
`<socket-dir>/<task-id>.net.json` and read by the CLI. Add an optional health
object; absence means "no healthcheck / not daemon-managed".

```go
type VMHealth struct {
    State         string    `json:"state"`                    // starting|healthy|failing|unhealthy
    FailingStreak int       `json:"failing_streak,omitempty"` // consecutive failed probes
    Output        string    `json:"output,omitempty"`         // last probe output (bounded)
    Command       string    `json:"command,omitempty"`        // probe command, for context
    UpdatedAt     time.Time `json:"updated_at"`
}

type VMMetadata struct {
    // ... existing fields ...
    Health *VMHealth `json:"health,omitempty"`
}
```

`VMState` gains `Health *VMHealth` so `vm list --format json` carries it.

### Why a fourth state (`failing`)

The window worth closing is **not** "the retries are exhausted" — at that point
the VM is already being stopped and the sidecar removed, so the state would be
visible for milliseconds. The window is the *intermediate* one: probes are
failing, retries are not exhausted, and the task is still `RUNNING`. Docker's
own status hides it (it keeps reporting `healthy` until the verdict flips).

So the vocabulary is `starting` / `healthy` / `failing` / `unhealthy`, where
`failing` means "the last probe failed, no restart yet" and carries the streak
and the last output. That makes the gap visible for up to
`(retries - 1) × interval` (60s with the defaults).

### Publisher: the controller rewrites the sidecar on each probe

`writeVMMetadata` currently builds the network fields from the internal task and
writes once. Split it:

- `writeVMMetadata(task)` keeps building the network fields, but now **stores the
  snapshot** (`c.vmMetadata`) and writes the file (network only, as today).
- `publishHealth(state, failing, cmd, output)` composes a copy of the snapshot
  plus a fresh `VMHealth` and rewrites the file atomically.

Both take `healthMu`, which already guards the health feature and which the
monitor may safely hold (it must never take `c.mu`). Lock order is
`c.mu → healthMu` (startVM takes `c.mu`, then `healthMu`); nothing takes
`healthMu → c.mu`, and `publishHealth` takes only `healthMu`.

Writes are best-effort and never fail a task, exactly like the existing network
write. A probe writes at most once per interval (30s default).

### Emission points

| Moment | Published |
|--------|-----------|
| Gate start (`awaitHealthy`, before the first probe) | `starting`, streak 0 |
| Gate probe failed (verdict `starting`) | `starting`, streak N, output |
| Gate probe failed (verdict `unhealthy`) | `unhealthy`, streak N, output |
| Gate probe passed | `healthy` |
| Monitor probe passed | `healthy` |
| Monitor probe failed, verdict `starting` | `failing`, streak N, output |
| Monitor probe failed, verdict `unhealthy` (before `ForceStop`) | `unhealthy`, streak N, output |

`failing_streak` is the tracker's retry counter. Per Docker semantics,
start-period failures do not increment it, so the streak stays 0 while inside the
start period. `Tracker` gains a pure `Failures() int` accessor (no clock, no I/O,
so still table-testable).

A task with no healthcheck never publishes, so its sidecar has no health object
and the CLI shows `-`.

### CLI

- `enrichVMs`: copy `md.Health` into `vm.Health`.
- `vm status`: a `[Health]` section — state, failing streak, last output, probe
  command, observed-at.
- `vm list`: a `HEALTH` column (`healthy`/`failing`/`starting`/`unhealthy`/`-`) and
  the `health` field in JSON.

### Explicitly out of scope

- `service ps` stays state-derived (cluster-wide live health is #67).
- Foreground `vm create` VMs have no daemon and no monitor, so no health.

## Tasks

| # | Task | Files | Verify |
|---|------|-------|--------|
| T1 | Metadata + state types, round-trip, backward compat | `pkg/runtime/metadata.go`, `state.go`, `metadata_test.go` | `go test ./pkg/runtime/` |
| T2 | `Tracker.Failures()` | `pkg/healthcheck/healthcheck.go`, `_test.go` | `go test ./pkg/healthcheck/` |
| T3 | Publish from the gate + monitor; store the network snapshot | `pkg/swarmkit/executor.go`, `healthcheck.go`, `health_monitor.go`, tests | `go test ./pkg/swarmkit/` |
| T4 | CLI rendering (status section, list column, JSON) | `cmd/swarmcracker/vm_lookup.go`, `cmd_list.go`, `cmd_status.go`, tests | `go test ./cmd/swarmcracker/` |
| T5 | Docs | `guides/operations.md`, `reference/cli.md` | `cd docs-site && npm run verify` |

T1 → T2 → T3 → T4 → T5 (T3 consumes T1+T2; T4 consumes T1).

## Verification

- Unit tests per task (above).
- Independent whole-branch review at the frozen tip.
- Lab e2e on the 2-node nested-KVM lab: a service whose check passes until a
  marker appears; `vm status` on the task's node shows `failing` (with the streak
  and the last output) **before** the restart fires, then `unhealthy`, then the
  replacement task shows `healthy`.

## Risks

- **Sidecar churn.** One small atomic write per probe interval per healthchecked
  task. Bounded (the probe output is already capped at 4 KiB) and node-local;
  acceptable.
- **Clobbering network fields.** Avoided by composing from the stored snapshot
  rather than writing a fresh struct.
- **Reader compatibility.** An old CLI ignores the new key; an old sidecar (no
  `health`) parses fine. Both are additive.
- **A publish racing a teardown.** `stopHealthMonitor` cancels *and joins* the
  monitor before any teardown, so the monitor can never republish after the
  sidecar is removed. The gate runs on the agent's operation context, so a
  teardown during the gate can in principle leave one stale sidecar; it is
  invisible to every reader (the CLI only reads metadata for VMs it discovered or
  knows from state, and neither exists for a removed task) and it is the same
  pre-existing window as the network write. Not fixed here.
