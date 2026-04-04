# SwarmCracker Documentation

Welcome to the SwarmCracker documentation. This is your starting point for learning about, deploying, and using SwarmCracker.

## 🚀 Getting Started

**New to SwarmCracker? Start here:**

| Guide | Best For | Time |
|-------|----------|------|
| [Local Development](getting-started/local-dev.md) | Development, testing | 10-15 min |
| [Vagrant Cluster](getting-started/vagrant.md) | Multi-node testing | 20-30 min |
| [Firecracker VMs](getting-started/firecracker-vm.md) | Production-like setup | 30-45 min |
| [DigitalOcean](getting-started/digitalocean.md) | Cloud deployment | 40-60 min |

**[See all getting started options →](getting-started/)**

---

## 📚 Documentation by Topic

### User Guides

| Topic | Description |
|-------|-------------|
| [Installation](guides/installation.md) | Prerequisites and setup |
| [Configuration](guides/configuration.md) | Config options reference |
| [Networking](guides/networking.md) | VM networking setup |
| [VXLAN Overlay](VXLAN-OVERLAY.md) | Cross-node VM communication |
| [Init Systems](guides/init-systems.md) | Tini/dumb-init integration |
| [File Management](guides/file-management.md) | Rootfs and image handling |
| [Security Hardening](guides/security-hardening.md) | Security best practices |

### SwarmKit Integration

| Topic | Description |
|-------|-------------|
| [Quick Start](guides/swarmkit/quick-start.md) | Quick SwarmKit deployment |
| [User Guide](guides/swarmkit/user-guide.md) | Using SwarmKit features |
| [Overview](guides/swarmkit/overview.md) | SwarmKit architecture |
| [Comprehensive Deployment](guides/swarmkit/deployment-comprehensive.md) | Production multi-node setup |

### Architecture

| Topic | Description |
|-------|-------------|
| [System Architecture](architecture/system.md) | High-level design |
| [SwarmKit Integration](architecture/swarmkit-integration.md) | Integration details |

### Development

| Topic | Description |
|-------|-------------|
| [Getting Started](development/getting-started.md) | Contributing workflow |
| [Testing](development/testing.md) | Running and writing tests |
| [Secrets Prevention](development/secrets-prevention.md) | Security practices |

### Testing

| Topic | Description |
|-------|-------------|
| [Testing Strategy](testing/strategy.md) | Overall test approach |
| [Unit Tests](testing/unit.md) | Unit testing guide |
| [Integration Tests](testing/integration.md) | Integration testing |
| [E2E Tests](testing/e2e.md) | End-to-end testing with Firecracker |

---

## 🔍 Quick Links

**Common Tasks:**
- [Install SwarmCracker](guides/installation.md)
- [Deploy a cluster](guides/swarmkit/deployment-comprehensive.md)
- [Configure networking](guides/networking.md)
- [Set up VXLAN overlay](VXLAN-OVERLAY.md)
- [Run tests](development/testing.md)

**Development:**
- [Contribute](development/getting-started.md)
- [Architecture overview](architecture/system.md)

---

## 📖 Directory Structure

```
docs/
├── README.md                 # This file
├── VXLAN-OVERLAY.md          # VXLAN overlay networking guide
├── architecture/             # Technical design docs
│   ├── system.md
│   └── swarmkit-integration.md
├── development/              # Developer docs
│   ├── getting-started.md
│   ├── secrets-prevention.md
│   └── testing.md
├── getting-started/          # Quick start guides
│   ├── local-dev.md
│   ├── vagrant.md
│   ├── firecracker-vm.md
│   └── digitalocean.md
├── guides/                   # How-to guides
│   ├── installation.md
│   ├── configuration.md
│   ├── networking.md
│   ├── security-hardening.md
│   ├── init-systems.md
│   ├── file-management.md
│   └── swarmkit/
│       ├── README.md
│       ├── quick-start.md
│       ├── user-guide.md
│       ├── overview.md
│       └── deployment-comprehensive.md
└── testing/                  # Test documentation
    ├── strategy.md
    ├── unit.md
    ├── integration.md
    └── e2e.md
```

---

## 🤝 Contributing

Found an issue? Want to improve docs? See:
- [Contributing Guide](../CONTRIBUTING.md)
- [Development Getting Started](development/getting-started.md)

---

**Last Updated:** 2026-04-04
