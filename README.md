# SwarmCracker 🔥

[![Go Report Card](https://goreportcard.com/badge/github.com/restuhaqza/swarmcracker)](https://goreportcard.com/report/github.com/restuhaqza/swarmcracker)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

> **Firecracker microVMs meet SwarmKit orchestration**

SwarmCracker is a custom executor for [SwarmKit](https://github.com/moby/swarmkit) that runs containers as isolated [Firecracker](https://github.com/firecracker-microvm/firecracker) microVMs instead of traditional containers.

## 🎯 What It Does

SwarmCracker lets you use Docker Swarm's familiar orchestration features with hardware-isolated microVMs:

- ✅ **Run services as microVMs** - Each container gets its own kernel
- ✅ **Full Swarm compatibility** - Services, scaling, rolling updates, secrets
- ✅ **Strong isolation** - KVM-based hardware virtualization
- ✅ **No Kubernetes needed** - Keep Swarm's simplicity

## 🏗️ Architecture

![SwarmCracker Architecture](docs/architecture.png)

```
SwarmKit (Orchestration)
         │
         ▼
SwarmCracker Executor
    │           │
    ▼           ▼
Firecracker  OCI Images
   VMM         ↓
    │      Root Filesystem
    ▼
   MicroVM (Isolated)
```

## 🚀 Quick Start

### Prerequisites

- Linux with KVM support
- Go 1.21+
- Firecracker v1.0.0+
- Docker Swarm (or SwarmKit standalone)

### Installation

```bash
# Clone the repo
git clone https://github.com/restuhaqza/swarmcracker.git
cd swarmcracker

# Build
make build

# Install
make install
```

### Usage

```bash
# Start SwarmKit agent with SwarmCracker executor
swarmd \
  --executor firecracker \
  --firecracker-kernel /usr/share/firecracker/vmlinux \
  --firecracker-rootfs /var/lib/firecracker/rootfs
```

## 📖 Documentation

- **[Architecture Overview](docs/ARCHITECTURE.md)** - Detailed system design and component overview
- **[Installation Guide](docs/INSTALL.md)** - Step-by-step setup instructions
- **[Configuration Reference](docs/CONFIG.md)** - Complete configuration options and examples
- **[Testing Guide](docs/TESTING.md)** - How to run and write tests
- **[Development Guide](docs/DEVELOPMENT.md)** - Contributing and development workflow

### Quick Links

- [Prerequisites](docs/INSTALL.md#prerequisites)
- [Quick Start](docs/INSTALL.md#installation-methods)
- [Configuration Examples](docs/CONFIG.md#examples)
- [Writing Tests](docs/TESTING.md#writing-tests)
- [Contributing](docs/DEVELOPMENT.md#development-workflow)

## 🛠️ Configuration

SwarmCracker uses a simple YAML configuration file:

```yaml
# /etc/swarmcracker/config.yaml
executor:
  name: firecracker
  kernel_path: "/usr/share/firecracker/vmlinux"
  initrd_path: "/usr/share/firecracker/initrd.img"
  rootfs_dir: "/var/lib/firecracker/rootfs"
  socket_dir: "/var/run/firecracker"
  default_vcpus: 2
  default_memory_mb: 1024
  enable_jailer: true
  jailer:
    uid: 1000
    gid: 1000
    chroot_base_dir: "/srv/jailer"
network:
  bridge_name: "swarm-br0"
  enable_rate_limit: true
  max_packets_per_sec: 10000
```

## 🔧 Development

```bash
# Run tests
make test

# Run linting
make lint

# Build examples
make examples
```

## 📊 Status

Current version: **v0.1.0-alpha** (Proof of Concept)

**Test Coverage:** 63.3% overall

### Components Status

- [x] Executor interface implementation (95.2% coverage)
- [x] Task translator (SwarmKit → Firecracker) (98.1% coverage)
- [x] Configuration system (87.3% coverage)
- [x] VM lifecycle manager (54.4% coverage)
- [x] Image preparation layer (implementation complete, tests pending)
- [x] Network integration (9.1% coverage, limited by system requirements)
- [ ] Security hardening with jailer (code ready, needs testing)
- [ ] End-to-end integration testing
- [ ] Production deployment and testing

## 🤝 Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## 📝 License

Apache License 2.0 - see [LICENSE](LICENSE) for details.

## 🙏 Acknowledgments

- [SwarmKit](https://github.com/moby/swarmkit) - Orchestration engine
- [Firecracker](https://github.com/firecracker-microvm/firecracker) - MicroVM technology
- [firecracker-containerd](https://github.com/firecracker-microvm/firecracker-containerd) - Reference for container integration

## 🔗 Links

- [SwarmKit GitHub](https://github.com/moby/swarmkit)
- [Firecracker GitHub](https://github.com/firecracker-microvm/firecracker)
- [Project Documentation](docs/)

---

**Made with 🔥 by Restu Muzakir**
