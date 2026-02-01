# SwarmKit Deployment Guide - Comprehensive

Complete guide for deploying SwarmKit clusters with SwarmCracker executor, from single-node testing to production multi-node clusters.

## Table of Contents

1. [Overview](#overview)
2. [Prerequisites](#prerequisites)
3. [Quick Start - Single Node](#quick-start---single-node)
4. [Multi-Node Production Deployment](#multi-node-production-deployment)
5. [Security Hardening](#security-hardening)
6. [Operations & Maintenance](#operations--maintenance)
7. [Troubleshooting](#troubleshooting)
8. [Reference](#reference)

---

## Overview

### What is SwarmKit Deployment?

SwarmKit is a distributed orchestration engine that manages services across multiple nodes. Unlike Docker Swarm (which is a product built on SwarmKit), SwarmKit can run standalone without Docker dependency.

SwarmCracker integrates with SwarmKit as a **custom executor**, running containers as hardware-isolated Firecracker microVMs instead of traditional containers.

### Manager vs Worker Roles

**Manager Nodes:**
- Maintain cluster state via Raft consensus
- Make scheduling decisions
- Expose control API (swarmctl)
- Require odd numbers for HA (3, 5, 7)
- Can run tasks (but not recommended for production)

**Worker Nodes:**
- Execute tasks assigned by managers
- Run SwarmCracker executor
- Report task status back
- Can be promoted to managers
- Should be the bulk of your cluster

### High Availability Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    Management Layer (HA)                        │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                      │
│  │Manager 1 │◄─┤Manager 2 │◄─┤Manager 3 │  (Raft Consensus)     │
│  │(LEADER)  │  │          │  │          │                      │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘                      │
│       │             │             │                             │
└───────┼─────────────┼─────────────┼─────────────────────────────┘
        │             │             │
        └─────────────┴─────────────┘
                      │
        ┌─────────────┴─────────────┐
        │                           │
┌───────▼────────┐         ┌────────▼────────┐
│   Worker 1     │         │   Worker 2      │
│   + SwarmCracker       │   + SwarmCracker     │
│   ├─ VM: nginx-1      │   ├─ VM: nginx-2   │
│   ├─ VM: redis-1      │   ├─ VM: postgres-1│
│   └─ VM: app-1        │   └─ VM: app-2     │
└─────────────────┘         └─────────────────┘
```

### When to Use Single-Node vs Multi-Node

**Single-Node:**
- Local development and testing
- CI/CD pipelines
- Learning and experimentation
- Small-scale workloads (< 10 services)

**Multi-Node:**
- Production deployments
- High availability requirements
- Horizontal scaling
- Resource isolation across hosts
- Disaster recovery

---

## Prerequisites

### System Requirements

**Hardware:**
- Linux x86_64 with KVM support
- Minimum 2 CPU cores per worker
- 4GB RAM minimum (8GB+ recommended)
- 20GB disk space per worker

**Software:**
- Go 1.21+ (for building)
- Firecracker v1.0.0+
- Linux kernel 4.14+ (for KVM)

**Packages (Debian/Ubuntu):**
```bash
sudo apt-get update
sudo apt-get install -y \
    build-essential \
    git \
    wget \
    curl \
    bridge-utils \
    iproute2 \
    iptables \
    qemu-kvm \
    libvirt-daemon-system \
    libvirt-clients \
    tini
```

### Required Software

**1. Firecracker:**
```bash
# Download Firecracker
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.10.0/firecracker-v1.10.0-x86_64.tgz
tar -xzf firecracker-v1.10.0-x86_64.tgz
sudo mv release-v1.10.0-x86_64/firecracker-v1.10.0-x86_64 /usr/bin/firecracker
sudo mv release-v1.10.0-x86_64/jailer-v1.10.0-x86_64 /usr/bin/jailer
sudo chmod +x /usr/bin/firecracker /usr/bin/jailer

# Verify KVM access
ls -l /dev/kvm
# If permission denied, add user to kvm group:
sudo usermod -aG kvm $USER
# Log out and back in for group change to take effect
```

**2. SwarmKit (swarmd/swarmctl):**
```bash
# From source
git clone https://github.com/moby/swarmkit.git
cd swarmkit
make binaries
sudo cp ./bin/swarmd /usr/local/bin/
sudo cp ./bin/swarmctl /usr/local/bin/

# Verify
swarmd --version
swarmctl --version
```

**3. SwarmCracker:**
```bash
# From source
cd /opt
git clone https://github.com/restuhaqza/swarmcracker.git
cd swarmcracker
make build
sudo cp ./bin/swarmcracker /usr/local/bin/

# Verify
swarmcracker version
```

**4. Init Systems:**
```bash
# Install tini (recommended)
sudo apt-get install -y tini

# Or install dumb-init
sudo apt-get install -y dumb-init
```

**Optional: Automation Tools:**
```bash
# Ansible for multi-node deployment
sudo apt-get install -y ansible

# Terraform for infrastructure provisioning
wget https://releases.hashicorp.com/terraform/1.6.0/terraform_1.6.0_linux_amd64.zip
unzip terraform_1.6.0_linux_amd64.zip
sudo mv terraform /usr/local/bin/
```

### Network Requirements

**Required Ports:**

| Port | Protocol | Purpose |
|------|----------|---------|
| 2377 | TCP | Manager gRPC API |
| 7946 | TCP/UDP | RAFT gossip protocol |
| 4242+ | TCP | Remote API (customizable) |

**Firewall Rules:**
```bash
# For managers
sudo ufw allow 2377/tcp
sudo ufw allow 7946/tcp
sudo ufw allow 7946/udp
sudo ufw allow 4242/tcp

# For workers
sudo ufw allow 7946/tcp
sudo ufw allow 7946/udp

# Enable IP forwarding for VM networking
sudo sysctl -w net.ipv4.ip_forward=1
echo "net.ipv4.ip_forward=1" | sudo tee -a /etc/sysctl.conf
```

---

## Quick Start - Single Node

Get a SwarmKit cluster running locally with SwarmCracker in 5 minutes.

### Step 1: Start Manager

```bash
# Create state directory
mkdir -p /tmp/swarmkit/manager

# Start SwarmKit manager
swarmd \
  -d /tmp/swarmkit/manager \
  --listen-control-api /tmp/swarmkit/manager/swarm.sock \
  --hostname manager \
  --listen-remote-api 127.0.0.1:4242 \
  --debug

# Expected output:
# INFO[0000] msg="starting swarm"
# INFO[0000] msg="listening for connections" address=/tmp/swarmkit/manager/swarm.sock
```

**Leave this terminal open.**

### Step 2: Get Join Token

```bash
# Open new terminal
export SWARM_SOCKET=/tmp/swarmkit/manager/swarm.sock

# Inspect cluster
swarmctl cluster inspect default

# Look for:
# Join Tokens:
#   Worker: SWMTKN-1-xxx...yyy
#   Manager: SWMTKN-1-xxx...zzz

# Save the WORKER token
WORKER_TOKEN="SWMTKN-1-xxx...yyy"
```

### Step 3: Configure SwarmCracker

```bash
# Create config directory
sudo mkdir -p /etc/swarmcracker

# Create configuration
sudo tee /etc/swarmcracker/config.yaml > /dev/null <<EOF
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
  rootfs_dir: "/var/lib/firecracker/rootfs"
  default_vcpus: 2
  default_memory_mb: 1024
  init_system: "tini"

network:
  bridge_name: "swarm-br0"
  bridge_ip: "192.168.127.1/24"
  ip_mode: "static"
  nat_enabled: true

logging:
  level: "info"
  format: "json"
EOF

# Validate configuration
sudo swarmcracker validate --config /etc/swarmcracker/config.yaml

# Expected: Configuration is valid
```

### Step 4: Start Worker

```bash
# Create worker state directory
mkdir -p /tmp/swarmkit/worker

# Start worker with SwarmCracker executor
swarmd \
  -d /tmp/swarmkit/worker \
  --hostname worker \
  --join-addr 127.0.0.1:4242 \
  --join-token $WORKER_TOKEN \
  --listen-remote-api 127.0.0.1:4243 \
  --executor firecracker \
  --executor-config /etc/swarmcracker/config.yaml \
  --debug

# Expected output:
# INFO[0000] msg="connecting to manager" addr=127.0.0.1:4242
# INFO[0000] msg="successfully joined cluster"
```

**Leave this terminal open.**

### Step 5: Deploy Test Service

```bash
# Open another terminal
export SWARM_SOCKET=/tmp/swarmkit/manager/swarm.sock

# Deploy nginx service
swarmctl service create \
  --name nginx \
  --image nginx:alpine \
  --replicas 2

# Expected output:
# ID: <service-id>
# Name: nginx
# ...

# Wait a few seconds for tasks to start
sleep 10

# Check task status
swarmctl service ps nginx

# Expected output:
# ID     Name    Image          Node  DesiredState  CurrentState
# <id>   nginx.1 nginx:alpine   worker RUNNING       RUNNING
# <id>   nginx.2 nginx:alpine   worker RUNNING       RUNNING
```

### Step 6: Verify MicroVMs Running

```bash
# Check running Firecracker processes
ps aux | grep firecracker

# Check network bridge
ip addr show swarm-br0

# Expected: Bridge exists with IP 192.168.127.1/24

# Check TAP devices
ip link show | grep tap

# Expected: tapeth0, tapeth1, etc.

# Test VM connectivity
# (Need to find allocated IPs first)
swarmctl service ps nginx --format '{{ .Status }}'
```

### Step 7: Clean Up

```bash
# Remove service
export SWARM_SOCKET=/tmp/swarmkit/manager/swarm.sock
swarmctl service remove nginx

# Kill worker (Ctrl+C in worker terminal)
# Kill manager (Ctrl+C in manager terminal)

# Clean up state
sudo rm -rf /tmp/swarmkit
sudo ip link delete swarm-br0
```

### Troubleshooting Quick Start

**Problem:** Manager won't start
```bash
# Check if port 4242 is in use
lsof -i :4242

# Solution: Use different port
swarmd --listen-remote-api 127.0.0.1:4243
```

**Problem:** Worker can't join manager
```bash
# Test connectivity
curl http://127.0.0.1:4242

# Check token (must be WORKER token, not MANAGER token)
swarmctl cluster inspect default

# Check firewall
sudo ufw status
```

**Problem:** Tasks stuck in PENDING state
```bash
# Check worker logs
journalctl -u swarmd -f

# Verify SwarmCracker is available
which swarmcracker

# Check config validity
swarmcracker validate --config /etc/swarmcracker/config.yaml
```

---

## Multi-Node Production Deployment

Production-ready multi-node SwarmKit cluster with 3 managers (HA) and multiple workers.

### 4.1 Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│              Management Network (192.168.1.0/24)            │
│                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │Manager Node 1│  │Manager Node 2│  │Manager Node 3│     │
│  │.10           │  │.11           │  │.12           │     │
│  │(LEADER)      │◄─┤(REACHABLE)   │◄─┤(REACHABLE)   │     │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘     │
│         │                 │                 │             │
└─────────┼─────────────────┼─────────────────┼─────────────┘
          │                 │                 │
          └─────────────────┴─────────────────┘
                            │
          ┌─────────────────┴─────────────────┐
          │                                   │
┌─────────▼────────┐               ┌──────────▼───────┐
│  Worker Node 1   │               │  Worker Node 2   │
│  .20             │               │  .21             │
│  + SwarmCracker  │               │  + SwarmCracker  │
│  ├─ VM: nginx-1  │               │  ├─ VM: nginx-2  │
│  ├─ VM: redis-1  │               │  ├─ VM: postgres-1│
│  └─ VM: app-1    │               │  └─ VM: app-2    │
└──────────────────┘               └──────────────────┘

┌──────────────────┐
│  Worker Node N   │
│  .2N             │
│  + SwarmCracker  │
│  └─ VM: ...      │
└──────────────────┘
```

**Network Segmentation:**
- **Management Network:** Host-to-host communication (SwarmKit gRPC)
- **VM Network:** MicroVM-to-microVM (swarm-br0 bridge)
- **Internet Access:** NAT via bridge

### 4.2 Network Planning

**Management Network:**
- Purpose: Manager ↔ Agent communication
- Required: All nodes must reach each other
- Ports: 2377 (gRPC), 7946 (gossip)
- Recommendation: Dedicated VLAN or network

**VM Network:**
- Purpose: MicroVM communication
- Implementation: Linux bridge (swarm-br0)
- Subnet: 192.168.127.0/24 (default)
- NAT: Enabled for internet access

**IP Allocation:**

| Node Type | Role | IP Range |
|-----------|------|----------|
| Manager 1 | Manager/Leader | 192.168.1.10 |
| Manager 2 | Manager | 192.168.1.11 |
| Manager 3 | Manager | 192.168.1.12 |
| Worker 1 | Worker | 192.168.1.20 |
| Worker 2 | Worker | 192.168.1.21 |
| Worker N | Worker | 192.168.1.20+N |

**Firewall Rules:**

```bash
#!/bin/bash
# firewall-setup.sh

# Managers: Accept from all workers and other managers
sudo ufw allow from 192.168.1.0/24 to any port 2377 proto tcp
sudo ufw allow from 192.168.1.0/24 to any port 7946 proto tcp
sudo ufw allow from 192.168.1.0/24 to any port 7946 proto udp
sudo ufw allow from 192.168.1.0/24 to any port 4242 proto tcp

# Workers: Accept from managers
sudo ufw allow from 192.168.1.10 to any port 7946 proto tcp
sudo ufw allow from 192.168.1.10 to any port 7946 proto udp
sudo ufw allow from 192.168.1.11 to any port 7946 proto tcp
sudo ufw allow from 192.168.1.11 to any port 7946 proto udp
sudo ufw allow from 192.168.1.12 to any port 7946 proto tcp
sudo ufw allow from 192.168.1.12 to any port 7946 proto udp
```

### 4.3 Manager Setup

**Repeat on each manager node (192.168.1.10, .11, .12):**

#### Step 1: Install Dependencies

```bash
# Update system
sudo apt-get update && sudo apt-get upgrade -y

# Install required packages
sudo apt-get install -y \
    build-essential \
    git \
    wget \
    curl \
    bridge-utils \
    iproute2 \
    iptables \
    qemu-kvm \
    tini

# Enable KVM
sudo usermod -aG kvm $USER
# Log out and back in
```

#### Step 2: Install Software

```bash
# Install Firecracker
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.10.0/firecracker-v1.10.0-x86_64.tgz
tar -xzf firecracker-v1.10.0-x86_64.tgz
sudo mv release-v1.10.0-x86_64/firecracker-v1.10.0-x86_64 /usr/bin/firecracker
sudo chmod +x /usr/bin/firecracker

# Install SwarmKit
git clone https://github.com/moby/swarmkit.git /tmp/swarmkit
cd /tmp/swarmkit
make binaries
sudo cp ./bin/swarmd /usr/local/bin/
sudo cp ./bin/swarmctl /usr/local/bin/
```

#### Step 3: Create Manager Configuration

```bash
# Create state directory
sudo mkdir -p /var/lib/swarmkit/manager

# Create systemd service file
sudo tee /etc/systemd/system/swarmd-manager.service > /dev/null <<EOF
[Unit]
Description=SwarmKit Manager
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmd \\
  -d /var/lib/swarmkit/manager \\
  --listen-control-api /var/run/swarmkit/swarm.sock \\
  --hostname manager-\$(hostname) \\
  --listen-remote-api 0.0.0.0:4242 \\
  --debug
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# On manager-1 (FIRST manager only):
sudo systemctl start swarmd-manager
sudo systemctl enable swarmd-manager

# Get join tokens
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
sudo swarmctl cluster inspect default

# Save both tokens
MANAGER_TOKEN="SWMTKN-1-xxx...zzz"
WORKER_TOKEN="SWMTKN-1-xxx...yyy"
```

#### Step 4: Join Additional Managers

```bash
# On manager-2 (192.168.1.11):
sudo mkdir -p /var/lib/swarmkit/manager

sudo swarmd \
  -d /var/lib/swarmkit/manager \
  --hostname manager-2 \
  --listen-control-api /var/run/swarmkit/swarm.sock \
  --listen-remote-api 192.168.1.11:4242 \
  --join-addr 192.168.1.10:4242 \
  --join-token $MANAGER_TOKEN \
  --debug

# On manager-3 (192.168.1.12):
sudo mkdir -p /var/lib/swarmkit/manager

sudo swarmd \
  -d /var/lib/swarmkit/manager \
  --hostname manager-3 \
  --listen-control-api /var/run/swarmkit/swarm.sock \
  --listen-remote-api 192.168.1.12:4242 \
  --join-addr 192.168.1.10:4242 \
  --join-token $MANAGER_TOKEN \
  --debug
```

#### Step 5: Verify Raft Consensus

```bash
# On any manager
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Check node status
sudo swarmctl node ls

# Expected output:
# ID            Name       Membership  Status  Availability  Manager Status
# <id>          manager-1  ACCEPTED    READY   ACTIVE        LEADER *
# <id>          manager-2  ACCEPTED    READY   ACTIVE        REACHABLE
# <id>          manager-3  ACCEPTED    READY   ACTIVE        REACHABLE

# Verify cluster health
sudo swarmctl cluster inspect default
```

### 4.4 Worker Setup

**Repeat on each worker node:**

#### Step 1: Install Dependencies

```bash
# Same as managers, plus:
sudo apt-get install -y \
    build-essential \
    git \
    wget \
    curl \
    bridge-utils \
    iproute2 \
    iptables \
    qemu-kvm \
    tini
```

#### Step 2: Install Software

```bash
# Install Firecracker
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.10.0/firecracker-v1.10.0-x86_64.tgz
tar -xzf firecracker-v1.10.0-x86_64.tgz
sudo mv release-v1.10.0-x86_64/firecracker-v1.10.0-x86_64 /usr/bin/firecracker
sudo chmod +x /usr/bin/firecracker

# Install SwarmKit
git clone https://github.com/moby/swarmkit.git /tmp/swarmkit
cd /tmp/swarmkit
make binaries
sudo cp ./bin/swarmd /usr/local/bin/

# Install SwarmCracker
git clone https://github.com/restuhaqza/swarmcracker.git /opt/swarmcracker
cd /opt/swarmcracker
make build
sudo cp ./bin/swarmcracker /usr/local/bin/
```

#### Step 3: Configure SwarmCracker

```bash
# Create config directory
sudo mkdir -p /etc/swarmcracker
sudo mkdir -p /var/lib/firecracker/rootfs
sudo mkdir -p /var/lib/firecracker/logs

# Create worker configuration
sudo tee /etc/swarmcracker/worker.yaml > /dev/null <<EOF
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
  rootfs_dir: "/var/lib/firecracker/rootfs"
  socket_dir: "/var/run/firecracker"
  default_vcpus: 2
  default_memory_mb: 1024
  init_system: "tini"
  init_grace_period: 10
  enable_jailer: false

network:
  bridge_name: "swarm-br0"
  bridge_ip: "192.168.127.1/24"
  ip_mode: "static"
  nat_enabled: true
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
  address: ":9090"
  format: "prometheus"
EOF

# Validate configuration
sudo swarmcracker validate --config /etc/swarmcracker/worker.yaml
```

#### Step 4: Create Worker Service

```bash
# Create systemd service file
sudo tee /etc/systemd/system/swarmd-worker.service > /dev/null <<EOF
[Unit]
Description=SwarmKit Worker with SwarmCracker
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmd \\
  -d /var/lib/swarmkit/worker \\
  --hostname worker-\$(hostname) \\
  --join-addr 192.168.1.10:4242 \\
  --join-token $WORKER_TOKEN \\
  --listen-remote-api 0.0.0.0:4242 \\
  --executor firecracker \\
  --executor-config /etc/swarmcracker/worker.yaml \\
  --debug
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# Start worker
sudo systemctl daemon-reload
sudo systemctl start swarmd-worker
sudo systemctl enable swarmd-worker

# Check status
sudo systemctl status swarmd-worker
```

#### Step 5: Verify Worker Registration

```bash
# On any manager
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# List nodes
sudo swarmctl node ls

# Expected: Workers show as ACCEPTED, READY, ACTIVE
# ID            Name       Membership  Status  Availability  Manager Status
# <id>          manager-1  ACCEPTED    READY   ACTIVE        LEADER *
# <id>          manager-2  ACCEPTED    READY   ACTIVE        REACHABLE
# <id>          manager-3  ACCEPTED    READY   ACTIVE        REACHABLE
# <id>          worker-1   ACCEPTED    READY   ACTIVE
# <id>          worker-2   ACCEPTED    READY   ACTIVE
```

### 4.5 Service Deployment

#### Deploy Web Services

```bash
# On any manager
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Deploy nginx (3 replicas)
sudo swarmctl service create \
  --name nginx \
  --image nginx:alpine \
  --replicas 3 \
  --env NGINX_PORT=8080

# Deploy Redis (1 replica)
sudo swarmctl service create \
  --name redis \
  --image redis:alpine \
  --replicas 1

# Deploy PostgreSQL (1 replica)
sudo swarmctl service create \
  --name postgres \
  --image postgres:15-alpine \
  --replicas 1 \
  --env POSTGRES_PASSWORD=secretpassword

# Deploy global service (1 per node)
sudo swarmctl service create \
  --name node-exporter \
  --image prom/node-exporter:latest \
  --mode global
```

#### Manage Services

```bash
# List services
sudo swarmctl service ls

# Inspect service
sudo swarmctl service inspect nginx --pretty

# Check task distribution
sudo swarmctl service ps nginx

# Scale service
sudo swarmctl service update nginx --replicas 10

# Update service (rolling update)
sudo swarmctl service update nginx \
  --image nginx:1.25-alpine \
  --update-parallelism 2 \
  --update-delay 10s

# Remove service
sudo swarmctl service remove nginx
```

#### Access MicroVMs

```bash
# Check VM IPs
sudo swarmctl service ps nginx

# On worker, check TAP devices
ip addr show | grep tap

# Ping VM from host
ping -c 3 192.168.127.42

# Check VM logs
sudo journalctl -u swarmd-worker -f | grep firecracker
```

---

## Security Hardening

### 5.1 Network Security

#### Firewall Best Practices

```bash
#!/bin/bash
# hardened-firewall.sh

# Default policy: deny incoming
sudo ufw default deny incoming
sudo ufw default allow outgoing

# Allow SSH
sudo ufw allow 22/tcp

# Allow SwarmKit management traffic from specific subnet
sudo ufw allow from 192.168.1.0/24 to any port 2377 proto tcp
sudo ufw allow from 192.168.1.0/24 to any port 7946 proto tcp
sudo ufw allow from 192.168.1.0/24 to any port 7946 proto udp

# Allow manager API from specific hosts only
sudo ufw allow from 192.168.1.100 to any port 4242 proto tcp

# Enable firewall
sudo ufw enable

# Log denied packets
sudo ufw logging on
```

#### Network Isolation

```bash
# Isolate VM network from host (except bridge IP)
sudo iptables -A FORWARD -i swarm-br0 -o eth0 -j DROP
sudo iptables -A FORWARD -i eth0 -o swarm-br0 -m state --state RELATED,ESTABLISHED -j ACCEPT
sudo iptables -A FORWARD -i swarm-br0 -o swarm-br0 -j ACCEPT

# Block inter-VM communication (if needed)
sudo iptables -A FORWARD -i swarm-br0 -o swarm-br0 -j DROP

# Allow specific VM pairs
sudo iptables -I FORWARD -i swarm-br0 -o swarm-br0 \
  -s 192.168.127.10 -d 192.168.127.20 -j ACCEPT
```

### 5.2 Runtime Security

#### Enable Jailer (Production)

```yaml
# /etc/swarmcracker/worker.yaml
executor:
  enable_jailer: true
  jailer:
    uid: 1000
    gid: 1000
    chroot_base_dir: "/srv/jailer"
    netns: ""
```

#### Resource Limits

```yaml
# In service spec (future SwarmKit feature)
resources:
  limits:
    vcpus: 4
    memory_mb: 2048
  reservations:
    vcpus: 1
    memory_mb: 512
```

### 5.3 Secrets Management

#### SwarmKit Secrets

```bash
# Create secret
echo "my-secret-password" | sudo swarmctl secret create db-password -

# Use secret in service
sudo swarmctl service create \
  --name postgres \
  --image postgres:15-alpine \
  --secret db-password \
  --env POSTGRES_PASSWORD_FILE=/run/secrets/db_password

# List secrets
sudo swarmctl secret ls

# Remove secret
sudo swarmctl secret remove db-password
```

---

## Operations & Maintenance

### 6.1 Scaling the Cluster

#### Adding Workers

```bash
# Provision new host
# Install dependencies (see Worker Setup)
# Get worker token
sudo swarmctl cluster inspect default

# Start worker with new token
swarmd \
  -d /var/lib/swarmkit/worker \
  --hostname worker-new \
  --join-addr 192.168.1.10:4242 \
  --join-token <WORKER_TOKEN> \
  --executor firecracker \
  --executor-config /etc/swarmcracker/worker.yaml
```

#### Adding Managers (Keep Odd Number)

```bash
# Current: 3 managers
# Best: Add 2 more for 5 total (maintain odd number)

# On new manager host
sudo swarmctl node promote worker-new
```

#### Removing Nodes

```bash
# Drain node (reschedule tasks)
sudo swarmctl node drain worker-1

# Remove from cluster
sudo swarmctl node rm worker-1

# Stop daemon
sudo systemctl stop swarmd-worker
sudo systemctl disable swarmd-worker
```

### 6.2 Monitoring

#### Health Checks

```bash
#!/bin/bash
# cluster-health.sh

# Check manager availability
echo "=== Managers ==="
sudo swarmctl node ls --format '{{ .Hostname }}: {{ .Status }} {{ .ManagerStatus }}'

# Check worker availability
echo "=== Workers ==="
sudo swarmctl node ls --format '{{ .Hostname }}: {{ .Status }}' | grep worker

# Check service health
echo "=== Services ==="
sudo swarmctl service ls

# Check task status
echo "=== Tasks ==="
sudo swarmctl service ps nginx --format '{{ .Name }}: {{ .Status }}'
```

#### Log Aggregation

```bash
# Collect logs from all nodes
for host in manager-1 manager-2 manager-3 worker-1 worker-2; do
  ssh $host "journalctl -u swarmd* --since '1 hour ago' > /tmp/swarmkit-${host}.log"
done
```

### 6.3 Backup & Recovery

#### Backup Raft State

```bash
#!/bin/bash
# backup-raft.sh

BACKUP_DIR="/backup/swarmkit/$(date +%Y%m%d)"
mkdir -p $BACKUP_DIR

# On each manager
sudo cp -r /var/lib/swarmkit/manager $BACKUP_DIR/
sudo swarmctl cluster inspect default > $BACKUP_DIR/cluster-state.json

# Upload to S3/Glacier
aws s3 sync $BACKUP_DIR s3://backups/swarmkit/
```

#### Restore Raft State

```bash
# Stop all managers
sudo systemctl stop swarmd-manager

# Restore raft state
sudo cp -r /backup/swarmkit/20240201/manager/* /var/lib/swarmkit/manager/

# Start managers one by one
sudo systemctl start swarmd-manager
```

### 6.4 Upgrades

#### Rolling Upgrade Strategy

```bash
# Upgrade managers one at a time
# On manager-2:
sudo systemctl stop swarmd-manager
# Install new binary
sudo systemctl start swarmd-manager

# Wait for Raft consensus
sudo swarmctl node ls

# Repeat for manager-3

# Upgrade workers in batches
for node in worker-1 worker-2; do
  ssh $node "sudo systemctl stop swarmd-worker"
  ssh $node "sudo cp swarmd-new /usr/local/bin/swarmd"
  ssh $node "sudo systemctl start swarmd-worker"
done
```

---

## Troubleshooting

### Common Issues

#### Manager Won't Start

**Symptoms:** Manager daemon exits immediately

**Diagnosis:**
```bash
# Check logs
sudo journalctl -u swarmd-manager -n 50

# Check port conflicts
lsof -i :2377
lsof -i :4242
```

**Solutions:**
1. Kill conflicting process: `sudo kill -9 <pid>`
2. Change port: `--listen-remote-api 0.0.0.0:4243`
3. Check permissions: `ls -la /var/lib/swarmkit/manager`

#### Worker Can't Join Manager

**Symptoms:** Worker logs show "connection refused" or "unauthorized"

**Diagnosis:**
```bash
# Test connectivity
curl http://192.168.1.10:4242

# Check token type (must be WORKER token)
sudo swarmctl cluster inspect default

# Check firewall
sudo ufw status
```

**Solutions:**
1. Use correct token (WORKER, not MANAGER)
2. Open firewall ports
3. Verify manager is reachable

#### MicroVMs Not Networking

**Symptoms:** VMs boot but can't reach network

**Diagnosis:**
```bash
# Check bridge exists
ip addr show swarm-br0

# Check TAP devices
ip link show | grep tap

# Check IP forwarding
sysctl net.ipv4.ip_forward

# Check NAT rules
sudo iptables -t nat -L -n -v
```

**Solutions:**
1. Enable IP forwarding: `sudo sysctl -w net.ipv4.ip_forward=1`
2. Create bridge: `sudo ip link add swarm-br0 type bridge`
3. Add NAT rules: `sudo iptables -t nat -A POSTROUTING -s 192.168.127.0/24 -j MASQUERADE`

#### Services Stuck Pending

**Symptoms:** Tasks in PENDING state indefinitely

**Diagnosis:**
```bash
# Check task status
sudo swarmctl service ps <service>

# Check worker logs
sudo journalctl -u swarmd-worker -f

# Verify SwarmCracker
which swarmcracker
sudo swarmcracker validate --config /etc/swarmcracker/worker.yaml
```

**Solutions:**
1. Verify SwarmCracker is installed
2. Check config file validity
3. Check Firecracker is available
4. Verify KVM access: `ls -l /dev/kvm`

### Debug Commands

```bash
# Cluster health
sudo swarmctl cluster inspect default

# Node status
sudo swarmctl node ls

# Service status
sudo swarmctl service ls
sudo swarmctl service ps <service>

# Task inspection
sudo swarmctl task inspect <task-id>

# Agent logs
sudo journalctl -u swarmd* -f

# SwarmCracker logs
sudo journalctl -u swarmd-worker -f | grep -i error

# Firecracker processes
ps aux | grep firecracker

# Network status
ip addr show swarm-br0
bridge link
sudo iptables -t nat -L -n -v
```

---

## Reference

### Configuration Files

#### Manager Config Reference

```yaml
# swarmd manager flags (no YAML config)
-d /var/lib/swarmkit/manager              # State directory
--listen-control-api /var/run/swarmkit/swarm.sock  # Control socket
--hostname manager-1                      # Node hostname
--listen-remote-api 0.0.0.0:4242         # Remote API address
--debug                                   # Enable debug logging
--join-addr <IP>:4242                     # Manager to join (for additional managers)
--join-token <TOKEN>                      # Join token
```

#### Worker Config Reference

```yaml
# /etc/swarmcracker/worker.yaml
executor:
  kernel_path: "/path/to/vmlinux"
  rootfs_dir: "/var/lib/firecracker/rootfs"
  socket_dir: "/var/run/firecracker"
  default_vcpus: 2
  default_memory_mb: 1024
  init_system: "tini"  # or "dumb-init" or "none"
  init_grace_period: 10
  enable_jailer: false

network:
  bridge_name: "swarm-br0"
  bridge_ip: "192.168.127.1/24"
  ip_mode: "static"
  nat_enabled: true

logging:
  level: "info"  # debug, info, warn, error
  format: "json"
  output: "stdout"
```

### CLI Commands

#### swarmd (Daemon)

```bash
# Start manager
swarmd -d /state/dir --listen-control-api /var/run/swarm.sock --hostname <name>

# Start worker
swarmd -d /state/dir --hostname <name> --join-addr <manager-ip>:4242 --join-token <token>

# With custom executor
swarmd ... --executor firecracker --executor-config /path/to/config.yaml
```

#### swarmctl (Control CLI)

```bash
# Cluster management
swarmctl cluster inspect default
swarmctl cluster update

# Node management
swarmctl node ls
swarmctl node inspect <node-id>
swarmctl node drain <node-id>
swarmctl node activate <node-id>
swarmctl node promote <node-id>
swarmctl node demote <node-id>

# Service management
swarmctl service create --name <name> --image <image> --replicas <n>
swarmctl service ls
swarmctl service inspect <service-id>
swarmctl service update <service-id> --replicas <n>
swarmctl service remove <service-id>

# Task management
swarmctl service ps <service-id>
swarmctl task inspect <task-id>
```

#### swarmcracker (Executor)

```bash
# Validate configuration
swarmcracker validate --config /etc/swarmcracker/worker.yaml

# Version info
swarmcracker version

# (Note: Most operations are done via swarmd executor interface)
```

### Ports Reference

| Port | Protocol | Direction | Purpose |
|------|----------|-----------|---------|
| 2377 | TCP | Inbound to managers | Manager gRPC API |
| 7946 | TCP | Inbound to all nodes | RAFT gossip |
| 7946 | UDP | Inbound to all nodes | RAFT gossip |
| 4242 | TCP | Inbound to managers | Remote API (default) |
| 9090 | TCP | Optional | Metrics endpoint |

### System Requirements Reference

**Minimum (Development):**
- 2 CPU cores
- 4GB RAM
- 20GB disk

**Recommended (Production):**
- 4+ CPU cores per worker
- 16GB+ RAM per worker
- 100GB+ SSD storage
- 10Gbps network (between nodes)

**Manager Nodes:**
- 2 CPU cores (light load)
- 4GB RAM
- 20GB disk (Raft state is small)

---

## Next Steps

1. **Monitor your cluster** - Set up monitoring and alerting
2. **Secure your deployment** - Enable TLS, configure firewalls
3. **Automate operations** - Use Ansible/Terraform for repeatable deployments
4. **Test disaster recovery** - Practice restoring from backups
5. **Scale gradually** - Start small, add nodes as needed

## Support

- **Documentation:** See `/docs/guides/` for more guides
- **Issues:** Report bugs on GitHub
- **Community:** Join discussions in GitHub Discussions

---

**Last Updated:** 2026-02-01  
**SwarmKit Version:** Latest from GitHub  
**SwarmCracker Version:** v1.0.0+
