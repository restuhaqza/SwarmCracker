# 🚀 Quick Start Guide - SwarmCracker Test Cluster

## 📍 Location
```
projects/swarmcracker/test-automation/
```

## ⚡ 3 Steps to Start Testing

### 1️⃣ Install Prerequisites (one time)

**KVM/libvirt (recommended):**
```bash
sudo apt-get update
sudo apt-get install -y qemu-kvm libvirt-daemon-system vagrant
vagrant plugin install vagrant-libvirt

# Fix AppArmor for QEMU (Kali/Debian)
echo 'security_driver = "none"' | sudo tee -a /etc/libvirt/qemu.conf
sudo systemctl restart libvirtd
```

**VirtualBox (legacy — no nested virt support):**
```bash
sudo apt-get update
sudo apt-get install -y virtualbox vagrant
```

> ⚠️ **VirtualBox does not support Firecracker networking.** The nested
> virtualization layer doesn't pass through TAP device packets. Use
> KVM/libvirt for MicroVM testing.

### 2️⃣ Start the Cluster

**KVM/libvirt:**
```bash
cd test-automation/
VAGRANT_VAGRANTFILE=Vagrantfile.libvirt vagrant up
```

**VirtualBox (legacy):**
```bash
cd test-automation/
VAGRANT_VAGRANTFILE=Vagrantfile.ansible vagrant up
```

### 3️⃣ Deploy with Ansible

```bash
cd infrastructure/ansible/

# Full cluster setup
ANSIBLE_INVENTORY=inventory/libvirt ansible-playbook site.yml

# Deploy MicroVMs
ANSIBLE_INVENTORY=inventory/libvirt ansible-playbook playbooks/deploy-microvms.yml

# Test connectivity
ANSIBLE_INVENTORY=inventory/libvirt ansible-playbook playbooks/test-connectivity.yml
```

---

## 🎯 What You Get

- **3 VMs** (1 manager + 2 workers)
- **SwarmCracker cluster** fully configured
- **Firecracker** installed on workers
- **VXLAN overlay** for cross-node L2 networking
- **MicroVMs** provisioned via Firecracker REST API

---

## 🎮 Quick Commands

```bash
# Cluster status
vagrant status

# SSH into a node
vagrant ssh manager
vagrant ssh worker1
vagrant ssh worker2

# Destroy cluster
vagrant destroy -f
```
