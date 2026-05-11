#!/bin/bash
# Deployment script for swarmd-firecracker agent
# Usage: ./scripts/deploy-firecracker-agent.sh <worker-host> [join-token]

set -e

WORKER_HOST="${1:-192.168.56.11}"
JOIN_TOKEN="${2:-}"

# Validate join token is provided and not a placeholder
if [ -z "$JOIN_TOKEN" ]; then
    echo "Error: Join token is required. Usage: $0 <worker-host> <join-token>" >&2
    exit 1
fi
if [ "$JOIN_TOKEN" = "REQUIRED" ] || [ "$JOIN_TOKEN" = "<join-token>" ]; then
    echo "Error: Join token must be a valid token, not a placeholder. Usage: $0 <worker-host> <join-token>" >&2
    exit 1
fi
MANAGER_ADDR="192.168.56.10:4242"
FIRECRACKER_VERSION="v1.14.1"

echo "🚀 Deploying swarmd-firecracker to ${WORKER_HOST}..."

# Check if binary is built
if [ ! -f "./build/swarmd-firecracker" ]; then
    echo "❌ Binary not found. Building..."
    make swarmd-firecracker
fi

echo "📦 Creating deployment package..."
DEPLOY_DIR="/tmp/swarmcracker-deploy-$$"
mkdir -p "$DEPLOY_DIR"

# Copy binary
cp ./build/swarmd-firecracker "$DEPLOY_DIR/"

# Create systemd service file
cat > "$DEPLOY_DIR/swarmd-firecracker.service" << EOF
[Unit]
Description=SwarmKit Agent with Firecracker Executor
After=network.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/swarmd-firecracker \\
  --hostname worker-firecracker \\
  --join-addr ${MANAGER_ADDR} \\
  --join-token ${JOIN_TOKEN} \\
  --listen-remote-api 0.0.0.0:4242 \\
  --kernel-path /usr/share/firecracker/vmlinux \\
  --rootfs-dir /var/lib/firecracker/rootfs \\
  --socket-dir /var/run/firecracker \\
  --bridge-name swarm-br0

Restart=always
RestartSec=5s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

# Create setup script
cat > "$DEPLOY_DIR/setup.sh" << 'SETUP_EOF'
#!/bin/bash
set -e

echo "🔧 Setting up directories..."
mkdir -p /var/lib/firecracker/rootfs
mkdir -p /var/run/firecracker
mkdir -p /var/lib/swarmkit
mkdir -p /var/run/swarmkit
mkdir -p /usr/share/firecracker

echo "📥 Installing binary..."
cp swarmd-firecracker /usr/local/bin/
chmod +x /usr/local/bin/swarmd-firecracker

echo "🔥 Installing Firecracker..."
if ! command -v firecracker &> /dev/null; then
    cd /tmp
    wget -q https://github.com/firecracker-microvm/firecracker/releases/download/v1.14.1/firecracker-v1.14.1-x86_64.tgz
    tar -xzf firecracker-v1.14.1-x86_64.tgz
    mv release-v1.14.1-x86_64/firecracker-v1.14.1-x86_64 /usr/bin/firecracker
    mv release-v1.14.1-x86_64/jailer-v1.14.1-x86_64 /usr/bin/jailer
    chmod +x /usr/bin/firecracker /usr/bin/jailer
    rm -rf firecracker-v1.14.1-x86_64.tgz release-v1.14.1-x86_64
    echo "✅ Firecracker installed"
else
    echo "✅ Firecracker already installed"
fi

echo "🐧 Downloading kernel..."
if [ ! -f /usr/share/firecracker/vmlinux ]; then
    wget -q https://github.com/firecracker-microvm/firecracker/releases/download/v1.14.1/vmlinux-v5.15-x86_64.bin -O /usr/share/firecracker/vmlinux
    echo "✅ Kernel downloaded"
else
    echo "✅ Kernel already exists"
fi

echo "🌐 Setting up bridge..."
if ! ip link show swarm-br0 &> /dev/null; then
    ip link add name swarm-br0 type bridge
    ip addr add 192.168.127.1/24 dev swarm-br0
    ip link set swarm-br0 up
    echo "✅ Bridge created"
else
    echo "✅ Bridge already exists"
fi

echo "🛑 Stopping old swarmd (if running)..."
systemctl stop swarmd 2>/dev/null || true
systemctl disable swarmd 2>/dev/null || true

echo "📋 Installing systemd service..."
cp swarmd-firecracker.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable swarmd-firecracker

echo "▶️  Starting swarmd-firecracker..."
systemctl start swarmd-firecracker

echo ""
echo "✅ Setup complete!"
echo "📊 Check status: systemctl status swarmd-firecracker"
echo "📝 View logs: journalctl -u swarmd-firecracker -f"
SETUP_EOF

chmod +x "$DEPLOY_DIR/setup.sh"

# Create tarball
echo "📦 Creating deployment tarball..."
tar czf "$DEPLOY_DIR.tgz" -C "$DEPLOY_DIR" .
DEPLOY_TAR="$DEPLOY_DIR.tgz"

echo ""
echo "📤 To deploy, run the following on your machine:"
echo ""
echo "  scp $DEPLOY_TAR root@${WORKER_HOST}:/tmp/"
echo "  ssh root@${WORKER_HOST} 'cd /tmp && tar xzf $(basename $DEPLOY_TAR) && ./setup.sh'"
echo ""
echo "Or if you have direct access:"
echo ""

# Try to deploy directly if we can SSH
if ssh -o ConnectTimeout=2 -o BatchMode=yes root@${WORKER_HOST} exit 2>/dev/null; then
    echo "✅ SSH access available, deploying directly..."
    scp "$DEPLOY_TAR" "root@${WORKER_HOST}:/tmp/"
    ssh "root@${WORKER_HOST}" "cd /tmp && tar xzf $(basename $DEPLOY_TAR) && ./setup.sh"
    rm -rf "$DEPLOY_DIR" "$DEPLOY_TAR"
    echo ""
    echo "✅ Deployment complete!"
    echo "📊 Check status: ssh root@${WORKER_HOST} 'systemctl status swarmd-firecracker'"
else
    echo "❌ Cannot SSH directly. Manual deployment required:"
    echo ""
    echo "  1. Copy $DEPLOY_TAR to ${WORKER_HOST}:/tmp/"
    echo "  2. SSH into ${WORKER_HOST}"
    echo "  3. Run: cd /tmp && tar xzf $(basename $DEPLOY_TAR) && ./setup.sh"
    echo ""
    echo "📦 Deployment package: $DEPLOY_TAR"
fi
