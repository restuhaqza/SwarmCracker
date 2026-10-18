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

Directory where OCI images are converted to ext4 root filesystems. It holds the
per-image cache, and each task's private copy lives in a `tasks/` subdirectory so
a task ID can never collide with an image name:

- `<image>-<tag>.ext4` — the shared **cache**. It is a template: no VM ever boots
  from it, and nothing a guest writes can reach it. An image whose name contains a
  literal `-` (e.g. `my-app:latest`) also carries a short hash, so refs that
  flatten to the same name cannot collide.
- `tasks/<task-id>.ext4` — the private, writable copy a task actually boots from.
  It is created when the task starts (a sparse copy, reflinked where the
  filesystem supports one), deleted when the task is removed or its VM is stopped,
  and reaped by the periodic cleanup if a task dies without a teardown.

With `jailer` enabled the jailer copies the rootfs into its chroot, so a task then
holds two copies on disk.

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

Enable Firecracker jailer for additional process isolation (chroot, UID/GID drop, network namespace). Per-task cgroup limits are independent of this flag — see [cgroup v2 host requirements](#cgroup-v2-host-requirements).

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

### CNI address pools (CLI flags, not config keys)

> **Not read from the config file.** The CNI pools are `swarmd-firecracker` flags.

With `--enable-cni`, networks are carved from two pools:

| Flag | Default | Description |
|------|---------|-------------|
| `--cni-subnet-pool` | `--subnet` | Pool for the predefined networks (`ingress`, `docker_gwbridge`). Keeping it at the flat L2 subnet is what makes ingress task IPs reachable cluster-wide, so override it only deliberately |
| `--cni-overlay-subnet-pool` | `10.0.0.0/8` | Pool user-defined networks are carved from. Kept separate so a network created without `--subnet` never takes the flat L2 subnet |
| `--cni-subnet-size` | `24` | Prefix length of each allocated subnet |

A user-defined network created without `--subnet` is carved from
`--cni-overlay-subnet-pool`, skipping any subnet that overlaps the flat L2 subnet
(`--subnet`) or one a network already holds. An explicit `--subnet` is honored but
has to be free, and a network keeps the subnet it was given when it is
re-allocated after a restart.

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

> **No credential key here.** `images.registry_auth` is not part of the config
> schema, so setting it has no effect. Registry credentials for a private image
> are read from the manager's standard Docker config at
> `service create/update --with-registry-auth` time — see
> [Registry Credentials](#registry-credentials).

---

## metrics

> **Not implemented.** Only the `metrics` config section (`enabled`, `address`,
> `format`) is inert: it is accepted by the config loader but has no consumers.
> The Prometheus endpoint itself *is* implemented and metric series are written
> and alertable. Metrics are always served by the daemon's health server at
> `http://127.0.0.1:8080/metrics` (configurable with `--health-addr`). The same
> server exposes `/healthz` and the live per-task health at `/v1/tasks/health`
> that `service ps` / `task ls` fan out to; see
> [Container Healthchecks](/guides/operations/#live-health-cluster-wide).
>
> Not every declared series has a writer yet — see
> [Metrics](/guides/operations/#metrics) for exactly which are populated and
> [Alerting](/guides/alerting/) for the default rules built on those signals.

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

## cgroup v2 host requirements

SwarmCracker applies per-task **cgroup v2** limits outside the YAML config: there
are no `cgroup.*` keys in this file. The daemon flags control it
(`swarmd-firecracker` is flags-only, like the VXLAN and CNI settings above).

| Flag | Default | Description |
|------|---------|-------------|
| `--enable-cgroups` | `true` | Apply per-task cgroup limits. **No longer requires `--enable-jailer`** — it applies to both the jailed and the direct start paths |
| `--parent-cgroup` | `firecracker` | Parent cgroup passed to the **jailer** for its own cgroup; it does not move SwarmCracker's per-task cgroup |
| `--cgroup-version` | auto-detect | `v1`/`v2` passed to the **jailer**. SwarmCracker's own per-task cgroup is always cgroup v2 |

Each service task's cgroup is `/sys/fs/cgroup/swarmcracker/<task-id>/`, holding
the VMM process (Firecracker, or the jailer process that launches it). `vm
create` creates one only when `--cpuset` or `--numa-node` is given. The quota
files and their values are described in
[CPU Pinning and cgroup Limits](/guides/advanced/#cpu-pinning-and-cgroup-limits).

### Requirements

The host must satisfy all of these:

- **cgroup v2 is mounted** at `/sys/fs/cgroup`. A v1-only host is not supported
  for these limits: the daemon cannot create its cgroup manager, and a requested
  cpuset or NUMA node list fails (see below). Without a placement request, the
  task runs without limits.
- The kernel **advertises** `cpu`, `memory`, `io` and `cpuset` in the parent
  cgroup's `cgroup.controllers`.
- The daemon can **write `cgroup.subtree_control`** in `/sys/fs/cgroup` and in
  `/sys/fs/cgroup/swarmcracker`, so the controllers are delegated into the
  per-task cgroup. On a normal install the daemon runs as root and the parent is
  the cgroup root, which it writes directly. If the daemon runs inside its own
  cgroup — a container, or a unit whose `/sys/fs/cgroup` is a delegated subtree
  (cgroup namespace) — that cgroup must delegate the four controllers. For a
  systemd unit that is `Delegate=yes`:

  ```ini
  # /etc/systemd/system/swarmcracker-worker.service.d/cgroup-delegate.conf
  [Service]
  Delegate=yes
  ```

  ```bash
  sudo systemctl daemon-reload
  sudo systemctl restart swarmcracker-worker
  ```

### What the operator sees when delegation fails

A **requested cpuset or NUMA node list** turns a delegation failure into a task
failure. The error names the requested set, the controllers it tried to enable,
the parent that refused, and the host's online CPUs:

```text
cannot apply cpuset "0-3" to task <task-id>: failed to enable cgroup controllers [cpu memory io cpuset]: cpuset: cannot delegate through parent /sys/fs/cgroup: <write error> (host online CPUs: 0-7)
```

When the kernel does not advertise the controller at all, the same wrapper
carries `not advertised by the kernel: cpuset` instead. On a host without cgroup
v2 the message is:

```text
cannot apply cpuset "0-3" to task <task-id>: the cgroup manager could not be created: cgroup v2 not available on this system (host online CPUs: unknown)
```

A task that requested **neither a cpuset nor a NUMA binding** is not failed by
any of this: controller delegation and cgroup creation degrade to warnings, and
the VM starts:

```text
Failed to enable cgroup controllers, continuing without the unavailable ones
Failed to create cgroup, running the VM without cgroup limits
```

Read the second warning literally. When the `cpu` or `memory` controller cannot
be written — `cpu.max`, or `memory.max`/`memory.high` — the daemon removes the
**whole** per-task cgroup, so **no limit applies at all**, not just the one whose
controller was missing. An unavailable `io` controller costs only `io.weight`,
which is best-effort anyway. Only an unavailable **`cpuset`** controller leaves
a fully working quota cgroup: with no CPU or memory placement requested there is
nothing to refuse. (If a placement *was* requested and only the quota fails, the
cgroup is kept with the placement in place — see
[CPU Pinning and cgroup Limits](/guides/advanced/#cpu-pinning-and-cgroup-limits).)

Check the controllers the kernel offers before rolling out pinned services:

```bash
cat /sys/fs/cgroup/cgroup.controllers
cat /sys/fs/cgroup/cgroup.subtree_control
cat /sys/fs/cgroup/swarmcracker/cgroup.subtree_control
```

See [Security → Verify isolation](/guides/security/#verify-isolation) for the
per-task files to read once a VM is running.

---

## Hugepages and NUMA host prerequisites

`--hugepages` and `--numa-node` are per-workload requests (`service create`,
`service update`, `vm create`), not daemon settings: there are no config keys for
them and no host-wide switch. The **host** must provide what they ask for, and
the node enforces the request when it starts the VM. See
[Advanced → Hugepages and NUMA Placement](/guides/advanced/#hugepages-and-numa-placement)
for the flag semantics and on-host verification.

### Hugepage pool

The node that runs the VM needs a pre-allocated **2M** hugetlbfs pool:

```bash
grep -i huge /proc/meminfo
# HugePages_Total:    1024
# HugePages_Free:     1024
# Hugepagesize:       2048 kB
```

- **2M pages only.** `Hugepagesize` must be `2048 kB`; a host configured for 1G
  pages is refused. Firecracker v1.15.1 offers only 2M hugetlbfs pages through
  `huge_pages`, and SwarmCracker exposes exactly that; it does not expose a
  transparent-hugepage setting.
- **Allocate the pool explicitly.** Firecracker maps guest memory from the pool
  and does not allocate huge pages on demand. Set the total with
  `sysctl -w vm.nr_hugepages=<pages>` and persist it (for example in
  `/etc/sysctl.d/90-hugepages.conf`). The value is the pool **total**, not an
  increment: 2 MiB of guest memory needs one page, so a 2 GiB VM needs 1024
  pages, and the pool must also cover pages already in use and every other
  hugepage-backed VM on the node.
- **Memory must be an even number of MiB.** An odd `--memory` is rejected while
  the task is translated, because the 2M pages cannot cover the odd tail.
- **The pool is host-global and shared.** It is not partitioned per service.
  Constrain hugepage workloads to nodes you have sized (for example a
  `node.labels.hugepages==yes` node label plus a matching `--constraint`).

The request's parity is checked earlier, while the task is translated
(`resolveHugepages`). The node then checks `Hugepagesize` and
`HugePages_Free >= mem_size_mib / 2` **before** the VMM process or its cgroup
exists. If the pool is short, the task fails and names the request, the pages
needed, the pool, and the total that would fix it:

```text
task <task-id>: cannot back a 512 MiB VM with 2M huge pages (256 pages needed): the host pool has 100 free of 512 allocated: raise the pool total to 668 pages with sysctl vm.nr_hugepages=668
```

`668` is `(Total - Free) + required`: `vm.nr_hugepages` sets the total, so
setting it to the requirement alone would still leave the VM short. A host with
the wrong page size is reported first, in the same wrapper, with
`the host's hugepage size is ... KiB, but Firecracker supports only 2M (2048 KiB) pages`.

### NUMA nodes

`--numa-node` lists host **node numbers** (a cpulist such as `0` or `0-1`) and
binds the VM's memory through `cpuset.mems`. Check what the host has online:

```bash
cat /sys/devices/system/node/online      # e.g. 0-1
cat /sys/devices/system/node/node*/meminfo | grep MemTotal
```

The nodes must be online on the node that runs the VM, and allowed by the
task's parent cgroup. Before the task's cgroup directory is created, the node
pre-checks every requested node against the host's online memory-node list
(`/sys/devices/system/node/online`) and the parent cgroup's allowed memory nodes
(`cpuset.mems.effective`, from the nearest readable ancestor; if none is
readable, only the online list is enforced); if either excludes a requested
node, the task fails and the remaining nodes are never used as a fallback. That pre-check is deliberate: the kernel by itself
accepts an offline-but-possible node, keeps it in `cpuset.mems`, and silently
narrows the effective `Mems_allowed_list` to the parent ∩ online intersection:

```text
cannot apply NUMA node list "3" to task <task-id>: cgroup validate stage: requested NUMA nodes 3 are not grantable: node(s) 3 are not online (host online NUMA nodes: 0-1)
```

A node the parent withholds is reported separately, against the parent's
effective file:

```text
cannot apply NUMA node list "3" to task <task-id>: cgroup validate stage: requested NUMA nodes 3 are not grantable: node(s) 3 are excluded by the parent cgroup's cpuset.mems.effective 0-1 (/sys/fs/cgroup/swarmcracker/cpuset.mems.effective; host online NUMA nodes: 0-1)
```

Because the binding is written through the cgroup, the host also needs the
cgroup v2 `cpuset` controller delegated as described above; a NUMA-only request
creates a per-task cgroup even when `--cpuset` is not given. There is **no
scheduler admission** for either feature: the CLI validates syntax only, and the
task can be placed on a node that cannot satisfy the request and fail there. Use
placement constraints to keep these workloads on suitable nodes.

---

## Registry Credentials

Pulling a **private** image needs a registry credential. SwarmCracker does not
read one from this config file: the CLI resolves it from the standard Docker
configuration, when a service is created or updated, and stores it on the
service spec.

### Where credentials are read from

`service create --with-registry-auth` resolves the credential with the Docker
keychain, which reads:

1. **`~/.docker/config.json`** — the file `docker login <registry>` writes. It
   must be the config of the account that runs `swarmcracker` (commonly `root`),
   so a login done as your own user is **not** visible to the manager:

   ```bash
   sudo docker login registry.example.com   # writes /root/.docker/config.json
   ```

2. **`$DOCKER_CONFIG/config.json`** — an alternative config file location.
3. **Credential helpers** — the `credsStore` and `credHelpers` entries in that
   file (for example `docker-credential-pass` or
   `docker-credential-osxkeychain`).

The lookup is per repository: a credential stored for `registry.example.com`
serves `registry.example.com/team/api:1.4.0`. A bare image name such as
`nginx:alpine` resolves against `index.docker.io`.

Within the Docker config, Docker's own precedence applies: a `credHelpers` entry
for the registry is consulted first, then the default `credsStore`, then an
inline `auths` entry.

There is **no** `swarmcracker login` command. Use `docker login` to write the
Docker config; the CLI only reads it.

### When credentials are resolved

Credentials are resolved at **create/update time**, not at pull time.
`service create --with-registry-auth` (and `service update
--with-registry-auth`) read the config, encode an `X-Registry-Auth` payload, and
store it on the service spec. A missing credential fails `service create` before
any request is sent, and `service update` before the update request is sent: the
update first reads the service to decide whether it is golden, so its
`GetService` lookup does run. Nothing is created or modified in either case.
The [CLI Reference → Registry
authentication](/reference/cli/#registry-authentication) has the per-command
semantics and the `service update` gotchas.

### What runs where

- The **CLI** resolves the credential and stores it on the service spec, so the
  account that runs `swarmcracker` needs the Docker config (or credential
  helper) at the moment the command runs. The CLI dials the manager's local
  socket, so in a normal install that is the manager itself.
- The **worker** consumes the stored payload when it pulls the image and needs
  **no** local Docker config and **no** `docker login`.
- The payload never reaches logs, metrics, or the JSON renderings of
  `service inspect`, `task inspect`, `service ls` and `task ls`: those replace it
  with `***`, so an operator can see that a service carries credentials without
  seeing what they are. The plain-text renderings (`service inspect` with a
  non-JSON `--format`, the tables) omit the credential field entirely.

### In the config file

`images.registry_auth` is **not** a config key. The image preparer has an
internal `registry_auth` field (`pkg/image`'s `PreparerConfig`) which is a
preparer-global fallback, but nothing populates it from the config file, so
setting it in YAML has no effect (the loader accepts unknown keys and ignores
them). Configure credentials through the Docker config instead.

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
swarmcracker config file validate

# Validate a specific config file (positional or --config)
swarmcracker config file validate /path/to/config.yaml
swarmcracker config file validate --config /path/to/config.yaml

# Show effective configuration (defaults applied)
swarmcracker config file ls
```

---

**See Also:** [CLI Reference](/reference/cli/) | [Operations Guide](/guides/operations/) | [Getting Started](/getting-started/)
