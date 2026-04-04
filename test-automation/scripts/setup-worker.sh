#!/bin/bash
# setup-worker.sh - Configure and start SwarmKit worker with SwarmCracker
# Run on worker VMs

set -e

# Ensure Go is in PATH
export PATH=$PATH:/usr/local/go/bin

echo "🚀 Setting up SwarmKit Worker..."

# Get worker number from hostname
WORKER_NUM=$(hostname | grep -oP 'swarm-worker-\K\d+')
WORKER_INDEX=${WORKER_NUM:-1}

echo "Worker index: $WORKER_INDEX"

# Get manager private IP dynamically
MANAGER_PRIVATE_IP=$(getent hosts swarmcracker-manager | awk '{ print $1 }' | head -1)
if [ -z "$MANAGER_PRIVATE_IP" ]; then
  # Fallback: try to fetch from DNS or use hardcoded value
  echo "⚠️  Could not resolve swarmcracker-manager, trying common private IPs..."
  # Try to fetch manager IP from the token server
  for ip in 10.104.0.6 10.15.0.8 192.168.56.10; do
    if curl -s --connect-timeout 2 "http://$ip:8000/tokens.json" > /dev/null 2>&1; then
      MANAGER_PRIVATE_IP="$ip"
      echo "✅ Found manager at $MANAGER_PRIVATE_IP"
      break
    fi
  done
fi

if [ -z "$MANAGER_PRIVATE_IP" ]; then
  echo "❌ Could not determine manager IP address"
  exit 1
fi

echo "Manager Private IP: $MANAGER_PRIVATE_IP"

# Build SwarmCracker and Custom Agent
echo "📦 Building SwarmCracker & Agent..."
if [ -d "/tmp/swarmcracker" ]; then
  echo "📂 Found local SwarmCracker source at /tmp/swarmcracker"
  cd /tmp/swarmcracker
elif [ -d "/swarmcracker" ]; then
  echo "📂 Found local SwarmCracker source at /swarmcracker"
  cd /swarmcracker
else
  echo "☁️ Cloning SwarmCracker from GitHub..."
  cd /tmp
  if [ ! -d "swarmcracker" ]; then
    git clone https://github.com/restuhaqza/swarmcracker.git
  fi
  cd swarmcracker
  git pull
fi

# Build CLI
go build -o /tmp/swarmcracker-binary ./cmd/swarmcracker/
cp /tmp/swarmcracker-binary /usr/local/bin/swarmcracker
chmod +x /usr/local/bin/swarmcracker

# Build Custom Agent (swarmd-firecracker)
go build -o /tmp/swarmd-firecracker-binary ./cmd/swarmd-firecracker/
cp /tmp/swarmd-firecracker-binary /usr/local/bin/swarmd-firecracker
chmod +x /usr/local/bin/swarmd-firecracker

echo "✅ SwarmCracker binaries installed"

# Create SwarmCracker config directory
mkdir -p /etc/swarmcracker

# Create SwarmCracker config (reference only, agent uses flags)
cat > /etc/swarmcracker/config.yaml <<EOF
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

# Validate config (skip if kernel is missing or too small)
echo "🔍 Validating SwarmCracker config..."
KERNEL_SIZE=$(stat -f%z "/usr/share/firecracker/vmlinux" 2>/dev/null || stat -c%s "/usr/share/firecracker/vmlinux" 2>/dev/null || echo "0")
if [ -s "/usr/share/firecracker/vmlinux" ] && [ "$KERNEL_SIZE" -gt 1000000 ]; then
  swarmcracker validate --config /etc/swarmcracker/config.yaml
else
  echo "⚠️  Skipping validation - kernel not installed or too small (size: $KERNEL_SIZE bytes)"
fi

# Prepare socket directory (for consistency)
echo "🔧 Preparing socket directory..."
bash /tmp/scripts/prepare-socket.sh

