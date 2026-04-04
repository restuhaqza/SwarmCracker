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
| [Init Systems](guides/init-systems.md) | Tini/dumb-init integration |
| [File Management](guides/file-management.md) | Rootfs and image handling |

### SwarmKit Integration

| Topic | Description |
|-------|-------------|
| [Deployment Guide](guides/swarmkit/deployment.md) | Production deployment |
| [User Guide](guides/swarmkit/user-guide.md) | Using SwarmKit features |
| [Overview](guides/swarmkit/overview.md) | SwarmKit architecture |

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

---

## 🧪 Testing & Reports

| Report | Status |
|--------|--------|
| [Coverage Summary](reports/coverage/) | 87% overall coverage |
| [E2E Tests](reports/e2e/) | Real VM testing complete |
| [Unit Tests](reports/unit/) | Per-package coverage |
| [Bug Reports](reports/BUGS_ISSUES.md) | Known issues |

---

## 🔍 Quick Links

**Common Tasks:**
- [Install SwarmCracker](guides/installation.md)
- [Deploy a cluster](guides/swarmkit/deployment.md)
- [Configure networking](guides/networking.md)
- [Run tests](development/testing.md)

**Development:**
- [Contribute](development/getting-started.md)
- [Architecture overview](architecture/system.md)
- [Test coverage](reports/coverage/)

**Infrastructure:**
- [Test automation](../test-automation/README.md)
- [Deployment scripts](../test-automation/scripts/)

---

## 📖 Full Documentation Index

```
docs/
├── getting-started/           # Quick start guides
│   ├── README.md
│   ├── local-dev.md
│   ├── vagrant.md
│   ├── firecracker-vm.md
│   └── digitalocean.md
├── guides/                    # How-to guides
│   ├── installation.md
│   ├── configuration.md
│   ├── networking.md
│   ├── init-systems.md
│   ├── file-management.md
│   └── swarmkit/
│       ├── deployment.md
│       ├── user-guide.md
│       └── overview.md
├── architecture/              # Technical docs
│   ├── system.md
│   └── swarmkit-integration.md
├── development/               # Developer docs
│   ├── getting-started.md
│   ├── testing.md
│   └── secrets-prevention.md
└── reports/                   # Test reports
    ├── coverage/
    ├── e2e/
    ├── unit/
    └── archive/               # Historical reports
```

---

## 🤝 Contributing

Found an issue? Want to improve docs? See:
- [Contributing Guide](../CONTRIBUTING.md)
- [Development Getting Started](development/getting-started.md)

---

**Last Updated:** 2026-04-04  
**Version:** v0.1.0-alpha
