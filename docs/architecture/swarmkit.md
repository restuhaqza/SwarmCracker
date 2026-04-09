# SwarmKit Notes

## What SwarmKit Is

SwarmKit is an orchestration toolkit from the Moby project. Not the same as "Docker Swarm" - Docker Swarm is a product built on SwarmKit.

SwarmKit provides:
- Node discovery
- Raft consensus
- Task scheduling
- Desired state reconciliation
- Distributed config storage

## SwarmKit vs Docker Swarm

| | SwarmKit | Docker Swarm |
|--|----------|--------------|
| What | Engine/library | Docker's orchestration |
| CLI | `swarmctl` | `docker service` |
| Agent | `swarmd` | Docker Engine |
| Extensible | Custom executors | Docker containers only |
| Needs Docker | No | Yes |

Docker Swarm uses SwarmKit internally. SwarmCracker integrates with SwarmKit directly, not through Docker.

## Architecture

### Components

**Managers**
- Accept specs, maintain state via Raft
- Make scheduling decisions
- 3, 5, or 7 for fault tolerance

**Workers**
- Execute tasks via executor interface
- Report status to managers
- Can be promoted to manager

**Services**
- Replicated: N copies
- Global: one per node

**Tasks**
- Unit of work
- Lifecycle: NEW → ASSIGNED → RUNNING → COMPLETE/FAILED

### Executor Interface

SwarmKit uses a pluggable executor. Default runs Docker containers. SwarmCracker implements a custom executor for Firecracker microVMs.

```
SwarmKit Manager → gRPC → swarmd Agent → Executor → MicroVM
```

## Standalone SwarmKit

### Install

```bash
git clone https://github.com/moby/swarmkit.git
cd swarmkit
make binaries

# Produces swarmd and swarmctl
sudo cp bin/swarmd bin/swarmctl /usr/local/bin/
```

### Start Cluster

```bash
# Manager
swarmd -d /tmp/node-1 \
  --listen-control-api /tmp/node-1/swarm.sock \
  --hostname node-1 \
  --listen-remote-api 0.0.0.0:4242

# Get join token
export SWARM_SOCKET=/tmp/node-1/swarm.sock
swarmctl cluster inspect default
```

### Join Workers

```bash
swarmd -d /tmp/node-2 \
  --hostname node-2 \
  --join-addr 127.0.0.1:4242 \
  --join-token <WORKER_TOKEN>
```

### Deploy Service

```bash
swarmctl service create --name redis --image redis:3.0.5
swarmctl service ls
swarmctl service update redis --replicas 6
```

## SwarmCracker Integration

SwarmCracker implements the SwarmKit executor interface:

```go
type Executor interface {
    Prepare(*PrepareTaskRequest) (*PrepareTaskResponse, error)
    Start(*StartTaskRequest) (*StartTaskResponse, error)
    Stop(*StopTaskRequest) (*StopTaskResponse, error)
    Remove(*RemoveTaskRequest) (*RemoveTaskResponse, error)
}
```

### Worker with SwarmCracker

```bash
swarmd -d /var/lib/swarmkit/worker \
  --hostname worker-1 \
  --join-addr <manager>:4242 \
  --join-token <TOKEN> \
  --executor firecracker \
  --executor-config /etc/swarmcracker/config.yaml
```

Tasks run as Firecracker microVMs with KVM isolation.

## Why SwarmKit Direct

- No Docker dependency
- Custom executor support
- Same engine Docker Swarm uses
- Proven at scale

## Terminology

Say | Not
---|-----
SwarmKit orchestration | Docker Swarm orchestration
swarmctl | docker service
swarmd | Docker daemon
Custom executor | Container

---

**Source:** https://github.com/moby/swarmkit