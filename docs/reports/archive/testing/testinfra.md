# Test Infrastructure (testinfra)

Test infrastructure (testinfra) validates that your test environment is properly configured for running tests.

## Purpose

Testinfra checks:
- ✅ System requirements (OS, architecture, Go version)
- ✅ Hardware virtualization (KVM)
- ✅ Software dependencies (Firecracker, kernel)
- ✅ Container runtime (Docker/Podman)
- ✅ Network permissions
- ✅ System resources (disk space, memory)

## Running testinfra

### Quick Check
```bash
make testinfra

# Or
go test -v ./test/testinfra/...
```

### Specific Checks
```bash
# Prerequisites only
go test -v ./test/testinfra/... -run TestInfra_Prerequisites

# SwarmKit check
go test -v ./test/testinfra/... -run TestInfra_SwarmKitInstallation

# Build check
go test -v ./test/testinfra/... -run TestInfra_BuildSwarmCracker
```

## Checks Performed

### 1. Go Version
**Check**: Go version is 1.21+ or higher

**Command**: `go version`

**Example**:
```
✓ Go version: go version go1.24.9 linux/amd64
```

**Fix if fails**:
```bash
# Install newer Go
# Visit https://go.dev/dl/
```

---

### 2. Architecture
**Check**: System architecture is amd64 or arm64

**Example**:
```
✓ Architecture: amd64
```

**Note**: Other architectures may not be supported

---

### 3. Operating System
**Check**: OS is Linux

**Example**:
```
✓ Operating System: linux
```

**Note**: macOS and Windows not currently supported

---

### 4. KVM Device
**Check**: `/dev/kvm` device exists and is accessible

**Command**: `ls -l /dev/kvm`

**Example**:
```
✓ KVM device found: /dev/kvm
```

**Fix if fails**:
```bash
# Check if KVM is enabled
ls -l /dev/kvm

# Add user to kvm group
sudo usermod -aG kvm $USER

# Log out and back in
```

---

### 5. Firecracker Binary
**Check**: Firecracker binary is in PATH

**Command**: `firecracker --version`

**Example**:
```
✓ Firecracker version: Firecracker v1.14.1
```

**Fix if fails**:
```bash
# Download Firecracker
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/firecracker-v1.8.0-x86_64
sudo mv firecracker-v1.8.0-x86_64 /usr/local/bin/firecracker
sudo chmod +x /usr/local/bin/firecracker

# Verify
firecracker --version
```

---

### 6. Firecracker Kernel
**Check**: Firecracker kernel image is available

**Searched paths**:
- `/home/kali/.local/share/firecracker/vmlinux`
- `/usr/share/firecracker/vmlinux`
- `/boot/vmlinux`
- `/var/lib/firecracker/vmlinux`

**Example**:
```
✓ Firecracker kernel found: /home/kali/.local/share/firecracker/vmlinux (20.28 MB)
```

**Fix if fails**:
```bash
# Create directory
sudo mkdir -p /usr/share/firecracker

# Download kernel
sudo wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/vmlinux-v1.8.0 -O /usr/share/firecracker/vmlinux

# Or use local path
mkdir -p ~/.local/share/firecracker
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/vmlinux-v1.8.0 \
  -O ~/.local/share/firecracker/vmlinux
```

---

### 7. Container Runtime
**Check**: Docker or Podman is available

**Example**:
```
✓ Docker: Docker version 27.5.1+dfsg4, build cab968b3
```

**Fix if fails**:
```bash
# Install Docker
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker $USER

# Or Podman
sudo apt install podman
```

---

### 8. Network Permissions
**Check**: Can create network bridges

**Example**:
```
✓ Network permissions OK (created test bridge: test-br-1738342943)
```

**Fix if fails**:
```bash
# Run with sudo/CAP_NET_ADMIN
sudo setcap cap_net_admin+ep $(which swarmcracker)

# Or run tests with sudo (not recommended)
```

---

### 9. Disk Space
**Check**: At least 5 GB available

**Example**:
```
✓ Available disk space: 206.27 GB
```

**Fix if fails**:
```bash
# Clean up space
df -h
# Remove unnecessary files
```

---

### 10. System Memory
**Check**: At least 4 GB total memory

**Example**:
```
✓ Total memory: 15.62 GB
```

**Fix if fails**:
```bash
# Check memory usage
free -h
# Close unnecessary applications
```

---

## Using Checkers Programmatically

### Firecracker Checker
```go
package main

import (
    "fmt"
    "github.com/restuhaqza/swarmcracker/test/testinfra/checks"
)

func main() {
    fc := checks.NewFirecrackerChecker()
    errors := fc.Validate()

    if len(errors) > 0 {
        fmt.Println("Firecracker checks failed:")
        for _, err := range errors {
            fmt.Println("  -", err)
        }
    } else {
        fmt.Println("✓ Firecracker OK")
        fmt.Println("  Binary:", fc.GetBinaryPath())
        fmt.Println("  Kernel:", fc.GetKernelPath())
    }
}
```

