# Deployment Guide Index

Quick navigation for all SwarmKit deployment resources.

## 🚀 Quick Start

**New to SwarmCracker?** Start here:

1. [Installation Guide](../guides/installation.md) - Install dependencies
2. [Local Development Example](../examples/local-dev/README.md) - Single-node test cluster
3. [Comprehensive Deployment Guide](../guides/swarmkit/deployment-comprehensive.md) - Full documentation

## 📚 Documentation

### Core Guides

| Guide | Description | Length |
|-------|-------------|--------|
| [Comprehensive Deployment Guide](../guides/swarmkit/deployment-comprehensive.md) | Complete production deployment guide | 31KB |
| [Basic Deployment Example](../guides/swarmkit/deployment.md) | Simple deployment walkthrough | 5KB |
| [Configuration Reference](../guides/configuration.md) | All configuration options | - |
| [Networking Guide](../guides/networking.md) | VM networking setup | - |
| [Init Systems Guide](../guides/init-systems.md) | Tini/dumb-init integration | - |

### SwarmKit Guides

| Guide | Description |
|-------|-------------|
| [SwarmKit Overview](../guides/swarmkit/overview.md) | SwarmKit architecture |
| [SwarmKit User Guide](../guides/swarmkit/user-guide.md) | Using SwarmKit features |
| [SwarmKit Audit](../guides/swarmkit/audit.md) | Security audit |

## 🏗️ Examples

### Local Development

**Purpose:** Single-node cluster for testing and development

- [README](../examples/local-dev/README.md) - Setup guide
- [start.sh](../examples/local-dev/start.sh) - Startup script
- [config/worker.yaml](../examples/local-dev/config/worker.yaml) - Configuration

**Use Cases:**
- Local development
- Testing features
- Learning SwarmKit
- CI/CD pipelines

### Production Cluster

**Purpose:** Multi-node HA cluster for production

- [README](../examples/production-cluster/README.md) - Deployment guide
- [deploy.sh](../examples/production-cluster/deploy.sh) - Deployment script
- [verify-cluster.sh](../examples/production-cluster/verify-cluster.sh) - Health check
- [config/worker.yaml](../examples/production-cluster/config/worker.yaml) - Configuration

**Use Cases:**
- Production deployments
- High availability
- Horizontal scaling
- Disaster recovery

## 🔧 Scripts & Automation

### Deployment Scripts

| Script | Purpose | Location |
|--------|---------|----------|
| `start.sh` | Local dev cluster startup | `examples/local-dev/` |
| `deploy.sh` | Production cluster deployment | `examples/production-cluster/` |
| `verify-cluster.sh` | Cluster health check | `examples/production-cluster/` |

### Systemd Services

| Service | Purpose | Location |
|---------|---------|----------|
| `swarmd-manager.service` | Manager daemon | `scripts/systemd/` |
| `swarmd-worker.service` | Worker daemon | `scripts/systemd/` |

## 📖 Deployment Scenarios

### Scenario 1: Single-Node Testing

**When:** Local development, learning, testing

1. Follow [Local Development README](../examples/local-dev/README.md)
2. Run `./start.sh manager`
3. Run `./start.sh worker`
4. Run `./start.sh deploy`

**Time:** 5 minutes

### Scenario 2: Multi-Node Production

**When:** Production deployment, HA requirements

1. Follow [Production Cluster README](../examples/production-cluster/README.md)
2. Use `deploy.sh` on each node
3. Verify with `verify-cluster.sh`

**Time:** 30-60 minutes (depending on node count)

### Scenario 3: Custom Deployment

**When:** Custom infrastructure, specific requirements

1. Read [Comprehensive Deployment Guide](../guides/swarmkit/deployment-comprehensive.md)
2. Follow manual deployment steps
3. Customize configuration as needed

**Time:** Variable

## 🎯 Common Tasks

### Deploy a Service

```bash
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
swarmctl service create --name nginx --image nginx:alpine --replicas 3
```

### Check Cluster Health

```bash
# Nodes
swarmctl node ls

# Services
swarmctl service ls

# Tasks
swarmctl service ps nginx
```

### Scale a Service

```bash
swarmctl service update nginx --replicas 10
```

### Update a Service

```bash
swarmctl service update nginx --image nginx:1.25-alpine
```

## 🔍 Troubleshooting

**Issues?** Check these resources:

1. [Comprehensive Troubleshooting](../guides/swarmkit/deployment-comprehensive.md#troubleshooting) - Common issues and solutions
2. [Verification Script](../examples/production-cluster/verify-cluster.sh) - Automated health checks
3. [Debug Commands](../guides/swarmkit/deployment-comprehensive.md#debug-commands) - Manual diagnostics

## 📚 Additional Resources

### Architecture

- [System Architecture](../architecture/system.md) - System design
- [SwarmKit Integration](../architecture/swarmkit-integration.md) - Integration details

### Development

- [Testing Guide](../development/testing.md) - Running tests
- [Development Guide](../development/getting-started.md) - Contributing

### Project

- [Main README](../README.md) - Project overview
- [PROJECT.md](../PROJECT.md) - Roadmap and status
- [CONTRIBUTING.md](../CONTRIBUTING.md) - Contribution guidelines

## 🆘 Getting Help

- **Documentation:** Start with guides above
- **Examples:** See `examples/` directory
- **Issues:** Report bugs on GitHub
- **Community:** Join discussions on GitHub

---

**Last Updated:** 2026-02-01
**SwarmCracker Version:** v1.0.0+
