# SwarmCracker Agent Setup Guide

## Quick Start - Setelah Build Selesai

### 1. Copy Binary ke Worker
```bash
# Dari host
cd projects/swarmcracker
vagrant upload swarmcracker-agent /tmp/swarmcracker-agent --name worker1
```

### 2. Install di Worker
```bash
vagrant ssh worker1 -c "
sudo cp /tmp/swarmcracker-agent /usr/local/bin/
sudo chmod +x /usr/local/bin/swarmcracker-agent
swarmcracker-agent --version
"
```

### 3. Stop swarmd Worker
```bash
vagrant ssh worker1 -c "
sudo systemctl stop swarmd
sudo systemctl disable swarmd
"
```

### 4. Buat Konfigurasi Agent
```bash
vagrant ssh worker1 -c "
sudo mkdir -p /etc/swarmcracker

sudo tee /etc/swarmcracker/config.yaml > /dev/null <<'EOF'
firecracker_path: '/usr/local/bin/firecracker'
kernel_path: '/usr/share/firecracker/vmlinux'
rootfs_dir: '/var/lib/firecracker/rootfs'
socket_dir: '/var/run/firecracker'
default_vcpus: 2
default_memory_mb: 2048
bridge_name: 'swarm-br0'
debug: true
EOF
"
```

### 5. Get Worker Token dari Manager
```bash
vagrant ssh manager -c "
sudo swarmctl -s /var/run/swarmkit/swarm.sock cluster inspect default | grep Worker
"
```

### 6. Buat Systemd Service untuk Agent
```bash
vagrant ssh worker1 -c "
sudo tee /etc/systemd/system/swarmcracker-agent.service > /dev/null <<'EOF'
[Unit]
Description=SwarmCracker Agent - Firecracker Executor for SwarmKit
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmcracker-agent \\
  --config /etc/swarmcracker/config.yaml \\
  --manager-addr 192.168.56.10:4242 \\
  --join-token SWMTKN-1-0ez8b0cbdw56pp79c1zxmtnbr1dywdlvmp7z23u3ufut5gkezr-d1gvmdj71sdxtzw3u78ym8yi5 \\
  --foreign-id swarm-worker-1 \\
  --state-dir /var/lib/swarmcracker
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable swarmcracker-agent
sudo systemctl start swarmcracker-agent
"
```

### 7. Verify Agent Running
```bash
vagrant ssh worker1 -c "
sudo systemctl status swarmcracker-agent --no-pager | head -15
"
```

### 8. Test Deploy Service
```bash
# Dari manager
vagrant ssh manager -c "
sudo swarmctl -s /var/run/swarmkit/swarm.sock service create \\
  --name nginx \\
  --image nginx:alpine \\
  --replicas 2

sleep 5

sudo swarmctl -s /var/run/swarmkit/swarm.sock task ls
"
```

### Expected Output - Tasks RUNNING!
```
ID    Service  Desired State  Last State    Node
----  -------  -------------  ----------   ----
xxx   nginx.1  READY          RUNNING       worker1  ← RUNNING! ✅
xxx   nginx.2  READY          RUNNING       manager  ← RUNNING! ✅
```

## Arsitektur

```
┌─────────────────────────────────────┐
│  Manager Node (swarmd)             │
│  - Orchestrates tasks              │
│  - Schedules to workers            │
└─────────────────────────────────────┘
              ↓ gRPC
┌─────────────────────────────────────┐
│  Worker Node (swarmcracker-agent)  │
│  - Receives tasks via gRPC         │
│  - SwarmCracker Executor            │
│    ├─ Image Preparer               │
│    ├─ Network Manager              │
│    └─ VMM Manager                  │
└─────────────────────────────────────┘
              ↓
┌─────────────────────────────────────┐
│  Firecracker VMM                   │
│  - MicroVM per task                │
│  - KVM isolation                   │
│  - Own kernel                      │
└─────────────────────────────────────┘
```

## Troubleshooting

### Agent fails to start
```bash
# Check logs
vagrant ssh worker1 -c "
sudo journalctl -u swarmcracker-agent -n 50 --no-pager
"
```

### Tasks still REJECTED
```bash
# Verify agent is registered with manager
vagrant ssh manager -c "
sudo swarmctl -s /var/run/swarmkit/swarm.sock node ls
"

# Check agent logs
vagrant ssh worker1 -c "
sudo journalctl -u swarmcracker-agent -f
"
```

### Network bridge issues
```bash
# Check bridge exists
vagrant ssh worker1 -c "
ip link show sw-arm0
brctl show
"
```

---

**Last Updated:** 2026-02-01
**Status:** Pending Agent Binary Build
