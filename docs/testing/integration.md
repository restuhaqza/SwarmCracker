# Integration Testing

Integration tests verify that components work together with real external dependencies like Firecracker and container runtimes.

## Prerequisites

Integration tests require:

1. **Firecracker** - MicroVM runtime
2. **Firecracker Kernel** - vmlinux kernel image
3. **KVM Access** - Hardware virtualization support
4. **Container Runtime** - Docker or Podman

### Setup Prerequisites

```bash
# Install Firecracker
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/firecracker-v1.8.0-x86_64
sudo mv firecracker-v1.8.0-x86_64 /usr/local/bin/firecracker
sudo chmod +x /usr/local/bin/firecracker

# Download kernel
sudo mkdir -p /usr/share/firecracker
sudo wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/vmlinux-v1.8.0 -O /usr/share/firecracker/vmlinux

# Or use local path
mkdir -p ~/.local/share/firecracker
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/vmlinux-v1.8.0 \
  -O ~/.local/share/firecracker/vmlinux

# Add user to KVM group
sudo usermod -aG kvm $USER
# Log out and back in

# Verify
ls -l /dev/kvm
```

## Running Integration Tests

### All Integration Tests
```bash
make integration-test

# Or with go test
go test -v -tags=integration ./test/integration/...
```

### Specific Test
```bash
# Prerequisites check
go test -v ./test/integration/... -run TestIntegration_PrerequisitesOnly

# Image preparation
go test -v ./test/integration/... -run TestIntegration_ImagePreparation

# VMM lifecycle
go test -v ./test/integration/... -run TestIntegration_VMMManager

# End-to-end flow
go test -v ./test/integration/... -run TestIntegration_EndToEnd
```

### With Timeout
Integration tests may take longer when pulling images:
```bash
go test -v -tags=integration -timeout 30m ./test/integration/...
```

## Test Descriptions

### TestIntegration_PrerequisitesOnly
**Purpose**: Check what prerequisites are available

**Requirements**: None

**What it checks**:
- ✅ Firecracker binary
- ✅ Firecracker kernel
- ✅ KVM device
- ✅ Container runtime (Docker/Podman)

**Duration**: < 1 second

**Use for**: Verifying test environment setup

---

### TestIntegration_ImagePreparation
**Purpose**: Test OCI image to rootfs conversion

**Requirements**:
- Container runtime (Docker or Podman)

**What it tests**:
- Pulls real container images
- Creates ext4 filesystem images
- Verifies output format and size

**Duration**: 5-10 minutes (first run, with image pull)

**Example output**:
```
=== RUN   TestIntegration_ImagePreparation
    integration_test.go:xxx: Testing image preparation with docker
    integration_test.go:xxx: Rootfs created at: /tmp/xxx/nginx-latest.ext4
    integration_test.go:xxx: Rootfs size: 8.45 MB (8864768 bytes)
--- PASS: TestIntegration_ImagePreparation (312.45s)
```

---

### TestIntegration_VMMManager
**Purpose**: Test VMM lifecycle management

**Requirements**:
- Firecracker binary
- Firecracker kernel
- KVM access

**What it tests**:
- VM creation and monitoring
- Status reporting
- Cleanup verification

**Duration**: 5-10 seconds

**Use for**: Validating Firecracker integration

---

### TestIntegration_EndToEnd
**Purpose**: Complete end-to-end test

**Requirements**: All prerequisites

**What it tests**:
1. Pull image → Prepare rootfs
2. Start VM → Verify running
3. Check task status
4. Cleanup resources

**Duration**: 2-5 minutes

**Example flow**:
```
1. Prepare task (pull image, create rootfs)
2. Start task (launch Firecracker VM)
3. Wait for VM to be ready
4. Verify VM is running
5. Stop VM
6. Remove VM and cleanup
```

---

### TestIntegration_NetworkSetup
**Purpose**: Test network configuration

**Requirements**: Root privileges (for bridge creation)

**What it tests**:
- Bridge creation
- TAP device setup
- Network cleanup

**Note**: May skip without privileges

---

### TestIntegration_TaskTranslation
**Purpose**: Test task to VMM config translation

**Requirements**: Kernel image

**What it tests**:
- Generates Firecracker JSON config
- Verifies config structure
- Tests resource mapping

**Duration**: < 1 second

## Test Structure

```
test/integration/
├── integration_test.go      # Main integration tests
├── README.md                # Integration test guide
├── FIRECRACKER_SETUP.md     # Firecracker setup instructions
└── check-prereqs.sh         # Prerequisites checker
```

