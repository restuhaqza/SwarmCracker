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
| `--kernel` | — | — | Override kernel path |
| `--rootfs-dir` | — | — | Override rootfs directory |
| `--ssh-key` | — | — | SSH private key for remote deployment |
| `--known-hosts` | — | — | SSH known_hosts file |
| `--insecure-ssh` | — | `false` | Skip SSH host key verification |

### Commands

#### `swarmcracker cluster`

Cluster lifecycle management.

| Subcommand | Description |
|------------|-------------|
| `init` | Initialize a new cluster |
| `join` | Join an existing cluster |
| `leave` | Leave the cluster |
| `status` | Show cluster status |
| `health` | Run cluster health checks |

**`cluster init` flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--advertise-addr` | auto-detect | Address to advertise |
| `--listen-addr` | `0.0.0.0:4242` | Listen address |
| `--state-dir` | `/var/lib/swarmkit` | State directory |
| `--config-dir` | `/etc/swarmcracker` | Config directory |
| `--kernel` | `/usr/share/firecracker/vmlinux` | Firecracker kernel |
| `--rootfs-dir` | `/var/lib/firecracker/rootfs` | Rootfs directory |
| `--socket-dir` | `/var/run/firecracker` | Socket directory |
| `--vcpus` | `1` | Default vCPUs |
| `--memory` | `512` | Default memory (MB) |
| `--bridge-name` | `swarm-br0` | Bridge name |
| `--subnet` | `192.168.127.0/24` | VM subnet |
| `--bridge-ip` | `192.168.127.1/24` | Bridge IP |
| `--vxlan-enabled` | `false` | Enable VXLAN overlay |
| `--vxlan-peers` | — | VXLAN peer IPs (comma-separated) |
| `--debug` | `false` | Debug logging |
| `--force` | `false` | Force init |

**`cluster join` flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--token` | (required) | Join token |
| `--addr` | (required) | Manager address |

**`cluster leave` flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--purge` | `false` | Remove all state and config |
| `--force` | `false` | Force leave |
| `--keep-network` | `false` | Keep bridge/TAP devices |
| `--state-dir` | `/var/lib/swarmkit` | State directory |

#### `swarmcracker node`

Node management.

| Subcommand | Description |
|------------|-------------|
| `ls` | List nodes |
| `inspect` | Inspect a node |
| `update` | Update node labels/spec |

#### `swarmcracker service`

Service (deployment) management.

| Subcommand | Description |
|------------|-------------|
| `create` | Create a new service |
| `ls` | List services |
| `inspect` | Inspect a service |
| `update` | Update a service |
| `rm` | Remove a service |

**`service create` flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | (required) | Service name |
| `--image` | (required) | Container image |
| `--replicas` | `1` | Number of replicas |
| `--cpu` | — | CPU limit (e.g. `0.5`, `1.0`) |
| `--memory` | — | Memory limit (e.g. `256m`, `1g`) |
| `--env` | — | Environment variables |
| `--network` | — | Network to attach |
| `--port` | — | Published ports |

#### `swarmcracker task`

Task management.

| Subcommand | Description |
|------------|-------------|
| `ls` | List tasks |
| `inspect` | Inspect a task |
| `logs` | View task logs |

**`task ls` flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `table` | Output format (`table`, `json`) |
| `--filter` | — | Filter (e.g. `state=running`) |
| `--quiet` / `-q` | `false` | IDs only |
| `--all` | `false` | Include stopped |
| `--no-trunc` | `false` | Don't truncate |
| `--node` | — | Filter by node |
| `--service` | — | Filter by service |

#### `swarmcracker vm`

Direct Firecracker microVM management.

| Subcommand | Description |
|------------|-------------|
| `create` | Create a microVM |
| `ls` | List microVMs |
| `stop` | Stop a microVM |
| `logs` | View VM logs |

**`vm create` flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--name` | `-n` | auto | VM name |
| `--cpu` | — | `1` | vCPUs |
| `--memory` | `-m` | `512` | Memory (MB) |
| `--network` | — | — | Network to attach |
| `--detach` | `-d` | `false` | Detached mode |
| `--env` | `-e` | — | Environment variables |

#### `swarmcracker network`

Network management.

| Subcommand | Description |
|------------|-------------|
| `create` | Create overlay network |
| `ls` | List networks |
| `inspect` | Inspect network |
| `rm` | Remove network |

