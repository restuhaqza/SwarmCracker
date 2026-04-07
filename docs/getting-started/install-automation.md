# Installation Automation Guide

Automated installation and cluster setup for SwarmCracker.

## Quick Start

### Initialize Manager (One Command)

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- init
```

### Join Worker (One Command)

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- join \
  --manager 192.168.1.10:4242 \
  --token SWMTKN-1-abc123xyz
```

---

## Installation Modes

### 1. Interactive Mode (Default)

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash
```

**Prompts for:**
- Node type (manager/worker/skip)
- Network configuration
- State directories

---

### 2. Init Mode (Manager Setup)

Initialize a new cluster:

```bash
# Basic
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- init

# With custom advertise address
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- init \
  --advertise-addr 192.168.1.10:4242

# With VXLAN overlay
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- init \
  --vxlan-enabled \
  --vxlan-peers 192.168.1.11,192.168.1.12

# With custom resources
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- init \
  --vcpus 2 \
  --memory 1024

# Full example
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- init \
  --hostname swarm-manager \
  --advertise-addr 192.168.1.10:4242 \
  --vxlan-enabled \
  --vxlan-peers 192.168.1.11,192.168.1.12 \
  --vcpus 2 \
  --memory 1024 \
  --debug
```

**What it does:**
1. Downloads latest SwarmCracker release from GitHub
2. Verifies checksum
3. Installs binaries to `/usr/local/bin`
4. Installs Firecracker if missing
5. Downloads Firecracker kernel
6. Runs `swarmcracker init` with provided flags
7. Generates join tokens
8. Prints worker join command

**Output:**
```
✅ Installation & Initialization Complete

SwarmCracker cluster is ready!

Next steps:
  1. Get join token: sudo cat /var/lib/swarmkit/join-tokens.txt
  2. On workers, run:
     curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- join \
       --manager <MANAGER_IP>:4242 --token <TOKEN>
```

---

### 3. Join Mode (Worker Setup)

Join an existing cluster:

```bash
# Basic
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- join \
  --manager 192.168.1.10:4242 \
  --token SWMTKN-1-abc123xyz

# With VXLAN (must match manager config)
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- join \
  --manager 192.168.1.10:4242 \
  --token SWMTKN-1-abc123xyz \
  --vxlan-enabled \
  --vxlan-peers 192.168.1.10,192.168.1.12

# With custom hostname
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- join \
  --manager 192.168.1.10:4242 \
  --token SWMTKN-1-abc123xyz \
  --hostname worker-01
```

**What it does:**
1. Downloads and installs SwarmCracker
2. Validates connectivity to manager
3. Runs `swarmcracker join` with provided token
4. Starts worker service
5. Verifies cluster join

---

### 4. Legacy Worker Mode (Backward Compatible)

Old style worker setup (uses `swarmd-firecracker` directly):

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- \
  --worker \
  --manager 192.168.1.10:4242 \
  --token SWMTKN-1-abc123xyz
```

**Note:** Prefer `join` mode for new deployments — it uses the new `swarmcracker` CLI which provides better error handling and systemd integration.

---

## CLI Flags Reference

### Common Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--hostname NAME` | Node hostname | Auto-detect |
| `--state-dir DIR` | State directory | `/var/lib/swarmkit` |
| `--install-dir DIR` | Binary install location | `/usr/local/bin` |
| `--debug` | Enable debug logging | `false` |

### Network Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--bridge NAME` | Bridge name | `swarm-br0` |
| `--subnet CIDR` | VM subnet | `192.168.127.0/24` |
| `--bridge-ip IP` | Bridge IP | `192.168.127.1/24` |
| `--advertise-addr ADDR` | Address to advertise | Auto-detect |

### VXLAN Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--vxlan-enabled` | Enable VXLAN overlay | `false` |
| `--vxlan-peers IPS` | Comma-separated peer IPs | `""` |

### Resource Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--vcpus N` | Default vCPUs per VM | `1` |
| `--memory MB` | Default memory per VM | `512` |

### Firecracker Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--kernel-path PATH` | Kernel image path | `/usr/share/firecracker/vmlinux` |
| `--rootfs-dir DIR` | Rootfs directory | `/var/lib/firecracker/rootfs` |

