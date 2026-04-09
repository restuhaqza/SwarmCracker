# SwarmCracker Documentation Index

> Firecracker microVM orchestration with SwarmKit — v0.6.0

---

## Start Here

| Task | Guide |
|------|-------|
| **Install SwarmCracker** | [Getting Started](getting-started/) |
| **Configure cluster** | [Configuration Guide](guides/configuration.md) |
| **Deploy services** | [SwarmKit Guide](guides/swarmkit.md) |
| **CLI commands** | [CLI Reference](reference/cli.md) |

---

## Documentation

### Getting Started

| Document | Description |
|----------|-------------|
| [Installation](getting-started/) | Setup, prerequisites, quick start |

### Guides

| Guide | Description |
|-------|-------------|
| [Configuration](guides/configuration.md) | Config options, defaults |
| [SwarmKit](guides/swarmkit.md) | Services, nodes, tasks |
| [Networking](guides/networking.md) | TAP, bridge, VXLAN |
| [Security](guides/security.md) | Jailer, cgroups, seccomp |
| [Snapshots](guides/snapshots.md) | VM state persistence |
| [Advanced](guides/advanced.md) | Rolling updates, multi-arch, init |

### Reference

| Reference | Description |
|-----------|-------------|
| [CLI Reference](reference/cli.md) | `swarmcracker` + `swarmctl` commands |

### Architecture

| Document | Description |
|----------|-------------|
| [System Overview](architecture/) | Components, data flow |
| [SwarmKit Integration](architecture/swarmkit.md) | Executor interface |

### Development

| Document | Description |
|----------|-------------|
| [Contributing](development/) | Code style, PR process |
| [Testing](testing/) | Test strategy, coverage |

---

## Quick Reference

### Key Commands

```bash
# Initialize cluster
swarmcracker init --hostname manager-1

# Join worker
swarmcracker join --manager <ip>:4242 --token <token>

# Create service
swarmctl create-service nginx:latest

# Scale service
swarmctl scale <svc-id> 3

# List nodes
swarmctl ls-nodes
```

### Config Locations

| Path | Description |
|------|-------------|
| `/etc/swarmcracker/config.yaml` | Main config |
| `/var/run/swarmkit/swarm.sock` | Control socket |
| `/var/lib/swarmkit` | State directory |
| `/var/lib/jailer` | Jailer sandboxes |

---

## Versions

| Component | Version |
|-----------|---------|
| SwarmCracker | v0.6.0 |
| Firecracker | v1.15.1 |
| SwarmKit | v2.1.1 |
| Go | 1.21+ |

---

## External Links

- [GitHub Repository](https://github.com/restuhaqza/SwarmCracker)
- [Releases](https://github.com/restuhaqza/SwarmCracker/releases)
- [Issues](https://github.com/restuhaqza/SwarmCracker/issues)
- [Firecracker](https://github.com/firecracker-microvm/firecracker)
- [SwarmKit](https://github.com/moby/swarmkit)

---

**Last Updated**: 2026-04-09 | **Doc Version**: 2.0