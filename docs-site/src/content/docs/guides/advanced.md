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

`cpuset.mems` is written next to a requested `cpuset.cpus`, but it is **not**
part of the request and there is no flag for it: it is a **NUMA-node** list
(a node mask such as `0-1`, not CPU numbers), and the daemon resolves it
automatically — the nearest readable ancestor `cpuset.mems` (`swarmcracker/`,
then the cgroup root), else the host's online nodes from
`/sys/devices/system/node/online`. A pin therefore never restricts memory to a
subset of the nodes the host already allows.

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
or the 1 vCPU / 512 MiB defaults): the CPU quota is one microsecond-second per
vCPU, and the memory cap is the guest's memory plus the headroom below.

**A malformed request fails the task; a host-side quota failure does not.** The
warnings above describe enforcement failing on the host after a valid request.
A request that cannot be honoured at all — a CPU limit below 1000us (for example
a sub-1-nanocore value), or a cpuset that does not parse — is rejected while the
limits are derived, **before any cgroup file is written**, and fails the task
even when it requested no cpuset.

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
`--cpuset` is given**; without it the command never touches `/sys/fs/cgroup`.
The cgroup is removed on stop, on remove, and when a start fails.

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