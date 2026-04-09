# SwarmCracker Documentation

Documentation for SwarmCracker - Firecracker MicroVMs with SwarmKit orchestration.

---

## Quick Start

| Step | Guide |
|------|-------|
| Install | [Getting Started](docs/getting-started/) |
| First microVM | [SwarmKit Guide](docs/guides/swarmkit.md) |
| Dev setup | [Development](docs/development/) |

---

## Documentation

### Getting Started

- [Installation & Quick Start](docs/getting-started/README.md)

### User Guides

- [Configuration](docs/guides/configuration.md)
- [Networking](docs/guides/networking.md)
- [Security](docs/guides/security.md)
- [Snapshots](docs/guides/snapshots.md)
- [Advanced Topics](docs/guides/advanced.md)

### SwarmKit

- [SwarmKit Integration](docs/guides/swarmkit.md)

### Architecture

- [System Design](docs/architecture/README.md)
- [SwarmKit Architecture](docs/architecture/swarmkit.md)

### Development

- [Contributing](docs/development/README.md)

### Reference

- [CLI Reference](docs/reference/cli.md)

---

## Ansible Automation

```bash
cd infrastructure/ansible

# Deploy cluster
ansible-playbook -i inventory/libvirt/hosts site.yml -v

# Test
ansible-playbook -i inventory/libvirt/hosts test-cluster.yml
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