### Kernel Checker
```go
kc := checks.NewKernelChecker()
errors := kc.Validate()

if len(errors) > 0 {
    fmt.Println("Kernel checks failed:")
    for _, err := range errors {
        fmt.Println("  -", err)
    }
} else {
    version, _ := kc.GetKernelVersion()
    fmt.Println("✓ Kernel OK")
    fmt.Println("  Version:", version)
}
```

### Network Checker
```go
nc := checks.NewNetworkChecker()
errors := nc.Validate()

if len(errors) > 0 {
    fmt.Println("Network checks failed:")
    for _, err := range errors {
        fmt.Println("  -", err)
    }
}
```

## Test Output Example

### Successful Run
```
=== RUN   TestInfra_Prerequisites
    testinfra_test.go:21: Checking infrastructure prerequisites...
=== RUN   TestInfra_Prerequisites/GoVersion
    testinfra_test.go:87: Go version: go version go1.24.9 linux/amd64
=== RUN   TestInfra_Prerequisites/Architecture
    testinfra_test.go:98: Architecture: amd64
=== RUN   TestInfra_Prerequisites/OperatingSystem
    testinfra_test.go:108: Operating System: linux
=== RUN   TestInfra_Prerequisites/KVM
    testinfra_test.go:143: KVM device found: /dev/kvm
=== RUN   TestInfra_Prerequisites/Firecracker
    testinfra_test.go:156: Firecracker version: Firecracker v1.14.1
=== RUN   TestInfra_Prerequisites/FirecrackerKernel
    testinfra_test.go:173: Firecracker kernel found: /home/kali/.local/share/firecracker/vmlinux (20.28 MB)
=== RUN   TestInfra_Prerequisites/ContainerRuntime
    testinfra_test.go:190: Docker: Docker version 27.5.1+dfsg4, build cab968b3
=== RUN   TestInfra_Prerequisites/NetworkPermissions
    testinfra_test.go:213: Warning: Cannot create network bridge (may require privileges): exit status 2
    testinfra_test.go:214: Network permissions check skipped
=== RUN   TestInfra_Prerequisites/DiskSpace
    testinfra_test.go:245: Available disk space: 206.27 GB
=== RUN   TestInfra_Prerequisites/Memory
    testinfra_test.go:270: Total memory: 15.62 GB
=== NAME  TestInfra_Prerequisites
    testinfra_test.go:68:
    testinfra_test.go:69: === Infrastructure Check Summary ===
    testinfra_test.go:70: Passed: 9
    testinfra_test.go:71: Failed: 0
    testinfra_test.go:72: Skipped: 1
    testinfra_test.go:73: ==================================
--- PASS: TestInfra_Prerequisites (0.02s)
PASS
```

## CI/CD Usage

### Pre-test Check
```yaml
- name: Check Infrastructure
  run: |
    go test -v ./test/testinfra/... -run TestInfra_Prerequisites

- name: Run Tests
  run: |
    make test
```

### Validate Before Deploy
```yaml
- name: Validate Test Environment
  run: |
    go test ./test/testinfra/... -v
```

## Troubleshooting

### All Checks Pass But Tests Fail
- Check test-specific requirements
- Review test logs for specific errors
- Ensure resources are available (memory, disk)

### Specific Check Fails
1. Run the check individually
2. Read the error message
3. Follow the fix instructions above
4. Re-run testinfra to verify

### Permissions Issues
```bash
# Check group membership
groups

# Add to necessary groups
sudo usermod -aG kvm,docker $USER

# Log out and back in
```

## Best Practices

1. **Run First**: Always run testinfra before other tests
2. **Fix Issues**: Address all failed checks before proceeding
3. **Regular Checks**: Run periodically to catch environment changes
4. **CI Integration**: Include in CI pipeline to validate runners
5. **Document Custom Paths**: If using non-standard paths, document them

## Customization

### Custom Kernel Path
```bash
export SWARMCRACKER_KERNEL_PATH=/path/to/vmlinux
```

### Custom Firecracker Path
```bash
export PATH=$PATH:/custom/path/to/firecracker
```

## Summary Table

| Check | Required | Command | Fix Difficulty |
|-------|----------|---------|----------------|
| Go 1.21+ | ✅ Yes | `go version` | Easy |
| Linux/amd64 | ✅ Yes | `uname -m` | N/A |
| KVM | ✅ Yes | `ls -l /dev/kvm` | Easy |
| Firecracker | ✅ Yes | `firecracker --version` | Easy |
| Kernel | ✅ Yes | Check file exists | Easy |
| Docker/Podman | ✅ Yes | `docker --version` | Easy |
| Network | ⚠️ Optional | Create bridge | Medium |
| Disk 5GB | ✅ Yes | `df -h` | Varies |
| Memory 4GB | ⚠️ Recommended | `free -h` | Varies |

## Resources

- [Firecracker Installation](https://github.com/firecracker-microvm/firecracker)
- [KVM Setup](https://www.linux-kvm.org/page/HOWTO)
- [Docker Installation](https://docs.docker.com/engine/install/)
- [Unit Tests](unit.md) | [Integration Tests](integration.md) | [E2E Tests](e2e.md)

---

**Related**: [Unit Tests](unit.md) | [Integration Tests](integration.md) | [E2E Tests](e2e.md)
