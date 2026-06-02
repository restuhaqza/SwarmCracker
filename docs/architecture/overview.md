# Architecture Overview

> High-level architecture, components, data flow, and deployment topology for SwarmCracker.

---

## System Architecture

SwarmCracker is a Firecracker microVM orchestrator built on SwarmKit. It transforms SwarmKit container tasks into hardware-isolated Firecracker microVMs, providing per-VM kernel isolation without the complexity of Kubernetes.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          SwarmCracker Cluster                                │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   ┌───────────────────────────────────────────────────────────────────┐    │
│   │                      Control Plane (Manager Nodes)                 │    │
│   │  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐            │    │
│   │  │  Manager-1  │◄──►│  Manager-2  │◄──►│  Manager-3  │            │    │
│   │  │  Raft Leader│    │Raft Follower│    │Raft Follower│            │    │
│   │  │  Port 4242  │    │  Port 4242  │    │  Port 4242  │            │    │
│   │  └──────┬──────┘    └──────┬──────┘    └──────┬──────┘            │    │
│   │         │                  │                  │                   │    │
│   │         └──────────────────┼──────────────────┘                   │    │
│   │                            │                                      │    │
│   │                    gRPC API (TLS)                                 │    │
│   └───────────────────────────────────────────────────────────────────┘    │
│                                    │                                        │
│                                    ▼                                        │
│   ┌───────────────────────────────────────────────────────────────────┐    │
│   │                      Worker Nodes (Compute Plane)                  │    │
│   │                                                                    │    │
│   │  ┌──────────────────────────┐    ┌──────────────────────────┐    │    │
│   │  │        Worker-1          │    │        Worker-2          │    │    │
│   │  │  ┌────────────────────┐  │    │  ┌────────────────────┐  │    │    │
│   │  │  │ swarmd-firecracker │  │    │  │ swarmd-firecracker │  │    │    │
│   │  │  │   (SwarmKit Agent) │  │    │  │   (SwarmKit Agent) │  │    │    │
│   │  │  └──────┬─────────────┘  │    │  └──────┬─────────────┘  │    │    │
│   │  │         │                │    │         │                │    │    │
│   │  │  ┌──────▼─────────────┐  │    │  ┌──────▼─────────────┐  │    │    │
│   │  │  │     Executor       │  │    │  │     Executor       │  │    │    │
│   │  │  │ pkg/swarmkit/      │  │    │  │ pkg/swarmkit/      │  │    │    │
│   │  │  └──────┬─────────────┘  │    │  └──────┬─────────────┘  │    │    │
│   │  │         │                │    │         │                │    │    │
│   │  │  ┌──────▼─────────────┐  │    │  ┌──────▼─────────────┐  │    │    │
│   │  │  │   Firecracker VMs  │  │    │  │   Firecracker VMs  │  │    │    │
│   │  │  │  ┌───┐ ┌───┐ ┌───┐ │  │◄──►│  │  ┌───┐ ┌───┐ ┌───┐ │  │    │    │
│   │  │  │  │VM1│ │VM2│ │VM3│ │  │VXLAN│  │VM4│ │VM5│ │VM6│ │  │    │    │
│   │  │  │  └───┘ └───┘ └───┘ │  │    │  │  └───┘ └───┘ └───┘ │  │    │    │
│   │  │  └────────────────────┘  │    │  └────────────────────┘  │    │    │
│   │  │                          │    │                          │    │    │
│   │  │     swarm-br0            │    │     swarm-br0            │    │    │
│   │  │   192.168.127.1/24       │    │   192.168.127.1/24       │    │    │
│   │  └──────────────────────────┘    └──────────────────────────┘    │    │
│   │                                                                    │    │
│   └───────────────────────────────────────────────────────────────────┘    │
│                                                                             │
│   ┌───────────────────────────────────────────────────────────────────┐    │
│   │                      Infrastructure Services                        │    │
│   │                                                                    │    │
│   │  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐            │    │
│   │  │   Consul    │    │    IPAM     │    │   VXLAN     │            │    │
│   │  │  Discovery  │    │  Allocator  │    │  Overlay    │            │    │
│   │  │  Port 8500  │    │ 192.168.127 │    │  ID 100     │            │    │
│   │  └─────────────┘    └─────────────┘    │  UDP 4789  │            │    │
│   │                                      └─────────────┘            │    │
│   └───────────────────────────────────────────────────────────────────┘    │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Core Components

### SwarmKit Integration (`pkg/swarmkit/`)

