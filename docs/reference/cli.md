# CLI Reference

> Complete command reference for `swarmcracker` and `swarmctl`.

---

## swarmcracker

Main SwarmCracker CLI for cluster management.

### Commands

| Command | Description |
|---------|-------------|
| `swarmcracker init` | Initialize manager node |
| `swarmcracker join` | Join worker to cluster |
| `swarmcracker run` | Run executor daemon |
| `swarmcracker deploy` | Deploy service |
| `swarmcracker validate` | Validate configuration |
| `swarmcracker version` | Show version |
| `swarmcracker list` | List running VMs |
| `swarmcracker status` | Show cluster status |
| `swarmcracker logs` | View logs |
| `swarmcracker stop` | Stop VMs |
| `swarmcracker volume` | Volume management |
| `swarmcracker snapshot` | Snapshot operations |

---

### swarmcracker init

Initialize a manager node:

```bash
swarmcracker init --hostname manager-1
```

**Options:**

| Flag | Default | Description |
|------|---------|-------------|
| `--hostname` | `<system-hostname>` | Node hostname |
| `--listen-addr` | `0.0.0.0:4242` | Remote API address |
| `--data-dir` | `/var/lib/swarmkit` | State directory |
| `--config` | `/etc/swarmcracker/config.yaml` | Config file |

---

### swarmcracker join

Join a worker node to cluster:

```bash
swarmcracker join \
  --hostname worker-1 \
  --manager <manager-ip>:4242 \
  --token SWMTKN-1-...
```

**Options:**

| Flag | Description |
|------|-------------|
| `--hostname` | Node hostname |
| `--manager` | Manager address (IP:port) |
| `--token` | Join token (worker or manager) |
| `--config` | Config file path |

---

### swarmcracker run

Run the executor daemon:

```bash
swarmcracker run --config /etc/swarmcracker/config.yaml
```

**Options:**

| Flag | Description |
|------|-------------|
| `--config` | Config file path |
| `--socket` | SwarmKit socket path |

---

### swarmcracker validate

Validate configuration:

```bash
swarmcracker validate --config config.yaml
```

---

### swarmcracker version

Show version information:

```bash
swarmcracker version

# Output:
# SwarmCracker v0.6.0
# Firecracker v1.15.1
# SwarmKit v2.1.1
```

---

## swarmctl

SwarmKit control client for cluster operations.

### Services

| Command | Description |
|---------|-------------|
| `swarmctl ls` | List all services |
| `swarmctl ls-services` | List services (alias) |
| `swarmctl create-service <image>` | Create service from image |
| `swarmctl scale <svc-id> <n>` | Scale to N replicas |
| `swarmctl update <svc-id> [flags]` | Update service |
| `swarmctl rm-service <svc-id>` | Remove service |
| `swarmctl inspect <id>` | Inspect service/task (JSON) |

**Update Flags:**

| Flag | Description |
|------|-------------|
| `--image <image>` | Update container image |
| `--replicas <n>` | Update replica count |
| `--env KEY=VALUE` | Add/update environment variable |

---

### Nodes

| Command | Description |
|---------|-------------|
| `swarmctl ls-nodes` | List all nodes |
| `swarmctl drain <node-id>` | Drain node (no new tasks) |
| `swarmctl activate <node-id>` | Activate node |
| `swarmctl pause-node <node-id>` | Pause node |
| `swarmctl promote <node-id>` | Promote worker to manager |
| `swarmctl demote <node-id>` | Demote manager to worker |

---

### Tasks

| Command | Description |
|---------|-------------|
| `swarmctl ls-tasks` | List all tasks |

---

### Snapshots

| Command | Description |
|---------|-------------|
| `swarmctl snapshot create <vm-id> --name <n>` | Create snapshot |
| `swarmctl snapshot list <vm-id>` | List snapshots |
| `swarmctl snapshot restore <vm-id> --name <n>` | Restore snapshot |
| `swarmctl snapshot delete <vm-id> --name <n>` | Delete snapshot |

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SWARM_SOCKET` | `/var/run/swarmkit/swarm.sock` | Control socket |
| `SWARM_STATE_DIR` | `/var/lib/swarmkit` | State directory |
| `SWARMCRACKER_CONFIG` | `/etc/swarmcracker/config.yaml` | Config file |

---

## Examples

### Initialize Cluster

```bash
# Manager node
swarmcracker init --hostname manager-1

# Get join token
swarmctl cluster inspect default

# Worker node
swarmcracker join --hostname worker-1 --manager 192.168.1.10:4242 --token SWMTKN-1-...
```

### Deploy Services

```bash
# Create service
swarmctl create-service nginx:latest

# Scale
swarmctl scale svc-nginx-143022 5

# Update
swarmctl update svc-nginx --image nginx:1.25

# Remove
swarmctl rm-service svc-nginx-143022
```

### Manage Nodes

```bash
# List nodes
swarmctl ls-nodes

# Drain for maintenance
swarmctl drain worker-abc

# Promote to manager
swarmctl promote worker-def

# Reactivate
swarmctl activate worker-abc
```

### Snapshots

```bash
# Create snapshot
swarmctl snapshot create svc-nginx --name backup-1

# List snapshots
swarmctl snapshot list svc-nginx

# Restore
swarmctl snapshot restore svc-nginx --name backup-1
```

---

## TLS Authentication

swarmctl automatically uses TLS certificates:

- **Cert:** `$SWARM_STATE_DIR/certificates/swarm-node.crt`
- **Key:** `$SWARM_STATE_DIR/certificates/swarm-node.key`
- **CA:** `$SWARM_STATE_DIR/certificates/swarm-root-ca.crt`

---

## Notes

- Service IDs are short strings (first 12 chars work)
- Node IDs can also be shortened
- `inspect` outputs full JSON for detailed info
- All commands require socket access (root or firecracker user)

---

**See Also:** [Configuration](../guides/configuration.md) | [SwarmKit Guide](../guides/swarmkit.md)