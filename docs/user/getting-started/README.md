# Getting Started with SwarmCracker

> Set up a SwarmCracker cluster in under 10 minutes.

---

## Prerequisites

### Hardware

| Requirement | Minimum | Recommended |
|-------------|---------|-------------|
| Manager node | 1 vCPU, 1 GB RAM | 2 vCPU, 2 GB RAM |
| Worker node | 2 vCPU, 4 GB RAM | 4 vCPU, 8 GB RAM |

### Software

- **Linux** (Ubuntu 20.04+, Debian 11+, or KVM-compatible distro)
- **KVM** — Hardware virtualization enabled
- **Root access** — For bridge and Firecracker setup

### Verify KVM

```bash
ls -la /dev/kvm                    # Must exist
lscpu | grep Virtualization        # VT-x (Intel) or AMD-V (AMD)
```

### Verify Nested Virtualization (for Vagrant/libvirt testing)

If running SwarmCracker inside a VM (e.g., Vagrant), nested virtualization must be enabled:

```bash
# Check if nested virt is enabled
cat /sys/module/kvm_intel/parameters/nested  # Should show 'Y'

# Enable if needed
sudo modprobe kvm_intel nested=1
```

---

## Installation

### Option 1: One-Line Install (Recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/docs/site/install.sh | sudo bash
```

This installs:
- Firecracker v1.15.1
- Jailer (security sandbox)
- SwarmCracker binaries
- Default configuration

### Option 2: Build from Source

```bash
git clone https://github.com/restuhaqza/SwarmCracker
cd SwarmCracker
make build
sudo make install
```

See [Build Guide](../guides/advanced.md#build-from-source) for details.

### Setup Firecracker Kernel

**Critical:** Firecracker requires an uncompressed ELF kernel. The kernel must be at `/usr/share/firecracker/vmlinux`.

```bash
# Option A: Extract from host kernel (recommended)
sudo mkdir -p /usr/share/firecracker
./test-automation/scripts/extract-vmlinux.sh /boot/vmlinuz-* /usr/share/firecracker/vmlinux

# Verify
file /usr/share/firecracker/vmlinux
# Should show: ELF 64-bit LSB executable, x86-64
```

⚠️ **Do not download from GitHub raw URLs** — they return HTML pages, not binaries!

### Option 3: Vagrant Test Cluster

For local development with a 3-node cluster:

```bash
git clone https://github.com/restuhaqza/SwarmCracker
cd SwarmCracker
vagrant up
```

See [Developer Docs](../../dev/) for details.

---

## Quick Start

### 1. Initialize Manager

```bash
# On manager node
# IMPORTANT: Set --advertise-remote-api to your actual IP!
MANAGER_IP=$(ip addr show eth0 | grep 'inet ' | awk '{print $2}' | cut -d/ -f1)

swarmd-firecracker --manager \
  --hostname manager-1 \
  --listen-remote-api 0.0.0.0:4242 \
  --advertise-remote-api $MANAGER_IP:4242 \
  --kernel-path /usr/share/firecracker/vmlinux \
  --rootfs-dir /var/lib/firecracker/rootfs \
  --bridge-name swarm-br0
```

This creates:
- SwarmKit manager with Raft consensus
- Control socket at `/var/run/swarmkit/swarm.sock`
- TLS certificates for secure communication
- Join tokens at `/var/lib/swarmkit/manager/join-tokens.txt`

⚠️ **Without `--advertise-remote-api`, workers cannot connect!**

### 2. Get Join Token

```bash
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
swarmctl cluster inspect default

# Output includes:
# Join Tokens:
#   Worker: SWMTKN-1-abc123...
#   Manager: SWMTKN-1-def456...
```

### 3. Join Workers

```bash
# On each worker node
swarmcracker join \
  --hostname worker-1 \
  --manager <manager-ip>:4242 \
  --token <WORKER_TOKEN>
```

### 4. Verify Cluster

```bash
swarmctl ls-nodes

