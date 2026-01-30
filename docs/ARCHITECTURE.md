# SwarmCracker Architecture

## High-Level Architecture

```mermaid
graph TB
    subgraph "SwarmKit Orchestration Layer"
        MGR[SwarmKit Manager<br/>Orchestrator, Scheduler<br/>Allocator, Dispatcher]
        AGENT[SwarmKit Agent]
    end

    subgraph "SwarmCracker Executor Layer"
        EXEC[SwarmCracker Executor]
        TRANS[Task Translator]
        IMG[Image Preparer]
        NET[Network Manager]
        VMM[VMM Manager]
    end

    subgraph "Firecracker Layer"
        FC[Firecracker API]
        MICROVM[MicroVM<br/>KVM Isolation]
    end

    MGR -->|gRPC| AGENT
    AGENT -->|Executor API| EXEC
    EXEC --> TRANS
    EXEC --> IMG
    EXEC --> NET
    EXEC --> VMM
    TRANS -->|VM Config| FC
    IMG -->|Rootfs| FC
    NET -->|TAP/Bridge| FC
    VMM -->|Lifecycle| FC
    FC --> MICROVM

    style MGR fill:#2496ED,stroke:#0D4771,color:#fff
    style AGENT fill:#2496ED,stroke:#0D4771,color:#fff
    style EXEC fill:#E8652D,stroke:#B8400A,color:#fff
    style TRANS fill:#6DB33F,stroke:#3A6A1F,color:#fff
    style IMG fill:#6DB33F,stroke:#3A6A1F,color:#fff
    style NET fill:#6DB33F,stroke:#3A6A1F,color:#fff
    style VMM fill:#6DB33F,stroke:#3A6A1F,color:#fff
    style FC fill:#FF6B35,stroke:#C44D1D,color:#fff
    style MICROVM fill:#FFD23F,stroke:#CCAA00,color:#000
```

## Component Overview

SwarmCracker bridges SwarmKit orchestration with Firecracker microVMs through a pluggable executor architecture.

## Detailed Component Flow

```mermaid
graph LR
    subgraph "SwarmKit Worker"
        A[Agent API]
    end

    subgraph "SwarmCracker Components"
        B[SwarmCracker Executor]
        C[Task Translator]
        D[Image Preparer]
        E[Network Manager]
        F[VMM Manager]
    end

    subgraph "Firecracker Resources"
        G[Rootfs Images]
        H[TAP Devices & Bridges]
        I[Firecracker API Socket]
    end

    subgraph "Execution Environment"
        J[Firecracker MicroVM]
    end

    A -->|Prepare/Start/Wait/Stop| B
    B --> C
    B --> D
    B --> E
    B --> F
    C -->|Config JSON| I
    D --> G
    E --> H
    F --> I
    G --> I
    H --> I
    I --> J

    style A fill:#2496ED,stroke:#0D4771,color:#fff
    style B fill:#E8652D,stroke:#B8400A,color:#fff
    style C fill:#6DB33F,stroke:#3A6A1F,color:#fff
    style D fill:#6DB33F,stroke:#3A6A1F,color:#fff
    style E fill:#6DB33F,stroke:#3A6A1F,color:#fff
    style F fill:#6DB33F,stroke:#3A6A1F,color:#fff
    style G fill:#95A5A6,stroke:#7F8C8D,color:#fff
    style H fill:#95A5A6,stroke:#7F8C8D,color:#fff
    style I fill:#FF6B35,stroke:#C44D1D,color:#fff
    style J fill:#FFD23F,stroke:#CCAA00,color:#000
```

## Package Structure

| Package | Purpose | Status | Test Coverage |
|---------|---------|--------|---------------|
| `pkg/types` | Shared data structures & interfaces | ✅ Complete | N/A |
| `pkg/executor` | Main executor implementation | ✅ Complete | 95.2% |
| `pkg/translator` | Task → VM config conversion | ✅ Complete | 98.1% |
| `pkg/config` | Configuration management | ✅ Complete | 87.3% |
| `pkg/lifecycle` | VM start/stop/monitor | ✅ Complete | 54.4% |
| `pkg/network` | TAP/bridge network management | ✅ Complete | 9.1% |
| `pkg/image` | OCI image → root filesystem | ✅ Complete | 0% (pending) |
| `test/mocks` | Mock implementations for testing | ✅ Complete | N/A |

## Data Flow

### Sequence Diagram

