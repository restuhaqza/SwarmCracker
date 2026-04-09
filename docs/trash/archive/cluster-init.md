# Cluster Initialization Guide

Initialize and join SwarmCracker clusters using kubeadm/k3s-style commands.

## Overview

SwarmCracker now provides `init` and `join` commands for easy cluster bootstrapping, similar to how `kubeadm` works for Kubernetes or `k3s` works for lightweight clusters.

**Commands:**
- `swarmcracker init` - Initialize a new cluster (runs on manager node)
- `swarmcracker join` - Join an existing cluster (runs on worker nodes)

---

## Quick Start

### 1. Initialize Manager Node

On the node that will be your cluster manager:

```bash
# Basic initialization (auto-detects IP address)
sudo swarmcracker init

# Or specify the advertise address explicitly
sudo swarmcracker init --advertise-addr 192.168.1.10:4242
```

**Output:**
```
✅ SwarmCracker cluster initialized!

Manager: swarm-manager (192.168.1.10:4242)

To add workers to this cluster:
  swarmcracker join 192.168.1.10:4242 --token <WORKER_TOKEN>

Join tokens saved to:
  /var/lib/swarmkit/join-tokens.txt
```

### 2. Get Join Token

On the manager node, retrieve the worker join token:

```bash
# Read from file
sudo cat /var/lib/swarmkit/join-tokens.txt

# Or view service logs
sudo journalctl -u swarmcracker-manager -n 50
```

### 3. Join Worker Nodes

On each worker node:

```bash
sudo swarmcracker join 192.168.1.10:4242 --token SWMTKN-1-xxxxxxxxxxxx
```

---

## Advanced Configuration

### Custom Resources

Set default vCPU and memory for microVMs:

```bash
swarmcracker init --vcpus 2 --memory 1024
```

### VXLAN Overlay Network

Enable cross-node VM networking with VXLAN:

```bash
# On manager
swarmcracker init --vxlan-enabled --vxlan-peers 192.168.1.11,192.168.1.12

# On workers (same VXLAN config)
swarmcracker join 192.168.1.10:4242 \
  --token SWMTKN-1-... \
  --vxlan-enabled \
  --vxlan-peers 192.168.1.11,192.168.1.12
```

### Custom Network Configuration

```bash
swarmcracker init \
  --bridge-name swarm-br0 \
  --subnet 192.168.127.0/24 \
  --bridge-ip 192.168.127.1/24
```

### Debug Mode

Enable verbose logging for troubleshooting:

```bash
swarmcracker init --debug
swarmcracker join 192.168.1.10:4242 --token SWMTKN-1-... --debug
```

---

## Command Reference

### `swarmcracker init`

Initialize a new SwarmCracker cluster.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--advertise-addr` | auto-detect | Address to advertise to the cluster |
| `--listen-addr` | `0.0.0.0:4242` | Address to listen for incoming connections |
| `--state-dir` | `/var/lib/swarmkit` | State directory for cluster data |
| `--config-dir` | `/etc/swarmcracker` | Configuration directory |
| `--kernel` | `/usr/share/firecracker/vmlinux` | Path to Firecracker kernel |
| `--rootfs-dir` | `/var/lib/firecracker/rootfs` | Directory for container rootfs |
| `--socket-dir` | `/var/run/firecracker` | Directory for Firecracker sockets |
| `--vcpus` | `1` | Default vCPUs per microVM |
| `--memory` | `512` | Default memory (MB) per microVM |
| `--bridge-name` | `swarm-br0` | Bridge name for VM networking |
| `--subnet` | `192.168.127.0/24` | Subnet for VM IP allocation |
| `--bridge-ip` | `192.168.127.1/24` | Bridge IP address |
| `--vxlan-enabled` | `false` | Enable VXLAN overlay |
| `--vxlan-peers` | `""` | Comma-separated VXLAN peer IPs |
| `--debug` | `false` | Enable debug logging |

**What it does:**
1. Creates required directories
2. Generates configuration files (`/etc/swarmcracker/manager-config.yaml`)
3. Creates systemd service (`swarmcracker-manager.service`)
4. Starts the manager daemon
5. Generates and saves join tokens

---

### `swarmcracker join`

Join an existing SwarmCracker cluster.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--token` | **(required)** | Join token from manager |
| `--worker` | `true` | Join as a worker node |
| `--manager`, `-m` | `false` | Join as a manager node (requires manager token) |
| `--advertise-addr` | auto-detect | Address to advertise to the cluster |
| `--hostname` | auto-detect | Hostname for this node |
| `--state-dir` | `/var/lib/swarmkit` | State directory for cluster data |
| `--config-dir` | `/etc/swarmcracker` | Configuration directory |
| `--kernel` | `/usr/share/firecracker/vmlinux` | Path to Firecracker kernel |
| `--rootfs-dir` | `/var/lib/firecracker/rootfs` | Directory for container rootfs |
| `--socket-dir` | `/var/run/firecracker` | Directory for Firecracker sockets |
| `--vcpus` | `1` | Default vCPUs per microVM |
| `--memory` | `512` | Default memory (MB) per microVM |
| `--bridge-name` | `swarm-br0` | Bridge name for VM networking |
| `--subnet` | `192.168.127.0/24` | Subnet for VM IP allocation |
| `--bridge-ip` | `192.168.127.1/24` | Bridge IP address |
| `--vxlan-enabled` | `false` | Enable VXLAN overlay |
| `--vxlan-peers` | `""` | Comma-separated VXLAN peer IPs |
| `--debug` | `false` | Enable debug logging |

