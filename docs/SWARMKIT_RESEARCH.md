# SwarmKit Research Notes

## Executive Summary

**SwarmKit** is a standalone orchestration engine toolkit for distributed systems, developed by the Moby project. It is NOT the same as "Docker Swarm" - Docker Swarm is a high-level product that uses SwarmKit under the hood.

## What is SwarmKit?

SwarmKit is a **toolkit for orchestrating distributed systems at any scale**. It includes primitives for:
- Node discovery
- Raft-based consensus
- Task scheduling
- Desired state reconciliation
- Distributed configuration storage

### Key Characteristics

1. **Distributed**: Uses Raft Consensus Algorithm, no single point of failure
2. **Secure**: Mutual TLS for authentication, authorization, and encryption (automatic certificate issuance and rotation)
3. **Simple**: Operationally simple, no external database needed
4. **Standalone**: Can be used WITHOUT Docker

## SwarmKit vs Docker Swarm

### Critical Distinction

| Aspect | SwarmKit | Docker Swarm |
|--------|----------|--------------|
| **What is it?** | Orchestration engine/library | Docker's orchestration feature |
| **Usage** | Standalone or embedded | Built into Docker Engine |
| **Interface** | `swarmctl` CLI | `docker service` commands |
| **Agents** | `swarmd` daemon | Docker Engine in swarm mode |
| **Extensibility** | Custom executors | Limited to container executors |
| **Dependencies** | Go library, no Docker required | Requires Docker Engine |

### Relationship

- **Docker Swarm** is a product built ON TOP OF SwarmKit
- SwarmKit is the orchestration engine that Docker Swarm uses internally
- SwarmKit can be used standalone (without Docker) for custom orchestration solutions
- SwarmCracker integrates with SwarmKit DIRECTLY, not via Docker Swarm

## SwarmKit Architecture

### Components

1. **Manager Nodes**
   - Accept user specifications
   - Maintain cluster state via Raft consensus
   - Reconcile desired state with actual state
   - Make scheduling decisions

2. **Worker Nodes**
   - Execute tasks using an **Executor**
   - Report task status back to managers
   - Can be promoted/demoted to managers

3. **Services**
   - Higher-level abstraction for organizing tasks
   - Define desired state (replicas, updates, etc.)
   - Two types: Replicated Services and Global Services

4. **Tasks**
   - The fundamental unit of work
   - Scheduled onto worker nodes
   - Executed by the executor

### Executor Interface

**This is the key integration point for SwarmCracker!**

SwarmKit uses a **pluggable executor interface**:
- Default executor: Docker Container Executor
- **Custom executors can be swapped out easily**
- Executor is responsible for:
  - Starting tasks
  - Stopping tasks
  - Monitoring task status
  - Reporting task state

**SwarmCracker IS a custom SwarmKit executor** that runs Firecracker microVMs instead of containers!

## SwarmKit Standalone Usage

### Installation

```bash
# From source
git clone https://github.com/moby/swarmkit.git
cd swarmkit
make binaries

# This produces:
# - swarmd (SwarmKit daemon/agent)
# - swarmctl (SwarmKit control CLI)
```

### Starting a SwarmKit Cluster

#### 1. Initialize First Manager Node

```bash
# Start first node as manager
swarmd -d /tmp/node-1 \
  --listen-control-api /tmp/node-1/swarm.sock \
  --hostname node-1 \
  --listen-remote-api 127.0.0.1:4242

# Get join tokens
export SWARM_SOCKET=/tmp/node-1/swarm.sock
swarmctl cluster inspect default
```

#### 2. Join Worker Nodes

```bash
# Join additional nodes as workers
swarmd -d /tmp/node-2 \
  --hostname node-2 \
  --join-addr 127.0.0.1:4242 \
  --join-token <WORKER_TOKEN> \
  --listen-remote-api 127.0.0.1:4343

swarmd -d /tmp/node-3 \
  --hostname node-3 \
  --join-addr 127.0.0.1:4242 \
  --join-token <WORKER_TOKEN> \
  --listen-remote-api 127.0.0.1:4344
```

#### 3. Deploy Services

```bash
# Create a service
swarmctl service create --name redis --image redis:3.0.5

# List services
swarmctl service ls

# Inspect service
swarmctl service inspect redis

# Scale service
swarmctl service update redis --replicas 6

# Update service image
swarmctl service update redis --image redis:3.0.6

# Update with rolling options
swarmctl service update redis \
  --image redis:3.0.7 \
  --update-parallelism 2 \
  --update-delay 10s
```

