# Bugs and Issues Found

## Critical Issues (Blockers)

### 1. Hardcoded Firecracker Binary Path
**Location:** `pkg/lifecycle/vmm.go:96`
**Severity:** CRITICAL
**Status:** 🔴 NOT FIXED

**Code:**
```go
cmd := exec.Command("firecracker-v1.0.0",
    "--api-sock", socketPath,
    "--config-file", "/dev/stdin",
)
```

**Problem:**
- Binary name is hardcoded to `firecracker-v1.0.0`
- System has `firecracker` binary, not versioned name
- Causes "executable file not found" error

**Impact:**
- Cannot start any Firecracker VMs
- All VM launch tests fail
- Blocks Phase 2 and Phase 4

**Fix Required:**
```go
// Option 1: Use LookPath
binaryPath, err := exec.LookPath("firecracker")
if err != nil {
    return fmt.Errorf("firecracker not found: %w", err)
}
cmd := exec.Command(binaryPath, "--api-sock", socketPath)

// Option 2: Use from config
binaryPath := vm.config.BinaryPath
if binaryPath == "" {
    binaryPath, err = exec.LookPath("firecracker")
}
cmd := exec.Command(binaryPath, "--api-sock", socketPath)
```

---

### 2. Config File Not Supported via Stdin
**Location:** `pkg/lifecycle/vmm.go:96-100`
**Severity:** CRITICAL
**Status:** 🔴 NOT FIXED

**Code:**
```go
cmd := exec.Command("firecracker-v1.0.0",
    "--api-sock", socketPath,
    "--config-file", "/dev/stdin",  // This doesn't work!
)
cmd.Stdin = strings.NewReader(configStr)
```

**Problem:**
- Firecracker doesn't support `--config-file /dev/stdin`
- Configuration must be sent via API after starting
- Current approach causes startup failure

**Impact:**
- VMs cannot be configured
- Boot source and machine config cannot be set
- Blocks all VM functionality

**Fix Required:**
```go
// Step 1: Start Firecracker without config
cmd := exec.Command("firecracker", "--api-sock", socketPath)
if err := cmd.Start(); err != nil {
    return fmt.Errorf("failed to start firecracker: %w", err)
}

// Step 2: Wait for API server
if err := waitForAPIServer(socketPath, 10*time.Second); err != nil {
    cmd.Process.Kill()
    return fmt.Errorf("API server not ready: %w", err)
}

// Step 3: Send boot source via API
if err := putBootSource(ctx, socketPath, kernelPath, rootfsPath); err != nil {
    cmd.Process.Kill()
    return err
}

// Step 4: Send machine config via API
if err := putMachineConfig(ctx, socketPath, vcpus, memory); err != nil {
    cmd.Process.Kill()
    return err
}

// Step 5: Start instance
if err := putActions(ctx, socketPath, "InstanceStart"); err != nil {
    cmd.Process.Kill()
    return err
}
```

**Helper Functions Needed:**
```go
func putBootSource(ctx context.Context, socket, kernel, rootfs string) error {
    bootSource := map[string]interface{}{
        "kernel_image_path": kernel,
        "boot_args":         "console=ttyS0 reboot=k panic=1 pci=off",
        "drives": []map[string]interface{}{
            {
                "drive_id":      "rootfs",
                "path_on_host":  rootfs,
                "is_root_device": true,
                "is_read_only":  false,
            },
        },
    }
    return putAPI(ctx, socket, "/boot-source", bootSource)
}

func putMachineConfig(ctx context.Context, socket string, vcpus, memMB int64) error {
    config := map[string]interface{}{
        "vcpu_count":  vcpus,
        "mem_size_mib": memMB,
        "ht_enabled":  false,
    }
    return putAPI(ctx, socket, "/machine-config", config)
}

func putActions(ctx context.Context, socket, action string) error {
    actions := map[string]string{
        "action_type": action,
    }
    return putAPI(ctx, socket, "/actions", actions)
}

func putAPI(ctx context.Context, socket, path string, data interface{}) error {
    body, _ := json.Marshal(data)
    client := &http.Client{Timeout: 5 * time.Second}
    req, _ := http.NewRequestWithContext(ctx, "PUT",
        "http://unix"+socket+path,
        bytes.NewReader(body),
    )
    req.Header.Set("Content-Type", "application/json")
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusCreated {
        return fmt.Errorf("unexpected status: %d", resp.StatusCode)
    }
    return nil
}
```

