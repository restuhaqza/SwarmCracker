# Docker Compose — Demo Only

⚠️ **This is a single-host demo. Real networking (VXLAN) does not work inside Docker Compose.**

The docker-compose setup uses `privileged: true` and `/dev/kvm` passthrough, which:
- Only works on a single Linux host with KVM
- Cannot establish VXLAN tunnels between containers
- Uses a hardcoded join token (placeholder)

## For real deployments

See the [Getting Started guide](https://swarmcracker.dev/getting-started/) for the blessed deployment path:

```bash
# One-line install
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash

# Setup Firecracker + network
sudo swarmcracker setup install
sudo swarmcracker setup network

# Initialize cluster (manager) or join (worker)
sudo swarmcracker cluster init --advertise-addr <IP>:4242
sudo swarmcracker cluster join --token <TOKEN> <MANAGER_IP>:4242
```

For Ansible-based production deployment: [Ansible Guide](../docs/user/guides/ansible.md)
