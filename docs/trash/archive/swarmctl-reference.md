# swarmctl CLI Reference

SwarmCracker's `swarmctl` is a lightweight SwarmKit control client for managing clusters.

## Commands

### Services

| Command | Description |
|---------|-------------|
| `ls-services`, `ls` | List all services |
| `create-service <image>` | Create a service from an image |
| `rm-service <service-id>` | Remove a service |
| `inspect <id>` | Inspect a service or task (JSON output) |
| `scale <service-id> <replicas>` | Scale service to N replicas |
| `update <service-id> [flags]` | Update service configuration |

### Update Flags

| Flag | Description |
|------|-------------|
| `--image <image>` | Update container image |
| `--replicas <n>` | Update number of replicas |
| `--env <KEY=VALUE>` | Add/update environment variable |

### Nodes

| Command | Description |
|---------|-------------|
| `ls-nodes` | List all nodes in the cluster |
| `drain <node-id>` | Drain a node (reschedule tasks elsewhere) |
| `activate <node-id>` | Activate a drained/paused node |
| `pause-node <node-id>` | Pause a node (no new tasks) |
| `promote <node-id>` | Promote worker to manager |
| `demote <node-id>` | Demote manager to worker |

### Tasks

| Command | Description |
|---------|-------------|
| `ls-tasks` | List all tasks |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SWARM_SOCKET` | `/var/run/swarmkit/swarm.sock` | Path to swarm socket |
| `SWARM_STATE_DIR` | `/var/lib/swarmkit` | State directory for TLS certs |

## Examples

### Basic Workflow

```bash
# List nodes
swarmctl ls-nodes

# Create a service
swarmctl create-service nginx:latest
# Output: Service created: abc123
#         Name: svc-nginx-143022
#         Image: nginx:latest

# List services
swarmctl ls

# Scale to 3 replicas
swarmctl scale abc123 3

# Update image
swarmctl update abc123 --image nginx:1.25

# Remove service
swarmctl rm-service abc123
```

### Node Management

```bash
# List nodes
swarmctl ls-nodes

# Drain node for maintenance
swarmctl drain node-abc

# Promote worker to manager
swarmctl promote node-def

# Demote manager to worker
swarmctl demote node-ghi
```

## TLS Authentication

swarmctl automatically loads TLS certificates from:
- Certificate: `$SWARM_STATE_DIR/certificates/swarm-node.crt`
- Key: `$SWARM_STATE_DIR/certificates/swarm-node.key`
- CA: `$SWARM_STATE_DIR/certificates/swarm-root-ca.crt`

## Notes

- Service names are auto-generated from image name + timestamp
- `inspect` outputs full JSON for detailed information
- Node commands require the node ID (first 12 characters work)

---

**Last Updated**: 2026-04-09
**SwarmCracker Version**: v0.6.0