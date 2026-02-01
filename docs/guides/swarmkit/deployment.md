# SwarmKit Deployment Example

This example demonstrates how to deploy SwarmCracker with SwarmKit standalone (no Docker required).

## Prerequisites

- Linux with KVM support
- Go 1.21+
- Firecracker v1.0.0+
- SwarmKit installed (swarmd/swarmctl)

### Install SwarmKit

```bash
# From source
git clone https://github.com/moby/swarmkit.git
cd swarmkit
make binaries

# Install to PATH
sudo cp ./bin/swarmd /usr/local/bin/
sudo cp ./bin/swarmctl /usr/local/bin/

# Verify
swarmd --version
swarmctl --version
```

## Architecture

```
┌──────────────────────────────────────────────────────┐
│              SwarmKit Manager Node                   │
│  (swarmd - manager role)                             │
│  - Maintains cluster state via Raft                  │
│  - Makes scheduling decisions                        │
│  - Exposes control API (swarm.sock)                  │
└──────────────────────────────────────────────────────┘
                         │
                         │ gRPC (task assignment)
                         │
         ┌───────────────┼───────────────┐
         │               │               │
┌────────▼─────┐ ┌───────▼──────┐ ┌────▼────────┐
│  Worker 1    │ │  Worker 2    │ │  Worker N   │
│  (swarmd)    │ │  (swarmd)    │ │  (swarmd)   │
│  +           │ │  +           │ │  +          │
│  SwarmCracker│ │  SwarmCracker│ │  SwarmCracker│
│  Executor    │ │  Executor    │ │  Executor   │
│              │ │              │ │             │
│  ┌────────┐  │ │  ┌────────┐  │ │  ┌────────┐ │
│  │MicroVM │  │ │  │MicroVM │  │ │  │MicroVM │ │
│  │nginx-1 │  │ │  │nginx-2 │  │ │  │nginx-3 │ │
│  └────────┘  │ │  └────────┘  │ │  └────────┘ │
└──────────────┘ └──────────────┘ └──────────────┘
```

## Step 1: Start SwarmKit Manager

### On manager host (or first terminal for testing):

```bash
# Create state directory
sudo mkdir -p /var/lib/swarmkit/manager

# Start SwarmKit manager
sudo swarmd \
  -d /var/lib/swarmkit/manager \
  --listen-control-api /var/run/swarmkit/swarm.sock \
  --hostname manager-1 \
  --listen-remote-api 0.0.0.0:4242 \
  --debug
```

**Explanation:**
- `-d /var/lib/swarmkit/manager` - State directory
- `--listen-control-api` - Unix socket for local management
- `--hostname manager-1` - Node identifier
- `--listen-remote-api 0.0.0.0:4242` - Remote API for workers
- `--debug` - Enable debug logging

### Get Join Tokens

In another terminal:

```bash
# Set socket location
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Inspect cluster to get tokens
sudo swarmctl cluster inspect default
```

Output includes:
```
Join Tokens:
  Worker: SWMTKN-1-3vi7ajem0jed8guusgvyl98nfg18ibg4pclify6wzac6ucrhg3-0117z3s2ytr6egmmnlr6gd37n
  Manager: SWMTKN-1-3vi7ajem0jed8guusgvyl98nfg18ibg4pclify6wzac6ucrhg3-d1ohk84br3ph0njyexw0wdagx
```

**Save the worker token** - you'll need it for joining workers.

## Step 2: Configure SwarmCracker

### On each worker host:

```bash
# Create configuration
sudo mkdir -p /etc/swarmcracker
sudo tee /etc/swarmcracker/config.yaml <<EOF
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
  rootfs_dir: "/var/lib/firecracker/rootfs"
  default_vcpus: 2
  default_memory_mb: 1024

network:
  bridge_name: "swarm-br0"
  bridge_ip: "192.168.127.1/24"
  dhcp_enabled: true

logging:
  level: "info"
  format: "json"
EOF

# Validate configuration
sudo swarmcracker validate --config /etc/swarmcracker/config.yaml
```

## Step 3: Start SwarmKit Workers with SwarmCracker

### On worker host 1:

