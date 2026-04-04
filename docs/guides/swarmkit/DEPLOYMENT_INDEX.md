# SwarmKit Deployment Guide Index

Complete guide to deploying SwarmCracker with SwarmKit.

## 🚀 Quick Start

**New to SwarmCracker?** Choose your path:

### For Testing & Development
1. [Quick Start Guide](./quick-start.md) ⭐ **Start Here**
   - Get running in 15 minutes
   - Single-node or multi-node
   - Basic service management

### For Production Deployment
2. [Comprehensive Deployment Guide](./deployment-comprehensive.md) 📖 **Production Ready**
   - Full production setup
   - High availability
   - Security hardening
   - Performance tuning

## 📚 Documentation Structure

### Getting Started

| Guide | Audience | Time |
|-------|----------|------|
| [Quick Start](./quick-start.md) | Beginners | 15 min |
| [Comprehensive Deployment](./deployment-comprehensive.md) | DevOps Engineers | 1-2 hours |
| [User Guide](./user-guide.md) | Service Operators | 30 min |

### Configuration & Operations

| Guide | Description |
|-------|-------------|
| [Configuration Reference](../configuration.md) | All config options |
| [Networking Guide](../networking.md) | VM networking setup |
| [Init Systems Guide](../init-systems.md) | Tini/dumb-init integration |
| [Overview](./overview.md) | SwarmKit architecture |

### Advanced Topics

| Guide | Description |
|-------|-------------|
| [Audit Report](./audit.md) | Security audit |
| [Clarification Summary](./clarification-summary.md) | Design decisions |

## 🎯 Deployment Scenarios

### Scenario 1: Local Development

**Purpose:** Test on your laptop

**Prerequisites:**
- Linux/Mac with KVM
- Go 1.21+
- Firecracker

**Steps:**
1. Follow [Quick Start Guide](./quick-start.md)
2. Run single-node setup
3. Deploy test services
4. Experiment and learn

**Time:** 15 minutes

**Resources Needed:**
- 2GB RAM
- 2 CPU cores
- 10GB disk

### Scenario 2: Multi-Node Test Cluster

**Purpose:** Test before production

**Prerequisites:**
- 3 Linux machines (VMs or bare metal)
- Network connectivity
- Root access

