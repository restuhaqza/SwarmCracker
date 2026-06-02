# SwarmKit Package Reference

> `pkg/swarmkit/` — Executor, Controller, VMM Manager, and Task Translator.

---

## Overview

The `pkg/swarmkit` package implements the SwarmKit executor interface, transforming SwarmKit container tasks into Firecracker microVMs. It is the core integration point between SwarmKit's orchestration and Firecracker's VM execution.

**Package Structure:**

```
pkg/swarmkit/
├── executor.go         # Executor implementation (SwarmKit interface)
├── vmm.go              # VMM Manager (Firecracker process management)
├── translator.go       # Task → Firecracker config translation
├── interfaces.go       # Interface definitions
├── mocks.go            # Test mocks
└── configs/            # SwarmKit configuration helpers
```

---

## Executor

**File:** `executor.go`

The `Executor` struct implements SwarmKit's `swarmkit_exec.Executor` interface, providing the bridge between SwarmKit task scheduling and Firecracker VM execution.

### Type Definition

```go
type Executor struct {
    config        *Config
    imagePrep     types.ImagePreparer
    networkMgr    types.NetworkManager
    volumeMgr     *storage.VolumeManager
    secretMgr     *storage.SecretManager
    vmmMgr        VMMManagerInterface
    controllers   map[string]*Controller
    executorMu    sync.RWMutex
    cleanupCancel context.CancelFunc
    cleanupDone   chan struct{}
    networkKeys   []*api.EncryptionKey
    cleanupMu     sync.Mutex
}
```

### Configuration

```go
type Config struct {
    FirecrackerPath  string   `yaml:"firecracker_path"`
    KernelPath       string   `yaml:"kernel_path"`
    RootfsDir        string   `yaml:"rootfs_dir"`
    SocketDir        string   `yaml:"socket_dir"`
    DefaultVCPUs     int      `yaml:"default_vcpus"`
    DefaultMemoryMB  int      `yaml:"default_memory_mb"`
    BridgeName       string   `yaml:"bridge_name"`
    Subnet           string   `yaml:"subnet"`
    BridgeIP         string   `yaml:"bridge_ip"`
    IPMode           string   `yaml:"ip_mode"`
    NATEnabled       bool     `yaml:"nat_enabled"`
    VXLANEnabled     bool     `yaml:"vxlan_enabled"`
    VXLANPeers       []string `yaml:"vxlan_peers"`
    Debug            bool     `yaml:"debug"`
    ReservedCPUs     int      `yaml:"reserved_cpus"`
    ReservedMemoryMB int      `yaml:"reserved_memory_mb"`
    MaxImageAgeDays  int      `yaml:"max_image_age_days"`
    StateDir         string   `yaml:"state_dir"`

    // Jailer configuration
    EnableJailer    bool   `yaml:"enable_jailer"`
    JailerPath      string `yaml:"jailer_path"`
    JailerUID       int    `yaml:"jailer_uid"`
    JailerGID       int    `yaml:"jailer_gid"`
    JailerChrootDir string `yaml:"jailer_chroot_dir"`
    ParentCgroup    string `yaml:"parent_cgroup"`
    CgroupVersion   string `yaml:"cgroup_version"`
    EnableCgroups   bool   `yaml:"enable_cgroups"`

    // Network identity
    Hostname      string `yaml:"hostname"`
    JoinAddr      string `yaml:"join_addr"`
    AdvertiseAddr string `yaml:"advertise_addr"`

    // Consul service discovery
    ConsulEnabled bool   `yaml:"consul_enabled"`
    ConsulAddress string `yaml:"consul_address"`
}
```

### Constructor

```go
func NewExecutor(config *Config) (*Executor, error)
```

**Parameters:**
- `config` — Executor configuration (required)

**Defaults Applied:**

