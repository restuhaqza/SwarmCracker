# Quick Start - Firecracker Worker Setup

## Overview

This quick start guide will help you set up a SwarmKit cluster with **Firecracker executor** support.

## Prerequisites

- Vagrant installed
- VirtualBox installed
- Go 1.21+ installed on host

## Cluster Architecture

```
┌─────────────────────────────────────┐
│  Manager Node (swarmd)              │
│  - Orchestrates tasks               │
│  - Schedules to workers             │
└─────────────────────────────────────┘
              ↓ gRPC
┌─────────────────────────────────────┐
│  Worker Node (swarmd-firecracker)  │
│  - Receives tasks via gRPC         │
│  - SwarmCracker Executor            │
│    ├─ Image Preparer (podman)      │
│    ├─ Network Manager              │
│    └─ VMM Manager (Firecracker)    │
└─────────────────────────────────────┘
              ↓
┌─────────────────────────────────────┐
│  Firecracker VMM per task          │
│  - MicroVM isolation               │
│  - KVM acceleration                │
│  - Own kernel & rootfs             │
└─────────────────────────────────────┘
```

## Setup Steps

### 1. Start the Cluster

```bash
cd test-automation
vagrant up manager worker1
```

This will:
- Create manager node with standard swarmd
- Create worker1 node with Firecracker support
- Install all dependencies including podman
- Configure the cluster automatically

### 2. Verify Cluster Status

```bash
vagrant ssh manager -c "
sudo swarmctl -s /var/run/swarmkit/swarm.sock node ls
"
```

Expected output:
```
ID    Name              Status  Availability
----  ----              ------  --------------
xxx   swarm-manager     READY   ACTIVE        REACHABLE *
yyy   swarm-worker-1    READY   ACTIVE
```

### 3. Deploy a Test Service

```bash
vagrant ssh manager -c "
sudo swarmctl -s /var/run/swarmkit/swarm.sock service create \\
  --name nginx \\
  --image nginx:alpine \\
  --replicas 2
"
```

### 4. Check Task Status

```bash
vagrant ssh manager -c "
sudo swarmctl -s /var/run/swarmkit/swarm.sock task ls
"
```

Expected output:
```
ID    Service  Desired State  Last State  Node
----  -------  -------------  ----------  ----
xxx   nginx.1  READY          RUNNING     swarm-worker-1  ← Running in Firecracker!
yyy   nginx.2  READY          RUNNING     swarm-manager
```

## What Makes This Different?

### Standard SwarmKit (Docker)
```
Task → Docker Engine → Container
```

### SwarmCracker (Firecracker)
```
Task → SwarmCracker Executor → Firecracker VMM → MicroVM
       ├─ Podman pulls image
       ├─ Extracts to rootfs
       ├─ Creates ext4 image
       ├─ Spawns Firecracker VM
       └─ Runs in isolated KVM VM
```

## Key Features

✅ **Strong Isolation** - Each task runs in its own Firecracker microVM
✅ **KVM Acceleration** - Near-native performance
✅ **Own Kernel** - Complete kernel isolation per task
✅ **Compatible** - Works with standard SwarmKit APIs
✅ **Podman Integration** - Pulls standard OCI images

## Troubleshooting

### Check Worker Logs
```bash
vagrant ssh worker1 -c "
sudo journalctl -u swarmd-firecracker -f
"
```

### Check Running Firecracker VMs
```bash
vagrant ssh worker1 -c "
ps aux | grep firecracker
"
```

### List RootFS Images
```bash
vagrant ssh worker1 -c "
sudo ls -lh /var/lib/firecracker/rootfs/
"
```

### View Network Configuration
```bash
vagrant ssh worker1 -c "
ip link show swarm-br0
brctl show
"
```

## Next Steps

1. **Deploy More Services** - Test different workloads
2. **Add More Workers** - `vagrant up worker2`
3. **Monitor Resources** - Check CPU/Memory per microVM
4. **Custom Images** - Build and deploy custom container images

---

**Status:** ✅ Production Ready (Infrastructure)
**Last Updated:** 2026-02-02
