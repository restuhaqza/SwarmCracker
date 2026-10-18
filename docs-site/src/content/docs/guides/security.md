---
title: Security & Jailer
description: Harden Firecracker microVMs with the jailer — chroot, privilege dropping, cgroup limits, and syscall filtering.
---

SwarmCracker runs every workload as a Firecracker microVM: KVM hardware
virtualisation gives the guest its own kernel, so a container escape is a
hypervisor escape, not a shared-kernel privilege escalation. On the host side,
the optional **Firecracker jailer** adds a second ring of defence around the
VMM process itself.

Per-task **cgroup v2** limits (CPU, memory, I/O, an optional CPU pin and an
optional NUMA memory binding) are applied by the daemon **independently of the
jailer**: enabling the jailer is not required for them, and this guide verifies
both. Guest memory can also be backed by the host's 2M hugepage pool; that is a
per-workload request enforced by the node at start, not a jailer setting.

This guide covers the host-side isolation model, how to enable the jailer, and a
hardening checklist.

## Isolation model

```
┌─ Host ──────────────────────────────────────────────────────────────┐
│  swarmd-firecracker (root, orchestrates VM lifecycle)               │
│                                                                     │
│  ┌─ Jailer sandbox (unprivileged uid 1000) ──────────────────────┐  │
│  │  chroot: /var/lib/swarmcracker/jailer/<task-id>/              │  │
│  │  ├─ root/            VM root filesystem                       │  │
│  │  ├─ run/             Firecracker API socket                   │  │
│  │  └─ log/             logging FIFO                             │  │
│  │                                                               │  │
│  │  ┌─ Firecracker process ──────────────────────────────────┐   │  │
│  │  │  • pid + net namespace isolated                        │   │  │
│  │  │  • cgroup cpu/memory limits                            │   │  │
│  │  │  • Firecracker seccomp-bpf policy                      │   │  │
│  │  └────────────────────────────────────────────────────────┘   │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                │                                    │
│                            KVM │ /dev/kvm                            │
│                                ▼                                    │
│  ┌─ Guest microVM (own kernel, hardware-isolated) ───────────────┐  │
│  │  workload container(s)                                        │  │
│  └───────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

The jailer provides:

- **Filesystem isolation** — Firecracker is chrooted; it cannot see the host filesystem.
- **Privilege dropping** — the VMM runs as an unprivileged user, not root.
- **Cgroup placement** — the jailer puts the VMM in its own cgroup under `--parent-cgroup`. SwarmCracker's per-task cgroup v2 limits (CPU, memory, I/O, `--cpuset` and `--numa-node`) are applied by the daemon on both the jailed and direct paths, so they do not depend on this flag.
- **Namespace isolation** — dedicated PID and network namespaces and TAP device.
- **Syscall filtering** — Firecracker applies its own seccomp-bpf policy.

For production paths and privilege model of the daemon itself, see
[Contributing → Security](/contributing/security/).

## Enabling the jailer

Jailer settings live under `executor.jailer` in `/etc/swarmcracker/config.yaml`:

```yaml
executor:
  enable_jailer: true
  jailer:
    uid: 1000                       # unprivileged uid to run Firecracker as
    gid: 1000                       # matching gid
    chroot_base_dir: "/srv/jailer"  # base directory for per-VM chroots
    netns: ""                       # optional network namespace
```

| Option | Default | Description |
|--------|---------|-------------|
| `executor.enable_jailer` | `false` | Master switch for the jailer sandbox |
| `executor.jailer.uid` | `1000` | UID the jailed Firecracker process runs as |
| `executor.jailer.gid` | `1000` | GID the jailed Firecracker process runs as |
| `executor.jailer.chroot_base_dir` | `/srv/jailer` | Base directory for per-VM chroots |
| `executor.jailer.netns` | `""` | Optional network namespace name |

:::note
`jailer.uid`, `jailer.gid`, and `jailer.chroot_base_dir` are **required** when
`enable_jailer` is true — config validation rejects an incomplete jailer block.
:::

### Create the Firecracker user

```bash
sudo groupadd -r -g 1000 firecracker
sudo useradd  -r -u 1000 -g 1000 -s /usr/sbin/nologin firecracker
sudo usermod -aG kvm firecracker
```

### Install the jailer binary

The jailer ships with Firecracker. If you installed Firecracker via
`swarmcracker setup install`, the jailer is already on the host; otherwise:

```bash
curl -fsSL https://github.com/firecracker-microvm/firecracker/releases/download/v1.15.1/firecracker-v1.15.1-x86_64.tgz | tar xz
sudo cp release-v1.15.1-x86_64/jailer /usr/local/bin/
sudo chmod +x /usr/local/bin/jailer
```

### Wire the jailer into the daemon

`swarmcracker cluster init` / `cluster join` generate the
`swarmcracker-manager.service` / `swarmcracker-worker.service` units and pass VM
settings to `swarmd-firecracker` as CLI flags. **The jailer is not yet wired into
those generated flags**, so enable it by adding the flags below to the unit's
`ExecStart` (or run the daemon directly).

```bash
sudo swarmd-firecracker \
  --join-addr 192.168.1.10:4242 \
  --join-token SWMTKN-1-... \
  --enable-jailer \
  --jailer-path /usr/local/bin/jailer \
  --jailer-uid 1000 \
  --jailer-gid 1000 \
  --jailer-chroot-dir /var/lib/swarmcracker/jailer \
  --parent-cgroup firecracker \
  --cgroup-version v2 \
  --enable-cgroups
