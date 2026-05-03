# End-to-End Testing — The Full Journey

> From bare metal to running production workloads, step by step.

---

## What This Covers

We'll walk through the entire lifecycle of a SwarmCracker deployment:

```
Bare Metal → Install → Set Up Cluster → Deploy Services → Scale → Update → Snapshot → Rollback → Monitor → Clean Up
```

### Target Setup

```
┌──────────────────────────────────────────────────────────────┐
│                    A 3-Node Cluster                          │
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

## Phase 0: Get Your Environment Ready

### What You Need (Per Node)

| Role | vCPU | RAM | Disk |
|------|------|-----|------|
| Manager | 2+ | 2 GB | 20 GB SSD |
| Worker | 4+ | 8 GB | 40 GB SSD |

### OS and Kernel

```bash
# Ubuntu 22.04 LTS (recommended) or Debian 12+
cat /etc/os-release

# You'll need kernel 5.15+ for Firecracker
uname -r
```

### Check KVM Access

```bash
# See if KVM is there
ls -la /dev/kvm

# Verify your CPU supports virtualization
lscpu | grep Virtualization
# You should see: VT-x (Intel) or AMD-V (AMD)

# If KVM isn't loaded yet
sudo modprobe kvm_intel   # Intel
# or
sudo modprobe kvm_amd     # AMD

# Make it persistent across reboots
echo "kvm_intel" | sudo tee /etc/modules-load.d/kvm.conf
```

### Network Setup

```bash
# Each node needs a static or reserved DHCP IP
# Make sure nodes can reach each other
ping -c 3 192.168.1.11   # from manager
ping -c 3 192.168.1.12   # from manager

# Open the ports we need
sudo ufw allow 4242/tcp   # SwarmKit gRPC
sudo ufw allow 7946/tcp   # SwarmKit control
sudo ufw allow 7946/udp   # SwarmKit gossip
sudo ufw allow 4789/udp   # VXLAN overlay
```

---

## Phase 1: Install SwarmCracker

### One-Line Install (On All Nodes)

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/docs/site/install.sh | sudo bash
```

This sets up:
- Firecracker v1.15.1 → `/usr/local/bin/firecracker`
- Jailer → `/usr/local/bin/jailer`
- swarmcracker binary → `/usr/local/bin/swarmcracker`
- swarmd-firecracker → `/usr/local/bin/swarmd-firecracker`
- swarmcracker-agent → `/usr/local/bin/swarmcracker-agent`
- swarmctl → `/usr/local/bin/swarmctl`
- Default config → `/etc/swarmcracker/config.yaml`
- Data directory → `/var/lib/swarmkit/`

### Build From Source (Alternative)

```bash
git clone https://github.com/restuhaqza/SwarmCracker.git
cd SwarmCracker

# Install build tools
make install-tools

# Build everything
make all

# Install to system
sudo cp build/swarmcracker /usr/local/bin/
sudo cp build/swarmd-firecracker /usr/local/bin/
sudo cp build/swarmcracker-agent /usr/local/bin/
```

### Make Sure It Works

```bash
swarmcracker version
# SwarmCracker v0.6.0
# Firecracker v1.15.1
# SwarmKit v2.1.1

swarmcracker --help
```

---

## Phase 2: Get Your Cluster Running

### Start the Manager

```bash
# On manager-1 (192.168.1.10)
sudo swarmcracker init \
  --hostname manager-1 \
  --listen-addr 0.0.0.0:4242
```

You should see:
```
✓ SwarmKit manager initialized
✓ Control socket: /var/run/swarmkit/swarm.sock
✓ TLS certificates generated
✓ Join tokens saved to /var/lib/swarmkit/join-tokens.txt
✓ Node ID: abc123def456
```

### Get Your Join Tokens

```bash
# Option A: Read the saved tokens
sudo cat /var/lib/swarmkit/join-tokens.txt

# Option B: Use swarmctl
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
swarmctl cluster inspect default
```

