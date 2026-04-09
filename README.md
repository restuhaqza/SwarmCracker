<div align="center">

# SwarmCracker

Firecracker MicroVMs with SwarmKit Orchestration

[![Go Report Card](https://goreportcard.com/badge/github.com/restuhaqza/swarmcracker)](https://goreportcard.com/report/github.com/restuhaqza/swarmcracker)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/restuhaqza/swarmcracker)](https://github.com/restuhaqza/SwarmCracker/releases)

</div>

---

Custom executor for [SwarmKit](https://github.com/moby/swarmkit) that runs containers as isolated [Firecracker](https://github.com/firecracker-microvm/firecracker) microVMs.

## Features

| | |
|--|--|
| MicroVM isolation | Each container gets its own kernel via KVM |
| SwarmKit orchestration | Services, scaling, rolling updates |
| Hardware security | KVM virtualization, not just namespaces |
| Fast startup | MicroVMs boot in milliseconds |
| Cross-node networking | Bridge + VXLAN overlay |
| Zero-downtime updates | Rolling updates with health checks |

## Architecture

```
SwarmKit Manager → swarmd-firecracker → Firecracker VMM → MicroVM
```

Workers run `swarmd-firecracker`, translating SwarmKit tasks into Firecracker configs.

## Quick Start

### Initialize Manager

```bash
sudo swarmcracker init

# Or specify IP
sudo swarmcracker init --advertise-addr 192.168.1.10:4242
```

### Get Join Token

```bash
sudo cat /var/lib/swarmkit/join-tokens.txt
```

### Join Workers

```bash
sudo swarmcracker join 192.168.1.10:4242 --token SWMTKN-1-...
```

See [Getting Started](docs/getting-started/) for cluster setup options.

---

### One-Line Install

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash
```

Options: Manager (init cluster), Worker (join existing), Skip (binaries only).

Non-interactive:
```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash -s -- \
  --worker --manager 192.168.1.10:4242 --token SWMTKN-1-xxxxx
```

---

### Prerequisites

- Linux with KVM (`ls /dev/kvm`)
- Firecracker v1.14+ (auto-installed)
- Go 1.24+ (build only)

### Build

```bash
git clone https://github.com/restuhaqza/swarmcracker.git
cd swarmcracker
make all
```

### Deploy

```bash
swarmcracker deploy nginx:alpine --hosts worker-1,worker-2
swarmcracker run nginx:alpine  # local
swarmcracker status
```

---

### Manual Setup

```bash
# Manager
swarmd -d /tmp/manager --listen-control-api /tmp/manager/swarm.sock \
  --hostname manager --listen-remote-api 0.0.0.0:4242

# Token
swarmctl --socket /tmp/manager/swarm.sock cluster inspect default

# Worker
swarmd-firecracker --hostname worker-1 \
  --join-addr <manager>:4242 --join-token <TOKEN> \
  --kernel-path /usr/share/firecracker/vmlinux \
  --rootfs-dir /var/lib/firecracker/rootfs

# Service
swarmctl --socket /tmp/manager/swarm.sock service create --name nginx --image nginx:alpine
```

## Documentation

| | |
|--|--|
| [Installation](docs/getting-started/installation.md) | Setup |
| [Networking](docs/guides/networking.md) | Bridge, VXLAN |
| [SwarmKit](docs/guides/swarmkit/user-guide.md) | Orchestration |
| [Architecture](docs/architecture/system.md) | Design |

## Download

```bash
curl -LO https://github.com/restuhaqza/SwarmCracker/releases/download/v0.6.0/swarmcracker-v0.6.0-linux-amd64.tar.gz
tar xzf swarmcracker-v0.6.0-linux-amd64.tar.gz
```

[Releases](https://github.com/restuhaqza/SwarmCracker/releases)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache 2.0