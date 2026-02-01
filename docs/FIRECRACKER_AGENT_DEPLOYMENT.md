# Firecracker Agent Deployment Guide

This guide explains how to deploy the `swarmd-firecracker` agent to run SwarmKit tasks as Firecracker microVMs.

## Overview

The `swarmd-firecracker` binary is a modified SwarmKit agent that integrates the SwarmCracker executor. It can:
- Join any existing SwarmKit cluster
- Run tasks as Firecracker microVMs instead of containers
- Work with standard `swarmctl` management tool

## Quick Start

### 1. Build the Binary

```bash
cd /path/to/swarmcracker
make swarmd-firecracker
```

This creates `./build/swarmd-firecracker`.

### 2. Copy to Worker Node

```bash
# Copy binary to worker
scp ./build/swarmd-firecracker root@worker-node:/usr/local/bin/swarmd-firecracker

# Make executable
ssh root@worker-node "chmod +x /usr/local/bin/swarmd-firecracker"
```

### 3. Prepare Directories

```bash
# Create required directories
ssh root@worker-node << 'EOF'
mkdir -p /var/lib/firecracker/rootfs
mkdir -p /var/run/firecracker
mkdir -p /var/lib/swarmkit
mkdir -p /var/run/swarmkit
EOF
```

### 4. Install Firecracker (if not already installed)

```bash
ssh root@worker-node << 'EOF'
# Download Firecracker
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.14.1/firecracker-v1.14.1-x86_64.tgz
tar -xzf firecracker-v1.14.1-x86_64.tgz
sudo mv release-v1.14.1-x86_64/firecracker-v1.14.1-x86_64 /usr/bin/firecracker
sudo mv release-v1.14.1-x86_64/jailer-v1.14.1-x86_64 /usr/bin/jailer
sudo chmod +x /usr/bin/firecracker /usr/bin/jailer
rm -rf firecracker-v1.14.1-x86_64.tgz release-v1.14.1-x86_64

# Set up KVM permissions
sudo usermod -aG kvm $(whoami)
EOF
```

### 5. Download Kernel Image

```bash
ssh root@worker-node << 'EOF'
# Create kernel directory
mkdir -p /usr/share/firecracker

# Download official Firecracker kernel
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.14.1/vmlinux-v5.15-x86_64.bin -O /usr/share/firecracker/vmlinux
EOF
```

### 6. Join Cluster

#### Option A: Start as Worker (recommended)

```bash
ssh root@worker-node << 'EOF'
# Get join token from manager first
# WORKER_TOKEN=$(swarmctl cluster inspect default --format {{.WorkerJoinToken}})

# Then start worker
swarmd-firecracker \
  --hostname worker-firecracker \
  --join-addr 192.168.56.10:4242 \
  --join-token SWMTKN-1-0ez8b0cbdw56pp79c1zxmtnbr1dywdlvmp7z23u3ufut5gkezr-d1gvmdj71sdxtzw3u78ym8yi5 \
  --listen-remote-api 0.0.0.0:4242 \
  --kernel-path /usr/share/firecracker/vmlinux \
  --rootfs-dir /var/lib/firecracker/rootfs \
  --socket-dir /var/run/firecracker \
  --bridge-name swarm-br0 \
  --debug
EOF
```

#### Option B: Start as Manager (for HA cluster)

```bash
ssh root@worker-node << 'EOF'
# Get manager join token from manager
# MANAGER_TOKEN=$(swarmctl cluster inspect default --format {{.ManagerJoinToken}})

# Then start manager
swarmd-firecracker \
  --hostname manager-2 \
  --join-addr 192.168.56.10:4242 \
  --join-token MANAGER_TOKEN_HERE \
  --listen-remote-api 0.0.0.0:4242 \
  --manager \
  --kernel-path /usr/share/firecracker/vmlinux \
  --rootfs-dir /var/lib/firecracker/rootfs \
  --debug
EOF
```

### 7. Verify

```bash
# From manager, check nodes
swarmctl node ls

# You should see the new node listed
# ID              _hostname              STATUS  AVAILABILITY  MANAGER STATUS
# abc123...        manager                Ready   Active        Leader
# def456...        worker-firecracker     Ready   Active
```

## Deploying a Service

Once the Firecracker-enabled worker is running:

```bash
# Deploy nginx service
swarmctl service create \
  --name nginx \
  --image nginx:alpine \
  --replicas 2 \
  --reservations-cpu 1 \
  --reservations-memory 512MB

# Check tasks
swarmctl service ps nginx

# Check task status (should be RUNNING, not REJECTED)
swarmctl task inspect $(swarmctl task ls -q | head -1)
```

## Systemd Service

For production use, create a systemd service:

```bash
cat > /tmp/swarmd-firecracker.service << 'EOF'
[Unit]
Description=SwarmKit Agent with Firecracker Executor
After=network.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/swarmd-firecracker \
  --hostname worker-firecracker \
  --join-addr 192.168.56.10:4242 \
  --join-token SWMTKN-1-0ez8b0cbdw56pp79c1zxmtnbr1dywdlvmp7z23u3ufut5gkezr-d1gvmdj71sdxtzw3u78ym8yi5 \
  --listen-remote-api 0.0.0.0:4242 \
  --kernel-path /usr/share/firecracker/vmlinux \
  --rootfs-dir /var/lib/firecracker/rootfs \
  --socket-dir /var/run/firecracker \
  --bridge-name swarm-br0

Restart=always
RestartSec=5s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

# Install service
scp /tmp/swarmd-firecracker.service root@worker-node:/etc/systemd/system/
ssh root@worker-node "systemctl daemon-reload && systemctl enable swarmd-firecracker && systemctl start swarmd-firecracker"
```

## Configuration Options

| Option | Default | Description |
|--------|---------|-------------|
| `--state-dir` | `/var/lib/swarmkit` | State directory |
| `--join-addr` | - | Manager address (host:port) |
| `--join-token` | - | Cluster join token |
| `--listen-remote-api` | `0.0.0.0:4242` | Remote API listen address |
| `--listen-control-api` | `/var/run/swarmkit/swarm.sock` | Control API socket path |
| `--hostname` | System hostname | Node hostname |
| `--manager` | `false` | Start as manager |
| `--kernel-path` | `/usr/share/firecracker/vmlinux` | Firecracker kernel |
| `--rootfs-dir` | `/var/lib/firecracker/rootfs` | Rootfs storage |
| `--socket-dir` | `/var/run/firecracker` | Socket directory |
| `--default-vcpus` | `1` | Default VCPUs per VM |
| `--default-memory` | `512` | Default memory MB per VM |
| `--bridge-name` | `swarm-br0` | Network bridge name |
| `--debug` | `false` | Enable debug logging |

## Troubleshooting

### Task Shows REJECTED

If tasks are rejected, check:

1. **Firecracker installed:**
   ```bash
   which firecracker
   firecracker --version
   ```

2. **Kernel image exists:**
   ```bash
   ls -lh /usr/share/firecracker/vmlinux
   ```

3. **KVM access:**
   ```bash
   ls -l /dev/kvm
   groups | grep kvm
   ```

4. **Bridge exists:**
   ```bash
   ip link show swarm-br0
   ```

5. **Worker logs:**
   ```bash
   journalctl -u swarmd-firecracker -f
   ```

### Cannot Join Cluster

1. Check network connectivity:
   ```bash
   ping 192.168.56.10
   telnet 192.168.56.10 4242
   ```

2. Verify join token is correct:
   ```bash
   # On manager
   swarmctl cluster inspect default
   ```

3. Check firewall rules:
   ```bash
   # On worker
   sudo ufw status
   sudo iptables -L -n
   ```

### VM Fails to Start

1. Check Firecracker logs (in debug mode)
2. Verify kernel and rootfs paths
3. Check available memory/CPU
4. Review task status:
   ```bash
   swarmctl task inspect <task-id>
   ```

## Current Cluster Setup

Based on your existing cluster:

```
Manager: 192.168.56.10 (socket: /var/run/swarmkit/swarm.sock)
Worker1: 192.168.56.11
Join Token: SWMTKN-1-0ez8b0cbdw56pp79c1zxmtnbr1dywdlvmp7z23u3ufut5gkezr-d1gvmdj71sdxtzw3u78ym8yi5
```

### Deploying to Worker1

Since SSH keys aren't set up, you'll need to manually copy files:

```bash
# Step 1: Create deployment package
cd /home/kali/.openclaw/workspace/projects/swarmcracker
tar czf swarmd-firecracker-deploy.tgz \
  build/swarmd-firecracker \
  docs/FIRECRACKER_AGENT_DEPLOYMENT.md

# Step 2: Copy to worker1 (manually, via scp or file share)
# scp swarmd-firecracker-deploy.tgz root@192.168.56.11:/tmp/

# Step 3: On worker1
# tar xzf /tmp/swarmd-firecracker-deploy.tgz -C /tmp
# cp /tmp/build/swarmd-firecracker /usr/local/bin/
# chmod +x /usr/local/bin/swarmd-firecracker

# Step 4: Stop old swarmd
# systemctl stop swarmd
# systemctl disable swarmd

# Step 5: Start new swarmd-firecracker (see above commands)
```

## Success Criteria

After deployment, verify:

- [ ] Node appears in `swarmctl node ls`
- [ ] nginx tasks show state `RUNNING` (not `REJECTED`)
- [ ] Tasks are running in Firecracker VMs
- [ ] Service scaling works: `swarmctl service update nginx --replicas 4`
- [ ] Cluster remains stable

## Next Steps

1. Deploy to worker1 (192.168.56.11)
2. Test with nginx service
3. Scale service to 4 replicas
4. Verify all tasks running in Firecracker
5. Document any issues found