| Field | Default Value |
|-------|---------------|
| `FirecrackerPath` | `"firecracker"` |
| `KernelPath` | `"/usr/share/firecracker/vmlinux"` |
| `RootfsDir` | `"/var/lib/firecracker/rootfs"` |
| `SocketDir` | `"/var/run/firecracker"` |
| `DefaultVCPUs` | `1` |
| `DefaultMemoryMB` | `512` |
| `BridgeName` | `"swarm-br0"` |
| `Subnet` | `"192.168.127.0/24"` |
| `BridgeIP` | `"192.168.127.1/24"` |
| `IPMode` | `"static"` |

**Example:**

```go
config := &swarmkit.Config{
    KernelPath:       "/usr/share/firecracker/vmlinux",
    RootfsDir:        "/var/lib/firecracker/rootfs",
    DefaultVCPUs:     2,
    DefaultMemoryMB:  1024,
    BridgeName:       "swarm-br0",
    ConsulEnabled:    true,
    ConsulAddress:    "127.0.0.1:8500",
}

exec, err := swarmkit.NewExecutor(config)
if err != nil {
    log.Fatal(err)
}
```

### SwarmKit Interface Methods

The Executor implements these SwarmKit executor interface methods:

#### Configure

```go
func (e *Executor) Configure(ctx context.Context, driver swarmkit_exec.Driver) error
```

**Purpose:** Initialize executor with SwarmKit driver for task management.

**Parameters:**
- `ctx` — Context for cancellation
- `driver` — SwarmKit driver for task queue operations

**Side Effects:**
- Creates network infrastructure (bridge, VXLAN)
- Starts Consul peer discovery watcher (if enabled)
- Initializes cleanup goroutine

---

#### Create

```go
func (e *Executor) Create(ctx context.Context, task *api.Task) (swarmkit_exec.Controller, error)
```

**Purpose:** Create a controller for a new task.

**Parameters:**
- `ctx` — Context for cancellation
- `task` — SwarmKit task specification

**Returns:**
- `Controller` — Task controller for lifecycle management
- `error` — Creation error (e.g., unsupported runtime)

**Implementation:**

```go
func (e *Executor) Create(ctx context.Context, task *api.Task) (swarmkit_exec.Controller, error) {
    // Check task runtime type
    container := task.Spec.GetContainer()
    if container == nil {
        return nil, fmt.Errorf("unsupported runtime type")
    }

    // Create controller
    ctrl := &Controller{
        task:       task,
        executor:   e,
        vmm:        e.vmmMgr,
        preparer:   e.imagePrep,
        networkMgr: e.networkMgr,
    }

    // Store controller
    e.executorMu.Lock()
    e.controllers[task.ID] = ctrl
    e.executorMu.Unlock()

    return ctrl, nil
}
```

---

#### Close

```go
func (e *Executor) Close() error
```

**Purpose:** Shutdown executor and cleanup all resources.

**Side Effects:**
- Stops cleanup goroutine
- Removes all controllers
- Cleanup network infrastructure

---

## Controller

**File:** `executor.go`

The `Controller` manages the lifecycle of a single task/VM.

### Type Definition

```go
type Controller struct {
    task       *api.Task
    executor   *Executor
    vmm        VMMManagerInterface
    preparer   types.ImagePreparer
    networkMgr types.NetworkManager
    
    mu         sync.RWMutex
    closed     bool
}
```

### Methods

#### Prepare

```go
func (c *Controller) Prepare(ctx context.Context) error
```

**Purpose:** Prepare task for execution (image prep, network setup).

**Steps:**
1. Validate task runtime (must be container)
2. Prepare rootfs image via `ImagePreparer.Prepare()`
3. Setup TAP device and network via `NetworkManager.CreateTapDevice()`
4. Allocate IP address
5. Prepare volumes via `VolumeManager`
6. Inject secrets/configs

---

#### Start

```go
func (c *Controller) Start(ctx context.Context) error
```

**Purpose:** Start the Firecracker VM.

