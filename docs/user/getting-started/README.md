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

### Quick Install

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/docs/site/install.sh | sudo bash
```

This pulls Firecracker, the jailer, SwarmCracker binaries, and sets up defaults.

### Build It Yourself

```bash
git clone https://github.com/restuhaqza/SwarmCracker
cd SwarmCracker
make build
sudo make install
```

### The Kernel Thing

Firecracker needs an uncompressed ELF kernel at `/usr/share/firecracker/vmlinux`. 

Don't try downloading from GitHub raw URLs — you'll get HTML, not a binary.

Extract it from your host kernel instead:

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

The `--advertise-remote-api` flag is critical. Workers need to reach the manager, and without it they'll try to connect to `0.0.0.0` which won't work.

```bash
MANAGER_IP=$(ip addr show eth0 | grep 'inet ' | awk '{print $2}' | cut -d/ -f1)

swarmd-firecracker --manager \
  --hostname manager-1 \
  --listen-remote-api 0.0.0.0:4242 \
  --advertise-remote-api $MANAGER_IP:4242 \
  --kernel-path /usr/share/firecracker/vmlinux \
  --rootfs-dir /var/lib/firecracker/rootfs \
  --bridge-name swarm-br0
```

This starts:
- SwarmKit manager (Raft consensus for cluster state)
- Control socket at `/var/run/swarmkit/swarm.sock`
- TLS certificates
- Join tokens saved to `/var/lib/swarmkit/manager/join-tokens.txt`

### Get the Join Token

```bash
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
swarmctl cluster inspect default
```

Look for the Worker token in the output.

### Join Workers

```bash
swarmcracker join \
  --hostname worker-1 \
  --manager <manager-ip>:4242 \
  --token <WORKER_TOKEN>
```

### Check the Cluster

```bash
swarmctl ls-nodes
```

You should see all your nodes with `READY` status.

---

## Run Something

### Deploy a Service

```bash
swarmctl create-service nginx:latest
```

### Scale It

```bash
swarmctl scale svc-nginx-143022 3
```

### See What's Running

```bash
swarmctl ls-tasks
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
# If it says "HTML document", re-extract from host kernel
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
ps aux | grep swarmd | grep advertise   # Verify advertise flag set
```

If the manager shows `advertise-remote-api 0.0.0.0:4242`, that's wrong. It needs the actual IP.

### Services Not Starting

```bash
swarmctl ls-nodes           # Check nodes are ready
journalctl -u swarmd -f     # Watch logs
file /usr/share/firecracker/vmlinux   # Verify kernel is ELF
```

---

## Next

- [Configuration](guides/configuration.md) — More options
- [Networking](guides/networking.md) — VXLAN setup
- [Security](guides/security.md) — Jailer hardening
- [CLI Reference](reference/cli.md) — All commands