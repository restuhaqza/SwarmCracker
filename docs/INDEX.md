# SwarmCracker Documentation Index

Welcome to the SwarmCracker documentation! This is your central hub for all project documentation.

---

## 🎯 Find What You Need

### I want to...

| Goal | Documentation |
|------|---------------|
| **Install SwarmCracker** | [Installation Guide](getting-started/installation.md) |
| **Build from source** | [Build Guide](guides/build-from-source.md) |
| **Deploy my first microVM** | [Quick Start](guides/swarmkit/quick-start.md) |
| **Set up development environment** | [Development Guide](development/getting-started.md) |
| **Configure networking** | [Networking Guide](guides/networking/swarm-networking.md) |
| **Use VM snapshots** | [Snapshot CLI](guides/features/snapshot-cli.md) |
| **Secure with Jailer** | [Security Hardening](guides/security/security-hardening.md) |
| **Enable rolling updates** | [Rolling Updates](guides/rolling-updates.md) |
| **Deploy on multiple architectures** | [Multi-Arch Support](guides/multi-arch-support.md) |
| **Run tests** | [Testing Guide](testing/strategy.md) |

---

## 📁 Documentation Categories

### 🚀 Getting Started
New to SwarmCracker? Start here!

- [Overview](getting-started/README.md)
- [Installation Guide](getting-started/installation.md) ⭐ **Recommended**
- [Local Development](getting-started/local-dev.md)
- [Vagrant Setup](getting-started/vagrant.md)
- [Cluster Init](getting-started/cluster-init.md)
- [DigitalOcean Deployment](getting-started/digitalocean.md)
- [Firecracker VM Setup](getting-started/firecracker-vm.md)

### 📖 User Guides
How-to guides for common tasks.

#### Core
- [Configuration](guides/configuration.md)
- [Rolling Updates](guides/rolling-updates.md)
- [Multi-Arch Support](guides/multi-arch-support.md)
- [Init Systems](guides/init-systems.md)
- [File Management](guides/file-management.md)
- [Build from Source](guides/build-from-source.md)

#### Features
- [Snapshot CLI](guides/features/snapshot-cli.md) - VM snapshot commands
- [Snapshot Complete](guides/features/snapshot-complete.md) - Full workflow (v1.14.x+)

#### Networking
- [Swarm Networking](guides/networking/swarm-networking.md) - VXLAN, bridges, cross-node

#### Security
- [Security Hardening](guides/security/security-hardening.md) - Production best practices

### 🔷 SwarmKit Integration
Using SwarmKit with SwarmCracker.

- [Overview](guides/swarmkit/overview.md)
- [User Guide](guides/swarmkit/user-guide.md)
- [Quick Start](guides/swarmkit/quick-start.md)
- [Comprehensive Deployment](guides/swarmkit/deployment-comprehensive.md)

### 🏗️ Architecture
Technical design and implementation details.

- [System Design](architecture/system.md)
- [SwarmKit Integration](architecture/swarmkit-integration.md)
- [Jailer Design](architecture/jailer-design.md) - Firecracker isolation
- [Jailer Status](architecture/jailer-status.md) - Implementation progress
- [VXLAN Design](architecture/vxlan-design.md) - Overlay networking

### 💻 Development
For contributors and developers.

- [Getting Started](development/getting-started.md)
- [Testing Guide](development/testing.md)
- [Secrets Prevention](development/secrets-prevention.md)
- [Conventions](CONVENTIONS.md)

### 🧪 Testing
Testing strategies and results.

- [Strategy](testing/strategy.md)
- [Test Summary](testing/test-summary.md) ⭐ **All tests passing**
- [Manual Test Results](testing/manual-test-results.md)
- [Jailer Tests](testing/jailer-tests.md)
- [Unit Tests](testing/unit.md)
- [Integration Tests](testing/integration.md)
- [E2E Tests](testing/e2e.md)

### 🤖 Infrastructure Automation
Ansible automation for cluster deployment.

- [Ansible README](../infrastructure/ansible/README.md)
- [Install Automation](getting-started/install-automation.md)

---

## 🔍 Search Documentation

Looking for something specific? Try searching for:

- `install` - Installation guides
- `snapshot` - VM snapshot features
- `jailer` - Security isolation
- `network` - Networking configuration
- `vxlan` - Overlay networks
- `security` - Security hardening
- `test` - Testing guides
- `rolling` - Rolling updates

---

## 📊 Documentation Statistics

| Category | Files | Description |
|----------|-------|-------------|
| Getting Started | 7 | Installation and first steps |
| User Guides | 14 | How-to guides (core + features + networking + security) |
| SwarmKit | 4 | SwarmKit integration guides |
| Architecture | 5 | Technical design documents |
| Development | 4 | Developer guides |
| Testing | 7 | Test strategies and results |

---

## 🔗 Quick Links

- **[GitHub Repository](https://github.com/restuhaqza/SwarmCracker)**
- **[Releases](https://github.com/restuhaqza/SwarmCracker/releases)**
- **[Issues](https://github.com/restuhaqza/SwarmCracker/issues)**

---

## 📝 Contributing to Documentation

We welcome documentation contributions! Please:

1. Follow the existing structure
2. Use clear, concise language
3. Include code examples
4. Add troubleshooting sections
5. Update this index when adding new files

See [CONTRIBUTING.md](../CONTRIBUTING.md) for more details.

---

**Last Updated:** 2026-04-08  
**Documentation Version:** 1.2  
**SwarmCracker Version:** v0.2.1