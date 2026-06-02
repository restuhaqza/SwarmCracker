# Security Package Reference

> `pkg/security/` — Jailer, Seccomp Profiles, Capability Management.

---

## Overview

The `pkg/security` package provides security isolation for Firecracker VMs through jailer sandboxing (chroot, UID/GID drop), seccomp syscall filtering, and capability management.

**Package Structure:**

```
pkg/security/
├── jailer.go           # Jailer (chroot, UID/GID, netns)
├── seccomp.go          # Seccomp syscall filtering
├── manager.go          # SecurityManager (orchestration)
```

---

## Jailer

**File:** `jailer.go`

The `Jailer` provides multi-layered isolation for Firecracker VMs.

### Type Definition

```go
type Jailer struct {
    UID           int    // UID to run Firecracker as
    GID           int    // GID to run Firecracker as
    ChrootBaseDir string // Base directory for jails
    NetNS         string // Network namespace path
    Enabled       bool   // Isolation enabled
}
```

### Constructor

```go
func NewJailer(uid, gid int, chrootDir, netNS string) *Jailer
```

**Example:**

```go
jailer := security.NewJailer(1000, 1000, "/srv/jailer", "/var/run/netns/firecracker")
```

---

### Methods

#### SetupJail

```go
func (j *Jailer) SetupJail(vmID string) (*JailContext, error)
```

**Purpose:** Create isolated environment for VM.

**Steps:**

1. **Create jail directory**
   ```go
   jailPath := filepath.Join(j.ChrootBaseDir, vmID)
   os.MkdirAll(jailPath, 0755)
   ```

2. **Create subdirectories**
   ```go
   dirs := []string{
       filepath.Join(jailPath, "run"),
       filepath.Join(jailPath, "dev"),
       filepath.Join(jailPath, "proc"),
   }
   ```

3. **Set resource limits**
   ```go
   j.setResourceLimits(ctx)
   // • RLIMIT_NOFILE: 4096
   // • RLIMIT_CPU: unlimited
   // • RLIMIT_AS: unlimited (managed by Firecracker)
   ```

**Returns:**

```go
type JailContext struct {
    Enabled    bool
    JailPath   string
    UID        int
    GID        int
    NetNS      string
    OriginalWD string
}
```

---

#### EnterJail

```go
func (j *Jailer) EnterJail(ctx *JailContext) error
```

**Purpose:** Enter chroot jail (call after fork, before exec).

**Steps:**

```go
// 1. Change to jail directory
os.Chdir(ctx.JailPath)

// 2. Change root (requires CAP_SYS_CHROOT)
syscall.Chroot(ctx.JailPath)

// 3. Change to root directory
os.Chdir("/")

// 4. Drop privileges
syscall.Setgid(ctx.GID)
syscall.Setuid(ctx.UID)
```

---

#### SetupNetworkNamespace

```go
func (j *Jailer) SetupNetworkNamespace(ctx *JailContext) error
```

**Purpose:** Enter network namespace if configured.

---

#### CleanupJail

```go
func (j *Jailer) CleanupJail(ctx *JailContext) error
```

**Purpose:** Remove jail directory after VM termination.

---

#### Validate

```go
func (j *Jailer) Validate() error
```

**Purpose:** Validate jailer configuration.

**Checks:**
- UID/GID >= 0
- ChrootBaseDir is non-empty
- Base directory can be created

---

## Seccomp

**File:** `seccomp.go`

### SeccompProfile

```go
type SeccompProfile struct {
    DefaultAction string       `json:"defaultAction"`
    Architectures []string     `json:"architectures"`
    Syscalls      []SyscallRule `json:"syscalls"`
}

type SyscallRule struct {
    Names  []string `json:"names"`
    Action string   `json:"action"`
}
```

### Default Profile

**Default Action:** `SCMP_ACT_ERRNO` — Unknown syscalls return errors.

**Allowed Syscalls:**

