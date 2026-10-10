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
| `--version` | `-v` | — | Print version information |
| `--help` | `-h` | — | Help for any command |

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
| `rm <node-id>` | Remove a node |

`node ls` flags: `--filter`, `--format` (`table`, `json`), `--quiet` / `-q`.
`node inspect` flags: `--format`, `--pretty`.

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
| `--replicas` | `1` | Number of replicas |
| `--cpu` | — | CPU limit (cores, e.g. `1.5`) |
| `--memory` | — | Memory limit (e.g. `512M`, `1G`) |
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
| `--dns` | — | Guest DNS nameserver, an IP address (repeatable) |
| `--network` | — | Attach the service to a user-defined network (repeatable; one network per service for now) |
| `--secret` | — | Grant access to a secret: `[src=]NAME[,target=PATH][,mode=0400][,uid=N][,gid=N]` (repeatable) |
| `--config` | — | Grant access to a config: `[src=]NAME[,target=PATH][,mode=0444][,uid=N][,gid=N]` (repeatable) |
| `--user` | — | **Not supported** — rejected with a clear error |
| `--cap-add` | — | **Not supported** — rejected with a clear error |
| `--cap-drop` | — | **Not supported** — rejected with a clear error |
| `--read-only` | — | **Not supported** — rejected with a clear error |

`--mount` / `--volume` attach data into the guest. A **named volume**
(`type=volume,source=myvol` or `myvol:/data`) is a volume created with
`swarmcracker volume create`; a **bind** mount (`type=bind,source=/host/path` or
`/host/path:/data`) copies a host path from the node running the task. Mounts
are materialized into a **private, per-task copy of the rootfs** at start time
(so the shared image cache is never modified and mounts never leak between
tasks), which means the content is a snapshot taken when the task starts — the
data is not live-shared with the host. Read-only mounts (`:ro` / `readonly`) are
not written back to the volume when the task stops.

`--mode global` runs one task per node (SwarmKit schedules it) and ignores
`--replicas`. `--constraint` and `--placement-pref` are enforced by the SwarmKit
scheduler (`node.hostname`, `node.role`, `node.labels.*`, …). `--restart-*` and
`--update-*` / `--rollback-*` are enforced by SwarmKit's restart and
rolling-update orchestration.

`--hostname` and `--dns` are honored: the value travels to the executor and the
guest init applies it at boot (the VM's hostname and `/etc/resolv.conf`).
Because a SwarmCracker workload is a whole microVM rather than a process sharing
a kernel, the container-execution flags `--user`, `--cap-add`, `--cap-drop` and
`--read-only` cannot be enforced — the CLI **rejects them with a clear error** at
create time instead of accepting and silently ignoring them. `--hostname` and
`--dns` are also rejected with `--golden`, whose images boot their own init.

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

`--image` and `--golden` are mutually exclusive. With `--golden`, the reference
is recorded as the `swarmcracker.golden` service label and no OCI image is pulled.

**`service update` flags:** `--image`, `--replicas`, `--cpu-limit`, `--memory-limit`, `--env-add`, `--env-rm`, `--publish-mode`, `--mount`, `--volume` / `-v` (replace the mounts), `--network`, `--secret`, `--config`, `--hostname`, `--dns`, `--rollback`, `--force` / `-f`, and the scheduling/lifecycle flags from `service create` (`--mode`, `--constraint`, `--placement-pref`, `--restart-*`, `--update-*`, `--rollback-*`). The rejected execution flags (`--user`, `--cap-add`, `--cap-drop`, `--read-only`) are also accepted here so they fail with the same clear error. `--mode` is accepted but a **change** is rejected (SwarmKit does not allow it); recreate the service to change its mode.

`--replicas` on `service update` applies only when the flag is present: passing
`--replicas 0` scales to zero, while omitting it leaves the replica count
unchanged. `service scale <service> 0` always scales to zero.
`--publish-mode ingress|host` on `service update` changes the mode of the
service's existing published ports (it errors if the service has none).
`service inspect` prints the published ports and, for ingress services, the
service VIP.
`service ls` / `service ps` flags: `--filter`, `--format`, `--quiet` / `-q`, and `--no-trunc` for `ps`.

#### `swarmcracker task`

Task management.

| Subcommand | Description |
|------------|-------------|
| `ls` | List tasks |
| `inspect <task-id>` | Inspect a task |

`task ls` flags: `--all`, `--filter`, `--format`, `--no-trunc`, `--node`, `--service`, `--quiet` / `-q`.

#### `swarmcracker vm`

Direct Firecracker microVM management.

| Subcommand | Description |
|------------|-------------|
| `create [image]` | Create a microVM from an OCI image or a golden image |
| `list` (alias `ls`) | List microVMs (CLI-created and daemon/service-managed) |
| `attach <vm>` | Attach to a running microVM's serial console |
| `logs <vm-id>` | View VM logs |
| `stop <vm-id>` | Stop a microVM |
| `snapshot` | Manage VM snapshots (`create`, `restore`, `list`, `delete`, `cleanup`). `restore` boots a VM from a snapshot and registers it (manage it with `vm stop` / `vm list`) |
| `status <vm-id>` | Show detailed VM status |

**`vm create` flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--name` | `-n` | auto | VM name |
| `--cpu` | — | `1` | vCPUs |
| `--memory` | `-m` | `512` | Memory (MB) |
| `--network` | — | — | Network to attach |
| `--detach` | `-d` | `false` | Detached mode |
| `--env` | `-e` | — | Environment variables |
| `--golden` | — | — | Boot a prebuilt golden image (`name` or `name@version`) instead of an OCI image |
| `--golden-dir` | — | `<rootfs-dir>/golden` | Directory containing golden image artifacts |

`vm list` (alias `vm ls`) flags: `--all`, `--format` (`table` or `json`; any other value is rejected), `--socket-dir`. `vm logs` flags: `--follow` / `-f`, `--since` (a duration like `1h`/`30m` or an RFC3339/date timestamp), `--tail`. `vm stop` flags: `--force` / `-f`, `--timeout`. `vm attach` flags: `--socket-dir` (default `/var/run/firecracker`); `<vm>` is a task ID or any unique prefix. Detach with **Ctrl-P Ctrl-Q**.

`vm list` and `vm status` also cover microVMs started by the daemon for services (discovered from `<socket-dir>/*.sock`; stale sockets are filtered with a liveness probe). For those VMs the guest IP comes from a `<task-id>.net.json` file the daemon writes next to the socket, and is shown by `vm status` and `vm list --format json` (the table has no IP column). `vm stop` deliberately refuses to kill a service VM — use `swarmcracker service scale <service> 0` or `swarmcracker service rm <service>` so SwarmKit updates the desired state instead of recreating the task.

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
are **isolated** from services on other networks. `network rm` fails with a clear
error while any service is still attached. `network bridge status` flags:
`--bridge` (default `swarm-br0`), `--format` (`table`, `json`). `network ls` and
`network inspect` flags: `--format` (`table`/`json`, `inspect` defaults to `json`).

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
| `export <name> --output <file>` | Export volume data |
| `import <name> <archive>` | Import volume data |

`volume create` flags: `--type` / `-t` (`dir` or `block`), `--size` / `-s` (MB), `--opt`. The `--volumes-dir` / `-d` global flag sets the storage directory (default `/var/lib/swarmcracker/volumes`).

`volume rm` requires `--force` / `-f` to confirm. `volume restore` requires `--snapshot` / `-s <file>`; `volume export` writes to `--output` / `-o <file>` (default `<name>.tar.gz`); `volume ls --json` emits JSON.

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
