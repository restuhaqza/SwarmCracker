# Firecracker Jailer Integration Plan

**Phase 6, Priority 1** - Security isolation for production deployments

---

## Problem Statement

Currently SwarmCracker runs Firecracker VMM processes directly without jailer isolation:
- No cgroup resource limits (CPU, memory, disk I/O)
- No namespace isolation (PID, network, mount)
- No seccomp syscall filtering
- Firecracker process runs with full host privileges

This is acceptable for development but **not production-ready** for multi-tenant workloads.

---

## Solution: Firecracker Jailer

The Firecracker jailer is a lightweight process isolation tool that:
- Creates a chroot environment for each MicroVM
- Applies cgroup v2 limits (CPU, memory, I/O)
- Drops privileges (runs as unprivileged user)
- Applies seccomp filters to restrict syscalls
- Isolates namespaces (PID, network, mount, UTS)

---

## Implementation Plan

### Phase 1: Foundation (Week 1)

#### 1.1 Create `pkg/jailer` Package

**File:** `pkg/jailer/jailer.go`

```go
package jailer

// Jailer manages Firecracker jailer process lifecycle
type Jailer struct {
    config *Config
}

// Config holds jailer configuration
type Config struct {
    FirecrackerPath string  // Path to firecracker binary
    JailerPath      string  // Path to jailer binary
    ChrootBaseDir   string  // Base directory for jailer chroots
    UID             int     // User ID to run as
    GID             int     // Group ID to run as
    NetNS           string  // Network namespace (optional)
    CgroupVersion   string  // "v1" or "v2"
}

// VMConfig holds per-VM jailer settings
type VMConfig struct {
    TaskID      string
    VcpuCount   int
    MemoryMB    int
    KernelPath  string
    RootfsPath  string
    NetworkNS   string
    CgroupPath  string
}

// Start launches a Firecracker VM inside jailer
func (j *Jailer) Start(ctx context.Context, cfg VMConfig) (*Process, error)

// Stop terminates a jailed VM
func (j *Jailer) Stop(ctx context.Context, taskID string) error
```

#### 1.2 Jailer Command Construction

Jailer command format:
```bash
jailer \
  --id <task-id> \
  --exec-file <firecracker-binary> \
  --uid <uid> \
  --gid <gid> \
  --chroot-base-dir <base-dir> \
  --netns <netns-path> \
  --cgroup-version <v1|v2> \
  -- \
  --api-sock /run/firecracker/<task-id>.sock
```

**Key flags:**
- `--id`: Unique VM identifier (becomes chroot dir name)
- `--exec-file`: Path to Firecracker binary
- `--uid/--gid`: Drop privileges to unprivileged user
- `--chroot-base-dir`: Base directory for chroots (e.g., `/var/lib/swarmcracker/jailer`)
- `--netns`: Network namespace path (for CNI integration)
- `--cgroup-version`: Use v2 (modern systems) or v1

#### 1.3 Chroot Directory Structure

Jailer creates this structure automatically:
```
/var/lib/swarmcracker/jailer/<task-id>/
├── root/           # Chroot filesystem
│   └── bin/
│       └── firecracker  # Symlink to host binary
├── run/
│   └── firecracker/
│       └── <task-id>.sock  # API socket
└── log/
    └── fifo.log           # Logging FIFO
```

---

### Phase 2: Cgroup Integration (Week 2)

#### 2.1 Cgroup v2 Controller

**File:** `pkg/jailer/cgroup.go`

```go
// CgroupManager manages cgroup resources for jailed VMs
type CgroupManager struct {
    basePath string // /sys/fs/cgroup/swarmcracker
}

// CreateCgroup creates cgroup with resource limits
func (m *CgroupManager) CreateCgroup(taskID string, limits ResourceLimits) error

// ResourceLimits defines CPU/memory/IO constraints
type ResourceLimits struct {
    CPUQuotaUs     int64  // CPU quota in microseconds
    CPUMax         string // CPU max (e.g., "100000 1000000" = 1 CPU)
    MemoryMax      int64  // Memory limit in bytes
    MemoryHigh     int64  // Memory throttle threshold
    IOWeight       uint64 // IO weight (1-10000)
    IOReadBPS      int64  // Read bandwidth limit
    IOWriteBPS     int64  // Write bandwidth limit
}
```

**Cgroup v2 paths:**
```
/sys/fs/cgroup/swarmcracker/<task-id>/
├── cpu.max          # CPU quota/period
├── memory.max       # Memory limit
├── memory.high      # Throttle threshold
├── io.weight        # IO weight
└── io.max           # IO bandwidth limits
```

#### 2.2 Cgroup Setup Flow

1. Create cgroup directory: `mkdir /sys/fs/cgroup/swarmcracker/<task-id>`
2. Set CPU limit: `echo "100000 1000000" > cpu.max` (1 CPU core)
3. Set memory limit: `echo "536870912" > memory.max` (512MB)
4. Add jailer process to cgroup: `echo <PID> > cgroup.procs`

---

### Phase 3: Seccomp Policies (Week 2-3)

#### 3.1 Seccomp Filter

