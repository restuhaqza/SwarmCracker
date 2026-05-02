# SwarmCracker Documentation Index

> Firecracker microVM orchestration with SwarmKit — v0.6.0

---

## Documentation Structure

### 👤 User Documentation

| Section | Description |
|---------|-------------|
| [Getting Started](user/getting-started/) | Installation, quick start, prerequisites |
| [Guides](user/guides/) | Configuration, networking, security, snapshots |
| [Architecture](user/architecture/) | System design, SwarmKit integration |
| [Reference](user/reference/) | CLI commands |

### 🔧 Developer Documentation

| Section | Description |
|---------|-------------|
| [Contributing](dev/contributing.md) | Code style, PR process |
| [Conventions](dev/conventions.md) | File naming standards |
| [Testing](dev/testing/) | Test strategy, coverage |
| [Architecture](dev/architecture/) | Internal integration details |

### 📋 Planning

| Document | Description |
|----------|-------------|
| [TODO Implementation](planning/todo-implementation.md) | Remaining TODO items |
| [Init/Deinit Plan](planning/init-deinit.md) | Lifecycle strategy |

### 🔬 Research

| Document | Description |
|----------|-------------|
| [Image Preparation](research/image-preparation.md) | Rootfs preparation |
| [Image SDK](research/image-sdk.md) | SDK investigation |
| [Archived](research/archived/) | Historical docs |

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

**Last Updated:** 2026-04-19 | **Doc Version:** 3.0