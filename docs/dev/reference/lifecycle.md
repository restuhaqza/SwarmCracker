# Lifecycle Package Reference

> `pkg/lifecycle/` — VM lifecycle management, Firecracker API communication.

---

## Overview

The `pkg/lifecycle` package manages Firecracker VM lifecycle operations: start, stop, pause, resume, and state tracking.

**Package Structure:**

```
pkg/lifecycle/
├── vmm.go              # VMMManager and VMInstance
├── mocks.go            # Test mocks
```

---

## VMMManager

**File:** `vmm.go`

The `VMMManager` manages Firecracker VM processes and API communication.

### Type Definition

```go
type VMMManager struct {
    config    *ManagerConfig
    vms       map[string]*VMInstance
    mu        sync.RWMutex
    socketDir string
}
```

### Configuration

```go
type ManagerConfig struct {
    KernelPath      string  // vmlinux path
    RootfsDir       string  // rootfs storage directory
    SocketDir       string  // API socket directory
    DefaultVCPUs    int     // default vCPU count
    DefaultMemoryMB int     // default memory in MB
    EnableJailer    bool    // enable jailer sandbox
}
```

### Constructor

```go
func NewVMMManager(config interface{}) VMMManager
```

**Parameters:**
- `config` — `*ManagerConfig` or any interface (defaults applied)

**Defaults Applied:**

| Field | Default Value |
|-------|---------------|
| `SocketDir` | `"/var/run/firecracker"` |

**Example:**

```go
config := &lifecycle.ManagerConfig{
    KernelPath:      "/usr/share/firecracker/vmlinux",
    RootfsDir:       "/var/lib/firecracker/rootfs",
    SocketDir:       "/var/run/firecracker",
    DefaultVCPUs:    2,
    DefaultMemoryMB: 1024,
}

vmm := lifecycle.NewVMMManager(config)
```

---

## VMInstance

**File:** `vmm.go`

### Type Definition

```go
type VMInstance struct {
    ID             string      // Task ID
    PID            int         // Firecracker process PID
    Config         interface{} // VM configuration
    state          VMState     // Current state (private)
    CreatedAt      time.Time   // Creation timestamp
    SocketPath     string      // API socket path
    InitSystem     string      // Init type (tini/dumb-init)
    GracePeriodSec int         // Shutdown grace period
    
    mu             sync.RWMutex // Protects state
}
```

### State Methods

```go
func (v *VMInstance) SetState(newState VMState)
func (v *VMInstance) GetState() VMState
```

**Thread-safe state access.**

---

## VM States

```go
type VMState string

const (
    VMStateNew      VMState = "new"       // Created but not started
    VMStateStarting VMState = "starting"  // Firecracker booting
    VMStateRunning  VMState = "running"   // VM operational
    VMStateStopping VMState = "stopping"  // Shutdown initiated
    VMStateStopped  VMState = "stopped"   // VM terminated
    VMStateCrashed  VMState = "crashed"   // Unexpected failure
)
```

---

## Methods

### Start

```go
func (vm *VMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error
```

**Purpose:** Start a Firecracker VM for the task.

**Steps:**

1. **Find Firecracker binary**
   ```go
   fcBinary, err := exec.LookPath("firecracker")
   ```

2. **Start process with API socket**
   ```go
   cmd := exec.Command(fcBinary, "--api-sock", socketPath)
   cmd.Stdout = os.Stdout
   cmd.Stderr = os.Stderr
   cmd.Start()
   ```

3. **Wait for API ready**
   ```go
   waitForAPIServer(socketPath, 10*time.Second)
   ```

4. **Configure VM via API**
   ```go
   vm.configureVM(ctx, socketPath, config)
   ```

5. **Send InstanceStart action**
   ```go
   client := newUnixClient(socketPath, 5*time.Second)
   actions := ActionsType{ActionType: "InstanceStart"}
   client.Put("/actions", actions)
   ```

6. **Track instance**
   ```go
   vm.vms[task.ID] = &VMInstance{
       ID:         task.ID,
       PID:        cmd.Process.Pid,
       SocketPath: socketPath,
       state:      VMStateRunning,
       CreatedAt:  time.Now(),
   }
   ```

**Error Handling:**
- Deferred cleanup on failure (kill process, remove socket)
- Validates config is not nil
- Checks for duplicate VM

---

### Stop

```go
func (vm *VMMManager) Stop(ctx context.Context, taskID string) error
```

**Purpose:** Stop a running VM.

**Steps:**

1. Get VM instance
2. Send `InstanceStop` action with timeout
3. Wait for process exit
4. Update state to `VMStateStopped`

---

### Pause

```go
func (vm *VMMManager) Pause(ctx context.Context, taskID string) error
```

**Purpose:** Pause VM for snapshot.

**API Call:**

```go
client.Put("/vm", VMStatePaused)
```

---

### Resume

```go
func (vm *VMMManager) Resume(ctx context.Context, taskID string) error
```

**Purpose:** Resume paused VM.

**API Call:**

```go
client.Put("/vm", VMStateResumed)
```

---

### GetInfo

```go
func (vm *VMMManager) GetInfo(taskID string) (*VMInstance, error)
```

**Purpose:** Get VM instance info.

**Returns:**
- `*VMInstance` — Instance details (ID, PID, state, socket)
- `error` — Not found error

---

### List

