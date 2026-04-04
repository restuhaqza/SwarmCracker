#!/bin/bash
# setup-worker-firecracker.sh - Configure SwarmKit worker with Firecracker executor
# This is the NEW approach using the fixed swarmd-firecracker binary

set -e

echo "🚀 Setting up SwarmKit Worker with Firecracker Executor..."

# Get worker number from hostname
WORKER_NUM=$(hostname | grep -oP 'worker-\K\d+')
WORKER_INDEX=${WORKER_NUM:-1}

echo "Worker index: $WORKER_INDEX"

# Step 1: Install dependencies
echo "📦 Installing dependencies..."
apt-get update
apt-get install -y curl jq git vim podman

# Step 2: Build SwarmCracker swarmd-firecracker
echo "🔨 Building swarmd-firecracker..."
if [ -d "/swarmcracker" ]; then
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

echo "Building swarmd-firecracker..."
go build -o /tmp/swarmd-firecracker ./cmd/swarmd-firecracker/

if [ $? -ne 0 ]; then
  echo "❌ Failed to build swarmd-firecracker"
  exit 1
fi

# Install binary
cp /tmp/swarmd-firecracker /usr/local/bin/swarmd-firecracker
chmod +x /usr/local/bin/swarmd-firecracker

echo "✅ swarmd-firecracker installed"

# Step 3: Prepare directories
echo "🔧 Preparing directories..."
mkdir -p /var/lib/firecracker/rootfs
mkdir -p /var/run/firecracker
mkdir -p /var/lib/swarmkit/worker

# Step 4: Fetch tokens from manager
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
  exit 1
fi

# Step 5: Create systemd service for swarmd-firecracker
echo "🔧 Creating systemd service..."
cat > /etc/systemd/system/swarmd-firecracker.service <<EOF
[Unit]
Description=SwarmKit Worker with Firecracker Executor
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmd-firecracker \\
  --state-dir /var/lib/swarmkit/worker \\
  --hostname $(hostname) \\
  --join-addr 192.168.56.10:4242 \\
  --join-token $WORKER_TOKEN \\
  --listen-remote-api 0.0.0.0:4243 \\
  --kernel-path /usr/share/firecracker/vmlinux \\
  --rootfs-dir /var/lib/firecracker/rootfs \\
  --socket-dir /var/run/firecracker \\
  --default-vcpus 2 \\
  --default-memory 2048 \\
  --bridge-name swarm-br0 \\
  --debug
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# Step 6: Enable and start service
echo "🚀 Starting swarmd-firecracker..."
systemctl daemon-reload
systemctl enable swarmd-firecracker
systemctl restart swarmd-firecracker

# Wait for service to start
echo "⏳ Waiting for swarmd-firecracker to start..."
sleep 10

# Check if worker is running
if systemctl is-active --quiet swarmd-firecracker; then
  echo "✅ swarmd-firecracker is running"
else
  echo "❌ Failed to start swarmd-firecracker"
  journalctl -u swarmd-firecracker -n 50
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
echo "Executor: Firecracker"
echo ""
