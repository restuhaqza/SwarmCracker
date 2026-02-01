# Production Multi-Node Deployment

Production-ready SwarmKit cluster with 3 managers (HA) and multiple workers. This setup is designed for high availability, security, and scalability.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    Management Network                           │
│                     (192.168.1.0/24)                            │
│                                                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐         │
│  │Manager Node 1│  │Manager Node 2│  │Manager Node 3│         │
│  │.10           │  │.11           │  │.12           │         │
│  │(LEADER)      │◄─┤(REACHABLE)   │◄─┤(REACHABLE)   │         │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘         │
│         │                 │                 │                  │
└─────────┼─────────────────┼─────────────────┼──────────────────┘
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
```

## Prerequisites

### Hardware

**Per Manager Node:**
- 2 CPU cores minimum
- 4GB RAM minimum
- 20GB disk (SSD recommended)

**Per Worker Node:**
- 4+ CPU cores
- 16GB RAM (32GB recommended)
- 100GB disk (SSD recommended)
- KVM support

### Network

- All nodes must communicate on ports 2377, 7946 (TCP/UDP)
- Management network: 192.168.1.0/24 (or your own)
- VM network: 192.168.127.0/24 (isolated bridge)
- Internet access for image pulls

### Software

- Linux (Ubuntu 20.04+ or Debian 11+ recommended)
- Go 1.21+
- Firecracker v1.10.0+
- SwarmKit (latest from GitHub)
- SwarmCracker (latest from GitHub)

## Quick Start

### Option 1: Automated Deployment

```bash
# Deploy 3 managers + 2 workers
./deploy.sh 3 2
```

This script will:
1. Detect system configuration
2. Install dependencies on all nodes
3. Configure networking
4. Deploy managers with Raft consensus
5. Deploy workers with SwarmCracker
6. Verify cluster health

### Option 2: Manual Deployment

See the [Manual Deployment](#manual-deployment) section below.

## Manual Deployment

### Step 1: Prepare All Nodes

**On ALL nodes (managers and workers):**

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
    libvirt-daemon-system \
    libvirt-clients \
    tini

# Enable IP forwarding
echo "net.ipv4.ip_forward=1" | sudo tee -a /etc/sysctl.conf
sudo sysctl -p

# Add user to kvm group
sudo usermod -aG kvm $USER
# Log out and back in for group change to take effect
```

### Step 2: Install Firecracker

**On ALL nodes:**

```bash
# Download Firecracker
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.10.0/firecracker-v1.10.0-x86_64.tgz
tar -xzf firecracker-v1.10.0-x86_64.tgz

# Install binaries
sudo mv release-v1.10.0-x86_64/firecracker-v1.10.0-x86_64 /usr/bin/firecracker
sudo mv release-v1.10.0-x86_64/jailer-v1.10.0-x86_64 /usr/bin/jailer
sudo chmod +x /usr/bin/firecracker /usr/bin/jailer

# Verify
firecracker --version
```

### Step 3: Install SwarmKit

**On ALL nodes:**

```bash
# Build from source
git clone https://github.com/moby/swarmkit.git /tmp/swarmkit
cd /tmp/swarmkit
make binaries

# Install binaries
sudo cp ./bin/swarmd /usr/local/bin/
sudo cp ./bin/swarmctl /usr/local/bin/

# Verify
swarmd --version
swarmctl --version
```

### Step 4: Install SwarmCracker (Workers Only)

**On worker nodes ONLY:**

```bash
# Build from source
git clone https://github.com/restuhaqza/swarmcracker.git /opt/swarmcracker
cd /opt/swarmcracker
make build

# Install binary
sudo cp ./bin/swarmcracker /usr/local/bin/

# Verify
swarmcracker version
```

### Step 5: Configure Firewall

**On manager nodes:**

```bash
sudo ufw allow 22/tcp    # SSH
sudo ufw allow from 192.168.1.0/24 to any port 2377 proto tcp
sudo ufw allow from 192.168.1.0/24 to any port 7946 proto tcp
sudo ufw allow from 192.168.1.0/24 to any port 7946 proto udp
sudo ufw allow from 192.168.1.0/24 to any port 4242 proto tcp
sudo ufw enable
```

**On worker nodes:**

```bash
sudo ufw allow 22/tcp    # SSH
sudo ufw allow from 192.168.1.10 to any port 7946 proto tcp   # Manager 1
sudo ufw allow from 192.168.1.11 to any port 7946 proto tcp   # Manager 2
sudo ufw allow from 192.168.1.12 to any port 7946 proto tcp   # Manager 3
sudo ufw allow from 192.168.1.10 to any port 7946 proto udp
sudo ufw allow from 192.168.1.11 to any port 7946 proto udp
sudo ufw allow from 192.168.1.12 to any port 7946 proto udp
sudo ufw enable
```

