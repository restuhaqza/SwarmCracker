# E2E Tests

End-to-end tests for SwarmCracker, testing the complete VM lifecycle from deployment to cleanup.

## Overview

These tests verify the full integration of SwarmCracker with SwarmKit and Firecracker, including:

- Service deployment
- VM startup and verification
- Service scaling
- Service updates
- Command execution in VMs
- Resource limits
- Network isolation
- Image preparation
- Failure recovery

## Prerequisites

### Required Software

1. **SwarmKit** (swarmd + swarmctl)
   ```bash
   go install github.com/moby/swarmkit/cmd/swarmd@latest
   go install github.com/moby/swarmkit/cmd/swarmctl@latest
   ```

2. **Firecracker**
   ```bash
   # Download from GitHub releases
   wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.5.0/firecracker-v1.5.0-x86_64
   sudo mv firecracker-v1.5.0-x86_64 /usr/local/bin/firecracker
   chmod +x /usr/local/bin/firecracker
   ```

3. **Container Runtime** (Docker or Podman)
   ```bash
   # Ubuntu/Debian
   sudo apt install docker.io
   
   # OR
   sudo apt install podman
   ```

4. **KVM Support**
   ```bash
   # Check if KVM is available
   ls -la /dev/kvm
   
   # If not available, install kvm-ok
   sudo apt install cpu-checker
   kvm-ok
   ```

### System Requirements

- Linux with KVM support
- 4GB+ RAM
- 2 CPU cores minimum
- 10GB free disk space

## Running Tests

### Quick Start

Using the test runner (recommended):

```bash
# Run all E2E tests
./test/e2e/run.sh

# Run with benchmarks
./test/e2e/run.sh --benchmarks

# Skip setup (if swarmd already running)
./test/e2e/run.sh --skip-setup

# Skip teardown (keep resources for inspection)
./test/e2e/run.sh --skip-teardown
```

### Manual Execution

```bash
# 1. Start swarmd
mkdir -p /var/run/swarmkit
swarmd --listen-remote-api 0.0.0.0:4242 \
       --state-dir /tmp/swarmkit \
       --manager \
       --addr localhost:4242 \
       --debug &

# 2. Set environment
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# 3. Run tests
go test ./test/e2e/... -v

# 4. Cleanup
swarmctl service ls -q | xargs -r swarmctl service rm
pkill firecracker
```

### Run Specific Tests

```bash
# Full workflow test
go test ./test/e2e -run TestFullWorkflow -v

# Service lifecycle test
go test ./test/e2e -run TestServiceLifecycle -v

# Multiple services test
go test ./test/e2e -run TestMultipleServices -v

# Resource limits test
go test ./test/e2e -run TestResourceLimits -v
```

### Run with Go Test Flags

```bash
# Verbose output
go test ./test/e2e/... -v

# Short mode (skip E2E tests)
go test ./test/e2e/... -short -v

# With coverage
go test ./test/e2e/... -cover -v

# With timeout
go test ./test/e2e/... -timeout 15m -v
```

## Test Structure

### Test Suites

1. **Full Workflow Test** (`TestFullWorkflow_DeployVerifyExecuteCleanup`)
   - Deploys service
   - Verifies VMs running
   - Executes command in VM
   - Scales service
   - Updates service
   - Cleans up

2. **Service Lifecycle Test** (`TestServiceLifecycle_DeployScaleUpdateRemove`)
   - Deploy → Scale Up → Scale Down → Update → Remove

3. **Multiple Services Test** (`TestMultipleServices_DeployAndVerify`)
   - Deploys 3 services simultaneously
   - Verifies all services running
   - Cleans up all services

4. **Resource Limits Test** (`TestResourceLimits_VMLimits`)
   - Deploys with CPU/memory limits
   - Verifies limits are enforced

5. **Network Isolation Test** (`TestNetworkIsolation_ServiceNetworking`)
   - Tests VM network connectivity
   - Verifies isolation

6. **Image Preparation Test** (`TestImagePreparation_DifferentImages`)
   - Tests different container images
   - Verifies rootfs preparation

7. **Failure Recovery Test** (`TestFailureRecovery_VMRestart`)
   - Kills running VM
   - Verifies automatic restart

### Helper Functions

- `deployService()` - Creates a SwarmKit service
- `verifyVMsRunning()` - Checks if VMs are running
- `executeCommandInVM()` - Executes command in VM
- `scaleService()` - Scales service replicas
- `updateService()` - Updates service image
- `cleanupService()` - Removes service
- `getRunningVMs()` - Gets list of running VMs

## CI/CD Integration

### GitHub Actions

```yaml
name: E2E Tests

on: [push, pull_request]

jobs:
  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v2
      
      - name: Install dependencies
        run: |
          go install github.com/moby/swarmkit/cmd/swarmd@latest
          go install github.com/moby/swarmkit/cmd/swarmctl@latest
          wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.5.0/firecracker-v1.5.0-x86_64
          sudo mv firecracker-v1.5.0-x86_64 /usr/local/bin/firecracker
          chmod +x /usr/local/bin/firecracker
      
      - name: Run E2E tests
        run: ./test/e2e/run.sh
      
      - name: Upload logs
        if: failure()
        uses: actions/upload-artifact@v2
        with:
          name: e2e-logs
          path: /tmp/swarmcracker-e2e-logs/
```

