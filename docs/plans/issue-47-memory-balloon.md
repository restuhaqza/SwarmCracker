# Plan — #47 (part 1): memory balloon

Branch: `feat/memory-balloon` · Issue: [#47](https://github.com/restuhaqza/SwarmCracker/issues/47)
Program: `docs/plans/backlog-program.md` · Slice 3 of the sequential backlog.

## Current state (measured)

- VMs get a static size: `pkg/translator/translator.go:180-182` → `applyResources`
  (`:432-447`) → `MachineConfig{VcpuCount,MemSizeMib,Smt}` (`:106-110`), defaults
  1 vCPU / 512 MiB at `:158-162`. A second, divergent producer exists:
  `pkg/swarmkit/translator.go:59-81` sizes from `Resources.Reservations` and emits a
  `map[string]interface{}` literal at `:93-119`.
- **There is no balloon anywhere in the repo** (0 references).
- Bring-up, non-jailer (`pkg/lifecycle/vmm.go`): `Start` (`:156`) → spawn `:194` →
  `waitForAPIServer` `:215` → `configureVM` `:221`/def `:292` (boot-source `:310-333`,
  **machine-config `:336-359`**, drives `:362-396`, vsock `:401-426`) → **InstanceStart**
  inline in `Start` `:226-255` (`PUT /actions` `:235`).
- Bring-up, jailer (`pkg/swarmkit/vmm.go`): `configureVM` `:457`
  (machine-config `:469`, boot-source `:478`, drives `:505`, NICs `:537`, vsock `:550`)
  → **InstanceStart `:556-559`**. Both `startWithJailer` `:328`/`:439` and the
  non-jailer `Start` `:310` call this single `configureVM`.
- HTTP: the two packages have **separate** helpers. `pkg/swarmkit` already has
  `putAPI(ctx, socketPath, path string, data interface{}) error` (`vmm.go:1070`, errors
  wrapped as `API returned status %d: %s`); `pkg/lifecycle` has only
  `newUnixClient` (`:817-827`) plus hand-rolled requests, so it needs a small local
  `putAPI` equivalent.
- Latent defect noticed while reading: both translators emit the SMT key as `"smt"`
  (`pkg/translator/translator.go:109`, `pkg/swarmkit/translator.go:109`) while the
  lifecycle typed struct reads `ht_enabled` (`vmm.go:129`) and the jailer reads `smt`
  (`pkg/swarmkit/vmm.go:384`). T2 must determine whether that silently drops SMT on a
  live path and, if so, fix it or report it with evidence.
- CLI: `service create` maps `--cpu`/`--memory` into `Resources.Limits`
  (`cmd/swarmcracker/cmd_service_spec.go:215-223`); the existing label carrier pattern is
  `svcLabels[types.DiskSizeLabel]` (`cmd_service_spec.go:127-137`). `vm create` has
  `--cpu`/`--memory` (`cmd/swarmcracker/cmd_vm.go:292-293`) which bypass `Resources` and
  are passed as ints.
- Metrics: `pkg/metrics/prometheus.go` has `VMsRunning`/`VMsTotal`/`VMBootDuration`/
  `VMBootErrors` with real writers (called from `pkg/swarmkit/executor.go:870-889`); the
  four per-VM series (`VMCPU :117`, `VMMemory :126`, `VMNetRx/Tx :135/:144`) have **no
  writer** and are not this slice's problem.

## Design

Add a **virtio-balloon device** to the VM, configured at creation:

1. A service (or `vm create`) may declare a balloon target in MiB.
2. The target travels as the `swarmcracker.balloon` annotation (services) / lifecycle
   config field (`vm create`), and both VMM layers issue **`PUT /balloon` before
   `InstanceStart`** — the device cannot be added to a running VM.
3. The device is created with `deflate_on_oom: true`, so the guest can take the memory
   back when it is under pressure rather than getting OOM-killed.
4. A Prometheus gauge with a **real writer** records the configured target, so
   `swarmcracker_vm_balloon_mib{instance,task_id,service}` reflects it.

Absent configuration means **no `PUT /balloon` and byte-identical behaviour to today** —
this is the property the tests must pin.

### Interfaces (binding)

```go
// pkg/types/task.go
// BalloonLabel carries the memory-balloon target in MiB on a service, e.g.
// "swarmcracker.balloon=256". Absent means no balloon device is configured.
const BalloonLabel = "swarmcracker.balloon"

// BalloonConfig describes the virtio-balloon device configured for a VM.
type BalloonConfig struct {
	// AmountMib is how much memory the balloon reclaims from the guest (MiB).
	AmountMib int64
	// DeflateOnOOM lets the guest reclaim ballooned memory under memory pressure.
	DeflateOnOOM bool
}

// pkg/translator + pkg/swarmkit/translator
// Both emit, only when configured, next to the existing "vsock" key:
//   "balloon": map[string]interface{}{
//       "amount_mib":               <n>,
//       "deflate_on_oom":           true,
//       "stats_polling_interval_s": 0,
//   }

// pkg/metrics/prometheus.go
func RecordVMBalloon(instance, taskID, service string, mib float64)
```

CLI flag: `--balloon-mib <int>` (an explicit MiB integer rather than a `512m` string,
because the API needs an exact integer and the unit must not be ambiguous).

## Global constraints

- Go 1.26. `gofmt -l pkg cmd` must print nothing.
- Errors: `fmt.Errorf("...: %w", err)`; reject invalid input with a message naming the
  offending value — never accept a flag and ignore it.
- A flag/field that is not honoured must fail loudly. Absent config must not change any
  request bytes.
- Tests: table-driven + testify, host- and privilege-independent.
- Tests run on the Linux test server (`root@192.0.2.10`, checkout `/root/sc-verify`);
  this workstation is darwin and `cmd/...` does not build here.
- `pkg/...` coverage stays ≥ 85%; a touched package must not drop by more than 0.1pp.
- Docs live in `docs-site/src/content/docs/`.
- Pre-existing root-only failures exist in `pkg/image`, `pkg/swarmkit`, `pkg/network`;
  verify with `/root/sc-cmp.sh <base-sha> <pkgs...>` and require an **empty head-only**
  set rather than a green package.

## Task T1 — carry the balloon target from the spec to the VM config (pure)

Goal: a service can declare a balloon target and both translators emit it into the VM
config, with validation, and emit nothing when unset.

Files: `pkg/types/task.go`, `pkg/translator/translator.go`,
`pkg/swarmkit/translator.go`, and their `_test.go` files.

Steps:
1. Add `BalloonLabel` and `BalloonConfig` exactly as above.
2. In **both** translators, read the `types.BalloonLabel` annotation from the task. When
   absent, emit nothing (keep today's map/struct byte-identical). When present, parse it
   as a positive integer MiB and emit the `"balloon"` entry described above.
3. Validate against the machine size the translator already computed: an amount that is
   not a positive integer, or that is `>=` `MemSizeMib`, must fail the translation with a
   clear error naming the value and the memory size — not be ignored.
4. Keep the emission next to the existing `"vsock"` key so both producers stay in step.

Verification: `go test -count=1 ./pkg/translator/... ./pkg/swarmkit/... ./pkg/types/...`
on the test server (pkg/swarmkit has pre-existing root-only failures — use
`bash /root/sc-cmp.sh 6b99e8b ./pkg/translator/ ./pkg/swarmkit/ ./pkg/types/` and require
an empty head-only set). Tests must cover: annotation absent → no `balloon` key (assert
the exact absence, not just a nil); a valid value → the three fields with the right
values; a non-integer and a zero/negative value → error; an amount ≥ memory → error.

Depends on: —

## Task T2 — issue `PUT /balloon` before `InstanceStart` in both VMM paths

Goal: the device actually exists on the VM, in both the service/jailer path and the
`vm create` path.

Files: `pkg/swarmkit/vmm.go`, `pkg/lifecycle/vmm.go`, and their `_test.go` files.

Steps:
1. `pkg/lifecycle/vmm.go`: add a small local `putAPI(ctx, socketPath, path string, data interface{}) error` mirroring the swarmkit helper (`pkg/swarmkit/vmm.go:1070`),
   reusing `newUnixClient` (`:817-827`) and wrapping errors with the path.
2. In **both** `configureVM`s (`pkg/swarmkit/vmm.go:457`, `pkg/lifecycle/vmm.go:292`),
   after the machine-config step and **before** the caller's InstanceStart, issue
   `PUT /balloon` when a balloon config is present in the config being applied. The two
   insertion points are between `:553` and `:556` (swarmkit) and right after the
   machine-config block at `:359` (lifecycle).
3. Wire the value from the config the layer receives: the swarmkit path reads the
   `"balloon"` entry produced by T1; the lifecycle path reads its own config field (added
   here) which T3 will set from `vm create`. Report the exact data flow you found.
4. **Investigate the `smt`/`ht_enabled` mismatch** noted above: decide from the code
   whether a live path silently drops the SMT setting. If it does, fix it minimally
   (accept the key the translators actually emit) and add a test; if it does not, say so
   in the report with `file:line` evidence. Do not change behaviour for an unset value.
5. No balloon config → the requests are exactly as before (assert this).

Verification: `go test -count=1 ./pkg/swarmkit/... ./pkg/lifecycle/...` plus
`bash /root/sc-cmp.sh 6b99e8b ./pkg/swarmkit/ ./pkg/lifecycle/` with an empty head-only
set. Tests must cover, **in both packages**: the request sequence has `/balloon` before
`/actions` (clone the template in `pkg/swarmkit/vsock_test.go:137-157`, which uses
`capture.get("/vsock")` + `capture.sequence()`), the exact JSON body sent to `/balloon`,
and that no `/balloon` request is made when unconfigured.

Depends on: T1.

## Task T3 — CLI: `--balloon-mib` on services and on `vm create`

Goal: a user can request a balloon, and an invalid request fails before anything is
created.

Files: `cmd/swarmcracker/cmd_service.go`, `cmd/swarmcracker/cmd_service_spec.go`,
`cmd/swarmcracker/cmd_vm.go`, and their `_test.go` files.

Steps:
1. `service create`: add `--balloon-mib` and thread it into `serviceCreateOptions`, then
   into the service labels as `types.BalloonLabel` (follow the `--disk`/`DiskSizeLabel`
   pattern at `cmd_service_spec.go:127-137`; keep `buildServiceSpec` pure).
2. `service update`: the same flag, tri-state like `--with-registry-auth`: absent
   preserves the current label, a value sets it, and an explicit `0` removes it. Handle
   `anyFlagChanged`.
3. `vm create`: add `--balloon-mib` and set the lifecycle config field T2 added.
4. Validate before any RPC: a non-positive value is an error; a value `>=` the effective
   memory size is an error naming both numbers. An unset flag must not add a label.
5. Do not log anything that is not already logged.

Verification: `go test -count=1 ./cmd/swarmcracker/` plus
`bash /root/sc-cmp.sh 6b99e8b ./cmd/swarmcracker/` with an empty head-only set. Tests must
cover: the spec carries the label when set and not when unset (assert absence); the
update tri-state; the two validation errors; that the flag is registered on both
commands.

Depends on: T1.

## Task T4 — a balloon gauge with a real writer

Goal: the configured balloon target is observable, without adding a dead metric.

Files: `pkg/metrics/prometheus.go`, `pkg/swarmkit/executor.go`, `_test.go` files.

Steps:
1. Add `VMBalloonMB *prometheus.GaugeVec` (prefix `swarmcracker_`, name `vm_balloon_mib`,
   labels `instance,task_id,service`) and `RecordVMBalloon(instance, taskID, service
   string, mib float64)`, following the existing `RecordVMStarted` style
   (`prometheus.go:154,179`).
2. Call it where the VM actually starts (`pkg/swarmkit/executor.go:888-889`, next to
   `RecordVMStarted`), using the task's balloon annotation, and **only** when configured
   (no zero-valued series for every VM).
3. Unit-test the recorder; the writer path is proven by the controller's lab e2e (a real
   `/metrics` scrape), so say so in the report.

Verification: `go test -count=1 ./pkg/metrics/...` plus
`bash /root/sc-cmp.sh 6b99e8b ./pkg/metrics/ ./pkg/swarmkit/` with an empty head-only set.

Depends on: T2.

## Task T5 — documentation

Goal: the flag, the device, its limits and the metric are documented.

Files: `docs-site/src/content/docs/reference/cli.md`,
`docs-site/src/content/docs/guides/advanced.md` (or `guides/configuration.md` if that is
where resource tuning lives), and `guides/operations.md` for the metric.

Steps:
1. Document `--balloon-mib` on `service create`/`service update` and `vm create`: MiB
   units, must be less than the memory size, `deflate_on_oom` behaviour, and that the
   device is created before boot (so it cannot be added to a running VM).
2. State the guest requirement plainly: the balloon only takes effect if the guest kernel
   has the virtio-balloon driver; otherwise the device exists but the guest's memory is
   unchanged.
3. Document `swarmcracker_vm_balloon_mib` with its labels, and say it is only emitted for
   VMs that configured a balloon.
4. Verify with the docs pipeline (`cd docs-site && npm run verify` on Node 22).

Verification: docs pipeline passes; `grep` shows the flag and the metric documented.

Depends on: T3, T4.

## Verification (program-level, by the controller)

- `go test` for the touched packages as root and as an unprivileged user.
- `/root/sc-cmp.sh` with an empty head-only set, plus `verify.sh`.
- CI green (test / lint / build / coverage gate).
- **Lab e2e (2 nodes)**: `service create --memory 512m --balloon-mib 256` pinned to one
  node; then on that node query the Firecracker API socket directly
  (`curl --unix-socket <socket> -X GET http://localhost/balloon`) to prove the device
  exists with `amount_mib: 256`, scrape the daemon's `/metrics` for
  `swarmcracker_vm_balloon_mib`, and read `MemTotal` inside the guest via
  `vm exec` to record whether virtio-balloon actually took the memory (with the guest
  kernel's support stated explicitly either way). Negative control: a service without the
  flag must have **no** balloon endpoint (the GET must 404) and no metric. Restore the lab
  to `main` afterwards.
- Independent review before merge.

## Acceptance mapping (the #47 criteria this slice closes)

| Criterion | Where |
|---|---|
| Balloon device configurable per service/VM | T1–T3 + lab e2e |
| Opt-in with a clear failure when the host cannot satisfy it | T1/T3 validation (`amount >= memory` is rejected with both numbers) |
| Metrics reflect ballooned memory | T4 + lab e2e scrape |
| No behaviour change when unconfigured | T1/T2/T3 absence tests |
| Docs updated | T5 |

## Out of scope (recorded)

- **Runtime adjustment** of the balloon target (`PATCH /balloon` on a live VM): the CLI
  cannot address another node's Firecracker socket today, so this needs a manager-side
  path; it is a second slice.
- The other three #47 sub-areas: CPU pinning/`--cpuset`, hugepages, cgroup v2 (program
  slices 5 and 6). NUMA stays rejected-with-a-clear-error per slice 6.
- The `pkg/metrics` per-VM series with no writer (`VMCPU`/`VMMemory`/`VMNetRx/Tx`) — that
  is #88's in-guest metrics work.
