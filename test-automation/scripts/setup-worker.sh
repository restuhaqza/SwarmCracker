#!/bin/bash
# setup-worker.sh - Configure and start SwarmKit worker with SwarmCracker
# Run on worker VMs

set -e

# Ensure Go is in PATH
export PATH=$PATH:/usr/local/go/bin

echo "🚀 Setting up SwarmKit Worker..."

# Get worker number from hostname
WORKER_NUM=$(hostname | grep -oP 'worker-\K\d+')
WORKER_INDEX=${WORKER_NUM:-1}

echo "Worker index: $WORKER_INDEX"

# Build SwarmKit from source
echo "📦 Building SwarmKit..."
cd /tmp
if [ ! -d "swarmkit" ]; then
  git clone https://github.com/moby/swarmkit.git
fi
cd swarmkit
git pull

echo "Go version: $(go version)"
echo "Exploring SwarmKit structure..."
ls -la

# Find the actual main.go locations
echo "Looking for swarmd main package..."
# Be explicit - we want swarmd/cmd/swarmd/main.go, NOT external-ca-example
SWARMMD_MAIN="./swarmd/cmd/swarmd/main.go"

if [ ! -f "$SWARMMD_MAIN" ]; then
  echo "❌ Could not find swarmd main.go at $SWARMMD_MAIN"
  find . -name "main.go" | head -10
  exit 1
fi

echo "Found: $SWARMMD_MAIN"
SWARMMD_DIR=$(dirname "$SWARMMD_MAIN")
echo "Building from: $SWARMMD_DIR"

# Build swarmd
echo "🔨 Building swarmd..."
mkdir -p swarmd/bin
cd "$SWARMMD_DIR"
go build -o /tmp/swarmkit/swarmd/bin/swarmd .

if [ $? -ne 0 ]; then
  echo "❌ Failed to build swarmd"
  exit 1
fi

cd /tmp/swarmkit

# Verify binary
if [ ! -f "swarmd/bin/swarmd" ]; then
  echo "❌ swarmd binary not found"
  exit 1
fi

# Stop swarmd service if running (to avoid "Text file busy" error)
if systemctl is-active --quiet swarmd 2>/dev/null; then
  echo "🛑 Stopping swarmd service to update binary..."
  systemctl stop swarmd
  sleep 2
fi

# Install binary
cp swarmd/bin/swarmd /usr/local/bin/
chmod +x /usr/local/bin/swarmd

echo "✅ SwarmKit binary installed"

# Build SwarmCracker
echo "📦 Building SwarmCracker..."
cd /tmp
if [ ! -d "swarmcracker" ]; then
  git clone https://github.com/restuhaqza/swarmcracker.git
fi
cd swarmcracker
git pull
go build -o swarmcracker ./cmd/swarmcracker/
cp swarmcracker /usr/local/bin/
chmod +x /usr/local/bin/swarmcracker

# Create SwarmCracker config directory
mkdir -p /etc/swarmcracker

# Create SwarmCracker config
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

# Validate config
echo "🔍 Validating SwarmCracker config..."
swarmcracker validate --config /etc/swarmcracker/config.yaml

# Prepare socket directory (for consistency, though workers don't expose control API)
echo "🔧 Preparing socket directory..."
bash /tmp/scripts/prepare-socket.sh

# Fetch tokens from manager
echo "📥 Fetching join tokens from manager..."
MAX_RETRIES=10
RETRY_COUNT=0

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
  if curl -s http://192.168.56.10:4242 > /dev/null 2>&1; then
    # Fetch tokens via HTTP API
    TOKENS=$(curl -s http://192.168.56.10:4242/cluster/default 2>/dev/null | jq -r '.JoinTokens.Worker // empty')
    
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
  echo "Please manually join the worker:"
  echo "  swarmd -d /var/lib/swarmkit/worker --hostname $(hostname) \\"
  echo "    --join-addr 192.168.56.10:4242 \\"
  echo "    --join-token <WORKER_TOKEN> \\"
  echo "    --listen-remote-api 0.0.0.0:4242 \\"
  echo "    --executor firecracker \\"
  echo "    --executor-config /etc/swarmcracker/config.yaml \\"
  echo "    --debug"
  exit 1
fi

# Create systemd service for swarmd worker
# NOTE: Upstream SwarmKit doesn't support --executor flag (Docker-specific)
# Firecracker integration requires custom agent/executor layer
cat > /etc/systemd/system/swarmd.service <<EOF
[Unit]
Description=SwarmKit Worker
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/swarmd \\
  -d /var/lib/swarmkit/worker \\
  --hostname $(hostname) \\
  --join-addr 192.168.56.10:4242 \\
  --join-token $WORKER_TOKEN \\
  --listen-remote-api 0.0.0.0:4243
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

echo ""
echo "=========================================="
echo "🎉 Worker setup complete!"
echo "=========================================="
echo ""
echo "Worker hostname: $(hostname)"
echo "Worker IP: 192.168.56.1${WORKER_INDEX}"
echo "Joined cluster: 192.168.56.10:4242"
echo ""