#### 4. Manage Nodes

```bash
# List nodes
swarmctl node ls

# Drain a node (for maintenance)
swarmctl node drain node-1

# Pause a node
swarmctl node pause node-2

# Activate a node
swarmctl node activate node-1
```

## Key SwarmKit Features

### Orchestration
- **Desired State Reconciliation**: Automatically maintains desired state
- **Service Types**: Replicated (N replicas) and Global (1 per node)
- **Configurable Updates**: Rolling updates with parallelism and delay controls
- **Restart Policies**: Automatic restart with configurable conditions

### Scheduling
- **Resource Awareness**: Tracks node resources
- **Constraints**: Limit where tasks can run (node.role, node.labels, etc.)
- **Strategies**: Spread strategy for load distribution

### Cluster Management
- **State Store**: Raft-based replicated state (in-memory reads)
- **Topology Management**: Dynamic role changes (worker ↔ manager)
- **Node Management**: Pause, Drain, Active states

### Security
- **Mutual TLS**: All node communication encrypted
- **Token-based Join**: Cryptographic tokens for node admission
- **Certificate Rotation**: Automatic TLS cert rotation (default: 3 months)

## SwarmKit API and Interfaces

### gRPC API
SwarmKit exposes a gRPC API for:
- Cluster management
- Service control
- Node operations
- Task monitoring

### Executor Interface
```go
type Executor interface {
    // Describe returns the executor description
    Describe(*DescribeRequest) (*DescribeResponse, error)
    
    // Prepare prepares a task for execution
    Prepare(*PrepareTaskRequest) (*PrepareTaskResponse, error)
    
    // Start starts a task
    Start(*StartTaskRequest) (*StartTaskResponse, error)
    
    // Stop stops a task
    Stop(*StopTaskRequest) (*StopTaskResponse, error)
    
    // Remove removes a task
    Remove(*RemoveTaskRequest) (*RemoveTaskResponse, error)
}
```

## How SwarmCracker Integrates

### Integration Point
SwarmCracker implements the **SwarmKit Executor interface** to run Firecracker microVMs instead of Docker containers.

### Architecture Flow
```
SwarmKit Manager
    ↓ (gRPC: Task assignment)
SwarmKit Agent (swarmd)
    ↓ (Executor interface)
SwarmCracker Executor ← CUSTOM IMPLEMENTATION
    ↓
Task Translator (SwarmKit task → Firecracker config)
    ↓
Image Preparer (OCI image → rootfs)
    ↓
Network Manager (TAP device + bridge)
    ↓
Firecracker VMM (launch microVM)
    ↓
MicroVM with isolated workload
```

### Key Benefits
1. **No Docker required** - SwarmKit can run standalone
2. **Custom executor** - Pluggable executor interface
3. **Familiar orchestration** - Same concepts as Docker Swarm but more flexible
4. **Production-grade** - Used by Docker Swarm at scale

## References

- **Official GitHub**: https://github.com/moby/swarmkit
- **Go Documentation**: https://pkg.go.dev/github.com/moby/swarmkit
- **Design Docs**: https://github.com/moby/swarmkit/tree/master/design
- **README**: https://github.com/moby/swarmkit/blob/master/README.md

## Key Takeaways for SwarmCracker

1. ✅ **We integrate with SwarmKit**, not Docker Swarm directly
2. ✅ **SwarmKit is standalone** - doesn't require Docker Engine
3. ✅ **We use the executor interface** - the same way Docker does
4. ✅ **SwarmKit is production-grade** - powers Docker Swarm
5. ❌ **Don't say "Docker Swarm"** - say "SwarmKit orchestration"
6. ✅ **Clarify the relationship** when mentioning Docker: "SwarmKit is the orchestration engine used by Docker Swarm"

## Terminology Guide

| ✅ Use This | ❌ Not This |
|-------------|-------------|
| SwarmKit | Docker Swarm (when referring to the engine) |
| swarmd | docker swarmd |
| swarmctl | docker service |
| SwarmKit orchestration | Docker Swarm orchestration (in our context) |
| Custom executor | Docker container |
| MicroVM orchestration | Container orchestration |

---

**Research Date**: 2026-02-01  
**Researcher**: SwarmCracker Subagent  
**Purpose**: Clarify SwarmKit vs Docker Swarm distinction for accurate documentation
