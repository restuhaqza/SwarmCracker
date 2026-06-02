# Configuration Reference — SwarmCracker

> Every configuration key in `swarmcracker` YAML config files, with defaults, types, and descriptions.

---

## Top-Level Structure

```yaml
executor:   # VM execution settings
network:    # Network infrastructure
logging:    # Log output configuration
images:     # Image preparation and caching
metrics:    # Metrics collection
snapshot:   # Snapshot management
jailer:     # Security jailer (deprecated — use executor.jailer)
```

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

### executor.init_grace_period

| Property | Value |
|----------|-------|
| **Type** | `int` |
| **Default** | `10` |
| **Unit** | seconds |
| **Required** | No |

Seconds to wait for graceful shutdown via init system before force-killing the VM.

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
| `uid` | int | `0` | UID for the Firecracker process inside the jail |
| `gid` | int | `0` | GID for the Firecracker process inside the jail |
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
| **Options** | `"json"`, `"console"` |
| **Required** | No |

Log output format. `json` for structured logging (recommended for production). `console` for human-readable output.

### logging.output

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"stderr"` |
| **Options** | `"stderr"`, `"stdout"`, file path |
| **Required** | No |

Where logs are written. Use a file path like `/var/log/swarmcracker/daemon.log` for persistent logging.

---

## images

### images.cache_dir

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"/var/cache/swarmcracker"` |
| **Required** | No |

Directory for OCI image layer caching. Layer caching avoids re-pulling unchanged layers.

### images.max_cache_size_mb

| Property | Value |
|----------|-------|
| **Type** | `int` |
| **Default** | `10240` (10 GB) |
| **Required** | No |

Maximum total size of cached images. Oldest images are evicted when the limit is reached.

### images.enable_layer_cache

| Property | Value |
|----------|-------|
| **Type** | `bool` |
| **Default** | `true` |
| **Required** | No |

Enable OCI layer caching. Disable to always pull fresh images.

---

## metrics

### metrics.enabled

| Property | Value |
|----------|-------|
| **Type** | `bool` |
| **Default** | `true` |
| **Required** | No |

Enable metrics collection and exposure.

### metrics.address

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"127.0.0.1:9100"` |
| **Required** | No |

Address to expose Prometheus metrics on. Bind to `127.0.0.1` for security; use `0.0.0.0` if you need remote scraping.

### metrics.format

| Property | Value |
|----------|-------|
| **Type** | `string` |
| **Default** | `"prometheus"` |
| **Options** | `"prometheus"` |
| **Required** | No |

Metrics format. Currently only Prometheus text format is supported.

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
| **Default** | `"/var/lib/swarmcracker/snapshots"` |
| **Required** | Yes (when enabled) |

Directory where VM snapshots (memory dumps + state files) are stored.

### snapshot.max_snapshots

| Property | Value |
|----------|-------|
| **Type** | `int` |
| **Default** | `10` |
| **Required** | No |

Maximum number of snapshots to retain. Oldest snapshots are deleted when the limit is reached.

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
  format: console
  output: stderr
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

images:
  cache_dir: /var/cache/swarmcracker
  max_cache_size_mb: 20480
  enable_layer_cache: true

metrics:
  enabled: true
  address: 127.0.0.1:9100

snapshot:
  enabled: true
  snapshot_dir: /var/lib/swarmcracker/snapshots
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

# Validate a specific config file
swarmcracker config validate --config /path/to/config.yaml

# Show effective configuration (defaults applied)
swarmcracker config list
```

---

**See Also:** [CLI Reference](../reference/cli.md) | [Operations Guide](../guides/operations.md) | [Getting Started](../getting-started/README.md)