```

| Flag | Default | Purpose |
|------|---------|---------|
| `--enable-jailer` | `false` | Enable the jailer |
| `--jailer-path` | `/usr/local/bin/jailer` | Jailer binary path |
| `--jailer-uid` / `--jailer-gid` | `1000` | UID/GID for jailed processes |
| `--jailer-chroot-dir` | `/var/lib/swarmcracker/jailer` | Chroot base directory |
| `--parent-cgroup` | `firecracker` | Cgroup the jailer places the VMM in (jailer only — it does not move SwarmCracker's per-task cgroup) |
| `--cgroup-version` | auto-detect | `v1` or `v2` passed to the jailer; SwarmCracker's per-task cgroup is always v2 |
| `--enable-cgroups` | `true` | Apply per-task CPU/memory/I/O limits; independent of `--enable-jailer` |

There are no cluster-wide placement settings: CPU pinning, 2M hugepage backing and
NUMA memory binding are requested **per service or VM** with `--cpuset`,
`--hugepages` and `--numa-node` (`service create`, `service update`,
`vm create`). Each is a hard promise — a task that cannot be pinned, cannot get
its hugepage pool, or cannot be bound to the listed nodes fails to start rather
than running with a silently weaker placement. See
[CPU Pinning and cgroup Limits](/guides/advanced/#cpu-pinning-and-cgroup-limits),
[Hugepages](/guides/advanced/#hugepages) and
[NUMA placement](/guides/advanced/#numa-placement) for the enforced files and the
failure policy.

To persist the change:

```bash
sudo systemctl daemon-reload
sudo systemctl restart swarmcracker-worker
```

:::caution
The `swarmcracker` CLI reads `/etc/swarmcracker/config.yaml`. The **daemon**
(`swarmd-firecracker`) is configured entirely by flags and does not accept a
`--config` file. Keep the two in sync.
:::

## Verify isolation

**Process ownership** — Firecracker should run as the unprivileged user, not root:

```bash
ps -o user,pid,cmd -C firecracker
# firecr+  12345  /usr/local/bin/jailer --id vm-xxx ...
```

**Chroot layout** — one directory per task:

```bash
ls -la /var/lib/swarmcracker/jailer/<task-id>/
# root/   VM root filesystem
# run/    Firecracker API socket
# log/    logging FIFO
```

**Cgroup limits and placement** — one cgroup per task, whether or not the jailer
is enabled:

```bash
TASK=<task-id>
BASE=/sys/fs/cgroup/swarmcracker/$TASK

cat "$BASE/cpuset.cpus"          # requested pin (empty = not pinned)
cat "$BASE/cpuset.mems"          # NUMA memory binding (node mask, e.g. 0-1)
cat "$BASE/cgroup.procs"         # the VMM process's PID
cat "$BASE/cpu.max"              # "<quota> 1000000" microseconds
cat "$BASE/memory.max"           # guest memory + 128 MiB, in bytes
cat "$BASE/memory.high"          # 90% of memory.max
cat "$BASE/memory.current"       # live usage
cat "$BASE/io.weight"            # "default 100"

