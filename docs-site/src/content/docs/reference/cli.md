---
title: "CLI Command Reference"
description: "Complete reference for the `swarmcracker` and `swarmctl` command-line tools."
---

> Complete reference for the `swarmcracker` and `swarmctl` command-line tools.

---

## swarmcracker

The main CLI for cluster management, service deployment, and VM operations.

### Global Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--config` | `-c` | `/etc/swarmcracker/config.yaml` | Configuration file path |
| `--log-level` | — | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `--kernel` | — | — | Override the Firecracker kernel path |
| `--rootfs-dir` | — | — | Override the rootfs directory |
| `--ssh-key` | — | — | SSH private key for remote deployment |
| `--known-hosts` | — | — | Path to the SSH known_hosts file |
| `--insecure-ssh` | — | `false` | Skip SSH host key verification |
| `--output` | `-o` | — | Machine output format for commands that support it (`table`, `json`, `name`) |
| `--host` | `-H` | — | Manager API endpoint (e.g. `https://mgr:4244`); selects the v1 API instead of the local control socket |
| `--ca` | — | — | PEM CA bundle for the manager API; required with `-H` (plaintext HTTP is refused) |
| `--token-file` | — | — | File containing the bearer token for `-H` |
| `--token` | — | — | Explicit bearer token (wins over `--token-file`) |
| `--tls-server-name` | — | — | TLS server name override when the certificate does not name the dialled host |
| `--version` | `-v` | — | Print version information |
| `--help` | `-h` | — | Help for any command |

`-H` / `--host` is accepted only by the commands that implement a remote read:
`service ls|inspect|ps` (with `-o json`, or `-o name` on the list forms; a remote
table exists only for `service ls`), `task ls`, `node ls`, `config ls`,
`secret ls`, `network ls`, `vm exec`, `vm attach` and
`vm snapshot stop-restored`. Every other command refuses `-H` with a `usage`
error (exit `2`) rather than silently reading the local cluster. That includes
the other `inspect` forms (`task`, `node`, `config`, `secret`, `network`) and the
other `vm snapshot` subcommands: they are local-only until they grow a contract
path.

### Commands

#### `swarmcracker cluster`

Cluster lifecycle management.

| Subcommand | Description |
|------------|-------------|
| `init` | Initialize a new cluster (manager) |
| `join <manager-addr>` | Join an existing cluster |
| `leave` | Leave the cluster |
| `deinit` | Deinitialize the local manager |
| `reset` | Reset the node completely |
| `status [vm-id]` | Cluster overview (no argument), or a VM's status when a VM id is given |
| `health` | Run cluster health checks |
| `token [worker\|manager]` | Display join tokens (`worker`, `manager`, or both) |

`cluster token` takes an optional role argument and must run on a manager node
(it reads the tokens over the local SwarmKit control socket). There are no
`token create` / `token list` / `token rotate` subcommands.

**`cluster init` flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--advertise-addr` | auto-detect | Address advertised to the cluster |
| `--listen-addr` | `0.0.0.0:4242` | Listen address |
| `--state-dir` | `/var/lib/swarmkit` | Cluster state directory |
| `--config-dir` | `/etc/swarmcracker` | Configuration directory |
| `--kernel` | `/usr/share/firecracker/vmlinux` | Firecracker kernel |
| `--rootfs-dir` | `/var/lib/firecracker/rootfs` | Rootfs directory |
| `--socket-dir` | `/var/run/firecracker` | Firecracker socket directory |
| `--snapshot-dir` | `/var/lib/firecracker/snapshots` | Snapshot directory (also added to the unit's writable paths) |
| `--vcpus` | `1` | Default vCPUs per microVM |
| `--memory` | `512` | Default memory (MB) per microVM |
| `--bridge-name` | `swarm-br0` | VM bridge name |
| `--subnet` | `192.168.127.0/24` | VM subnet |
| `--bridge-ip` | `192.168.127.1/24` | Bridge IP |
| `--vxlan-enabled` | `false` | Enable VXLAN overlay |
| `--vxlan-peers` | — | Comma-separated VXLAN peer IPs |
| `--enable-cni` | `true` | Enable the CNI network provider |
| `--debug` | `false` | Debug logging |
| `--force` | `false` | Force init even if a manager exists |

**`cluster join` flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--token` | (required) | Join token from the manager |
| `--advertise-addr` | auto-detect | Address advertised to the cluster |
| `--hostname` | auto-detect | Node hostname |
| `--worker` | `true` | Join as a worker |
| `--manager` / `-m` | `false` | Join as a manager (needs a manager token) |
| `--enable-cni` | `true` | Enable the CNI network provider |
| `--state-dir` | `/var/lib/swarmkit` | Cluster state directory |
| `--snapshot-dir` | `/var/lib/firecracker/snapshots` | Snapshot directory (passed to the unit and added to its writable paths) |
| `--vxlan-enabled` / `--vxlan-peers` | — | VXLAN overlay options |

**`cluster leave` flags:** `--purge`, `--force`, `--keep-network`, `--state-dir`, `--bridge-name`, `--config-dir`
**`cluster deinit` flags:** `--purge`, `--force`, `--cleanup-network`, `--keep-tokens`, `--state-dir`, `--rootfs-dir`, `--bridge-name`, `--config-dir`
**`cluster reset` flags:** `--hard`, `--keep-config`, `--keep-rootfs`, `--state-dir`, `--rootfs-dir`, `--bridge-name`, `--config-dir`
**`cluster health` flags:** `--format` (`table`, `json`, `nagios`), `--quiet` / `-q`

#### `swarmcracker node`

Node management.

| Subcommand | Description |
|------------|-------------|
| `ls` | List nodes |
| `inspect <node-id>` | Inspect a node |
| `drain <node-id>` | Drain a node (reschedule its tasks) |
| `activate <node-id>` | Activate a drained node |
| `promote <node-id>` | Promote a worker to manager |
| `update <node-id>` | Update a node's placement labels (`--label-add` / `--label-rm`) |
| `rm <node-id>` | Remove a node |

`node ls` flags: `--filter`, `--output` / `-o` (`table`, `json`, `name`), legacy
`--format` (`table`, `json`), `--quiet` / `-q`.

`-o json` emits the v1 `NodeList` envelope — `schemaVersion`, `kind`, `items`
and `next`. `-o name` emits one node ID per line. Both accept the same filters:
`role=`, `membership=`, `availability=`, `state=`, `id=`, `hostname=`,
`label=key` (key present) and `label=key=value`; enum and hostname values are
case-insensitive, label values are exact, and `id=` is exact and
case-sensitive (it is not a prefix match). An empty `-o json` result is
`items: []` and an empty legacy `--format json` result is `[]`. On an empty
cluster, table mode prints `No nodes found`; a filter that matches nothing
instead prints the column header and `Total: 0 node(s)`. A failure in `-o json`
mode writes a single `Error` document to stderr and the process exits with that
error's status code instead of `0`; `-o name` prints the failure as plain text
and exits the same way. `-o` and `--format` must not be combined.

`node inspect` flags: `--format`, `--pretty`.

`node update` flags: `--label-add KEY=VALUE` and `--label-rm KEY`, both
repeatable and at least one required. For `--label-add`, `KEY=VALUE` is required
(a missing `=` or a blank key is rejected with the offending argument named), and
an existing key is overwritten. `--label-rm` removes a key; removing a key the
node does not have is a **no-op**, not an error.

Labels are stored on the node's spec and matched by the scheduler's
`node.labels.*` placement constraints, so labelling a node is how a
`node.labels.<key>==<value>` constraint becomes satisfiable:

