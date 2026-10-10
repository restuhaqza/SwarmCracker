---
title: Security & Jailer
description: Harden Firecracker microVMs with the jailer — chroot, privilege dropping, cgroup limits, and syscall filtering.
---

SwarmCracker runs every workload as a Firecracker microVM: KVM hardware
virtualisation gives the guest its own kernel, so a container escape is a
hypervisor escape, not a shared-kernel privilege escalation. On the host side,
the optional **Firecracker jailer** adds a second ring of defence around the
VMM process itself.

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
- **Resource limits** — cgroup v1/v2 caps CPU, memory, and I/O per VM.
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
| `--parent-cgroup` | `firecracker` | Parent cgroup for VM limits |
| `--cgroup-version` | auto-detect | `v1` or `v2` |
| `--enable-cgroups` | `true` | Enforce cgroup resource limits |

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

**Cgroup limits** — under the parent cgroup:

```bash
cat /sys/fs/cgroup/firecracker/<task-id>/cpu.max        # quota period
cat /sys/fs/cgroup/firecracker/<task-id>/memory.max     # limit in bytes
cat /sys/fs/cgroup/firecracker/<task-id>/memory.current # live usage
```

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
| cgroup limits enforced per VM | `cat /sys/fs/cgroup/firecracker/<task-id>/memory.max` |
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

### `failed to create cgroup: operation not permitted`

Verify cgroup v2 is mounted, or force v1:

```bash
cat /sys/fs/cgroup/cgroup.controllers   # should list controllers
# then either fix the mount, or set --cgroup-version v1
```

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

## References

- [Firecracker jailer documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md)
- [Cgroup v2](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html)
- [seccomp(2)](https://man7.org/linux/man-pages/man2/seccomp.2.html)
- [Configuration guide](/guides/configuration/) — full `executor.jailer` schema
- [Advanced guide](/guides/advanced/) — privileged builds and custom kernels
