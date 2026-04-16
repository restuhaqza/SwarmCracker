# SwarmCracker — End-to-End Scenario Plan

> A complete walkthrough from bare metal to running production workloads.

---

## Overview

This plan covers the full lifecycle of a SwarmCracker deployment:

```
Bare Metal → Install → Init Cluster → Deploy Services → Scale → Update → Snapshot → Rollback → Monitor → Teardown
```

### Target Topology

```
┌──────────────────────────────────────────────────────────────┐
│                    3-Node Cluster                             │
│                                                               │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────┐        │
│  │  Manager-1   │  │   Worker-1    │  │   Worker-2    │       │
│  │  192.168.1.10│  │  192.168.1.11 │  │  192.168.1.12 │       │
│  │              │  │              │  │              │         │
│  │ SwarmKit API │  │ 3x nginx VMs │  │ 2x nginx VMs │        │
│  │ swarmctl     │  │ 1x redis VM  │  │ 2x redis VMs │        │
│  └──────────────┘  │ swarm-br0    │  │ swarm-br0    │        │
│         │ gRPC     │ VXLAN        │  │ VXLAN        │        │
│         └──────────┴──────┬───────┘────────────────┘        │
└──────────────────────────┴──────────────────────────────────┘
```

---

## Phase 0: Environment Preparation

### 0.1 Hardware Requirements (per node)

| Role | vCPU | RAM | Disk |
|------|------|-----|------|
| Manager | 2+ | 2 GB | 20 GB SSD |
| Worker | 4+ | 8 GB | 40 GB SSD |

### 0.2 OS & Kernel

```bash
# Ubuntu 22.04 LTS (recommended) or Debian 12+
cat /etc/os-release

# Kernel 5.15+ required for Firecracker
uname -r
```

### 0.3 Verify KVM Access

```bash
# Check KVM device exists
ls -la /dev/kvm

# Verify CPU virtualization support
lscpu | grep Virtualization
# Expected: Virtualization: VT-x (Intel) or AMD-V (AMD)

# If KVM not loaded
sudo modprobe kvm_intel   # Intel
# or
sudo modprobe kvm_amd     # AMD

# Make persistent
echo "kvm_intel" | sudo tee /etc/modules-load.d/kvm.conf
```

### 0.4 Network Configuration

```bash
# Each node needs a static or DHCP-reserved IP
# Ensure inter-node connectivity
ping -c 3 192.168.1.11   # from manager
ping -c 3 192.168.1.12   # from manager

# Open required ports
sudo ufw allow 4242/tcp   # SwarmKit gRPC
sudo ufw allow 7946/tcp   # SwarmKit control
sudo ufw allow 7946/udp   # SwarmKit gossip
sudo ufw allow 4789/udp   # VXLAN overlay
```

---

## Phase 1: Installation

### 1.1 One-Line Install (All Nodes)

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/docs/site/install.sh | sudo bash
```

This installs:
- Firecracker v1.15.1 → `/usr/local/bin/firecracker`
- Jailer → `/usr/local/bin/jailer`
- swarmcracker binary → `/usr/local/bin/swarmcracker`
- swarmd-firecracker → `/usr/local/bin/swarmd-firecracker`
- swarmcracker-agent → `/usr/local/bin/swarmcracker-agent`
- swarmctl → `/usr/local/bin/swarmctl`
- Default config → `/etc/swarmcracker/config.yaml`
- Data directory → `/var/lib/swarmkit/`

### 1.2 Build from Source (Alternative)

```bash
git clone https://github.com/restuhaqza/SwarmCracker.git
cd SwarmCracker

# Install build tools
make install-tools

# Build all binaries
make all

# Install to system
sudo cp build/swarmcracker /usr/local/bin/
sudo cp build/swarmd-firecracker /usr/local/bin/
sudo cp build/swarmcracker-agent /usr/local/bin/
```

### 1.3 Verify Installation

```bash
swarmcracker version
# SwarmCracker v0.4.0
# Firecracker v1.15.1
# SwarmKit v2.1.1

swarmcracker --help
```

---

## Phase 2: Cluster Initialization

### 2.1 Initialize Manager Node

```bash
# On manager-1 (192.168.1.10)
sudo swarmcracker init \
  --hostname manager-1 \
  --listen-addr 0.0.0.0:4242
```

Expected output:
```
✓ SwarmKit manager initialized
✓ Control socket: /var/run/swarmkit/swarm.sock
✓ TLS certificates generated
✓ Join tokens saved to /var/lib/swarmkit/join-tokens.txt
✓ Node ID: abc123def456
```

### 2.2 Retrieve Join Tokens

```bash
# Option A: Read saved tokens
sudo cat /var/lib/swarmkit/join-tokens.txt

# Option B: Use swarmctl
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
swarmctl cluster inspect default
```

### 2.3 Join Worker Nodes

```bash
# On worker-1 (192.168.1.11)
sudo swarmcracker join \
  --hostname worker-1 \
  --manager 192.168.1.10:4242 \
  --token SWMTKN-1-<worker-token>

