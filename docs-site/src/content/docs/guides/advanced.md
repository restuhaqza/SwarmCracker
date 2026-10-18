---
title: "Advanced Topics"
description: "Rolling updates, memory balloon, multi-arch, init systems, build from source."
---

> Rolling updates, memory balloon, multi-arch, init systems, build from source.

---

## Rolling Updates

SwarmCracker supports zero-downtime rolling updates via SwarmKit orchestration.

### How It Works

1. Manager creates new task with updated spec
2. SwarmCracker starts new Firecracker VM
3. VM reports RUNNING status
4. Manager waits for Monitor period (default: 5s)
5. Manager stops old task
6. Executor removes old VM

### Update a Service

```bash
# Update image (triggers rolling update)
swarmctl update svc-nginx --image nginx:1.25

# Update with environment
swarmctl update svc-app --env LOG_LEVEL=debug
```

### Configuration

SwarmKit controls update behavior (not SwarmCracker):

| Parameter | Default | Description |
|-----------|---------|-------------|
| Parallelism | 1 | Tasks updated simultaneously |
| Delay | 0s | Wait between batches |
| Monitor | 5s | Verify task stability |
| Failure Action | pause | On failure: pause/continue/rollback |

---

## Memory Balloon

A service and a `vm create` VM can declare a **virtio-balloon** target: how much
guest memory the node reclaims from the VM. The balloon inflates to that target
from boot and holds it there regardless of host pressure; the **guest** can take
the memory back when *it* comes under pressure. A balloon never lets a VM grow
beyond its memory limit.

```bash
# A 1 GiB VM that can give back up to 256 MiB
swarmcracker service create --name cache --image redis:7 \
    --memory 1G --balloon-mib 256

# Direct VMs take the same flag
swarmcracker vm create --memory 1024 --balloon-mib 256 alpine:latest

# Change the target on a running service (rolls the task), or remove it
swarmcracker service update cache --balloon-mib 384
swarmcracker service update cache --balloon-mib 0
```

### Units and validation

`--balloon-mib` is an integer count of **MiB**, not a size string (`256`, not
`256m`). It must be a **positive integer smaller than the memory the VM will boot
with**: a target at or above the memory can never be honoured, so the request is
rejected with an error naming both numbers — before the service is created or the
VM is prepared. That memory is the size the VM will actually boot with — the
declared `--memory`, or the **512 MiB default** when nothing is declared — so
`--memory 256m --balloon-mib 300` is rejected rather than accepted against a
floor the VM would never have.

On `service create` and `vm create` the flag is optional; an omitted flag means
no balloon device. Because the flag exists only to configure one, an explicit
`--balloon-mib 0` is rejected as invalid rather than read as "no balloon". On
`service update` the flag is **tri-state**: omitting it keeps the service's
current target, a value replaces it, and `--balloon-mib 0` removes it. An update
validates a new target against the memory the task will get — the
`--memory-limit` given on the same command, else the limit already on the
service.

### The device, and when it takes effect

The balloon is created **before the VM boots** (`PUT /balloon` runs before
`InstanceStart`), and Firecracker rejects the request once a VM is running. It
therefore **cannot be added to a running VM**: enable it when the VM is created,
or let `service update` roll the task with the new target.

The device is configured with **`deflate_on_oom: true`**, so when the guest comes
under memory pressure it can take the ballooned pages back instead of being
OOM-killed — reclamation is cooperative, not a hard cap. Statistics polling is
left off.

The balloon only takes effect if the **guest kernel has the virtio-balloon
driver**. If it does not, the device still exists on the host side, but the
guest's memory use is unchanged — the guest has no way to hand pages back. Check
the image's kernel before relying on the flag.

A service that does not declare a balloon gets **no balloon key at all**, so its
VM config and the requests sent to Firecracker are byte-identical to a build
without the feature.

### How the target reaches the node