**Steps:**
1. Translate task to Firecracker config
2. Start Firecracker process via `VMMManager.Start()`
3. Configure VM (kernel, rootfs, network, machine config)
4. Send InstanceStart action
5. Wait for VM to reach running state

---

#### Wait

```go
func (c *Controller) Wait(ctx context.Context) error
```

**Purpose:** Wait for task completion.

**Returns:**
- `nil` — Task completed successfully
- `error` — Task failed or context canceled

---

#### Stop

```go
func (c *Controller) Stop(ctx context.Context) error
```

**Purpose:** Gracefully stop the VM.

**Steps:**
1. Send graceful shutdown signal
2. Wait for init grace period
3. Force kill if still running

---

#### Remove

```go
func (c *Controller) Remove(ctx context.Context) error
```

**Purpose:** Remove task and cleanup resources.

**Cleanup Steps:**
1. Stop VM (if running)
2. Remove TAP device
3. Release IP allocation
4. Cleanup jailer directory (if enabled)
5. Remove controller from executor

---

#### Close

```go
func (c *Controller) Close() error
```

**Purpose:** Close controller without cleanup (for failed tasks).

---

## VMMManager

**File:** `vmm.go`

The `VMMManager` manages Firecracker VM processes and API communication.

### Type Definition

```go
type VMMManager struct {
    config    *VMMConfig
    vms       map[string]*VMInstance
    mu        sync.RWMutex
    socketDir string
}

type VMInstance struct {
    ID             string
    PID            int
    Config         interface{}
    state          VMState
    CreatedAt      time.Time
    SocketPath     string
    InitSystem     string
    GracePeriodSec int
    mu             sync.RWMutex
}
```

### VM States

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

### Constructor

```go
func NewVMMManager(config interface{}) VMMManager
```

**Parameters:**
- `config` — VMMConfig or any config interface

---

### Methods

#### Start

```go
func (vm *VMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error
```

**Purpose:** Start a Firecracker VM for the task.

**Steps:**
1. Find Firecracker binary
2. Start process with API socket
3. Wait for API server ready (10s timeout)
4. Configure VM via HTTP API
5. Send InstanceStart action
6. Track VM instance

---

#### Stop

```go
func (vm *VMMManager) Stop(ctx context.Context, taskID string) error
```

**Purpose:** Stop a running VM.

---

#### Pause

```go
func (vm *VMMManager) Pause(ctx context.Context, taskID string) error
```

**Purpose:** Pause VM (for snapshot).

---

#### Resume

```go
func (vm *VMMManager) Resume(ctx context.Context, taskID string) error
```

**Purpose:** Resume paused VM.

---

#### GetInfo

```go
func (vm *VMMManager) GetInfo(taskID string) (*VMInstance, error)
```

**Purpose:** Get VM instance info.

---

#### List

```go
func (vm *VMMManager) List() []string
```

**Purpose:** List all managed VM IDs.

---

#### Cleanup

```go
func (vm *VMMManager) Cleanup(ctx context.Context, taskID string) error
```

**Purpose:** Cleanup VM resources (socket, state).

---

### Firecracker API Types

```go
type BootSource struct {
    KernelImagePath string  `json:"kernel_image_path"`
    BootArgs        string  `json:"boot_args,omitempty"`
}

type Drive struct {
    DriveID      string `json:"drive_id"`
    IsRootDevice bool   `json:"is_root_device"`
    IsReadOnly   bool   `json:"is_read_only"`
    PathOnHost   string `json:"path_on_host"`
}

type MachineConfig struct {
    VCPUs      int  `json:"vcpu_count"`
    MemSizeMib int  `json:"mem_size_mib"`
    HtEnabled  bool `json:"ht_enabled"`
}

type ActionsType struct {
    ActionType string `json:"action_type"`
}
```

---

## Translator

**File:** `translator.go`

The `Translator` converts SwarmKit task specifications into Firecracker VM configurations.

### Type Definition