### Step 6: Deploy Managers

**On Manager 1 (192.168.1.10):**

```bash
# Create state directory
sudo mkdir -p /var/lib/swarmkit/manager

# Create systemd service
sudo tee /etc/systemd/system/swarmd-manager.service > /dev/null <<'EOF'
[Unit]
Description=SwarmKit Manager
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmd \
  -d /var/lib/swarmkit/manager \
  --listen-control-api /var/run/swarmkit/swarm.sock \
  --hostname manager-1 \
  --listen-remote-api 0.0.0.0:4242 \
  --debug
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# Start manager
sudo systemctl daemon-reload
sudo systemctl start swarmd-manager
sudo systemctl enable swarmd-manager

# Get join tokens
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
sudo swarmctl cluster inspect default

# Save tokens (use for joining other nodes)
MANAGER_TOKEN="SWMTKN-1-xxx...zzz"  # Copy MANAGER token
WORKER_TOKEN="SWMTKN-1-xxx...yyy"   # Copy WORKER token
```

**On Manager 2 (192.168.1.11):**

```bash
sudo mkdir -p /var/lib/swarmkit/manager

sudo tee /etc/systemd/system/swarmd-manager.service > /dev/null <<'EOF'
[Unit]
Description=SwarmKit Manager
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmd \
  -d /var/lib/swarmkit/manager \
  --listen-control-api /var/run/swarmkit/swarm.sock \
  --hostname manager-2 \
  --listen-remote-api 192.168.1.11:4242 \
  --join-addr 192.168.1.10:4242 \
  --join-token $MANAGER_TOKEN \
  --debug
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl start swarmd-manager
sudo systemctl enable swarmd-manager
```

**On Manager 3 (192.168.1.12):**

```bash
sudo mkdir -p /var/lib/swarmkit/manager

sudo tee /etc/systemd/system/swarmd-manager.service > /dev/null <<'EOF'
[Unit]
Description=SwarmKit Manager
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmd \
  -d /var/lib/swarmkit/manager \
  --listen-control-api /var/run/swarmkit/swarm.sock \
  --hostname manager-3 \
  --listen-remote-api 192.168.1.12:4242 \
  --join-addr 192.168.1.10:4242 \
  --join-token $MANAGER_TOKEN \
  --debug
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl start swarmd-manager
sudo systemctl enable swarmd-manager
```

### Step 7: Verify Manager Cluster

**On any manager:**

```bash
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Check node status
sudo swarmctl node ls

# Expected output:
# ID            Name       Membership  Status  Availability  Manager Status
# <id>          manager-1  ACCEPTED    READY   ACTIVE        LEADER *
# <id>          manager-2  ACCEPTED    READY   ACTIVE        REACHABLE
# <id>          manager-3  ACCEPTED    READY   ACTIVE        REACHABLE
```

### Step 8: Deploy Workers

**On each worker node:**

```bash
# Configure SwarmCracker
sudo mkdir -p /etc/swarmcracker
sudo mkdir -p /var/lib/firecracker/rootfs
sudo mkdir -p /var/lib/firecracker/logs

# Copy worker configuration
sudo cp config/worker.yaml /etc/swarmcracker/

# Edit configuration if needed
sudo nano /etc/swarmcracker/worker.yaml

# Create systemd service
sudo tee /etc/systemd/system/swarmd-worker.service > /dev/null <<'EOF'
[Unit]
Description=SwarmKit Worker with SwarmCracker
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmd \
  -d /var/lib/swarmkit/worker \
  --hostname worker-$(hostname) \
  --join-addr 192.168.1.10:4242 \
  --join-token $WORKER_TOKEN \
  --listen-remote-api 0.0.0.0:4242 \
  --executor firecracker \
  --executor-config /etc/swarmcracker/worker.yaml \
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

### Step 9: Verify Worker Registration

**On any manager:**

```bash
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# List nodes
sudo swarmctl node ls

# Expected: All workers show as ACCEPTED, READY, ACTIVE
```

## Configuration Files

### Manager Configuration

See `config/manager.yaml` for reference. Managers use command-line flags, not YAML config.

### Worker Configuration

See `config/worker.yaml` for full SwarmCracker configuration.

Key settings:
- Bridge: `swarm-br0` (192.168.127.1/24)
- Kernel: `/usr/share/firecracker/vmlinux`
- Init system: `tini`
- Metrics: Enabled on port 9090

## Deploy Services

**On any manager:**

```bash
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Web tier
sudo swarmctl service create \
  --name nginx \
  --image nginx:alpine \
  --replicas 6 \
  --env NGINX_PORT=8080