| Category | Syscalls |
|----------|----------|
| Basic | `read`, `write`, `open`, `close`, `stat`, `poll`, `mmap`, `munmap`, `brk` |
| Networking | `socket`, `bind`, `listen`, `accept`, `connect`, `sendmsg`, `recvmsg`, `sendto`, `recvfrom` |
| Process | `clone`, `fork`, `execve`, `exit`, `wait4`, `kill`, `getpid`, `getppid` |
| Filesystem | `mkdir`, `rmdir`, `creat`, `unlink`, `chmod`, `chown`, `dup`, `dup2`, `pipe` |
| Memory | `mprotect`, `madvise`, `msync`, `mlock`, `munlock` |
| Signals | `sigaction`, `sigprocmask`, `sigsuspend`, `rt_sigaction` |

### Blocked Syscalls

| Syscall | Risk | Why Blocked |
|---------|------|-------------|
| `mount`, `umount2` | Filesystem manipulation | Prevent mounting arbitrary filesystems |
| `pivot_root`, `chroot` | Root directory change | Prevent escaping isolation |
| `swapon`, `swapoff` | Memory swap control | Prevent swap manipulation |
| `reboot`, `kexec_load` | System reboot | Prevent host reboot |
| `init_module`, `delete_module` | Kernel modules | Prevent loading kernel code |
| `iopl`, `ioperm` | Hardware I/O | Prevent direct hardware access |
| `acct` | Process accounting | Prevent accounting manipulation |
| `settimeofday`, `clock_settime` | Time manipulation | Prevent clock attacks |
| `sethostname`, `setdomainname` | Host identity | Prevent spoofing |
| `ptrace` | Process tracing | Prevent debugging attacks |

---

### Constructor

```go
func NewSeccompProfile() *SeccompProfile
```

---

### Methods

#### Generate

```go
func (sp *SeccompProfile) Generate() ([]byte, error)
```

**Purpose:** Generate JSON seccomp profile for Firecracker.

---

#### Apply

```go
func (sp *SeccompProfile) Apply(pid int) error
```

**Purpose:** Apply seccomp profile to process.

---

## SecurityManager

**File:** `manager.go`

Orchestrates jailer and seccomp for comprehensive isolation.

### Type Definition

```go
type SecurityManager struct {
    jailer       *Jailer
    seccomp      *SeccompProfile
    capabilities []string
}
```

### Constructor

```go
func NewSecurityManager(jailer *Jailer, seccomp *SeccompProfile) *SecurityManager
```

---

### Methods

#### Setup

```go
func (sm *SecurityManager) Setup(vmID string) (*SecurityContext, error)
```

**Purpose:** Setup all isolation mechanisms.

**Steps:**
1. Setup jail (chroot, directories, limits)
2. Prepare seccomp profile
3. Return context for EnterJail

---

#### ApplyToProcess

```go
func (sm *SecurityManager) ApplyToProcess(pid int) error
```

**Purpose:** Apply seccomp to running process.

---

## Resource Limits

### ApplyResourceLimits

```go
func ApplyResourceLimits(pid int, limits ResourceLimits) error
```

**Purpose:** Apply cgroup-based resource limits.

```go
type ResourceLimits struct {
    MaxCPUs      int
    MaxMemoryMB  int
    MaxFD        int
    MaxProcesses int
}
```

**Implementation (cgroups v2):**

```go
cgroupPath := fmt.Sprintf("/sys/fs/cgroup/swarmcracker/%d", pid)
os.MkdirAll(cgroupPath, 0755)

// CPU limit
cpuQuota := limits.MaxCPUs * 100000
os.WriteFile(filepath.Join(cgroupPath, "cpu.max"), 
    []byte(fmt.Sprintf("%d 100000", cpuQuota)), 0644)

// Memory limit
memoryBytes := limits.MaxMemoryMB * 1024 * 1024
os.WriteFile(filepath.Join(cgroupPath, "memory.max"),
    []byte(fmt.Sprintf("%d", memoryBytes)), 0644)

// Add process to cgroup
os.WriteFile(filepath.Join(cgroupPath, "cgroup.procs"),
    []byte(fmt.Sprintf("%d", pid)), 0644)
```

---

### CleanupCgroup

```go
func CleanupCgroup(pid int) error
```

**Purpose:** Remove cgroup after VM termination.

---

## Capability Management

### Capabilities

Firecracker VMs run with minimal capabilities:

