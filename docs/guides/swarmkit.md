# SwarmKit Guide

> Manage services, nodes, and tasks with SwarmKit — SwarmCracker's orchestration engine.

---

## What is SwarmKit?

**SwarmKit** is a toolkit for orchestrating distributed systems. SwarmCracker integrates directly with SwarmKit, bypassing Docker Swarm entirely.

| SwarmKit | Docker Swarm |
|----------|--------------|
| Orchestration engine/library | Docker's orchestration feature |
| `swarmctl` CLI | `docker service` commands |
| Custom executors pluggable | Limited to Docker containers |
| **No Docker required** | Requires Docker Engine |

**SwarmCracker implements the SwarmKit executor interface to run Firecracker microVMs.**

---

## Architecture

```
SwarmKit Manager
    │ (assigns tasks via gRPC)
    ▼
SwarmKit Worker (swarmd)
    │ (calls executor interface)
    ▼
SwarmCracker Executor ← Custom implementation
    │
    ▼
Firecracker MicroVMs
```

---

## Commands Reference

### Services

| Command | Description |
|---------|-------------|
| `swarmctl ls` | List all services |
| `swarmctl create-service <image>` | Create service from image |
| `swarmctl scale <svc-id> <n>` | Scale to N replicas |
| `swarmctl update <svc-id> [flags]` | Update service config |
| `swarmctl rm-service <svc-id>` | Remove service |
| `swarmctl inspect <id>` | Inspect service/task |

**Update Flags:**

| Flag | Description |
|------|-------------|
| `--image <image>` | Update container image |
| `--replicas <n>` | Change replica count |
| `--env KEY=VALUE` | Add/update environment variable |

### Nodes

| Command | Description |
|---------|-------------|
| `swarmctl ls-nodes` | List all nodes |
| `swarmctl drain <node-id>` | Drain node (no new tasks) |
| `swarmctl activate <node-id>` | Activate node |
| `swarmctl pause-node <node-id>` | Pause node |
| `swarmctl promote <node-id>` | Promote to manager |
| `swarmctl demote <node-id>` | Demote to worker |

### Tasks

| Command | Description |
|---------|-------------|
| `swarmctl ls-tasks` | List all tasks |

---

## Service Management

### Create Service

```bash
swarmctl create-service nginx:latest

# Output:
# Service created: svc-nginx-143022
# Image: nginx:latest
```

Service name is auto-generated: `svc-<image>-<timestamp>`

### Scale Service

```bash
swarmctl scale svc-nginx-143022 5

# Creates 5 Firecracker microVMs
# Distributed across available worker nodes
```

### Update Service

```bash
# Update image (triggers rolling update)
swarmctl update svc-nginx --image nginx:1.25

# Update replicas
swarmctl update svc-nginx --replicas 10

# Add environment variable
swarmctl update svc-nginx --env APP_ENV=production
```

### Remove Service

```bash
swarmctl rm-service svc-nginx-143022

# Stops all tasks and removes microVMs
```

---

## Node Management

### Node Availability States

| State | Description |
|-------|-------------|
| **ACTIVE** | Accept new tasks, run existing |
| **PAUSED** | No new tasks, existing continue |
| **DRAINED** | No new tasks, reschedule existing |

### Drain Node for Maintenance

```bash
swarmctl drain worker-abc

# Tasks rescheduled to other nodes
# No new tasks assigned
```

### Promote Worker to Manager

```bash
swarmctl promote worker-def

# Node joins Raft consensus
# Participates in scheduling decisions
```

### Demote Manager to Worker

```bash
swarmctl demote manager-ghi

# Node leaves Raft consensus
# Only executes tasks
```

---

## Task Lifecycle

Tasks transition through states:

```
NEW → ASSIGNED → ACCEPTED → PREPARING → STARTING → RUNNING → COMPLETE/FAILED
```

| State | Description |
|-------|-------------|
| NEW | Task created by manager |
| ASSIGNED | Manager assigned to a node |
| ACCEPTED | Worker accepted the task |
| PREPARING | Executor preparing (VM setup) |
| STARTING | Executor starting (VM boot) |
| RUNNING | Task running successfully |
| COMPLETE | Task finished |
| FAILED | Task failed |

---

## Rolling Updates

SwarmKit automatically performs rolling updates when you change a service:

1. Manager creates new task with updated spec
2. SwarmCracker starts new Firecracker VM
3. VM reports RUNNING status
4. Manager stops old task
5. Executor removes old VM

**Controlled by SwarmKit's update policy:**
- Parallelism: Number of simultaneous updates
- Delay: Wait time between batches
- Monitor: Duration to verify stability

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SWARM_SOCKET` | `/var/run/swarmkit/swarm.sock` | Control socket |
| `SWARM_STATE_DIR` | `/var/lib/swarmkit` | TLS certificates |

---

## Examples

### Deploy Web Application

```bash
# Create frontend
swarmctl create-service myapp-frontend:latest

# Scale to 3 replicas
swarmctl scale svc-myapp-frontend 3

# Create backend
swarmctl create-service myapp-backend:latest

# Create database (single replica)
swarmctl create-service postgres:15
```

### Maintenance Workflow

```bash
# Drain node for maintenance
swarmctl drain worker-1

# Wait for tasks to reschedule
swarmctl ls-tasks

# Perform maintenance on worker-1
# ...

# Reactivate node
swarmctl activate worker-1
```

---

## Troubleshooting

### Node Won't Join

```bash
# Check manager reachable
curl http://<manager-ip>:4242

# Verify join token
swarmctl cluster inspect default
```

### Services Not Starting

```bash
# Check node availability
swarmctl ls-nodes

# Check task status
swarmctl ls-tasks

# Check executor logs
journalctl -u swarmcracker -f
```

---

**See Also:** [Configuration](configuration.md) | [Architecture](../architecture/swarmkit.md)