```mermaid
sequenceDiagram
    participant SK as SwarmKit
    participant Agent as SwarmKit Agent
    participant Exec as Executor
    participant Trans as Translator
    participant Img as Image Preparer
    participant Net as Network Manager
    participant VMM as VMM Manager
    participant FC as Firecracker
    participant VM as MicroVM

    SK->>Agent: Assign Task
    Agent->>Exec: Prepare(ctx, task)
    Exec->>Trans: Translate Task
    Trans-->>Exec: VM Config JSON
    Exec->>Img: Prepare Rootfs
    Img->>Img: Pull OCI Image
    Img->>Img: Extract Rootfs
    Img-->>Exec: Rootfs Path
    Exec->>Net: Setup Network
    Net->>Net: Create TAP Device
    Net->>Net: Attach to Bridge
    Net-->>Exec: TAP Device Name
    Exec->>VMM: Create VM Socket
    Exec->>FC: PUT /machine-config
    Exec->>FC: PUT /boot-source
    Exec->>FC: PUT /drives
    Exec->>FC: PUT /network-interfaces
    Exec->>Agent: Prepare Complete
    Agent->>Exec: Start(ctx, task)
    Exec->>FC: PUT /actions (InstanceStart)
    FC->>VM: Launch MicroVM
    VM-->>FC: Running
    FC-->>Exec: VM Started
    Exec->>Agent: Start Complete
    Agent->>Exec: Wait(ctx, task)
    Exec->>FC: GET /machine-config
    FC-->>Exec: VM Status
    Exec->>Agent: Task Status
    Agent->>Exec: Stop(ctx, task)
    Exec->>FC: PUT /actions (InstanceStop)
    FC->>VM: Stop MicroVM
    Exec->>VMM: Cleanup Socket
    Exec->>Net: Remove TAP
    Exec->>Agent: Remove Complete
```

### Process Flow

1. **Task Assignment** - SwarmKit dispatcher assigns task to agent
2. **Translation** - Task translator converts to Firecracker config
3. **Image Prep** - OCI image converted to root filesystem
4. **Network Setup** - TAP devices created and attached
5. **VM Launch** - Firecracker VMM starts microVM
6. **Monitoring** - Executor tracks VM status
7. **Cleanup** - On completion, resources are freed

## Integration Points

### With SwarmKit

SwarmCracker implements the `executor.Executor` interface from SwarmKit:

```go
type Executor interface {
    Prepare(ctx context.Context, t *Task) error
    Start(ctx context.Context, t *Task) error
    Wait(ctx context.Context, t *Task) (*TaskStatus, error)
    Stop(ctx context.Context, t *Task) error
    Remove(ctx context.Context, t *Task) error
}
```

### With Firecracker

SwarmCracker uses Firecracker's REST API:

```bash
PUT /boot-source      # Configure kernel
PUT /machine-config   # Set resources
PUT /drives           # Attach rootfs & volumes
PUT /network-interfaces  # Setup networking
PUT /actions          # Start/stop VM
```

## Security Model

```mermaid
graph TB
    subgraph HOST["Host Machine"]
        direction TB
        EXECUTOR["SwarmCracker Executor<br/>(privileged daemon)"]
        VMM_PROC["Firecracker VMM Process<br/>(unprivileged user)"]
        
        subgraph MICROVM["Firecracker MicroVM<br/>(KVM Isolation)"]
            WORKLOAD["Container Workload<br/>(Cannot access host or other VMs)"]
        end
    end

    EXECUTOR --> VMM_PROC
    VMM_PROC --> MICROVM
    MICROVM --> WORKLOAD

    style EXECUTOR fill:#E8652D,stroke:#B8400A,color:#fff,stroke-width:3px
    style VMM_PROC fill:#FF6B35,stroke:#C44D1D,color:#fff,stroke-width:2px
    style MICROVM fill:#FFD23F,stroke:#CCAA00,color:#000,stroke-width:2px
    style WORKLOAD fill:#6DB33F,stroke:#3A6A1F,color:#fff
    style HOST fill:#2C3E50,stroke:#1A252F,color:#fff,stroke-width:4px
```

### Security Boundaries

| Layer | Privilege Level | Isolation Mechanism |
|-------|----------------|---------------------|
| **SwarmCracker Executor** | Privileged (root) | Systemd service limits |
| **Firecracker VMM** | Unprivileged user | User namespace, chroot |
| **MicroVM** | None | KVM hardware virtualization |
| **Workload** | None | Kernel namespace isolation |

## Future Enhancements

- [ ] VM snapshot support for instant startup
- [ ] Live migration between hosts
- [ ] Custom metrics via vsock
- [ ] exec into container
- [ ] Log aggregation
- [ ] Health check integration