# Fetch tokens from manager
echo "📥 Fetching join tokens from manager..."
MAX_RETRIES=30  # Increased retries
RETRY_COUNT=0

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
  if curl -s "http://${MANAGER_PRIVATE_IP}:8000/tokens.json" > /dev/null 2>&1; then
    # Fetch tokens via temporary HTTP server
    TOKENS=$(curl -s "http://${MANAGER_PRIVATE_IP}:8000/tokens.json" 2>/dev/null | jq -r '.worker // empty')
    
    if [ -n "$TOKENS" ] && [ "$TOKENS" != "null" ]; then
      WORKER_TOKEN="$TOKENS"
      echo "✅ Retrieved worker token"
      break
    fi
  fi
  
  RETRY_COUNT=$((RETRY_COUNT + 1))
  echo "⏳ Waiting for manager... ($RETRY_COUNT/$MAX_RETRIES)"
  sleep 5
done

if [ -z "$WORKER_TOKEN" ] || [ "$WORKER_TOKEN" = "null" ]; then
  echo "❌ Failed to retrieve worker token from manager"
  exit 1
fi

# Create systemd service for swarmd-firecracker
cat > /etc/systemd/system/swarmd.service <<EOF
[Unit]
Description=SwarmKit Worker (Firecracker)
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/swarmd-firecracker \\
  --state-dir /var/lib/swarmkit/worker \\
  --hostname $(hostname) \\
  --join-addr ${MANAGER_PRIVATE_IP}:4242 \\
  --join-token $WORKER_TOKEN \\
  --listen-remote-api 0.0.0.0:4243 \\
  --kernel-path /usr/share/firecracker/vmlinux \\
  --rootfs-dir /var/lib/firecracker/rootfs \\
  --bridge-name swarm-br0 \\
  --debug
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# Enable and start service
systemctl daemon-reload
systemctl enable swarmd
systemctl restart swarmd

# Wait for service to start
echo "⏳ Waiting for SwarmKit worker to start..."
sleep 10

# Check if worker is running
if systemctl is-active --quiet swarmd; then
  echo "✅ SwarmKit worker is running"
else
  echo "❌ Failed to start SwarmKit worker"
  journalctl -u swarmd -n 50
  exit 1
fi

# Setup VXLAN overlay for cross-node communication
echo ""
echo "🌐 Setting up VXLAN overlay network..."

# Get physical interface and local IP
PHYS_INTERFACE=$(ip route | grep "${MANAGER_PRIVATE_IP%.*}" | head -1 | awk '{print $3}')
LOCAL_IP=$(ip addr show "$PHYS_INTERFACE" | grep "inet " | awk '{print $2}' | cut -d'/' -f1)

# Calculate overlay IP based on worker index
OVERLAY_BASE="10.30"
OVERLAY_HOST=$((100 + WORKER_INDEX))
OVERLAY_IP="${OVERLAY_BASE}.${OVERLAY_HOST}.1/24"

# Get peer IPs (other workers)
PEER_IPS=()
if [ "$WORKER_INDEX" -eq 1 ]; then
  # Worker 1 peers with worker 2
  PEER_IPS+=("192.168.56.12")
elif [ "$WORKER_INDEX" -eq 2 ]; then
  # Worker 2 peers with worker 1
  PEER_IPS+=("192.168.56.11")
fi

# Run VXLAN setup script if it exists
if [ -f "/vagrant/scripts/setup-vxlan-overlay.sh" ]; then
  bash /vagrant/scripts/setup-vxlan-overlay.sh \
    "swarm-br0" \
    "$OVERLAY_IP" \
    100 \
    "$PHYS_INTERFACE" \
    "$LOCAL_IP" \
    "${PEER_IPS[@]}"
  
  # Add routes to remote worker VM subnets
  if [ "$WORKER_INDEX" -eq 1 ]; then
    echo "Adding route to worker2 VM subnet..."
    ip route add 192.168.128.0/24 via ${OVERLAY_BASE}.102 dev swarm-br0 2>/dev/null || true
  elif [ "$WORKER_INDEX" -eq 2 ]; then
    echo "Adding route to worker1 VM subnet..."
    ip route add 192.168.127.0/24 via ${OVERLAY_BASE}.101 dev swarm-br0 2>/dev/null || true
  fi
else
  echo "⚠️  VXLAN setup script not found - skipping overlay configuration"
fi

echo ""
echo "=========================================="
echo "🎉 Worker setup complete!"
echo "=========================================="
echo ""
echo "Worker hostname: $(hostname)"
echo "Manager IP: ${MANAGER_PRIVATE_IP}:4242"
echo "Overlay IP: $OVERLAY_IP"
echo ""
