# Security Guide

> Secure your microVMs with Jailer — resource isolation, cgroups, namespaces.

---

## Overview

SwarmCracker uses **Jailer** to isolate Firecracker microVMs:

```
┌─────────────────────────────────────────────────────────┐
│  Jailer Sandbox                                          │
│  ┌─────────────────────────────────────────────────┐   │
│  │  Firecracker Process                             │   │
│  │  - PID namespace isolated                        │   │
│  │  - Network namespace isolated                    │   │
│  │  - Mounted in separate rootfs                    │   │
│  │  - Cgroups limit CPU/memory                      │   │
│  │  - Seccomp filters syscalls                      │   │
│  └─────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

**Jailer provides:**
- **Process isolation** — PID namespace
- **Network isolation** — Network namespace (separate TAP)
- **Filesystem isolation** — Chroot jail
- **Resource limits** — Cgroups (CPU, memory, I/O)
- **Syscall filtering** — Seccomp-bpf

---

## Configuration

```yaml
jailer:
  enabled: true
  uid: 998            # firecracker user
  gid: 998            # firecracker group
  chroot_base: "/var/lib/jailer"
  cgroup_version: "v2"
  seccomp_level: "basic"
```

| Option | Default | Description |
|--------|---------|-------------|
| `enabled` | `true` | Enable Jailer sandbox |
| `uid` | `998` | User ID for Firecracker process |
| `gid` | `998` | Group ID for Firecracker process |
| `chroot_base` | `/var/lib/jailer` | Chroot directory |
| `cgroup_version` | `v2` | Cgroup version (v1 or v2) |
| `seccomp_level` | `basic` | Syscall filter level |

---

## Setup Jailer

### 1. Create firecracker User

```bash
sudo useradd -r -u 998 -g 998 firecracker
sudo groupadd -r -g 998 firecracker
```

### 2. Install Jailer

```bash
# Download Firecracker release (includes Jailer)
curl -fsSL https://github.com/firecracker-microvm/firecracker/releases/download/v1.15.1/firecracker-v1.15.1-x86_64.tgz | tar xz

sudo cp release-v1.15.1-x86_64/jailer /usr/local/bin/
sudo chmod +x /usr/local/bin/jailer
```

### 3. Create Jailer Directory

```bash
sudo mkdir -p /var/lib/jailer
sudo chown firecracker:firecracker /var/lib/jailer
```

---

## Cgroup Limits

Limit resources per VM:

```yaml
jailer:
  cgroup:
    cpu_quota: 50000    # 50% of CPU (100000 = 100%)
    memory_limit: "512M"
    io_weight: 100      # I/O priority (1-10000)
```

### Verify Cgroups

```bash
# Check cgroup hierarchy
ls /sys/fs/cgroup/firecracker/

# Check CPU limit
cat /sys/fs/cgroup/firecracker/svc-nginx/cpu.max

# Check memory limit
cat /sys/fs/cgroup/firecracker/svc-nginx/memory.max
```

---

## Seccomp Filtering

Control syscall access:

| Level | Description |
|-------|-------------|
| `none` | No filtering (development only) |
| `basic` | Block dangerous syscalls |
| `strict` | Minimal syscall set |

**Basic level blocks:**
- `execve`, `fork`, `clone` — No new processes
- `mount`, `umount` — No filesystem changes
- `chroot`, `pivot_root` — No root changes
- `kexec_load` — No kernel loading

---

## Filesystem Isolation

Each VM gets isolated rootfs:

```
/var/lib/jailer/
├── svc-nginx-abc123/
│   ├── rootfs/           # VM root filesystem
│   ├── kernel/           # vmlinux binary
│   └── firecracker.socket
└── svc-redis-def456/
│   ├── rootfs/
│   ├── kernel/
│   └── firecracker.socket
```

VM cannot access host filesystem.

---

## Network Isolation

Each VM gets its own network namespace:

```
┌─────────────────────────────────────────────────────────┐
│  Jailer Sandbox                                          │
│  ┌─────────────────────────────────────────────────┐   │
│  │  Network Namespace                               │   │
│  │  - tap0 (only visible in this namespace)        │   │
│  │  - No host network access                       │   │
│  └─────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

VM can only see its own TAP device, not host network.

---

## Security Checklist

| Item | Status | Command |
|------|--------|---------|
| KVM access restricted | ✅ | `ls -la /dev/kvm` (owned by firecracker) |
| Jailer user created | ✅ | `id firecracker` |
| Cgroups configured | ✅ | `ls /sys/fs/cgroup/firecracker/` |
| Seccomp enabled | ✅ | `cat config.yaml \| grep seccomp` |
| Chroot directory secured | ✅ | `ls -la /var/lib/jailer` |
| Network namespace per VM | ✅ | `ip netns list` |

---

## Disable Jailer (Development Only)

For debugging without sandbox:

```yaml
jailer:
  enabled: false
```

⚠️ **Warning:** VMs run without isolation. Use only for development.

---

## Troubleshooting

### Permission Denied

```bash
# Check firecracker user has KVM access
sudo usermod -aG kvm firecracker

# Check Jailer directory ownership
sudo chown -R firecracker:firecracker /var/lib/jailer
```

### Cgroup Errors

```bash
# Check cgroup v2 enabled
mount | grep cgroup2

# Enable cgroup v2 (if needed)
sudo grub-edit-config --update-kernel=ALL --remove-args="systemd.unified_cgroup_hierarchy=0"
```

### Seccomp Blocking Needed Syscall

```bash
# Use basic level for development
# Check which syscalls blocked
grep -r "SCMP_ACT" /usr/share/firecracker/seccomp/
```

---

## Reference

| Topic | Link |
|-------|------|
| Jailer docs | [Firecracker Jailer](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md) |
| Cgroups v2 | [Kernel Cgroups](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html) |
| Seccomp | [Seccomp BPF](https://man7.org/linux/man-pages/man2/seccomp.2.html) |

---

**See Also:** [Configuration](configuration.md) | [Advanced](advanced.md)