# On worker-2 (192.168.1.12)
sudo swarmcracker join \
  --hostname worker-2 \
  --manager 192.168.1.10:4242 \
  --token SWMTKN-1-<worker-token>
```

### 2.4 Verify Cluster Health

```bash
# List all nodes
swarmctl ls-nodes
# ID            STATUS   HOSTNAME     AVAILABILITY   ROLE
# abc123        READY    manager-1    ACTIVE         MANAGER
# def456        READY    worker-1     ACTIVE         WORKER
# ghi789        READY    worker-2     ACTIVE         WORKER

# Check cluster status
swarmcracker status
```

**✅ Phase 2 Complete: 3-node cluster is operational**

---

## Phase 3: Service Deployment

### 3.1 Deploy Nginx Web Service

```bash
swarmctl create-service nginx:latest
# Service created: svc-nginx-143022
# Image: nginx:latest
```

### 3.2 Scale the Service

```bash
# Scale to 5 replicas across workers
swarmctl scale svc-nginx-143022 5
```

### 3.3 Verify Tasks (MicroVMs)

```bash
swarmctl ls-tasks
# ID          SERVICE     STATUS    NODE        STATE
# task-001    nginx       RUNNING   worker-1    RUNNING
# task-002    nginx       RUNNING   worker-1    RUNNING
# task-003    nginx       RUNNING   worker-1    RUNNING
# task-004    nginx       RUNNING   worker-2    RUNNING
# task-005    nginx       RUNNING   worker-2    RUNNING
```

Each task = 1 Firecracker microVM with its own kernel.

### 3.4 Deploy Redis Backend

```bash
swarmctl create-service redis:7-alpine
swarmctl scale svc-redis-<id> 3
```

### 3.5 Inspect Running VMs

```bash
swarmcracker list
# VM ID         SERVICE     NODE        STATUS     MEMORY    VCPUS
# vm-nginx-001  nginx       worker-1    RUNNING    128MB     1
# vm-nginx-002  nginx       worker-1    RUNNING    128MB     1
# vm-nginx-003  nginx       worker-1    RUNNING    128MB     1
# vm-nginx-004  nginx       worker-2    RUNNING    128MB     1
# vm-redis-001  redis       worker-1    RUNNING    256MB     1
# vm-redis-002  redis       worker-2    RUNNING    256MB     1
```

### 3.6 Test Service Connectivity

```bash
# Get VM IP from bridge
# Each VM gets an IP on swarm-br0 (e.g., 172.17.0.x)
# Verify networking between VMs
```

**✅ Phase 3 Complete: 8 microVMs running (5 nginx + 3 redis)**

---

## Phase 4: Service Updates & Rollbacks

### 4.1 Rolling Update

```bash
# Update nginx image version
swarmctl update svc-nginx-143022 --image nginx:1.25

# SwarmKit performs rolling update:
# 1. Start new VM with nginx:1.25
# 2. Health check passes
# 3. Drain traffic from old VM
# 4. Stop old VM
# 5. Repeat for each replica
```

### 4.2 Update with Environment Variables

```bash
swarmctl update svc-nginx-143022 \
  --env NGINX_PORT=8080 \
  --env WORKER_PROCESSES=auto
```

### 4.3 Monitor Update Progress

```bash
# Watch tasks during update
swarmctl ls-tasks

# Check logs
swarmcracker logs svc-nginx-143022
```

### 4.4 Rollback (via Snapshot — see Phase 5)

If the update causes issues, restore from a pre-update snapshot.

**✅ Phase 4 Complete: Zero-downtime rolling update verified**

---

## Phase 5: Snapshots & Recovery

### 5.1 Create Pre-Update Snapshot

```bash
# Before making changes, snapshot a VM
swarmctl snapshot create svc-nginx-143022 --name pre-update-v1
```

### 5.2 List Snapshots

```bash
swarmctl snapshot list svc-nginx-143022
# NAME               CREATED              SIZE
# pre-update-v1      2026-04-11T13:00Z    128MB
```

### 5.3 Restore Snapshot

```bash
# Something broke? Rollback:
swarmctl snapshot restore svc-nginx-143022 --name pre-update-v1
```

### 5.4 Delete Old Snapshots

```bash
swarmctl snapshot delete svc-nginx-143022 --name old-backup
```

**✅ Phase 5 Complete: Snapshot backup/restore verified**

---

## Phase 6: Node Operations

### 6.1 Drain a Worker (Maintenance Mode)

```bash
# Drain worker-1 for kernel upgrade
swarmctl drain worker-1

# Tasks are rescheduled to worker-2
swarmctl ls-tasks
# All tasks now on worker-2
```

### 6.2 Reactivate Worker

```bash
swarmctl activate worker-1
# Tasks rebalance back to worker-1
```

### 6.3 Promote Worker to Manager

```bash
# For high availability, add a second manager
swarmctl promote worker-2

