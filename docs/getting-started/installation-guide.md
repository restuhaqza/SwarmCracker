# 🔥 SwarmCracker — Full Installation Guide

This guide walks you through setting up a complete SwarmCracker cluster from scratch — manager, workers, Firecracker microVMs, and cross-node networking.

---

## 📋 Table of Contents

1. [Prerequisites](#1-prerequisites)
2. [Architecture Overview](#2-architecture-overview)
3. [Method 1: One-Line Install (Recommended)](#3-method-1-one-line-install-recommended)
4. [Method 2: Vagrant Test Cluster](#4-method-2-vagrant-test-cluster)
5. [Method 3: Manual Install](#5-method-3-manual-install)
6. [Cross-Node Networking (VXLAN)](#6-cross-node-networking-vxlan)
7. [Deploying Services](#7-deploying-services)
8. [Verifying Your Cluster](#8-verifying-your-cluster)
9. [Troubleshooting](#9-troubleshooting)
10. [Clean Up / Uninstall](#10-clean-up--uninstall)

---

## 1. Prerequisites

### Hardware Requirements

| Node Role | Minimum | Recommended |
|-----------|---------|-------------|
| Manager   | 1 vCPU, 1 GB RAM, 10 GB disk | 2 vCPU, 2 GB RAM |
| Worker    | 2 vCPU, 4 GB RAM, 20 GB disk | 4 vCPU, 8 GB RAM |

> 💡 Each microVM uses ~128 MB RAM + 1 vCPU by default. Plan accordingly.

### Software Requirements

- **Linux** — Ubuntu 20.04+, Debian 11+, or any distro with KVM support
- **KVM** — Hardware virtualization (`/dev/kvm` must exist)
- **Root access** — For bridge, firewall, and Firecracker setup
- **Internet** — To download binaries and container images

### Verify KVM Support

```bash
# Check KVM device exists
ls -la /dev/kvm

# Check CPU virtualization support
lscpu | grep -i virtualization
# Should show: Virtualization: VT-x (Intel) or AMD-V (AMD)

# If /dev/kvm doesn't exist but CPU supports it:
sudo modprobe kvm_intel    # Intel
# or
sudo modprobe kvm_amd      # AMD
```

### Install Required Packages

```bash
# Debian/Ubuntu
sudo apt-get update
sudo apt-get install -y curl wget tar git iptables iproute2 bridge-utils \
  dnsmasq kmod ca-certificates

# RHEL/CentOS/Fedora
sudo dnf install -y curl wget tar git iptables iproute bridge-utils \
  dnsmasq kmod ca-certificates
```

---

## 2. Architecture Overview

```
┌──────────────────────────────────────────────────────────────┐
│                     SwarmCracker Cluster                      │
│                                                               │
│  ┌─────────────────┐                                         │
│  │  Manager Node    │  SwarmKit control plane                 │
│  │  swarmd          │  - Schedules tasks                      │
│  │  Port 4242       │  - Manages cluster state                │
│  └────────┬─────────┘                                         │
│           │ gRPC                                              │
│     ┌─────┴──────────────────┐                               │
│     │                        │                                │
│  ┌──▼──────────────┐  ┌──────▼──────────┐                   │
│  │  Worker-1        │  │  Worker-2        │                  │
│  │  swarmd-firecracker│ │  swarmd-firecracker│                │
│  │  swarm-br0        │  │  swarm-br0        │                │
│  │  192.168.128.1/24 │  │  192.168.129.1/24 │                │
│  │  ┌──────┐┌──────┐│  │  ┌──────┐┌──────┐│                │
│  │  │VM 1  ││VM 2  ││  │  │VM 3  ││VM 4  ││                │
│  │  │nginx ││redis ││  │  │app   ││db    ││                │
│  │  └──────┘└──────┘│  │  └──────┘└──────┘│                │
│  └───────┬──────────┘  └───────┬──────────┘                │
│          └──── VXLAN Overlay (UDP 4789) ─────┘              │
│                                                               │
└──────────────────────────────────────────────────────────────┘
```

### Key Components

| Component | Description |
|-----------|-------------|
| `swarmd` | SwarmKit manager daemon |
| `swarmd-firecracker` | Custom SwarmKit executor that runs tasks as Firecracker microVMs |
| `swarmctl` | SwarmKit CLI tool |
| `swarm-br0` | Linux bridge for VM networking |
| `vxlan100` | VXLAN tunnel for cross-node VM communication |
| `dnsmasq` | DHCP server for VM IP assignment |
| `firecracker` | MicroVM hypervisor (KVM-based) |

### Network Layout

| Network | Subnet | Purpose |
|---------|--------|---------|
| Host network | `192.168.56.0/24` | Inter-node communication |
| Worker-1 VMs | `192.168.128.0/24` | MicroVMs on worker-1 |
| Worker-2 VMs | `192.168.129.0/24` | MicroVMs on worker-2 |
| VXLAN overlay | Tunnels between workers | Cross-node VM traffic |

> ⚠️ Each worker **must** use a **unique** VM subnet to avoid IP conflicts.

---

## 3. Method 1: One-Line Install (Recommended)

### Install on Manager

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash
```

Select **Option 1** (Manager) when prompted. The script will:

1. ✅ Download and install `swarmcracker`, `swarmd-firecracker`, `swarmcracker-agent`
2. ✅ Build and install SwarmKit tools (`swarmd`, `swarmctl`)
3. ✅ Start the SwarmKit manager on port 4242
4. ✅ Print the join command with token

The output will look like:

```
✅ Manager Ready

  Manager Info
  ─────────────────────────────────────
  Hostname:    manager
  API:         0.0.0.0:4242
  State dir:   /var/lib/swarmcracker/manager
  Socket:      /var/lib/swarmcracker/manager/swarm.sock
  IP:          192.168.56.10

  To add workers, run this on each worker node:
  curl -fsSL ... | bash -s -- --worker \
    --manager 192.168.56.10:4242 \
    --token SWMTKN-1-xxxxx...
```

### Install on Worker-1

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash -s -- \
  --worker \
  --manager 192.168.56.10:4242 \
  --token SWMTKN-1-xxxxx... \
  --hostname swarm-worker-1 \
  --subnet 192.168.128.0/24 \
  --bridge-ip 192.168.128.1/24
```

### Install on Worker-2

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash -s -- \
  --worker \
  --manager 192.168.56.10:4242 \
  --token SWMTKN-1-xxxxx... \
  --hostname swarm-worker-2 \
  --subnet 192.168.129.0/24 \
  --bridge-ip 192.168.129.1/24
```

> ⚠️ **Important:** Each worker must have a unique `--subnet` and `--bridge-ip`. Increment the third octet for each new worker.

### All CLI Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--worker` | No | — | Set up as worker node |
| `--manager` | Yes (with `--worker`) | — | Manager address (`IP:PORT`) |
| `--token` | Yes (with `--worker`) | — | SwarmKit join token |
| `--hostname` | No | System hostname | Node hostname |
| `--bridge` | No | `swarm-br0` | Bridge interface name |
| `--subnet` | No | `192.168.127.0/24` | VM network CIDR |
| `--bridge-ip` | No | `192.168.127.1/24` | Bridge IP (VM gateway) |
| `--state-dir` | No | `/var/lib/swarmcracker/worker` | State directory |
| `--kernel-path` | No | `/usr/share/firecracker/vmlinux` | Firecracker kernel |
| `--rootfs-dir` | No | `/var/lib/firecracker/rootfs` | Rootfs storage |
| `--install-dir` | No | `/usr/local/bin` | Binary install directory |

---

## 4. Method 2: Vagrant Test Cluster

Use this for **local testing** with VirtualBox VMs.

### Prerequisites

```bash
# Install VirtualBox and Vagrant
sudo apt-get install -y virtualbox vagrant

# Go to test directory
cd test-automation
```

### Start the Cluster

```bash
# Start all 3 nodes (manager + 2 workers)
vagrant up

# This takes ~5-10 minutes on first run
# It automatically:
#   - Installs Go, Podman, Firecracker
#   - Builds SwarmCracker from source
#   - Configures the SwarmKit cluster
#   - Starts swarmd-firecracker on workers
#   - Sets up VXLAN overlay networking
```

### Verify

```bash
# Check cluster nodes
vagrant ssh manager -c "
  sudo swarmctl --socket /var/lib/swarmcracker/manager/swarm.sock node ls
"

# Deploy test service
vagrant ssh manager -c "
  sudo swarmctl --socket /var/lib/swarmcracker/manager/swarm.sock service create \
    --name test-service --image alpine:latest --replicas 3
"

# Check running microVMs
vagrant ssh worker1 -c "pgrep -a firecracker"
vagrant ssh worker2 -c "pgrep -a firecracker"
```

### Common Commands

```bash
vagrant up              # Start all nodes
vagrant up worker2      # Start specific node
vagrant ssh worker1     # SSH into a node
vagrant status          # Check status
vagrant halt            # Stop all (keeps data)
vagrant destroy -f      # Delete all VMs
```

### VM Configuration

| VM | IP | RAM | CPUs | Role |
|----|----|-----|------|------|
| manager | 192.168.56.10 | 2 GB | 2 | SwarmKit manager |
| worker1 | 192.168.56.11 | 4 GB | 4 | SwarmCracker worker |
| worker2 | 192.168.56.12 | 4 GB | 4 | SwarmCracker worker |

---

## 5. Method 3: Manual Install

For environments where you need full control over every step.

### Step 1: Install SwarmKit Tools (All Nodes)

```bash
# Install Go (if not present)
sudo apt-get install -y golang-go
# or download from https://go.dev/dl/

# Build swarmd and swarmctl
export PATH=$PATH:/usr/local/go/bin
cd /tmp
git clone --depth 1 https://github.com/moby/swarmkit.git
cd swarmkit/swarmd

# swarmd (daemon)
go build -o /usr/local/bin/swarmd ./cmd/swarmd

# swarmctl (CLI)
go build -o /usr/local/bin/swarmctl ./cmd/swarmctl

cd / && rm -rf /tmp/swarmkit
```

### Step 2: Install Firecracker (Workers Only)

```bash
# Detect architecture
ARCH=$(uname -m)
[ "$ARCH" = "x86_64" ] && FC_ARCH="x86_64"
[ "$ARCH" = "aarch64" ] && FC_ARCH="aarch64"
FC_VER="v1.15.0"

# Download Firecracker
sudo mkdir -p /usr/local/bin /usr/share/firecracker
curl -fsSL "https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VER}/firecracker-${FC_VER}-${FC_ARCH}.tgz" \
  | sudo tar xz -C /tmp
sudo cp /tmp/release-${FC_VER}-${FC_ARCH}/firecracker-${FC_VER}-${FC_ARCH} /usr/local/bin/firecracker
sudo chmod +x /usr/local/bin/firecracker

# Download kernel
curl -fsSL "https://s3.amazonaws.com/spec.ccfc.min/ci-artifacts/kernels/x86_64/vmlinux-5.10.217" \
  -o /tmp/vmlinux
sudo cp /tmp/vmlinux /usr/share/firecracker/vmlinux

# Create rootfs directory
sudo mkdir -p /var/lib/firecracker/rootfs

# Verify
firecracker --version
ls -lh /usr/share/firecracker/vmlinux
```

### Step 3: Install SwarmCracker (Workers Only)

```bash
# Option A: Download pre-built release
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash -s -- --skip-setup
# Then copy binaries manually

# Option B: Build from source
cd /tmp
git clone https://github.com/restuhaqza/SwarmCracker.git
cd SwarmCracker

# Build CLI
go build -o /usr/local/bin/swarmcracker ./cmd/swarmcracker/

# Build swarmd-firecracker (the custom SwarmKit executor)
go build -o /usr/local/bin/swarmd-firecracker ./cmd/swarmd-firecracker/

# Build agent (optional)
go build -o /usr/local/bin/swarmcracker-agent ./cmd/swarmcracker-agent/
```

### Step 4: Start Manager

```bash
sudo mkdir -p /var/lib/swarmcracker/manager

sudo swarmd \
  -d /var/lib/swarmcracker/manager \
  --hostname swarm-manager \
  --listen-control-api /var/lib/swarmcracker/manager/swarm.sock \
  --listen-remote-api 0.0.0.0:4242 \
  > /tmp/swarmcracker-manager.log 2>&1 &

# Wait for it to start
sleep 5

# Get join token
sudo swarmctl --socket /var/lib/swarmcracker/manager/swarm.sock cluster inspect default \
  | grep -oP 'SWMTKN-[0-9a-zA-Z-]+' | head -1
```

### Step 5: Start Workers

On **each worker node**, run:

```bash
# Enable IP forwarding
sudo sysctl -w net.ipv4.ip_forward=1

# Load kernel modules
sudo modprobe br_netfilter
sudo modprobe vxlan

sudo mkdir -p /var/lib/swarmcracker/worker

# REPLACE these values for your environment:
# - MANAGER_IP: your manager's IP
# - JOIN_TOKEN: from Step 4
# - HOSTNAME: unique per worker
# - SUBNET: unique per worker (increment third octet)
# - BRIDGE_IP: first IP in your subnet

sudo swarmd-firecracker \
  --state-dir /var/lib/swarmcracker/worker \
  --hostname swarm-worker-1 \
  --join-addr 192.168.56.10:4242 \
  --join-token SWMTKN-1-xxxxx... \
  --listen-remote-api 0.0.0.0:4243 \
  --kernel-path /usr/share/firecracker/vmlinux \
  --rootfs-dir /var/lib/firecracker/rootfs \
  --bridge-name swarm-br0 \
  --subnet 192.168.128.0/24 \
  --bridge-ip 192.168.128.1/24 \
  --nat-enabled \
  > /tmp/swarmcracker-worker.log 2>&1 &

# Verify
sleep 5
pgrep -a swarmd-firecracker
```

**Worker-2 example** (note different subnet):

```bash
sudo swarmd-firecracker \
  --state-dir /var/lib/swarmcracker/worker \
  --hostname swarm-worker-2 \
  --join-addr 192.168.56.10:4242 \
  --join-token SWMTKN-1-xxxxx... \
  --listen-remote-api 0.0.0.0:4243 \
  --kernel-path /usr/share/firecracker/vmlinux \
  --rootfs-dir /var/lib/firecracker/rootfs \
  --bridge-name swarm-br0 \
  --subnet 192.168.129.0/24 \
  --bridge-ip 192.168.129.1/24 \
  --nat-enabled \
  > /tmp/swarmcracker-worker.log 2>&1 &
```

### Step 6: Verify Cluster

```bash
# From manager
sudo swarmctl --socket /var/lib/swarmcracker/manager/swarm.sock node ls

# Expected output:
# ID    Name             Status  Availability  Manager Status
# xxx   swarm-worker-1   READY   ACTIVE
# yyy   swarm-worker-2   READY   ACTIVE
```

---

## 6. Cross-Node Networking (VXLAN)

For microVMs on different workers to communicate, you need a VXLAN overlay. This is **required** for multi-worker clusters.

### What VXLAN Does

```
Worker-1 VM (192.168.128.x)  ←─── VXLAN tunnel ───→  Worker-2 VM (192.168.129.x)
     ↑                                                            ↑
  swarm-br0                                                 swarm-br0
  192.168.128.1                                              192.168.129.1
     ↑                                                            ↑
  enp0s8 (192.168.56.11)  ←─── physical network ──→  enp0s8 (192.168.56.12)
```

### Manual VXLAN Setup

**On Worker-1** (192.168.56.11):

```bash
# Create VXLAN interface
sudo ip link add vxlan100 type vxlan \
  id 100 \
  local 192.168.56.11 \
  remote 192.168.56.12 \
  dev enp0s8 \
  dstport 4789

# Bring up and attach to bridge
sudo ip link set vxlan100 up
sudo ip link set vxlan100 master swarm-br0

# Add route to Worker-2's VM subnet
sudo ip route add 192.168.129.0/24 dev swarm-br0 scope link

# NAT for cross-worker traffic
sudo iptables -t nat -A POSTROUTING \
  -s 192.168.129.0/24 -d 192.168.128.0/24 \
  -j MASQUERADE
```

**On Worker-2** (192.168.56.12):

```bash
# Create VXLAN interface (pointing back to Worker-1)
sudo ip link add vxlan100 type vxlan \
  id 100 \
  local 192.168.56.12 \
  remote 192.168.56.11 \
  dev enp0s8 \
  dstport 4789

# Bring up and attach to bridge
sudo ip link set vxlan100 up
sudo ip link set vxlan100 master swarm-br0

# Add route to Worker-1's VM subnet
sudo ip route add 192.168.128.0/24 dev swarm-br0 scope link

# NAT for cross-worker traffic
sudo iptables -t nat -A POSTROUTING \
  -s 192.168.128.0/24 -d 192.168.129.0/24 \
  -j MASQUERADE
```

### Verify VXLAN

```bash
# Check VXLAN interface exists
ip link show vxlan100

# Check bridge has VXLAN attached
bridge link | grep vxlan100

# Test Worker-1 → Worker-2 bridge
ping -c 2 192.168.129.1
# Expected: 64 bytes, ~5-15ms latency

# Test cross-worker VM-to-VM (after deploying services)
ping -c 2 <worker2-vm-ip>
```

### Adding More Workers

For a 3rd worker (192.168.56.13, subnet 192.168.130.0/24):

```bash
# On the new Worker-3:
sudo ip link add vxlan100 type vxlan \
  id 100 local 192.168.56.13 \
  dev enp0s8 dstport 4789

# Add FDB entries for all existing workers
sudo bridge fdb append 00:00:00:00:00:00 dst 192.168.56.11 dev vxlan100
sudo bridge fdb append 00:00:00:00:00:00 dst 192.168.56.12 dev vxlan100

sudo ip link set vxlan100 up
sudo ip link set vxlan100 master swarm-br0

# Routes to all other workers' VM subnets
sudo ip route add 192.168.128.0/24 dev swarm-br0 scope link
sudo ip route add 192.168.129.0/24 dev swarm-br0 scope link

# NAT rules
sudo iptables -t nat -A POSTROUTING -s 192.168.128.0/24 -d 192.168.130.0/24 -j MASQUERADE
sudo iptables -t nat -A POSTROUTING -s 192.168.129.0/24 -d 192.168.130.0/24 -j MASQUERADE

# On each existing worker, add Worker-3:
sudo bridge fdb append 00:00:00:00:00:00 dst 192.168.56.13 dev vxlan100
sudo ip route add 192.168.130.0/24 dev swarm-br0 scope link
sudo iptables -t nat -A POSTROUTING -s 192.168.130.0/24 -d <local-vm-subnet> -j MASQUERADE
```

---

## 7. Deploying Services

### Create a Service

```bash
SWARM_SOCK=/var/lib/swarmcracker/manager/swarm.sock

# Simple alpine service
sudo swarmctl --socket $SWARM_SOCK service create \
  --name web \
  --image alpine:latest \
  --replicas 3

# Nginx service
sudo swarmctl --socket $SWARM_SOCK service create \
  --name nginx \
  --image nginx:alpine \
  --replicas 2

# Redis (1 replica)
sudo swarmctl --socket $SWARM_SOCK service create \
  --name redis \
  --image redis:alpine \
  --replicas 1
```

### Manage Services

```bash
# List services
sudo swarmctl --socket $SWARM_SOCK service ls

# List tasks (microVMs)
sudo swarmctl --socket $SWARM_SOCK task ls

# Scale a service
sudo swarmctl --socket $SWARM_SOCK service update web --replicas 5

# Remove a service
sudo swarmctl --socket $SWARM_SOCK service remove web
```

### Check MicroVMs

```bash
# List running Firecracker VMs on a worker
ps aux | grep firecracker

# Check bridge and TAP devices
ip link show swarm-br0
ip link show | grep tap

# Check dnsmasq DHCP leases
cat /var/lib/misc/dnsmasq.leases 2>/dev/null

# Check worker logs
tail -f /tmp/swarmcracker-worker.log
```

---

## 8. Verifying Your Cluster

Run these checks after setup:

```bash
SWARM_SOCK=/var/lib/swarmcracker/manager/swarm.sock

# 1. Check all nodes are READY
sudo swarmctl --socket $SWARM_SOCK node ls
# All nodes should show: Status=READY, Availability=ACTIVE

# 2. Deploy test service
sudo swarmctl --socket $SWARM_SOCK service create \
  --name smoke-test --image alpine:latest --replicas 2

# 3. Wait for tasks to start (10-30 seconds)
sleep 20
sudo swarmctl --socket $SWARM_SOCK task ls
# All tasks should show: Desired State=RUNNING, Last State=RUNNING

# 4. Verify microVMs are running (on each worker)
ps aux | grep firecracker | grep -v grep

# 5. Test VM networking
# Find a VM IP from worker logs:
grep "ip=" /tmp/swarmcracker-worker.log | tail -1 | grep -oP 'ip=\K[0-9.]+'
# Then ping it:
ping -c 2 <vm-ip>

# 6. Test cross-worker connectivity (if using VXLAN)
# From worker-1, ping a VM on worker-2:
ping -c 2 <worker2-vm-ip>
# Expected: 0% packet loss, ~40-70ms latency
```

### Health Checklist

| Check | Command | Expected |
|-------|---------|----------|
| Manager running | `pgrep swarmd` | PID shown |
| Workers connected | `swarmctl node ls` | All READY |
| Firecracker installed | `firecracker --version` | v1.14+ |
| Bridge exists | `ip link show swarm-br0` | state UP |
| VMs running | `pgrep firecracker` | PIDs shown |
| VMs reachable | `ping <vm-ip>` | 0% loss |
| Cross-worker works | `ping <remote-vm-ip>` | 0% loss |

---

## 9. Troubleshooting

### Worker Won't Join Manager

```bash
# Check manager is reachable
curl http://<MANAGER_IP>:4242

# Check join token is valid
# Re-get token from manager:
sudo swarmctl --socket /var/lib/swarmcracker/manager/swarm.sock cluster inspect default

# Check firewall isn't blocking port 4242
sudo iptables -L -n | grep 4242
# If blocked:
sudo iptables -I INPUT -p tcp --dport 4242 -j ACCEPT
```

### Tasks Stuck in PENDING

```bash
# Check worker logs
tail -50 /tmp/swarmcracker-worker.log

# Common causes:
# 1. Firecracker not installed → install.sh handles this
# 2. /dev/kvm missing → check KVM support
# 3. Rootfs not created → check disk space
# 4. Bridge not up → ip link show swarm-br0

# Check disk space
df -h /var/lib/firecracker/

# Check KVM
ls -la /dev/kvm
```

### Firecracker VMs Not Starting

```bash
# Check Firecracker is installed
which firecracker
firecracker --version

# Check kernel exists
ls -lh /usr/share/firecracker/vmlinux
# Should be ~12 MB+

# Check worker logs for errors
tail -100 /tmp/swarmcracker-worker.log | grep -i error

# Common issue: Firecracker arch mismatch
# Ensure you downloaded the correct arch (x86_64 vs aarch64)
```

### VMs Not Networking

```bash
# Check bridge is UP
ip link show swarm-br0

# Check dnsmasq is running
pgrep dnsmasq
# If not running, check worker logs for DHCP setup errors

# Check TAP devices are attached to bridge
bridge link | grep tap

# Check IP forwarding is enabled
sysctl net.ipv4.ip_forward
# Should be 1

# Check NAT rules
sudo iptables -t nat -L POSTROUTING -n
# Should have MASQUERADE rules for your VM subnet

# Check VM boot args for correct gateway
grep "boot_args" /tmp/swarmcracker-worker.log | tail -1
# Should show: ip=<vm-ip>::<bridge-ip>:255.255.255.0
# If gateway is wrong (e.g., 192.168.127.1 instead of your bridge IP),
# update to the latest SwarmCracker version
```

### Cross-Worker VXLAN Not Working

```bash
# Check VXLAN interface exists
ip link show vxlan100

# Check workers can reach each other (physical network)
ping -c 1 <other-worker-ip>

# Check FDB entries
bridge fdb show dev vxlan100 | grep dst

# Check routes
ip route | grep 192.168
# Should show routes to other workers' VM subnets

# Check NAT rules for cross-subnet traffic
sudo iptables -t nat -L POSTROUTING -n | grep 192.168

# Re-create VXLAN if needed
sudo ip link delete vxlan100 2>/dev/null
# Then re-run the VXLAN setup commands from Section 6
```

### High Memory Usage

```bash
# Check how many microVMs are running
pgrep -c firecracker

# Check memory per VM (~128 MB default)
free -h

# Reduce VM memory (edit SwarmCracker config or use service constraints)
# Each VM uses: vcpus * 1 thread + memory_mb RAM

# Kill stale VMs if needed
sudo pkill firecracker
# Workers will recreate them from SwarmKit tasks
```

### Permission Denied Errors

```bash
# Firecracker needs access to /dev/kvm
ls -la /dev/kvm
# Should be: crw-rw---- 1 root kvm

# Add user to kvm group
sudo usermod -aG kvm $USER
# Log out and back in

# swarmd-firecracker needs root for bridge/tap/firecracker
# Run with sudo or as root
```

---

## 10. Clean Up / Uninstall

### Stop All Services

```bash
# Remove all services from cluster
SWARM_SOCK=/var/lib/swarmcracker/manager/swarm.sock
sudo swarmctl --socket $SWARM_SOCK service ls -q | xargs -I {} sudo swarmctl --socket $SWARM_SOCK service remove {}

# Stop workers
sudo pkill swarmd-firecracker

# Stop manager
sudo pkill swarmd

# Stop all Firecracker VMs
sudo pkill firecracker
```

### Remove Network Configuration

```bash
# Delete VXLAN
sudo ip link delete vxlan100 2>/dev/null

# Delete bridge
sudo ip link delete swarm-br0 2>/dev/null

# Remove NAT rules
sudo iptables -t nat -F POSTROUTING

# Remove routes
sudo ip route flush table main
# (careful: this removes all routes!)
```

### Remove Binaries

```bash
sudo rm -f /usr/local/bin/swarmcracker
sudo rm -f /usr/local/bin/swarmd-firecracker
sudo rm -f /usr/local/bin/swarmcracker-agent
sudo rm -f /usr/local/bin/swarmd
sudo rm -f /usr/local/bin/swarmctl
sudo rm -f /usr/local/bin/firecracker
```

### Remove Data

```bash
# Remove state directories
sudo rm -rf /var/lib/swarmcracker
sudo rm -rf /var/lib/swarmkit

# Remove Firecracker data
sudo rm -rf /var/lib/firecracker
sudo rm -rf /usr/share/firecracker

# Remove config
sudo rm -rf /etc/swarmcracker

# Remove logs
rm -f /tmp/swarmcracker-*.log
```

### Full Reset (Nuclear Option)

```bash
# ⚠️ This removes EVERYTHING related to SwarmCracker

# Stop everything
sudo pkill -9 firecracker 2>/dev/null
sudo pkill -9 swarmd 2>/dev/null
sudo pkill -9 dnsmasq 2>/dev/null

# Clean network
sudo ip link delete vxlan100 2>/dev/null
sudo ip link delete swarm-br0 2>/dev/null
sudo iptables -t nat -F
sudo iptables -F

# Clean files
sudo rm -rf /var/lib/swarmcracker /var/lib/swarmkit /var/lib/firecracker
sudo rm -rf /usr/share/firecracker /etc/swarmcracker
sudo rm -f /usr/local/bin/swarm{cracker,d,-firecracker} /usr/local/bin/swarmctl /usr/local/bin/firecracker
rm -f /tmp/swarmcracker-*.log

echo "✅ SwarmCracker fully removed"
```

### Vagrant Clean Up

```bash
cd test-automation
vagrant destroy -f
# Removes all VMs and their data
```

---

## Quick Reference

### One-Line Commands

```bash
# Install as manager
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash

# Install as worker (non-interactive)
curl -fsSL ... | bash -s -- --worker --manager <IP>:4242 --token <TOKEN>

# Check cluster
swarmctl --socket /var/lib/swarmcracker/manager/swarm.sock node ls

# Deploy service
swarmctl --socket /var/lib/swarmcracker/manager/swarm.sock service create --name web --image nginx:alpine --replicas 3

# Check tasks
swarmctl --socket /var/lib/swarmcracker/manager/swarm.sock task ls

# Check microVMs
pgrep -a firecracker
```

### Important File Locations

| File/Dir | Description |
|----------|-------------|
| `/usr/local/bin/swarmd-firecracker` | Main executor binary |
| `/usr/local/bin/swarmcracker` | CLI tool |
| `/usr/local/bin/swarmctl` | SwarmKit CLI |
| `/usr/local/bin/firecracker` | MicroVM hypervisor |
| `/usr/share/firecracker/vmlinux` | VM kernel |
| `/var/lib/firecracker/rootfs/` | Rootfs images |
| `/var/lib/swarmcracker/manager/` | Manager state |
| `/var/lib/swarmcracker/worker/` | Worker state |
| `/tmp/swarmcracker-manager.log` | Manager log |
| `/tmp/swarmcracker-worker.log` | Worker log |

### Default Ports

| Port | Service |
|------|---------|
| 4242 | SwarmKit manager API |
| 4243 | Worker remote API |
| 4789 | VXLAN overlay |
| 8000 | Token server (Vagrant only) |
