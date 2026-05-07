# Architecture Overview

> System design, components, and integration details.

---

## System Architecture

```mermaid
graph TB
    subgraph "SwarmCracker Cluster"
        subgraph "Control Plane"
            M1[Manager-1<br/>Raft Leader<br/>Port 4242]
            M2[Manager-2<br/>Raft Follower<br/>Port 4242]
            M3[Manager-3<br/>Raft Follower<br/>Port 4242]
        end
        
        subgraph "Worker Nodes"
            W1[Worker-1<br/>SwarmKit Agent<br/>Firecracker Exec]
            W2[Worker-2<br/>SwarmKit Agent<br/>Firecracker Exec]
        end
        
        subgraph "MicroVMs"
            VM1[VM1]
            VM2[VM2]
            VM3[VM3]
            VM4[VM4]
            VM5[VM5]
        end
        
        subgraph "Network Layer"
            TAP[TAP Devices<br/>per VM]
            BR[Linux Bridge<br/>per node]
            VX[VXLAN Tunnel<br/>cross-node]
        end
    end
    
    M1 -->|gRPC| W1
    M1 -->|gRPC| W2
    M2 -->|gRPC| W1
    M2 -->|gRPC| W2
    M3 -->|gRPC| W1
    M3 -->|gRPC| W2
    
    W1 --> VM1
    W1 --> VM2
    W1 --> VM3
    W2 --> VM4
    W2 --> VM5
    
    VM1 --> TAP
    VM2 --> TAP
    VM3 --> TAP
    VM4 --> TAP
    VM5 --> TAP
    TAP --> BR
    BR -->|VXLAN Overlay<br/>UDP 4789| VX
```

---

## Components

### SwarmKit Manager

| Component | Purpose |
|-----------|---------|
| **Raft Consensus** | Distributed decision making |
| **Scheduler** | Assigns tasks to workers |
| **State Store** | In-memory cluster state |
| **Control API** | gRPC for `swarmctl` |

### SwarmKit Worker

| Component | Purpose |
|-----------|---------|
| **Agent** | Communicates with manager |
| **Executor** | Runs tasks (SwarmCracker) |
| **Status Reporter** | Reports task state |

### SwarmCracker Executor

| Component | Purpose |
|-----------|---------|
| **Task Translator** | SwarmKit task → Firecracker config |
| **VM Manager** | Start/stop microVMs |
| **Network Setup** | TAP devices, bridges |
| **Jailer Integration** | Security sandboxing |

---

## Data Flow

### Task Execution Flow

```mermaid
sequenceDiagram
    participant U as User
    participant C as swarmctl
    participant M as Manager
    participant S as Scheduler
    participant W as Worker
    participant E as Executor
    participant F as Firecracker VM
    
    U->>C: Create service
    C->>M: gRPC: CreateTask
    M->>S: Schedule task
    S->>W: Assign task
    W->>E: Execute task
    E->>E: Prepare (VM setup)
    E->>F: Start (VM boot)
    F-->>E: VM running
    E-->>W: Report status
    W-->>M: Update state
    M-->>C: Task running
    C-->>U: Service deployed
```

---

## Packages

| Package | Description |
|---------|-------------|
| `pkg/config` | Configuration parsing |
| `pkg/executor` | SwarmKit executor implementation |
| `pkg/translator` | Task → Firecracker config translation |
| `pkg/network` | TAP/bridge/VXLAN setup |
| `pkg/jailer` | Security sandboxing |
| `pkg/storage` | Rootfs/volume management |
| `pkg/snapshot` | VM state persistence |
| `pkg/metrics` | Prometheus metrics |
| `pkg/swarmkit` | SwarmKit integration |

---

## Security Model

### Jailer Isolation

```mermaid
graph TB
    subgraph "Host System"
        subgraph "Jailer Sandbox"
            subgraph "Firecracker Process"
                PID[PID namespace<br/>isolated]
                NET[Network namespace<br/>isolated]
                CHR[Chroot<br/>/var/lib/jailer/&lt;vm&gt;]
                CG[Cgroups<br/>CPU/memory limits]
                SEC[Seccomp<br/>syscall filter]
            end
        end
    end
```

---

## Networking Model

### Single Node

```mermaid
graph TB
    subgraph "Host"
        BR[swarm-br0<br/>192.168.127.1/24]
        TAP1[tap0]
        TAP2[tap1]
        
        BR --> TAP1
        BR --> TAP2
    end
    
    VM1[VM 1]
    VM2[VM 2]
    
    TAP1 --> VM1
    TAP2 --> VM2
```

### Multi-Node (VXLAN)

```mermaid
graph LR
    subgraph "Node 1"
        BR1[swarm-br0]
        VM1[VM1]
        VM2[VM2]
        BR1 --> VM1
        BR1 --> VM2
    end
    
    subgraph "Node 2"
        BR2[swarm-br0]
        VM3[VM3]
        VM4[VM4]
        BR2 --> VM3
        BR2 --> VM4
    end
    
    BR1 <-->|VXLAN<br/>UDP 4789| BR2
```

---

## Resource Limits

| Resource | Default | Configurable |
|----------|---------|--------------|
| VM vCPUs | 2 | `default_vcpus` |
| VM Memory | 1024 MB | `default_memory_mb` |
| VM Disk | 1 GB | rootfs size |
| CPU Quota | 100% | `cgroup.cpu_quota` |
| Memory Limit | VM RAM | `cgroup.memory_limit` |

---

## Related Documentation

| Topic | Document |
|-------|----------|
| SwarmKit integration | [SwarmKit Integration](swarmkit.md) |

---

**See Also:** [Getting Started](../getting-started/) | [Guides](../guides/) | [User Docs Home](../README.md)