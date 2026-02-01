#!/bin/bash
# prepare-socket.sh - Ensure SwarmKit socket directory and file exist
# Run on all nodes (manager and workers)

set -e

echo "🔧 Preparing SwarmKit socket environment..."

# Create socket directory with proper permissions
SOCKET_DIR="/var/run/swarmkit"
SOCKET_FILE="$SOCKET_DIR/swarm.sock"

# Remove old socket if it exists (stale from previous run)
if [ -S "$SOCKET_FILE" ]; then
  echo "🧹 Removing old socket file..."
  rm -f "$SOCKET_FILE"
fi

# Create directory
sudo mkdir -p "$SOCKET_DIR"
sudo chmod 755 "$SOCKET_DIR"

# Create tmpfiles.d configuration for persistence across reboots
echo "📝 Creating tmpfiles.d configuration..."
sudo tee /etc/tmpfiles.d/swarmkit.conf > /dev/null <<EOF
# Create SwarmKit socket directory at boot
d /var/run/swarmkit 0755 root root -
EOF

echo "✅ Socket directory prepared: $SOCKET_DIR"
echo "✅ Tmpfiles configuration created: /etc/tmpfiles.d/swarmkit.conf"
echo ""
echo "Note: The actual socket file will be created by swarmd when it starts."
echo "This script only ensures the directory exists with proper permissions."