**File:** `pkg/jailer/seccomp.go`

```go
// SeccompPolicy defines syscall filtering rules
type SeccompPolicy struct {
    DefaultAction string        // "kill", "errno", "allow"
    Syscalls      []SyscallRule // Allowed syscalls
}

// SyscallRule defines action for specific syscalls
type SyscallRule struct {
    Action   string   // "allow", "errno", "kill"
    Names    []string // Syscall names
    ErrnoRet *uint    // Errno value (if action=errno)
}

// DefaultPolicy returns SwarmCracker's default seccomp policy
func DefaultPolicy() *SeccompPolicy
```

**Default SwarmCracker policy** (minimal syscalls for Firecracker):
- `accept`, `bind`, `connect` - networking
- `brk`, `mmap`, `munmap` - memory
- `close`, `dup`, `fcntl` - file descriptors
- `exit`, `exit_group` - process termination
- `getpid`, `getuid`, `getgid` - process info
- `ioctl`, `poll`, `select` - I/O multiplexing
- `read`, `write` - I/O
- `sched_yield` - scheduling
- `socket`, `sendto`, `recvfrom` - sockets

#### 3.2 Seccomp Integration

Jailer supports seccomp via `--seccomp` flag:
```bash
jailer --seccomp <policy-json> ...
```

Policy JSON format:
```json
{
  "defaultAction": "kill",
  "syscalls": [
    {
      "action": "allow",
      "names": ["read", "write", "close", "exit", ...]
    }
  ]
}
```

---

### Phase 4: VMMManager Integration (Week 3)

#### 4.1 Update `pkg/swarmkit/vmm.go`

Add jailer support to existing VMMManager:

```go
type VMMManager struct {
    firecrackerPath string
    jailerPath      string
    useJailer       bool
    jailerConfig    *jailer.Config
    socketDir       string
    processes       map[string]*exec.Cmd
    processMutex    sync.Mutex
    logger          zerolog.Logger
}

// Start modified to support jailer mode
func (v *VMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error {
    if v.useJailer {
        return v.startWithJailer(ctx, task, config)
    }
    return v.startDirect(ctx, task, config) // existing implementation
}
```

#### 4.2 Jailer Start Implementation

```go
func (v *VMMManager) startWithJailer(ctx context.Context, task *types.Task, config interface{}) error {
    cfg, ok := config.(map[string]interface{})
    if !ok {
        return fmt.Errorf("invalid config type")
    }

    // Build jailer command
    jailerCmd := exec.CommandContext(
        ctx,
        v.jailerPath,
        "--id", task.ID,
        "--exec-file", v.firecrackerPath,
        "--uid", strconv.Itoa(v.jailerConfig.UID),
        "--gid", strconv.Itoa(v.jailerConfig.GID),
        "--chroot-base-dir", v.jailerConfig.ChrootBaseDir,
        "--cgroup-version", "v2",
        "--",
        "--api-sock", filepath.Join(v.socketDir, task.ID+".sock"),
    )

    // Start jailer (which starts firecracker inside chroot)
    if err := jailerCmd.Start(); err != nil {
        return fmt.Errorf("failed to start jailer: %w", err)
    }

    // Store process reference
    v.processes[task.ID] = jailerCmd

    // Wait for socket (now in chroot path)
    socketPath := filepath.Join(
        v.jailerConfig.ChrootBaseDir,
        task.ID,
        "run",
        "firecracker",
        task.ID+".sock",
    )
    if err := v.waitForSocket(socketPath, 10*time.Second); err != nil {
        return fmt.Errorf("jailer socket not created: %w", err)
    }

    // Configure VM (same as before, but socket path differs)
    return v.configureVM(ctx, task, socketPath, config)
}
```

---

### Phase 5: Configuration & CLI (Week 3-4)

#### 5.1 Update Executor Config

**File:** `pkg/executor/executor.go`

```go
type Config struct {
    // ... existing fields ...

    // Jailer settings
    EnableJailer    bool           `yaml:"enable_jailer"`
    JailerUID       int            `yaml:"jailer_uid"`
    JailerGID       int            `yaml:"jailer_gid"`
    JailerChrootDir string         `yaml:"jailer_chroot_dir"`
    CgroupVersion   string         `yaml:"cgroup_version"`
    SeccompPolicy   *SeccompPolicy `yaml:"seccomp_policy"`
}
```

#### 5.2 CLI Flags for swarmd-firecracker

**File:** `cmd/swarmd-firecracker/main.go`

```go
&cli.BoolFlag{
    Name:  "enable-jailer",
    Usage: "Enable Firecracker jailer for enhanced security",
    Value: false,
},
&cli.IntFlag{
    Name:  "jailer-uid",
    Usage: "UID to run jailed Firecracker processes",
    Value: 1000,
},
&cli.IntFlag{
    Name:  "jailer-gid",
    Usage: "GID to run jailed Firecracker processes",
    Value: 1000,
},
&cli.StringFlag{
    Name:  "jailer-chroot-dir",
    Usage: "Base directory for jailer chroots",
    Value: "/var/lib/swarmcracker/jailer",
},
&cli.StringFlag{
    Name:  "cgroup-version",
    Usage: "Cgroup version (v1 or v2)",
    Value: "v2",
},
```

