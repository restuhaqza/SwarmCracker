# Multi-node SwarmCracker lab

Reusable automation for standing up a **multi-node SwarmCracker cluster with the
VXLAN overlay** and smoke-testing cross-host microVM networking.

It creates `N` nested Ubuntu VMs on a single KVM/libvirt host, provisions the
Firecracker executor on each, forms a SwarmKit cluster, and verifies that a
microVM scheduled on one node is reachable from the other nodes.

This is the reproducible form of that flow, automated by
[`cluster-lab.sh`](cluster-lab.sh).

## Why this exists

The older `test-automation/*.sh` cluster scripts were legacy — hard-coded
`/home/kali/...` paths, old `swarmd-manager`/`swarmd-worker` unit names, and
Vagrant-only — and have been removed. Ansible (`infrastructure/ansible/`) remains
as the advanced/production path but requires you to supply the hosts.

This script is self-contained and creates a working multi-node cluster on a
single modern KVM host.

## Prerequisites (on the host)

- Linux with KVM (`/dev/kvm`) and **nested virtualization** enabled
  (`cat /sys/module/kvm_intel/parameters/nested` → `Y`).
- `libvirt` + `virsh`, `virt-install`, `qemu-img`, `genisoimage` (or `mkisofs`),
  `python3`, `rsync`, `curl`, Go 1.26+.
- Host runtime assets used to seed the guests, produced by running the installer
  once on the host:

  ```bash
  sudo swarmcracker setup install --download-kernel --download-rootfs --download-cni
  ```

  Expected: `/usr/local/bin/firecracker` (+`jailer`), `/usr/share/firecracker/vmlinux`,
  `/var/lib/firecracker/rootfs/*.ext4`, `/opt/cni/bin/*`.

## Usage

```bash
# One-shot: build binaries, fetch base image, create 2 VMs, provision,
# form the cluster and print status.
sudo test-automation/multinode/cluster-lab.sh up 2

# Or step by step
sudo test-automation/multinode/cluster-lab.sh build
sudo test-automation/multinode/cluster-lab.sh image
sudo test-automation/multinode/cluster-lab.sh create 3
sudo test-automation/multinode/cluster-lab.sh provision
sudo test-automation/multinode/cluster-lab.sh cluster
sudo test-automation/multinode/cluster-lab.sh test

# Inspect / enter / tear down
sudo test-automation/multinode/cluster-lab.sh status
sudo test-automation/multinode/cluster-lab.sh ssh 2
sudo test-automation/multinode/cluster-lab.sh destroy
```

`test` deploys one replica of `nginx:alpine` per node and prints a
from-node → to-node matrix of `ping` and `HTTP` results. A healthy cluster shows
`ping=OK http=200` for **remote** microVMs, not just the local one.

## Configuration

All settings are environment variables (defaults in the script header). Common
ones:

| Variable | Default | Meaning |
|---|---|---|
| `LAB_PREFIX` | `sc-lab` | VM name prefix (`<prefix>-node1`, ...) |
| `LAB_NODES` | `2` | default node count |
| `LAB_NET` | `default` | libvirt network |
| `LAB_CPUS` / `LAB_MEM` / `LAB_DISK` | `2` / `2048` / `24G` | per-VM resources |
| `LAB_ROOT` | `/var/lib/swarmcracker-lab` | state, images, VM disks |
| `LAB_IMAGE` | `<root>/noble-server-cloudimg-amd64.img` | base cloud image |
| `LAB_BIN` | `<root>/bin` | where binaries are built |
| `LAB_SUBNET` | `192.168.127.0/24` | microVM overlay subnet (must be `/24`) |
| `LAB_BRIDGE` | `swarm-br0` | microVM bridge name |
| `LAB_IMAGE_OCI` | `nginx:alpine` | image used by `test` |

Example with 3 nodes and a custom overlay subnet:

```bash
sudo LAB_NODES=3 LAB_SUBNET=192.168.140.0/24 \
  test-automation/multinode/cluster-lab.sh up
```

## Topology

```
host (KVM + libvirt network 192.168.122.0/24)
 ├─ <prefix>-node1   manager   192.168.122.x   swarm-br0 = 192.168.127.1/24
 ├─ <prefix>-node2   worker    192.168.122.y   swarm-br0 = 192.168.127.2/24
 └─ <prefix>-nodeN   worker    192.168.122.z   swarm-br0 = 192.168.127.N/24
        └─ VXLAN (VNI 100, UDP 4789) forms one L2 segment across nodes
```

Each node's `--vxlan-peers` is the list of the *other* nodes' transport IPs, so
every microVM bridge is reachable across hosts.

## Notes and limitations

- The two nodes share the overlay subnet with distinct bridge IPs. Per-task IPs
  are hashed from the task ID, so cross-node IP collisions are unlikely but not
  impossible; use a wider subnet for large labs.
- Guest MAC addresses are unique per task (see the duplicate-MAC fix in the
  report). Older binaries that emit a constant `AA:FC:00:00:00:00` will not work
  across nodes.
- The cloud-init seed is served over HTTP on the libvirt bridge and injected via
  the SMBIOS serial (`ds=nocloud-net;...`), which is reliable across modern
  cloud images.
