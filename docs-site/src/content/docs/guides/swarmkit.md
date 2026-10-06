---
title: "SwarmKit Guide"
description: "Manage services, nodes, and tasks with SwarmKit — SwarmCracker's orchestration engine."
---

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

> **Note:** `swarmcracker` is the primary CLI for deploying and operating
> services. `swarmctl` is a low-level debug/ops client that talks directly to
> the SwarmKit control socket; prefer `swarmcracker` for day-to-day use.

---

## Architecture

```
SwarmKit Manager
    │ (assigns tasks via gRPC)
    ▼
SwarmKit Worker (swarmd-firecracker)
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
# Service created: <SERVICE_ID>
# Name: svc-nginx-143022
# Image: nginx:latest
# Replicas: 1
```

If `--name` is omitted, the service name is auto-generated as
`svc-<image-basename>-<HHMMSS>`.

### Service from a Golden Image

A service can boot a prebuilt golden image (a full guest with its own
systemd/OpenRC init) instead of building a rootfs from an OCI image. Build one
with `swarmcracker image build`, then reference it by `name[@version]`:

```bash
swarmcracker service create --name web --golden ubuntu-24.04-docker@1.0.0 --replicas 2
```

This sets the `swarmcracker.golden` service label. The equivalent explicit form
is:

```bash
swarmcracker service create --name web --image swarmcracker/golden:ubuntu-24.04-docker-1.0.0 \
  --label swarmcracker.golden=ubuntu-24.04-docker@1.0.0
```

The `--image` value is a placeholder only: SwarmKit validates container image
references, so a golden service still needs a syntactically valid one. It is
never pulled because image preparation is skipped.

The executor skips OCI image preparation and boots the golden rootfs with the
kernel its recipe pinned (`kernel_profile`). Each task gets its own writable
copy of the template (roughly the template's on-disk size per replica), so
replicas never share a root filesystem and removing a service never touches the
golden artifact. A missing artifact fails the task instead of silently falling
back to an OCI image.

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
# Check the manager's API port is reachable
nc -zv <manager-ip> 4242

# Verify the join token from the manager
swarmcracker cluster token
```

### Services Not Starting

```bash
# Check node availability
swarmctl ls-nodes

# Check task status
swarmctl ls-tasks

# Check executor logs (manager or worker node)
journalctl -u swarmcracker-worker -f
```

---

**See Also:** [Configuration](/guides/configuration/) | [Architecture](/architecture/swarmkit/)