```bash
# Create state directory
sudo mkdir -p /var/lib/swarmkit/worker-1

# Start worker with SwarmCracker executor
sudo swarmd \
  -d /var/lib/swarmkit/worker-1 \
  --hostname worker-1 \
  --join-addr <MANAGER_IP>:4242 \
  --join-token <WORKER_TOKEN> \
  --listen-remote-api 0.0.0.0:4243 \
  --executor firecracker \
  --executor-config /etc/swarmcracker/config.yaml \
  --debug
```

**Replace:**
- `<MANAGER_IP>` - IP address of manager host
- `<WORKER_TOKEN>` - Worker token from Step 1

### On worker host 2:

```bash
sudo mkdir -p /var/lib/swarmkit/worker-2

sudo swarmd \
  -d /var/lib/swarmkit/worker-2 \
  --hostname worker-2 \
  --join-addr <MANAGER_IP>:4242 \
  --join-token <WORKER_TOKEN> \
  --listen-remote-api 0.0.0.0:4244 \
  --executor firecracker \
  --executor-config /etc/swarmcracker/config.yaml \
  --debug
```

**Note:** Use different `--listen-remote-api` ports for each worker if running on the same host.

### On worker host 3 (same pattern):

```bash
sudo mkdir -p /var/lib/swarmkit/worker-3

sudo swarmd \
  -d /var/lib/swarmkit/worker-3 \
  --hostname worker-3 \
  --join-addr <MANAGER_IP>:4242 \
  --join-token <WORKER_TOKEN> \
  --listen-remote-api 0.0.0.0:4245 \
  --executor firecracker \
  --executor-config /etc/swarmcracker/config.yaml \
  --debug
```

## Step 4: Verify Cluster

From the manager host (or any terminal with access to swarm.sock):

```bash
# Set socket location
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# List nodes
sudo swarmctl node ls
```

Expected output:
```
ID            Name      Membership  Status  Availability  Manager Status
<id>          manager-1 ACCEPTED    READY   ACTIVE        LEADER *
<id>          worker-1  ACCEPTED    READY   ACTIVE
<id>          worker-2  ACCEPTED    READY   ACTIVE
<id>          worker-3  ACCEPTED    READY   ACTIVE
```

## Step 5: Deploy Services as MicroVMs

### Deploy a Web Service

```bash
# Deploy nginx service (3 replicas)
sudo swarmctl service create \
  --name nginx \
  --image nginx:alpine \
  --replicas 3

# List services
sudo swarmctl service ls

# Inspect service
sudo swarmctl service inspect nginx

# Check task status
sudo swarmctl service ps nginx
```

Each replica runs as a **hardware-isolated Firecracker microVM** with:
- Full kernel separation via KVM
- Isolated network (TAP device + bridge)
- OCI-compliant root filesystem
- Fast startup (milliseconds)

### Deploy a Database Service

```bash
# Deploy PostgreSQL
sudo swarmctl service create \
  --name postgres \
  --image postgres:15-alpine \
  --replicas 1 \
  --env POSTGRES_PASSWORD=mysecretpassword
```

### Deploy a Global Service (one per node)

```bash
# Deploy monitoring agent (runs on ALL nodes)
sudo swarmctl service create \
  --name monitoring-agent \
  --image prom/node-exporter:latest \
  --mode global
```

## Step 6: Manage Services

### Scale Services

```bash
# Scale nginx to 10 replicas
sudo swarmctl service update nginx --replicas 10

# Verify scaling
sudo swarmctl service ps nginx | wc -l
```

### Update Services (Rolling Updates)

```bash
# Update nginx image with rolling update
sudo swarmctl service update nginx \
  --image nginx:1.25-alpine \
  --update-parallelism 2 \
  --update-delay 10s

# Watch update progress
sudo swarmctl service ps nginx
```

### View Service Logs

```bash
# On worker host, view Firecracker logs
sudo journalctl -u swarmd -f | grep firecracker

# Or check SwarmCracker logs
sudo ls -la /var/lib/firecracker/logs/
sudo cat /var/lib/firecracker/logs/<task-id>.log
```

### Remove Services

```bash
# Remove a service
sudo swarmctl service remove nginx
```

## Step 7: Manage Nodes

### Drain a Node (Maintenance Mode)

```bash
# Drain worker-1 (reschedules tasks elsewhere)
sudo swarmctl node drain worker-1

# Verify tasks moved
sudo swarmctl service ps nginx
```

### Reactivate a Node

```bash
# Reactivate worker-1
sudo swarmctl node activate worker-1
```

