# CLI Command Reference

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
| `status <vm-id>` | Show detailed VM status |
| `health` | Run cluster health checks |
| `token [worker\|manager]` | Display join tokens |

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

#### `swarmcracker service`

Service (replicated microVM) management.

| Subcommand | Description |
|------------|-------------|
| `create` | Create a service |
| `ls` | List services |
| `inspect <service-id>` | Inspect a service |
| `ps <service>` | List the tasks of a service |
| `update <service>` | Update a service |
| `scale <service> <replicas>` | Scale a service |
| `rm <service>` | Remove a service |

**`service create` flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | (required) | Service name |
| `--image` | (required) | Container image |
| `--replicas` | `1` | Number of replicas |
| `--cpu` | — | CPU limit (cores, e.g. `1.5`) |
| `--memory` | — | Memory limit (e.g. `512M`, `1G`) |
| `--env` / `-e` | — | Environment variables |
| `--command` | — | Override the container command |
| `--args` | — | Container arguments |
| `--label` / `-l` | — | Service labels |

**`service update` flags:** `--image`, `--replicas`, `--cpu-limit`, `--memory-limit`, `--env-add`, `--env-rm`, `--force` / `-f`.
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
| `create <image>` | Create a microVM from an OCI image |
| `list` | List microVMs |
| `logs <vm-id>` | View VM logs |
| `stop <vm-id>` | Stop a microVM |
| `snapshot` | Manage VM snapshots (`create`, `restore`, `list`, `delete`, `cleanup`) |

**`vm create` flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--name` | `-n` | auto | VM name |
| `--cpu` | — | `1` | vCPUs |
| `--memory` | `-m` | `512` | Memory (MB) |
| `--network` | — | — | Network to attach |
| `--detach` | `-d` | `false` | Detached mode |
| `--env` | `-e` | — | Environment variables |

`vm list` flags: `--all`, `--format`. `vm logs` flags: `--follow` / `-f`, `--since`, `--tail`. `vm stop` flags: `--force` / `-f`, `--timeout`.

#### `swarmcracker network`

Network management.

| Subcommand | Description |
|------------|-------------|
| `bridge` | Bridge network (`status`) |
| `vxlan` | VXLAN overlay (`ls`, `status`) |

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

#### `swarmcracker asset`

Firecracker asset management.

| Subcommand | Description |
|------------|-------------|
| `kernel` | Kernels (`ls`, `verify`) |
| `rootfs` | Rootfs images (`ls`) |

#### `swarmcracker config`

Configuration management.

| Subcommand | Description |
|------------|-------------|
| `ls` | List configuration files |
| `validate` | Validate the configuration file |
| `migrate` | Migrate configuration to the latest schema version |

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

---

## swarmctl

Lightweight control-socket client for debugging and manual inspection. It talks
directly to the SwarmKit control socket (default `/var/run/swarmkit/swarm.sock`,
override with `SWARM_SOCKET`), and must run on a manager node.

```bash
# Services
swarmctl ls-services
swarmctl create-service nginx:alpine --name web --replicas 2
swarmctl scale <service-id> 3
swarmctl update <service-id> --image nginx:1.25-alpine
swarmctl inspect <service-id|task-id>
swarmctl rm-service <service-id>

# Nodes
swarmctl ls-nodes
swarmctl drain <node-id>
swarmctl activate <node-id>
swarmctl promote <node-id>

# Tasks
swarmctl ls-tasks
swarmctl logs <task-id> --lines 100
swarmctl metrics <task-id>
swarmctl stop-task <task-id>

# Volumes and snapshots
swarmctl volume create <name>
swarmctl volume list
swarmctl snapshot create <task-id>
swarmctl snapshot list
swarmctl snapshot restore <task-id> <snapshot-id>
```

---

## Deprecated Commands

The following legacy commands still work but print a deprecation warning and
will be removed in a future release:

| Legacy | Use Instead |
|--------|-------------|
| `swarmcracker init` | `swarmcracker cluster init` |
| `swarmcracker join` | `swarmcracker cluster join` |
| `swarmcracker leave` | `swarmcracker cluster leave` |
| `swarmcracker deinit` | `swarmcracker cluster deinit` |
| `swarmcracker reset` | `swarmcracker cluster reset` |
| `swarmcracker status` | `swarmcracker cluster status` |
| `swarmcracker run` | `swarmcracker vm create` |
| `swarmcracker list` | `swarmcracker vm list` |
| `swarmcracker logs` | `swarmcracker vm logs` |
| `swarmcracker stop` | `swarmcracker vm stop` |
| `swarmcracker snapshot` | `swarmcracker vm snapshot` |
| `swarmcracker deploy` | `swarmcracker service create` |
| `swarmcracker validate` | `swarmcracker config validate` |
| `swarmcracker metrics` | `swarmcracker cluster status` |

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

See the [Configuration Guide](../guides/configuration.md) for the full
`config.yaml` reference. Default location: `/etc/swarmcracker/config.yaml`.

```yaml
version: 1

executor:
  name: firecracker
  kernel_path: /usr/share/firecracker/vmlinux
  rootfs_dir: /var/lib/firecracker/rootfs
  socket_dir: /var/run/firecracker
  default_vcpus: 1
  default_memory_mb: 512
  enable_jailer: false
  init_system: tini      # none | tini | dumb-init

network:
  bridge_name: swarm-br0
  subnet: 192.168.127.0/24
  bridge_ip: 192.168.127.1/24
  ip_mode: static
  nat_enabled: true

images:
  cache_dir: /var/cache/swarmcracker
  max_cache_size_mb: 1024

logging:
  level: info
  format: text
  output: stdout
```

---

## See Also

- [Configuration Guide](../guides/configuration.md)
- [API Reference](../../dev/reference/api.md)
- [Network Reference](../../dev/reference/network.md)
- [Architecture Overview](../../architecture/overview.md)
