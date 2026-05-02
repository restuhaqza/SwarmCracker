<div align="center">

# SwarmCracker

Firecracker MicroVMs with SwarmKit Orchestration

[![Go Report Card](https://goreportcard.com/badge/github.com/restuhaqza/swarmcracker)](https://goreportcard.com/report/github.com/restuhaqza/swarmcracker)
[![Coverage](https://codecov.io/gh/restuhaqza/swarmcracker/branch/main/graph/badge.svg)](https://codecov.io/gh/restuhaqza/swarmcracker)
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

![SwarmCracker Architecture](docs/architecture/swarmcracker-architecture.svg)

### Components

| Component | Role |
|-----------|------|
| **Manager Node** | SwarmKit Raft consensus, IPAM, task scheduling |
| **Worker Nodes** | swarmd-firecracker executor, Firecracker VMM |
| **Consul** | Service discovery for VXLAN peer registration |
| **swarm-br0** | Local bridge connecting TAP devices |
| **swarm-br0-vxlan** | VXLAN overlay for cross-node communication |
| **MicroVMs** | Isolated Firecracker VMs with overlay IPs |

### Network Flow

```
SwarmKit Manager → swarmd-firecracker → Firecracker VMM → MicroVM
                         ↓
                    swarm-br0 (bridge)
                         ↓
                 swarm-br0-vxlan (VXLAN ID 100)
                         ↓
                 Consul → FDB Entries → Remote Worker
```

Workers run `swarmd-firecracker`, translating SwarmKit tasks into Firecracker configs.

### VXLAN Cross-Node Communication

SwarmCracker uses VXLAN overlay networking for communication between MicroVMs on different worker nodes:

- **Overlay Network**: `192.168.127.0/24`
- **Underlay Network**: Physical host IPs (`192.168.121.x`)
- **VXLAN ID**: 100
- **UDP Port**: 4789
- **Service Discovery**: Consul `WatchPeers()` for dynamic FDB updates

#### How It Works

1. **Task Scheduled** → Manager assigns task to worker
2. **IPAM Allocation** → Overlay IP assigned (e.g., `192.168.127.105`)
3. **MicroVM Start** → Firecracker VM boots with IP in kernel args
4. **TAP Attachment** → TAP device connected to `swarm-br0`
5. **Consul Registration** → Worker registers VXLAN service
6. **Peer Discovery** → `WatchPeers()` populates VXLAN FDB entries
7. **Cross-Node Traffic** → Packets flow via VXLAN UDP tunnel

#### Verified Test Results (2026-05-02)

| Test | Result |
|------|--------|
| Worker1 → Worker2 VM | ✅ 10/10 packets, 0% loss, 4-8ms |
| Worker2 → Worker1 VM | ✅ 5/5 packets, 0% loss, 3-11ms |
| Consul Registration | ✅ All 3 nodes registered |
| VXLAN FDB Entries | ✅ 6 flood destinations (2 per node) |

#### Key Implementation Details

| File | Purpose |
|------|--------|
| `pkg/swarmkit/executor.go` | Local IP detection for Consul registration |
| `pkg/network/vxlan.go` | `fdb append` for multiple flood destinations |
| `pkg/discovery/consul.go` | Catalog query (bypass TCP health checks) |

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

See [Getting Started](docs/user/getting-started/) for cluster setup options.

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
- Go 1.25+ (build only)

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
| [Getting Started](docs/user/getting-started/) | Setup |
| [Networking](docs/user/guides/networking.md) | Bridge, VXLAN |
| [SwarmKit](docs/user/guides/swarmkit.md) | Orchestration |
| [Architecture](docs/user/architecture/) | Design |
| [CLI Reference](docs/user/reference/cli.md) | Commands |

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