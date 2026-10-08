---
title: "Lifecycle Package Reference"
description: "`pkg/lifecycle/` — VM lifecycle management, Firecracker API communication."
---

> `pkg/lifecycle/` — VM lifecycle management, Firecracker API communication.

---

## Overview

The `pkg/lifecycle` package manages Firecracker VM lifecycle operations: start, stop, pause, resume, and state tracking.

**Package Structure:**

```
pkg/lifecycle/
├── vmm.go              # VMMManager and VMInstance
├── mocks_test.go       # Test mocks
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
func NewVMMManager(config interface{}) types.VMMManager
```

**Parameters:**
- `config` — `*ManagerConfig` (a non-`*ManagerConfig` value falls back to a default `ManagerConfig` with `SocketDir: "/var/run/firecracker"`)

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
   body, _ := json.Marshal(actions)
   req, _ := http.NewRequestWithContext(ctx, "PUT",
       "http://localhost/actions", bytes.NewReader(body))
   req.Header.Set("Content-Type", "application/json")
   resp, err := client.Do(req)
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
func (vm *VMMManager) Stop(ctx context.Context, task *types.Task) error
```

**Purpose:** Stop a running VM.

**Steps:**

1. Look up the VM instance by `task.ID`
2. Mark it `VMStateStopping`
3. If an init system is set, attempt a graceful SIGTERM shutdown; otherwise
   send `SendCtrlAltDel`
4. Force-kill after the grace period if the process is still alive
5. Update state to `VMStateStopped`

---

### Pause

Pause is implemented in `pkg/snapshot` by `pauseVM()`, which issues a
**`PATCH /vm`** request (Firecracker v1.14+):

```go
payload := map[string]interface{}{"state": "Paused"}
// PATCH /vm
```

`pkg/lifecycle` itself has no pause method; `VMMManager.Snapshot` delegates to
`pkg/snapshot.Manager`.

---

### Resume

Resume is implemented in `pkg/snapshot` by `resumeVM()`, using the same
endpoint with `{"state": "Resumed"}`:

```go
payload := map[string]interface{}{"state": "Resumed"}
// PATCH /vm
```

A restore resumes automatically via the `resume_vm: true` flag on
`PUT /snapshot/load`.

---

### Wait

```go
func (vm *VMMManager) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error)
```

**Purpose:** Report the current status of the VM (running / complete / orphaned).

---

### Describe

```go
func (vm *VMMManager) Describe(ctx context.Context, task *types.Task) (*types.TaskStatus, error)
```

**Purpose:** Describe the VM's state, including `vm_id`, `pid`, `state` and uptime.

---

### Remove

```go
func (vm *VMMManager) Remove(ctx context.Context, task *types.Task) error
```

**Purpose:** Remove a VM and clean up resources.

**Cleanup Steps:**
1. Kill the VM process if still running
2. Remove the API socket file
3. Remove the VM from the tracking map

---

### Snapshot

```go
func (vm *VMMManager) Snapshot(ctx context.Context, task *types.Task, opts interface{}) (interface{}, error)
```

**Status:** Placeholder — returns an error directing callers to use
`pkg/snapshot.Manager` directly.

---

### Restore

```go
func (vm *VMMManager) Restore(ctx context.Context, task *types.Task, snap interface{}) error
```

**Status:** Placeholder — returns an error directing callers to use
`pkg/snapshot.Manager` directly.

---

### SetConsoleWriter

```go
func (vm *VMMManager) SetConsoleWriter(w io.Writer)
```

**Purpose:** Redirect the serial console of VMs started from now on to `w`
(a detached caller points this at a log file so the long-lived Firecracker
child does not keep the caller's stdio open).

---

## Firecracker API

### Client

```go
func newUnixClient(socketPath string, timeout time.Duration) *http.Client
```

`newUnixClient` returns a standard `*http.Client` whose transport dials the
Firecracker API over a Unix socket. Callers build requests directly (for
example a `PUT` to `http://localhost/actions` with the action body) rather than
going through helper methods.

**Uses HTTP over Unix socket:**

```go
// HTTP client with Unix transport
client := &http.Client{
    Timeout: timeout,
    Transport: &http.Transport{
        DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
            var d net.Dialer
            return d.DialContext(ctx, "unix", socketPath)
        },
    },
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
| `/vm` | PATCH | Pause/resume (`{"state": "Paused"}` / `{"state": "Resumed"}`) |

---

## Configure VM

```go
func (vm *VMMManager) configureVM(ctx context.Context, socketPath string, config interface{}) error
```

**Purpose:** Apply full VM configuration via API.

The config is a `map[string]interface{}` (or JSON string) whose keys are parsed
case-by-case. Only the sections present are applied.

**Steps:**

1. **Configure boot source** (if `boot-source` present)
   ```go
   // PUT http://localhost/boot-source
   ```

2. **Configure machine** (if `machine-config` present)
   ```go
   // PUT http://localhost/machine-config
   ```

3. **Configure drives** (if `drives` present)
   ```go
   // PUT http://localhost/drives/<drive_id>
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
| Snapshot operations | [Operations Guide](/guides/operations/) |