| Component | File | Purpose |
|-----------|------|---------|
| **Executor** | `executor.go` | Implements SwarmKit's `swarmkit_exec.Executor` interface |
| **Controller** | `executor.go` | Per-task controller managing VM lifecycle |
| **VMMManager** | `vmm.go` | Firecracker process management, API communication |
| **Translator** | `translator.go` | Converts SwarmKit tasks to Firecracker VM configs |

**Key Interfaces:**

```go
// Executor implements SwarmKit's executor interface
type Executor struct {
    config        *Config
    imagePrep     types.ImagePreparer
    networkMgr    types.NetworkManager
    volumeMgr     *storage.VolumeManager
    secretMgr     *storage.SecretManager
    vmmMgr        VMMManagerInterface
    controllers   map[string]*Controller
}
```

### Network Layer (`pkg/network/`)

| Component | File | Purpose |
|-----------|------|---------|
| **NetworkManager** | `manager.go` | Bridge, TAP device, VXLAN orchestration |
| **IPAllocator** | `manager.go` | Static/deterministic IP allocation from subnet |
| **VXLANManager** | `vxlan.go` | Cross-node overlay network (UDP 4789) |
| **CNIClient** | `cni.go` | CNI plugin integration for SwarmKit networks |
| **Discovery** | `discovery.go` | Consul-based peer discovery |

**Network Flow:**

```
Task assigned → IPAllocator.Allocate(task.ID) → TAP device created
                → Connect to swarm-br0 → VXLAN FDB updated
                → VM boots with assigned IP → Consul registers peer
```

### VM Lifecycle (`pkg/lifecycle/`)

| Component | File | Purpose |
|-----------|------|---------|
| **VMMManager** | `vmm.go` | VM start/stop/pause/resume operations |
| **VMInstance** | `vmm.go` | Per-VM state tracking (PID, socket, state) |
| **Firecracker API** | `vmm.go` | HTTP PUT/GET to Unix socket API |

**VM States:**

```go
type VMState string

const (
    VMStateNew      VMState = "new"
    VMStateStarting VMState = "starting"
    VMStateRunning  VMState = "running"
    VMStateStopping VMState = "stopping"
    VMStateStopped  VMState = "stopped"
    VMStateCrashed  VMState = "crashed"
)
```

### Storage (`pkg/storage/`)

| Component | File | Purpose |
|-----------|------|---------|
| **VolumeManager** | `volume.go` | Multi-driver volume orchestration |
| **DirectoryDriver** | `volume_dir.go` | Host-directory backed volumes |
| **BlockDriver** | `volume_block.go` | Block device backed volumes |
| **SecretManager** | `credential_store.go` | Secrets and configs injection |
| **MetaStore** | `volume_meta.go` | Volume metadata tracking |

**Volume Types:**

| Type | Driver | Use Case |
|------|--------|----------|
| `dir` | DirectoryDriver | Shared filesystem, development |
| `block` | BlockDriver | High-performance I/O, databases |

### Image Preparation (`pkg/image/`)

| Component | File | Purpose |
|-----------|------|---------|
| **ImagePreparer** | `preparer.go` | OCI image → ext4 rootfs conversion |
| **InitInjector** | `init.go` | Init system injection (tini, dumb-init) |
| **OCIImageInfo** | `oci_info.go` | OCI image configuration parsing |
| **Detector** | `detector.go` | Init system type detection |

**Preparation Pipeline:**

```
1. Pull OCI image (remote or local)
2. Extract layers to temp directory
3. Detect init system type (tini/dumb-init/systemd/none)
4. Inject init system if needed (InjectIntoDir)
5. Create ext4 filesystem image
6. Cache at /var/lib/firecracker/rootfs/<image-id>.ext4
```

### Security (`pkg/security/`)

| Component | File | Purpose |
|-----------|------|---------|
| **Jailer** | `jailer.go` | chroot, UID/GID drop, network namespace |
| **Seccomp** | `seccomp.go` | Syscall filtering for guest VMs |
| **SecurityManager** | `manager.go` | Orchestrates jailer + seccomp |

**Jailer Isolation:**

```
┌─────────────────────────────────────────────┐
│                  Host System                 │
│  ┌─────────────────────────────────────┐    │
│  │           Jailer Sandbox            │    │
│  │  ┌─────────────────────────────┐    │    │
│  │  │    Firecracker Process      │    │    │
│  │  │  • PID namespace isolated   │    │    │
│  │  │  • chroot /srv/jailer/<vm>  │    │    │
│  │  │  • UID/GID dropped          │    │    │
│  │  │  • Seccomp filter applied   │    │    │
│  │  └─────────────────────────────┘    │    │
│  └─────────────────────────────────────┘    │
└─────────────────────────────────────────────┘
```

