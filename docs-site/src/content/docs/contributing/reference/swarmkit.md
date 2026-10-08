---
title: "SwarmKit Package Reference"
description: "`pkg/swarmkit/` — Executor, Controller, VMM Manager, and Task Translator."
---

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
├── mocks_test.go       # Test mocks
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
    NATEnabled       *bool    `yaml:"nat_enabled"`
    VXLANEnabled     bool     `yaml:"vxlan_enabled"`
    VXLANPeers       []string `yaml:"vxlan_peers"`
    Debug            bool     `yaml:"debug"`
    ReservedCPUs     int      `yaml:"reserved_cpus"`
    ReservedMemoryMB int      `yaml:"reserved_memory_mb"`
    MaxImageAgeDays  int      `yaml:"max_image_age_days"`
    StateDir         string   `yaml:"state_dir"`
    LogDir           string   `yaml:"log_dir"`
    GoldenDir        string   `yaml:"golden_dir"`
    KernelProfiles   map[string]string `yaml:"-"`

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
func (e *Executor) Configure(ctx context.Context, node *api.Node) error
```

**Purpose:** Apply SwarmKit node state to the executor.

**Parameters:**
- `ctx` — Context for cancellation
- `node` — SwarmKit node being configured

Currently a no-op (node state is read directly when needed).

---

#### Controller

```go
func (e *Executor) Controller(t *api.Task) (swarmkit_exec.Controller, error)
```

**Purpose:** Return the controller for a task, creating it on first request.

**Parameters:**
- `t` — SwarmKit task specification

**Returns:**
- `Controller` — Task controller for lifecycle management (a cached controller is returned if one already exists)
- `error` — Creation error

---

#### Describe

```go
func (e *Executor) Describe(ctx context.Context) (*api.NodeDescription, error)
```

**Purpose:** Report host resources (CPUs, memory, Firecracker/KVM availability) to SwarmKit.

---

#### SetNetworkBootstrapKeys

```go
func (e *Executor) SetNetworkBootstrapKeys(keys []*api.EncryptionKey) error
```

**Purpose:** Store VXLAN overlay encryption keys and apply them to the network manager.

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
    task         *api.Task
    config       *Config
    imagePrep    types.ImagePreparer
    networkMgr   types.NetworkManager
    volumeMgr    *storage.VolumeManager
    secretMgr    *storage.SecretManager
    vmmMgr       VMMManagerInterface
    trans        types.TaskTranslator
    mu           sync.Mutex
    prepared     bool
    started      bool
    startTime    time.Time
    internalTask *types.Task
    socketPath   string
    logger       zerolog.Logger
    OnRemove     func()
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
2. Prepare rootfs image via `ImagePreparer.Prepare()` (or a golden image)
3. Prepare TAP device and network via `NetworkManager.PrepareNetwork()`
4. Prepare volumes via `VolumeManager`
5. Inject secrets/configs

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

#### Shutdown

```go
func (c *Controller) Shutdown(ctx context.Context) error
```

**Purpose:** Gracefully stop the VM.

**Steps:**
1. Send graceful shutdown signal via `VMMManager.Stop()`
2. Clean up the task network
3. Mark the task as not started

---

#### Terminate

```go
func (c *Controller) Terminate(ctx context.Context) error
```

**Purpose:** Forcefully terminate the VM without a grace period.

**Steps:**
1. Force kill the VM process via `VMMManager.Stop()` with the caller's context
2. Mark the task as not started

---

#### Update

```go
func (c *Controller) Update(ctx context.Context, t *api.Task) error
```

**Purpose:** Update the task spec before it starts; a no-op for already-started
tasks (SwarmKit creates a new task for rolling updates).

---

#### Remove

```go
func (c *Controller) Remove(ctx context.Context) error
```

**Purpose:** Remove task and cleanup resources.

**Cleanup Steps:**
1. Sync volume data back
2. Clean up the task network (`NetworkManager.CleanupNetwork()`)
3. Remove VM (stops if running and removes sockets)
4. Remove the rootfs image
5. Remove controller from executor

---

#### ContainerStatus / PortStatus

```go
func (c *Controller) ContainerStatus(ctx context.Context) (*api.ContainerStatus, error)
func (c *Controller) PortStatus(ctx context.Context) (*api.PortStatus, error)
```

**Purpose:** Report the VM's status (task ID, PID, exit code) to SwarmKit.

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
    firecrackerPath string
    jailerPath      string
    socketDir       string
    useJailer       bool
    jailerConfig    *jailer.Config
    jailer          *jailer.Jailer
    cgroupMgr       *jailer.CgroupManager
    processes       map[string]*exec.Cmd
    processWaits    map[string]*processWait
    processMutex    sync.Mutex
    consoles        map[string]*console.Server
    consoleMutex    sync.Mutex
    logFiles        map[string]*os.File
    logMutex        sync.Mutex
    logDir          string
    logger          zerolog.Logger
}
```

