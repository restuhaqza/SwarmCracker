# SwarmCracker Documentation

> Firecracker microVM orchestration with SwarmKit — no Docker, no Kubernetes required.

---

## Quick Links

| What you need | Where to go |
|---------------|-------------|
| **Install SwarmCracker** | [Getting Started](getting-started/) |
| **Configure your cluster** | [Configuration Guide](guides/configuration.md) |
| **Run services** | [SwarmKit Guide](guides/swarmkit.md) |
| **CLI commands** | [CLI Reference](reference/cli.md) |
| **Understand the architecture** | [Architecture Overview](architecture/) |
| **Contribute to the project** | [Development Guide](development/) |

---

## Documentation Structure

### 🚀 Getting Started

Install and run SwarmCracker in under 10 minutes.

- **[Installation](getting-started/)** — Prerequisites, one-line install, cluster setup
- **[Local Development](getting-started/)** — Vagrant test cluster, dev environment

### 📖 Guides

Practical guides for using SwarmCracker.

| Guide | Description |
|-------|-------------|
| [Configuration](guides/configuration.md) | Config file structure, options, defaults |
| [SwarmKit](guides/swarmkit.md) | Service deployment, scaling, node management |
| [Networking](guides/networking.md) | VXLAN overlay, TAP devices, cross-node traffic |
| [Security](guides/security.md) | Jailer hardening, resource isolation, cgroups |
| [Snapshots](guides/snapshots.md) | VM state persistence, crash recovery |
| [Advanced](guides/advanced.md) | Rolling updates, multi-arch, init systems |

### 📚 Reference

Technical reference documentation.

| Reference | Description |
|-----------|-------------|
| [CLI Reference](reference/cli.md) | `swarmcracker` and `swarmctl` commands |

### 🏗️ Architecture

System design and integration details.

- **[System Overview](architecture/)** — Components, data flow, execution model
- **[SwarmKit Integration](architecture/swarmkit.md)** — Executor interface implementation

### 🔧 Development

Contributing to SwarmCracker.

- **[Contributing Guide](development/)** — Code style, testing, PR process
- **[Testing Overview](testing/)** — Test strategy, coverage

---

## Version Information

| Component | Version |
|-----------|---------|
| SwarmCracker | v0.6.0 |
| Firecracker | v1.15.1 |
| SwarmKit | v2.1.1 |
| Go | 1.21+ |

---

## External Resources

- **[GitHub Repository](https://github.com/restuhaqza/SwarmCracker)**
- **[Firecracker Docs](https://github.com/firecracker-microvm/firecracker)**
- **[SwarmKit Docs](https://github.com/moby/swarmkit)**
- **[Report Issues](https://github.com/restuhaqza/SwarmCracker/issues)**

---

**Last Updated**: 2026-04-09 | **Doc Version**: 2.0