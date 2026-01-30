# Installation Guide

This guide covers installing and setting up SwarmCracker on Linux systems.

## Prerequisites

### System Requirements

- **OS**: Linux (kernel 4.14+ recommended)
- **Architecture**: x86_64 or ARM64
- **Virtualization**: KVM support enabled
- **Memory**: 4GB+ recommended (depends on workload)
- **Storage**: 10GB+ free space

### Required Software

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.21+ | Building SwarmCracker |
| Firecracker | v1.0.0+ | MicroVM runtime |
| Docker | 20.10+ | Container runtime (for image prep) |
| make | any | Build automation |

## Check Prerequisites

### Verify KVM Support

```bash
# Check if KVM is available
ls -la /dev/kvm

# Test KVM functionality
cat /dev/kvm
# Should output: kvm: No such device or address (expected, proves KVM exists)
```

### Check Virtualization Support

```bash
# For Intel/AMD
grep -E 'vmx|svm' /proc/cpuinfo

# Should show flags for your CPU
```

## Installation Methods

### Method 1: Build from Source

#### Clone Repository

```bash
git clone https://github.com/restuhaqza/swarmcracker.git
cd swarmcracker
```

#### Install Firecracker

**Download Firecracker:**

```bash
# Download latest release
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/firecracker-v1.8.0-x86_64

# Make executable
chmod +x firecracker-v1.8.0-x86_64

# Move to path
sudo mv firecracker-v1.8.0-x86_64 /usr/local/bin/firecracker
```

**Verify Firecracker:**

```bash
firecracker --version
# Expected: Firecracker v1.8.0
```

#### Build SwarmCracker

```bash
# Download dependencies
go mod download

# Run tests (optional)
make test

# Build binaries
make build

# Install to $GOPATH/bin or /usr/local/bin
make install
```

#### Verify Installation

```bash
swarmcracker-kit --help
```

### Method 2: Using Docker

Build a Docker image:

```bash
make docker-image
```

Run from Docker:

```bash
docker run -d \
  --device /dev/kvm \
  --cap-add NET_ADMIN \
  -v /var/lib/firecracker:/var/lib/firecracker \
  swarmcracker:latest
```

## Configuration

### Create Configuration File

Create `/etc/swarmcracker/config.yaml`:

```yaml
executor:
  name: firecracker
  kernel_path: "/usr/share/firecracker/vmlinux"
  initrd_path: "/usr/share/firecracker/initrd.img"
  rootfs_dir: "/var/lib/firecracker/rootfs"
  socket_dir: "/var/run/firecracker"
  default_vcpus: 2
  default_memory_mb: 1024
  enable_jailer: true
  jailer:
    uid: 1000
    gid: 1000
    chroot_base_dir: "/srv/jailer"
    netns: "/var/run/netns/firecracker"

network:
  bridge_name: "swarm-br0"
  enable_rate_limit: true
  max_packets_per_sec: 10000

logging:
  level: "info"
  format: "json"
  output: "stdout"

images:
  cache_dir: "/var/cache/swarmcracker"
  max_cache_size_mb: 10240
  enable_layer_cache: true

metrics:
  enabled: true
  address: "0.0.0.0:9090"
  format: "prometheus"
```

### Create Directories

```bash
# Create required directories
sudo mkdir -p /var/lib/firecracker/rootfs
sudo mkdir -p /var/run/firecracker
sudo mkdir -p /var/cache/swarmcracker
sudo mkdir -p /etc/swarmcracker
sudo mkdir -p /srv/jailer

# Set permissions
sudo chown -R $(whoami):$(whoami) /var/lib/firecracker
sudo chown -R $(whoami):$(whoami) /var/run/firecracker
sudo chown -R $(whoami):$(whoami) /var/cache/swarmcracker
```

### Prepare Kernel

Firecracker needs a kernel image. Options:

**Option 1: Use pre-built kernel**

```bash
# Download official Firecracker kernel
wget https://s3.amazonaws.com/spec.ccfc.minimize/elfbins/kernel/vmlinux-4.14.188
sudo mv vmlinux-4.14.188 /usr/share/firecracker/vmlinux
```

**Option 2: Build custom kernel**

See [Firecracker docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/building-rootfs-and-kernel-guide.md) for details.

## Network Setup

### Create Bridge