### Configuration (`pkg/config/`)

| Component | File | Purpose |
|-----------|------|---------|
| **Config** | `config.go` | YAML configuration loading/validation |
| **ExecutorConfig** | `config.go` | Executor-specific settings |
| **NetworkConfig** | `config.go` | Network settings (bridge, subnet, VXLAN) |
| **SnapshotConfig** | `config.go` | Snapshot retention policies |

---

## Data Flow

### Task Execution Sequence

```
┌──────────┐     ┌──────────┐     ┌──────────┐     ┌──────────┐     ┌──────────┐
│  User    │     │ swarmctl │     │  Manager │     │  Worker  │     │ Executor │
└──────────┘     └──────────┘     └──────────┘     └──────────┘     └──────────┘
     │               │               │               │               │
     │ create-service│               │               │               │
     │──────────────►│               │               │               │
     │               │ gRPC: Create  │               │               │
     │               │──────────────►│               │               │
     │               │               │ Raft: Store   │               │
     │               │               │───┐           │               │
     │               │               │◄──┘           │               │
     │               │               │ Schedule      │               │
     │               │               │──────────────►│               │
     │               │               │               │ AssignTask   │
     │               │               │               │──────────────►│
     │               │               │               │               │ Prepare
     │               │               │               │               │───┐
     │               │               │               │               │◄──┘
     │               │               │               │               │ Start
     │               │               │               │               │───┐
     │               │               │               │               │◄──┘
     │               │               │               │ ReportStatus │
     │               │               │               │◄──────────────│
     │               │               │ UpdateState   │               │
     │               │               │───┐           │               │
     │               │               │◄──┘           │               │
     │               │ Response      │               │               │
     │◄──────────────│◄──────────────│               │               │
     │               │               │               │               │
```

### Image Preparation Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Image Preparation Pipeline                           │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   ┌─────────────┐                                                           │
│   │  OCI Image  │  nginx:alpine                                             │
│   │  Reference  │                                                           │
│   └──────┬──────┘                                                           │
│          │                                                                   │
│          ▼                                                                   │
│   ┌─────────────┐                                                           │
│   │    Pull     │  remote.Get(imageRef)                                     │
│   │   Layers    │  → v1.Image                                               │
│   └──────┬──────┘                                                           │
│          │                                                                   │
│          ▼                                                                   │
│   ┌─────────────┐                                                           │
│   │   Extract   │  mutate.Extract(image)                                    │
│   │  to TempDir │  → /tmp/swarmcracker-<id>/                                │
│   └──────┬──────┘                                                           │
│          │                                                                   │
│          ▼                                                                   │
│   ┌─────────────┐                                                           │
│   │   Detect    │  Detector.Detect(tempDir)                                 │
│   │  Init Type  │  → tini / dumb-init / systemd / none                      │
│   └──────┬──────┘                                                           │
│          │                                                                   │
│          ▼                                                                   │
│   ┌─────────────┐                                                           │
│   │   Inject    │  InitInjector.InjectIntoDir(tempDir, ociInfo)             │
│   │    Init     │  → /sbin/init symlink created                             │
│   └──────┬──────┘                                                           │
│          │                                                                   │
│          ▼                                                                   │
│   ┌─────────────┐                                                           │
│   │   Create    │  mke2fs -t ext4 -d tempDir rootfs.ext4                    │
│   │   ext4 FS   │  → /var/lib/firecracker/rootfs/<image-id>.ext4            │
│   └──────┬──────┘                                                           │
│          │                                                                   │
│          ▼                                                                   │
│   ┌─────────────┐                                                           │
│   │    Cache    │  Check if cached, reuse if valid                          │
│   │   Check     │                                                           │
│   └─────────────┘                                                           │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Deployment Topology

### Single-Node Development

```
┌─────────────────────────────────────────┐
│              Single Node                 │
│                                         │
│  ┌────────────────────────────────────┐ │
│  │        Manager + Worker            │ │
│  │                                    │ │
│  │  swarmd-firecracker --manager      │ │
│  │    • Raft (single node)            │ │
│  │    • Scheduler                     │ │
│  │    • Executor                      │ │
│  │                                    │ │
│  │  ┌─────────┐ ┌─────────┐          │ │
│  │  │   VM1   │ │   VM2   │          │ │
│  │  └─────────┘ └─────────┘          │ │
│  │                                    │ │
│  │      swarm-br0 (local only)       │ │
│  │      192.168.127.0/24              │ │
│  └────────────────────────────────────┘ │
│                                         │
└─────────────────────────────────────────┘
```