```go
func (vm *VMMManager) List() []string
```

**Purpose:** List all managed VM IDs.

---

### Cleanup

```go
func (vm *VMMManager) Cleanup(ctx context.Context, taskID string) error
```

**Purpose:** Cleanup VM resources after termination.

**Cleanup Steps:**
1. Remove API socket file
2. Remove VM from tracking map
3. Release allocated resources

---

## Firecracker API

### Client

```go
type unixClient struct {
    socketPath string
    timeout    time.Duration
}

func newUnixClient(socketPath string, timeout time.Duration) *unixClient
```

### HTTP Methods

```go
func (c *unixClient) Get(path string) ([]byte, error)
func (c *unixClient) Put(path string, body interface{}) error
```

**Uses HTTP over Unix socket:**

```go
// Dial Unix socket
conn, err := net.Dial("unix", socketPath)

// HTTP client with Unix transport
client := &http.Client{
    Transport: &unixTransport{socketPath: socketPath},
}
```

---

### API Types

```go
type ActionsType struct {
    ActionType string `json:"action_type"`
}

type BootSource struct {
    KernelImagePath string  `json:"kernel_image_path"`
    BootArgs        string  `json:"boot_args,omitempty"`
    Drives          []Drive `json:"drives,omitempty"`
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
```

---

### API Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/actions` | PUT | Start/stop VM |
| `/boot-source` | PUT | Configure kernel |
| `/drives/{id}` | PUT | Configure disk |
| `/machine-config` | PUT | Configure CPU/memory |
| `/network-interfaces/{id}` | PUT | Configure network |
| `/vm` | GET | Get VM info |
| `/vm` | PUT | Pause/resume |

---

## Configure VM

```go
func (vm *VMMManager) configureVM(ctx context.Context, socketPath string, config interface{}) error
```

**Purpose:** Apply full VM configuration via API.

**Steps:**

1. **Configure machine**
   ```go
   machine := MachineConfig{
       VCPUs:      vmConfig.VCPUs,
       MemSizeMib: vmConfig.MemoryMB,
       HtEnabled:  false,
   }
   client.Put("/machine-config", machine)
   ```

2. **Configure boot source**
   ```go
   boot := BootSource{
       KernelImagePath: vmConfig.KernelPath,
       BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off",
   }
   client.Put("/boot-source", boot)
   ```

3. **Configure root drive**
   ```go
   drive := Drive{
       DriveID:      "rootfs",
       PathOnHost:   vmConfig.RootfsPath,
       IsRootDevice: true,
       IsReadOnly:   false,
   }
   client.Put("/drives/rootfs", drive)
   ```

4. **Configure network interface**
   ```go
   iface := NetworkInterface{
       IfaceID:     "eth0",
       GuestMac:    generateMac(taskID),
       HostDevName: tapDevice.Name,
   }
   client.Put("/network-interfaces/eth0", iface)
   ```

---

## Graceful Shutdown

When `InitSystem` is set (tini/dumb-init):

```go
// 1. Send SIGTERM to init process
syscall.Kill(pid, syscall.SIGTERM)

// 2. Wait grace period
time.Sleep(time.Duration(gracePeriodSec) * time.Second)

// 3. Force kill if still running
if processStillRunning {
    syscall.Kill(pid, syscall.SIGKILL)
}
```

**Configuration:**

```yaml
executor:
  init_system: "tini"
  init_grace_period: 10  # seconds
```

---

## Process Monitoring

```go
// Wait for process exit
func waitForProcess(pid int, timeout time.Duration) error {
    for i := 0; i < int(timeout/time.Second); i++ {
        if !processExists(pid) {
            return nil
        }
        time.Sleep(time.Second)
    }
    return fmt.Errorf("process did not exit within timeout")
}
```

---

## API Server Ready Check

```go
func waitForAPIServer(socketPath string, timeout time.Duration) error {
    for i := 0; i < int(timeout/time.Millisecond); i += 100 {
        if _, err := net.Dial("unix", socketPath); err == nil {
            return nil
        }
        time.Sleep(100 * time.Millisecond)
    }
    return fmt.Errorf("API server not ready after %v", timeout)
}
```

---

## Testing

### Mock VMMManager

```go
type MockVMMManager struct {
    VMs      map[string]*VMInstance
    StartErr error
    StopErr  error
}

func (m *MockVMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error {
    if m.StartErr != nil {
        return m.StartErr
    }
    m.VMs[task.ID] = &VMInstance{
        ID:     task.ID,
        PID:    12345,
        state:  VMStateRunning,
    }
    return nil
}
```

---

## Error Handling

### Common Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `"task cannot be nil"` | Nil task passed | Provide valid task |
| `"VM already exists"` | Duplicate task ID | Remove existing VM first |
| `"firecracker binary not found"` | Not installed | Install Firecracker |
| `"API server not ready"` | Boot timeout | Check Firecracker logs |
| `"failed to configure VM"` | API error | Validate config values |
| `"process did not exit"` | Graceful shutdown timeout | Increase grace period |

---

## Related Documentation

| Topic | Document |
|-------|----------|
| SwarmKit executor | [SwarmKit Reference](swarmkit.md) |
| Network setup | [Network Reference](network.md) |
| Init systems | [Image Reference](image.md#init-injection) |
| Snapshot operations | [Operations Guide](../../user/guides/operations.md) |