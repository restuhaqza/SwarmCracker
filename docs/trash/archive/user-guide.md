# SwarmKit Guide for SwarmCracker

## What is SwarmKit?

**SwarmKit** is a toolkit for orchestrating distributed systems at any scale. It provides:

- **Node discovery** - Automatic detection and management of cluster nodes
- **Raft-based consensus** - Distributed decision making without single point of failure
- **Task scheduling** - Intelligent workload distribution
- **Desired state reconciliation** - Automatic maintenance of cluster state
- **Security** - Mutual TLS, token-based joining, automatic certificate rotation

### Key Point
> **SwarmKit is NOT "Docker Swarm"** - Docker Swarm is a product built ON TOP OF SwarmKit. SwarmCracker integrates with SwarmKit directly, giving you microVM orchestration without requiring Docker Swarm or Kubernetes.

## SwarmKit vs Docker Swarm

### The Critical Difference

| Aspect | SwarmKit | Docker Swarm |
|--------|----------|--------------|
| **What is it?** | Orchestration engine/library | Docker's orchestration feature |
| **How you use it** | Standalone (swarmd/swarmctl) | Built into Docker Engine |
| **Interface** | `swarmctl` CLI | `docker service` commands |
| **Agent** | `swarmd` daemon | Docker Engine in swarm mode |
| **Flexibility** | Custom executors pluggable | Limited to Docker containers |
| **Dependencies** | No Docker required | Requires Docker Engine |

### When to Use Each

- **Use SwarmKit directly** (SwarmCracker approach):
  - Want custom executor (like Firecracker microVMs)
  - Don't want Docker dependency
  - Need pluggable orchestration engine
  - Building custom orchestration solutions

- **Use Docker Swarm**:
  - Already using Docker ecosystem
  - Want Docker CLI integration
  - Standard container orchestration is sufficient
  - Don't need custom executors

### Relationship Explained

```
Docker Swarm (Product)
    ↓ (built on top of)
SwarmKit (Orchestration Engine)
    ↓ (implements)
Raft Consensus + gRPC API + Executor Interface
```

**SwarmCracker** bypasses Docker Swarm and implements the SwarmKit executor interface directly to run Firecracker microVMs.

## SwarmKit Architecture

### Components

#### 1. Manager Nodes
- **Purpose**: Accept specifications, maintain cluster state, make scheduling decisions
- **Features**:
  - Raft-based consensus for high availability
  - In-memory state store for fast reads
  - Reconciliation loop maintains desired state
  - Can be 3, 5, or 7 managers for fault tolerance

#### 2. Worker Nodes
- **Purpose**: Execute tasks using an executor
- **Features**:
  - Run tasks via executor interface
  - Report task status to managers
  - Can be promoted to managers dynamically
  - Handle task failures and restarts

#### 3. Services
- **Purpose**: Organize tasks into logical units
- **Types**:
  - **Replicated Services**: Run N replicas (e.g., "3 nginx instances")
  - **Global Services**: Run one task per node (e.g., monitoring agent)

#### 4. Tasks
- **Purpose**: Fundamental unit of work
- **Lifecycle**: 
  - NEW → ASSIGNED → ACCEPTED → PREPARING → STARTING → RUNNING → COMPLETE/FAILED
  - Automatically rescheduled on failure
  - Can be stopped, removed, or updated

#### 5. Executor Interface (Key for SwarmCracker!)
- **Purpose**: Pluggable interface for running tasks
- **Default**: Docker Container Executor
- **Custom**: SwarmCracker implements this for Firecracker microVMs!

```
SwarmKit Manager
    ↓ (assigns task via gRPC)
SwarmKit Agent (swarmd)
    ↓ (calls executor interface)
Custom Executor ← SwarmCracker implements this
    ↓ (runs workload)
Firecracker MicroVMs
```

## Using SwarmKit Standalone

### Installation

