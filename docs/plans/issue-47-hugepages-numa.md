# Issue #47 part 3 — hugepages + NUMA placement (closes #47)

Status: **plan of record** for slice 6 of `docs/plans/backlog-program.md`.
Base: `main` @ `3f33ec9` (slice 5 merged). Branch: `feat/hugepages-numa`.

## Why

#47's remaining scope items are "opt-in hugepages" and "optional NUMA
placement". Slice 5 delivered cgroup v2 enforcement and `--cpuset`; the issue
stays open only for these two. Its acceptance criteria require hugepages to be
requestable per VM and to **fail clearly** when the host cannot satisfy the
request.

Firecracker v1.15.1 supports `huge_pages: "None" | "2M"` in
`PUT /machine-config` (`docs/hugepages.md` in the Firecracker tree). Two facts
shape the design:

1. Firecracker maps guest memory with `MAP_NORESERVE` and draws from the host's
   **pre-allocated** 2M hugetlbfs pool. If the pool is too small, Firecracker
   "may behave erratically or receive SIGBUS" — so a pre-flight check is a
   correctness requirement, not a nicety.
2. Snapshots of hugepage-backed VMs can only be restored via **UFFD**; the
   file-backed restore path this repo uses cannot restore them. That is a
   documented limitation, not something this slice can fix (UFFD restore is
   #46 / slice 12).

Firecracker has **no NUMA topology**: a guest sees one node. Host-side placement
is still meaningful and is what the issue asks for ("optional NUMA placement"),
so `--numa-node` binds the VM's **memory** to the named host NUMA nodes through
`cpuset.mems`, which the kernel enforces (observable as `Mems_allowed_list` in
`/proc/<pid>/status`). Combining it with `--cpuset` gives a full node placement.

## Surface

| Flag | Verbs | Meaning |
|---|---|---|
| `--hugepages` | `service create`, `service update`, `vm create` | Back guest memory with 2M huge pages. Boolean; on update, `--hugepages=false` clears it. |
| `--numa-node <nodelist>` | `service create`, `service update`, `vm create` | Bind the VM's memory to these host NUMA nodes (Linux cpulist syntax, e.g. `0`, `0-1`). On update, `--numa-node ""` clears it. |

Storage is the established label→annotation bridge:

- `swarmcracker.hugepages=true` (only `true` is stored; absent = 4K memory)
- `swarmcracker.numa-node=<canonical nodelist>`

Both flags are validated **before any RPC** (no scheduler admission, like
`--cpuset`), canonicalised (`1,0` → `0-1`), and rejected together with
`--rollback` (a rollback restores the previous spec and ignores other flags).

## Semantics (rulings)

- **R1 — hugepages is a hard promise.** The node pre-checks the host pool before
  starting the VMM: 2M pages only, `mem_size_mib` even, `HugePages_Free >=
  mem_size_mib/2`. If any check fails, the task **fails** with an error naming
  the request and the host's pool (`required N pages, free F of T`). Rationale:
  a successful start with an undersized pool corrupts the VM (SIGBUS), so a
  warning is not acceptable here. *Cost if wrong:* a VM that could have run on
  4K pages is refused until the operator allocates the pool — which is the
  intended, visible failure.
- **R2 — NUMA placement is real, not a rejection** (deviation from the program
  row "explicit rejection for NUMA"). The issue's scope asks for optional NUMA
  placement; `cpuset.mems` makes it implementable and verifiable, and rejecting
  a flag outright would leave the scope item unmet. Guest-visible NUMA topology
  remains unsupported and is documented as such. *Cost if wrong:* one extra flag
  surface to maintain; a user who expected a "not supported" error gets a
  working placement instead.
- **R3 — offline NUMA node is a hard error**, exactly like an offline CPU in
  `--cpuset`: the kernel would otherwise silently fall back to another node
  (memory is allocated from whatever is allowed), which is the silent ignore the
  project forbids.
- **R4 — 2M only.** `--hugepages` is a boolean; Firecracker supports no other
  page size. A `huge_pages` value other than `2M` never reaches the API.
- **R5 — no new config knob for the pool.** The host's pool is read from
  `/proc/meminfo`; operators size it with `vm.nr_hugepages`. *Cost:* no
  per-daemon override, which no scenario needs.
- **R6 — snapshot of a hugepages VM is documented, not blocked.** Creating a
  snapshot works; restoring it through the file backend cannot (Firecracker's
  limitation, UFFD only). The snapshot metadata does not carry the hugepage flag,
  so a guard here would need a metadata change; the limitation is documented and
  the failure is loud (Firecracker rejects the load). *Cost:* an operator can
  create a snapshot they cannot restore until slice 12.
- **R7 — balloon + hugepages interaction is documented.** The balloon can still
  inflate but cannot reclaim hugepage backing at 4K granularity.
- **R8 — both daemon start paths and `vm create`.** The hugepages check runs
  once in `Start` before either path; `vm create` checks before starting the VMM.
  A cgroup is created when **either** `cpuset` or `numa-node` is requested, so
  `cpuset.mems` can be set; `vm create` without either is unchanged.

## Ordering argument (why `cpuset.mems` is effective)

Slice 5 creates the cgroup **before** the VMM exists and attaches the process
right after `cmd.Start`, before `configureVM` pushes `machine-config`. Guest
memory is allocated by Firecracker when it applies the machine config, i.e.
after the attach, so the `cpuset.mems` restriction is in force for the guest
memory allocation. The lab e2e verifies `Mems_allowed_list` and `cpuset.mems`
rather than assuming it.

## Tasks

### T1 — primitives (types + host check), pure and unit-tested

- `pkg/types`: `AnnotationHugepages`/`HugepagesLabel` and
  `AnnotationNumaNode`/`NumaLabel`; `ParseHugepages(raw) (bool, error)` (accepts
  `""`/`none`/`false` and `true`/`2m`, case-insensitive; rejects anything else,
  naming the allowed values); `CgroupLimits.NumaNodes` (raw trimmed string,
  never parsed in `types`); `ResolveCgroupLimits` reads both annotations.
- `pkg/jailer/hugepages.go`: `HugepagesPool{PageSizeKB, Total, Free}`,
  `ReadHugepagesPool(path)`, `RequiredHugepages(sizeMiB)`, and
  `CheckHugepages(pool, sizeMiB) error` returning a typed `*HugepagesError`
  (request, required pages, pool) with an actionable hint.
- Table-driven tests with fixture `/proc/meminfo` content; no root, no host
  dependency.

### T2 — wiring (translator, daemon, lifecycle, cgroup policy)

- `pkg/translator`: `MachineConfig.HugePages string` (`json:"huge_pages,omitempty"`),
  set to `"2M"` when the task requests it; an unconfigured VM's config stays
  byte-identical (omitempty). Reject an odd `mem_size_mib` when hugepages is on.
- `pkg/swarmkit`: `MachineConfig.HugePages` field for the API/tests; the
  hugepages check in `Start` **before** either start path; error wrapping that
  names the request and the pool.
- `pkg/lifecycle`: `vm create` sets the annotation and checks the pool before
  starting; its machine-config map carries `huge_pages`.
- `pkg/jailer/cgroup_policy.go`: parse `CgroupLimits.NumaNodes` with
  `ParseCPUSet` and set `limits.CpusetMems` (explicit request wins over the
  auto-resolved parent/nodes), with a typed error naming the label; creation gate
  becomes `cpuset || numa` in both packages.
- Tests: translator mapping (on/off, odd size), policy (nodes → mems, invalid
  list), daemon/lifecycle (pool short → start fails, no process left, no cgroup
  left; no request → no behaviour change).

### T3 — CLI

- `--hugepages` and `--numa-node` on `service create`, `service update`,
  `vm create`, with exact help text.
- Pre-RPC validation: `--numa-node` uses the existing cpulist canonicaliser;
  `--hugepages` is a boolean; both rejected with `--rollback`.
- `service update` tri-state: absent keeps, `--hugepages=false` /
  `--numa-node ""` clears.
- Tests mirroring `cmd_cpuset_test.go`, including the command-level valid path.

### T4 — docs

- `reference/cli.md` (three flag tables + the syntax/canonicalisation/clearing
  paragraph), `guides/advanced.md` (hugepages and NUMA sections next to the
  pinning material, with on-host verification commands), `guides/configuration.md`
  (host pool prerequisites + error shape), `guides/security.md` (the flags
  alongside the existing cgroup material).
- State the limitations: 2M only; snapshot restore via file backend unsupported;
  balloon cannot reclaim hugepage backing; the guest sees a single NUMA node.

## Verification

- Unit tests per task; `gofmt`/`go vet`; **golangci-lint must say `0 issues`**
  (gocyclo threshold 20; `pkg/jailer` and `pkg/types` are not excluded — keep new
  functions small).
- `sc-cmp.sh 3f33ec9` head-only empty for the touched packages.
- Lab e2e on the 2-node nested-KVM cluster:
  1. allocate a pool (`vm.nr_hugepages`), `--hugepages` VM runs and its process
     reports hugetlb pages (`/proc/<pid>/status` `HugetlbPages`, or `smaps`);
  2. a request larger than the free pool **fails** the task naming required vs
     free, with no VM and no cgroup left;
  3. `--numa-node 0` → `cpuset.mems=0` and `Mems_allowed_list: 0`; an offline
     node fails the task naming the request and the host's online nodes;
  4. teardown removes the cgroup; unset flags behave exactly as before.
- `verify.sh feat/hugepages-numa main` (head-only empty, lint 0), CI green, then
  squash-merge (closes #47).

## Risks

- The hugepage pool is host-global and shared; the check is a pre-flight
  snapshot, so concurrent starts can still race. Accepted (the error is loud and
  the pool is operator-controlled).
- `pkg/translator` must not gain a dependency on `pkg/jailer`; host checks stay
  in the VMM paths.
- The lifecycle path has no `EnableCgroups` gate (parked in slice 5); NUMA
  requests on `vm create` create a cgroup regardless, which is the same
  behaviour `--cpuset` already has.

## Out of scope

Guest NUMA topology / SRAT, transparent hugepages, 1G pages, hugepage-aware
snapshot restore (UFFD, slice 12), NUMA-aware scheduling (slice 9), and any
scheduler admission for pool or node availability.
