# SwarmCracker Documentation

> Firecracker microVM orchestration with SwarmKit — no Docker, no Kubernetes required.

---

## How Docs Are Organized

```
docs/
├── user/           # 👤 Running SwarmCracker — start here
│   ├── guides/     # Step-by-step guides
│   └── reference/  # CLI & config reference
├── dev/            # 🔧 Contributing & hacking
│   ├── reference/  # Package & API documentation
│   ├── testing/    # Test guides
│   └── architecture/ # Integration design
├── architecture/   # 🏗️ System architecture & diagrams
├── planning/       # 📋 Implementation plans & roadmap
└── research/       # 🔬 Research notes & archived docs
```

---

## Quick Navigation

### 👤 For Users (Running SwarmCracker)

| You want to... | Go here |
|----------------|---------|
| **Install & set up** | [Configuration Guide](user/guides/configuration.md) |
| **Deploy your first service** | [SwarmKit Guide](user/guides/swarmkit.md) |
| **Manage VMs directly** | [CLI Reference](user/reference/cli.md) |
| **Set up networking** | [Networking Guide](user/guides/networking.md) |
| **Use snapshots** | [Snapshots Guide](user/guides/snapshots.md) |
| **Advanced usage** | [Advanced Guide](user/guides/advanced.md) |
| **Security hardening** | [Security Guide](user/guides/security.md) |
| **Disaster recovery** | [Disaster Recovery Guide](user/guides/disaster-recovery.md) |
| **Jailer quickstart** | [Jailer Quickstart](user/guides/jailer-quickstart.md) |
| **Operations** | [Operations Guide](user/guides/operations.md) |

### 🔧 For Developers (Contributing Code)

| You want to... | Go here |
|----------------|---------|
| **Contribute** | [Contributing Guide](dev/contributing.md) |
| **Understand the API** | [API Reference](dev/reference/api.md) |
| **Learn the architecture** | [Architecture Overview](architecture/overview.md) |
| **Run tests** | [Testing → README](dev/testing/README.md) |
| **Write E2E tests** | [Testing → E2E Tests](dev/testing/e2e-tests.md) |
| **Follow code style** | [Conventions](dev/conventions.md) |
| **Security review** | [Dev Security](dev/security.md) |

### 📋 Reference Docs

| Package | Reference |
|---------|-----------|
| **Config** | [Config Reference](dev/reference/config.md) |
| **Image** | [Image Reference](dev/reference/image.md) |
| **Lifecycle** | [Lifecycle Reference](dev/reference/lifecycle.md) |
| **Network** | [Network Reference](dev/reference/network.md) |
| **Security** | [Security Reference](dev/reference/security.md) |
| **Storage** | [Storage Reference](dev/reference/storage.md) |
| **SwarmKit** | [SwarmKit Reference](dev/reference/swarmkit.md) |

---

## Architecture

SwarmCracker transforms SwarmKit container tasks into Firecracker microVMs:

```
User → swarmcracker CLI → gRPC → swarmd-firecracker (manager)
                                        ↓
                                  SwarmKit orchestration
                                        ↓
                               swarmd-firecracker (worker)
                                        ↓
                            Executor → Firecracker microVM
                                        ↓
                              VXLAN overlay networking
```

📖 [Full Architecture Overview](architecture/overview.md)  
🖼️ [Architecture Diagram (SVG)](architecture/swarmcracker-architecture.svg)

---

## Versions

| Component | Version |
|-----------|---------|
| SwarmCracker | v0.8.0+ |
| Firecracker | v1.15.1 |
| SwarmKit | v2.1.1 |
| Go | 1.26 |

---

## Useful Links

- **[GitHub Repo](https://github.com/restuhaqza/SwarmCracker)** — source code, issues, PRs
- **[Firecracker Docs](https://github.com/firecracker-microvm/firecracker)** — the VM engine
- **[SwarmKit Docs](https://github.com/moby/swarmkit)** — the orchestration layer
- **[Report a Bug](https://github.com/restuhaqza/SwarmCracker/issues)**

---

**Last updated:** 2026-06-26 | **Doc version:** 4.0