#### From Source
```bash
# Clone SwarmKit repository
git clone https://github.com/moby/swarmkit.git
cd swarmkit

# Build binaries
make binaries

# This produces:
# - ./bin/swarmd (SwarmKit daemon/agent)
# - ./bin/swarmctl (SwarmKit control CLI)

# Install to PATH
sudo cp ./bin/swarmd /usr/local/bin/
sudo cp ./bin/swarmctl /usr/local/bin/
```

#### Verify Installation
```bash
swarmd --version
swarmctl --version
```

### Starting a SwarmKit Cluster

#### Step 1: Initialize First Manager

```bash
# Create data directory
mkdir -p /tmp/node-1

# Start first node as manager
swarmd -d /tmp/node-1 \
  --listen-control-api /tmp/node-1/swarm.sock \
  --hostname node-1 \
  --listen-remote-api 0.0.0.0:4242

# In another terminal, get join tokens
export SWARM_SOCKET=/tmp/node-1/swarm.sock
swarmctl cluster inspect default

# Output shows:
# Join Tokens:
#   Worker: SWMTKN-1-...
#   Manager: SWMTKN-1-...
```

**Explanation of flags:**
- `-d /tmp/node-1` - Data directory for this node
- `--listen-control-api` - Unix socket for local management
- `--hostname` - Node identifier
- `--listen-remote-api` - Remote API address for other nodes

#### Step 2: Join Worker Nodes

```bash
# On worker machines (or additional terminals for testing)
mkdir -p /tmp/node-2

swarmd -d /tmp/node-2 \
  --hostname node-2 \
  --join-addr <manager-ip>:4242 \
  --join-token <WORKER_TOKEN> \
  --listen-remote-api 0.0.0.0:4243

# Add third worker
mkdir -p /tmp/node-3

swarmd -d /tmp/node-3 \
  --hostname node-3 \
  --join-addr <manager-ip>:4242 \
  --join-token <WORKER_TOKEN> \
  --listen-remote-api 0.0.0.0:4244
```

**Replace:**
- `<manager-ip>` with IP of first node (use `127.0.0.1` for local testing)
- `<WORKER_TOKEN>` with worker token from `swarmctl cluster inspect`

#### Step 3: Verify Cluster

```bash
# List all nodes
export SWARM_SOCKET=/tmp/node-1/swarm.sock
swarmctl node ls

# Output:
# ID            Name      Membership  Status  Availability  Manager Status
# <id>          node-1    ACCEPTED    READY   ACTIVE        LEADER *
# <id>          node-2    ACCEPTED    READY   ACTIVE
# <id>          node-3    ACCEPTED    READY   ACTIVE
```

### Deploying Services

#### Create a Service

```bash
# Basic service creation
swarmctl service create --name redis --image redis:3.0.5

# With replicas
swarmctl service create --name nginx --image nginx:latest --replicas 3

# With environment variables
swarmctl service create --name app \
  --image myapp:latest \
  --env APP_ENV=production \
  --env DEBUG=false

# With resource constraints
swarmctl service create --name db \
  --image postgres:15 \
  --replicas 1 \
  --memory-reservation 512MB \
  --cpu-limit 2
```

#### List and Inspect Services

```bash
# List all services
swarmctl service ls

# Inspect specific service
swarmctl service inspect redis

# Detailed output
ID: <service-id>
Name: redis
Replicas: 1/1
Template:
  Container:
    Image: redis:3.0.5

Task ID    Service  Slot  Image           Desired State  Last State  Node
<task-id>  redis    1     redis:3.0.5    RUNNING       RUNNING     node-1
```

#### Scale Services

```bash
# Scale to 6 replicas
swarmctl service update redis --replicas 6

# Verify
swarmctl service inspect redis
# Shows 6 tasks distributed across nodes
```

#### Update Services

```bash
# Update image (rolling update)
swarmctl service update redis --image redis:3.0.6

# Update with parallelism and delay
swarmctl service update redis \
  --image redis:3.0.7 \
  --update-parallelism 2 \
  --update-delay 10s

# This updates 2 tasks at a time, waiting 10s between batches
```

#### Service Update Options

