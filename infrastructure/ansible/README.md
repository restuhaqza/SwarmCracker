# SwarmCracker Ansible Infrastructure

Automated deployment of SwarmCracker MicroVM clusters using Ansible.

## Structure

```
infrastructure/ansible/
├── site.yml                     # Entry point — full cluster setup
├── ansible.cfg                  # Ansible configuration
├── group_vars/all.yml           # Global variables
│
├── inventory/                   # Host inventories
│   ├── libvirt/                 # KVM/libvirt cluster (recommended)
│   ├── virtualbox-fresh/        # VirtualBox (legacy, no nested virt)
│   └── production/              # Production (bare metal / cloud)
│
├── playbooks/                   # Operational playbooks
│   ├── setup-cluster.yml        # Full cluster: common → manager → workers → firecracker → networking
│   ├── cluster-common.yml       # (internal) Common prerequisites
│   ├── cluster-manager.yml      # (internal) Manager deployment
│   ├── cluster-workers.yml      # (internal) Worker deployment
│   ├── deploy-microvms.yml      # Provision MicroVMs across workers
│   ├── test-connectivity.yml    # Verify cross-node VM networking
│   └── teardown.yml             # Remove all components
│
└── roles/                       # Ansible roles
    ├── common/                  # Kernel modules, packages, dirs, NTP, KVM check
    ├── manager/                 # SwarmCracker manager install + systemd service
    ├── worker/                  # SwarmCracker worker install + join cluster
    ├── firecracker/             # Firecracker binary, kernel, rootfs download
    ├── networking/              # Bridge, VXLAN overlay, NAT
    └── microvm/                 # TAP + Firecracker REST API VM provisioning
```

## Quick Start

### 1. Bring up test VMs (KVM/libvirt — recommended)

```bash
# Prerequisites
sudo apt-get install -y qemu-kvm libvirt-daemon-system vagrant
vagrant plugin install vagrant-libvirt

# Start cluster
cd test-automation/
VAGRANT_VAGRANTFILE=Vagrantfile.libvirt vagrant up
```

> **Note:** VirtualBox is available but does not support nested virtualization
> (Firecracker network passthrough fails). Use KVM/libvirt for MicroVM testing.

### 2. Deploy full cluster

```bash
cd infrastructure/ansible/
ANSIBLE_INVENTORY=inventory/libvirt ansible-playbook site.yml
```

This runs: **common → manager → workers → firecracker → networking**

### 3. Deploy MicroVMs

```bash
# Default: 1 VM per worker
ANSIBLE_INVENTORY=inventory/libvirt ansible-playbook playbooks/deploy-microvms.yml

# Custom VMs
ANSIBLE_INVENTORY=inventory/libvirt ansible-playbook playbooks/deploy-microvms.yml \
  --extra-vars '{"microvm_vms": [{"name":"nginx","ip_offset":1,"vcpus":2,"memory_mb":256}]}'
```

### 4. Test cross-node connectivity

```bash
ANSIBLE_INVENTORY=inventory/libvirt ansible-playbook playbooks/test-connectivity.yml
```

### 5. Teardown

```bash
ANSIBLE_INVENTORY=inventory/libvirt ansible-playbook playbooks/teardown.yml
```

## Roles

| Role | Target | Description |
|------|--------|-------------|
| `common` | All nodes | Packages, kernel modules, KVM, IP forwarding, NTP, directories |
| `manager` | Managers | SwarmCracker binary, config, systemd service, join token |
| `worker` | Workers | SwarmCracker binary, config, systemd service, join cluster |
| `firecracker` | Workers | Firecracker binary, kernel, rootfs image download |
| `networking` | Workers | Bridge, VXLAN overlay, NAT masquerade |
| `microvm` | Workers | TAP device, VM config, Firecracker REST API provisioning |

## Variables

Key variables in `group_vars/all.yml`:

| Variable | Default | Description |
|----------|---------|-------------|
| `swarmcracker_version` | `v0.2.1` | Release version to deploy |
| `swarmcracker_bridge_name` | `swarm-br0` | SwarmCracker control bridge |
| `swarmcracker_subnet` | `192.168.127.0/24` | SwarmCracker control subnet |
| `network_bridge_ip` | `172.20.0.1` | VM bridge gateway IP |
| `network_bridge_subnet` | `172.20.0.0/24` | VM network subnet |
| `network_vxlan_enabled` | `true` | Enable VXLAN overlay for cross-node L2 |
| `network_vxlan_vni` | `100` | VXLAN VNI identifier |
| `network_nat_enabled` | `true` | Enable NAT for VM outbound access |
| `firecracker_version` | `v1.14.0` | Firecracker release version |
| `microvm_default_vcpus` | `1` | Default VM CPU count |
| `microvm_default_memory_mb` | `128` | Default VM memory |

## Tag-Based Execution

Run only specific roles:

```bash
ansible-playbook site.yml --tags firecracker    # Only Firecracker install
ansible-playbook site.yml --tags networking      # Only network setup
ansible-playbook site.yml --tags common          # Only prerequisites
```

## Network Topology

```
  ┌──────────────────┐    VXLAN overlay    ┌──────────────────┐
  │   Worker-1       │◄───────────────────►│   Worker-2       │
  │                  │    (L2 bridge,      │                  │
  │                  │     VNI 100)        │                  │
  │  swarm-br0       │                    │  swarm-br0       │
  │  172.20.0.1/24   │                    │  172.20.0.1/24   │
  │       │          │                    │       │          │
  │   tap-vm01       │                    │   tap-vm11       │
  │       │          │                    │       │          │
  │  ┌────▼───┐      │                    │  ┌────▼───┐      │
  │  │ VM-1   │◄─────┼────────────────────┼──│ VM-2   │      │
  │  │172.20  │      │                    │  │172.20  │      │
  │  │.0.11   │      │                    │  │.0.21   │      │
  │  └────────┘      │                    │  └────────┘      │
  └──────────────────┘                    └──────────────────┘
```

## Known Limitations

- **VirtualBox:** Nested virtualization does not support Firecracker network
  passthrough. TAP devices inside VirtualBox guests do not transmit packets.
  Use KVM/libvirt instead.
- **Ubuntu rootfs boot:** The `nomodeset vga=off pci=off` kernel args are
  required to prevent framebuffer MMIO errors with the quickstart guide kernel.
- **Boot time:** Ubuntu-based MicroVMs need ~45 seconds to boot and apply
  netplan configuration. The playbook waits 45s before connectivity tests.
