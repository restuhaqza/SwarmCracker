#!/bin/bash
# setup-manager.sh - Configure and start SwarmKit manager
# Run on manager VM only

set -e

# Ensure Go is in PATH
export PATH=$PATH:/usr/local/go/bin

echo "🚀 Setting up SwarmKit Manager..."

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

# Build swarmctl
echo "🔨 Building swarmctl..."
# Be explicit - we want swarmd/cmd/swarmctl/main.go
SWARMCTL_MAIN="./swarmd/cmd/swarmctl/main.go"

if [ ! -f "$SWARMCTL_MAIN" ]; then
  echo "❌ Could not find swarmctl main.go at $SWARMCTL_MAIN"
  exit 1
fi

echo "Found: $SWARMCTL_MAIN"
SWARMCTL_DIR=$(dirname "$SWARMCTL_MAIN")
echo "Building from: $SWARMCTL_DIR"

cd "$SWARMCTL_DIR"
go build -o /tmp/swarmkit/swarmd/bin/swarmctl .

if [ $? -ne 0 ]; then
  echo "❌ Failed to build swarmctl"
  exit 1
fi

cd /tmp/swarmkit

# Verify binaries
if [ ! -f "swarmd/bin/swarmd" ]; then
  echo "❌ swarmd binary not found"
  exit 1
fi

if [ ! -f "swarmd/bin/swarmctl" ]; then
  echo "❌ swarmctl binary not found"
  exit 1
fi

# Stop swarmd service if running (to avoid "Text file busy" error)
if systemctl is-active --quiet swarmd 2>/dev/null; then
  echo "🛑 Stopping swarmd service to update binaries..."
  systemctl stop swarmd
  sleep 2
fi

# Install binaries
cp swarmd/bin/swarmd /usr/local/bin/
cp swarmd/bin/swarmctl /usr/local/bin/
chmod +x /usr/local/bin/swarmd /usr/local/bin/swarmctl

echo "✅ SwarmKit binaries installed"

# Verify swarmctl binary (swarmd has no --version flag)
echo "🔍 Verifying swarmctl binary..."
/usr/local/bin/swarmctl --version 2>&1 || echo "swarmctl version check failed"

# Build SwarmCracker
echo "📦 Building SwarmCracker..."
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

go build -o /tmp/swarmcracker-binary ./cmd/swarmcracker/
cp /tmp/swarmcracker-binary /usr/local/bin/swarmcracker
chmod +x /usr/local/bin/swarmcracker

# Create SwarmCracker config directory
mkdir -p /etc/swarmcracker

# Create SwarmCracker config (manager doesn't need it but good for testing)
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
  level: "info"
  format: "text"
EOF

# Prepare socket directory using helper script
echo "🔧 Preparing socket directory..."
bash /tmp/scripts/prepare-socket.sh

# Initialize SwarmKit cluster data directory
echo "🔧 Initializing SwarmKit manager..."
# Clean up any old state completely
systemctl stop swarmd 2>/dev/null || true
rm -rf /var/lib/swarmkit/manager/*
rm -f /var/run/swarmkit/swarm.sock
mkdir -p /var/lib/swarmkit/manager
mkdir -p /var/run/swarmkit

# Create systemd service for swarmd manager with socket permission fix
cat > /etc/systemd/system/swarmd.service <<EOF
[Unit]
Description=SwarmKit Manager
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmd \\
  -d /var/lib/swarmkit/manager \\
  --listen-control-api /var/run/swarmkit/swarm.sock \\
  --hostname swarm-manager \\
  --listen-remote-api 0.0.0.0:4242
ExecStartPost=-/bin/chmod 666 /var/run/swarmkit/swarm.sock
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# Enable and start service
echo "🔧 Starting swarmd service..."
systemctl daemon-reload
systemctl enable swarmd
systemctl start swarmd

# Wait for service to start
echo "⏳ Waiting for SwarmKit manager to start..."
sleep 15

# Check if manager is running
if systemctl is-active --quiet swarmd; then
  echo "✅ SwarmKit manager service is active"
else
  echo "❌ Failed to start SwarmKit manager"
  echo "Service status:"
  systemctl status swarmd --no-pager || true
  echo "Service logs:"
  journalctl -u swarmd -n 50 --no-pager
  exit 1
fi

# Get cluster info and join tokens
echo ""
echo "📋 Cluster Information:"
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock

# Wait for socket to be ready (with timeout)
echo "⏳ Waiting for socket file..."
SOCKET_READY=0
for i in {1..60}; do
  if [ -S /var/run/swarmkit/swarm.sock ]; then
    echo "✅ Swarm socket is ready at $SWARM_SOCKET"
    SOCKET_READY=1
    break
  fi
  echo "Waiting for socket... ($i/60)"
  sleep 1
done

if [ $SOCKET_READY -eq 0 ]; then
  echo "❌ Socket file not created after 60 seconds"
  echo "Checking service logs..."
  journalctl -u swarmd -n 100 --no-pager
  echo "Checking /var/run/swarmkit:"
  ls -lah /var/run/swarmkit/ || true
  exit 1
fi

# Display cluster info
sleep 2
swarmctl cluster inspect default || true

# Extract and save join tokens
echo ""
echo "🔑 Join Tokens:"
echo "---"
swarmctl cluster inspect default | grep -A 2 "JoinTokens" || true

# Save tokens to file for workers to fetch
swarmctl cluster inspect default > /tmp/cluster-info.txt
WORKER_TOKEN=$(grep "Worker:" /tmp/cluster-info.txt | awk '{print $2}' | tr -d '[:space:]')
MANAGER_TOKEN=$(grep "Manager:" /tmp/cluster-info.txt | awk '{print $2}' | tr -d '[:space:]')

echo ""
echo "WORKER_TOKEN=$WORKER_TOKEN" > /etc/swarmcracker/tokens.env
echo "MANAGER_TOKEN=$MANAGER_TOKEN" >> /etc/swarmcracker/tokens.env
chmod 644 /etc/swarmcracker/tokens.env

# Serve tokens via HTTP for workers
echo "🌐 Starting HTTP server for token distribution..."
mkdir -p /tmp/swarm-tokens
# Create a JSON file that workers can easily parse
cat > /tmp/swarm-tokens/tokens.json <<EOF
{
  "worker": "$WORKER_TOKEN",
  "manager": "$MANAGER_TOKEN"
}
EOF
chmod 644 /tmp/swarm-tokens/tokens.json

# Kill any existing python http server on port 8000
pkill -f "http.server 8000" || true

# Start python http server in background
cd /tmp/swarm-tokens
nohup python3 -m http.server 8000 > /tmp/token-server.log 2>&1 &
echo "✅ Token server running on port 8000"

echo "✅ Tokens saved to /etc/swarmcracker/tokens.env"
echo ""
echo "=========================================="
echo "🎉 Manager setup complete!"
echo "=========================================="
echo ""
echo "Manager IP: 192.168.56.10"
echo "Manager API: http://192.168.56.10:4242"
echo ""
echo "Next steps:"
echo "1. Workers will auto-join using tokens"
echo "2. From manager: vagrant ssh manager"
echo "3. Check nodes: swarmctl node ls"
echo ""
