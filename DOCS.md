# SwarmCracker Documentation

Documentation for SwarmCracker - Firecracker MicroVMs with SwarmKit orchestration.

---

## Quick Start

| Step | Guide |
|------|-------|
| Install | [Installation Guide](docs/getting-started/installation.md) |
| First microVM | [Quick Start](docs/guides/swarmkit/quick-start.md) |
| Dev setup | [Local Development](docs/getting-started/local-dev.md) |

---

## Documentation

### Getting Started

- [Overview](docs/getting-started/README.md)
- [Installation](docs/getting-started/installation.md)
- [Local Dev](docs/getting-started/local-dev.md)
- [Vagrant](docs/getting-started/vagrant.md)
- [Cluster Init](docs/getting-started/cluster-init.md)
- [DigitalOcean](docs/getting-started/digitalocean.md)

### User Guides

- [Configuration](docs/guides/configuration.md)
- [Networking](docs/guides/networking.md)
- [Security](docs/guides/security-hardening.md)
- [Rolling Updates](docs/guides/rolling-updates.md)
- [Multi-Arch](docs/guides/multi-arch-support.md)

### SwarmKit

- [Overview](docs/guides/swarmkit/overview.md)
- [User Guide](docs/guides/swarmkit/user-guide.md)
- [Quick Start](docs/guides/swarmkit/quick-start.md)

### Architecture

- [System Design](docs/architecture/system.md)
- [SwarmKit Integration](docs/architecture/swarmkit-integration.md)
- [VXLAN](docs/vxlan-overlay.md)

### Development

- [Getting Started](docs/development/getting-started.md)
- [Testing](docs/development/testing.md)

---

## Ansible Automation

```bash
cd infrastructure/ansible

# Deploy cluster
ansible-playbook -i inventory/virtualbox-fresh site.yml -v

# Test
ansible-playbook -i inventory/virtualbox-fresh test-cluster.yml
```

---

## Project Status

| Phase | Status |
|-------|--------|
| 1-4 | Done - shutdown, health, rolling updates, multi-arch |
| 5 | Done - Ansible automation |
| 6 | Pending - auto-scaling, jailer, dashboard |

---

## Release v0.6.0 (2026-04-08)

- [v0.6.0](https://github.com/restuhaqza/SwarmCracker/releases/tag/v0.6.0) - Jailer, swarmctl, cluster init
- [v0.5.0](https://github.com/restuhaqza/SwarmCracker/releases/tag/v0.5.0) - VXLAN, NAT
- [v0.2.0](https://github.com/restuhaqza/SwarmCracker/releases/tag/v0.2.0) - Phase 1-4

---

## Links

- [GitHub](https://github.com/restuhaqza/SwarmCracker)
- [Firecracker](https://github.com/firecracker-microvm/firecracker)
- [SwarmKit](https://github.com/moby/swarmkit)

---

**Last Updated:** 2026-04-09