# Output:
# ID            STATUS   HOSTNAME     AVAILABILITY
# abc123        READY    manager-1    ACTIVE
# def456        READY    worker-1     ACTIVE
# ghi789        READY    worker-2     ACTIVE
```

---

## Deploy Services

### Create a Service

```bash
swarmctl create-service nginx:latest

# Output:
# Service created: svc-nginx-143022
# Image: nginx:latest
```

### Scale Service

```bash
swarmctl scale svc-nginx-143022 3
```

### Verify Tasks

```bash
swarmctl ls-tasks

# Each task is a Firecracker microVM
# ID          SERVICE     STATUS    NODE
# task-abc    nginx       RUNNING   worker-1
# task-def    nginx       RUNNING   worker-2
# task-ghi    nginx       RUNNING   worker-1
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    SwarmCracker Cluster                      │
│                                                              │
│   ┌──────────────┐                                           │
│   │ Manager       │  SwarmKit control plane                  │
│   │ swarmd        │  - Schedules tasks                       │
│   └───────────────┘  - Maintains cluster state               │
│          │ gRPC                                             │
│    ┌─────┴─────────────────┐                               │
│    │                       │                                │
│ ┌──▼─────────┐  ┌─────────▼──┐                             │
│ │ Worker-1    │  │ Worker-2    │                            │
│ │ swarm-br0   │  │ swarm-br0   │  VXLAN overlay            │
│ │ ┌───┐┌───┐ │  │ ┌───┐┌───┐ │                            │
│ │ │VM1││VM2│ │  │ │VM3││VM4│ │                            │
│ │ └───┘└───┘ │  │ └───┘└───┘ │                            │
│ └─────────────┘  └─────────────┘                            │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

**Key Components:**

| Component | Purpose |
|-----------|---------|
| **SwarmKit Manager** | Scheduling, state management, Raft consensus |
| **SwarmKit Worker** | Executes tasks via executor interface |
| **Firecracker Executor** | SwarmCracker's custom executor (microVMs) |
| **swarm-br0** | Linux bridge for VM networking |
| **VXLAN Overlay** | Cross-node VM communication |

---

## Next Steps

- [Configuration Guide](../guides/configuration.md) — Customize settings
- [SwarmKit Guide](../guides/swarmkit.md) — Full service management
- [Networking Guide](../guides/networking.md) — VXLAN, TAP devices
- [Security Guide](../guides/security.md) — Jailer hardening

---

## Troubleshooting

### Kernel: Invalid ELF Magic Number

This means the kernel file is not a valid ELF binary (likely HTML or corrupted).

```bash
# Check kernel file type
file /usr/share/firecracker/vmlinux

# If it shows "HTML document", re-extract from host:
./test-automation/scripts/extract-vmlinux.sh /boot/vmlinuz-* /usr/share/firecracker/vmlinux
```

### KVM Not Found

```bash
sudo modprobe kvm_intel   # Intel
sudo modprobe kvm_amd     # AMD
```

### Missing KVM Capabilities (Nested Virtualization)

If running inside a VM:

```bash
# Check nested virt
cat /sys/module/kvm_intel/parameters/nested

# Enable if 'N'
sudo modprobe -r kvm_intel
sudo modprobe kvm_intel nested=1

# Or add to /etc/modprobe.d/kvm-nested.conf:
# options kvm_intel nested=1
```

### Node Won't Join

```bash
# Check manager is reachable
curl http://<manager-ip>:4242

# Verify token
sudo cat /var/lib/swarmkit/manager/join-tokens.txt

# Check manager advertise address is set
ps aux | grep swarmd | grep advertise
```

### Image Extraction Fails

```bash
# Ensure Docker is installed and running
sudo systemctl status docker

# Pull image manually to verify
sudo docker pull nginx:alpine
```

### Services Not Starting

```bash
# Check node status
swarmctl ls-nodes

# Check executor logs
journalctl -u swarmd -f

# Verify kernel is ELF
file /usr/share/firecracker/vmlinux
```

---

**See Also:** [CLI Reference](../reference/cli.md) | [Architecture](../architecture/)