# Data tier
sudo swarmctl service create \
  --name redis \
  --image redis:alpine \
  --replicas 3

sudo swarmctl service create \
  --name postgres \
  --image postgres:15-alpine \
  --replicas 1 \
  --env POSTGRES_PASSWORD=changeme

# Global monitoring
sudo swarmctl service create \
  --name node-exporter \
  --image prom/node-exporter:latest \
  --mode global

# Check status
sudo swarmctl service ls
sudo swarmctl service ps nginx
```

## Operations

### Scaling

```bash
# Add worker
# 1. Provision new host
# 2. Install dependencies (Step 1-5)
# 3. Start worker (Step 8)

# Scale service
sudo swarmctl service update nginx --replicas 20

# Add manager (maintain odd number)
sudo swarmctl node promote worker-1
```

### Updates

```bash
# Rolling update
sudo swarmctl service update nginx \
  --image nginx:1.25-alpine \
  --update-parallelism 2 \
  --update-delay 10s

# Monitor update
sudo swarmctl service ps nginx
```

### Maintenance

```bash
# Drain node (reschedule tasks)
sudo swarmctl node drain worker-1

# Perform maintenance
# ...

# Reactivate node
sudo swarmctl node activate worker-1
```

## Backup & Recovery

### Backup Raft State

```bash
#!/bin/bash
# backup-raft.sh

BACKUP_DIR="/backup/swarmkit/$(date +%Y%m%d)"
mkdir -p $BACKUP_DIR

# On each manager
for host in manager-1 manager-2 manager-3; do
  ssh $host "sudo tar -czf - /var/lib/swarmkit/manager" > $BACKUP_DIR/manager-$host.tar.gz
done

# Upload to remote storage
aws s3 sync $BACKUP_DIR s3://backups/swarmkit/
```

### Restore Raft State

```bash
# Stop all managers
sudo systemctl stop swarmd-manager

# Restore from backup
sudo tar -xzf /backup/swarmkit/20240201/manager-manager-1.tar.gz -C /

# Start managers
sudo systemctl start swarmd-manager
```

## Monitoring

### Health Checks

```bash
./verify-cluster.sh
```

This script checks:
- Manager availability
- Worker registration
- Service health
- VM networking

### Metrics

SwarmCracker exposes Prometheus metrics on port 9090:

```bash
# Access metrics
curl http://worker-1:9090/metrics

# Example metrics:
# swarmcracker_vms_running{node="worker-1"} 15
# swarmcracker_vms_total{node="worker-1"} 1234
# swarmcracker_tasks_running{node="worker-1"} 15
```

## Troubleshooting

See the [Comprehensive Deployment Guide](../../docs/guides/swarmkit/deployment-comprehensive.md#troubleshooting) for detailed troubleshooting steps.

### Quick Diagnostics

```bash
# Check cluster health
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
sudo swarmctl node ls
sudo swarmctl service ls

# Check logs
sudo journalctl -u swarmd* -f

# Check SwarmCracker
sudo swarmcracker validate --config /etc/swarmcracker/worker.yaml

# Check networking
ip addr show swarm-br0
bridge link
```

## Security Considerations

1. **Network Isolation:** Use separate VLANs for management and VM traffic
2. **Firewall Rules:** Restrict access to manager API (port 4242)
3. **TLS:** Enable mutual TLS for production (future feature)
4. **Secrets:** Use SwarmKit secrets for sensitive data
5. **Jailer:** Enable Firecracker jailer for additional isolation

## Files

- `deploy.sh` - Automated deployment script
- `verify-cluster.sh` - Cluster health verification
- `config/worker.yaml` - Worker configuration
- `config/manager.yaml` - Manager reference
- `ansible/` - Ansible playbooks for automation
- `terraform/` - Terraform configs for infrastructure

## Next Steps

1. Set up monitoring (Prometheus + Grafana)
2. Configure log aggregation (ELK/Loki)
3. Implement backup automation
4. Enable TLS encryption
5. Set up CI/CD pipelines

## See Also

- [Local Development Guide](../local-dev/README.md)
- [Comprehensive Deployment Guide](../../docs/guides/swarmkit/deployment-comprehensive.md)
- [Networking Guide](../../docs/guides/networking.md)
- [Init Systems Guide](../../docs/guides/init-systems.md)
