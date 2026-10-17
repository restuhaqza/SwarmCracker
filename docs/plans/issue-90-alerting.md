# Plan — #90: alerting (crash-loop, unhealthy task, disk, notification sink)

Branch: `feat/alerting` · Issue: [#90](https://github.com/restuhaqza/SwarmCracker/issues/90)
Slice 4 of the sequential backlog program (`docs/plans/backlog-program.md`).

## Current state (verified by recon, with file:line)

The alerting story is broken in three independent ways:

1. **The rules are never loaded.** `infrastructure/observability/prometheus/prometheus.yml`
   has no `rule_files:` key, so `alerts.yml` (mounted by `docker-compose.yml:15-16`) is
   inert. There is no `alerting:` block either.
2. **Most rules reference series that do not exist.** Of the 15 series declared in
   `pkg/metrics/prometheus.go`, only 6 have a writer (`vms_running`, `vms_total`,
   `vm_boot_duration_seconds`, `vm_boot_errors_total`, `vm_balloon_mib`, and the
   Prometheus-generated `up`). The rules also reference `node_filesystem_*`,
   `swarmcracker_vms_expected` and `consul_health_node_status`, exported nowhere in this
   repo. A rule on a non-existent series can never fire — it is false confidence.
3. **The signals the failure modes need do not exist.** There is no crash counter
   (only the overloaded `vm_boot_errors_total`), no health metric (health state lives in
   node-local sidecars and one HTTP endpoint, `pkg/runtime/metadata.go:22-75`), and
   `swarmcracker_disk_usage_bytes` is declared but never written.

Two real bugs surfaced on the way and are in scope because this slice owns these metrics:

- `RecordVMCrashed` increments **both** `vm_boot_errors_total` and decrements `vms_running`,
  and it is called from the boot-failure site (`pkg/swarmkit/executor.go:885`) where the VM
  was never counted as running. So a boot failure drives `vms_running` **negative** and
  reports a boot failure as a crash.
- The reason label mixes boot failures (`boot_failure`) with post-start crashes
  (`unexpected_exit`, `unhealthy`, `exit_error`, `force_terminate`) — so the existing
  `VMBootFailure` rule fires for ordinary workload exits.

## Design

### Signals (one writer each, no second source of truth)

| Metric | Type | Labels | Writer |
|---|---|---|---|
| `swarmcracker_vm_boot_errors_total` | CounterVec | `instance,reason` | **only** the boot-failure path (`startVM`), reason always `boot_failure` |
| `swarmcracker_vm_crashes_total` (new) | CounterVec | `instance,service,reason` | the post-start failure paths in `Wait`/`Terminate` (`unexpected_exit`, `unhealthy`, `exit_error`, `force_terminate`) |
| `swarmcracker_vm_health` (new) | GaugeVec (one-hot 0/1) | `instance,service,state` | `Controller.publishHealth` — the single choke point where health state changes |
| `swarmcracker_disk_usage_bytes` (has no writer today) | GaugeVec | `instance,path` | a sampler on the existing daemon ticker |
| `swarmcracker_disk_total_bytes` (new) | GaugeVec | `instance,path` | the same sampler |

- **Health** reuses `pkg/runtime.VMHealth`'s existing state strings
  (`starting`/`healthy`/`failing`/`unhealthy`) rather than inventing a health source. It is
  a **one-hot** gauge: exactly one state has value 1 for a given
  `{instance,service}`; the previous state is set to 0 on transition, so
  `swarmcracker_vm_health{state="unhealthy"} > 0` is a valid alert expression.
- **Disk** is sampled on the existing `periodicCleanup` ticker
  (`pkg/swarmkit/executor.go:328-358`, 5-minute `vmTicker`), one series per configured path
  (`rootfs`, `snapshots`, `state`), via `syscall.Statfs` (the precedent is
  `pkg/image/preparer.go:789-796`). A path that is not configured or does not exist produces
  **no series** (not a zero) and a debug log. Total is exported alongside usage so a
  pressure alert can be a ratio.

### Rules

`alerts.yml` is rewritten so that **every** `expr` references a series that a writer in this
repo actually sets, and `prometheus.yml` gains `rule_files:` + an `alerting:` block pointing
at a new Alertmanager service. Rules that reference series this repo does not export
(`node_filesystem_*`, `consul_health_node_status`, `swarmcracker_vms_expected`,
`swarmcracker_manager_health`, `swarmcracker_raft_health`, `swarmcracker_vxlan_*`) are
**removed**, not left to look functional. The four required failure modes get default rules:

```
VMBootFailureLoop   increase(swarmcracker_vm_boot_errors_total[15m]) > 3
VMCrashLoop         increase(swarmcracker_vm_crashes_total[15m]) >= 3
TaskUnhealthy       swarmcracker_vm_health{state="unhealthy"} > 0   (for 2m)
DiskWarning/Critical  swarmcracker_disk_usage_bytes / swarmcracker_disk_total_bytes > 0.8 / 0.9
NodeDown            up{job="swarmcracker"} == 0
```

### Notification sink

A new `infrastructure/observability/alertmanager/alertmanager.yml` with a **webhook** sink
enabled by default (a documented local listener) and Slack/email shown as commented
configuration; an `alertmanager` service added to `docker-compose.yml`; and a new
`docs-site/src/content/docs/guides/alerting.md` documenting the sink, the routes and how to
verify a simulated condition.

## Interfaces (binding)

```go
// pkg/metrics/prometheus.go — one helper per series, mirroring the existing Record* style.

// RecordVMBootError counts a VM that never reached running. It must NOT touch vms_running
// or the crash counter.
func RecordVMBootError(instance, reason string)

// RecordVMCrash counts a VM failure after it reached running. It decrements vms_running
// (the VM was counted running) and does NOT touch vm_boot_errors_total.
func RecordVMCrash(instance, service, reason string)

// RecordVMHealth publishes a one-hot health state for a service on a node: the given state
// becomes 1 and the other three become 0. An empty/unknown state clears all four series.
func RecordVMHealth(instance, service, state string)

// RecordDiskUsage publishes used and total bytes for one path. Called only for paths that
// exist; never called to publish a missing path as 0.
func RecordDiskUsage(instance, path string, usedBytes, totalBytes uint64)
```

`RecordVMCrashed` is **removed** (it is the overloaded helper); its two call sites become
`RecordVMBootError` (the boot path) and `RecordVMCrash` (the post-start paths).

## Global constraints

- Go 1.26. `gofmt -l pkg cmd` must print nothing. No `@ts-ignore`-style silencing; no flag
  or setting accepted and ignored.
- Do not create a second health source: read `pkg/runtime`'s existing state.
- Nothing may change when a VM has no healthcheck and no failure: no new series, and the
  existing metrics keep their current meaning (`vms_running` may no longer go negative —
  that is the one intended correction, and it must be covered by a test).
- Tests: table-driven + testify, **host- and privilege-independent**. A command-level test
  that loads configuration must supply it explicitly (temp `--config`); never rely on
  `/etc/swarmcracker/config.yaml` (CI has none).
- Tests run on the Linux test server (`root@192.0.2.10`, checkout `/root/sc-verify`);
  this workstation is darwin and `cmd/...` does not build here.
- Pre-existing root-only failures exist in `pkg/image`, `pkg/swarmkit`, `pkg/network`.
  Verify with `/root/sc-cmp.sh <base-sha> <pkgs...>` and require an **empty head-only** set.
- Docs live in `docs-site/src/content/docs/` and are verified with `cd docs-site && npm run verify`.
- Metrics docs must not over-promise: if a series still has no writer after this slice, say
  so (the file already carries such a caveat at `guides/operations.md:339-343`).

## Task T1 — the metrics and their writers

Goal: every signal above has exactly one writer, and the boot/crash split is real.

Files: `pkg/metrics/prometheus.go`, `pkg/metrics/prometheus_test.go`,
`pkg/swarmkit/executor.go`, plus a new `pkg/swarmkit/diskusage.go` (or similar) and its test.

Steps:
1. Implement the four helpers above with the semantics in the interface block. Register the
   two new series with `promauto` like their neighbours; keep the namespace and help text
   style. `RecordVMCrashed` is deleted.
2. Repoint the call sites: `startVM`'s boot failure (`executor.go:885`) →
   `RecordVMBootError(host, "boot_failure")`; the `Wait`/`Terminate` paths (`:1084`, `:1093`,
   `:1102`, `:1187`) → `RecordVMCrash(host, service, reason)` with the **same reason strings**
   they use today. Keep `RecordVMStarted`/`RecordVMStopped` unchanged, so
   `vms_running` only ever decrements for a VM that was started.
3. Call `RecordVMHealth` from `publishHealth` (`executor.go:1050-1073`) with the state it is
   already about to persist. If the service name is not directly available there, derive it
   the way `startVM` does (`executor.go:899-902`) — do not change `publishHealth`'s signature
   if you can avoid it; if you cannot, say so in your report.
4. Add the disk sampler: a small function that, given the executor's configured paths
   (rootfs, snapshots, state), `Statfs`s each existing one and calls `RecordDiskUsage`.
   Call it once at daemon start and on the existing 5-minute ticker in `periodicCleanup`
   (`:328-358`). A missing path → no series + a debug log.
5. Tests (non-vacuous — each must fail if its writer is removed or the semantics inverted):
   boot error does **not** touch `vms_running`/crashes; crash decrements `vms_running` and does
   **not** touch `vm_boot_errors_total`; the one-hot health gauge flips exactly one state to 1
   and the rest to 0, and clears on an empty state; the disk sampler publishes a series for a
   temp dir that exists and none for a path that does not, with the right byte values.

Verification: `go test -count=1 -race ./pkg/metrics/... ./pkg/swarmkit/` and
`bash /root/sc-cmp.sh <base> ./pkg/metrics/ ./pkg/swarmkit/` with an empty head-only set.

Depends on: —

## Task T2 — rules, wiring, and the Alertmanager sink

Goal: the rules load, they only reference exported series, and a sink exists.

Files: `infrastructure/observability/prometheus/alerts.yml`,
`infrastructure/observability/prometheus/prometheus.yml`,
`infrastructure/observability/alertmanager/alertmanager.yml` (new),
`infrastructure/observability/docker-compose.yml`,
`pkg/metrics/alertrules_test.go` (new).

Steps:
1. Rewrite `alerts.yml`: keep `SwarmCrackerNodeDown` and the four required failure-mode
   rules above, with sane `for:` durations, `severity` labels and annotations that name the
   runbook. Remove every rule whose metric this repo does not export (list them in your
   report). Keep the existing group/file style.
2. Add `rule_files: [alerts.yml]` and an `alerting: alertmanagers:` block to
   `prometheus.yml`, pointing at the Alertmanager service by its compose DNS name.
3. Add `alertmanager/alertmanager.yml` with a default **webhook** receiver (a documented
   local endpoint), sensible `route`/`group_by` (by `alertname`, `instance`), `repeat_interval`,
   and Slack/email receivers **commented out** with the placeholders an operator must fill.
   Add the `alertmanager` service to `docker-compose.yml` (same network as Prometheus, config
   mounted read-only, port published like the others).
4. **The guard test** (`pkg/metrics/alertrules_test.go`): parse `alerts.yml` with
   `gopkg.in/yaml.v3` and, for every rule `expr`, extract the metric names and assert each one
   exists in the **registered** metric families (gather them from the default Prometheus
   registry — this ties the rule file to the code, not to a hand-maintained allowlist). It
   must also assert the file has at least one rule for each of the four failure modes, and
   that `prometheus.yml` references the rules file (`rule_files`). A test that would pass with
   a rule pointing at a typo'd metric name is not acceptable. Parse from the repo root
   relative path; if the path is wrong in CI the test fails loudly (do not skip on a missing
   file).
   Also assert `alertmanager.yml` parses and its default receiver is reachable/configured
   (i.e. the route's receiver name is defined).

Verification: `go test -count=1 -run Alert ./pkg/metrics/` plus the existing metrics tests.
If `promtool` is available on the test server, run `promtool check rules alerts.yml` and
record the output; if not, say so (the Go test is the authoritative check).

Depends on: T1 (the rule metric names must match what T1 writes).

## Task T3 — documentation

Goal: an operator can turn alerting on and verify it.

Files: new `docs-site/src/content/docs/guides/alerting.md`;
`docs-site/src/content/docs/guides/operations.md` (metrics section);
`docs-site/src/content/docs/reference/api.md` if it lists metrics; `configuration.md` if it
claims metrics are not implemented.

Steps:
1. New `guides/alerting.md`: what the four failure modes are and which metric each rule
   reads, the notification sink and how to change it, how the rules are wired to Prometheus,
   and a **verification recipe** for a simulated condition (start the stack, force a crash
   loop / a failing healthcheck / a disk threshold, and see the alert reach the sink — or the
   config check if a live page is not possible).
2. Update the metrics list in `operations.md` to the truth after this slice: which series are
   written now, which still are not, and the removal of the rules that referenced non-existent
   series.
3. Correct any page that says metrics are not implemented
   (`configuration.md:362-367`) if that is now false — and if it is still true for some
   series, say exactly which.
4. Verify with the docs pipeline (`cd docs-site && npm run verify` on Node 22).

Depends on: T1, T2.

## Verification (program-level, by the controller)

- `go test -count=1 ./pkg/metrics/... ./pkg/swarmkit/...` + `sc-cmp.sh` with an empty
  head-only set + `verify.sh`.
- CI green (test / lint / build) — the only non-root, config-less environment.
- **Lab e2e (2 nodes):** deploy the branch; scrape each node's `/metrics` and prove, with a
  simulated condition:
  1. a service that exits immediately repeatedly → `swarmcracker_vm_crashes_total` rises and
     `swarmcracker_vm_boot_errors_total` does **not**;
  2. a service with a failing healthcheck → `swarmcracker_vm_health{state="unhealthy"}` becomes
     1 and the other states 0;
  3. `swarmcracker_disk_usage_bytes` and `_total_bytes` are present for the configured paths;
  4. a normal service does not move the crash counter and shows `state="healthy"` (or no series
     when it has no healthcheck).
  Then `promtool check rules` if available, and validate `prometheus.yml` parses with the new
  `rule_files`/`alerting` blocks. Restore the lab to `main` afterwards.
- Independent review before merge.

## Acceptance mapping (#90)

| Criterion | Where |
|---|---|
| Four failure modes have default rules | T2 (`alerts.yml`) + the guard test |
| A documented notification sink | T2 (Alertmanager) + T3 (`guides/alerting.md`) |
| Every rule references an exported metric | T2's guard test (registry-gathered) |
| Rules file parses; each new metric has a writer test | T1 + T2 tests |
| Documented verification that a simulated condition produces the signal | T3 + the lab e2e |
| Docs updated | T3 |

## Out of scope (recorded)

- Exporting manager/raft health, VXLAN peer counts and Consul health — their rules are
  removed here rather than left inert; each needs a writer in the process that owns the
  signal (the manager, not the agent).
- In-guest per-task CPU/memory/network series (`#88`) — still declared-but-unwritten, and
  `operations.md` keeps saying so.
- A Grafana dashboard for the new series (the existing dashboards are unchanged).
