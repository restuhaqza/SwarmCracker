---
title: "Configuration Reference — SwarmCracker"
description: "Every configuration key in `swarmcracker` YAML config files, with defaults, types, and descriptions."
---

> Every configuration key in `swarmcracker` YAML config files, with defaults, types, and descriptions.

---

## Top-Level Structure

```yaml
version:    # Config schema version (currently 1; optional, defaults to 1)
executor:   # VM execution settings
network:    # Network infrastructure
logging:    # Log output configuration
images:     # Accepted but not implemented (see below)
metrics:    # Accepted but not implemented (see below)
snapshot:   # Snapshot management
```

The security jailer is **not** a top-level section. Configure it under
`executor.enable_jailer` plus `executor.jailer` (`uid`, `gid`,
`chroot_base_dir`, `netns`) — see [executor.jailer](#executorjailer).

---

## executor

### executor.name

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"firecracker"` |
| **Required** | No |

Executor backend name. Currently only `"firecracker"` is supported.

### executor.kernel_path

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"/usr/share/firecracker/vmlinux"` |
| **Required** | Yes |

Path to the uncompressed Linux kernel ELF binary used to boot all VMs.

### executor.kernel_profiles

| Property | Value |
|----------|-------|
| **Type** | `map[string]string` |
| **Default** | `{"guest-runtime-6.1": "/usr/share/firecracker/vmlinux-runtime"}` |
| **Required** | No |

Maps a golden-image kernel profile name to a kernel image path on the host.
Prebuilt golden images resolve their kernel through this registry, so a runtime
kernel can be selected per image instead of host-wide via `executor.kernel_path`.
The built-in profile is `guest-runtime-6.1`.

### executor.initrd_path

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `""` |
| **Required** | No |

Optional path to an initrd image. Leave empty to boot directly from the rootfs.

### executor.rootfs_dir

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"/var/lib/firecracker/rootfs"` |
| **Required** | Yes |

Directory where OCI images are converted to ext4 root filesystems. Each image gets a subdirectory based on its content hash.

### executor.socket_dir

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"/var/run/firecracker"` |
| **Required** | Yes |

Directory for Firecracker Unix domain sockets. One socket per VM, named `<task-id>.sock`.

### executor.default_vcpus

| Property | Value |
|----------|-------|
| **Type** | `int` |
| **Default** | `1` |
| **Range** | `1` – `host CPUs` |
| **Required** | No |

Default number of virtual CPUs per VM when not specified in the task spec.

### executor.default_memory_mb

| Property | Value |
|----------|-------|
| **Type** | `int` |
| **Default** | `512` |
| **Range** | `128` – `host memory` |
| **Required** | No |

Default memory in megabytes per VM when not specified in the task spec.

### executor.enable_jailer

| Property | Value |
|----------|-------|
| **Type** | `bool` |
| **Default** | `false` |
| **Required** | No |

Enable Firecracker jailer for additional process isolation (chroot, UID/GID drop, network namespace, cgroups).

### executor.init_system

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"tini"` |
| **Options** | `"tini"`, `"dumb-init"`, `"none"` |
| **Required** | No |

Init system injected into the VM rootfs. `tini` provides proper signal handling and zombie reaping. Use `none` for images with their own init (e.g., systemd-based).

> **Currently ignored.** `executor.init_system` is not read by the CLI, which
> hard-codes the `tini` init system (`cmd/swarmcracker/helpers.go`).

### executor.init_grace_period

| Property | Value |
|----------|-------|
| **Type** | `int` |
| **Default** | `10` |
| **Unit** | seconds |
| **Required** | No |

Seconds to wait for graceful shutdown via init system before force-killing the VM.

> **Currently ignored.** `executor.init_grace_period` is not read anywhere.

### executor.jailer

Jailer sub-configuration (only used when `executor.enable_jailer` is `true`).

```yaml
executor:
  enable_jailer: true
  jailer:
    uid: 1000
    gid: 1000
    chroot_base_dir: /srv/jailer
    netns: swarmcracker
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `uid` | int | `1000` | UID for the Firecracker process inside the jail |
| `gid` | int | `1000` | GID for the Firecracker process inside the jail |
| `chroot_base_dir` | string | `/srv/jailer` | Base directory for jail chroots (one per VM) |
| `netns` | string | `""` | Network namespace name (empty = host namespace) |

---

## network

### network.bridge_name

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"swarm-br0"` |
| **Max Length** | `15` (IFNAMSIZ) |
| **Pattern** | `[a-zA-Z0-9_-]+` |
| **Required** | No |

Name of the Linux bridge interface that VMs attach to. Must be 15 characters or fewer and contain only alphanumeric, hyphen, or underscore characters.

### network.subnet

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"192.168.127.0/24"` |
| **Format** | CIDR notation |
| **Required** | No |

Subnet for VM IP allocation. Each VM gets a deterministic IP from this subnet based on its task ID hash.

### network.bridge_ip

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"192.168.127.1/24"` |
| **Format** | CIDR notation |
| **Required** | No |

IP address assigned to the bridge interface. Acts as the default gateway for VMs.

### network.ip_mode

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"static"` |
| **Options** | `"static"`, `"dhcp"` |
| **Required** | No |

IP allocation mode. `static` assigns deterministic IPs via SHA-256 hash. `dhcp` uses dnsmasq for dynamic allocation.

### network.nat_enabled

| Property | Value |
|----------|-------|
| **Type** | `bool` |
| **Default** | `true` |
| **Required** | No |

Enable NAT masquerading on the bridge for internet access. When `false`, VMs are isolated to the bridge subnet with no external connectivity.

### network.enable_rate_limit

| Property | Value |
|----------|-------|
| **Type** | `bool` |
| **Default** | `false` |
| **Required** | No |

Enable per-VM network rate limiting.

### network.max_packets_per_sec

| Property | Value |
|----------|-------|
| **Type** | `int` |
| **Default** | `0` (unlimited) |
| **Required** | No |

Maximum packets per second per VM when rate limiting is enabled.

### VXLAN overlay (CLI flags, not config keys)

> **Not read from the config file.** `network.vxlan_enabled` and
> `network.vxlan_static_peers` are accepted by the loader but not consumed. The
> executor reads VXLAN settings from `swarmd-firecracker` flags instead.

Enable the VXLAN overlay for cross-node VM networking with the daemon flags:

```bash
swarmd-firecracker --vxlan-enabled --vxlan-peers 192.168.1.11,192.168.1.12
```

| Flag | Default | Description |
|------|---------|-------------|
| `--vxlan-enabled` | `false` | Create the VXLAN interface `<bridge_name>-vxlan` (for example `swarm-br0-vxlan`) and attach it to the bridge |
| `--vxlan-peers` | empty | Comma-separated static VXLAN peer IPs, for small clusters without Consul-based discovery |

`swarmcracker cluster init` / `cluster join` accept the same
`--vxlan-enabled` and `--vxlan-peers` flags and bake them into the generated
`swarmd-firecracker` systemd unit.

---

## logging

### logging.level

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"info"` |
| **Options** | `"debug"`, `"info"`, `"warn"`, `"error"` |
| **Required** | No |

Log level. `debug` includes token operations and internal state changes. Production should use `info` or higher.

### logging.format

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"json"` |
| **Options** | `"text"`, `"json"` |
| **Required** | No |

Log output format. The code default is `json`, which emits structured logs
suitable for production log aggregation. `text` is the human-readable
alternative; `config.example.yaml` uses `text` for illustration only.

### logging.output

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"stdout"` |
| **Options** | `"stdout"`, `"stderr"`, file path |
| **Required** | No |

Where logs are written. Use a file path like `/var/log/swarmcracker/daemon.log` for persistent logging.

---

## images

> **Not implemented.** The `images` section (`cache_dir`, `max_cache_size_mb`,
> `enable_layer_cache`) is accepted by the config loader, and `cache_dir` is
> defaulted, but no code consumes these keys. Image preparation uses
> `executor.rootfs_dir` and its own cache handling. Do not rely on these keys
> taking effect.

---

## metrics

> **Not implemented.** The `metrics` section (`enabled`, `address`, `format`)
> is accepted by the config loader but has no consumers. Prometheus metrics are
> always served by the daemon's health server at
> `http://127.0.0.1:8080/metrics` (configurable with `--health-addr`).

---

## snapshot

### snapshot.enabled

| Property | Value |
|----------|-------|
| **Type** | `bool` |
| **Default** | `false` |
| **Required** | No |

Enable VM snapshot support. Requires the snapshot directory to exist and be writable.

### snapshot.snapshot_dir

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"/var/lib/firecracker/snapshots"` |
| **Required** | Yes (when enabled) |

Directory where VM snapshots (memory dumps + state files) are stored.

### snapshot.max_snapshots

| Property | Value |
|----------|-------|
| **Type** | `int` |
| **Default** | `3` |
| **Required** | No |

Maximum number of snapshots to retain per service. Oldest snapshots are deleted when the limit is reached.

### snapshot.max_age

| Property | Value |
|----------|-------|
| **Type** | `duration string` |
| **Default** | `"168h"` (7 days) |
| **Format** | Go duration: `"24h"`, `"7d"`, `"168h"` |
| **Required** | No |

Maximum age of snapshots before they are eligible for cleanup. Supports `d` suffix for days.

### snapshot.auto_snapshot

| Property | Value |
|----------|-------|
| **Type** | `bool` |
| **Default** | `false` |
| **Required** | No |

Automatically create snapshots before service updates. Recommended for production.

### snapshot.compress

| Property | Value |
|----------|-------|
| **Type** | `bool` |
| **Default** | `false` |
| **Required** | No |

Compress snapshots to reduce disk usage at the cost of slower create/restore.

---

## Complete Example

### Development (single node)

```yaml
executor:
  name: firecracker
  kernel_path: /usr/share/firecracker/vmlinux
  rootfs_dir: /var/lib/firecracker/rootfs
  socket_dir: /var/run/firecracker
  default_vcpus: 1
  default_memory_mb: 512
  init_system: tini
  init_grace_period: 10

network:
  bridge_name: swarm-br0
  subnet: 192.168.127.0/24
  bridge_ip: 192.168.127.1/24
  ip_mode: static
  nat_enabled: true

logging:
  level: debug
  format: text
  output: stdout
```

### Production (multi-node cluster)

```yaml
executor:
  name: firecracker
  kernel_path: /usr/share/firecracker/vmlinux
  rootfs_dir: /var/lib/firecracker/rootfs
  socket_dir: /var/run/firecracker
  default_vcpus: 2
  default_memory_mb: 512
  init_system: tini
  init_grace_period: 10
  enable_jailer: true
  jailer:
    uid: 1000
    gid: 1000
    chroot_base_dir: /srv/jailer

network:
  bridge_name: swarm-br0
  subnet: 192.168.127.0/24
  bridge_ip: 192.168.127.1/24
  ip_mode: static
  nat_enabled: true

logging:
  level: info
  format: json
  output: /var/log/swarmcracker/daemon.log

snapshot:
  enabled: true
  snapshot_dir: /var/lib/firecracker/snapshots
  max_snapshots: 20
  max_age: 168h
  auto_snapshot: true
  compress: true
```

---

## Configuration File Location

The config file is loaded from (in order of priority):

1. `--config` / `-c` CLI flag
2. `SWARMCRACKER_CONFIG` environment variable
3. `/etc/swarmcracker/config.yaml` (default)

## Validation

```bash
# Validate config without starting
swarmcracker config validate

# Validate a specific config file (positional or --config)
swarmcracker config validate /path/to/config.yaml
swarmcracker config validate --config /path/to/config.yaml

# Show effective configuration (defaults applied)
swarmcracker config ls
```

---

**See Also:** [CLI Reference](/reference/cli/) | [Operations Guide](/guides/operations/) | [Getting Started](/getting-started/)