### Production Cluster (HA)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          Production Cluster (HA)                             │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                          Load Balancer                               │  │
│   │                      (HAProxy / nginx / Consul)                      │  │
│   │                       Port 4242 (SwarmKit API)                       │  │
│   └───────────────────────────────┬─────────────────────────────────────┘  │
│                                   │                                         │
│           ┌───────────────────────┼───────────────────────┐                │
│           │                       │                       │                │
│           ▼                       ▼                       ▼                │
│   ┌─────────────┐         ┌─────────────┐         ┌─────────────┐         │
│   │  Manager-1  │◄───────►│  Manager-2  │◄───────►│  Manager-3  │         │
│   │   Leader    │  Raft   │  Follower   │  Raft   │  Follower   │         │
│   │   Zone A    │         │   Zone B    │         │   Zone C    │         │
│   └──────┬──────┘         └──────┬──────┘         └──────┬──────┘         │
│          │                       │                       │                │
│          └───────────────────────┼───────────────────────┘                │
│                                  │                                         │
│                                  │ gRPC (TLS)                               │
│                                  ▼                                         │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                          Worker Pool                                 │  │
│   │                                                                     │  │
│   │  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐      │  │
│   │  │Worker-1 │ │Worker-2 │ │Worker-3 │ │Worker-4 │ │Worker-5 │      │  │
│   │  │ Zone A  │ │ Zone A  │ │ Zone B  │ │ Zone B  │ │ Zone C  │      │  │
│   │  │ 8 VMs   │ │ 8 VMs   │ │ 8 VMs   │ │ 8 VMs   │ │ 8 VMs   │      │  │
│   │  └────┬────┘ └────┬────┘ └────┬────┘ └────┬────┘ └────┬────┘      │  │
│   │       │           │           │           │           │            │  │
│   │       └───────────┴───────────┼───────────┴───────────┘            │  │
│   │                               │                                    │  │
│   │                       VXLAN Overlay                                │  │
│   │                       UDP Port 4789                                │  │
│   │                       ID: 100                                      │  │
│   │                                                                     │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                     Consul Service Discovery                         │  │
│   │                                                                     │  │
│   │  ┌─────────┐ ┌─────────┐ ┌─────────┐                               │  │
│   │  │Consul-1 │ │Consul-2 │ │Consul-3 │                               │  │
│   │  │ Zone A  │ │ Zone B  │ │ Zone C  │                               │  │
│   │  │ Port8500│ │ Port8500│ │ Port8500│                               │  │
│   │  └─────────┘ └─────────┘ └─────────┘                               │  │
│   │                                                                     │  │
│   │  Services: swarmcracker-worker (UDP 4789)                           │  │
│   │  WatchPeers() → VXLAN FDB updates                                  │  │
│   │                                                                     │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Package Dependencies

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          Package Dependency Graph                            │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│                           pkg/types (shared interfaces)                     │
│                                     │                                       │
│            ┌────────────────────────┼────────────────────────┐             │
│            │                        │                        │             │
│            ▼                        ▼                        ▼             │
│   pkg/swarmkit            pkg/network           pkg/lifecycle              │
│   (Executor)              (NetworkManager)      (VMMManager)               │
│        │                       │                      │                   │
│        │                       │                      │                   │
│        ├───────────────────────┼──────────────────────┤                   │
│        │                       │                      │                   │
│        ▼                       ▼                      ▼                   │
│   pkg/image              pkg/security            pkg/storage               │
│   (ImagePreparer)        (Jailer)                (VolumeManager)           │
│        │                       │                      │                   │
│        │                       │                      │                   │
│        ▼                       ▼                      ▼                   │
│   pkg/config             pkg/snapshot            pkg/metrics               │
│   (Config)               (Snapshot)              (Metrics)                 │
│                                                                             │
│   External Dependencies:                                                    │
│   • github.com/moby/swarmkit/v2 (SwarmKit agent/API)                       │
│   • github.com/google/go-containerregistry (OCI image handling)            │
│   • github.com/rs/zerolog (structured logging)                             │
│   • gopkg.in/yaml.v3 (configuration parsing)                               │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Resource Management

### Default Resource Limits

