# CLI Reference — SwarmCracker

> Complete reference for all `swarmcracker` and `swarmctl` commands, flags, and examples.

---

## swarmcracker (Cluster Management CLI)

The `swarmcracker` binary is the primary CLI for cluster lifecycle management: init, join, leave, deinit, and operational commands.

### Global Flags

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--config`, `-c` | `SWARMCRACKER_CONFIG` | `/etc/swarmcracker/config.yaml` | Configuration file path |
| `--state-dir`, `-d` | `SWARM_STATE_DIR` | `/var/lib/swarmcracker` | State directory |
| `--verbose`, `-v` | — | `false` | Enable verbose output |
| `--json` | — | `false` | Output in JSON format |

---

### cluster init

Initialize a new SwarmCracker cluster (manager node).

```bash
swarmcracker cluster init [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--advertise-addr` | auto-detect | Advertise address for the manager |
| `--listen-addr` | `0.0.0.0:4242` | Listen address for the SwarmKit API |
| `--force-new-cluster` | `false` | Force creation of a new cluster from existing state |

**Example:**
```bash
swarmcracker cluster init --advertise-addr 192.168.1.10:4242
```

**What it does:**
1. Generates TLS certificates (CA, server, client)
2. Creates SwarmKit state directory
3. Starts `swarmd-firecracker` as manager
4. Generates join tokens for workers

---

### cluster join

Join a worker node to an existing SwarmCracker cluster.

```bash
swarmcracker cluster join [flags] <manager-addr>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--token` | — | Join token from the manager |
| `--advertise-addr` | auto-detect | Advertise address for this worker |
| `--hostname` | hostname | Node hostname |

**Example:**
```bash
swarmcracker cluster join --token SWMTKN-1-xxx 192.168.1.10:4242
```

---

### cluster leave

Remove a node from the cluster.

```bash
swarmcracker cluster leave [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--force`, `-f` | `false` | Force leave without graceful shutdown |

**Example:**
```bash
swarmcracker cluster leave --force
```

---

### cluster token

Manage join tokens.

```bash
swarmcracker cluster token [flags]
```

| Subcommand | Description |
|------------|-------------|
| `create` | Create a new join token |
| `list` | List existing tokens |
| `revoke` | Revoke a specific token |
| `rotate` | Rotate all tokens |

**Example:**
```bash
swarmcracker cluster token create --role worker
swarmcracker cluster token list
```

---

### init (standalone)

Initialize SwarmCracker in standalone mode (no SwarmKit cluster).

```bash
swarmcracker init [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--kernel-path` | `/usr/share/firecracker/vmlinux` | Firecracker kernel image |
| `--rootfs-dir` | `/var/lib/firecracker/rootfs` | Root filesystem directory |
| `--bridge-name` | `swarm-br0` | Bridge interface name |
| `--subnet` | `192.168.127.0/24` | VM subnet |
| `--consul-address` | — | Consul address for service discovery |
| `--enable-consul` | `false` | Enable Consul discovery |

**Example:**
```bash
swarmcracker init --kernel-path /usr/share/firecracker/vmlinux
```

---

### deinit

Tear down SwarmCracker, stopping all VMs and removing infrastructure.

```bash
swarmcracker deinit [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--force`, `-f` | `false` | Force deinit without confirmation |

**Example:**
```bash
swarmcracker deinit --force
```

**What it does:**
1. Stops all running VMs
2. Stops `swarmd-firecracker` service
3. Removes bridge and network devices
4. Cleans up state directory (optional)

---

### config

Manage SwarmCracker configuration.

```bash
swarmcracker config [command] [flags]
```

| Subcommand | Description |
|------------|-------------|
| `list` | Show current effective configuration |
| `validate` | Validate configuration file |

**Example:**
```bash
swarmcracker config list
swarmcracker config validate --config /etc/swarmcracker/config.yaml
```

---

### doctor

Run health checks on the node.

```bash
swarmcracker doctor [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | Output in JSON format |

**Example:**
```bash
swarmcracker doctor
```

**Checks performed:**
- KVM availability
- Firecracker binary
- Kernel image
- Bridge existence
- dnsmasq status
- Consul connectivity (if enabled)

---

### vm

Manage Firecracker microVMs directly (bypasses SwarmKit).

```bash
swarmcracker vm [command] [flags]
```

#### vm create

Create and start a new VM.

```bash
swarmcracker vm create [flags] <image>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--cpu` | `1` | Number of vCPUs |
| `--memory`, `-m` | `512` | Memory in MB |
| `--name` | auto-generated | VM name |
| `--rootfs` | from image | Root filesystem path |
| `--kernel` | `/usr/share/firecracker/vmlinux` | Kernel image path |
| `--network` | `bridge` | Network mode (bridge, nat, none) |
| `--env`, `-e` | — | Environment variables (can repeat) |
| `--volume`, `-v` | — | Volume mounts (src:dst format) |
| `--command` | — | Override container command |

**Examples:**
```bash
# Basic nginx
swarmcracker vm create alpine --command "nginx -g 'daemon off;'"

# Custom resources
swarmcracker vm create redis -m 1024 --cpu 2

# With volumes
swarmcracker vm create postgres -v /data/pg:/var/lib/postgresql
```

#### vm list

List all running VMs.

```bash
swarmcracker vm list [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | Output in JSON format |
| `--quiet`, `-q` | `false` | Only show VM IDs |

**Example:**
```bash
swarmcracker vm list --json
```

#### vm stop

Stop a running VM.

```bash
swarmcracker vm stop [flags] <vm-id>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--force`, `-f` | `false` | Force kill (skip graceful shutdown) |
| `--timeout` | `30` | Grace period in seconds |

**Example:**
```bash
swarmcracker vm stop my-vm --timeout 10
```

#### vm logs

View VM console logs.

```bash
swarmcracker vm logs [flags] <vm-id>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--follow`, `-f` | `false` | Follow log output |
| `--since` | — | Show logs since timestamp (e.g., 5m, 1h) |
| `--tail` | `all` | Number of lines to show from the end |

**Example:**
```bash
swarmcracker vm logs --follow --tail 100 my-vm
```

---

### service

Manage SwarmKit services (deploy VMs across the cluster).

```bash
swarmcracker service [command] [flags]
```

#### service create

Create a new service (deploys VMs across workers).

```bash
swarmcracker service create [flags] <image>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | — | Service name |
| `--replicas` | `1` | Number of VM replicas |
| `--cpu` | `1` | vCPUs per VM |
| `--memory`, `-m` | `512` | Memory in MB per VM |
| `--network` | `bridge` | Network driver |
| `--env`, `-e` | — | Environment variables |
| `--volume`, `-v` | — | Volume mounts |
| `--secret` | — | Secret references |
| `--config` | — | Config references |
| `--constraint` | — | Scheduling constraints |
| `--label`, `-l` | — | Service labels |
| `--restart-condition` | `any` | Restart policy (none, on-failure, any) |
| `--restart-delay` | `5s` | Delay between restart attempts |
| `--restart-max-attempts` | `0` | Max restart attempts (0 = unlimited) |
| `--update-parallelism` | `1` | Max number of VMs updated simultaneously |
| `--update-delay` | `0s` | Delay between updates |
| `--rollback` | `false` | Rollback on update failure |
| `--port`, `-p` | — | Publish port (host:vm) |
| `--mode` | `replicated` | Service mode (replicated, global) |

**Examples:**
```bash
# Basic web service
swarmcracker service create --name web --replicas 3 -p 8080:80 nginx:alpine

# With secrets and configs
swarmcracker service create --name app \
  --secret db_password \
  --config app_config \
  -e ENV=production \
  myapp:latest

# Global mode (one VM per node)
swarmcracker service create --name agent --mode global monitoring-agent
```

#### service list

List all services.

```bash
swarmcracker service list [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | JSON output |
| `--quiet`, `-q` | `false` | Only show service IDs |

#### service inspect

Inspect a service.

```bash
swarmcracker service inspect [flags] <service-id>
```

#### service ps

List tasks (VMs) for a service.

```bash
swarmcracker service ps [flags] <service-id>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--no-trunc` | `false` | Don't truncate output |
| `--json` | `false` | JSON output |

#### service update

Update a service configuration.

```bash
swarmcracker service update [flags] <service-id>
```

| Flag | Description |
|------|-------------|
| `--replicas` | Change replica count |
| `--cpu` | Change vCPU count |
| `--memory`, `-m` | Change memory limit |
| `--image` | Update the image |
| `--env-add` | Add environment variable |
| `--env-rm` | Remove environment variable |
| `--secret-add` | Add secret |
| `--secret-rm` | Remove secret |
| `--force` | Force update even if no changes |

#### service remove

Remove a service.

```bash
swarmcracker service remove [flags] <service-id>
```

#### service logs

View logs for all VMs in a service.

```bash
swarmcracker service logs [flags] <service-id>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--follow`, `-f` | `false` | Follow log output |
| `--tail` | `all` | Number of lines to show from end |
| `--timestamps`, `-t` | `false` | Show timestamps |

---

### task

Manage SwarmKit tasks (individual VM instances).

```bash
swarmcracker task [command] [flags]
```

#### task list

List all tasks across the cluster.

```bash
swarmcracker task list [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | JSON output |
| `--filter`, `-f` | — | Filter by key=value |
| `--quiet`, `-q` | `false` | Only show task IDs |
| `--no-trunc` | `false` | Don't truncate IDs |
| `--node` | — | Filter by node name |
| `--service` | — | Filter by service ID |

**Filters:**
- `desired-state=running|shutdown|accepted`
- `node=<node-id>`
- `service=<service-id>`
- `label=<key>=<value>`

**Example:**
```bash
swarmcracker task list --filter desired-state=running --node worker-1
```

#### task inspect

Inspect a specific task (VM).

```bash
swarmcracker task inspect [flags] <task-id>
```

---

### network

Manage networking.

```bash
swarmcracker network [command] [flags]
```

#### network vxlan

Manage VXLAN peers.

```bash
swarmcracker network vxlan [command]
```

| Subcommand | Description |
|------------|-------------|
| `list` | List active VXLAN peers |
| `add` | Add a peer (IP address) |
| `remove` | Remove a peer |

**Example:**
```bash
swarmcracker network vxlan list
swarmcracker network vxlan add 192.168.1.11
```

---

### node

Manage cluster nodes.

```bash
swarmcracker node [command] [flags]
```

#### node list

List all nodes.

```bash
swarmcracker node list [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | JSON output |
| `--quiet`, `-q` | `false` | Only show node IDs |

#### node inspect

Inspect a node.

```bash
swarmcracker node inspect [flags] <node-id>
```

#### node drain

Drain a node (stop scheduling, move VMs).

```bash
swarmcracker node drain [flags] <node-id>
```

#### node activate

Activate a drained node.

```bash
swarmcracker node activate [flags] <node-id>
```

---

### snapshot

Manage VM snapshots.

```bash
swarmcracker snapshot [command] [flags]
```

#### snapshot create

Create a snapshot of a running VM.

```bash
swarmcracker snapshot create [flags] <vm-id>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | auto-generated | Snapshot name |
| `--compress` | `false` | Compress the snapshot |
| `--metadata` | — | Key-value metadata (key=value) |

#### snapshot restore

Restore a VM from a snapshot.

```bash
swarmcracker snapshot restore [flags] <snapshot-id>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--force` | `false` | Force restore (stop VM if running) |
| `--new-id` | — | Restore with a new VM ID |

#### snapshot list

List all snapshots.

```bash
swarmcracker snapshot list [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | JSON output |
| `--vm` | — | Filter by VM ID |

#### snapshot delete

Delete a snapshot.

```bash
swarmcracker snapshot delete [flags] <snapshot-id>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--force`, `-f` | `false` | Force delete without confirmation |
| `--all` | `false` | Delete all snapshots |

---

### status

Show the status of a VM or the node.

```bash
swarmcracker status [flags] [vm-id]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--watch`, `-w` | `false` | Watch mode (refresh every second) |

**Example:**
```bash
swarmcracker status              # Node status
swarmcracker status my-vm        # VM status
swarmcracker status --watch      # Watch node status
```

---

### metrics

Show resource metrics.

```bash
swarmcracker metrics [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--watch`, `-w` | `false` | Watch mode |
| `--interval` | `2s` | Refresh interval |
| `--json` | `false` | JSON output |

**Example:**
```bash
swarmcracker metrics --watch --interval 5s
```

---

### stop

Stop a VM (deprecated — use `vm stop` instead).

```bash
swarmcracker stop [flags] <vm-id>
```

---

### reset

Reset the node, stopping all VMs and cleaning up network resources.

```bash
swarmcracker reset [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--force`, `-f` | `false` | Skip confirmation |
| `--keep-state` | `false` | Keep state directory |

**Example:**
```bash
swarmcracker reset --force
```

**What it does:**
1. Kills all Firecracker processes
2. Removes all TAP devices
3. Removes bridge interface
4. Cleans up socket files
5. Clears state (unless `--keep-state`)

---

### logs

View VM logs (deprecated — use `vm logs` instead).

```bash
swarmcracker logs [flags] <vm-id>
```

---

## swarmctl (SwarmKit-format CLI)

`swarmctl` is a minimal SwarmKit-compatible CLI for direct SwarmKit gRPC API interaction.

```bash
swarmctl [command] [flags]
```

### Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--socket` | `/var/run/swarmkit/swarm.sock` | Control API socket path |
| `--tls` | `true` | Use TLS |
| `--tlscacert` | — | CA certificate path |
| `--tlscert` | — | Client certificate path |
| `--tlskey` | — | Client key path |

### service create

Create a service (SwarmKit native format).

```bash
swarmctl service create [flags] <image> [command...]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | — | Service name |
| `--replicas` | `1` | Number of replicas |
| `--cpu-limit` | `0.5` | CPU limit per replica |
| `--memory-limit` | `64m` | Memory limit per replica |

**Example:**
```bash
swarmctl service create --name web --replicas 3 nginx:alpine
```

### service ls/inspect/update/rm

Standard SwarmKit service subcommands.

### node ls

List all nodes.

```bash
swarmctl node ls
```

### task ls/inspect

List and inspect tasks.

---

## swarmd-firecracker (Daemon)

The `swarmd-firecracker` binary is the SwarmKit agent with Firecracker executor. Typically managed by `swarmcracker cluster init/join`, but can be run directly.

```bash
swarmd-firecracker [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--state-dir`, `-d` | `/var/lib/swarmkit` | State directory |
| `--join-addr` | — | Manager address to join |
| `--join-token` | — | Join token |
| `--listen-remote-api` | `0.0.0.0:4242` | Remote API listen address |
| `--listen-control-api` | `/var/run/swarmkit/swarm.sock` | Control API socket |
| `--hostname` | hostname | Node hostname |
| `--manager` | `false` | Start as manager |
| `--force-new-cluster` | `false` | Force new cluster |
| `--kernel-path` | `/usr/share/firecracker/vmlinux` | Kernel image |
| `--rootfs-dir` | `/var/lib/firecracker/rootfs` | Rootfs directory |
| `--socket-dir` | `/var/run/firecracker` | Firecracker socket dir |
| `--default-vcpus` | `1` | Default vCPUs per VM |
| `--default-memory` | `512` | Default memory in MB |
| `--bridge-name` | `swarm-br0` | Bridge name |
| `--subnet` | `192.168.127.0/24` | VM subnet |
| `--bridge-ip` | `192.168.127.1/24` | Bridge IP |
| `--ip-mode` | `static` | IP allocation mode |
| `--nat-enabled` | `true` | Enable NAT |
| `--vxlan-enabled` | `false` | Enable VXLAN overlay |
| `--vxlan-peers` | — | VXLAN peer IPs (comma-separated) |
| `--consul-enabled` | `false` | Enable Consul discovery |
| `--consul-address` | `localhost:8500` | Consul address |
| `--enable-jailer` | `false` | Enable jailer isolation |
| `--jailer-path` | `firecracker` | Jailer binary path |
| `--jailer-uid` | `0` | Jailer UID |
| `--jailer-gid` | `0` | Jailer GID |
| `--jailer-chroot-dir` | `/srv/jailer` | Chroot directory |
| `--enable-cni` | `false` | Enable CNI plugin support |
| `--cni-bin-dir` | `/opt/cni/bin` | CNI plugin directory |
| `--cni-conf-dir` | `/etc/cni/net.d` | CNI config directory |
| `--max-image-age` | `7` | Days before image cleanup |
| `--reserved-cpus` | `0` | CPUs reserved for system |
| `--reserved-memory` | `0` | Memory in MB reserved for system |
| `--log-level` | `info` | Log level (debug, info, warn, error) |

**Example (direct manager start):**
```bash
swarmd-firecracker \
  --manager \
  --force-new-cluster \
  --listen-remote-api 0.0.0.0:4242 \
  --hostname manager-1 \
  --vxlan-enabled \
  --consul-enabled --consul-address 192.168.1.10:8500
```

**Example (worker join):**
```bash
swarmd-firecracker \
  --join-addr 192.168.1.10:4242 \
  --join-token SWMTKN-1-xxx \
  --hostname worker-1 \
  --vxlan-enabled \
  --consul-enabled --consul-address 192.168.1.10:8500
```

---

## Environment Variables

| Variable | Used By | Description |
|----------|---------|-------------|
| `SWARMCRACKER_CONFIG` | swarmcracker | Config file path |
| `SWARM_STATE_DIR` | swarmcracker, swarmctl | State directory |
| `SWARM_SOCKET` | swarmctl | Control API socket |
| `DOCKER_HOST` | swarmctl (fallback) | Docker socket (compatibility) |

---

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | General error |
| `2` | Usage error (invalid flags/args) |
| `3` | Connection error (manager/worker unreachable) |
| `4` | Timeout error |
| `5` | Permission denied |

---

**See Also:** [Configuration Guide](../guides/configuration.md) | [Getting Started](../getting-started/README.md) | [Networking Guide](../guides/networking.md)