**Steps:**
1. Follow [Quick Start Guide - Multi-Node](./quick-start.md#-multi-node-deployment)
2. Deploy 1 manager, 2 workers
3. Test service orchestration
4. Verify failover

**Time:** 30 minutes

**Resources Needed:**
- 3 machines × 2GB RAM
- Network bandwidth

### Scenario 3: Production Cluster

**Purpose:** Run production workloads

**Prerequisites:**
- 5+ Linux machines (3 managers, 2+ workers)
- Load balancer
- Monitoring stack
- Backup strategy

**Steps:**
1. Follow [Comprehensive Deployment Guide](./deployment-comprehensive.md)
2. Set up HA managers (3 or 5 nodes)
3. Configure worker nodes
4. Set up monitoring & logging
5. Configure backups
6. Test disaster recovery

**Time:** 2-4 hours

**Resources Needed:**
- 5+ machines × 4GB RAM
- Load balancer
- Monitoring infrastructure
- Storage for backups

### Scenario 4: Hybrid Cloud

**Purpose:** Multi-cloud deployment

**Prerequisites:**
- VPN/Tunnel between clouds
- Consistent networking
- Central management

**Steps:**
1. Deploy managers in primary region
2. Connect workers across regions
3. Configure networking overlays
4. Test cross-region latency
5. Set up regional failover

**Time:** 4-6 hours

## 🔧 Common Operations

### Deploy a Service

```bash
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Simple service
swarmctl service create --name web --image nginx:alpine --replicas 3

# With environment variables
swarmctl service create \
  --name db \
  --image postgres:15 \
  --env POSTGRES_PASSWORD=secret \
  --replicas 1

# Global service (one per node)
swarmctl service create \
  --name monitor \
  --image prometheus:latest \
  --mode global
```

### Monitor Services

```bash
# List all services
swarmctl service ls

# Check service tasks
swarmctl service ps web

# Inspect specific task
swarmctl task inspect <task-id> --pretty

# View service logs
swarmctl service logs web
```

### Scale Services

```bash
# Scale up
swarmctl service update web --replicas 10

# Scale down
swarmctl service update web --replicas 2

# Auto-scaling (requires external tool)
watch -n 60 'swarmctl service update web --replicas $(calculate_desired)'
```

### Update Services

```bash
# Rolling update (default)
swarmctl service update web --image nginx:1.25-alpine

# With rollback parameters
swarmctl service update web \
  --image nginx:1.25-alpine \
  --rollback-param parallelism:1 \
  --rollback-param delay:10s

# Force update
swarmctl service update web \
  --force \
  --image nginx:latest
```

### Remove Services

```bash
# Remove single service
swarmctl service rm web

# Remove all services
swarmctl service ls -q | xargs swarmctl service rm

# Remove with cleanup
swarmctl service rm web && \
  sudo rm -rf /var/lib/firecracker/rootfs/web-*
```

## 🔍 Troubleshooting

### Quick Diagnosis

```bash
# Check cluster health
swarmctl node ls
swarmctl service ls
swarmctl task ls

# Check SwarmCracker
sudo systemctl status swarmcracker
sudo journalctl -u swarmcracker -n 50

# Check Firecracker VMs
sudo ps aux | grep firecracker
sudo ls -la /var/run/firecracker/
```

### Common Issues

| Issue | Solution |
|-------|----------|
| Worker can't join | Check firewall, verify token |
| Service not starting | Check SwarmCracker logs, verify image |
| VM not running | Check kernel path, verify resources |
| Network not working | Check bridge, verify NAT rules |
| High memory usage | Reduce replicas, add workers |

**For detailed troubleshooting:** See [Comprehensive Guide - Troubleshooting](./deployment-comprehensive.md#troubleshooting)

## 📊 Monitoring & Observability

### Health Checks

```bash
# Manager health
curl http://localhost:4242/health

# Worker connectivity
swarmctl node ls

# Service health
swarmctl service ps --no-trunc
```

### Metrics

```bash
# SwarmCracker metrics
curl http://localhost:9090/metrics

# VM resource usage
sudo virsh domstats
```

### Logging

```bash
# Follow logs
sudo journalctl -u swarmcracker -f

# Export logs
sudo journalctl -u swarmcracker --since "1 hour ago" > swarmcracker.log
```

## 🏗️ Architecture

### Single-Node

```
┌─────────────────────────────┐
│     Manager + Worker         │
│     swarmd (both roles)      │
│     + SwarmCracker          │
└─────────────────────────────┘
```

### Multi-Node (HA)

```
        ┌──────────────┐
        │  Manager 1   │
        │  (Leader)    │
        └──────┬───────┘
               │ (Raft)
        ┌──────┴──────┐
        │             │
   ┌────▼─────┐ ┌────▼─────┐
   │Manager 2 │ │Manager 3 │
   └────┬─────┘ └────┬─────┘
        │            │
        └────┬───────┘
             │
      ┌──────┴──────┐
      │             │
 ┌────▼─────┐ ┌────▼─────┐
 │Worker 1  │ │Worker N  │
 │+SwarmCracker│ │+SwarmCracker│
 └──────────┘ └──────────┘
```

## 📖 Additional Resources

### Project Documentation

- [Main README](../../README.md) - Project overview
- [Architecture](../../architecture/system.md) - System design
- [Development Guide](../../development/getting-started.md) - Contributing

### External References

- [SwarmKit GitHub](https://github.com/moby/swarmkit)
- [Firecracker GitHub](https://github.com/firecracker-microvm/firecracker)
- [Docker Swarm Docs](https://docs.docker.com/engine/swarm/)

## 🆘 Getting Help

### Documentation

1. Start with [Quick Start](./quick-start.md)
2. Review [Comprehensive Guide](./deployment-comprehensive.md)
3. Check [Troubleshooting](./deployment-comprehensive.md#troubleshooting)

### Community

- **GitHub Issues:** Report bugs
- **Discussions:** Ask questions
- **PRs:** Contribute improvements

### Professional Support

For enterprise support, contact: [Support Email]

---

**Documentation Version:** v2.0
**Last Updated:** 2026-04-04
**Maintained By:** SwarmCracker Team
