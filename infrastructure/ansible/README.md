# SwarmCracker Cluster Automation

Ansible playbooks and roles for automated SwarmCracker cluster deployment.

## Directory Structure

```
infrastructure/ansible/
├── ansible.cfg              # Ansible configuration
├── inventory/               # Cluster inventory
│   ├── production/         # Production cluster
│   └── staging/           # Staging cluster
├── group_vars/            # Group variables
│   ├── all.yml           # Variables for all hosts
│   ├── managers.yml      # Manager-specific variables
│   └── workers.yml       # Worker-specific variables
├── host_vars/            # Host-specific variables
├── roles/               # Ansible roles
│   ├── common/         # Common setup (prerequisites)
│   ├── manager/        # SwarmCracker manager setup
│   └── worker/         # SwarmCracker worker setup
├── site.yml            # Main playbook
├── setup-manager.yml   # Manager-only playbook
└── setup-worker.yml    # Worker-only playbook
```

## Quick Start

### 1. Configure Inventory

Edit `inventory/production/hosts`:

```ini
[managers]
manager1 ansible_host=192.168.1.10

[workers]
worker1 ansible_host=192.168.1.11
worker2 ansible_host=192.168.1.12

[swarmcracker:children]
managers
workers
```

### 2. Configure Variables

Edit `group_vars/all.yml`:

```yaml
swarmcracker_version: "v0.2.0"
swarmcracker_arch: "amd64"  # or "arm64"
swarmcracker_download_url: "https://github.com/restuhaqza/SwarmCracker/releases/download/{{ swarmcracker_version }}"
```

### 3. Deploy Cluster

```bash
# Deploy entire cluster (managers + workers)
ansible-playbook -i inventory/production site.yml

# Deploy managers only
ansible-playbook -i inventory/production setup-manager.yml

# Deploy workers only
ansible-playbook -i inventory/production setup-worker.yml --extra-vars "manager_host=192.168.1.10"

# Deploy with custom variables
ansible-playbook -i inventory/production site.yml \
  --extra-vars "swarmcracker_version=v0.2.0"
```

### 4. Verify Deployment

```bash
# Check manager status
ansible managers -i inventory/production -m command -a "swarmctl node ls"

# Check worker status
ansible workers -i inventory/production -m command -a "systemctl status swarmd-firecracker"
```

## Requirements

- Ansible 2.9+
- Python 3.8+ on control node
- SSH access to target hosts
- Target hosts: Ubuntu 20.04+/Debian 11+/CentOS 8+

## Roles

### common
- Install system dependencies (Go, Firecracker, etc.)
- Configure kernel modules (KVM, VXLAN)
- Setup network bridges
- Configure firewall rules

### manager
- Download and install SwarmCracker binaries
- Initialize SwarmKit manager
- Configure manager service
- Generate worker join tokens

### worker
- Download and install SwarmCracker binaries
- Join SwarmKit cluster
- Configure worker service
- Setup VXLAN networking

## Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `swarmcracker_version` | `v0.2.0` | SwarmCracker release version |
| `swarmcracker_arch` | `amd64` | Architecture (amd64/arm64) |
| `swarmcracker_install_dir` | `/usr/local/bin` | Binary installation directory |
| `swarmcracker_state_dir` | `/var/lib/swarmcracker` | State directory |
| `swarmcracker_kernel_path` | `/usr/share/firecracker/vmlinux` | Firecracker kernel path |
| `swarmcracker_rootfs_dir` | `/var/lib/firecracker/rootfs` | Rootfs directory |
| `swarmcracker_bridge_name` | `swarm-br0` | Network bridge name |
| `swarmcracker_subnet` | `192.168.127.0/24` | VM subnet |
| `swarmcracker_bridge_ip` | `192.168.127.1/24` | Bridge IP |
| `swarmcracker_vxlan_enabled` | `true` | Enable VXLAN networking |
| `swarmcracker_vxlan_vni` | `100` | VXLAN VNI |
| `swarmcracker_vxlan_port` | `4789` | VXLAN UDP port |

## Examples

### Single Node (Development)

```ini
[managers]
localhost ansible_connection=local

[workers]

[swarmcracker:children]
managers
```

### Multi-Node Production

```ini
[managers]
manager1 ansible_host=192.168.1.10
manager2 ansible_host=192.168.1.11
manager3 ansible_host=192.168.1.12

[workers]
worker1 ansible_host=192.168.1.20
worker2 ansible_host=192.168.1.21
worker3 ansible_host=192.168.1.22
worker4 ansible_host=192.168.1.23
worker5 ansible_host=192.168.1.24

[swarmcracker:children]
managers
workers
```

### Mixed Architecture

```ini
[managers]
manager1 ansible_host=192.168.1.10 swarmcracker_arch=amd64

[workers]
worker1 ansible_host=192.168.1.11 swarmcracker_arch=amd64
worker2 ansible_host=192.168.1.12 swarmcracker_arch=arm64  # ARM server
```

## Troubleshooting

### Check Ansible Connection

```bash
ansible all -i inventory/production -m ping
```

### Verbose Output

```bash
ansible-playbook -i inventory/production site.yml -vvv
```

### Dry Run

```bash
ansible-playbook -i inventory/production site.yml --check --diff
```

### Cleanup

```bash
ansible-playbook -i inventory/production teardown.yml
```

## See Also

- [SwarmCracker Installation Guide](../../docs/getting-started/installation-guide.md)
- [SwarmCracker Documentation](../../docs/)
- [Ansible Documentation](https://docs.ansible.com/)