For a service the target travels as the **`swarmcracker.balloon` service label**,
which SwarmKit copies onto each task and the node reads when it translates the
task into a VM config (a `vm create` VM carries it as a task annotation instead).
You can set that label directly with `--label swarmcracker.balloon=<n>`, but
`--balloon-mib` and a `swarmcracker.balloon` label that **disagree** are refused
rather than resolved silently; a label whose value agrees is accepted (compared
as the literal string, so `--label swarmcracker.balloon=0256` is refused
alongside `--balloon-mib 256` even though the numbers agree). A hand-set label
bypasses the CLI's check and is validated by the node at translation time, where
an invalid value fails the task loudly.

---

## CPU Pinning and cgroup Limits

`--cpuset` pins a VM's VMM process to a set of host CPUs. Independently, each
service task gets a **cgroup v2** cgroup that caps the VM's CPU, memory and I/O.
The pin is a hard promise; the quotas are defence in depth around the VM size
the hypervisor already enforces.

```bash
# Pin a service to CPUs 0-3 and give it two vCPUs
swarmcracker service create --name api --image myapp:1.4 \
    --cpu 2 --cpuset 0-3

# Re-pin it (effective on the next task SwarmKit starts), or remove the pin
swarmcracker service update api --cpuset 4-7
swarmcracker service update api --cpuset ""

# A direct VM is pinned on the host that runs the command
swarmcracker vm create --cpu 2 --memory 1024 --cpuset 0-3 alpine:latest
```

