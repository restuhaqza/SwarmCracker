# Architecture Overview

> System design, components, and integration details.

---

## System Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                        SwarmCracker Cluster                          │
│                                                                      │
│   ┌─────────────────────────────────────────────────────────────┐  │
│   │                    SwarmKit Control Plane                    │  │
│   │  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐       │  │
│   │  │ Manager-1    │  │ Manager-2    │  │ Manager-3    │       │  │
│   │  │ (Raft Leader)│  │ (Raft Follower)│ │ (Raft Follower)│    │  │
│   │  │ Port 4242    │  │ Port 4242    │  │ Port 4242    │       │  │
│   │  └──────────────┘  └──────────────┘  └──────────────┘       │  │
│   └─────────────────────────────┬───────────────────────────────┘  │
│                                 │ gRPC                              │
│   ┌─────────────────────────────┴───────────────────────────────┐  │
│   │                    Worker Nodes                              │  │
│   │  ┌──────────────────┐  ┌──────────────────┐                │  │
│   │  │ Worker-1         │  │ Worker-2         │                │  │
│   │  │ SwarmKit Agent   │  │ SwarmKit Agent   │                │  │
│   │  │ Firecracker Exec │  │ Firecracker Exec │                │  │
│   │  │ ┌───┐┌───┐┌───┐ │  │ ┌───┐┌───┐      │                │  │
│   │  │ │VM1││VM2││VM3│ │  │ │VM4││VM5│      │                │  │
│   │  │ └───┘└───┘└───┘ │  │ └───┘└───┘      │                │  │
│   │  └──────────────────┘  └──────────────────┘                │  │
│   │         swarm-br0              swarm-br0                    │  │
│   └─────────────────────────────┬───────────────────────────────┘  │
│                                 │ VXLAN Overlay (UDP 4789)         │
│   ┌─────────────────────────────────────────────────────────────┐  │
│   │                    Network Layer                             │  │
│   │  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐       │  │
│   │  │ TAP Devices  │  │ Linux Bridge │  │ VXLAN Tunnel │       │  │
│   │  │ (per VM)     │  │ (per node)   │  │ (cross-node) │       │  │
│   │  └──────────────┘  └──────────────┘  └──────────────┘       │  │
│   └─────────────────────────────────────────────────────────────┘  │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
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

```
User → swarmctl → Manager → Scheduler → Worker → Executor → Firecracker VM
         │          │          │          │          │
         │          │          │          │          ▼
         │          │          │          │    Prepare (VM setup)
         │          │          │          │          │
         │          │          │          │          ▼
         │          │          │          │    Start (VM boot)
         │          │          │          │          │
         │          │          │          │          ▼
         │          │          │          │    Wait (monitor)
         │          │          │          │          │
         │          │          │          ▼          │
         │          │          │    Report Status    │
         │          │          │          │          │
         │          │          ▼          │          │
         │          │    Update State Store         │
         │          │          │          │          │
         ▼          ▼          ▼          ▼          ▼
      Done      Reconcile   Persist    Complete  Running
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

```
┌─────────────────────────────────────────────────┐
│  Host System                                    │
│  ┌─────────────────────────────────────────┐   │
│  │  Jailer Sandbox                          │   │
│  │  ┌───────────────────────────────────┐  │   │
│  │  │  Firecracker Process               │  │   │
│  │  │  - PID namespace (isolated)        │  │   │
│  │  │  - Network namespace (isolated)    │  │   │
│  │  │  - Chroot (/var/lib/jailer/<vm>)   │  │   │
│  │  │  - Cgroups (CPU/memory limits)     │  │   │
│  │  │  - Seccomp (syscall filter)        │  │   │
│  │  └───────────────────────────────────┘  │   │
│  └─────────────────────────────────────────┘   │
└─────────────────────────────────────────────────┘
```

---

## Networking Model

### Single Node

```
┌─────────────────────────────────────────────────┐
│  Host                                           │
│  ┌─────────────────────────────────────────┐   │
│  │  swarm-br0 (192.168.127.1/24)            │   │
│  └──────────────┬──────────────────────────┘   │
│         ┌───────┴───────┐                       │
│    ┌────▼───┐       ┌───▼────┐                  │
│    │ tap0   │       │ tap1   │                  │
│    └────┬───┘       └───┬────┘                  │
└─────────┼───────────────┼────────────────────────┘
     ┌────▼───┐       ┌───▼────┐
     │  VM 1  │       │  VM 2  │
     └────────┘       └────────┘
```

### Multi-Node (VXLAN)

```
┌─────────────────┐          ┌─────────────────┐
│  Node 1         │          │  Node 2         │
│  swarm-br0      │          │  swarm-br0      │
│  ┌───┐┌───┐    │◄──VXLAN──►│  ┌───┐┌───┐    │
│  │VM1││VM2│    │  UDP4789 │  │VM3││VM4│    │
│  └───┘└───┘    │          │  └───┘└───┘    │
└─────────────────┘          └─────────────────┘
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

**See Also:** [Getting Started](../getting-started/) | [Guides](../guides/)