## Writing Integration Tests

### Basic Template
```go
//go:build integration
// +build integration

package integration_test

import (
    "testing"
    "github.com/stretchr/testify/require"
)

func TestIntegration_YourFeature(t *testing.T) {
    // Check prerequisites
    if !hasFirecracker() {
        t.Skip("Firecracker not found")
    }

    // Test logic
    t.Run("Setup", func(t *testing.T) {
        // Setup code
    })

    t.Run("Execute", func(t *testing.T) {
        // Execute test
    })

    t.Run("Cleanup", func(t *testing.T) {
        // Cleanup
    })
}
```

### Using Test Fixtures
```go
import "github.com/restuhaqza/swarmcracker/test/e2e/fixtures"

func TestIntegration_WithFixtures(t *testing.T) {
    task := fixtures.SmallTestTask("test-1")

    // Test with fixture
    err := executor.Prepare(ctx, task)
    require.NoError(t, err)
}
```

## Troubleshooting

### "Firecracker not found"
```bash
which firecracker
# If not found, install from releases
```

### "KVM device not available"
```bash
ls -l /dev/kvm
# Add user to kvm group
sudo usermod -aG kvm $USER
```

### "Kernel not found"
```bash
# Check searched paths
ls -la /usr/share/firecracker/vmlinux
ls -la ~/.local/share/firecracker/vmlinux
ls -la /boot/vmlinux
```

### "Permission denied on /dev/kvm"
```bash
# Fix permissions
sudo chmod 666 /dev/kvm
# Or add user to kvm group (recommended)
sudo usermod -aG kvm $USER
```

### "Image pull fails"
```bash
# Test container runtime manually
docker pull alpine:latest
# Or with podman
podman pull alpine:latest
```

### "VM fails to start"
```bash
# Check kernel path
ls -la ~/.local/share/firecracker/vmlinux

# Check KVM access
ls -l /dev/kvm

# Enable debug logging
export SWARMCRACKER_LOG_LEVEL=debug
```

## CI/CD Integration

### GitHub Actions Example
```yaml
name: Integration Tests

on: [push, pull_request]

jobs:
  integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'

      - name: Install Firecracker
        run: |
          wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/firecracker-v1.8.0-x86_64
          sudo mv firecracker-v1.8.0-x86_64 /usr/local/bin/firecracker
          sudo chmod +x /usr/local/bin/firecracker

      - name: Install Kernel
        run: |
          sudo mkdir -p /usr/share/firecracker
          sudo wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/vmlinux-v1.8.0 -O /usr/share/firecracker/vmlinux

      - name: Run Integration Tests
        run: go test -v -tags=integration -timeout 30m ./test/integration/...
```

## Expected Output

### Successful Test Run
```
=== RUN   TestIntegration_EndToEnd
    integration_test.go:xxx: Running integration tests with docker
    integration_test.go:xxx: Preparing task...
    integration_test.go:xxx: Rootfs created at: /tmp/xxx/nginx-latest.ext4
    integration_test.go:xxx: Starting task...
    integration_test.go:xxx: Task is running!
    integration_test.go:xxx: Cleaning up...
--- PASS: TestIntegration_EndToEnd (120.45s)
```

### Skipping Tests
```
--- SKIP: TestIntegration_EndToEnd (0.00s)
    integration_test.go:xxx: Firecracker not found. Install from: https://github.com/firecracker-microvm/firecracker
```

## Continuous Testing

For development, run integration tests in watch mode:
```bash
# Install entr
sudo apt install entr

# Watch for changes and re-run tests
find . -name "*.go" | entr -r go test -v -tags=integration ./test/integration/...
```

## Best Practices

1. **Check Prerequisites First**: Always check for required dependencies
2. **Skip Gracefully**: Use `t.Skip()` for missing dependencies
3. **Clean Up Resources**: Use `t.Cleanup()` for automatic cleanup
4. **Use Timeouts**: Integration tests can hang, set reasonable timeouts
5. **Log Verbosely**: Integration tests run less frequently, log everything
6. **Test Real Scenarios**: Use actual Firecracker, container runtime, etc.
7. **Verify Cleanup**: Ensure no resources leak after tests

## Resources

- [Firecracker Documentation](https://github.com/firecracker-microvm/firecracker)
- [Firecracker Setup](FIRECRACKER_SETUP.md)
- [Integration Test README](README.md)
- [Unit Tests](unit.md) | [E2E Tests](e2e.md)

---

**Related**: [Unit Tests](unit.md) | [E2E Tests](e2e.md) | [Test Infrastructure](testinfra.md)
