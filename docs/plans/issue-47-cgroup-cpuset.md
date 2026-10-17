# Plan — #47 part 2: cgroup v2 enforcement + `--cpuset`

Branch: `feat/cgroup-cpuset` · Issue: [#47](https://github.com/restuhaqza/SwarmCracker/issues/47)
Slice 5 of the sequential backlog program (`docs/plans/backlog-program.md`).
#47 stays open after this slice (hugepages + NUMA rejection = slice 6).

## Current state (verified, with file:line)

`pkg/jailer/cgroup.go` already implements a v2 manager, but it is **not the feature the
issue asks for**:

| Fact | Evidence |
|---|---|
| `ResourceLimits` has CPU/memory/IO quota fields, **no cpuset** | `cgroup.go:23-49` |
| No `cpuset.cpus`/`cpuset.mems` write anywhere in the repo | grep: zero non-doc hits |
| `io.weight` is written as bare `"100"` — the v2 format is `default <weight>`, so a real kernel rejects it (the write is warning-only, which is why nobody noticed) | `cgroup.go:290-296` |
| No controller delegation: nothing ever writes `cgroup.subtree_control`, so `cpu`/`memory`/`io`/`cpuset` are never enabled for child cgroups | grep: no `subtree_control` |
| The manager is created **only** when `--enable-jailer` is on (default **false**) | `pkg/swarmkit/vmm.go:146,198` |
| Limits are hardcoded from the VM's vCPU/mem, never from the task spec (`NanoCPUs` never reaches it); memory limit is exactly the guest size, which would OOM-kill the VMM (it carries guest RAM **plus** its own overhead) | `vmm.go:425-430` |
| `pkg/lifecycle` (`swarmcracker vm create`) has **zero** cgroup code | grep |
| `--cpuset` does not exist on any command | grep |

## Design

### 1. `pkg/jailer` primitives

- `ResourceLimits` gains `CpusetCPUs string` and `CpusetMems string`.
- `CreateCgroup` writes `cpuset.cpus` when non-empty, and seeds `cpuset.mems` **before**
  `cgroup.procs` — a v2 cpuset cgroup with empty `mems` cannot accept a process. When
  `CpusetMems` is empty, inherit the parent's `cpuset.mems`; if the parent has none, use
  the host's online node list. Never leave `mems` empty.
- Kernel rejection of a cpuset is translated into an actionable error naming the request
  **and** the host's online CPUs (`/sys/devices/system/cpu/online`), because "the VM ran
  unpinned" is not an acceptable outcome.
- `EnsureControllers()`: idempotent. Reads `cgroup.controllers` at the base path, enables
  the requested subset (`cpu memory io cpuset`) in the base's **and** its parent's
  `cgroup.subtree_control` (additive: never disables what is already enabled, never writes
  a controller the kernel does not advertise). Returns a typed error naming which
  controllers could not be enabled and why (the common real cause: the service's own cgroup
  is not delegated — documented as needing `Delegate=yes`).
- Fix `io.weight` to `default <weight>`; keep `io.max` as-is (BPS only).
- `ParseCPUSet(s string) (string, error)`: validates Linux cpulist syntax (`0`, `0-3`,
  `0-3,8,10-11`), rejects empty/negative/reversed/zero-padded-garbage, returns the
  **canonical** form (ascending, merged adjacent ranges) so two equivalent inputs compare
  equal. Lives in `pkg/jailer/cpuset.go` so the CLI and the writer share one parser.

### 2. Limits derived from the task, not from defaults

`types.ResolveCgroupLimits(task)` (new, `pkg/types/task.go`) returns plain values:

```go
type CgroupLimits struct {
    CPUQuotaUs  int64  // NanoCPUs/1000, or vcpus*1_000_000 when unset
    MemoryMaxB  int64  // guest bytes + CgroupMemoryHeadroom (128 MiB); 0 when unknown
    CpusetCPUs  string // canonical, "" when not requested
}
const CgroupMemoryHeadroom = 128 << 20
```

Rationale for the headroom: the cgroup holds the **VMM process**, whose resident set is the
guest's touched pages **plus** Firecracker's own ~10–30 MiB. A limit equal to the guest size
OOM-kills the VMM. 128 MiB is a documented constant, not a guess hidden in a literal.

### 3. Where it is applied

- **Daemon path** (`pkg/swarmkit/vmm.go`): after the VMM process exists — **both** the jailer
  and the direct start — `CreateCgroup` + `AddProcess(pid)`. The existing jailer-only calls
  move onto that single path (no double-create). Removal on teardown stays where it is today
  (and must actually run: no cgroup directory may survive its VM).
- **Lifecycle path** (`pkg/lifecycle/vmm.go`, `vm create`): the same two calls, but **only
  when a cpuset was requested** (`--cpuset`). Reason: the hypervisor already bounds guest
  CPU/memory; the placement promise is what `vm create --cpuset` makes, so that one must be
  honoured or fail.
- **Failure policy** (recorded ruling): a **cpuset** that cannot be applied is a **hard
  error** for that VM — the VM is not started and the task reports why. A **quota/IO**
  failure is a **warning** and the VM runs, because the guest is already bounded by the
  hypervisor and the cgroup is defence in depth. This keeps the "no silently ignored flag"
  contract for the setting that changes placement while not bricking hosts whose cgroup
  hierarchy is not delegated.

### 4. CLI surface

`--cpuset` on `service create`, `service update` and `vm create`. Pre-RPC validation via
`jailer.ParseCPUSet` (a malformed value never reaches the manager). Explicit-set tracking
like `--balloon-mib`'s (`cmd_service_spec.go:35-39`), so `service update --cpuset ""` is a
deliberate clear rather than an accident. The value travels as service label
`swarmcracker.cpuset` bridged to task annotation `swarmcracker.cpuset` — exactly the
`BalloonLabel` pattern (`pkg/swarmkit/executor.go:772-777`); `vm create` sets the task
annotation directly (`cmd_vm.go:341`).

## Interfaces (binding)

```go
// pkg/jailer/cpuset.go
func ParseCPUSet(s string) (string, error) // canonical ascending merged form

// pkg/jailer/cgroup.go
type ResourceLimits struct {
    CPUQuotaUs  int64
    CPUMax      string
    MemoryMax   int64
    MemoryHigh  int64
    IOWeight    int
    IOReadBPS   int64
    IOWriteBPS  int64
    CpusetCPUs  string // NEW, canonical; "" = do not pin
    CpusetMems  string // NEW; "" = inherit parent/host
}
func (m *CgroupManager) EnsureControllers(controllers ...string) error
func (m *CgroupManager) CreateCgroup(taskID string, limits ResourceLimits) error // + cpuset
func (m *CgroupManager) OnlineCPUs() (string, error)                            // /sys/devices/system/cpu/online

// pkg/types/task.go
const CpusetLabel = "swarmcracker.cpuset"
const CgroupMemoryHeadroom int64 = 128 << 20
type CgroupLimits struct{ CPUQuotaUs, MemoryMaxB int64; CpusetCPUs string }
func ResolveCgroupLimits(task *Task) (CgroupLimits, error) // validates the annotation
```

`pkg/types/annotations.go` gains `AnnotationCpuset = "swarmcracker.cpuset"`.

## Global constraints

- Go 1.26. `gofmt -l pkg cmd` prints nothing. No flag or setting accepted and ignored: every
  explicit value is honoured or rejected with an error naming the flag and the offending value.
- Tests must be **non-root and CI-safe**: a real `/sys/fs/cgroup` assertion only behind the
  existing `isCgroupV2Available()` skip; everything else against `t.TempDir()` (the pattern in
  `pkg/jailer/cgroup_test.go:23-28`). Table-driven + testify.
- Tests run on the Linux test server (`root@192.168.18.25`, checkout `/root/sc-verify`); the
  workstation is darwin (`cmd/...` does not build here). A command-level test that loads
  configuration must pass it explicitly (temp `--config`) — never rely on
  `/etc/swarmcracker/config.yaml`.
- Pre-existing root-only failures: `pkg/image` (9), `pkg/swarmkit` (`TestController_Prepare_Errors`
  + `prepare_with_valid_image`, `TestStartDirect_Push_SocketDirError`). Verify with
  `/root/sc-cmp.sh <merge-base> <pkgs...>`; require an **empty head-only** set.
- Docs live in `docs-site/src/content/docs/`; verify with `cd docs-site && npm run verify` (Node 22).
- Do not change the balloon, sizing, alerting or snapshot behaviour.

## Task T1 — cgroup v2 primitives (`pkg/jailer`)

Goal: the manager can enable controllers, pin a cgroup, and write a valid `io.weight`.

Files: `pkg/jailer/cgroup.go`, new `pkg/jailer/cpuset.go`, `pkg/jailer/cgroup_test.go`,
new `pkg/jailer/cpuset_test.go`.

Steps:
1. Add `ParseCPUSet` with canonicalisation (ascending, merge adjacent: `"3,1-2"` → `"1-3"`)
   and strict rejection (empty, `a`, `1-`, `-1`, `5-3`, `1,,2`, whitespace-only).
2. Add `CpusetCPUs`/`CpusetMems` to `ResourceLimits`; write `cpuset.cpus` and seed
   `cpuset.mems` (parent → host fallback) **before** `cgroup.procs`; wrap kernel errors with
   the requested value + `OnlineCPUs()`.
3. Add `EnsureControllers` as specified (additive, idempotent, typed error).
4. Fix `io.weight` to `default <weight>`.
5. `OnlineCPUs()` reading `/sys/devices/system/cpu/online`.
6. Tests: parser table (valid/invalid/canonical equivalence); `CreateCgroup` in a temp base
   asserts exact `cpuset.cpus`/`cpuset.mems`/`cpu.max`/`memory.max`/`io.weight` contents and
   that `mems` is written before `procs` (order matters — assert via the failure path when
   `mems` cannot be resolved); cpuset rejection messages name both the request and the online
   set; `EnsureControllers` against a fake hierarchy in a temp dir (no `/sys`).

Verification: `go test -count=1 -race ./pkg/jailer/` + `sc-cmp.sh <merge-base> ./pkg/jailer/`
with an empty head-only set. Depends on: —

## Task T2 — resolve from the spec and apply on both VMM paths

Goal: a service's declared CPU/memory/cpuset reaches its VM's cgroup, on the daemon and on
`vm create`, and a rejected cpuset stops the VM with a clear reason.

Files: `pkg/types/task.go`, `pkg/types/annotations.go`, `pkg/types/task_test.go`,
`pkg/swarmkit/vmm.go`, `pkg/swarmkit/executor.go` (the label→annotation bridge),
`pkg/lifecycle/vmm.go` (+ its manager config), the tests for both.

Steps:
1. `types.CgroupLimits` + `ResolveCgroupLimits` per the interface block; unit tests for the
   precedence (explicit NanoCPUs → quota; 0 → vCPU-derived; MemoryBytes → +headroom; missing
   annotation → no cpuset; malformed annotation → error naming the value).
2. Bridge `swarmcracker.cpuset` exactly like `BalloonLabel` (`executor.go:772-777`).
3. Daemon: `vmm.go` derives limits via `ResolveCgroupLimits`, calls `EnsureControllers` once,
   then `CreateCgroup`+`AddProcess` on **both** the jailer and direct start paths; move the
   existing jailer-only calls so a VM gets exactly one cgroup. A cpuset failure returns an
   error from `Start` (task FAILED, message names the request and the host's online CPUs); a
   quota/IO failure logs a warning and continues (ruling above).
4. Lifecycle: apply cgroups only when the task carries a cpuset annotation; otherwise behave
   exactly as today.
5. Tests: the daemon path with a fake VMM/jailer asserting `CreateCgroup` was called with the
   derived limits (fake manager injected, no `/sys` writes); the lifecycle path likewise;
   teardown removes the cgroup (assert `RemoveCgroup` ran); a cpuset rejection surfaces as an
   error from `Start`, not a started VM.

Verification: `go test -count=1 -race ./pkg/types/ ./pkg/swarmkit/ ./pkg/lifecycle/` +
`sc-cmp.sh` empty head-only (allowed pre-existing `pkg/swarmkit` failures only).
Depends on: T1.

## Task T3 — `--cpuset` on the CLI

Goal: the flag exists, is validated before any RPC, and is never silently dropped.

Files: `cmd/swarmcracker/cmd_service.go`, `cmd_service_spec.go` (+ `_test.go`),
`cmd_vm.go`, `cmd_service_update.go` (or wherever `service update` builds its spec),
`cmd_vm_test.go` / `cmd_service_spec_test.go`.

Steps:
1. `service create --cpuset <cpulist>`: validate with `jailer.ParseCPUSet`; set
   `types.CpusetLabel` on the service; explicit-set bit like `--balloon-mib`; a malformed
   value is an error naming the flag and the value.
2. `service update --cpuset`: tri-state (unset → unchanged; `""` → clear the label; value →
   validate + set), consistent with the existing `--balloon-mib` update semantics.
3. `vm create --cpuset`: same validation; set `types.AnnotationCpuset` on the task.
4. `--help` text names the syntax and that the host must have those CPUs online.
5. Tests: table-driven over valid/invalid values for all three commands (the command-level
   tests must pass a temp `--config` — CI has no `/etc/swarmcracker/config.yaml`); assert the
   label/annotation is set, that a bad value fails pre-RPC, and that `update` without the flag
   leaves it untouched.

Verification: `go test -count=1 -race ./cmd/swarmcracker/` + `sc-cmp.sh` empty head-only.
Depends on: T2.

## Task T4 — documentation

Goal: an operator can turn this on, knows what is enforced, and can verify it.

Files: `docs-site/src/content/docs/reference/cli.md`,
`guides/advanced.md`, `guides/configuration.md`, `guides/security.md`.

Steps:
1. `reference/cli.md`: `--cpuset` in the `service create` / `service update` / `vm create`
   flag tables, with the cpulist syntax and the "host must have those CPUs online" rule.
2. `guides/advanced.md`: a new "CPU pinning and cgroup limits" section — what is enforced
   (cpuset = hard promise; CPU/memory/IO quota = defence in depth, warned-not-fatal), the
   memory headroom constant and why, and the on-host verification commands
   (`cat /sys/fs/cgroup/swarmcracker/<task>/cpuset.cpus`, `cgroup.procs`, `taskset -pc <pid>`).
3. `guides/configuration.md`: the cgroup requirements (v2 mounted, controller delegation /
   `Delegate=yes`), and the exact failure the operator sees when the host cannot delegate.
4. `guides/security.md`: add cpuset to the daemon-flags/verification material it already has
   for `--parent-cgroup`/`--cgroup-version`/`--enable-cgroups`.
5. Verify with the docs pipeline.

Depends on: T3.

## Verification (program level, by the controller)

- `verify.sh feat/cgroup-cpuset main` → head-only empty; lint 0 issues.
- CI green (test / lint / build).
- **Lab e2e (2 nodes), branch binaries:**
  1. `--cpuset 0` on a service → `/sys/fs/cgroup/swarmcracker/<task>/cpuset.cpus` is `0`,
     `cgroup.procs` holds the Firecracker PID, and `/proc/<pid>/status` `Cpus_allowed_list`
     is exactly `0`;
  2. `--cpuset 999` (not online) → the task FAILS with a message naming `999` and the host's
     online CPUs, and **no** VM is left running (hard error, not a silent unpinned run);
  3. `--cpu 2 --memory 1024m` → `cpu.max` = `2000000 1000000`, `memory.max` = guest + 128 MiB,
     `io.weight` = `default 100` (accepted by the kernel, unlike the old bare `100`);
  4. teardown → the cgroup directory is gone, no leaked dirs/PIDs;
  5. a run without `--cpuset` still works and is not pinned.
  Then restore the lab to `main`.
- Independent review per task + a final whole-branch review.

## Acceptance mapping (#47, this slice's share)

| Criterion | Where |
|---|---|
| `--cpuset` pins vCPUs, verified on a KVM node | T1+T2+T3, lab e2e (1) |
| cgroup v2 limits applied and observable from the host | T1+T2, lab e2e (3)(4) |
| QoS exposed via `service create` / `vm create` | T3 |
| Unit tests for config mapping + on-node verification for pinning | T1–T3 tests, lab e2e (1)(2) |
| Docs updated (`configuration.md`, `cli.md`) | T4 |

## Out of scope (recorded)

- **Hugepages** and **NUMA rejection** → slice 6 (closes #47).
- Runtime balloon adjustment (`PATCH /balloon`) — still an open decision, not this slice.
- A cgroup metrics exporter (`cpu.stat` is readable from the host; adding series belongs with
  #88's in-guest metrics work).
- Jailer's own `--cgroup` argument path: the manager (post-start `AddProcess`) is the single
  mechanism for both start paths; populating the jailer's `--cgroup` flags as well would give
  two sources of truth.
- `pkg/swarmcracker-agent` (`cmd/swarmcracker-agent/main.go:126` hardcodes
  `EnableCgroups: false`) — that daemon has no jailer/cgroup wiring; left as is.