### Promote Worker to Manager

```bash
# Promote worker-1 to manager role
sudo swarmctl node promote worker-1

# Verify (shows as REACHABLE manager)
sudo swarmctl node ls
```

## Multi-Host Deployment

### For Production (Separate Hosts)

**Manager Host (192.168.1.10):**
```bash
sudo swarmd \
  -d /var/lib/swarmkit/manager \
  --listen-control-api /var/run/swarmkit/swarm.sock \
  --hostname manager-1 \
  --listen-remote-api 192.168.1.10:4242
```

**Worker Host 1 (192.168.1.11):**
```bash
sudo swarmd \
  -d /var/lib/swarmkit/worker-1 \
  --hostname worker-1 \
  --join-addr 192.168.1.10:4242 \
  --join-token <WORKER_TOKEN> \
  --listen-remote-api 192.168.1.11:4242 \
  --executor firecracker \
  --executor-config /etc/swarmcracker/config.yaml
```

**Worker Host 2 (192.168.1.12):**
```bash
sudo swarmd \
  -d /var/lib/swarmkit/worker-2 \
  --hostname worker-2 \
  --join-addr 192.168.1.10:4242 \
  --join-token <WORKER_TOKEN> \
  --listen-remote-api 192.168.1.12:4242 \
  --executor firecracker \
  --executor-config /etc/swarmcracker/config.yaml
```

## Local Testing (All on One Host)

For testing, you can run everything on one machine with different ports:

```bash
# Terminal 1: Manager
swarmd -d /tmp/manager \
  --listen-control-api /tmp/manager/swarm.sock \
  --hostname manager \
  --listen-remote-api 127.0.0.1:4242

# Terminal 2: Worker 1
swarmd -d /tmp/worker-1 \
  --hostname worker-1 \
  --join-addr 127.0.0.1:4242 \
  --join-token <TOKEN> \
  --listen-remote-api 127.0.0.1:4243 \
  --executor firecracker \
  --executor-config /etc/swarmcracker/config.yaml

# Terminal 3: Worker 2
swarmd -d /tmp/worker-2 \
  --hostname worker-2 \
  --join-addr 127.0.0.1:4242 \
  --join-token <TOKEN> \
  --listen-remote-api 127.0.0.1:4244 \
  --executor firecracker \
  --executor-config /etc/swarmcracker/config.yaml

# Terminal 4: Deploy services
export SWARM_SOCKET=/tmp/manager/swarm.sock
swarmctl service create --name nginx --image nginx:alpine --replicas 3
```

## Troubleshooting

### Worker Won't Join

```bash
# Check manager connectivity
curl http://<MANAGER_IP>:4242

# Verify token (should be WORKER token)
swarmctl cluster inspect default

# Check firewall
sudo ufw allow 4242/tcp
```

### Tasks Not Starting

```bash
# Check task status
swarmctl service ps <service-name>

# Check worker logs
sudo journalctl -u swarmd -f

# Check SwarmCracker executor
sudo swarmcracker validate --config /etc/swarmcracker/config.yaml
```

### Firecracker Issues

```bash
# Verify KVM access
ls -l /dev/kvm

# Check Firecracker binary
which firecracker
firecracker --version

# Test Firecracker manually
firecracker --api-sock /tmp/fc.sock
```

## Benefits of This Setup

✅ **No Docker Required** - Pure SwarmKit orchestration
✅ **Hardware Isolation** - Each task in its own microVM
✅ **Production-Grade** - SwarmKit powers Docker Swarm at scale
✅ **Simple Operations** - Familiar service concepts
✅ **High Availability** - Multiple managers with Raft consensus
✅ **Rolling Updates** - Zero-downtime deployments
✅ **Fast Startup** - MicroVMs boot in milliseconds

## Next Steps

1. **Add more managers** for high availability (3, 5, or 7)
2. **Configure resource limits** in service specs
3. **Set up monitoring** with global services
4. **Enable TLS** for production security
5. **Add health checks** to service definitions

## References

- [SwarmKit User Guide](user-guide.md) - Comprehensive SwarmKit documentation
- [Configuration Reference](../../configuration.md) - SwarmCracker configuration
- [Architecture](../../architecture/system.md) - System design

---

**Example Updated**: 2026-02-01  
**SwarmKit Version**: Latest from GitHub
