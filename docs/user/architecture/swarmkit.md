# Architecture

SwarmCracker takes SwarmKit tasks and runs them as Firecracker microVMs instead of regular containers.

---

## How It Works

SwarmKit schedules tasks. SwarmCracker's executor receives those tasks and turns each one into a Firecracker VM.

```
User
 │
 │ swarmctl CLI
 │
 ▼
Manager (swarmd-firecracker)
 │
 │ SwarmKit: schedules, Raft consensus
 │ gRPC
 │
┌┴────────────────────────────┐
│                             │
Worker-1                    Worker-2
swarmd-firecracker          swarmd-firecracker
    │                           │
    ▼                           ▼
swarm-br0                   swarm-br0
┌───┐┌───┐                  ┌───┐┌───┐
│VM ││VM │  ←── VXLAN ───→  │VM ││VM │
└───┘└───┘                  └───┘└───┘
```

---

## The Pieces

### Manager Node

Runs `swarmd-firecracker` in manager mode. It handles:

- **Raft consensus** — Keeps cluster state consistent across managers if you have multiple
- **Task scheduling** — Decides which worker runs each task
- **IPAM** — Assigns overlay network IPs to tasks
- **TLS** — Manages certificates for secure communication

### Worker Node

Runs `swarmd-firecracker` in worker mode. It:

- **Executes tasks** — Receives assignments from manager
- **Creates VMs** — Starts Firecracker with the right config
- **Attaches networking** — TAP devices to bridge, VXLAN for cross-node
- **Reports status** — Tells manager if VMs are healthy

### Firecracker

The actual VMM. Each VM is:

- Isolated with its own kernel
- Connected via TAP device
- Has its own IP on the overlay network

### Consul

Used for VXLAN peer discovery. Workers register their overlay IPs, and other workers learn about them through `WatchPeers()`. This populates the VXLAN forwarding database.

### Bridge (swarm-br0)

Linux bridge that connects all local TAP devices. VMs on the same worker talk through this.

### VXLAN (swarm-br0-vxlan)

Overlay network for cross-node communication. Encapsulates traffic in UDP packets to the underlay network.

---

## Network Flow

1. Manager schedules a task to Worker-1
2. IPAM assigns an overlay IP (like 192.168.127.105)
3. Worker-1 creates a TAP device, attaches to swarm-br0
4. Firecracker starts with the IP in kernel boot args
5. Worker registers in Consul
6. Other workers see the new peer, update VXLAN FDB
7. VMs can now talk across nodes via VXLAN tunnel

---

## What Makes This Different from Docker

| Docker | SwarmCracker |
|--------|--------------|
| Containers share kernel | Each VM has its own kernel |
| Namespaces for isolation | KVM virtualization |
| Process-level security | Hardware-level isolation |
| Fast startup (ms) | Fast startup (~100ms) |
| Shared cgroups | Per-VM resources |

The isolation is stronger. A compromised VM can't see other VMs' processes or memory the way a compromised container might.

---

## Code Layout

```
cmd/
├── swarmctl/         # CLI tool
├── swarmd-firecracker/  # Main daemon
├── swarmcracker/     # High-level CLI wrapper

pkg/
├── executor/         # Firecracker executor for SwarmKit
├── network/          # Bridge, TAP, VXLAN management
├── discovery/        # Consul peer discovery
├── swarmkit/         # SwarmKit integration
├── image/            # OCI image extraction
├── lifecycle/        # VM lifecycle management
├── jailer/           # Security sandboxing
├── storage/          # Volumes, secrets
```

---

## See Also

- [Getting Started](../getting-started/) — Set up a cluster
- [Networking](../guides/networking.md) — VXLAN details
- [SwarmKit Integration](swarmkit.md) — How tasks flow