```bash
# Constraint fails until a node carries the label
swarmcracker node update <node-id> --label-add disk=ssd
swarmcracker service create --name web --image nginx:alpine \
    --constraint node.labels.disk==ssd

# Remove it again
swarmcracker node update <node-id> --label-rm disk
```

`node`, `service`, and `task` references accept an exact ID, a unique ID prefix
(for example the 12-character ID shown by the `ls` commands), or a name
(`service`/`node` only). Ambiguous prefixes are rejected with the candidates listed.

#### `swarmcracker service`

Service (replicated microVM) management.

| Subcommand | Description |
|------------|-------------|
| `create` | Create a service |
| `ls` | List services |
| `inspect <service-id>` | Inspect a service |
| `ps <service>` | List the tasks of a service |
| `update <service>` | Update a service |
| `scale <service> <replicas>` | Scale a service (`0` stops all its tasks) |
| `rm <service>` | Remove a service |

**`service create` flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | (required) | Service name |
| `--image` | — | Container image (required unless `--golden`) |
| `--golden` | — | Boot a prebuilt golden image (e.g. `ubuntu-24.04-docker@1.0.0`) |
| `--with-registry-auth` | `false` | Send the manager's credentials for `--image` to the nodes (resolved at create time; see [Registry authentication](#registry-authentication)) |
| `--replicas` | `1` | Number of replicas |
| `--autoscale` | — | Keep the replica count between bounds by scaling on guest CPU: `min=2,max=10,metric=cpu,target=70[,cooldown-up=60s][,cooldown-down=5m]` (see [Autoscaling](/guides/operations/#autoscaling)) |
| `--cpu` | — | CPU cores (e.g. `1.5`); sizes the microVM (see below) |
| `--memory` | — | Memory size (e.g. `512M`, `1G`); sizes the microVM, honoured exactly (see below) |
| `--balloon-mib` | — | Memory-balloon target in MiB (positive integer, less than the memory the VM will boot with; see [Memory Balloon](/guides/advanced/#memory-balloon)) |
| `--cpuset` | — | Pin the VM to these host CPUs (Linux cpulist: `0`, `0-3`, `0-3,8`); the host must have those CPUs online (see [CPU Pinning and cgroup Limits](/guides/advanced/#cpu-pinning-and-cgroup-limits)) |
| `--hugepages` | `false` | Back the guest memory with 2M huge pages; the host must have a large enough pre-allocated hugepage pool (see [Hugepages](/guides/advanced/#hugepages)) |
| `--numa-node` | — | Bind the VM's memory to these host NUMA nodes (Linux cpulist: `0`, `0-1`); the host must have those nodes online (see [NUMA placement](/guides/advanced/#numa-placement)) |
| `--disk` | content-based | Minimum VM rootfs size (e.g. `10G`); adds the `swarmcracker.disk` service label |
| `--env` / `-e` | — | Environment variables |
| `--command` | — | Override the container command |
| `--args` | — | Container arguments |
| `--label` / `-l` | — | Service labels |
| `--publish` / `-p` | — | Publish a port: `[host:]container[/tcp\|udp]` (repeatable, e.g. `-p 8080:80`) |
| `--publish-mode` | `ingress` | How published ports are exposed: `ingress` (cluster load-balanced) or `host` (per-replica host port) |
| `--mode` | `replicated` | Service mode: `replicated` or `global` (one task per node) |
| `--constraint` | — | Placement constraint `key==value` or `key!=value` (repeatable, e.g. `node.hostname==worker1`) |
| `--placement-pref` | — | Placement preference `spread=<key>` (repeatable, e.g. `spread=node.labels.zone`) |
| `--restart-condition` | `any` | Restart condition: `none`, `on-failure` or `any` |
| `--restart-delay` | — | Delay between restart attempts (e.g. `5s`) |
| `--restart-max-attempts` | `0` | Max restart attempts before giving up (`0` = unlimited) |
| `--restart-window` | — | Window for evaluating the restart policy (e.g. `1h`) |
| `--update-order` | `stop-first` | Rolling-update order: `stop-first` or `start-first` |
| `--update-parallelism` | `0` | Tasks updated in parallel (`0` = unlimited) |
| `--update-delay` | — | Delay between updates (e.g. `10s`) |
| `--update-failure-action` | `pause` | Action on update failure: `pause`, `continue` or `rollback` |
| `--update-monitor` | — | Window to monitor a new task for failure (e.g. `30s`) |
| `--update-max-failure-ratio` | `0` | Fraction of tasks that may fail before the failure action (0–1) |
| `--rollback-*` | — | Same fields as `--update-*`, applied to rollbacks |
| `--mount` | — | Mount a volume or host path: `type=volume\|bind,source=<src>,target=<path>[,readonly]` (repeatable) |
| `--volume` / `-v` | — | Mount a volume or host path: `<src>:<dst>[:ro\|rw]` (repeatable) |
| `--hostname` | — | Guest VM hostname (RFC 1123) |
| `--dns` | — | Guest DNS nameserver, an IP address (repeatable). Unset points the guest at its node's bridge resolver, which serves service names (see [Service DNS](/guides/networking/#service-dns)) |
| `--network` | — | Attach the service to a user-defined network (repeatable; one NIC/IP per network) |
| `--secret` | — | Grant access to a secret: `[src=]NAME[,target=PATH][,mode=0400][,uid=N][,gid=N]` (repeatable) |
| `--config` | — | Grant access to a config: `[src=]NAME[,target=PATH][,mode=0444][,uid=N][,gid=N]` (repeatable) |
| `--health-cmd` | — | Container healthcheck command, run in the guest via `/bin/sh -c` (enables the healthcheck) |
| `--health-interval` | `30s` | Time between healthcheck runs (only with `--health-cmd`) |
| `--health-timeout` | `30s` | Per-check timeout (only with `--health-cmd`) |
| `--health-retries` | `3` | Consecutive failures before the task is unhealthy (only with `--health-cmd`) |
| `--health-start-period` | `0` | Grace period during which failures do not count (only with `--health-cmd`) |
| `--user` | — | **Not supported** — rejected with a clear error |
| `--cap-add` | — | **Not supported** — rejected with a clear error |
| `--cap-drop` | — | **Not supported** — rejected with a clear error |
| `--read-only` | — | **Not supported** — rejected with a clear error |

`--mount` / `--volume` attach data into the guest. A **named volume**
(`type=volume,source=myvol` or `myvol:/data`) is a volume created with
`swarmcracker volume create`; a **bind** mount (`type=bind,source=/host/path` or
`/host/path:/data`) copies a host path from the node running the task. Every task
boots from its own **private copy of the rootfs** — the shared image cache is
never booted or modified, so nothing a guest writes leaks between tasks — and
mounts are materialized into that copy at start time, which means the content is a
snapshot taken when the task starts — the data is not live-shared with the host.
Read-only mounts (`:ro` / `readonly`) are not written back to the volume when the
task stops.

`--mode global` runs one task per node (SwarmKit schedules it) and ignores
`--replicas`. `--constraint` and `--placement-pref` are enforced by the SwarmKit
scheduler (`node.hostname`, `node.role`, `node.labels.*`, …). `--restart-*` and
`--update-*` / `--rollback-*` are enforced by SwarmKit's restart and
rolling-update orchestration.

`--cpu` and `--memory` **size the microVM** the task boots in. A microVM is a
hard allocation, so the declared value is the hard cap (`Resources.Limits`):
`--memory 256M` boots a **256 MiB** VM. A declared value is honoured **exactly**,
with no silent floor — a memory size below 512 MiB is booted as asked, and
`1 vCPU / 512 MiB` apply only when nothing is declared. A `--cpu` value is a core
count resolved to whole vCPUs, with a minimum of one (`--cpu 2` boots 2 vCPUs;
`--cpu 1.5` and `--cpu 0.5` both boot 1). The size is resolved on the node in the
order `Resources.Limits` → `Resources.Reservations` → the `1 vCPU / 512 MiB`
default, so a task that declares only a reservation is sized from it too;
`--cpu`/`--memory` are what populate the `Limits` arm. On `service update`,
`--cpu-limit` and `--memory-limit` set those same `Limits` values, so they size
the VM that SwarmKit rolls next.

`--hostname` and `--dns` are honored: the value travels to the executor and the
guest init applies it at boot (the VM's hostname and `/etc/resolv.conf`). When
`--dns` is not given, the guest is pointed at its node's bridge gateway, where
the service-name records live — pass `--dns` to override that with your own
resolvers.
Because a SwarmCracker workload is a whole microVM rather than a process sharing
a kernel, the container-execution flags `--user`, `--cap-add`, `--cap-drop` and
`--read-only` cannot be enforced — the CLI **rejects them with a clear error** at
create time instead of accepting and silently ignoring them. `--hostname` and
`--dns` are also rejected with `--golden`, whose images boot their own init.

`--command` / `--args` replace the image's OCI entrypoint for the service's
tasks. The values travel to the guest as per-task kernel arguments and are
decoded by the init wrapper, so spaces, quotes and non-ASCII bytes inside an
argument survive intact. A value containing a line break or NUL is rejected at
create time: the kernel command line cannot carry it, and silently changing the
argument would be worse than failing. Both flags are rejected with `--golden`
for the same reason as `--hostname`/`--dns`.

`--publish 8080:80` exposes port `80` inside the microVM. Multiple mappings are
allowed (`-p 8080:80 -p 53:53/udp`); `tcp` is the default protocol. The host port
is required (ephemeral allocation is not supported yet).

`--publish-mode ingress` (default) load-balances the published port across the
service's healthy replicas through a per-service VIP, reachable on manager nodes
(`--replicas 3` gets one entry point). `--publish-mode host` forwards the host
port on the node running each replica, with per-node collision semantics. The
mapping is released when the service is removed or scaled down. See
[Publishing a Service Port](/guides/networking/#publishing-a-service-port).

`--disk` sets a **minimum** rootfs size. By default the rootfs is sized from the image content plus 50% overhead (floor 100 MB); with `--disk 10G` it is grown to at least 10 GB, leaving the rest as free space in the guest. The prepared rootfs is keyed by image, so requesting a larger disk rebuilds that image's rootfs (and it is not shrunk again by a later smaller request).

`--balloon-mib <int>` configures a **virtio-balloon device** that lets the node reclaim up to `<int>` MiB of the guest's memory. The unit is MiB — an explicit integer, not a `512m` size string — and the value must be a **positive integer smaller than the memory the VM will boot with** (the `--memory` given on the same command, else the 512 MiB default); an invalid value is rejected with an error naming both numbers, before any request is sent — including a target that only exceeds a *small* declared memory, so `--memory 256m --balloon-mib 300` fails at the CLI. Omitting the flag means no balloon; an explicit `--balloon-mib 0` is rejected as invalid rather than treated as "no balloon" (the flag exists only to configure a device). The target travels to the node as the service label `swarmcracker.balloon`, so combining `--balloon-mib` with a `--label swarmcracker.balloon=<n>` that **disagrees** is refused rather than resolved silently. The device is created before the VM boots, is configured with `deflate_on_oom: true`, and only takes effect when the guest kernel ships the virtio-balloon driver — see [Memory Balloon](/guides/advanced/#memory-balloon).

`--cpuset` pins the VM to host CPUs. The value is a Linux **cpulist** — a single
CPU (`0`), a range (`0-3`), or a comma-separated list of both (`0-3,8`) — and the
flag has **no shorthand**. The value is **canonicalised before it is stored**
(ranges ascending, overlapping and adjacent ranges merged), so `--cpuset 3,1-2`
is stored — and shown by `service inspect` — as `1-3`. A malformed or empty
value is rejected with an error naming the flag and the value before any request
is sent; on `service create` an empty value is an error, not "no pin".

The CLI validates the **syntax only**: there is no scheduler admission for CPU
availability. The pin is applied by the node when it starts the VM, so a task can
be placed on a node whose CPUs do not include the requested set and **fail
there** with an error naming the requested set and that host's online CPUs
(`/sys/devices/system/cpu/online`). A cpuset is a **hard promise** — a VM that
cannot be pinned does not start; the CPU/memory/IO quotas around it are
best-effort. See [CPU Pinning and cgroup Limits](/guides/advanced/#cpu-pinning-and-cgroup-limits).

The pin travels to the node as the `swarmcracker.cpuset` service label (a
`vm create` VM carries it as a task annotation), so a hand-set
`--label swarmcracker.cpuset=<list>` that **disagrees** with `--cpuset` is
refused rather than resolved silently. The check compares the flag's
**canonical** form with the label **exactly as written**, so the label must
already be canonical: `--label swarmcracker.cpuset=1-3 --cpuset 3,1-2` is
accepted (the flag canonicalises to `1-3`), while
`--label swarmcracker.cpuset=3,1-2 --cpuset 1-3` is refused even though both
name CPUs 1-3 — the label is not canonicalised before the comparison.

`--hugepages` asks the node to back the guest memory with **2M huge pages**
(2M is the only page size Firecracker v1.15.1 offers through `huge_pages`). It
is a boolean flag: on `service create` and `vm create` only the opt-in form
(`true`) is stored, as the `swarmcracker.hugepages` service label (a `vm create`
VM carries it as a task annotation); an omitted flag on those commands makes no
request, so the VM uses ordinary 4K memory. On `service update` an omitted flag
leaves the existing request untouched, and `--hugepages=false` removes it. The
host must already have a 2M hugepage pool large enough for the VM — see
[Hugepages](/guides/advanced/#hugepages) for sizing it and for the failure
shape. A request for a memory size that is not an even number of MiB is rejected
while the task is translated, because 2M pages cannot cover the odd tail.

`--numa-node <nodelist>` binds the VM's **memory** to the listed host NUMA nodes
through `cpuset.mems`. The value is a Linux **cpulist** of node numbers — `0`,
`0-1`, `0-1,3` — and is **canonicalised before it is stored** (`1,0` becomes
`0-1`). Like `--cpuset` it travels as the `swarmcracker.numa-node` service label
(annotation for `vm create`), and a hand-set label that disagrees with the flag
is refused. It binds only memory: it does not change the CPU affinity, so
combine it with `--cpuset` (for example `--cpuset 0-3 --numa-node 0`) for a full
node placement. Firecracker exposes **no guest NUMA topology** — the guest still
sees a single node. See [NUMA placement](/guides/advanced/#numa-placement) for
verification and the failure shape.

Neither flag is admitted by the scheduler: they are validated for syntax (and
canonicalised) **before any request is sent**, but the node applies them when it
starts the VM. A node whose pool or online nodes cannot satisfy the request
**fails the task**, naming the request and the host's state — this is a hard
error, not a warning. On `service create` and `vm create` an empty
`--numa-node ""` is an error, not "no binding". On `service update`, both flags
are also rejected together with `--rollback`.

`--image` and `--golden` are mutually exclusive. With `--golden`, the reference
is recorded as the `swarmcracker.golden` service label and no OCI image is pulled.

`--health-cmd` declares a container healthcheck. The command runs **inside the
microVM** through the guest agent over vsock — the same mechanism as `vm exec` —
via the guest shell (`/bin/sh -c`), and the check passes when the command exits
`0`. A task with a healthcheck is **not** reported `RUNNING` until a check
passes; this is what makes `--update-order start-first` wait for real readiness
(see [Container Healthchecks](/guides/operations/#container-healthchecks)). The
other four flags tune it and apply **only** when `--health-cmd` is set:
`--health-interval` (default `30s`), `--health-timeout` (default `30s`),
`--health-retries` (default `3`) and `--health-start-period` (default `0`).
Without `--health-cmd` no healthcheck exists and behaviour is unchanged. An
invalid value is **rejected** (`--health-retries 0`, `--health-interval 0s`), as
is any `--health-*` flag supplied without `--health-cmd`; no flag is silently
ignored.

**`service update` flags:** `--image`, `--replicas`, `--autoscale`, `--cpu-limit`, `--memory-limit`, `--balloon-mib` (tri-state: omit to preserve the current target, a MiB value to set it, `0` to remove it), `--cpuset` (tri-state: omit to preserve the current pin, a cpulist to set it, an empty value to remove it), `--hugepages` (tri-state: omit to preserve the current request, `--hugepages` to set it, `--hugepages=false` to remove it), `--numa-node` (tri-state: omit to preserve the current binding, a nodelist to set it, an empty value to remove it), `--env-add`, `--env-rm`, `--publish-mode`, `--mount`, `--volume` / `-v` (replace the mounts), `--network`, `--secret`, `--config`, `--hostname`, `--dns`, `--health-*` (replace the healthcheck; `--health-cmd` is required), `--with-registry-auth` (set, or with `=false` clear, the pull credential — see [Registry authentication](#registry-authentication)), `--rollback`, `--force` / `-f`, and the scheduling/lifecycle flags from `service create` (`--mode`, `--constraint`, `--placement-pref`, `--restart-*`, `--update-*`, `--rollback-*`). The rejected execution flags (`--user`, `--cap-add`, `--cap-drop`, `--read-only`) are also accepted here so they fail with the same clear error. `--mode` is accepted but a **change** is rejected (SwarmKit does not allow it); recreate the service to change its mode.

`--replicas` on `service update` applies only when the flag is present: passing
`--replicas 0` scales to zero, while omitting it leaves the replica count
unchanged. `service scale <service> 0` always scales to zero.
`--publish-mode ingress|host` on `service update` changes the mode of the
service's existing published ports (it errors if the service has none).
`service inspect` prints the published ports and, for ingress services, the
service VIP.
`--balloon-mib` on `service update` validates a new target against the memory the
updated task will carry — the `--memory-limit` given on the same command, else
the limit already on the service (and the 512 MiB default when it declares none)
— and sets the `swarmcracker.balloon` label, so the change takes effect on the
task SwarmKit starts next (the device cannot be reconfigured in place).

`--autoscale` gives a service a policy the manager keeps its replica count
inside: the count stays in the service spec, and the manager rewrites it between
`min` and `max` as load changes. The value is
`min=<n>,max=<n>,metric=cpu,target=<percent>`, with the optional
`cooldown-up=<dur>` and `cooldown-down=<dur>`; keys may be given in any order.
`min` is at least 1, `max` at least `min`, `target` is a percentage of one vCPU
above 0 and at most 100 (`target=70` is 0.70 of a vCPU per replica), and `cpu` is
the only metric in this release. The cooldowns default to `60s` and `300s`. The
policy is stored as the six `swarmcracker.autoscale.*` service labels (`min`,
`max`, `metric`, `target`, `cooldown_up`, `cooldown_down`), so it survives an
update that does not mention it and `service inspect` prints it back. A policy
declared purely through `--label` on `service create` is validated exactly like
the flag, and a `--label` that disagrees with `--autoscale` is refused rather
than resolved silently. A label under `swarmcracker.autoscale.` that is not one
of those six keys is refused at create/update rather than ignored, so a
misspelled key cannot leave a service that looks configured and never scales.

Two combinations are refused instead of stored and ignored: a policy on a
`--mode global` service (a global service runs one task per node and has no
replica count to scale), and an explicit replica count outside the policy — on
`service create --replicas`, `service update --replicas` and `service scale`
alike, with the error naming `min` and `max`. `--replicas 0` and `service scale
<service> 0` are the deliberate exception: zero means the service is paused, and
the loop neither aligns nor scales it. On `service update`, `--autoscale` is
tri-state: omit it to preserve the stored policy, give one to replace it, pass
`--autoscale ""` to remove every autoscale label. See
[Autoscaling](/guides/operations/#autoscaling) for the sampling cadence, the
hysteresis rules and the metrics.

`--cpuset` on `service update` is **tri-state**: omitting it leaves the service's
current pin untouched, a cpulist replaces it (canonicalised, as above), and an
explicit empty value — `--cpuset ""` — **removes** the pin. This clearing form
exists only on `service update`; on `service create` and `vm create` an explicit
empty value is an error. The new pin takes effect on the task SwarmKit starts
next, so a rolling update moves the VM to the requested CPUs. `--cpuset` cannot
be combined with `--rollback`, which restores the previous spec and ignores
other flags: the combination is rejected rather than silently dropping the pin.

`--hugepages` and `--numa-node` on `service update` are **tri-state** too.
Omitting a flag leaves the service's current request untouched; giving it
replaces the stored label (`--hugepages` sets `swarmcracker.hugepages=true`, and
`--numa-node 3,1-2` stores the canonical `1-3`); `--hugepages=false` and
`--numa-node ""` **remove** the request. A malformed `--numa-node` fails before
the service lookup, so no RPC is sent, and the new placement takes effect on the
task SwarmKit starts next (assigning `--hugepages` cannot change a running VM in
place). Neither flag can be combined with `--rollback`: the rollback branch
submits the previous spec and never consults them, so the combination —
including an explicit `--hugepages=false` or `--numa-node ""` clear — is
rejected instead of being silently dropped. See
[Hugepages](/guides/advanced/#hugepages) and
[NUMA placement](/guides/advanced/#numa-placement).

`service ls` / `service ps` flags: `--filter`, `--format`, `--quiet` / `-q`, and `--no-trunc`, `--health-port`, `--no-health` for `ps`.

`service logs <service>` streams the console logs of every task of a service in
one interleaved stream, each line prefixed with a short task ID. Flags:
`--follow` / `-f`, `--since`, `--tail` (per task; `-1` means as much as the node
serves, capped at 10000 lines). It requires the global `-H` / `--host`, because
only the manager can reach every node's console file; for one task on the local
node use `vm logs <task-id>`. A replica that has not written anything yet is
skipped rather than failing the command. See
[Operations § Reading logs across nodes](/guides/operations/#reading-logs-across-nodes).

`service ls` also accepts the global `-o` / `--output` flag. `-o json` emits the
v1 `ServiceList` envelope (`schemaVersion`, `kind`, `items`, `next`) and `-o name`
emits one ID per line; `--format json` remains the frozen legacy shape. On a
healthy empty cluster, both JSON paths emit valid JSON (`items: []` or `[]`)
instead of prose, and a failure in `-o json` mode writes a single `Error`
document to stderr.

`service inspect` also accepts the global `-o json` flag. It emits the v1
`ServiceDocument` envelope (`schemaVersion`, `kind`, identity, spec and observed
status) and resolves a full service ID, an exact name, or a unique ID prefix.
`--format json` remains the legacy raw SwarmKit object with credential
redaction; `-o name` and `-o table` are not defined for `service inspect`.
Not-found and ambiguous references exit with status 3.

`service ps` also accepts the global `-o json` / `-o name` flags. It emits the
v1 `TaskList` envelope scoped to the resolved service (or one task ID per line
for `-o name`); phase 0 omits live health from those modes, so `--health-port`
is rejected and `--quiet` is replaced by `-o name`. `--format json` keeps the
legacy task array, including its health and error fields.

`service ps` (and `task ls`) print a `HEALTH` column. It is the **live**
healthcheck state fetched from the node owning each task (`starting`, `healthy`,
`failing`, `unhealthy`) when that node's health server is reachable; a node that
answered without a live row for the task — an older daemon, or a task that has
not started — and any terminal task fall back to the **derived** health-gated
state (`STARTING` while the task is still being checked, `RUNNING` once a check
has passed, `FAILED` when the task failed its checks). A task with no healthcheck,
or one whose node did not answer at all, shows `-`. The same value is emitted as
`health` by `--format json`, with the raw observation as `live_health` when it
was fetched. See
[Container Healthchecks](/guides/operations/#seeing-health) for the gated-rollout
workflow, the live fan-out, and the API limitation behind the derived value.

`--no-health` skips the live lookup entirely (and the `ListNodes` call that
resolves node addresses), leaving only the derived value. `--health-port`
(default `8080`) selects the port of each node's health server; an invalid port
is rejected rather than ignored.

`service ps` (and `task ls`) also end in an `ERROR` column: the reason a task is
not running, taken from the task's `Status.Err` and **empty** for any task that
carries none (a row with no reason prints no trailing column at all). It is
deliberately not the task's `Status.Message`, which SwarmKit uses for routine
transition notes ("started") even on the success path — a healthy `RUNNING` task
would otherwise show `started` here. A blocked task — one the scheduler could not
place, or one the constraint or volume enforcer rejected — always carries an
`Err`, which is what the column shows. The table truncates a long reason at 40
characters; `task inspect` (or `--format json`) prints it in full. The column is
appended **after** `IMAGE`, so every existing column keeps its position.

When **no** task of a service is placed because the scheduler refused the
placement, the table is followed by one diagnostic line. When the task error
carries the constraint filter's own explanation, the line names the
constraint(s) and the labels the cluster actually reports:

```
no task is running: constraint "node.labels.disk==ssd" matches no node (available node labels: disk=hdd, hostname=sc-lab-node1, rack=r1, role=manager, role=worker)
```

SwarmKit's own reason is generic ("scheduling constraints not satisfied on 2
nodes") and truncated in the column, so this line is what tells an operator that
`disk=ssd` is missing rather than that the node count is wrong. Every constraint
on the service is named (the command cannot tell which of several failed),
`hostname` and `role` are included because `node.hostname` and `node.role` are
constraints too, and values are deduplicated and sorted.

A constraint is never blamed without evidence. SwarmKit joins *every* failing
filter into one error, so a service that carries a constraint can still be
refused because the nodes are full. When the error does not carry the constraint
filter's explanation, the line quotes the scheduler and lists the declared
constraints as declared, not as the cause:

```
no task is running: scheduler reported "no suitable node (insufficient resources on 1 node)"; declared constraints "node.labels.disk==ssd" may not be the cause (available node labels: disk=hdd, hostname=sc-lab-node1, rack=r1, role=manager, role=worker)
```

The line is a diagnostic only: it changes no exit code, it is printed only in
table mode, and it appears only when no task is placed (no task is `ASSIGNED`,
`ACCEPTED`, `PREPARING`, `READY`, `STARTING` or `RUNNING`) **and** at least one
`NEW`/`PENDING` task carries a scheduler error. Terminal rows do not suppress it,
so a service scaled to 0, re-constrained and scaled up again is diagnosed even
though its old `SHUTDOWN` rows are still listed. `task ls` prints the same line,
naming the constraints of every service in the listing; `task ls --health` does
not, because that table is built from the API's task list alone.

##### Registry authentication

`--with-registry-auth` makes the **CLI** resolve credentials for the service's
`--image` and attach them to the service spec, so a task can pull a private
image on a node that has no Docker configuration of its own. The CLI dials the
manager's local socket, so in a normal install that is the manager itself.
Credentials are read from the standard Docker config (`~/.docker/config.json`,
including credential helpers) — the file `docker login <registry>` writes. There
is **no** `swarmcracker login` command; the CLI only reads that config. See
[Registry Credentials](/guides/configuration/#registry-credentials) for where
the credentials are read from, and what runs where.

```bash
# Once, on the manager, as the user that runs swarmcracker (often root):
sudo docker login registry.example.com

# The worker needs no login and no local Docker config:
swarmcracker service create \
    --name api \
    --image registry.example.com/team/api:1.4.0 \
    --with-registry-auth
```

If no credential can be found for the image's registry, `service create` fails
**before any request is sent**, and `service update` fails **before the update
request is sent** — it first reads the service to decide whether it is golden,
so the `GetService` lookup does run. Nothing is created or modified in either
case, and the error names the registry and the remedy:

```text
--with-registry-auth: no registry credentials found for registry.example.com: run `docker login registry.example.com`
```

The flag is **rejected** with `--golden` (golden images are booted from a local
sealed image, not pulled from a registry), and `service update
--with-registry-auth` is rejected on a golden service for the same reason.

On `service update` the flag is tri-state, and the middle row is the one to
watch:

| Invocation | Effect on the stored credential |
|------------|---------------------------------|
| flag omitted | **Preserved** — the service keeps the credential it already carries |
| `--with-registry-auth` | Re-resolved for `--image`, which is therefore **required** (`--with-registry-auth requires --image`) |
| `--with-registry-auth=false` | **Cleared** — the spec stops carrying any credential |

`--rollback` **restores the previous spec's** credential and ignores the other
flags, so `--rollback` combined with `--with-registry-auth` is rejected.

> **Changing the registry without the flag.** `service update --image` does
> **not** re-resolve the credential. An update that names a new image but omits
> `--with-registry-auth` keeps the payload already on the spec — so pointing a
> service at an image in a **different** registry carries the **previous**
> registry's credential forward, and the next pull fails with a `401` on the
> node. Always pass `--with-registry-auth --image <ref>` when the new image lives
> in a different registry, or `--with-registry-auth=false` to drop the old one.

**Redaction.** The stored payload is never printed. The JSON renderings —
`service inspect` (its default output), `service ls --format json`, `service ps
--format json`, `task inspect`, `task ls --format json` and the equivalent
`swarmctl` output — replace the credential with `***` (masked on a copy of the
spec, so the response the command keeps using is untouched), so an operator can
see that a service carries credentials without seeing what they are. The
plain-text renderings (`service inspect` with a non-JSON `--format`, the tables)
omit the credential field entirely. The payload is also kept out of logs and
metrics.

#### `swarmcracker task`

Task management.

| Subcommand | Description |
|------------|-------------|
| `ls` | List tasks |
| `inspect <task-id>` | Inspect a task |

`task ls` flags: `-o` / `--output`, `--all`, `--filter`, `--format`, `--no-trunc`, `--node`, `--service`, `--quiet` / `-q`, `--health-port`, `--no-health`, `--health`.

`-o` selects the output mode, and `--service` accepts a service ID or, in the
new modes, a service name. `-o json` emits the v1 `TaskList` envelope
(`schemaVersion`, `kind`, `items`, `next`), and `-o name` emits one task ID per
line. Phase 0 omits `health` from the v1 envelope; the legacy table and
`--format json` keep their existing health fields. Empty results stay parseable
in JSON: `items: []` with `-o json` and `[]` with `--format json`; table mode
still prints `No tasks found`. `-o` and `--format` must not be combined in the
same invocation. In the new modes `--filter state=` is case-insensitive; the
legacy path keeps its original comparison.

`--health` reads live health through the manager API (`GET /v1/tasks?health=true`)
instead of each node's health server, so it requires `-H` / `--host`. It is
rejected with `--no-health`, with a changed `--health-port`, and with the legacy
`--format` flag. With `-o json` it emits exactly one `TaskList` document on
stdout (`health` included, and `partial` when the manager could not read a
node); `-o name` emits one task ID per line; table mode prints the same columns
as above with `HEALTH` taken from the API page. A partial page is a normal
document, not a failed request: the tasks are printed first, then the command
writes one `Error` document with code `partial` to stderr in `-o json` mode
(a message naming each silent node once in table and `-o name` modes) and exits **7**.
Pages are merged before either is printed, so a node that owns tasks on several
pages appears once in `partial` and once in that message.
A complete page exits **0**. Without `--health` every existing mode is unchanged.

`task ls` prints the same `HEALTH` column as `service ps` — live when the owning
node's health server is reachable, derived otherwise, also emitted as `health`
(and `live_health` when fetched) by `--format json`. Its rows end in the same
`ERROR` column as `service ps`: the task's `Status.Err`, empty when it carries
none and truncated at 40 characters (`task inspect` prints the full text).
`task inspect` shows a task's declared healthcheck (its test command, interval,
timeout, retries and start period) and, when a check failed, the reason the
executor recorded (for example `healthcheck failed: <cmd>: <output>`). A registry
credential the task carries is printed as `***`, never as its payload — see
[Registry authentication](#registry-authentication).

#### `swarmcracker vm`

Direct Firecracker microVM management.

| Subcommand | Description |
|------------|-------------|
| `create [image]` | Create a microVM from an OCI image or a golden image |
| `list` (alias `ls`) | List microVMs (CLI-created and daemon/service-managed) |
| `attach <vm>` | Attach to a running microVM's serial console |
| `exec <vm> -- <cmd...>` | Run a command inside a running microVM |
| `cp <src> <vm>:<dst>` | Copy a file between the host and a running microVM |
| `logs <vm-id>` | View VM logs |
| `stop <vm-id>` | Stop a microVM |
| `snapshot` | Manage VM snapshots (`create`, `restore`, `stop-restored`, `list`, `delete`, `cleanup`). `restore` boots a VM from a snapshot and registers it (manage it with `vm stop` / `vm list`); `stop-restored <task>` stops the VM a restore started (SIGTERM, then SIGKILL after 10 s, socket removed), locally or through the manager with `-H` |
| `status <vm-id>` | Show detailed VM status |

**`vm create` flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--name` | `-n` | auto | VM name |
| `--cpu` | — | `1` | vCPUs |
| `--memory` | `-m` | `512` | Memory (MB) |
| `--balloon-mib` | — | — | Memory-balloon target in MiB (positive integer, less than `--memory`; see [Memory Balloon](/guides/advanced/#memory-balloon)) |
| `--cpuset` | — | — | Pin the VM to these host CPUs (Linux cpulist: `0`, `0-3`, `0-3,8`); the host must have those CPUs online (see [CPU Pinning and cgroup Limits](/guides/advanced/#cpu-pinning-and-cgroup-limits)) |
| `--hugepages` | — | `false` | Back the guest memory with 2M huge pages; the host must have a large enough pre-allocated hugepage pool (see [Hugepages](/guides/advanced/#hugepages)) |
| `--numa-node` | — | — | Bind the VM's memory to these host NUMA nodes (Linux cpulist: `0`, `0-1`); the host must have those nodes online (see [NUMA placement](/guides/advanced/#numa-placement)) |
| `--network` | — | — | Network to attach |
| `--detach` | `-d` | `false` | Detached mode |
| `--env` | `-e` | — | Environment variables |
| `--golden` | — | — | Boot a prebuilt golden image (`name` or `name@version`) instead of an OCI image |
| `--golden-dir` | — | `<rootfs-dir>/golden` | Directory containing golden image artifacts |

`vm create --balloon-mib <int>` configures a virtio-balloon target on the VM, with the same MiB units and validation as `service create --balloon-mib` (a positive integer smaller than `--memory`, rejected before anything is created). It is carried on the task as the `swarmcracker.balloon` annotation; a direct `vm create` VM does **not** report the target as a Prometheus series (see [the metric](/guides/operations/#metrics)).

`vm create --cpuset <cpulist>` pins the VM to host CPUs, with the same syntax and
canonicalisation as `service create --cpuset` (`3,1-2` becomes `1-3`). An empty
value is an error; there is no "clear" form here. A direct VM gets **no cgroup at
all unless `--cpuset` or `--numa-node` is given** (unlike a service task, which
gets one whenever the daemon's cgroups are enabled), and the requested CPUs must
be online on the host running the command or the VM does not start. See
[CPU Pinning and cgroup Limits](/guides/advanced/#cpu-pinning-and-cgroup-limits).

`vm create --hugepages` and `--numa-node` have the same semantics as their
`service create` counterparts: 2M huge pages backed by the host's pre-allocated
pool, and a memory binding to the listed host NUMA nodes through `cpuset.mems`.
They are carried on the task as the `swarmcracker.hugepages` and
`swarmcracker.numa-node` annotations; the VM runs on the host that executes the
command, so the pool and the nodes must exist **there**. An empty
`--numa-node ""` is an error (there is no existing spec to clear). The node
checks the pool and the node list before starting the VMM: a request the host
cannot satisfy leaves **no VM and no cgroup** behind. See
[Hugepages](/guides/advanced/#hugepages) and
[NUMA placement](/guides/advanced/#numa-placement).

`vm create --cpu`/`--memory` size the VM directly (the defaults above are applied
when the flags are omitted), and a declared size is honoured **exactly** — no
512 MiB floor — so `--memory 256` boots a 256 MiB VM.

`vm list` (alias `vm ls`) flags: `--all`, `--format` (`table` or `json`; any other value is rejected), `--socket-dir`. `vm logs` flags: `--follow` / `-f`, `--since` (a duration like `1h`/`30m` or an RFC3339/date timestamp), `--tail`. Without `-H` it reads the local console file; with `-H` it reads the task through the manager API, which relays to the node that owns it, so a task on another node is readable too (the API serves at most 10000 lines per request, which is what `--tail -1` asks for). `vm stop` flags: `--force` / `-f`, `--timeout`. `vm attach` flags: `--socket-dir` (default `/var/run/firecracker`); `<vm>` is a task ID or any unique prefix, and the global `-H` switches the session to the manager's API (see below). Detach with **Ctrl-P Ctrl-Q**. `vm exec` flags: `--socket-dir` (default `/var/run/firecracker`), `-i`/`--interactive`, `-t`/`--tty`, `-e`/`--env` (`K=V`, repeatable), `-w`/`--workdir`, `--exit-code` (`guest`, the default, passes the guest status through; `api` maps any non-zero guest exit to `8`; a signal stays `128+signal`); everything after `--` is the guest command, and `-i` without `-t` only emits a warning. `vm cp` flags: `--socket-dir`, `--mode` (host→guest destination mode, default `0644`); exactly one argument carries the `<vm>:` prefix and only regular files are supported.

`vm exec` and `vm cp` reach a guest agent over Firecracker's vsock device (no extra host port is opened). The channel is a trusted control path — a host-local process can run commands in the guest **as root** — so access is limited to the daemon-owned socket directory, like `vm attach`. See [Guest Access](/guides/guest-access/) for usage, the guest agent, and limitations.

`vm exec -H https://manager:4244` (with `--ca` and `--token-file`) uses the manager's API (`POST /v1/tasks/{id}/exec`) instead of the local socket, so a CI runner needs only an API token and the cluster CA. That path requires the node broker on each node (`swarmd-firecracker --broker-listen`, default `0.0.0.0:4245`); a manager without a wired broker answers `501 not_implemented`. Interactive `-t` is not implemented over the API and returns the same typed 501.

`vm attach -H https://manager:4244` (with `--ca` and `--token-file`) streams the
serial console over the manager's API (`POST /v1/tasks/{id}/attach`) instead of
dialing the node's Unix socket, so an operator can reach a task's console from
any machine with the cluster CA and a token holding `tasks:exec`. The session is
one full-duplex HTTP/1.1 request: the chunked request body is your stdin and the
chunked response body is the console, both raw `application/octet-stream` and
flushed as bytes arrive. Ctrl-P Ctrl-Q detaches by closing the request body and
cancelling the request — the VM keeps running and the command exits `0`; the
console reaching EOF ends the session cleanly too. The terminal is put into raw
mode only when stdin is a terminal (a pipe or redirected stdin is forwarded
as-is). This path needs the same node broker as `vm exec`: without it the
manager answers `501 not_implemented` with `details.capability: "attach"`, and a
task that is not running or has no console answers `404 not_found` with
`details.reason: "console_unavailable"`. Because it relies on HTTP/1.1 full
duplex, the route does not work over HTTP/2. Without `-H`, `vm attach` is
unchanged and uses `--socket-dir` on the node that hosts the task.

**`vm snapshot stop-restored <task>`** stops the VM a snapshot restore started. A restore runs outside SwarmKit, so `vm stop` cannot reach it. Locally the command reads `<socket-dir>/<task>-restored.sock` (default `/var/run/firecracker`), resolves the Firecracker PID, sends SIGTERM, waits up to ten seconds, then SIGKILLs it and removes the socket; `<task>` may be an exact ID or any unique prefix. With `-H` it sends `DELETE /v1/tasks/{id}/restored` to the manager, which relays the stop to the node that holds the restored VM; the route needs the node's experimental snapshot capability and answers `404 not_found` with `details.reason: "no_restored_vm"` when nothing is tracked there.

`vm snapshot create`, `restore`, `list`, `delete` and `cleanup` are **local** commands: they talk to the node's Firecracker API socket or the snapshot directory directly, so they do not accept `-H` (only `stop-restored` does). `create` and `restore` also copy the whole task disk, so each takes a `--timeout` (default `5m`) that bounds the entire operation — snapshot plus copy. A deadline that fires there is an ordinary error, never `504`.

`--snapshot-timeout` is a `swarmd-firecracker` flag (default `5m`), not a flag on `vm snapshot`. It bounds one snapshot request the **manager** relays to a node over the API — create, restore, delete, list and stop-restored — and only on that path does exceeding it answer `504 timeout`. Size whichever deadline applies above your slowest disk copy: `--timeout` on a local `vm snapshot create` / `restore`, `--snapshot-timeout` on the daemon.

The daemon's tracing flags are also `swarmd-firecracker` flags: `--tracing-enabled` (off by default), `--tracing-endpoint` (OTLP/HTTP base URL, e.g. `http://127.0.0.1:4318`), `--tracing-service-name` (default `swarmd-firecracker`) and `--tracing-sample-ratio` (default `1`, meaning every trace). They configure trace export only; there is no equivalent key in the manager's YAML config. See [Operations § Tracing](/guides/operations/#tracing).

`vm list` and `vm status` also cover microVMs started by the daemon for services (discovered from `<socket-dir>/*.sock`; stale sockets are filtered with a liveness probe). For those VMs the guest IP comes from a `<task-id>.net.json` file the daemon writes next to the socket, and is shown by `vm status` and `vm list --format json` (the table has no IP column). The same file carries the daemon's **live healthcheck verdict**: `vm status` prints a `[Health]` section (state, consecutive failures, the check command, the last output, when it was observed) and `vm list` shows it in its `HEALTH` column (and as `health` in `--format json`). The states are `starting`, `healthy`, `failing` (a probe is failing but the retries threshold is not reached — the task is still running) and `unhealthy`. Because the file lives next to the VM socket, this is node-local: run the command on the node hosting the task. A task with no healthcheck, and a VM created directly with `vm create`, show `-`. `vm stop` deliberately refuses to kill a service VM — use `swarmcracker service scale <service> 0` or `swarmcracker service rm <service>` so SwarmKit updates the desired state instead of recreating the task.

#### `swarmcracker image`

Build and inspect golden VM images from recipes.

| Subcommand | Description |
|------------|-------------|
| `build <recipe.yaml>` | Build a golden image from a recipe |
| `list` | List built golden images |
| `inspect <name@version \| path.json>` | Show metadata for a built golden image |

**`image build` flags:** `--output-dir` (default `<rootfs-dir>/golden`, else `/var/lib/firecracker/golden`), `--force` (rebuild even if an up-to-date artifact exists).
**`image list` / `image inspect` flags:** `--output-dir` (same default).

`image build` reads a recipe (see `recipes/*.yaml`) and writes a sealed, versioned artifact plus `golden-*.json` metadata to the output directory. `image list` reads that metadata; `image inspect` accepts either `<name>@<version>` (resolved under `--output-dir`) or a direct path to a `.json` metadata file.

Golden images can be booted with `swarmcracker vm create --golden` or `swarmcracker service create --golden`. See the [Golden Images guide](/guides/golden-images/).

#### `swarmcracker network`

Manage user-defined networks and inspect host networking.

| Subcommand | Description |
|------------|-------------|
| `create --name <n> [--subnet <cidr>] [--driver overlay\|bridge]` | Create an isolated network with its own subnet |
| `ls` / `list` | List user-defined networks |
| `inspect <name\|id>` | Show a network's allocated subnet, gateway, bridge, and VNI |
| `rm <name\|id>` | Remove a network (blocked while services are attached) |
| `bridge status` | Bridge state, addresses, and attached interfaces |
| `vxlan ls` / `vxlan status` | VXLAN interfaces and peers |

Each user-defined network is allocated its own subnet, a per-node bridge
(`br-<name>`) and an overlay VXLAN ID. Services attached with
`service create --network <name>` get their VM IP from that network's subnet and
are **isolated** from services on other networks. `--network` is repeatable: a
service attached to several networks gets one NIC (and IP) per network, and
same-network reachability holds **across nodes** (each network has its own
per-network VXLAN fanned out to the other nodes). `network rm` fails with a clear
error while any service is still attached; removing the last attached service
tears down that node's bridge and VXLAN. `network bridge status` flags:
`--bridge` (default `swarm-br0`), `--format` (`table`, `json`). `network ls` and
`network inspect` flags: `--format` (`table`/`json`, `inspect` defaults to `json`).

`network ls` also accepts the global `-o json` / `-o name` flags. The v1
`NetworkList` contains user-defined networks only (the built-in `ingress` and
`docker_gwbridge` networks are excluded, matching the table) with identity,
driver, first IPAM pool, labels and attachability. `-o name` prints IDs.
`--format json` keeps the legacy raw network array.

#### `swarmcracker volume`

Persistent volume management.

| Subcommand | Description |
|------------|-------------|
| `create <name>` | Create a volume |
| `ls` | List volumes |
| `inspect <name>` | Inspect a volume |
| `rm <name>` | Delete a volume |
| `snapshot <name>` | Snapshot a volume |
| `restore <name> --snapshot <file>` | Restore a volume from a snapshot |
| `export <name> --output-file <file>` | Export volume data |
| `import <name> <archive>` | Import volume data |

`volume create` flags: `--type` / `-t` (`dir` or `block`), `--size` / `-s` (MB), `--opt`. The `--volumes-dir` / `-d` global flag sets the storage directory (default `/var/lib/swarmcracker/volumes`).

`volume rm` requires `--force` / `-f` to confirm. `volume restore` requires `--snapshot` / `-s <file>`; `volume export` writes to `--output-file` / `-O <file>` (default `<name>.tar.gz`; the old `-o` shorthand is now reserved for the global output format); `volume ls --json` emits JSON.

#### `swarmcracker asset`

Firecracker asset management.

| Subcommand | Description |
|------------|-------------|
| `kernel` | Kernels (`ls`, `verify`) |
| `rootfs` | Rootfs images (`ls`) |

#### `swarmcracker config`

Manage SwarmKit configs.

| Subcommand | Description |
|------------|-------------|
| `create <name> <file>` | Create a config from a file (`-` reads stdin) |
| `ls` / `list` | List configs |
| `inspect <name\|id>` | Inspect a config (the value is never shown) |
| `rm <name\|id>` | Remove a config (blocked while a service uses it) |
| `file ls` | List daemon configuration files |
| `file validate [path]` | Validate a daemon config file (defaults to `--config` / the default path) |
| `file migrate` | Migrate the daemon config to the latest schema version |

#### `swarmcracker secret`

Manage SwarmKit secrets.

| Subcommand | Description |
|------------|-------------|
| `create <name> <file>` | Create a secret from a file (`-` reads stdin) |
| `ls` / `list` | List secrets |
| `inspect <name\|id>` | Inspect a secret (the value is never shown) |
| `rm <name\|id>` | Remove a secret (blocked while a service uses it) |

`config ls` and `secret ls` also accept the global `-o json` / `-o name` flags.
The v1 documents carry metadata only (`id`, `name`, `revision`, timestamps,
labels, and `driver` for secrets); payloads never appear. `--format json` keeps
the legacy list shape, and `--quiet` is replaced by `-o name` on the v1 path.

Secret and config values are injected into the microVM's rootfs at the path a
service requests with `service create --secret` / `--config`. SwarmKit redacts
secret values from the control API, so they never appear in `inspect`.

#### `swarmcracker setup`

One-time node setup.

| Subcommand | Description |
|------------|-------------|
| `check` | Verify prerequisites (KVM, kernel modules, tools) |
| `install` | Download and install Firecracker, jailer, kernel, rootfs, CNI plugins |
| `network` | Create the VM bridge and enable NAT |
| `config` | Generate the configuration file |

**`setup install` flags:** `--download-kernel`, `--download-rootfs`, `--download-cni`, `--firecracker-version` (default `v1.15.1`).
**`setup network` flags:** `--bridge`, `--bridge-ip`, `--subnet`, `--nat`.
**`setup config` flags:** `--kernel`, `--rootfs-dir`, `--bridge`, `--bridge-ip`, `--subnet`, `--vcpus`, `--memory`, `--non-interactive`.

#### `swarmcracker doctor`

System and cluster diagnostics.

```bash
swarmcracker doctor            # human-readable report
swarmcracker doctor --json     # machine-readable
swarmcracker doctor --verbose  # detailed output
```

#### `swarmcracker metrics`

Show CPU, memory, and network usage for running microVMs. Includes VMs started
by the daemon for services, not just CLI-created ones.

```bash
swarmcracker metrics                 # table
swarmcracker metrics --format json
swarmcracker metrics --task <vm-id|prefix>
swarmcracker metrics --refresh 5     # watch mode
```

---

## swarmctl

Lightweight control-socket client for debugging and manual inspection. It talks
directly to the SwarmKit control socket (default `/var/run/swarmkit/swarm.sock`,
override with `SWARM_SOCKET`; state directory default `/var/lib/swarmkit`,
override with `SWARM_STATE_DIR`), and must run on a manager node (typically as
root).

```bash
# Services
swarmctl ls-services
swarmctl create-service nginx:alpine --name web --replicas 2
swarmctl scale <service-id> 3
swarmctl update <service-id> --image nginx:1.25-alpine
swarmctl inspect <service-id|task-id>
swarmctl rm-service <service-id>

# Networks
swarmctl create-network <name> --subnet 10.0.9.0/24
swarmctl ls-networks

# Nodes
swarmctl ls-nodes
swarmctl drain <node-id>
swarmctl activate <node-id>
swarmctl pause-node <node-id>
swarmctl promote <node-id>
swarmctl demote <node-id>

# Tasks
swarmctl ls-tasks
swarmctl logs <task-id> --lines 100
swarmctl metrics <task-id>
swarmctl stop-task <task-id>

# Volumes
swarmctl volume create <name> --size 512
swarmctl volume list
swarmctl volume inspect <name>
swarmctl volume rm <name>

# Snapshots
swarmctl snapshot create <task-id> <snapshot-name>
swarmctl snapshot list
swarmctl snapshot restore <snapshot-name>
swarmctl snapshot rm <snapshot-name>
```

Anything that serializes a service or task (`inspect`, `ls-services`,
`ls-tasks`) replaces a registry credential with `***`, the same redaction as the
main CLI.

---

## Deprecated Commands

The following legacy aliases still run (with a deprecation warning) and will be
removed in a future release:

| Legacy | Use Instead |
|--------|-------------|
| `swarmcracker init` | `swarmcracker cluster init` |
| `swarmcracker join` | `swarmcracker cluster join` |
| `swarmcracker leave` | `swarmcracker cluster leave` |
| `swarmcracker deinit` | `swarmcracker cluster deinit` |
| `swarmcracker reset` | `swarmcracker cluster reset` |
| `swarmcracker status` | `swarmcracker vm status` |
| `swarmcracker run` | `swarmcracker vm create` |
| `swarmcracker list` | `swarmcracker vm list` |
| `swarmcracker logs` | `swarmcracker vm logs` |
| `swarmcracker stop` | `swarmcracker vm stop` |
| `swarmcracker snapshot` | `swarmcracker vm snapshot` |
| `swarmcracker deploy` | `swarmcracker service create` (stub — see below) |
| `swarmcracker validate` | `swarmcracker config file validate` (stub — see below) |

`swarmcracker deploy` and `swarmcracker validate` are stubs: they print a
deprecation warning and then fail, so use the replacement commands instead.
`swarmcracker metrics` is a normal command and is not deprecated.

---

## Examples

### Initialize a Cluster

```bash
sudo swarmcracker cluster init \
    --advertise-addr 192.168.121.155:4242 \
    --vxlan-enabled \
    --vxlan-peers 192.168.121.129,192.168.121.43
```

### Get a Join Token

```bash
sudo swarmcracker cluster token worker
```

### Join a Worker

```bash
sudo swarmcracker cluster join 192.168.121.155:4242 \
    --token SWMTKN-1-xxxxx \
    --advertise-addr 192.168.121.129:4242
```

### Deploy a Service

```bash
swarmcracker service create \
    --name nginx \
    --image nginx:alpine \
    --replicas 2 \
    --cpu 0.5 \
    --memory 256M
```

### Create a VM Directly

```bash
sudo swarmcracker vm create \
    --name dev-vm \
    --cpu 2 \
    --memory 1024 \
    --detach \
    -e KEY=value \
    alpine:latest
```

### Check Cluster Health

```bash
sudo swarmcracker cluster health
sudo swarmcracker node ls
sudo swarmcracker doctor
```

---

## Configuration File

Default location: `/etc/swarmcracker/config.yaml` (override with `--config` /
`-c` or the `SWARMCRACKER_CONFIG` environment variable).

```yaml
version: 1
executor:
  kernel_path: /usr/share/firecracker/vmlinux
  rootfs_dir: /var/lib/firecracker/rootfs
network:
  bridge_name: swarm-br0
```

The [Configuration Guide](/guides/configuration/) is the canonical reference
for every key, its default, and whether it is implemented.

---

## See Also

- [Configuration Guide](/guides/configuration/)
- [API Reference](/reference/api/)
- [Network Reference](/contributing/reference/network/)
- [Architecture Overview](/architecture/)