#### `swarmcracker volume`

Volume management.

| Subcommand | Description |
|------------|-------------|
| `create` | Create a volume |
| `ls` | List volumes |
| `inspect` | Inspect a volume |
| `rm` | Remove a volume |

#### `swarmcracker config`

Configuration management.

| Subcommand | Description |
|------------|-------------|
| `show` | Show current configuration |
| `validate` | Validate configuration file |
| `migrate` | Migrate config between versions |

#### `swarmcracker setup`

One-time setup and diagnostics.

| Subcommand | Description |
|------------|-------------|
| `network` | Set up networking (bridge, forwarding) |
| `preflight` | Run pre-flight checks |
| `doctor` | System diagnostics |

#### `swarmcracker asset`

Rootfs image management.

| Subcommand | Description |
|------------|-------------|
| `pull` | Pull/prepare container image |
| `ls` | List cached rootfs images |

---

## swarmctl

Lightweight CLI for debugging and manual task inspection. Connects directly to the SwarmKit control socket.

```bash
# List tasks
swarmctl --state-dir /var/lib/swarmkit task ls

# Inspect a task
swarmctl --state-dir /var/lib/swarmkit task inspect <task-id>

# Volume operations
swarmctl volume ls
swarmctl volume create <name> --size 1G

# Snapshot operations
swarmctl snapshot ls <task-id>
swarmctl snapshot create <task-id>
swarmctl snapshot restore <task-id> <snapshot-id>
```

---

## Deprecated Commands

The following legacy commands still work but print deprecation warnings:

| Legacy | Use Instead |
|--------|-------------|
| `swarmcracker init` | `swarmcracker cluster init` |
| `swarmcracker join` | `swarmcracker cluster join` |
| `swarmcracker leave` | `swarmcracker cluster leave` |
| `swarmcracker deploy` | `swarmcracker service create` |
| `swarmcracker run` | `swarmcracker vm create` |
| `swarmcracker list` | `swarmcracker vm ls` |
| `swarmcracker stop` | `swarmcracker vm stop` |
| `swarmcracker logs` | `swarmcracker vm logs` |
| `swarmcracker status` | `swarmcracker cluster health` |
| `swarmcracker metrics` | `swarmcracker node inspect` |
| `swarmcracker validate` | `swarmcracker config validate` |
| `swarmcracker deinit` | `swarmcracker cluster leave --purge` |
| `swarmcracker reset` | `swarmcracker cluster leave --purge --force` |

---

## Examples

### Initialize a Cluster

```bash
swarmcracker cluster init \
    --advertise-addr 192.168.121.155 \
    --listen-addr 0.0.0.0:4242 \
    --vxlan-enabled \
    --vxlan-peers 192.168.121.129,192.168.121.43
```

### Join a Worker

```bash
swarmcracker cluster join \
    --addr 192.168.121.155:4242 \
    --token SWMTKN-1-xxxxx
```

### Deploy a Service

```bash
swarmcracker service create \
    --name nginx \
    --image nginx:alpine \
    --replicas 2 \
    --cpu 0.5 \
    --memory 256m
```

### Create a VM Directly

```bash
swarmcracker vm create \
    --name dev-vm \
    --cpu 2 \
    --memory 1024 \
    --detach \
    -e KEY=value \
    alpine:latest
```

### Check Cluster Health

```bash
swarmcracker cluster health
swarmcracker doctor
swarmcracker setup preflight
```

---

## Configuration File

See [Configuration Guide](../user/guides/configuration.md) for full `config.yaml` reference.

Default location: `/etc/swarmcracker/config.yaml`

```yaml
version: 1
kernel_path: /usr/share/firecracker/vmlinux
socket_dir: /var/run/firecracker
rootfs_dir: /var/lib/firecracker/rootfs
bridge_name: swarm-br0
subnet: 192.168.127.0/24
bridge_ip: 192.168.127.1/24
default_config:
  vcpus: 1
  memory: 512
vxlan_enabled: false
snapshot:
  enabled: true
  dir: /var/lib/swarmcracker/snapshots
  max_snapshots: 5
  max_age: 72h
```

---

## See Also

- [Configuration Guide](../user/guides/configuration.md)
- [API Reference](../dev/reference/api.md)
- [Network Architecture](../dev/reference/network.md)
- [Architecture Overview](../architecture/overview.md)
