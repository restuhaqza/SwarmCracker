# SwarmCracker Documentation

Complete documentation for SwarmCracker - Firecracker MicroVMs with SwarmKit Orchestration.

---

## 📖 Quick Start

**New to SwarmCracker?** Start here:

1. **[Installation Guide](docs/getting-started/installation.md)** - Complete setup instructions
2. **[Quick Start](docs/guides/swarmkit/quick-start.md)** - Deploy your first microVM in 5 minutes
3. **[Local Development](docs/getting-started/local-dev.md)** - Single-node testing setup

---

## 📚 Documentation Structure

### Getting Started
- **[Overview](docs/getting-started/README.md)** - Introduction and prerequisites
- **[Installation Guide](docs/getting-started/installation.md)** - Comprehensive installation (953 lines)
- **[Local Development](docs/getting-started/local-dev.md)** - Single-node setup
- **[Vagrant Setup](docs/getting-started/vagrant.md)** - VM-based testing
- **[DigitalOcean](docs/getting-started/digitalocean.md)** - Cloud deployment
- **[Firecracker VM](docs/getting-started/firecracker-vm.md)** - Manual VM setup

### User Guides
- **[Configuration](docs/guides/configuration.md)** - Configuration options and best practices
- **[Networking](docs/guides/networking.md)** - Network setup and VXLAN overlay
- **[Security Hardening](docs/guides/security-hardening.md)** - Security best practices
- **[Rolling Updates](docs/guides/rolling-updates.md)** - Zero-downtime deployments (953 lines)
- **[Multi-Arch Support](docs/guides/multi-arch-support.md)** - AMD64 + ARM64 support (464 lines)
- **[Init Systems](docs/guides/init-systems.md)** - Tini, dumb-init configuration
- **[File Management](docs/guides/file-management.md)** - Volume and file handling

### SwarmKit Guides
- **[Overview](docs/guides/swarmkit/overview.md)** - SwarmKit integration overview
- **[User Guide](docs/guides/swarmkit/user-guide.md)** - Complete SwarmKit usage
- **[Deployment](docs/guides/swarmkit/deployment-comprehensive.md)** - Comprehensive deployment guide
- **[Quick Start](docs/guides/swarmkit/quick-start.md)** - Quick SwarmKit setup

### Architecture
- **[System Design](docs/architecture/system.md)** - System architecture overview
- **[SwarmKit Integration](docs/architecture/swarmkit-integration.md)** - SwarmKit integration details
- **[VXLAN Overlay](docs/vxlan-overlay.md)** - VXLAN networking implementation

### Development
- **[Getting Started](docs/development/getting-started.md)** - Development environment setup
- **[Testing Guide](docs/development/testing.md)** - Testing strategies and procedures
- **[Secrets Prevention](docs/development/secrets-prevention.md)** - Secret management

### Testing
- **[Strategy](docs/testing/strategy.md)** - Testing strategy overview
- **[Unit Tests](docs/testing/unit.md)** - Unit testing guide
- **[Integration Tests](docs/testing/integration.md)** - Integration testing
- **[E2E Tests](docs/testing/e2e.md)** - End-to-end testing

### Infrastructure & Automation
- **[Ansible Automation](infrastructure/ansible/README.md)** - Ansible deployment automation
- **[Test Report](infrastructure/ansible/test-report.md)** - VirtualBox test results
- **[Verification Report](infrastructure/ansible/verification-report.md)** - Ansible verification
- **[Ansible Testing](test-automation/README.ansible-testing.md)** - Fresh Ubuntu VM testing

### Examples
- **[Local Dev](examples/local-dev/README.md)** - Local development examples
- **[Production Cluster](examples/production-cluster/README.md)** - Production deployment examples

---

## 🔧 Ansible Automation

### Quick Commands

```bash
cd infrastructure/ansible

# Deploy complete cluster
ansible-playbook -i inventory/virtualbox-fresh site.yml -v

# Deploy manager only
ansible-playbook -i inventory/virtualbox-fresh setup-manager.yml -v

# Deploy workers only
ansible-playbook -i inventory/virtualbox-fresh setup-worker.yml -v

# Test cluster
ansible-playbook -i inventory/virtualbox-fresh test-cluster.yml
```

### Inventory Files

| Inventory | Purpose |
|-----------|---------|
| `inventory/production/hosts` | Production cluster |
| `inventory/staging/hosts` | Staging cluster |
| `inventory/virtualbox/hosts` | Vagrant VMs (with provisioning) |
| `inventory/virtualbox-fresh/hosts` | Fresh Ubuntu VMs (Ansible testing) |

---

## 📊 Project Status

| Phase | Status | Features |
|-------|--------|----------|
| Phase 1 | ✅ Complete | Graceful shutdown, resource reporting, rootfs cleanup, VXLAN discovery |
| Phase 2 | ✅ Complete | Health checks, metrics, volumes, credential store |
| Phase 3 | ✅ Complete | Rolling updates, status reporting |
| Phase 4 | ✅ Complete | Multi-architecture (amd64/arm64) |
| Phase 5 | ✅ Complete | Ansible automation for cluster deployment |
| Phase 6 | 📋 Pending | Auto-scaling, jailer, BGP/EVPN, web dashboard |

---

## 🚀 Release Information

**Current Release:** v0.2.1  
**Release Date:** 2026-04-05  
**Supported Platforms:** Linux amd64, Linux arm64

### Release Notes
- [v0.2.1 Release](https://github.com/restuhaqza/SwarmCracker/releases/tag/v0.2.1) - Release pipeline fix
- [v0.2.0 Release](https://github.com/restuhaqza/SwarmCracker/releases/tag/v0.2.0) - Complete Phase 1-4
- [v0.1.0 Release](https://github.com/restuhaqza/SwarmCracker/releases/tag/v0.1.0) - Initial release

---

## 🔗 External Resources

- **GitHub Repository:** https://github.com/restuhaqza/SwarmCracker
- **Firecracker:** https://github.com/firecracker-microvm/firecracker
- **SwarmKit:** https://github.com/moby/swarmkit
- **OpenClaw:** https://docs.openclaw.ai

---

## 📝 Documentation Maintenance

### Adding New Documentation

1. Create `.md` file in appropriate directory
2. Add link to this index
3. Update relevant README files
4. Commit with descriptive message

### Documentation Standards

- Use clear, concise language
- Include code examples where applicable
- Add troubleshooting sections
- Keep guides up-to-date with latest release
- Use consistent formatting (headers, code blocks, tables)

---

**Last Updated:** 2026-04-06  
**Maintained By:** SwarmCracker Team
