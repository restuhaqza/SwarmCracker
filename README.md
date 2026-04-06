<div align="center">

# 🔥 SwarmCracker

**Firecracker MicroVMs with SwarmKit Orchestration**

[![Go Report Card](https://goreportcard.com/badge/github.com/restuhaqza/swarmcracker)](https://goreportcard.com/report/github.com/restuhaqza/swarmcracker)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/restuhaqza/swarmcracker)](https://github.com/restuhaqza/SwarmCracker/releases)

</div>

---

SwarmCracker is a custom executor for [SwarmKit](https://github.com/moby/swarmkit) that runs containers as isolated [Firecracker](https://github.com/firecracker-microvm/firecracker) microVMs.

## Why SwarmCracker?

| Feature | Benefit |
|---------|---------|
| 🔥 **MicroVM Isolation** | Each container gets its own kernel via KVM |
| 🐳 **SwarmKit Orchestration** | Services, scaling, rolling updates, secrets |
| 🛡️ **Hardware Security** | KVM virtualization, not just namespaces |
| ⚡ **Fast Startup** | MicroVMs boot in milliseconds |
| 🌐 **VM Networking** | Bridge + VXLAN for cross-node communication |
| 🔄 **Zero-Downtime Updates** | Rolling updates with health monitoring |

## Architecture

```
SwarmKit Manager → swarmd-firecracker Agent → Firecracker VMM → MicroVM
```

Each worker runs `swarmd-firecracker` which translates SwarmKit tasks into Firecracker microVM configurations.

## Quick Start

### One-Line Install

The installer downloads the latest release, installs binaries, and guides you through manager or worker setup:

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash
```

You'll be prompted to choose:
- **Manager** — initializes a SwarmKit cluster and prints the worker join command
- **Worker** — connects to an existing cluster (needs manager IP + join token)
- **Skip** — install binaries only

**Non-interactive (automation/SSH):**
```bash
# Install and configure as worker in one shot
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash -s -- \
  --worker \
  --manager 192.168.1.10:4242 \
  --token SWMTKN-1-xxxxx
```

**Full CLI flags:**
```
--worker         Set up as worker node
--manager ADDR   Manager address (required with --worker)
--token TOKEN    Join token (required with --worker)
--hostname NAME  Node hostname
--bridge NAME    Bridge name (default: swarm-br0)
--subnet CIDR    Subnet (default: 192.168.127.0/24)
--bridge-ip IP   Bridge IP (default: 192.168.127.1/24)
--state-dir DIR  State directory
--kernel-path    Kernel path
--rootfs-dir DIR Rootfs directory
--install-dir    Binary install dir (default: /usr/local/bin)
```

### Prerequisites

- Linux with KVM support (`ls /dev/kvm`)
- Firecracker v1.14+ (auto-installed by the script if missing)
- Go 1.24+ (only needed for building SwarmKit tools from source)

### Build from Source

```bash
git clone https://github.com/restuhaqza/swarmcracker.git
cd swarmcracker
make all
```

### Deploy

**1. Start SwarmKit manager:**
```bash
swarmd -d /tmp/manager --listen-control-api /tmp/manager/swarm.sock \
  --hostname manager --listen-remote-api 0.0.0.0:4242
```

**2. Get join token:**
```bash
swarmctl --socket /tmp/manager/swarm.sock cluster inspect default
```

**3. Start worker with Firecracker executor:**
```bash
swarmd-firecracker \
  --hostname worker-1 \
  --join-addr <manager-ip>:4242 \
  --join-token <WORKER_TOKEN> \
  --kernel-path /usr/share/firecracker/vmlinux \
  --rootfs-dir /var/lib/firecracker/rootfs \
  --bridge-name swarm-br0 \
  --subnet 192.168.127.0/24
```

**4. Deploy a service:**
```bash
swarmctl --socket /tmp/manager/swarm.sock service create --name nginx --image nginx:alpine
```

## Documentation

| Guide | Description |
|-------|-------------|
| [Installation](docs/guides/installation.md) | Full setup instructions |
| [Networking](docs/guides/networking.md) | Bridge, TAP, VXLAN configuration |
| [VXLAN Overlay](docs/vxlan-overlay.md) | Cross-node VM communication |
| [SwarmKit Deployment](docs/guides/swarmkit/deployment-comprehensive.md) | Production setup |
| [CLI Reference](docs/cli-reference.md) | Full CLI documentation |
| [Architecture](docs/architecture/system.md) | System design details |

## Download

Pre-built binaries available for Linux and macOS:

```bash
# Download from GitHub Releases
curl -LO https://github.com/restuhaqza/SwarmCracker/releases/download/v0.1.0/swarmcracker-v0.1.0-linux-amd64.tar.gz
tar xzf swarmcracker-v0.1.0-linux-amd64.tar.gz
```

[See all releases →](https://github.com/restuhaqza/SwarmCracker/releases)

## License

Apache 2.0 - See [LICENSE](LICENSE)