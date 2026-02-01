# Manual Worker Join Instructions

## Quick Start - Join Worker1 to Cluster

If the automated setup is stuck, you can manually join worker1:

### 1. SSH into Worker1
```bash
cd projects/swarmcracker/test-automation
vagrant ssh worker1
```

### 2. Create Worker Config
```bash
sudo mkdir -p /etc/swarmcracker

sudo tee /etc/swarmcracker/config.yaml > /dev/null <<EOF
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
  rootfs_dir: "/var/lib/firecracker/rootfs"
  default_vcpus: 2
  default_memory_mb: 2048

network:
  bridge_name: "swarm-br0"
  bridge_ip: "192.168.127.1/24"
  dhcp_enabled: true

logging:
  level: "debug"
  format: "text"
EOF
```

### 3. Get Join Token from Manager
```bash
# From manager node
vagrant ssh manager -c "
sudo swarmctl -s /var/run/swarmkit/swarm.sock cluster inspect default
"

# Copy the Worker token
# Example: SWMTKN-1-0ez8b0cbdw56pp79c1zxmtnbr1dywdlvmp7z23u3ufut5gkezr-d1gvmdj71sdxtzw3u78ym8yi5
```

### 4. Create Swarmd Service
```bash
# Replace WORKER_TOKEN with actual token from step 3
WORKER_TOKEN="SWMTKN-1-your-actual-token-here"

sudo tee /etc/systemd/system/swarmd.service > /dev/null <<EOF
[Unit]
Description=SwarmKit Worker
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/swarmd \\
  -d /var/lib/swarmkit/worker \\
  --hostname swarm-worker-1 \\
  --join-addr 192.168.56.10:4242 \\
  --join-token $WORKER_TOKEN \\
  --listen-remote-api 0.0.0.0:4243
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF
```

### 4. Start Swarmd
```bash
sudo systemctl daemon-reload
sudo systemctl enable swarmd
sudo systemctl start swarmd
```

### 5. Verify
```bash
# Check worker is running
sudo systemctl status swarmd

# Check from manager - worker should appear
# (Exit worker first)
exit

# From host or manager:
vagrant ssh manager -c "
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
swarmctl node ls
"
```

## Expected Output

Worker should appear in node list:
```
ID                         NAME             MEMBERSHIP  STATUS  AVAILABILITY  MANAGER STATUS
zh2c2ev4uu2khtbetkdnmsyta   swarm-manager    ACCEPTED    READY   ACTIVE        REACHABLE *
xxxxxx                     swarm-worker-1   ACCEPTED    READY   ACTIVE
```

## Troubleshooting

### Worker fails to join

**Check connectivity:**
```bash
# From worker1
ping 192.168.56.10
curl http://192.168.56.10:4242
```

**Check worker logs:**
```bash
sudo journalctl -u swarmd -f
```

**Check manager logs:**
```bash
vagrant ssh manager -c "sudo journalctl -u swarmd -f"
```

### Invalid token error

Token expires or changes when manager restarts. Get fresh token:
```bash
vagrant ssh manager -c "
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
swarmctl cluster inspect default | grep -A2 'JoinTokens'
"
```

### Firecracker not found

Install Firecracker:
```bash
cd /tmp
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.14.1/firecracker-v1.14.1-x86_64.tgz
tar -xzf firecracker-v1.14.1-x86_64.tgz
sudo mv release-v1.14.1-x86_64/firecracker-v1.14.1-x86_64 /usr/local/bin/firecracker
sudo chmod +x /usr/local/bin/firecracker
firecracker --version
```

## Join Worker2 (Same Process)

Repeat the same steps for worker2 with:
- Hostname: `swarm-worker-2`
- IP: `192.168.56.12`

First create worker2:
```bash
vagrant up worker2
```

Then repeat steps 1-5 above.

---

**Last Updated:** 2026-02-01
**Status:** Manager Ready, Workers Pending Join
