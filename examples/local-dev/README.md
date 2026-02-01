# Local Development Setup

Single-node SwarmKit cluster for local development and testing. Manager and worker run on the same machine with isolated networking.

## Prerequisites

- Linux with KVM support
- Go 1.21+
- Firecracker installed
- SwarmKit installed (swarmd/swarmctl)
- SwarmCracker installed

## Quick Start

### 1. Start Manager

```bash
# Terminal 1: Start manager
./start.sh manager
```

This starts a SwarmKit manager on `127.0.0.1:4242` with control socket at `/tmp/local-dev/manager/swarm.sock`.

### 2. Start Worker

```bash
# Terminal 2: Start worker
./start.sh worker
```

This starts a SwarmKit worker with SwarmCracker executor. Worker joins the manager cluster automatically.

### 3. Deploy Test Service

```bash
# Terminal 3: Deploy services
./start.sh deploy
```

Deploys nginx service (2 replicas) to test the cluster.

### 4. Verify

```bash
# Check nodes
export SWARM_SOCKET=/tmp/local-dev/manager/swarm.sock
swarmctl node ls

# Check services
swarmctl service ls

# Check tasks
swarmctl service ps nginx

# Check microVMs
ps aux | grep firecracker
ip addr show swarm-br0
ip link show | grep tap
```

## Architecture

```
┌────────────────────────────────────────────────────┐
│           Single Host (localhost)                  │
│                                                     │
│  ┌──────────────┐      ┌──────────────┐           │
│  │   Manager    │      │    Worker    │           │
│  │  Port 4242   │◄────►│  Port 4243   │           │
│  └──────────────┘      └──────┬───────┘           │
│                                │                    │
│                        ┌───────▼────────┐          │
│                        │  SwarmCracker  │          │
│                        │    Executor    │          │
│                        └───────┬────────┘          │
│                                │                    │
│                    ┌───────────┼───────────┐       │
│                    │           │           │       │
│               ┌────▼───┐  ┌───▼────┐  ┌──▼───┐    │
│               │  VM 1  │  │  VM 2  │  │ VM 3 │    │
│               │ nginx  │  │ nginx  │  │ redis│    │
│               └────────┘  └────────┘  └──────┘    │
│                                                     │
│  swarm-br0 (192.168.127.1/24)                      │
│  VM Network (bridge + NAT)                         │
└────────────────────────────────────────────────────┘
```

## Configuration

### Manager Config

State directory: `/tmp/local-dev/manager`
Control API: `/tmp/local-dev/manager/swarm.sock`
Remote API: `127.0.0.1:4242`

### Worker Config

State directory: `/tmp/local-dev/worker`
Remote API: `127.0.0.1:4243`
Join token: Auto-detected from manager

### SwarmCracker Config

See `config/worker.yaml` for full configuration.

Key settings:
- Bridge: `swarm-br0` (192.168.127.1/24)
- Kernel: `/usr/share/firecracker/vmlinux`
- Rootfs: `/var/lib/firecracker/rootfs`
- Init system: `tini`

## Usage Examples

### Deploy Services

```bash
# Web server
export SWARM_SOCKET=/tmp/local-dev/manager/swarm.sock
swarmctl service create --name nginx --image nginx:alpine --replicas 3

# Database
swarmctl service create --name redis --image redis:alpine --replicas 1

# Global service (1 per node)
swarmctl service create --name exporter --image prom/node-exporter:latest --mode global
```

### Scale Services

```bash
swarmctl service update nginx --replicas 10
```

### Update Services

```bash
swarmctl service update nginx --image nginx:1.25-alpine
```

### Remove Services

```bash
swarmctl service remove nginx
```

## Clean Up

```bash
# Stop all services
export SWARM_SOCKET=/tmp/local-dev/manager/swarm.sock
swarmctl service ls -q | xargs -I {} swarmctl service remove {}

# Kill worker (Ctrl+C in worker terminal)
# Kill manager (Ctrl+C in manager terminal)

# Clean up state
rm -rf /tmp/local-dev

# Clean up network
sudo ip link delete swarm-br0
```

## Troubleshooting

### Worker can't join manager

```bash
# Check manager is running
curl http://127.0.0.1:4242

# Check join token
export SWARM_SOCKET=/tmp/local-dev/manager/swarm.sock
swarmctl cluster inspect default

# Verify worker config
cat config/worker.yaml
```

### Tasks stuck in PENDING

```bash
# Check worker logs
journalctl -u swarmd -f

# Check SwarmCracker is available
which swarmcracker
swarmcracker validate --config config/worker.yaml
```

### MicroVMs not networking

```bash
# Check bridge
ip addr show swarm-br0

# Check TAP devices
ip link show | grep tap

# Verify IP forwarding
sysctl net.ipv4.ip_forward
```

## Advanced Usage

### Multiple Workers

```bash
# Terminal 2: Worker 1
./start.sh worker

# Terminal 3: Worker 2 (with different port)
export WORKER_PORT=4244
./start.sh worker

# Terminal 4: Worker 3
export WORKER_PORT=4245
./start.sh worker
```

### Custom Service Spec

```bash
cat > service-spec.yaml <<EOF
name: myapp
image: myapp:latest
replicas: 3
env:
  - ENV=production
  - LOG_LEVEL=info
EOF

swarmctl service create --config-file service-spec.yaml
```

## Files

- `start.sh` - Startup script
- `config/worker.yaml` - SwarmCracker worker configuration
- `config/manager.yaml` - SwarmKit manager configuration (reference)
- `README.md` - This file

## Next Steps

1. Experiment with different services
2. Test scaling and updates
3. Explore the SwarmKit CLI
4. Check out production deployment guide

## See Also

- [Production Deployment Guide](../production-cluster/README.md)
- [Comprehensive Deployment Guide](../../docs/guides/swarmkit/deployment-comprehensive.md)
- [Networking Guide](../../docs/guides/networking.md)
- [Init Systems Guide](../../docs/guides/init-systems.md)