The value is a Linux **cpulist** — `0`, `0-3`, `0-3,8` — and it is canonicalised
before it is stored (`3,1-2` becomes `1-3`). See the
[CLI reference](/reference/cli/#swarmcracker-service) for the flag semantics:
syntax, `--cpuset ""` clearing (on `service update` only), and why
`--cpuset` cannot be combined with `--rollback`.

### What is enforced

The cgroup lives at `/sys/fs/cgroup/swarmcracker/<task-id>/` and holds the VMM
process — Firecracker itself, or the jailer process that launches it when the
jailer is enabled.

| Enforced value | File | Limit | If it cannot be applied |
|----------------|------|-------|-------------------------|
| CPU set | `cpuset.cpus` | Exactly the requested CPUs | **Fatal** — the VM does not start |
| CPU quota | `cpu.max` | `<quota> 1000000`, from `--cpu`/`--cpu-limit` (1 vCPU = `1000000 1000000`) | Warning; the VM runs |
| Memory hard limit | `memory.max` | Guest memory **+ 128 MiB** | Warning; the VM runs |
| Memory throttle | `memory.high` | 90% of `memory.max` | Warning; the VM runs |
| I/O weight | `io.weight` | `default 100` | Warning; the VM runs |

`cpuset.mems` is written next to a requested `cpuset.cpus` as well. It is a
**NUMA-node** list (a node mask such as `0-1`, not CPU numbers). Without an
explicit request the daemon resolves it automatically — the nearest readable
ancestor `cpuset.mems` (`swarmcracker/`, then the cgroup root), else the host's
online nodes from `/sys/devices/system/node/online` — so a CPU pin never
restricts memory to a subset of the nodes the host already allows.
**`--numa-node` overrides that resolution** with an explicit list, which is what
binds the guest memory to the named nodes; see
[NUMA placement](#numa-placement) below.

**The cpuset is a hard promise.** If the requested CPUs are not online on the
node — or the `cpuset` controller cannot be delegated there — the VM is **not
started**. The error names the request and the host's online CPUs
(`/sys/devices/system/cpu/online`), for example:

```text
cannot apply cpuset "0-3" to task <task-id>: ... (host online CPUs: 0-1,8-15)
```

There is **no scheduler admission** for CPU availability: the CLI checks the
syntax, and a task can be placed on a node that cannot honour the pin. Use a
placement constraint (for example `node.labels.cpus==0-3`) to keep pinned
services off unsuitable nodes.

**The quotas are defence in depth.** A CPU, memory or I/O quota that cannot be
written is logged as a warning and the VM starts anyway — the hypervisor already
bounds the guest to its vCPU count and memory size. The cgroup is kept so a
requested pin still takes effect. Quotas come from the task's `Resources.Limits`,
else `Resources.Reservations`, else the VM's booted size (`--cpu` / `--memory`,
or the 1 vCPU / 512 MiB defaults): the CPU quota is 1,000,000 µs of CPU time per 1,000,000 µs period
per vCPU (one full core-second per second), and the memory cap is the guest's
memory plus the headroom below.

**A malformed request fails the task; a host-side quota failure does not.** The
warnings above describe enforcement failing on the host after a valid request.
A request that cannot be honoured at all — a CPU limit below 0.001 core (fewer
than 1,000,000 NanoCPUs, i.e. a quota under 1,000 µs), or a cpuset that does not
parse — is rejected while the
limits are derived, **before any cgroup file is written**, and fails the task
even when it requested no CPU or memory placement.

**Why `memory.max` is larger than the VM's memory.** The cgroup accounts for the
whole VMM process, whose resident memory is the guest's RAM *plus* Firecracker's
own allocations. A limit equal to the guest size would OOM-kill a healthy VM, so
the daemon adds **128 MiB** of headroom: a 512 MiB VM gets `memory.max` of
640 MiB, and `memory.high` (the throttle threshold) of 576 MiB. The guest is
still bounded at 512 MiB by the hypervisor; the extra 128 MiB is for the VMM, not
for the workload.

**Which VMs get a cgroup.** Service tasks get one whenever the daemon's
`--enable-cgroups` is on — **the default** — and this **no longer requires
`--enable-jailer`** (also off by default), so cgroups apply to both the jailed
and the direct start paths. A `vm create` VM gets a cgroup **only when
`--cpuset` or `--numa-node` is given**; without either the command never touches
`/sys/fs/cgroup`. The cgroup is removed on stop, on remove, and when a start
fails.

### Verify it on the host

Run these on the node that owns the task. The `service ps` **table truncates the
ID to 12 characters**, while the cgroup directory is named with the **full**
task ID — so take the ID from `-q`, which prints it untruncated:

```bash
# Full task ID of the service's first replica (substitute your service name;
# with several replicas, pick the task running on this node).
TASK=$(swarmcracker service ps -q api | head -1)
BASE=/sys/fs/cgroup/swarmcracker/$TASK

ls "$BASE"                       # the directory must exist, or TASK is wrong

cat "$BASE/cpuset.cpus"          # the pin, canonicalised (e.g. 0-3)
cat "$BASE/cgroup.procs"         # the VMM process's PID
cat "$BASE/cpu.max"              # "<quota> 1000000", e.g. "2000000 1000000"
cat "$BASE/memory.max"           # guest memory + 128 MiB, in bytes
cat "$BASE/memory.high"          # 90% of memory.max
cat "$BASE/io.weight"            # "default 100"

PID=$(cat "$BASE/cgroup.procs")
taskset -pc "$PID"               # the process's current CPU affinity
grep Cpus_allowed_list "/proc/$PID/status"
```

`taskset -pc <pid>` and `Cpus_allowed_list` must agree with `cpuset.cpus`; that
is the pin actually in effect, not just a stored request. An unpinned service VM
leaves `cpuset.cpus` empty, which means "inherit the parent's set", and a
`vm create` VM without `--cpuset` has no directory under
`/sys/fs/cgroup/swarmcracker/` at all. For a direct VM, use the ID `vm create`
prints on success (or `swarmcracker task ls -q`); it is already the full one.

There are **no cgroup metrics**. The daemon exports no CPU/memory/IO series from
these files, so the files above are the observable surface; see
[Metrics](/guides/operations/#metrics) for the series that do exist.

### Host requirements

cgroup v2 must be mounted at `/sys/fs/cgroup` and the daemon must be able to
delegate `cpu`, `memory`, `io` and `cpuset` into the per-task cgroups. On the
shipped install the daemon runs as **root** with `/sys/fs/cgroup` as the cgroup
root, and writes `cgroup.subtree_control` there directly — **no systemd drop-in
is needed**. Delegation only has to be arranged when the daemon's
`/sys/fs/cgroup` is a **delegated subtree** (a container, or a unit started in a
cgroup namespace); for such a systemd unit that is `Delegate=yes`. Either way, a
v1-only host or a refused delegation fails the pin with an error naming the
controllers that could not be enabled. See
[Configuration → cgroups](/guides/configuration/#cgroup-v2-host-requirements) for
the requirement, the exact error, and what happens without a pin.

---

## Hugepages and NUMA Placement

Two opt-in flags configure how a VM's memory is backed and placed on the host.
Both are per-workload — there is no daemon-wide switch and no `config.yaml` key —
and both are validated by the CLI for syntax only, then enforced by the node when
it starts the VM. A request the host cannot satisfy **fails the task**; it is
never silently downgraded.

```bash
# Service: 2 GiB guest memory backed by 2M pages, on NUMA node 0, CPUs 0-3
swarmcracker service create --name api --image myapp:1.4 \
    --memory 2G --hugepages --cpuset 0-3 --numa-node 0

# Change or clear either request (effective on the next task)
swarmcracker service update api --hugepages=false
swarmcracker service update api --numa-node ""

# Direct VM: the same flags, applied on the host running the command
swarmcracker vm create --memory 2048 --hugepages --numa-node 0 alpine:latest
```

### Hugepages

`--hugepages` asks Firecracker to back the guest memory with **2M huge pages**.
Firecracker supports no other hugepage size, so the flag is a boolean request,
not a size: it is stored as `swarmcracker.hugepages=true` and an omitted flag
(or `--hugepages=false` on update) means ordinary 4K memory.

#### Host prerequisites

The pages come from the host's **pre-allocated hugetlbfs pool**, which is
host-global and shared by every hugepage-backed VM on that node. On each node
that may run the workload:

```bash
grep -i huge /proc/meminfo
# HugePages_Total:    1024        <- pages currently allocated
# HugePages_Free:     1024        <- allocated pages not yet in use
# Hugepagesize:       2048 kB     <- must be 2M
```

- **The pool must already exist.** Firecracker maps guest memory with
  `MAP_NORESERVE` from that pool; it does not allocate huge pages on demand.
  Size the pool with `sysctl vm.nr_hugepages=<pages>` (persist it in
  `/etc/sysctl.d/`). The value is the pool **total**, not an increment: a 2 GiB
  VM needs `1024` pages, two concurrently running 2 GiB VMs need `2048`, and
  pages already used by something else count too.
- **The page size must be 2M.** A host configured with 1G pages fails the
  request; 1G pages and transparent hugepages are not supported.
- **Memory must be an even number of MiB.** 2M pages cannot cover an odd tail,
  so `--memory 513` is rejected while the task is translated rather than booted
  at 4K.

#### What the node checks

Before the VMM process exists — and before any cgroup is created — the node
reads `/proc/meminfo` and requires all of:

1. `Hugepagesize` is `2048 kB`.
2. The guest memory is a positive, even number of MiB.
3. `HugePages_Free` is at least `mem_size_mib / 2`.

The check uses the same `mem_size_mib` the VM boots with, so a refusal cannot
disagree with the guest size. A task that did not request hugepages never reads
the pool and behaves exactly as before.

#### Failure shape

A short pool is a **hard failure**: the task does not start, and it leaves no VM
and no cgroup behind. The error names the request, the pages needed, the host
pool, and the `vm.nr_hugepages` total that would leave enough free pages —
`(Total - Free) + required`:

```text
task <task-id>: cannot back a 512 MiB VM with 2M huge pages (256 pages needed): the host pool has 100 free of 512 allocated: raise the pool total to 668 pages with sysctl vm.nr_hugepages=668
```

A wrong page size is reported first, with the same request/pool wrapper:

```text
task <task-id>: cannot back a 512 MiB VM with 2M huge pages (256 pages needed): the host pool has 0 free of 0 allocated: the host's hugepage size is 1048576 KiB, but Firecracker supports only 2M (2048 KiB) pages: reconfigure the host pool with 2M pages
```

An odd size fails earlier, in translation, naming the size:

```text
invalid hugepage-backed memory size 513 MiB: 2M huge pages require a multiple of 2 MiB
```

There is **no scheduler admission** for pool capacity, and the check is a
pre-flight snapshot of a host-global pool — two starts racing for the last pages
can both pass it, and the loser faults later. Constrain hugepage workloads to
nodes you have sized for them (for example a `node.labels.hugepages==yes` label
plus `--constraint node.labels.hugepages==yes`).

#### Verify it on the host

Use the **full** task ID: the `service ps` table truncates it to 12 characters,
while the cgroup directory uses the whole ID. `service ps -q` prints it
untruncated.

```bash
TASK=$(swarmcracker service ps -q api | head -1)   # substitute your service
BASE=/sys/fs/cgroup/swarmcracker/$TASK
PID=$(cat "$BASE/cgroup.procs")

# The VMM process's hugetlb-backed memory, in kB (non-zero when hugepages are in use)
grep -E 'HugetlbPages|VmRSS' "/proc/$PID/status"

# The host pool the VM drew from; Free falls as the guest faults its memory in
grep -i huge /proc/meminfo
```

`HugetlbPages` is the authoritative per-process counter (`smaps` shows the same
backing per mapping); it grows toward `mem_size_mib` as the guest touches its
memory. `HugePages_Free` falls as those pages are faulted, so it may not drop by
the full page count immediately after boot. For a direct VM, use the ID
`vm create` printed (or `swarmcracker task ls -q`); it is already the full one.

### NUMA Placement

`--numa-node <nodelist>` binds the VM's **guest memory** to the listed host NUMA
nodes by writing `cpuset.mems` on the task's cgroup. The kernel then allocates
the guest's pages only from those nodes, which is observable as
`Mems_allowed_list` in `/proc/<pid>/status`.

```bash
# Memory on node 0, CPUs pinned to 0-3
swarmcracker service create --name db --image postgres:16 \
    --memory 4G --cpuset 0-3 --numa-node 0

# A range is allowed; the value is canonicalised before storage (3,1 -> 1,3)
swarmcracker service update db --numa-node 0-1
```

- **Syntax** is a Linux cpulist of **node numbers**: `0`, `0-1`, `0-1,3`.
  Overlapping and adjacent ranges are merged, so `1,0` is stored and shown as
  `0-1`. A malformed list is rejected before any RPC.
- **It binds memory, not CPUs.** CPU affinity is `--cpuset`; pass both for a
  full node placement. `--numa-node` does not derive CPUs from the node and
  `--cpuset` does not derive memory nodes.
- **It does not create guest topology.** Firecracker exposes no SRAT or NUMA
  topology, so the guest still sees **one** NUMA node (`numactl --hardware`
  inside the guest shows a single node). The placement is host-side only.
- **A NUMA-only request creates a cgroup**, on both the daemon and `vm create`
  paths, because `cpuset.mems` needs one — even when `--cpuset` was not given.
- **It overrides the automatic value.** Without the flag, a cgroup's
  `cpuset.mems` is inherited from the nearest readable ancestor or the host's
  online nodes; an explicit request replaces that with exactly the named nodes.
- **No scheduler admission.** The node applies the list at VM start. An offline
  node (or a node the host does not have) fails the task rather than falling
  back to another node, which would be the silent ignore this project forbids.

The failure names the request, the task, and the failing cgroup stage:

```text
cannot apply NUMA node list "3" to task <task-id>: cgroup cpuset stage (cpuset.mems): failed to write "3" (source: CpusetMems; online memory nodes: 0-1): ...
```

A malformed annotation that bypassed the CLI (a hand-set
`swarmcracker.numa-node` label) fails as:

```text
invalid NUMA node annotation swarmcracker.numa-node="1-" on task <task-id>: ...
```

#### Verify it on the host

```bash
TASK=$(swarmcracker service ps -q db | head -1)    # full task ID, not the 12-char table form
BASE=/sys/fs/cgroup/swarmcracker/$TASK
PID=$(cat "$BASE/cgroup.procs")

cat "$BASE/cpuset.mems"                 # the binding, canonicalised (e.g. 0 or 0-1)
cat "$BASE/cpuset.cpus"                 # the CPU pin, if --cpuset was also given
grep Mems_allowed_list "/proc/$PID/status"   # must equal cpuset.mems
```

`cpuset.mems` is the stored request; `Mems_allowed_list` is what the process
actually got. They must agree — if they do not, the binding never took effect
and the VM was refused (a placement failure is fatal). For a `vm create` VM,
take the ID the command printed instead of `service ps`.

### Known limitations

- **2M pages only.** 1G pages, transparent hugepages, and mixed page sizes are
  not supported; guest memory must be an even number of MiB.
- **Snapshot restore of a hugepage-backed VM is UFFD-only.** `vm snapshot
  create` works, but Firecracker can restore hugepage-backed memory only through
  a userfaultfd backend. SwarmCracker's file-backed restore path
  (`PUT /snapshot/load` with `mem_file_path`) cannot restore such a snapshot —
  Firecracker rejects the load loudly. UFFD restore is not implemented yet; see
  [Snapshots → Limitations](/guides/snapshots/#limitations).
- **The balloon cannot reclaim hugepage backing.** A hugepage-backed VM may
  still configure `--balloon-mib`, and the balloon can inflate, but it frees
  guest pages at 4K granularity and those pages are 2M-backed. Do not rely on
  the balloon to return hugepage memory to the host pool.
- **The guest sees a single NUMA node.** Host-side `--numa-node` placement has
  no guest-visible topology.
- **No scheduler admission and no reservation.** Pool and node availability are
  checked on the node at start; a task can be placed on a node that cannot
  satisfy it and fail there. The hugepage pool is host-global, so concurrent
  starts can race.

---

## Multi-Architecture Support

SwarmCracker supports multiple CPU architectures. Pick the variant that
matches your nodes through the image you deploy.

### Supported Architectures

| Arch | Firecracker Support | Notes |
|------|---------------------|-------|
| x86_64 | ✅ Full support | Primary target |
| arm64 | ✅ Experimental | AWS Graviton, Ampere |

### Architecture Constraints

`swarmctl create-service` is a minimal debug client and parses only
`--network`, `--name`, and `--replicas` — it does **not** accept
`--constraint`. Use `swarmcracker service` for deployment: `--constraint` and
`--placement-pref` are enforced by the SwarmKit scheduler (`node.hostname`,
`node.role`, `node.labels.*`, …), and the architecture itself is selected
through the image you deploy.

A `node.labels.<key>` constraint is satisfiable only once a matching label
exists on a node, and labels are set through `swarmcracker node update`:

```bash
# Label a node, then place work on it
swarmcracker node update <node-id> --label-add disk=ssd
swarmcracker service create --name web --image nginx:alpine \
    --constraint node.labels.disk==ssd
```

`--label-add KEY=VALUE` and `--label-rm KEY` are both repeatable and at least one
is required; an existing key is overwritten, and removing a key the node does not
have is a no-op. See [`node update`](/reference/cli/#swarmcracker-node).

### Multi-Arch Images

Use OCI image indexes for cross-arch compatibility:

```bash
# Build multi-arch image
docker buildx build --platform linux/amd64,linux/arm64 -t myapp:latest .

# SwarmCracker pulls correct variant based on node arch
```

---

## Init Systems

MicroVMs need an init system for proper process management.

### Why Init Matters

- **Zombie reaping** — Orphan processes must be reaped
- **Signal handling** — Forward SIGTERM to children
- **Process supervision** — Restart failed processes

### Supported Init Systems

| Init | Size | Features |
|------|------|----------|
| **tini** | ~20KB | Minimal, Docker default |
| **dumb-init** | ~30KB | Signal proxy, lightweight |
| **s6** | ~100KB | Process supervision |
| **systemd** | Large | Full service management |

### Configure Init

```yaml
executor:
  init_system: tini        # none | tini | dumb-init
  init_grace_period: 10    # seconds before SIGKILL during shutdown
```

### Rootfs with Init

```bash
# Install tini in rootfs
curl -fsSL https://github.com/krallin/tini/releases/download/v0.19.0/tini-static -o rootfs/sbin/tini
chmod +x rootfs/sbin/tini
```

---

## Build from Source

### Prerequisites

- Go 1.26+
- Git
- Make

### Clone and Build

```bash
# Clone repository
git clone https://github.com/restuhaqza/SwarmCracker
cd SwarmCracker

# Build binaries
make all

# Output:
# build/swarmcracker
# build/swarmd-firecracker
# build/swarmcracker-agent

# Install
sudo make install
```

### Build Targets

```bash
make all           # Build all binaries
make swarmcracker  # Build the main CLI
make test          # Run unit tests
make lint          # Run linters
make fmt           # Format code
make clean         # Clean build artifacts
make install       # Install binaries to $GOPATH/bin
```

### Development Build

```bash
# Build a debug/dev binary
go build -o build/swarmcracker ./cmd/swarmcracker

# Run the CLI
./build/swarmcracker --help
```

---

## systemd Services

`swarmcracker cluster init` (manager) and `swarmcracker cluster join` (worker)
generate and enable the systemd units for you:

- `swarmcracker-manager.service` — runs `swarmd-firecracker --manager` on the manager
- `swarmcracker-worker.service` — runs `swarmd-firecracker` on workers

Inspect and control them with systemd:

```bash
sudo systemctl status swarmcracker-manager
sudo systemctl restart swarmcracker-worker

# Logs
sudo journalctl -u swarmcracker-manager -f
sudo journalctl -u swarmcracker-worker -f
```

There is no standalone `swarmcracker` service.

---

## File Management

Manage rootfs and kernel images.

### Rootfs Directory

```yaml
executor:
  rootfs_dir: "/var/lib/firecracker/rootfs"
```

### Kernel Management

```yaml
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
```

### Image Storage

```bash
/var/lib/firecracker/
├── rootfs/                 # executor.rootfs_dir
│   ├── nginx-rootfs.ext4
│   └── redis-rootfs.ext4
└── golden/                 # prebuilt golden image artifacts

/usr/share/firecracker/
└── vmlinux                 # executor.kernel_path (default kernel)
```

---

## Troubleshooting

### Rolling Update Stuck

```bash
# Check task status
swarmctl ls-tasks

# Check node availability
swarmctl ls-nodes

# Force rollback if needed
swarmctl update <service-id> --image nginx:1.25-alpine
```

### Init Process Missing

```bash
# Check init in rootfs
ls rootfs/sbin/tini

# Verify init binary
file rootfs/sbin/tini
```

### Build Fails

```bash
# Check Go version
go version  # Must be 1.26+

# Check dependencies
go mod download

# Run lint for errors
make lint
```

---

**See Also:** [Configuration](/guides/configuration/) | [Contributing Guide](/contributing/guidelines/)