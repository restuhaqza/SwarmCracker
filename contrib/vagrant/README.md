# Vagrant — Development Cluster (Legacy)

⚠️ **Vagrant is no longer the recommended development setup.**

These Vagrantfiles are preserved for reference. They may not reflect the current
Ansible roles or binary dependencies.

## Recommended alternatives

### Quick development setup
```bash
# Use the test-automation E2E test suite
make test-e2e
```

### Production-like development
```bash
# Use the Ansible playbooks against libvirt VMs
cd infrastructure/ansible
ansible-playbook -i inventory/libvirt site.yml
```

### Getting started
```bash
# One-line install + setup
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash
swarmcracker setup install
swarmcracker setup network
swarmcracker cluster init --advertise-addr <IP>:4242
```

## Vagrantfile variants

| File | Purpose | Status |
|------|---------|--------|
| `Vagrantfile` | 3-node libvirt cluster | Possibly stale |
| `Vagrantfile.ansible` | Ansible-provisioned | Likely stale |
| `Vagrantfile.ansible-test` | Ansible testing variant | Likely stale |
| `Vagrantfile.digitalocean` | DigitalOcean cloud | Possibly stale |
| `Vagrantfile.virtualbox.bak` | VirtualBox backup | Stale |
| `Vagrantfile.digitalocean.bak` | DigitalOcean backup | Stale |