**What it does:**
1. Validates connectivity to manager
2. Creates required directories
3. Generates configuration files (`/etc/swarmcracker/worker-config.yaml`)
4. Creates systemd service (`swarmcracker-worker.service`)
5. Starts the worker daemon
6. Joins the cluster

---

## File Locations

### Manager Node

| File/Directory | Purpose |
|----------------|---------|
| `/etc/swarmcracker/manager-config.yaml` | Manager configuration |
| `/etc/systemd/system/swarmcracker-manager.service` | Systemd service |
| `/var/lib/swarmkit/` | Cluster state data |
| `/var/lib/swarmkit/join-tokens.txt` | Join tokens for workers |
| `/var/lib/firecracker/rootfs/` | Container rootfs images |
| `/var/run/firecracker/` | Firecracker sockets |
| `/var/run/swarmkit/swarm.sock` | Control API socket |

### Worker Node

| File/Directory | Purpose |
|----------------|---------|
| `/etc/swarmcracker/worker-config.yaml` | Worker configuration |
| `/etc/systemd/system/swarmcracker-worker.service` | Systemd service |
| `/var/lib/swarmkit/` | Node state data |
| `/var/lib/firecracker/rootfs/` | Container rootfs images |
| `/var/run/firecracker/` | Firecracker sockets |

---

## Troubleshooting

### Manager won't start

```bash
# Check service status
sudo systemctl status swarmcracker-manager

# View logs
sudo journalctl -u swarmcracker-manager -f

# Check if port is in use
sudo ss -tlnp | grep 4242
```

### Worker can't join

```bash
# Test connectivity to manager
nc -zv <manager-ip> 4242

# Check firewall rules
sudo iptables -L -n | grep 4242

# Verify token is correct
sudo cat /var/lib/swarmkit/join-tokens.txt
```

### VXLAN not working

Ensure all nodes have the same VXLAN configuration:
- Same `--vxlan-peers` list on all nodes
- Firewall allows UDP port 4789 (VXLAN)
- Bridge is created before starting service

```bash
# Check VXLAN interface
ip -d link show vxlan0

# Check FDB entries
bridge fdb show
```

---

## Migration from Manual Setup

If you previously set up the cluster manually with `swarmd-firecracker`:

1. Stop existing services:
   ```bash
   sudo systemctl stop swarmd-firecracker
   ```

2. Backup state (optional):
   ```bash
   sudo cp -r /var/lib/swarmkit /var/lib/swarmkit.backup
   ```

3. Run `swarmcracker init` or `swarmcracker join` with the same configuration

4. The CLI will regenerate config files and services with proper systemd integration

---

## Examples

### Single-Node Cluster

```bash
# Initialize on single node
swarmcracker init

# Deploy a test service
swarmcracker run nginx:latest
```

### 3-Node Cluster with VXLAN

```bash
# Manager (192.168.1.10)
swarmcracker init \
  --vxlan-enabled \
  --vxlan-peers 192.168.1.11,192.168.1.12

# Worker 1 (192.168.1.11)
swarmcracker join 192.168.1.10:4242 \
  --token SWMTKN-1-... \
  --vxlan-enabled \
  --vxlan-peers 192.168.1.10,192.168.1.12

# Worker 2 (192.168.1.12)
swarmcracker join 192.168.1.10:4242 \
  --token SWMTKN-1-... \
  --vxlan-enabled \
  --vxlan-peers 192.168.1.10,192.168.1.11
```

### High-Performance Cluster

```bash
# Manager with more resources
swarmcracker init \
  --vcpus 2 \
  --memory 1024 \
  --debug
```

---

## See Also

- [`swarmcracker status`](./cluster-status.md) - View cluster status
- [`swarmcracker deploy`](./deploying-services.md) - Deploy services to the cluster
- [Architecture Overview](../architecture/overview.md) - Understanding SwarmCracker components