| Flag | Description | Default |
|------|-------------|---------|
| `--update-parallelism` | Number of tasks to update simultaneously | 0 (all) |
| `--update-delay` | Delay between update batches | 0s |
| `--update-failure-action` | Action on update failure (pause/continue/rollback) | pause |
| `--update-monitor` | Duration to monitor task after update | 5s |

### Managing Nodes

#### List Nodes
```bash
swarmctl node ls
```

#### Inspect Node
```bash
swarmctl node inspect node-1
```

#### Node Availability

```bash
# Drain a node (reschedule tasks elsewhere, for maintenance)
swarmctl node drain node-1

# Pause a node (no new tasks, existing tasks continue)
swarmctl node pause node-2

# Activate a node (accept tasks normally)
swarmctl node activate node-1
```

**Availability States:**
- **ACTIVE**: Accept new tasks, run existing tasks
- **PAUSED**: Don't accept new tasks, continue running existing
- **DRAINED**: Don't accept new tasks, reschedule existing elsewhere

#### Promote Worker to Manager

```bash
swarmctl node promote node-2
# Node becomes manager, participates in Raft consensus
```

#### Demote Manager to Worker

```bash
swarmctl node demote node-2
# Node becomes worker, stops participating in consensus
```

### Removing Services and Nodes

```bash
# Remove a service
swarmctl service remove redis

# Remove a node (must be demoted first if manager)
swarmctl node demote node-3
swarmctl node remove node-3
```

## SwarmKit with SwarmCracker

### How SwarmCracker Integrates

SwarmCracker implements the **SwarmKit executor interface** to run Firecracker microVMs instead of containers:

```go
// SwarmKit calls this interface
type Executor interface {
    Prepare(*PrepareTaskRequest) (*PrepareTaskResponse, error)
    Start(*StartTaskRequest) (*StartTaskResponse, error)
    Stop(*StopTaskRequest) (*StopTaskResponse, error)
    Remove(*RemoveTaskRequest) (*RemoveTaskResponse, error)
}

// SwarmCracker implements this for Firecracker microVMs
type SwarmCrackerExecutor struct {
    // Translates SwarmKit tasks → Firecracker configs
    // Manages microVM lifecycle
    // Handles networking for each VM
}
```

### Starting SwarmKit with SwarmCracker

#### 1. Create SwarmCracker Configuration

```bash
cat > /etc/swarmcracker/config.yaml <<EOF
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
  rootfs_dir: "/var/lib/firecracker/rootfs"
  default_vcpus: 2
  default_memory_mb: 1024

network:
  bridge_name: "swarm-br0"
  bridge_ip: "192.168.127.1/24"
  dhcp_enabled: true

logging:
  level: "info"
  format: "json"
EOF
```

#### 2. Start SwarmKit Manager

```bash
# Manager doesn't need executor config (only workers execute tasks)
swarmd -d /var/lib/swarmkit/manager \
  --listen-control-api /var/run/swarmkit/swarm.sock \
  --hostname manager-1 \
  --listen-remote-api 0.0.0.0:4242
```

#### 3. Start SwarmKit Workers with SwarmCracker

```bash
# Worker with SwarmCracker executor
swarmd -d /var/lib/swarmkit/worker-1 \
  --hostname worker-1 \
  --join-addr <manager-ip>:4242 \
  --join-token <WORKER_TOKEN> \
  --listen-remote-api 0.0.0.0:4243 \
  --executor firecracker \
  --executor-config /etc/swarmcracker/config.yaml
```

**Key flags:**
- `--executor firecracker` - Use custom executor (SwarmCracker)
- `--executor-config` - Path to SwarmCracker configuration

#### 4. Deploy Services as MicroVMs

```bash
# Set swarmctl to talk to manager
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Deploy service (runs as Firecracker microVMs!)
swarmctl service create --name nginx --image nginx:latest --replicas 3

# Each replica is now a hardware-isolated microVM
# With full kernel separation via KVM
```

### Benefits of SwarmKit + SwarmCracker

1. **No Docker Required** - SwarmKit standalone, no Docker Engine
2. **Strong Isolation** - Each task gets its own kernel via KVM
3. **Simple Orchestration** - SwarmKit's proven orchestration
4. **Production-Grade** - SwarmKit powers Docker Swarm at scale
5. **Flexible** - Custom executor interface enables innovation

