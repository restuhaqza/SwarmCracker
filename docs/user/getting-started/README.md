# Getting Started

You'll need a machine with KVM. If you can run `ls /dev/kvm` and see a file, you're good.

---

## What You Need

### Hardware

Manager node can be small — it mostly coordinates things. Workers need more since they actually run VMs.

| Node | Minimum | Works Better |
|------|---------|--------------|
| Manager | 1 vCPU, 1 GB | 2 vCPU, 2 GB |
| Worker | 2 vCPU, 4 GB | 4 vCPU, 8 GB |

### Software

Ubuntu 20.04+ or Debian 11+ work well. Any KVM-compatible distro should do.

You need root access for setting up bridges and Firecracker.

### Check KVM

```bash
ls -la /dev/kvm                    # Must show a file
lscpu | grep Virtualization        # VT-x (Intel) or AMD-V (AMD)
```

If you're running inside a VM (like a Vagrant box), nested virtualization has to be on:

```bash
cat /sys/module/kvm_intel/parameters/nested  # Should be 'Y'

# If it's 'N':
sudo modprobe kvm_intel nested=1
```

---

## Install

### One-Line Install (blessed path — ADR-005)

`install.sh` only downloads the latest release binary and verifies its checksum.
Everything else is handled by the `swarmcracker setup` subcommand:

```bash
# 1. Install the binary
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash

# 2. Verify prerequisites (KVM, kernel modules, tools)
sudo swarmcracker setup check

# 3. Install Firecracker, jailer, kernel, and rootfs
sudo swarmcracker setup install --download-kernel --download-rootfs

# 4. Create the VM bridge + enable NAT
sudo swarmcracker setup network

# 5. Generate the config
sudo swarmcracker setup config --non-interactive
```

Repeat steps 2–5 on every node (manager and workers). Then start the cluster:

```bash
# On the manager node
sudo swarmcracker cluster init --advertise-addr <MANAGER_IP>:4242

# On each worker node
sudo swarmcracker cluster join --token <TOKEN> <MANAGER_IP>:4242
```

### Build It Yourself

```bash
git clone https://github.com/restuhaqza/SwarmCracker
cd SwarmCracker
make build
sudo make install
```

### The Kernel Thing

Firecracker needs an uncompressed ELF kernel at `/usr/share/firecracker/vmlinux`.
`swarmcracker setup install --download-kernel` downloads a known-good kernel for you.

Don't try downloading from GitHub raw URLs — you'll get HTML, not a binary.

To extract one from your host kernel instead:

```bash
sudo mkdir -p /usr/share/firecracker
./test-automation/scripts/extract-vmlinux.sh /boot/vmlinuz-* /usr/share/firecracker/vmlinux

# Check it worked
file /usr/share/firecracker/vmlinux
# Should say: ELF 64-bit LSB executable, x86-64
```

### Test Cluster with Vagrant

If you want to experiment locally:

```bash
git clone https://github.com/restuhaqza/SwarmCracker
cd SwarmCracker
vagrant up
```

---

## Start the Cluster

### Manager Node

```bash
sudo swarmcracker cluster init --advertise-addr <MANAGER_IP>:4242
```

`--advertise-addr` is critical: workers need to reach the manager, and without it
they'll try to connect to `0.0.0.0`, which won't work.

This starts:
- SwarmKit manager (Raft consensus for cluster state)
- Control socket at `/var/run/swarmkit/swarm.sock`
- TLS certificates
- Join tokens saved to `/var/lib/swarmkit/join-tokens.txt`

### Get the Join Token

```bash
sudo swarmcracker cluster token create --role worker
```

Look for the `SWMTKN-...` token in the output.

### Join Workers

```bash
sudo swarmcracker cluster join --token <TOKEN> <manager-ip>:4242
```

### Check the Cluster

```bash
sudo swarmcracker cluster status
sudo swarmcracker cluster health
```

You should see all your nodes with `READY` status.

---

## Run Something

### Deploy a Service

```bash
sudo swarmcracker service create --name web --replicas 3 -p 8080:80 nginx:alpine
```

### See What's Running

```bash
swarmcracker service list
swarmcracker service ps web
```

Each task is a Firecracker microVM.

---

## What's Actually Happening

```
Manager (swarmd)
    │
    │ gRPC: schedules tasks, maintains state
    │
┌───┴───────────────────┐
│                       │
Worker-1              Worker-2
swarm-br0             swarm-br0
┌───┐┌───┐            ┌───┐┌───┐
│VM1││VM2│  ← VXLAN → │VM3││VM4│
└───┘└───┘            └───┘└───┘
```

- Manager runs SwarmKit control plane
- Workers run swarmd-firecracker, which turns SwarmKit tasks into microVMs
- `swarm-br0` is a Linux bridge for local VM networking
- VXLAN connects VMs across different nodes

---

## Common Problems

### Kernel: Invalid ELF Magic Number

The kernel file isn't actually a kernel. Probably HTML from a bad download.

```bash
file /usr/share/firecracker/vmlinux
# If it says "HTML document", re-download:
sudo swarmcracker setup install --download-kernel
# Or extract from your host kernel:
./test-automation/scripts/extract-vmlinux.sh /boot/vmlinuz-* /usr/share/firecracker/vmlinux
```

### KVM Not Found

```bash
sudo modprobe kvm_intel   # Intel
sudo modprobe kvm_amd     # AMD
```

### Nested KVM Issues

Running inside a VM? Check:

```bash
cat /sys/module/kvm_intel/parameters/nested

# If it's 'N':
sudo modprobe -r kvm_intel
sudo modprobe kvm_intel nested=1
```

Or add `options kvm_intel nested=1` to `/etc/modprobe.d/kvm-nested.conf`.

### Workers Can't Connect

```bash
curl http://<manager-ip>:4242   # Check manager reachable
sudo swarmcracker cluster status
```

If the manager advertises `0.0.0.0:4242`, that's wrong. Re-init with
`--advertise-addr <actual-ip>:4242`.

### Services Not Starting

```bash
sudo swarmcracker cluster status      # Check nodes are ready
sudo swarmcracker doctor              # Diagnose common issues
journalctl -u swarmcracker -f         # Watch logs
file /usr/share/firecracker/vmlinux   # Verify kernel is ELF
```

---

## Next

- [Configuration](guides/configuration.md) — More options
- [Networking](guides/networking.md) — VXLAN setup
- [Security](guides/security.md) — Jailer hardening
- [CLI Reference](reference/cli.md) — All commands