---

## Multi-Node Cluster Example

### Step 1: Initialize Manager

On manager node (192.168.1.10):

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- init \
  --hostname manager \
  --vxlan-enabled \
  --vxlan-peers 192.168.1.11,192.168.1.12
```

**Save the output** — it contains the worker join command.

### Step 2: Get Join Token

```bash
sudo cat /var/lib/swarmkit/join-tokens.txt
```

### Step 3: Join Workers

On worker-1 (192.168.1.11):

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- join \
  --manager 192.168.1.10:4242 \
  --token SWMTKN-1-abc123xyz \
  --hostname worker-1 \
  --vxlan-enabled \
  --vxlan-peers 192.168.1.10,192.168.1.12
```

On worker-2 (192.168.1.12):

```bash
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- join \
  --manager 192.168.1.10:4242 \
  --token SWMTKN-1-abc123xyz \
  --hostname worker-2 \
  --vxlan-enabled \
  --vxlan-peers 192.168.1.10,192.168.1.11
```

### Step 4: Verify Cluster

On manager:

```bash
swarmcracker status
swarmcracker list nodes
```

---

## Ansible Integration

```yaml
- name: Deploy SwarmCracker Cluster
  hosts: swarmcracker
  become: true
  vars:
    manager_node: "{{ groups['swarmcracker'][0] }}"
    vxlan_peers: "{{ groups['swarmcracker'] | map('extract', hostvars, 'ansible_host') | join(',') }}"
  
  tasks:
    - name: Initialize Manager
      shell: |
        curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash -s -- init \
          --vxlan-enabled \
          --vxlan-peers "{{ vxlan_peers }}"
      when: inventory_hostname == manager_node
      register: init_result
    
    - name: Get Join Token
      command: cat /var/lib/swarmkit/join-tokens.txt
      when: inventory_hostname == manager_node
      register: join_token
      delegate_to: "{{ manager_node }}"
    
    - name: Join Workers
      shell: |
        curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash -s -- join \
          --manager {{ hostvars[manager_node]['ansible_host'] }}:4242 \
          --token "{{ join_token.stdout_lines[0] | regex_replace('WORKER_TOKEN=', '') }}" \
          --vxlan-enabled \
          --vxlan-peers "{{ vxlan_peers }}"
      when: inventory_hostname != manager_node
```

---

## Troubleshooting

### Installation Fails

```bash
# Run with debug output
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- init --debug

# Check logs
sudo journalctl -u swarmcracker-manager -f
```

### KVM Not Available

```
error: /dev/kvm not found — Firecracker VMs will not work

Fix:
  - Enable virtualization in BIOS (Intel VT-x / AMD-V)
  - Load KVM module: sudo modprobe kvm
  - Check: ls -la /dev/kvm
```

### Port Already in Use

```
error: Port 4242 is already in use

Fix:
  swarmcracker init --listen-addr 0.0.0.0:4243
```

### Worker Can't Join

```bash
# Test connectivity
nc -zv <manager-ip> 4242

# Check firewall
sudo iptables -L -n | grep 4242

# Verify token
sudo cat /var/lib/swarmkit/join-tokens.txt
```

---

## Uninstall

```bash
# Stop services
sudo systemctl stop swarmcracker-manager swarmcracker-worker

# Remove binaries
sudo rm /usr/local/bin/swarmcracker \
        /usr/local/bin/swarmd-firecracker \
        /usr/local/bin/swarmcracker-agent

# Remove state (optional - backs up first)
sudo mv /var/lib/swarmkit /var/lib/swarmkit.backup
sudo rm -rf /var/lib/firecracker
sudo rm -rf /etc/swarmcracker

# Remove systemd services
sudo rm /etc/systemd/system/swarmcracker-*.service
sudo systemctl daemon-reload
```

---

## See Also

- [Cluster Init Guide](./cluster-init.md) - Detailed `swarmcracker init`/`join` usage
- [Installation](./installation.md) - Manual installation steps
- [Architecture](../architecture/overview.md) - System design