## Advanced SwarmKit Features

### Service Constraints

Control where tasks run:

```bash
# Only on manager nodes
swarmctl service create --name monitoring \
  --image prometheus:latest \
  --constraint node.role==manager

# Only on specific node
swarmctl service create --name db \
  --image postgres:15 \
  --constraint node.hostname==node-1

# Custom labels
swarmctl node update --label-add ssd=true node-2
swarmctl service create --name cache \
  --image redis:latest \
  --constraint node.labels.ssd==true
```

### Service Modes

```bash
# Replicated (default)
swarmctl service create --name web \
  --image nginx:latest \
  --mode replicated --replicas 5

# Global (one per node)
swarmctl service create --name agent \
  --image monitoring-agent:latest \
  --mode global
```

### Rolling Updates

```bash
# Configure update strategy
swarmctl service create --name app \
  --image myapp:latest \
  --replicas 10 \
  --update-parallelism 2 \
  --update-delay 5s \
  --update-failure-action rollback \
  --update-monitor 10s

# Rolling update happens automatically on image change
swarmctl service update app --image myapp:v2.0
```

### Restart Policies

```bash
# Configure restart behavior
swarmctl service create --name worker \
  --image worker:latest \
  --restart-condition on-failure \
  --restart-delay 5s \
  --restart-max-attempts 3 \
  --restart-window 1m
```

**Restart Conditions:**
- `none` - Don't restart
- `on-failure` - Restart only on failure
- `any` - Always restart

### Resource Limits

```bash
swarmctl service create --name app \
  --image app:latest \
  --memory-limit 1GB \
  --memory-reservation 512MB \
  --cpu-limit 2 \
  --cpu-reservation 0.5
```

## Troubleshooting

### Node Won't Join Cluster

```bash
# Check manager is reachable
curl http://<manager-ip>:4242

# Verify token is correct (matches worker or manager)
swarmctl cluster inspect default

# Check firewall (port 4242 must be open)
sudo ufw status
sudo ufw allow 4242/tcp
```

### Services Not Starting

```bash
# Check service status
swarmctl service ps <service-name>

# Check node availability
swarmctl node ls

# Check node logs
journalctl -u swarmd -f
```

### Executor Issues

```bash
# Check if SwarmCracker is installed
which swarmcracker

# Validate configuration
swarmcracker validate --config /etc/swarmcracker/config.yaml

# Check executor integration in swarmd logs
journalctl -u swarmd | grep executor
```

## Best Practices

1. **Odd Number of Managers**: Use 3, 5, or 7 managers for fault tolerance
2. **Separate Manager/Worker Roles**: Don't run workloads on managers in production
3. **Resource Constraints**: Always set memory/cpu limits
4. **Health Checks**: Configure health checks for services
5. **Rolling Updates**: Use update-parallelism for zero-downtime deployments
6. **Monitoring**: Use global services for monitoring agents
7. **Regular Backups**: Backup SwarmKit manager data directories

## References

- **SwarmKit GitHub**: https://github.com/moby/swarmkit
- **SwarmKit Design Docs**: https://github.com/moby/swarmkit/tree/master/design
- **Go Documentation**: https://pkg.go.dev/github.com/moby/swarmkit
- **SwarmCracker Research**: See [overview.md](overview.md)

## Summary

- ✅ **SwarmKit** = Orchestration engine (standalone)
- ✅ **Docker Swarm** = Product using SwarmKit (built into Docker)
- ✅ **SwarmCracker** = Custom SwarmKit executor for Firecracker microVMs
- ✅ **No Docker required** - Use swarmd/swarmctl directly
- ✅ **Production-grade** - Same engine as Docker Swarm

**Key Message**: SwarmCracker integrates with SwarmKit directly, giving you microVM orchestration without needing Docker Swarm or Kubernetes complexity.

---

**Last Updated**: 2026-02-01
**See Also**: [overview.md](overview.md) | [System Architecture](../../architecture/system.md)