| Capability | Reason |
|------------|--------|
| `CAP_NET_BIND_SERVICE` | Allow binding to privileged ports |
| `CAP_NET_RAW` | Allow raw socket operations |
| `CAP_SETUID`, `CAP_SETGID` | Allow privilege drop inside VM |

### Drop Capabilities

```go
func DropCapabilities() error
```

**Purpose:** Drop unnecessary capabilities from guest.

---

## Hardening Checklist

### Production Deployment

- [ ] **Jailer enabled** (`enable_jailer: true`)
- [ ] **UID/GID configured** (non-root: 1000:1000)
- [ ] **Chroot directory** (`/srv/jailer` or similar)
- [ ] **Seccomp profile** applied to guest
- [ ] **Network namespace** (optional, for isolation)
- [ ] **Cgroup limits** applied per VM
- [ ] **File permissions** (config: 0600, state: 0700)

### Build Hardening

```bash
go build -v -trimpath -buildmode=pie \
    -ldflags "-s -w -X main.Version=..." \
    -o build/swarmd-firecracker ./cmd/swarmd-firecracker/main.go
```

| Flag | Purpose |
|------|---------|
| `-trimpath` | Remove filesystem paths (privacy) |
| `-buildmode=pie` | Position-independent executable (ASLR) |
| `-s -w` | Strip debug/symbols (smaller, less info leak) |

---

## Testing

### Mock Jailer

```go
type MockJailer struct {
    Enabled   bool
    JailPath  string
    SetupErr  error
    EnterErr  error
}

func (m *MockJailer) SetupJail(vmID string) (*JailContext, error) {
    if m.SetupErr != nil {
        return nil, m.SetupErr
    }
    return &JailContext{
        Enabled:  m.Enabled,
        JailPath: m.JailPath,
    }, nil
}
```

---

## Error Handling

### Common Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `"failed to create jail directory"` | Permission denied | Run with root or setup permissions |
| `"failed to chroot"` | CAP_SYS_CHROOT missing | Grant capability or use jailer binary |
| `"failed to setgid/setuid"` | Invalid UID/GID | Verify user/group exists |
| `"invalid UID/GID"` | Negative values | Use positive values |
| `"chroot_base_dir cannot be empty"` | Missing config | Set in YAML config |

---

## Security Model

### Trust Boundaries

| Component | Trust Level | Reason |
|-----------|-------------|--------|
| Host kernel | **Trusted** | TCB (Trusted Computing Base) |
| SwarmKit manager | **High** | Controls scheduling, secrets |
| swarmd-firecracker | **High** | KVM access, root for networking |
| Firecracker VMM | **High** | Rust-based, minimal attack surface |
| Guest VM | **Low** | Hardware-isolated, attack limited to VM |

### Isolation Layers

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              Host System                                     │
│                                                                             │
│   ┌─────────────────────────────────────────────────────────────────────┐  │
│   │                            Jailer Sandbox                            │  │
│   │                                                                     │  │
│   │   ┌────────────────────────────────────────────────────────────┐   │  │
│   │   │                    Firecracker Process                      │   │  │
│   │   │                                                            │   │  │
│   │   │   ┌──────────────────────────────────────────────────────┐ │   │  │
│   │   │   │                     Guest VM                          │ │   │  │
│   │   │   │                                                      │ │   │  │
│   │   │   │   Isolation:                                          │ │   │  │
│   │   │   │   • KVM (hardware)                                   │ │   │  │
│   │   │   │   • chroot (filesystem)                              │ │   │  │
│   │   │   │   • UID/GID drop (process)                           │ │   │  │
│   │   │   │   • seccomp (syscall)                                │ │   │  │
│   │   │   │   • cgroups (resources)                              │ │   │  │
│   │   │   │                                                      │ │   │  │
│   │   │   └──────────────────────────────────────────────────────┘ │   │  │
│   │   │                                                            │   │  │
│   │   └────────────────────────────────────────────────────────────┘   │  │
│   │                                                                     │  │
│   └─────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Related Documentation

| Topic | Document |
|-------|----------|
| Security hardening | [Security Guide](../security.md) |
| SwarmKit executor | [SwarmKit Reference](swarmkit.md) |
| User jailer guide | [Jailer Quickstart](../../user/guides/jailer-quickstart.md) |