#### 5.3 Configuration File Example

```yaml
# /etc/swarmcracker/executor-config.yaml
kernel_path: /usr/share/firecracker/vmlinux
rootfs_dir: /var/lib/firecracker/rootfs
socket_dir: /var/run/firecracker

# Jailer configuration
enable_jailer: true
jailer_uid: 1000
jailer_gid: 1000
jailer_chroot_dir: /var/lib/swarmcracker/jailer
cgroup_version: v2

# Resource limits per VM
default_vcpus: 1
default_memory_mb: 512
cpu_quota_us: 100000
memory_max_bytes: 536870912
```

---

### Phase 6: Testing & Validation (Week 4)

#### 6.1 Unit Tests

**File:** `pkg/jailer/jailer_test.go`

```go
func TestJailerStart(t *testing.T) {
    // Test jailer process creation
}

func TestCgroupLimits(t *testing.T) {
    // Test cgroup resource limits
}

func TestSeccompPolicy(t *testing.T) {
    // Test seccomp syscall filtering
}
```

#### 6.2 Integration Tests

**File:** `test/integration/jailer_test.go`

```go
func TestJailerIsolation(t *testing.T) {
    // Verify VM is properly isolated
    // - Can't access host filesystem
    // - Can't see host processes
    // - Limited by cgroup resources
}

func TestJailerResourceLimits(t *testing.T) {
    // Start VM with 256MB limit
    // Try to allocate 512MB
    // Verify OOM or throttling
}
```

#### 6.3 Security Validation

```bash
# Verify jailer chroot
ls -la /var/lib/swarmcracker/jailer/<task-id>/root/

# Verify cgroup limits
cat /sys/fs/cgroup/swarmcracker/<task-id>/cpu.max
cat /sys/fs/cgroup/swarmcracker/<task-id>/memory.max

# Verify process runs as unprivileged user
ps aux | grep firecracker

# Verify seccomp (check /proc/<pid>/status)
cat /proc/$(pidof firecracker)/status | grep Seccomp
# Should show: Seccomp: 2 (filter mode)
```

---

## Migration Path

### For Existing Deployments

1. **Backwards compatible**: Jailer is opt-in via `--enable-jailer` flag
2. **No config changes needed**: Existing configs work without jailer
3. **Gradual rollout**: Enable jailer on new nodes first

### Breaking Changes

- **Socket path changes**: With jailer, socket is in chroot path
  - Old: `/var/run/firecracker/<task-id>.sock`
  - New: `/var/lib/swarmcracker/jailer/<task-id>/run/firecracker/<task-id>.sock`
- **Requires jailer binary**: Must install Firecracker jailer
- **Cgroup support**: Requires cgroup v2 (systemd-based systems)

---

## Dependencies

### Required Packages

```bash
# Firecracker + Jailer (same release)
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.14.0/firecracker-v1.14.0-x86_64.tgz
tar xzf firecracker-v1.14.0-x86_64.tgz
sudo cp release-v1.14.0-x86_64/firecracker /usr/local/bin/
sudo cp release-v1.14.0-x86_64/jailer /usr/local/bin/

# Verify installation
firecracker --version
jailer --version
```

### System Requirements

- Linux kernel 4.14+ (cgroup v2 support)
- systemd (for cgroup v2)
- KVM support (`/dev/kvm`)
- Unprivileged user for jailer (e.g., `firecracker` user)

---

## Success Criteria

- [ ] Jailer binary integration complete
- [ ] Cgroup v2 resource limits working
- [ ] Seccomp policy applied
- [ ] All existing tests pass with jailer disabled
- [ ] New integration tests pass with jailer enabled
- [ ] Documentation updated
- [ ] Ansible playbooks updated to install jailer
- [ ] Production deployment validated

---

## Timeline

| Phase | Duration | Deliverables |
|-------|----------|--------------|
| 1. Foundation | Week 1 | `pkg/jailer` package, basic start/stop |
| 2. Cgroups | Week 2 | Resource limits, cgroup v2 integration |
| 3. Seccomp | Week 2-3 | Syscall filtering, security policies |
| 4. VMM Integration | Week 3 | VMMManager updated, CLI flags |
| 5. Testing | Week 4 | Unit tests, integration tests, validation |

**Total: 4 weeks**

---

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Cgroup v2 incompatibility | High | Support both v1 and v2, detect automatically |
| Performance overhead | Medium | Benchmark with/without jailer, document overhead |
| Breaking socket paths | Medium | Backwards compatible flag, migration guide |
| Seccomp breaks Firecracker | High | Start with permissive policy, tighten gradually |

---

## Next Steps

1. **Create `pkg/jailer` package structure**
2. **Implement basic jailer start/stop**
3. **Add cgroup v2 resource limits**
4. **Integrate with VMMManager**
5. **Add CLI flags and config options**
6. **Write tests**
7. **Update Ansible playbooks**
8. **Document and release**

---

**Author:** Claw  
**Created:** 2026-04-07  
**Status:** Ready for implementation
