# Integration Tests

This document describes how to run integration tests for SwarmCracker.

## Prerequisites

Integration tests require the following components:

### 1. Firecracker
Install Firecracker v1.14.0 or later (v1.15.1 is what `swarmcracker setup install` fetches) from the [official releases](https://github.com/firecracker-microvm/firecracker/releases).

```bash
# Download the release tarball for your architecture
ARCH="$(uname -m)"   # x86_64 or aarch64
wget "https://github.com/firecracker-microvm/firecracker/releases/download/v1.15.1/firecracker-v1.15.1-${ARCH}.tgz"

# Install
tar -xzf "firecracker-v1.15.1-${ARCH}.tgz"
sudo mv "release-v1.15.1-${ARCH}/firecracker-v1.15.1-${ARCH}" /usr/local/bin/firecracker
sudo chmod +x /usr/local/bin/firecracker

# Verify
firecracker --version
```

### 2. Firecracker Kernel
Download a compatible kernel image:

```bash
# Use the helper script, which downloads a Firecracker-compatible kernel
# to /usr/share/firecracker/vmlinux:
sudo ./test/integration/download-kernel.sh
```

### 3. KVM Access
Ensure your user has access to `/dev/kvm`:

```bash
# Add user to kvm group
sudo usermod -aG kvm $USER

# Log out and back in for changes to take effect

# Verify KVM access
ls -l /dev/kvm
```

### 4. Container Runtime
Install either Docker or Podman:

#### Docker
```bash
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker $USER
```

#### Podman
```bash
# Debian/Ubuntu
sudo apt install podman

# Verify
podman --version
```

### 5. Network Bridge (Optional)
For network tests, you may need to set up a bridge:

```bash
# Create bridge
sudo ip link add swarm-br0 type bridge
sudo ip addr add 172.17.0.1/16 dev swarm-br0
sudo ip link set swarm-br0 up

# Enable IP forwarding
sudo sysctl -w net.ipv4.ip_forward=1
```

## Running Integration Tests

### Run All Integration Tests
```bash
go test ./test/integration/... -v
```

### Run Specific Test
```bash
# Test prerequisites only
go test ./test/integration/... -v -run TestIntegration_PrerequisitesOnly

# Test image preparation
go test ./test/integration/... -v -run TestIntegration_ImagePreparation

# Test end-to-end flow
go test ./test/integration/... -v -run TestIntegration_EndToEnd
```

### With Timeout
Integration tests may take longer, especially when pulling images:

```bash
go test ./test/integration/... -v -timeout 30m
```

## Test Descriptions

### TestIntegration_PrerequisitesOnly
Checks what prerequisites are installed. Does not require any components.
- Always runs
- Reports what's available
- Provides installation instructions

### TestIntegration_ImagePreparation
Tests OCI image to rootfs conversion.
- Requires: Container runtime (docker or podman)
- Pulls real container images
- Creates ext4 filesystem images
- Verifies output format

### TestIntegration_VMMManager
Tests VMM lifecycle management.
- Requires: Firecracker
- Tests VM creation and monitoring
- Checks status reporting

### TestIntegration_NetworkSetup
Tests network configuration.
- May require: Root privileges
- Tests bridge creation
- Tests TAP device setup

### TestIntegration_TaskTranslation
Tests task to Firecracker config translation.
- No external requirements
- Verifies JSON config generation
- Tests resource mapping

### TestIntegration_EndToEnd
Complete end-to-end test.
- Requires: All prerequisites
- Pulls image → Prepares rootfs → Starts VM → Verifies → Cleanup
- Full integration test

## Expected Output

### With All Prerequisites
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

### Missing Prerequisites
Tests will be skipped with clear messages:
```
--- SKIP: TestIntegration_EndToEnd (0.00s)
    integration_test.go:xxx: Firecracker not found. Install from: https://github.com/firecracker-microvm/firecracker
```

## Troubleshooting

### Permission Denied on /dev/kvm
```bash
# Add user to kvm group
sudo usermod -aG kvm $USER

# Then log out and back in
```

### Firecracker Not Found
```bash
# Check if installed
which firecracker

# If not, install from releases
ARCH="$(uname -m)"
wget "https://github.com/firecracker-microvm/firecracker/releases/download/v1.15.1/firecracker-v1.15.1-${ARCH}.tgz"
tar -xzf "firecracker-v1.15.1-${ARCH}.tgz"
sudo mv "release-v1.15.1-${ARCH}/firecracker-v1.15.1-${ARCH}" /usr/local/bin/firecracker
sudo chmod +x /usr/local/bin/firecracker
```

### Docker Daemon Not Running
```bash
# Start docker
sudo systemctl start docker

# Enable on boot
sudo systemctl enable docker
```

### Image Pull Fails
```bash
# Test container runtime manually
docker pull alpine:latest

# Or with podman
podman pull alpine:latest
```

## CI/CD Integration

For automated testing, you can set up integration tests in GitHub Actions:

```yaml
name: Integration Tests

on: [push, pull_request]

jobs:
  integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: actions/setup-go@v6
        with:
          go-version: '1.26'

      - name: Install Firecracker
        run: |
          ARCH="$(uname -m)"
          wget "https://github.com/firecracker-microvm/firecracker/releases/download/v1.15.1/firecracker-v1.15.1-${ARCH}.tgz"
          tar -xzf "firecracker-v1.15.1-${ARCH}.tgz"
          sudo mv "release-v1.15.1-${ARCH}/firecracker-v1.15.1-${ARCH}" /usr/local/bin/firecracker
          sudo chmod +x /usr/local/bin/firecracker

      - name: Install Kernel
        run: sudo ./test/integration/download-kernel.sh

      - name: Run Integration Tests
        run: go test ./test/integration/... -v -timeout 30m
```

## Continuous Testing

For development, you can run integration tests in watch mode:

```bash
# Install entr
sudo apt install entr

# Watch for changes and re-run tests
find . -name "*.go" | entr -r go test ./test/integration/... -v
```
