# SwarmCracker Test Cluster - Automated Setup

Fully automated Vagrant setup for testing SwarmCracker with 1 manager + 2 workers.

## 🏗️ Architecture

```
┌──────────────────────────────────────────────────────────┐
│                  Host Machine (Kali)                     │
│                                                           │
│  ┌─────────────────┐  ┌─────────────────┐              │
│  │  Manager VM     │  │  Worker 1 VM    │              │
│  │  192.168.56.10  │  │  192.168.56.11  │              │
│  │  - 2GB RAM      │  │  - 4GB RAM      │              │
│  │  - 2 vCPUs      │  │  - 4 vCPUs      │              │
│  │  - swarmd       │  │  - swarmd       │              │
│  │  - swarmctl     │  │  - SwarmCracker │              │
│  └─────────────────┘  └─────────────────┘              │
│                       ┌─────────────────┐              │
│                       │  Worker 2 VM    │              │
│                       │  192.168.56.12  │              │
│                       │  - 4GB RAM      │              │
│                       │  - 4 vCPUs      │              │
│                       │  - swarmd       │              │
│                       │  - SwarmCracker │              │
│                       └─────────────────┘              │
└──────────────────────────────────────────────────────────┘
```

## 📋 Prerequisites

On your Kali host:

```bash
# Install VirtualBox
sudo apt-get update
sudo apt-get install -y virtualbox

# Install Vagrant
sudo apt-get install -y vagrant

# Verify installations
VBoxManage --version
vagrant --version
```

## 🚀 Quick Start

### 1. Start the Cluster

```bash
cd /home/kali/.openclaw/workspace/projects/swarmcracker/test-automation

# Make scripts executable
chmod +x *.sh scripts/*.sh

# Start all VMs (takes 5-10 minutes)
./start-cluster.sh
```

This will:
- Create 3 VMs (1 manager + 2 workers)
- Install all dependencies (Go, Firecracker, SwarmKit, SwarmCracker)
- Configure the SwarmKit cluster
- Join workers to the manager

### 2. Test Deployments

```bash
# Run automated tests
./test-deployment.sh
```

This will test:
- Service deployment (nginx, 3 replicas)
- Scaling (3 → 5 replicas)
- Rolling updates
- Multi-service stack (frontend, backend, database)
- MicroVM verification on workers

## 🎮 Common Commands

### VM Management

```bash
# Start all VMs
vagrant up

# Stop all VMs
vagrant halt

# Restart all VMs
vagrant reload

# Delete all VMs (clean slate)
vagrant destroy -f

# Start specific VM
vagrant up manager
vagrant up worker1
```

### SSH Access

```bash
# SSH into manager
vagrant ssh manager

# SSH into worker 1
vagrant ssh worker1

# SSH into worker 2
vagrant ssh worker2

# Exit from VM
exit
```

### Cluster Management (from manager)

```bash
# SSH into manager
vagrant ssh manager

# Use -s flag for socket path (recommended)
sudo swarmctl -s /var/run/swarmkit/swarm.sock node ls
sudo swarmctl -s /var/run/swarmkit/swarm.sock service ls
sudo swarmctl -s /var/run/swarmkit/swarm.sock service inspect nginx

# Or export environment variable
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
sudo swarmctl node ls
sudo swarmctl service ls
sudo swarmctl service inspect nginx
sudo swarmctl service ps nginx

# Create a service
sudo swarmctl service create --name web --image nginx:alpine --replicas 3

# Scale a service
sudo swarmctl service update web --replicas 10

# Remove a service
sudo swarmctl service remove web
```

### MicroVM Management (from workers)

```bash
# SSH into worker
vagrant ssh worker1

# List running microVMs
sudo swarmcracker list

# Check specific microVM status
sudo swarmcracker status <task-id>

# View microVM logs
sudo swarmcracker logs <task-id>

# Stop a microVM
sudo swarmcracker stop <task-id>
```

## 📊 Verification

### Check Cluster Health

```bash
# From manager node
vagrant ssh manager -c "
  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
  swarmctl node ls
"
```

Expected output:
```
ID            NAME              STATUS  AVAILABILITY  MANAGER STATUS
xxxxxx        swarm-manager     READY   ACTIVE        LEADER *
xxxxxx        swarm-worker-1    READY   ACTIVE
xxxxxx        swarm-worker-2    READY   ACTIVE
```

### Check Services

```bash
vagrant ssh manager -c "
  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
  swarmctl service ls
  swarmctl service ps nginx
"
```

### Check MicroVMs on Workers

```bash
vagrant ssh worker1 -c "sudo swarmcracker list"
vagrant ssh worker2 -c "sudo swarmcracker list"
```

## 🐛 Troubleshooting

### Socket File Not Created

**Problem:** `swarmd` doesn't create the unix socket file automatically.

**Solution:** The setup scripts now include `prepare-socket.sh` which:
1. Creates `/var/run/swarmkit` directory
2. Sets proper permissions (755)
3. Installs tmpfiles.d configuration for persistence across reboots