```bash
# Create bridge
sudo ip link add swarm-br0 type bridge
sudo ip addr add 172.18.0.1/16 dev swarm-br0
sudo ip link set swarm-br0 up

# Enable NAT (optional)
sudo iptables -t nat -A POSTROUTING -s 172.18.0.0/16 -j MASQUERADE
sudo iptables -A FORWARD -i swarm-br0 -o eth0 -j ACCEPT
sudo iptables -A FORWARD -i eth0 -o swarm-br0 -m state --state RELATED,ESTABLISHED -j ACCEPT

# Make persistent (Ubuntu/Debian)
cat << EOF | sudo tee /etc/network/interfaces.d/swarm-br0
auto swarm-br0
iface swarm-br0 inet static
    address 172.18.0.1
    netmask 255.255.0.0
    bridge_ports none
    bridge_stp off
    bridge_fd 0
EOF
```

### Verify Network

```bash
# Check bridge
ip link show swarm-br0

# Should show:
# 5: swarm-br0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP mode DEFAULT
```

## Running SwarmCracker

### CLI Tool Usage

The `swarmcracker-kit` CLI provides a simple interface for running and testing:

#### Validate Configuration

```bash
# Validate configuration file
swarmcracker-kit validate --config /etc/swarmcracker/config.yaml

# Expected output:
# ✓ Configuration is valid
#   Kernel: /usr/share/firecracker/vmlinux
#   Rootfs: /var/lib/firecracker/rootfs
#   Bridge: swarm-br0
#   VCPUs: 2
#   Memory: 1024 MB
#   Jailer: true
```

#### Run Containers as MicroVMs

```bash
# Run a container as a microVM (test mode - validation only)
swarmcracker-kit run --config /etc/swarmcracker/config.yaml --test nginx:latest

# Run with custom resources
swarmcracker-kit run --vcpus 2 --memory 1024 nginx:latest

# Run in detached mode
swarmcracker-kit run --detach nginx:latest

# Run with environment variables
swarmcracker-kit run -e APP_ENV=production -e DEBUG=false nginx:latest

# Override kernel or rootfs from command line
swarmcracker-kit run --kernel /custom/path/vmlinux nginx:latest
```

#### Show Version

```bash
swarmcracker-kit version

# Expected output:
# SwarmCracker Kit v0.1.0-alpha
#   Build Time: 2024-01-30T12:00:00Z
#   Git Commit: abc123def
#   Go Version: 1.21 (linux/amd64)
```

### Standalone Mode

```bash
# The CLI can be used standalone for testing and development
# No SwarmKit required for basic functionality

# Example: Test your setup
swarmcracker-kit run --test nginx:latest

# Example: Run with custom configuration
swarmcracker-kit run \
  --config /etc/swarmcracker/config.yaml \
  --vcpus 4 \
  --memory 2048 \
  nginx:latest
```

### With SwarmKit

```bash
# Start SwarmKit agent with custom executor
swarmd \
  --addr 0.0.0.0:4242 \
  --remote-addrs <manager-ip>:4242 \
  --executor-firecracker \
  --executor-firecracker-config /etc/swarmcracker/config.yaml
```

## Verification

### Test Setup

```bash
# Validate configuration
swarmcracker-kit validate

# Run in test mode (no actual VM execution)
swarmcracker-kit run --test nginx:latest

# Expected output:
# [INF] Test mode: validating image reference image=nginx:latest
# [INF] Task created successfully image=nginx:latest task_id=task-1234567890
```

### Run Example

```bash
# Run nginx microVM (test mode)
swarmcracker-kit run --test nginx:latest

# Run with full execution (requires Firecracker and proper setup)
# Note: This will actually start a microVM
swarmcracker-kit run --detach nginx:latest

# Expected: VM starts, nginx accessible via assigned IP
```

## Troubleshooting

### Firecracker Permission Denied

```bash
# Add user to kvm group
sudo usermod -a -G kvm $USER

# Log out and back in
```

### Bridge Creation Failed

```bash
# Install bridge-utils
sudo apt-get install bridge-utils

# Or use iproute2
sudo apt-get install iproute2
```

### Kernel Not Found

```bash
# Verify kernel exists
ls -la /usr/share/firecracker/vmlinux

# Check config
grep kernel_path /etc/swarmcracker/config.yaml
```

### Socket Permission Denied

```bash
# Fix socket directory permissions
sudo chown -R $(whoami):$(whoami) /var/run/firecracker
```

## Uninstallation

```bash
# Stop services
sudo systemctl stop swarmcracker

# Remove binaries
sudo rm /usr/local/bin/swarmcracker-kit
sudo rm /usr/local/bin/firecracker

# Remove directories
sudo rm -rf /var/lib/firecracker
sudo rm -rf /var/run/firecracker
sudo rm -rf /var/cache/swarmcracker
sudo rm -rf /etc/swarmcracker

# Remove bridge (optional)
sudo ip link delete swarm-br0
```

## Next Steps

- Read [Configuration Guide](CONFIG.md) for advanced config
- See [Development Guide](DEVELOPMENT.md) for contributing
- Check [Architecture docs](ARCHITECTURE.md) for internals
