# SwarmKit Quick Start

Get a SwarmCracker + SwarmKit cluster running in minutes.

## 🎯 What You'll Learn

By the end of this guide, you'll have:
- ✅ A working SwarmKit cluster (1 manager, 2 workers)
- ✅ SwarmCracker executor running on workers
- ✅ Services deployed as Firecracker MicroVMs
- ✅ Basic service management (scale, update, inspect)

**Time:** 15-20 minutes
**Difficulty:** Beginner

## 📋 Prerequisites

### System Requirements
- Linux with KVM support (Ubuntu 22.04+ recommended)
- Go 1.21+
- Firecracker v1.0.0+
- At least 2GB RAM per worker

### Install Dependencies

```bash
# Install Go (if not already installed)
sudo apt update
sudo apt install -y golang-1.21

# Install Firecracker
sudo apt install -y firecracker

# Install SwarmKit
git clone https://github.com/moby/swarmkit.git
cd swarmkit
make binaries
sudo cp ./bin/swarmd /usr/local/bin/
sudo cp ./bin/swarmctl /usr/local/bin/

# Verify installations
go version
firecracker --version
swarmd --version
swarmctl --version
```

## 🏗️ Architecture

```
┌──────────────────────────────────┐
│   SwarmKit Manager               │
│   (swarmd - manager role)        │
│   Port: 4242                    │
└──────────────────────────────────┘
           │         │
           │         │
┌──────────▼──┐  ┌──▼─────────────┐
│  Worker 1   │  │   Worker 2     │
│  (swarmd)   │  │   (swarmd)     │
│  +          │  │   +            │
│  SwarmCracker│  │   SwarmCracker│
│  Executor   │  │   Executor     │
└─────────────┘  └────────────────┘
```

## 🚀 Quick Start (Single-Node Testing)

For testing, you can run everything on one machine in separate terminals.

### Terminal 1: Start Manager

```bash
# Create state directory
mkdir -p /tmp/swarmkit-manager

# Start manager
swarmd \
  --listen-remote-api 0.0.0.0:4242 \
  --state-dir /tmp/swarmkit-manager \
  --manager \
  --addr manager:4242 \
  --debug
```

### Get Join Token

```bash
# In a new terminal
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
mkdir -p $(dirname $SWARM_SOCKET)
swarmctl cluster ls
swarmctl cluster inspect default --pretty
```

Copy the `Worker Join Token` from the output.

### Terminal 2: Configure & Start Worker

```bash
# Set the manager address and token
export SWARM_MANAGER_ADDR=127.0.0.1:4242
export SWARM_WORKER_TOKEN=<paste-token-here>

# Create worker state directory
mkdir -p /tmp/swarmkit-worker

# Start worker
swarmd \
  --state-dir /tmp/swarmkit-worker \
  --join-addr $SWARM_MANAGER_ADDR \
  --join-token $SWARM_WORKER_TOKEN \
  --debug
```

### Terminal 3: Start SwarmCracker Executor

```bash
# Build SwarmCracker
cd /path/to/swarmcracker
go build -o swarmcracker ./cmd/swarmcracker

# Configure SwarmCracker
cat > /tmp/worker-config.yaml <<EOF
executor:
  name: firecracker
  kernel_path: /usr/share/firecracker/vmlinux
  rootfs_dir: /var/lib/firecracker/rootfs
  socket_dir: /var/run/firecracker

network:
  bridge_name: swarm-br0
  enable_nat: true
  subnet: "192.168.127.0/24"

init_system: "tini"
EOF

# Start executor
sudo ./swarmcracker swarmkit \
  --config /tmp/worker-config.yaml \
  --socket /var/run/swarmkit/swarm.sock
```

### Deploy a Test Service

```bash
# Set swarmctl socket
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Create an nginx service
swarmctl service create \
  --name nginx \
  --image nginx:alpine \
  --replicas 2

# Check service status
swarmctl service ls
swarmctl service ps nginx

# Verify VMs are running
sudo ps aux | grep firecracker
```

## 🌐 Multi-Node Deployment

For production, deploy on separate machines.

### Manager Node (192.168.1.10)

```bash
# Start manager
swarmd \
  --listen-remote-api 0.0.0.0:4242 \
  --state-dir /var/lib/swarmkit/manager \
  --manager \
  --addr 192.168.1.10:4242 \
  --host-addr 192.168.1.10
```

### Worker Node 1 (192.168.1.11)

```bash
# Get join token from manager
export SWARM_MANAGER_ADDR=192.168.1.10:4242
export SWARM_WORKER_TOKEN=<token-from-manager>

# Start worker
swarmd \
  --state-dir /var/lib/swarmkit/worker \
  --join-addr $SWARM_MANAGER_ADDR \
  --join-token $SWARM_WORKER_TOKEN \
  --host-addr 192.168.1.11

# Start SwarmCracker executor
sudo swarmcracker swarmkit \
  --config /etc/swarmcracker/config.yaml \
  --socket /var/run/swarmkit/swarm.sock
```

### Worker Node 2 (192.168.1.12)

Same as Worker 1, with IP 192.168.1.12.

## 📊 Verify Cluster

From any node with swarmctl:

```bash
# Check nodes
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
swarmctl node ls

# Check services
swarmctl service ls

# Check tasks
swarmctl task ls
```

## 🎮 Manage Services

### Scale a Service

```bash
swarmctl service update nginx --replicas 5
```

### Update a Service

```bash
swarmctl service update nginx --image nginx:1.25-alpine
```

### Inspect a Service

```bash
swarmctl service inspect nginx --pretty
```

### Remove a Service

```bash
swarmctl service rm nginx
```

## 🧹 Clean Up

```bash
# Remove all services
swarmctl service ls -q | xargs swarmctl service rm

# Stop workers and managers
pkill swarmd
pkill swarmcracker

# Remove state directories
sudo rm -rf /tmp/swarmkit-*
sudo rm -rf /var/lib/swarmkit
```

## 📚 Next Steps

- **Production Deployment:** See [Comprehensive Deployment Guide](deployment-comprehensive.md)
- **Configuration:** See [Configuration Reference](../configuration.md)
- **Networking:** See [Networking Guide](../networking.md)
- **Troubleshooting:** See [Troubleshooting Guide](deployment-comprehensive.md#troubleshooting)

## 🆘 Troubleshooting

### Worker Can't Join Manager

```bash
# Check manager is listening
sudo netstat -tlnp | grep 4242

# Check firewall
sudo ufw allow 4242/tcp

# Check logs
journalctl -u swarmd -f
```

### Services Not Starting

```bash
# Check SwarmCracker logs
sudo journalctl -u swarmcracker -f

# Check if VMs are running
sudo ps aux | grep firecracker

# Check task status
swarmctl task ls -q | xargs -I {} swarmctl task inspect {}
```

### Network Issues

```bash
# Check bridge exists
ip link show swarm-br0

# Check NAT rules
sudo iptables -t nat -L -n

# Test VM networking
sudo ip netns list
```

---

**Need Help?** See [Comprehensive Guide](deployment-comprehensive.md) for detailed troubleshooting.

**Last Updated:** 2026-04-04