### Add Workers to the Cluster

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

### Check That Everything's Healthy

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

**✅ Phase 2 done: You've got a working 3-node cluster**

---

## Phase 3: Deploy Some Services

### Launch an Nginx Web Service

```bash
swarmctl create-service nginx:latest
# Service created: svc-nginx-143022
# Image: nginx:latest
```

### Scale It Up

```bash
# Scale to 5 replicas across workers
swarmctl scale svc-nginx-143022 5
```

### See Your Tasks (MicroVMs)

```bash
swarmctl ls-tasks
# ID          SERVICE     STATUS    NODE        STATE
# task-001    nginx       RUNNING   worker-1    RUNNING
# task-002    nginx       RUNNING   worker-1    RUNNING
# task-003    nginx       RUNNING   worker-1    RUNNING
# task-004    nginx       RUNNING   worker-2    RUNNING
# task-005    nginx       RUNNING   worker-2    RUNNING
```

Each task is its own Firecracker microVM with its own kernel.

### Add a Redis Backend

```bash
swarmctl create-service redis:7-alpine
swarmctl scale svc-redis-<id> 3
```

### Inspect Your Running VMs

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

### Test Connectivity

```bash
# Get the VM IP from the bridge
# Each VM gets an IP on swarm-br0 (like 172.17.0.x)
# Try pinging between VMs to verify networking
```

**✅ Phase 3 done: 8 microVMs running (5 nginx + 3 redis)**

---

## Phase 4: Update Services and Roll Back

### Rolling Update

```bash
# Update to a new nginx version
swarmctl update svc-nginx-143022 --image nginx:1.25

# SwarmKit does a rolling update:
# 1. Start new VM with nginx:1.25
# 2. Run health checks
# 3. Shift traffic away from old VM
# 4. Stop old VM
# 5. Repeat for each replica
```

### Update With Environment Variables

```bash
swarmctl update svc-nginx-143022 \
  --env NGINX_PORT=8080 \
  --env WORKER_PROCESSES=auto
```

### Watch the Update

```bash
# Watch tasks during the update
swarmctl ls-tasks

# Check logs
swarmcracker logs svc-nginx-143022
```

### Rollback (Using Snapshots — See Phase 5)

If something goes wrong, restore from a snapshot you made before the update.

**✅ Phase 4 done: Zero-downtime rolling update works**

---

## Phase 5: Snapshots and Recovery

### Make a Pre-Update Snapshot

```bash
# Before changing things, snapshot a VM
swarmctl snapshot create svc-nginx-143022 --name pre-update-v1
```

### List Your Snapshots

```bash
swarmctl snapshot list svc-nginx-143022
# NAME               CREATED              SIZE
# pre-update-v1      2026-04-11T13:00Z    128MB
```

### Restore a Snapshot

```bash
# Something broke? Roll back:
swarmctl snapshot restore svc-nginx-143022 --name pre-update-v1
```

### Delete Old Snapshots

```bash
swarmctl snapshot delete svc-nginx-143022 --name old-backup
```

**✅ Phase 5 done: Snapshots let you undo mistakes**

---

## Phase 6: Manage Nodes

### Drain a Worker (Maintenance Mode)

```bash
# Drain worker-1 for a kernel upgrade
swarmctl drain worker-1

# Tasks get rescheduled to worker-2
swarmctl ls-tasks
# All tasks now on worker-2
```

### Bring the Worker Back

```bash
swarmctl activate worker-1
# Tasks rebalance back to worker-1
```

### Promote a Worker to Manager

```bash
# Add a second manager for high availability
swarmctl promote worker-2

swarmctl ls-nodes
# worker-2 now shows ROLE: MANAGER
```

### Demote a Manager

```bash
swarmctl demote worker-2
```

**✅ Phase 6 done: Node lifecycle operations work**

---

## Phase 7: Monitor and Debug

