# Ansible Testing with Fresh Ubuntu VMs

This directory contains a Vagrantfile for creating **FRESH Ubuntu 22.04 VMs** specifically for testing Ansible automation scripts.

## Key Difference

- **Vagrantfile** (original) - Includes provisioning scripts that auto-install SwarmCracker
- **Vagrantfile.ansible** (this one) - Creates CLEAN Ubuntu VMs with NO provisioning

## Quick Start

### 1. Create Fresh VMs

```bash
cd /home/kali/.openclaw/workspace/projects/swarmcracker/test-automation

# Using the Ansible-specific Vagrantfile
VAGRANT_VAGRANTFILE=Vagrantfile.ansible vagrant up

# Or specify VMs individually
VAGRANT_VAGRANTFILE=Vagrantfile.ansible vagrant up manager
VAGRANT_VAGRANTFILE=Vagrantfile.ansible vagrant up worker1
VAGRANT_VAGRANTFILE=Vagrantfile.ansible vagrant up worker2
```

### 2. Verify VMs Are Fresh

```bash
# Test SSH connectivity
cd /home/kali/.openclaw/workspace/projects/swarmcracker/infrastructure/ansible
ansible all -i inventory/virtualbox-fresh -m ping

# Verify no SwarmCracker installed
ansible all -i inventory/virtualbox-fresh -m shell -a "which swarmcracker || echo 'FRESH!'"
```

### 3. Deploy with Ansible

```bash
# Deploy complete cluster
ansible-playbook -i inventory/virtualbox-fresh site.yml -v

# Deploy manager only
ansible-playbook -i inventory/virtualbox-fresh setup-manager.yml -v

# Deploy workers only
ansible-playbook -i inventory/virtualbox-fresh setup-worker.yml \
  --extra-vars "manager_host=192.168.56.10" -v
```

### 4. Verify Deployment

```bash
# Check cluster status
ansible managers -i inventory/virtualbox-fresh -m command -a "swarmctl node ls"

# Check services
ansible all -i inventory/virtualbox-fresh -m shell -a "systemctl is-active swarmd-firecracker"

# Deploy test services
ansible-playbook -i inventory/virtualbox-fresh test-cluster.yml
```

### 5. Cleanup

```bash
cd /home/kali/.openclaw/workspace/projects/swarmcracker/test-automation
VAGRANT_VAGRANTFILE=Vagrantfile.ansible vagrant destroy -f
```

---

## VM Configuration

| VM | Hostname | IP | vCPU | RAM | Purpose |
|----|----------|-----|------|-----|---------|
| manager | swarm-manager | 192.168.56.10 | 2 | 2GB | SwarmKit Manager |
| worker1 | swarm-worker-1 | 192.168.56.11 | 2 | 4GB | SwarmCracker Worker |
| worker2 | swarm-worker-2 | 192.168.56.12 | 2 | 4GB | SwarmCracker Worker |

## Network

- **Host-only Network:** 192.168.56.0/24
- **SSH:** Port 22 (forwarded to host ports 2222, 2200, 2201)
- **SwarmKit Manager:** Port 4242
- **Worker API:** Port 4243
- **VXLAN:** Port 4789/UDP

## Features Enabled

- ✅ Nested virtualization (for KVM/Firecracker)
- ✅ PAE (Physical Address Extension)
- ✅ IO APIC (I/O Advanced Programmable Interrupt Controller)
- ✅ 16MB VRAM

## Troubleshooting

### VM Not Booting
```bash
VAGRANT_VAGRANTFILE=Vagrantfile.ansible vagrant reload manager
```

### SSH Connection Refused
Wait 2-3 minutes for VM to fully boot, then:
```bash
VAGRANT_VAGRANTFILE=Vagrantfile.ansible vagrant provision manager --provision-with shell-inline
```

### Ansible Can't Connect
Check if VM is ready:
```bash
VAGRANT_VAGRANTFILE=Vagrantfile.ansible vagrant ssh manager
```

### Destroy Stuck VMs
```bash
# Kill locked processes
pkill -9 VBoxManage

# Force destroy
VAGRANT_VAGRANTFILE=Vagrantfile.ansible vagrant destroy -f
```

---

## Why Use This Vagrantfile?

### Original Vagrantfile
- ✅ Good for: Quick manual testing
- ❌ Bad for: Testing Ansible (already has SwarmCracker installed)

### Vagrantfile.ansible (This One)
- ✅ Good for: Testing Ansible automation from scratch
- ✅ Good for: Validating idempotency
- ✅ Good for: CI/CD testing
- ✅ Good for: Benchmarking deployment time

---

## Expected Deployment Time

| Phase | Time |
|-------|------|
| VM Creation (3 VMs) | 3-5 min |
| Ansible Common Role | 10-15 min |
| Ansible Manager Role | 5-8 min |
| Ansible Worker Role | 5-8 min |
| **Total** | **25-35 min** |

---

## See Also

- [Ansible Documentation](../README.md)
- [Getting Started](../../docs/getting-started/)
- [Original Vagrantfile](../Vagrantfile)