### VM States

>`pkg/swarmkit` does not define a `VMState` type; VM state constants live in
>`pkg/lifecycle` (see the [Lifecycle Reference](lifecycle.md#vm-states)).

### Constructor

```go
func NewVMMManager(firecrackerPath, socketDir string) (*VMMManager, error)
```

**Parameters:**
- `firecrackerPath` — path to the Firecracker binary
- `socketDir` — directory for Firecracker API sockets

**Returns:**
- `*VMMManager` — the manager
- `error` — construction error (e.g. binary not found)

`NewVMMManagerWithConfig(cfg *VMMManagerConfig) (*VMMManager, error)` is the
advanced constructor used when jailer/cgroup options are needed.

---

### Methods

The exported method set is:

```go
Start(ctx context.Context, task *types.Task, config interface{}) error
Stop(ctx context.Context, task *types.Task) error
ForceStop(ctx context.Context, task *types.Task) error
Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error)
GetPID(taskID string) int
CheckVMAPIHealth(ctx context.Context, taskID string) bool
IsRunning(taskID string) bool
Remove(ctx context.Context, task *types.Task) error
Describe(ctx context.Context, task *types.Task) (*types.TaskStatus, error)
GetRunningProcesses() map[string]*exec.Cmd
RemoveProcess(taskID string)
```

`putAPI(ctx, socketPath, path string, data interface{}) error` is the internal
helper that performs the `PUT` calls against the Firecracker API socket.

#### Start

```go
func (v *VMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error
```

**Purpose:** Start a Firecracker VM for the task.

**Steps:**
1. Find Firecracker binary
2. Start process with API socket
3. Wait for API server ready (10s timeout)
4. Configure VM via HTTP API
5. Send InstanceStart action
6. Track VM process

---

#### Stop

```go
func (v *VMMManager) Stop(ctx context.Context, task *types.Task) error
```

**Purpose:** Stop a running VM gracefully (SIGTERM, then SIGKILL after 10s).

---

#### ForceStop

```go
func (v *VMMManager) ForceStop(ctx context.Context, task *types.Task) error
```

**Purpose:** Kill the VM process immediately without a grace period.

---

#### Wait

```go
func (v *VMMManager) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error)
```

**Purpose:** Wait for the VM process to exit and return its task status.

---

#### GetPID

```go
func (v *VMMManager) GetPID(taskID string) int
```

**Purpose:** Return the Firecracker process PID for a task (0 if not running).

---

#### CheckVMAPIHealth

```go
func (v *VMMManager) CheckVMAPIHealth(ctx context.Context, taskID string) bool
```

**Purpose:** Query the VM's API socket to check liveness.

---

#### IsRunning

```go
func (v *VMMManager) IsRunning(taskID string) bool
```

**Purpose:** Report whether the VM process is alive (and not a zombie).

---

#### Remove

```go
func (v *VMMManager) Remove(ctx context.Context, task *types.Task) error
```

**Purpose:** Stop the VM if running and remove its socket and tracking state.

---

#### Describe

```go
func (v *VMMManager) Describe(ctx context.Context, task *types.Task) (*types.TaskStatus, error)
```

**Purpose:** Return the current status of the VM.

---

#### GetRunningProcesses

```go
func (v *VMMManager) GetRunningProcesses() map[string]*exec.Cmd
```

**Purpose:** Return a copy of the task-ID → process map.

---

#### RemoveProcess

```go
func (v *VMMManager) RemoveProcess(taskID string)
```

**Purpose:** Drop a task from process tracking and close its console.

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
    VcpuCount       int  `json:"vcpu_count"`
    MemSizeMib      int  `json:"mem_size_mib"`
    HtEnabled       bool `json:"ht_enabled"`
    TrackDirtyPages bool `json:"track_dirty_pages,omitempty"`
}

type NetworkInterface struct {
    IfaceID     string `json:"iface_id"`
    GuestMac    string `json:"guest_mac,omitempty"`
    HostDevName string `json:"host_dev_name,omitempty"`
}

type Action struct {
    ActionType string `json:"action_type"`
    TimeoutMS  int    `json:"timeout_ms,omitempty"`
}
```

---

## Translator

**File:** `translator.go`

The `Translator` converts SwarmKit task specifications into Firecracker VM configurations.

### Type Definition

The implementation type is unexported; callers use the `types.TaskTranslator`
interface returned by the constructors.

```go
type taskTranslatorImpl struct {
    // kernelPath, bridgeIP, kernelProfiles (private)
}
```

### Constructor

```go
func NewTaskTranslator(kernelPath, bridgeIP string) (types.TaskTranslator, error)
func NewTaskTranslatorWithProfiles(kernelPath, bridgeIP string, kernelProfiles map[string]string) (types.TaskTranslator, error)
```

---

### Methods

#### Translate

```go
func (t *taskTranslatorImpl) Translate(task *types.Task) (interface{}, error)
```

**Purpose:** Convert a SwarmKit task to a Firecracker VM config (a
`map[string]interface{}` consumed by the VMM manager).

**Translation Steps:**
1. Extract container spec from task
2. Determine vCPUs and memory from task resources or defaults
3. Build kernel boot args
4. Configure drives (rootfs path)
5. Setup network config
6. Apply init system settings

**Example Output:**

```go
map[string]interface{}{
    "boot-source": map[string]interface{}{
        "kernel_image_path": "/usr/share/firecracker/vmlinux",
        "boot_args":         "console=ttyS0 reboot=k panic=1 pci=off ip=dhcp",
    },
    "drives": []map[string]interface{}{
        {
            "drive_id":       "task-abc123",
            "path_on_host":   "/var/lib/firecracker/rootfs/<id>.ext4",
            "is_root_device": true,
            "is_read_only":   false,
        },
    },
    "machine-config": map[string]interface{}{
        "vcpu_count":   2,
        "mem_size_mib": 1024,
        "smt":          false,
    },
    "network-interfaces": []map[string]interface{}{ /* … */ },
}
```

---

## Interfaces

**File:** `interfaces.go`

### VMMManagerInterface

```go
type VMMManagerInterface interface {
    Start(ctx context.Context, task *types.Task, config interface{}) error
    Stop(ctx context.Context, task *types.Task) error
    ForceStop(ctx context.Context, task *types.Task) error
    Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error)
    Remove(ctx context.Context, task *types.Task) error
    GetPID(taskID string) int
    CheckVMAPIHealth(ctx context.Context, taskID string) bool
    IsRunning(taskID string) bool
    GetRunningProcesses() map[string]*exec.Cmd
    RemoveProcess(taskID string)
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
| Architecture overview | [Architecture Overview](/architecture/) |

---

**See Also:** [SwarmKit Integration Guide](/architecture/swarmkit/) | [SwarmKit User Guide](/guides/swarmkit/)