PID=$(cat "$BASE/cgroup.procs")
taskset -pc "$PID"               # affinity actually in effect
grep Cpus_allowed_list "/proc/$PID/status"
grep Mems_allowed_list "/proc/$PID/status"   # must equal cpuset.mems
grep HugetlbPages "/proc/$PID/status"        # non-zero when 2M pages back the guest
```

`taskset -pc`, `Cpus_allowed_list` and `Mems_allowed_list` must agree with
`cpuset.cpus` and `cpuset.mems`. The path is
`/sys/fs/cgroup/swarmcracker/<task-id>/` even with `--parent-cgroup` set — that
flag configures the jailer's own cgroup. A `vm create` VM has a cgroup only when
`--cpuset` or `--numa-node` was given. Use the **full** task ID
(`service ps -q`), not the 12-character table form.

**Syscall filtering** — Firecracker's own seccomp policy:

```bash
PID=$(pgrep -f 'firecracker.*--id')
grep Seccomp /proc/$PID/status
# Seccomp: 2   (filter mode active)
```

## Hardening checklist

| Item | Check |
|------|-------|
| KVM access limited to the daemon + firecracker user | `ls -la /dev/kvm` |
| Dedicated unprivileged user exists | `id firecracker` |
| Jailer enabled and validated | `swarmcracker config file validate` |
| cgroup limits enforced per VM | `cat /sys/fs/cgroup/swarmcracker/<task-id>/memory.max` |
| Requested CPU pin in effect | `cpuset.cpus` matches `taskset -pc $(cat cgroup.procs)` |
| Requested NUMA memory binding in effect | `cpuset.mems` matches `Mems_allowed_list` in `/proc/<pid>/status` |
| Hugepage-backed guest memory | `HugetlbPages` in `/proc/<pid>/status` is non-zero and `HugePages_Free` fell |
| Chroot base directory locked down | `ls -la /srv/jailer` |
| One network namespace + TAP per VM | `ip netns list` |
| Workloads never run as root inside the guest | image `USER` directive |
| Resource requests set on services | `--memory`, `--cpu` on `service create` |

## Troubleshooting

### `failed to start jailer: permission denied`

The firecracker user must own the chroot base and have KVM access:

```bash
sudo chown -R firecracker:firecracker /var/lib/swarmcracker/jailer
sudo usermod -aG kvm firecracker
```

### `cannot apply cpuset "0-3" to task …`

The node could not pin the VM, so it did not start. The message names the
controllers it tried to enable, the parent that refused, and the host's online
CPUs. Check both sides:

```bash
cat /sys/devices/system/cpu/online                  # what the host actually has
cat /sys/fs/cgroup/cgroup.controllers               # what the kernel advertises
cat /sys/fs/cgroup/cgroup.subtree_control            # what is delegated
cat /sys/fs/cgroup/swarmcracker/cgroup.subtree_control
```

If `cpuset` is missing from `cgroup.controllers`, the host kernel cannot provide
it. If the cpuset is online but the write failed, the daemon cannot delegate
through its parent cgroup: on a systemd unit whose `/sys/fs/cgroup` is a
delegated subtree, set `Delegate=yes` (see
[Configuration → cgroup v2 host requirements](/guides/configuration/#cgroup-v2-host-requirements)).
A request for CPUs the host does not have is a scheduling problem, not a cgroup
one — constrain the service to suitable nodes, or change the pin.

### `Failed to enable cgroup controllers, continuing…`

This is the quota path, not the pin: the task requested no CPU or memory
placement, or only the `cpu`/`memory`/`io` controllers it does not strictly need
were unavailable, so the VM starts without them. If the warning appears for
`cpu`, `memory` or `io` on a task that *did* request a cpuset or a NUMA binding,
the placement still takes effect — check `cpuset.cpus`, `cpuset.mems` and
`taskset -pc` as above. See
[CPU Pinning and cgroup Limits](/guides/advanced/#cpu-pinning-and-cgroup-limits).

### Firecracker exits immediately with `SIGSYS`

The seccomp policy blocked a syscall. Firecracker fails closed rather than run
with a weakened policy — check the kernel log for the blocked call and update
the policy rather than disabling filtering:

```bash
sudo dmesg | grep -i seccomp
```

### `socket not created: context deadline exceeded`

Check the worker logs and the common causes:

```bash
sudo journalctl -u swarmcracker-worker -f
```

- Firecracker or jailer binary not found at the configured path
- kernel/rootfs paths incorrect or unreadable
- chroot permissions wrong

## Disabling the jailer

For local debugging only:

```yaml
executor:
  enable_jailer: false
```

:::danger
Without the jailer the VMM runs with host privileges and no chroot. Never use
this in a multi-tenant or internet-facing deployment.
:::

Per-task cgroup limits (and a requested `--cpuset`) are applied on the direct
path too — disabling the jailer does not disable them.

## References

- [Firecracker jailer documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md)
- [Cgroup v2](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html)
- [seccomp(2)](https://man7.org/linux/man-pages/man2/seccomp.2.html)
- [Configuration guide](/guides/configuration/) — full `executor.jailer` schema
- [Advanced guide](/guides/advanced/) — privileged builds and custom kernels