### Check Cluster Status

```bash
swarmcracker status
```

### View Service Logs

```bash
swarmcracker logs svc-nginx-143022

# Follow logs in real-time
swarmcracker logs -f svc-nginx-143022
```

### Inspect a Specific Task/VM

```bash
swarmctl inspect <task-id>
# Full JSON with VM config, network, resources
```

### Check the Systemd Service

```bash
# If running as a systemd service
sudo systemctl status swarmcracker
sudo journalctl -u swarmcracker -f
```

### Debug Networking

```bash
# Check the bridge
ip link show swarm-br0

# Check VXLAN
bridge fdb show dev vxlan0

# Check TAP devices for each VM
ip link show tap-*
```

**✅ Phase 7 done: You can see what's going on**

---

## Phase 8: Manage Storage

### Create a Volume

```bash
swarmcracker volume create app-data --size 1G
```

### Attach Volume to a Service

```bash
swarmctl update svc-redis-<id> --volume app-data:/data
```

### List Volumes

```bash
swarmcracker volume ls
```

**✅ Phase 8 done: Persistent storage works**

---

## Phase 9: Clean Up

### Remove Services

```bash
swarmctl rm-service svc-nginx-143022
swarmctl rm-service svc-redis-<id>
```

### Verify All VMs Stopped

```bash
swarmcracker list
# (empty)
```

### Leave the Cluster (Workers)

```bash
# On worker-1 and worker-2
sudo swarmcracker leave
```

### Tear Down the Manager

```bash
# On manager-1
sudo swarmcracker leave --force
```

### Clean Up

```bash
sudo rm -rf /var/lib/swarmkit/
sudo rm -rf /var/run/swarmkit/
sudo rm -rf /etc/swarmcracker/
```

**✅ Phase 9 done: Clean teardown verified**

---

## Validation Checklist

Use this to make sure everything works:

### Infrastructure
- [ ] KVM available on all nodes
- [ ] Nodes can reach each other
- [ ] Required ports open (4242, 7946, 4789)

### Cluster
- [ ] Manager initialized
- [ ] Workers joined
- [ ] All nodes show READY
- [ ] TLS certificates generated
- [ ] gRPC communication works

### Workloads
- [ ] Service created and running
- [ ] Service scaled to multiple replicas
- [ ] Tasks spread across workers
- [ ] Each task = 1 running microVM
- [ ] Cross-VM networking works (VXLAN)

### Lifecycle
- [ ] Rolling update (zero downtime)
- [ ] Environment variables injected
- [ ] Snapshot created and verified
- [ ] Snapshot restore works
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
- [ ] Network debug commands work

### Cleanup
- [ ] All services removed
- [ ] All VMs stopped
- [ ] Workers left cluster
- [ ] Manager torn down
- [ ] Data directories cleaned

---

## Automate It

The production example has a deploy script that automates phases 1-3:

```bash
cd examples/production-cluster/
./deploy.sh --manager 192.168.1.10 --workers 192.168.1.11,192.168.1.12
```

See `examples/production-cluster/README.md` for details.

---

## Local Development Alternative

If you want to test without multi-node hardware:

```bash
cd examples/local-dev/
./start.sh
```

This runs a single-node cluster (manager + worker on the same machine). See `examples/local-dev/README.md`.

---

## Quick Troubleshooting

| Problem | Check This |
|---------|------------|
| KVM not found | `ls /dev/kvm` → `modprobe kvm_intel` |
| Worker won't join | Ping manager, check token, check port 4242 |
| VMs not starting | `journalctl -u swarmcracker -f`, check kernel images |
| No cross-node networking | Check VXLAN: `bridge fdb show dev vxlan0` |
| Service stuck updating | `swarmctl ls-tasks`, check health check config |
| Snapshot fails | Check disk space, VM must be RUNNING |

---

*This covers the complete SwarmCracker lifecycle. Each phase can be tested independently.*