```go
type TaskTranslator struct {
    config *Config
}

type Config struct {
    KernelPath    string
    InitrdPath    string
    DefaultVCPUs  int
    DefaultMemMB  int
    InitSystem    string
    NetworkConfig types.NetworkConfig
}
```

### Constructor

```go
func NewTaskTranslator(config *Config) *TaskTranslator
```

---

### Methods

#### Translate

```go
func (t *TaskTranslator) Translate(task *api.Task) (*types.VMConfig, error)
```

**Purpose:** Convert SwarmKit task to Firecracker VM config.

**Translation Steps:**
1. Extract container spec from task
2. Determine vCPUs and memory from task resources or defaults
3. Build kernel boot args
4. Configure drives (rootfs path)
5. Setup network config
6. Apply init system settings

**Example Output:**

```go
vmConfig := &types.VMConfig{
    KernelPath: "/usr/share/firecracker/vmlinux",
    BootArgs:   "console=ttyS0 reboot=k panic=1 pci=off ip=dhcp",
    RootfsPath: "/var/lib/firecracker/rootfs/nginx-alpine.ext4",
    VCPUs:      2,
    MemoryMB:   1024,
    Network: &types.NetworkConfig{
        TapDevice:  "tap-abc123",
        IPAddress:  "192.168.127.42",
        Gateway:    "192.168.127.1",
    },
    InitSystem:     "tini",
    GracePeriodSec: 10,
}
```

---

## Interfaces

**File:** `interfaces.go`

### VMMManagerInterface

```go
type VMMManagerInterface interface {
    Start(ctx context.Context, task *types.Task, config interface{}) error
    Stop(ctx context.Context, taskID string) error
    Pause(ctx context.Context, taskID string) error
    Resume(ctx context.Context, taskID string) error
    GetInfo(taskID string) (*VMInstance, error)
    List() []string
    Cleanup(ctx context.Context, taskID string) error
}
```

---

## Consul Integration

When `ConsulEnabled` is true, the executor:

1. Registers service in Consul catalog
2. Starts peer watcher via `WatchPeers()`
3. Updates VXLAN FDB on peer changes

```go
// Consul client creation
consulClient, err := discovery.NewConsulClient(discovery.ConsulConfig{
    Address:       config.ConsulAddress,
    ServiceID:     config.Hostname,
    LocalIP:       localIP,
    LocalHostname: config.Hostname,
    VXLANPort:     4789,
})

// Register and watch
consulClient.RegisterService(vxlanID, config.BridgeIP)
networkMgr.SetNodeDiscovery(consulClient)

go consulClient.WatchPeers(ctx, func(peers []string) {
    networkMgr.UpdateVXLANPeers(peers)
})
```

---

## Error Handling

### Common Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `"config cannot be nil"` | Nil config passed | Provide valid Config struct |
| `"unsupported runtime type"` | Task not container | Use container runtime tasks |
| `"VM already exists for task"` | Duplicate task ID | Wait for cleanup or force remove |
| `"firecracker binary not found"` | Firecracker not installed | Install Firecracker v1.15+ |
| `"firecracker API server not ready"` | Socket timeout | Check Firecracker process |

---

## Testing

The package includes comprehensive test coverage with mocks:

```go
// Create mock executor for testing
mockVMM := &MockVMMManager{}
mockNetwork := &MockNetworkManager{}
mockPreparer := &MockImagePreparer{}

exec := &Executor{
    vmmMgr:      mockVMM,
    networkMgr:  mockNetwork,
    imagePrep:   mockPreparer,
    controllers: make(map[string]*Controller),
}
```

---

## Related Documentation

| Topic | Document |
|-------|----------|
| Network internals | [Network Reference](network.md) |
| VM lifecycle | [Lifecycle Reference](lifecycle.md) |
| Image preparation | [Image Reference](image.md) |
| Architecture overview | [Architecture Overview](../../architecture/overview.md) |

---

**See Also:** [SwarmKit Integration Guide](../architecture/swarmkit-integration.md) | [SwarmKit User Guide](../../user/guides/swarmkit.md)