---

### 3. No Socket Cleanup on Failure
**Location:** `pkg/lifecycle/vmm.go:115-120`
**Severity:** MEDIUM
**Status:** 🔴 NOT FIXED

**Problem:**
- If API server fails to start, socket file remains
- Next start fails with "address already in use"
- Manual cleanup required

**Impact:**
- Failed tests leave zombie socket files
- Requires manual cleanup between test runs
- Poor user experience

**Fix Required:**
```go
func (vm *VMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error {
    // ... validation code ...

    socketPath := filepath.Join(vm.socketDir, task.ID+".sock")

    // Cleanup existing socket if present
    if _, err := os.Stat(socketPath); err == nil {
        os.Remove(socketPath)
    }

    // Start Firecracker process
    cmd := exec.Command("firecracker", "--api-sock", socketPath)
    if err := cmd.Start(); err != nil {
        return fmt.Errorf("failed to start firecracker: %w", err)
    }

    // Ensure cleanup on failure
    success := false
    defer func() {
        if !success {
            cmd.Process.Kill()
            os.Remove(socketPath)
        }
    }()

    // Wait for API server
    if err := waitForAPIServer(socketPath, 10*time.Second); err != nil {
        return fmt.Errorf("API server not ready: %w", err)
    }

    // ... configuration code ...

    success = true
    // ... rest of code ...
}
```

---

## Medium Issues

### 4. Missing Error Context
**Location:** Throughout codebase
**Severity:** LOW
**Status:** 🔴 NOT FIXED

**Problem:**
- Some errors don't include task ID or operation
- Makes debugging harder

**Example:**
```go
// Bad:
return fmt.Errorf("failed to start firecracker")

// Good:
return fmt.Errorf("task %s: failed to start firecracker: %w", task.ID, err)
```

---

## Fixed Issues

### ✅ 1. Nil Map Assignment
**Location:** `pkg/image/preparer.go:92`
**Severity:** CRITICAL
**Status:** ✅ FIXED

**Problem:**
```go
task.Annotations["rootfs"] = rootfsPath  // Panic if nil!
```

**Fix Applied:**
```go
// Initialize annotations map if nil
if task.Annotations == nil {
    task.Annotations = make(map[string]string)
}
task.Annotations["rootfs"] = rootfsPath
```

---

### ✅ 2. Import Errors in Tests
**Location:** `test/e2e/firecracker/*.go`
**Severity:** LOW
**Status:** ✅ FIXED

**Problem:**
- Unused imports causing compilation failures
- Missing imports for used types

**Fix Applied:**
- Removed unused imports (context, assert, etc.)
- Added required imports (time, tar, io, etc.)

---

### ✅ 3. Task Structure Mismatch
**Location:** `test/e2e/firecracker/*.go`
**Severity:** MEDIUM
**Status:** ✅ FIXED

**Problem:**
```go
// Old (incorrect):
task := &types.Task{
    Image: "nginx:latest",
    Command: []string{"/bin/sh"},
}

// New (correct):
task := &types.Task{
    Spec: types.TaskSpec{
        Runtime: &types.Container{
            Image: "nginx:latest",
            Command: []string{"/bin/sh"},
        },
    },
}
```

**Fix Applied:**
- Updated all test Task creations to use proper structure
- Changed `Resources` field to use proper nested structure

---

## Test Infrastructure Issues

### 5. Test Expectations Mismatch
**Location:** `test/e2e/firecracker/03_image_preparation_test.go`
**Severity:** LOW
**Status:** ⚠️ PARTIALLY FIXED

**Problem:**
- Tests expect directory-based rootfs
- Image preparer creates ext4 file
- Some assertions fail

**Status:**
- Root cause identified
- Tests partially updated
- Some test logic needs refinement

---

## Recommendations

### Immediate (For Phase 2)
1. Fix hardcoded binary path - 5 minutes
2. Implement API-based configuration - 30 minutes
3. Add socket cleanup - 10 minutes

### Short-term (For Phase 3)
4. Add more error context throughout - 1 hour
5. Update remaining test expectations - 30 minutes

### Long-term (For Production)
6. Add retry logic for transient failures
7. Implement proper init system for containers
8. Add comprehensive error recovery
9. Create integration test suite
10. Add performance benchmarks

---

*Last Updated: 2026-02-01*
*Total Issues: 5 Critical, 3 Fixed, 1 Test Infrastructure*
