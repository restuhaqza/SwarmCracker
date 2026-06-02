<div align="center">

# SwarmCracker

Firecracker MicroVMs with SwarmKit Orchestration

[![Go Report Card](https://goreportcard.com/badge/github.com/restuhaqza/swarmcracker)](https://goreportcard.com/report/github.com/restuhaqza/swarmcracker)
[![Coverage](https://codecov.io/gh/restuhaqza/swarmcracker/branch/main/graph/badge.svg)](https://codecov.io/gh/restuhaqza/swarmcracker)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/restuhaqza/swarmcracker)](https://github.com/restuhaqza/SwarmCracker/releases)

</div>

---

Imagine running containers, but with actual VMs instead. That's SwarmCracker — SwarmKit tasks become Firecracker microVMs, each with its own kernel and hardware-level isolation.

## Why You'll Like It

| Feature | What it means for you |
|---------|----------------------|
| Per-VM kernel | Real isolation — not just namespace walls |
| SwarmKit compatible | Everything you know from Docker Swarm just works |
| Hardware virtualization | KVM gives you actual VM security |
| Fast boot | MicroVMs boot in ~100ms |
| Cross-node networking | VXLAN connects your VMs across the whole cluster |
| Rolling updates | Deploy without taking anything offline |

## Architecture

![SwarmCracker Architecture](docs/architecture/swarmcracker-architecture.svg)

| Piece | What it does |
|------|-------------|
| Manager | Runs SwarmKit, schedules tasks, manages cluster state |
| Worker | Launches Firecracker VMs via swarmd-firecracker |
| Consul | VXLAN peer discovery across nodes |
| swarm-br0 | Local bridge for VM networking |
| VXLAN (ID 100) | Cross-node overlay on UDP 4789 |
| MicroVMs | Workloads with per-VM kernel isolation |

## Quick Start

### Prerequisites

- Linux with KVM (`ls /dev/kvm`)
- Firecracker v1.15+ (install script handles this)
- Go 1.26+ (build from source only)

### One-Line Install

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash
```

### Initialize a Cluster

```bash
# On the manager node
sudo swarmcracker cluster init --advertise-addr 192.168.1.10:4242

# Get join token
sudo swarmcracker cluster token create --role worker

# On each worker
sudo swarmcracker cluster join --token SWMTKN-1-xxx 192.168.1.10:4242
```

### Deploy Your First Service

```bash
# Deploy nginx across the cluster
swarmcracker service create --name web --replicas 3 -p 8080:80 nginx:alpine

# Verify
swarmcracker service list
swarmcracker service ps web
```

### Building From Source

```bash
git clone https://github.com/restuhaqza/swarmcracker.git
cd swarmcracker
make all
```

## Documentation

📖 **Full docs:** [swarmcracker.restuhaqza.dev](https://swarmcracker.restuhaqza.dev)

| Guide | What's inside |
|-------|---------------|
| [Getting Started](docs/user/getting-started/README.md) | Setup walkthrough, step by step |
| [Architecture](docs/architecture/overview.md) | Component overview and data flow |
| [CLI Reference](docs/user/reference/cli.md) | Every command, every flag |
| [Configuration](docs/user/guides/configuration.md) | All config keys and defaults |
| [Networking](docs/user/guides/networking.md) | VXLAN, bridges, how VMs communicate |
| [Operations](docs/user/guides/operations.md) | Monitoring, backup, troubleshooting |
| [Security](docs/dev/security.md) | Hardening, jailer, seccomp |

## Production Readiness: 10/10

- ✅ 87% test coverage (15 packages)
- ✅ 70/70 E2E tests passing
- ✅ All 47 security/code review items addressed
- ✅ Go 1.26, 8 linters, go vet clean
- ✅ Hardened builds (PIE, stripped symbols, trimmed paths)

## Security

SwarmCracker takes security seriously. Every workload runs in a hardware-isolated Firecracker microVM with:

- **KVM isolation** — separate kernel per VM
- **Jailer sandbox** — chroot + UID/GID drop + network namespace
- **Seccomp filtering** — privileged syscalls blocked in guest
- **Path traversal prevention** — validated task IDs, secrets, mounts
- **Command injection prevention** — PID-file signaling, no shell interpolation
- **Hardened builds** — PIE binaries with stripped symbols

Read our [Security Policy](SECURITY.md) and [Security Guide](docs/dev/security.md).

## Releases

```bash
curl -LO https://github.com/restuhaqza/SwarmCracker/releases/download/v0.7.0/swarmcracker-v0.7.0-linux-amd64.tar.gz
tar xzf swarmcracker-v0.7.0-linux-amd64.tar.gz
```

[All releases](https://github.com/restuhaqza/SwarmCracker/releases)

## Contributing

Check out [CONTRIBUTING.md](CONTRIBUTING.md) — we'd love your help!

## License

Apache 2.0