swarmctl ls-nodes
# worker-2 now shows ROLE: MANAGER
```

### 6.4 Demote Manager

```bash
swarmctl demote worker-2
```

**✅ Phase 6 Complete: Node lifecycle operations verified**

---

## Phase 7: Monitoring & Debugging

### 7.1 Check Cluster Status

```bash
swarmcracker status
```

### 7.2 View Service Logs

```bash
swarmcracker logs svc-nginx-143022

# Follow logs (live)
swarmcracker logs -f svc-nginx-143022
```

### 7.3 Inspect a Specific Task/VM

```bash
swarmctl inspect <task-id>
# Full JSON with VM config, network, resources
```

### 7.4 Check Systemd Service

```bash
# If running as systemd service
sudo systemctl status swarmcracker
sudo journalctl -u swarmcracker -f
```

### 7.5 Network Debugging

```bash
# Check bridge
ip link show swarm-br0

# Check VXLAN
bridge fdb show dev vxlan0

# Check TAP devices for each VM
ip link show tap-*
```

**✅ Phase 7 Complete: Monitoring and debugging workflows verified**

---

## Phase 8: Volume Management

### 8.1 Create a Volume

```bash
swarmcracker volume create app-data --size 1G
```

### 8.2 Attach Volume to Service

```bash
swarmctl update svc-redis-<id> --volume app-data:/data
```

### 8.3 List Volumes

```bash
swarmcracker volume ls
```

**✅ Phase 8 Complete: Persistent storage verified**

---

## Phase 9: Cleanup & Teardown

### 9.1 Remove Services

```bash
swarmctl rm-service svc-nginx-143022
swarmctl rm-service svc-redis-<id>
```

### 9.2 Verify All VMs Stopped

```bash
swarmcracker list
# (empty)
```

### 9.3 Leave Cluster (Workers)

```bash
# On worker-1 and worker-2
sudo swarmcracker leave
```

### 9.4 Teardown Manager

```bash
# On manager-1
sudo swarmcracker leave --force
```

### 9.5 Clean Up

```bash
sudo rm -rf /var/lib/swarmkit/
sudo rm -rf /var/run/swarmkit/
sudo rm -rf /etc/swarmcracker/
```

**✅ Phase 9 Complete: Clean teardown verified**

---

## Validation Checklist

Use this checklist to verify the complete E2E scenario:

### Infrastructure
- [ ] KVM available on all nodes
- [ ] Inter-node network connectivity
- [ ] Required ports open (4242, 7946, 4789)

### Cluster
- [ ] Manager initialized successfully
- [ ] Workers joined successfully
- [ ] All nodes show READY status
- [ ] TLS certificates generated
- [ ] gRPC communication working

### Workloads
- [ ] Service created and running
- [ ] Service scaled to multiple replicas
- [ ] Tasks distributed across workers
- [ ] Each task = 1 running microVM
- [ ] Cross-VM networking functional (VXLAN)

### Lifecycle
- [ ] Rolling update (zero downtime)
- [ ] Environment variable injection
- [ ] Snapshot created and verified
- [ ] Snapshot restore working
- [ ] Service removal and VM cleanup

### Node Operations
- [ ] Worker drain → tasks rescheduled
- [ ] Worker activate → tasks rebalanced
- [ ] Worker promoted to manager
- [ ] Manager demoted to worker

### Monitoring
- [ ] Cluster status command works
- [ ] Service logs accessible
- [ ] Task inspection returns valid JSON
- [ ] Network debug commands functional

### Cleanup
- [ ] All services removed
- [ ] All VMs stopped
- [ ] Workers left cluster
- [ ] Manager torn down
- [ ] Data directories cleaned

---

## Automation Script

The production example includes an automated deploy script:

```bash
cd examples/production-cluster/
./deploy.sh --manager 192.168.1.10 --workers 192.168.1.11,192.168.1.12
```

This automates Phases 1-3. See `examples/production-cluster/README.md` for details.

---

## Local Dev Alternative

For testing without multi-node hardware:

```bash
cd examples/local-dev/
./start.sh
```

Single-node cluster (manager + worker on same machine). See `examples/local-dev/README.md`.

---

## Troubleshooting Quick Reference

| Problem | Check |
|---------|-------|
| KVM not found | `ls /dev/kvm` → `modprobe kvm_intel` |
| Worker won't join | Ping manager, check token, check port 4242 |
| VMs not starting | `journalctl -u swarmcracker -f`, check kernel images |
| No cross-node networking | Check VXLAN: `bridge fdb show dev vxlan0` |
| Service stuck updating | `swarmctl ls-tasks`, check health check config |
| Snapshot fails | Check disk space, VM state must be RUNNING |

---

*This plan covers the complete SwarmCracker lifecycle. Each phase is independently testable.*