**Manual verification:**
```bash
# Check if socket exists
ls -l /var/run/swarmkit/swarm.sock

# If missing, run preparation script
sudo bash /tmp/scripts/prepare-socket.sh

# Restart swarmd
sudo systemctl restart swarmd

# Verify socket is created
ls -l /var/run/swarmkit/swarm.sock
```

**Persistent fix (tmpfiles):**
```bash
# The setup scripts automatically install this, but you can verify:
cat /etc/tmpfiles.d/swarmkit.conf

# Manual installation:
sudo cp scripts/swarmkit-tmpfiles.conf /etc/tmpfiles.d/swarmkit.conf
sudo systemd-tmpfiles --create
```

### VMs Won't Start

```bash
# Check VirtualBox logs
VBoxManage list vms
VBoxManage showvminfo swarm-manager

# Restart VirtualBox service
sudo systemctl restart vboxdrv
```

### Workers Can't Join Manager

```bash
# Check manager connectivity
vagrant ssh manager -c "curl http://192.168.56.10:4242"

# Check manager logs
vagrant ssh manager -c "journalctl -u swarmd -f"

# Check worker logs
vagrant ssh worker1 -c "journalctl -u swarmd -f"
```

### Tasks Not Starting

```bash
# Check task status
vagrant ssh manager -c "
  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
  swarmctl service ps nginx
"

# Check worker logs for errors
vagrant ssh worker1 -c "journalctl -u swarmd -n 100 | grep -i error"

# Check if Firecracker is working
vagrant ssh worker1 -c "firecracker --version"
vagrant ssh worker1 -c "ls -l /dev/kvm"
```

### Reset Everything

```bash
# Stop and destroy all VMs
vagrant destroy -f

# Start fresh
./start-cluster.sh
```

## 🧪 Manual Testing

### Deploy a Simple Web Service

```bash
vagrant ssh manager

export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Deploy nginx
swarmctl service create \
  --name nginx \
  --image nginx:alpine \
  --replicas 3

# Check status
swarmctl service ps nginx

# List microVMs on workers
# (from another terminal)
vagrant ssh worker1 -c "sudo swarmcracker list"
vagrant ssh worker2 -c "sudo swarmcracker list"
```

### Deploy a Stack with Multiple Services

```bash
vagrant ssh manager

export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Frontend
swarmctl service create \
  --name frontend \
  --image nginx:alpine \
  --replicas 2

# Backend
swarmctl service create \
  --name backend \
  --image python:3.11-slim \
  --replicas 2 \
  --env APP_ENV=production

# Database
swarmctl service create \
  --name db \
  --image postgres:15-alpine \
  --replicas 1 \
  --env POSTGRES_PASSWORD=mypassword

# Verify
swarmctl service ls
swarmctl service ps frontend
swarmctl service ps backend
swarmctl service ps db
```

### Test Rolling Updates

```bash
vagrant ssh manager

export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Create initial service
swarmctl service create \
  --name app \
  --image nginx:1.24-alpine \
  --replicas 4

# Watch the update
swarmctl service update app \
  --image nginx:1.25-alpine \
  --update-parallelism 2 \
  --update-delay 10s

# Monitor progress
watch -n 2 'swarmctl service ps app'
```

## 📈 Performance Testing

### Stress Test with Many Replicas

```bash
vagrant ssh manager

export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Deploy 20 microVMs
swarmctl service create \
  --name stress-test \
  --image nginx:alpine \
  --replicas 20

# Watch distribution
watch -n 1 'swarmctl service ps stress-test | grep RUNNING | wc -l'

# Check resource usage on workers
vagrant ssh worker1 -c "top -bn1 | head -20"
vagrant ssh worker2 -c "top -bn1 | head -20"
```

## 🧹 Cleanup

```bash
# Stop all VMs
vagrant halt

# Destroy all VMs
vagrant destroy -f

# Clean up test services
vagrant ssh manager -c "
  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
  swarmctl service ls -q | xargs -r swarmctl service remove
"
```

## 📚 Next Steps

Once the cluster is running:

1. **Experiment with different images**
   ```bash
   swarmctl service create --name redis --image redis:alpine --replicas 2
   ```

2. **Test resource limits**
   ```bash
   swarmctl service create --name app --image nginx:alpine \
     --limit-cpu 0.5 --limit-memory 512 --replicas 3
   ```

3. **Deploy global services** (one per node)
   ```bash
   swarmctl service create --name monitor \
     --image prom/node-exporter --mode global
   ```

4. **Test fault tolerance**
   - Stop a worker: `vagrant halt worker1`
   - Watch tasks reschedule: `swarmctl service ps nginx`
   - Start worker: `vagrant up worker1`

## 🎓 Learning Resources

- [SwarmKit Documentation](https://github.com/moby/swarmkit)
- [Firecracker Documentation](https://github.com/firecracker-microvm/firecracker)
- [SwarmCracker README](../README.md)

---

**Created:** 2026-02-01  
**Cluster Size:** 1 Manager + 2 Workers  
**Setup Time:** ~10 minutes