| Resource | Default | Configurable | Source |
|----------|---------|--------------|--------|
| VM vCPUs | 1 | `executor.default_vcpus` or task spec | Config / TaskSpec |
| VM Memory | 512 MB | `executor.default_memory_mb` or task spec | Config / TaskSpec |
| VM Disk | 1 GB | rootfs size (image-dependent) | OCI image layers |
| CPU Quota | Unlimited | `cgroup.cpu_quota` | Security package |
| Memory Limit | VM RAM | `cgroup.memory_limit` | Security package |
| File Descriptors | 4096 | `jailer.max_fd` | Security package |

### Task Spec Override

```yaml
# SwarmKit service spec
resources:
  limits:
    nano_cpus: 2000000000    # 2 vCPUs (nanoseconds)
    memory_bytes: 1073741824 # 1GB
```

---

## Network Topology

### VXLAN Cross-Node Traffic

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          VXLAN Overlay Network                               │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   Overlay Network: 192.168.127.0/24                                         │
│   VXLAN ID: 100                                                             │
│   UDP Port: 4789                                                            │
│                                                                             │
│   ┌─────────────────────┐                 ┌─────────────────────┐          │
│   │      Worker-1       │                 │      Worker-2       │          │
│   │                     │                 │                     │          │
│   │  ┌───────────────┐  │                 │  ┌───────────────┐  │          │
│   │  │   swarm-br0   │  │                 │  │   swarm-br0   │  │          │
│   │  │192.168.127.1  │  │                 │  │192.168.127.1  │  │          │
│   │  └───────┬───────┘  │                 │  └───────┬───────┘  │          │
│   │          │          │                 │          │          │          │
│   │  ┌───────▼───────┐  │                 │  ┌───────▼───────┐  │          │
│   │  │  vxlan100     │◄─┼───── UDP 4789 ──┼►│  vxlan100     │  │          │
│   │  │  (VNI: 100)   │  │                 │  │  (VNI: 100)   │  │          │
│   │  └───────┬───────┘  │                 │  └───────┬───────┘  │          │
│   │          │          │                 │          │          │          │
│   │  ┌───────▼───────┐  │                 │  ┌───────▼───────┐  │          │
│   │  │   TAP0        │  │                 │  │   TAP0        │  │          │
│   │  │192.168.127.10 │  │                 │  │192.168.127.20 │  │          │
│   │  └───────┬───────┘  │                 │  └───────┬───────┘  │          │
│   │          │          │                 │          │          │          │
│   │  ┌───────▼───────┐  │                 │  ┌───────▼───────┐  │          │
│   │  │    VM1        │◄─┼─── ICMP/Ping ───┼►│    VM3        │  │          │
│   │  │192.168.127.10 │  │    (direct)     │  │192.168.127.20 │  │          │
│   │  └───────────────┘  │                 │  └───────────────┘  │          │
│   │                     │                 │                     │          │
│   └─────────────────────┘                 └─────────────────────┘          │
│                                                                             │
│   Underlay: Physical network (192.168.1.0/24 example)                       │
│                                                                             │
│   ┌─────────────────────┐                 ┌─────────────────────┐          │
│   │   eth0 (Worker-1)   │◄──────────────►│   eth0 (Worker-2)   │          │
│   │   192.168.1.10      │    Physical     │   192.168.1.11      │          │
│   └─────────────────────┘    Network      └─────────────────────┘          │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### IP Allocation

**Static/Deterministic Allocation:**

```go
// IP is derived from SHA-256 hash of task ID
func (a *IPAllocator) hashToIP(vmID string) net.IP {
    h := sha256.New()
    h.Write([]byte(vmID))
    hash := h.Sum(nil)
    
    // Use hash to pick offset in subnet
    n := binary.BigEndian.Uint32(hash[:4]) % (size - 2)
    ip := subnetIP + n + 1
    
    return ip
}
```

**Collision Resolution:** Linear probing up to 256 attempts.

---

## Related Documentation

| Topic | Document |
|-------|----------|
| SwarmKit integration details | [SwarmKit Reference](../dev/reference/swarmkit.md) |
| Network internals | [Network Reference](../dev/reference/network.md) |
| VM lifecycle | [Lifecycle Reference](../dev/reference/lifecycle.md) |
| Storage drivers | [Storage Reference](../dev/reference/storage.md) |
| Image preparation | [Image Reference](../dev/reference/image.md) |
| Security isolation | [Security Reference](../dev/reference/security.md) |
| Configuration keys | [Config Reference](../dev/reference/config.md) |
| CLI commands | [CLI Reference](../dev/reference/cli.md) |

---

**See Also:** [User Architecture Overview](../user/architecture/README.md) | [SwarmKit Guide](../user/guides/swarmkit.md)