### GitLab CI

```yaml
e2e:
  stage: test
  image: golang:latest
  script:
    - apt update && apt install -y docker.io wget
    - go install github.com/moby/swarmkit/cmd/swarmd@latest
    - go install github.com/moby/swarmkit/cmd/swarmctl@latest
    - wget -O /usr/local/bin/firecracker https://github.com/firecracker-microvm/firecracker/releases/download/v1.5.0/firecracker-v1.5.0-x86_64
    - chmod +x /usr/local/bin/firecracker
    - ./test/e2e/run.sh
  artifacts:
    when: on_failure
    paths:
      - /tmp/swarmcracker-e2e-logs/
```

## Troubleshooting

### Tests Fail with "swarmd not found"

**Problem:** swarmd not in PATH
**Solution:**
```bash
export PATH=$PATH:$(go env GOPATH)/bin
```

### Tests Fail with "Firecracker not found"

**Problem:** Firecracker not installed
**Solution:**
```bash
# Download and install
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.5.0/firecracker-v1.5.0-x86_64
sudo mv firecracker-v1.5.0-x86_64 /usr/local/bin/firecracker
chmod +x /usr/local/bin/firecracker
```

### Tests Fail with "VM not starting"

**Problem:** VM fails to start
**Solution:**
```bash
# Check swarmd logs
tail -f /tmp/swarmkit-e2e/swarmd.log

# Check Firecracker logs
sudo journalctl -u swarmcracker -f

# Verify KVM is available
kvm-ok
```

### Tests Fail with "Permission denied"

**Problem:** Insufficient permissions
**Solution:**
```bash
# Run with sudo
sudo ./test/e2e/run.sh

# Or add user to required groups
sudo usermod -aG kvm,libvirt $USER
newgrp kvm
```

## Debugging

### Enable Verbose Logging

```bash
# Run tests with debug logs
SWARMCRACKER_LOG=debug ./test/e2e/run.sh
```

### Keep Resources After Tests

```bash
# Skip teardown to inspect VMs
./test/e2e/run.sh --skip-teardown

# Check running VMs
ps aux | grep firecracker

# Check VM directories
ls -la /srv/jailer/
```

### View Test Logs

```bash
# Test logs
cat /tmp/swarmcracker-e2e-logs/e2e-test.log

# Swarmd logs
cat /tmp/swarmcracker-e2e-logs/swarmd.log

# Benchmark results
cat /tmp/swarmcracker-e2e-logs/benchmark.log
```

## Writing New Tests

### Template

```go
func TestE2E_YourFeature(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping E2E test in short mode")
    }

    require.True(t, hasSwarmd(), "swarmd required")
    require.True(t, hasFirecracker(), "Firecracker required")

    ctx := context.Background()
    serviceName := "e2e-your-feature"

    // Cleanup before test
    _ = cleanupService(ctx, t, serviceName)

    // Your test logic here
    t.Run("Step1", func(t *testing.T) {
        // ...
    })

    // Cleanup after test
    cleanupService(ctx, t, serviceName)
}
```

### Best Practices

1. **Always cleanup** - Use `defer cleanupService()` to ensure cleanup
2. **Use unique names** - Prefix services with "e2e-" to identify test services
3. **Wait for readiness** - Add sleep/retry logic after operations
4. **Log everything** - Use `t.Log()` for debugging
5. **Check prerequisites** - Use helper functions to verify requirements
6. **Handle failures gracefully** - Use `require.NoError()` for critical failures

## Performance Benchmarks

### Running Benchmarks

```bash
# Run all benchmarks
./test/e2e/run.sh --benchmarks

# Run specific benchmark
go test ./test/e2e -bench=BenchmarkVMStartup -benchmem

# Run with CPU profiling
go test ./test/e2e -bench=. -cpuprofile=cpu.prof

# Run with memory profiling
go test ./test/e2e -bench=. -memprofile=mem.prof
```

### Benchmark Metrics

- **VM Startup Time:** Time from service creation to VM running
- **Image Preparation Time:** Time to extract and prepare rootfs
- **Service Scale Time:** Time to scale from 1 to N replicas
- **Memory Usage:** Peak memory during tests

## Contributing

When adding new E2E tests:

1. Follow the template above
2. Add documentation to this README
3. Update CI/CD configurations
4. Test locally before committing
5. Add timeout handling

## Resources

- [SwarmKit Documentation](https://github.com/moby/swarmkit)
- [Firecracker Documentation](https://github.com/firecracker-microvm/firecracker)
- [SwarmCracker Documentation](../../docs/)

---

**Last Updated:** 2026-04-04
**Maintained By:** SwarmCracker Team
