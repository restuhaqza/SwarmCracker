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

### Option 3: Vagrant Test Cluster

For local development with a 3-node cluster:

```bash
git clone https://github.com/restuhaqza/SwarmCracker
cd SwarmCracker
vagrant up
```

See [Development Setup](../development/) for details.

---

## Quick Start

### 1. Initialize Manager

```bash
# On manager node
swarmcracker init --hostname manager-1
```

This creates:
- SwarmKit manager with Raft consensus
- Control socket at `/var/run/swarmkit/swarm.sock`
- TLS certificates for secure communication

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

### KVM Not Found

```bash
sudo modprobe kvm_intel   # Intel
sudo modprobe kvm_amd     # AMD
```

### Node Won't Join

```bash
# Check manager is reachable
curl http://<manager-ip>:4242

# Verify token
swarmctl cluster inspect default
```

### Services Not Starting

```bash
# Check node status
swarmctl ls-nodes

# Check executor logs
journalctl -u swarmcracker -f
```

---

**See Also:** [CLI Reference](../reference/cli.md) | [Architecture](../architecture/)