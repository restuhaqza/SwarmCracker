# Getting Started with SwarmCracker

Choose your deployment scenario:

## 🚀 Quick Start Options

### [⭐ Full Installation Guide](installation.md)
**Best for:** First-time setup, production deployment
- **Covers:** One-line install, manual install, Vagrant, networking, troubleshooting
- **From scratch to running cluster**
- **Start here if you're new to SwarmCracker**

### [Local Development](local-dev.md)
**Best for:** Development, testing, learning
- **Requirements:** Local machine with KVM
- **Time:** 10-15 minutes
- **Isolation:** Single-machine testing
- **Use case:** Quick experiments, development workflow

### [Vagrant VirtualBox](vagrant.md)
**Best for:** Multi-node cluster testing
- **Requirements:** VirtualBox, Vagrant
- **Time:** 20-30 minutes
- **Isolation:** VM-based cluster (1 manager + N workers)
- **Use case:** Realistic cluster testing, demos

### [Firecracker VMs](firecracker-vm.md)
**Best for:** Production-like microVM deployment
- **Requirements:** Linux host with KVM, systemd
- **Requirements:** Firecracker v1.14+, proper kernel
- **Time:** 30-45 minutes
- **Isolation:** Full microVM isolation
- **Use case:** Production clusters, security testing

### [DigitalOcean](digitalocean.md)
**Best for:** Cloud deployment
- **Requirements:** DigitalOcean account, API token
- **Time:** 40-60 minutes
- **Isolation:** Cloud VMs with full networking
- **Use case:** Production cloud deployment

---

## 📋 Comparison

| Method | Complexity | Isolation | Cost | Speed |
|--------|-----------|-----------|------|-------|
| Local Dev | ⭐ Easy | Process | Free | ⚡ Fastest |
| Vagrant | ⭐⭐ Medium | VM | Free | 🚀 Fast |
| Firecracker VMs | ⭐⭐⭐ Hard | MicroVM | Free | 🐢 Slowest |
| DigitalOcean | ⭐⭐ Medium | Cloud VM | $$ | 🚀 Fast |

---

## 🔧 Prerequisites

All methods require:
- **Go 1.21+** - For building from source
- **Linux host** - KVM support required
- **Root/sudo** - For system configuration

### Check KVM Support
```bash
# Check if KVM is available
ls /dev/kvm

# Check if your CPU supports virtualization
lscpu | grep Virtualization
```

---

## 🎯 Recommendation

**First time?** → Read the **[Full Installation Guide](installation.md)** ⭐

**Just starting?** → Begin with **Local Development**

**Want a cluster?** → Use **Vagrant VirtualBox**

**Production testing?** → Deploy **Firecracker VMs**

**Cloud deployment?** → Use **DigitalOcean**

---

## 📚 Next Steps

After setup, see:
- [Configuration Guide](../guides/configuration.md)
- [Networking Guide](../guides/networking.md)
- [SwarmKit Deployment](../guides/swarmkit/deployment.md)
- [User Guide](../guides/swarmkit/user-guide.md)
