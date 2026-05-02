#!/bin/bash
# Setup script for SwarmCracker nodes
#
# Installs all dependencies and prepares the node for SwarmCracker:
#   - Docker (OCI image extraction fallback)
#   - Firecracker binary
#   - ELF kernel (extracted from host or bundled)
#   - Network bridge for microVMs
#   - SwarmCracker binaries
#
# Usage:
#   ./setup-node.sh [--manager|--worker] [--manager-ip IP]
#
# Options:
#   --manager       Setup as manager node (includes advertise address setup)
#   --worker        Setup as worker node
#   --manager-ip    IP address of manager for advertise (manager only)
#
# Environment:
#   SWARMCRACKER_BIN_DIR  - Path to SwarmCracker binaries (default: ./bin)

set -e

# Parse arguments
ROLE="worker"
MANAGER_IP=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --manager)
            ROLE="manager"
            shift
            ;;
        --worker)
            ROLE="worker"
            shift
            ;;
        --manager-ip)
            MANAGER_IP="$2"
            shift 2
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

echo "=== SwarmCracker Node Setup ==="
echo "Role: $ROLE"
if [ -n "$MANAGER_IP" ]; then
    echo "Manager IP: $MANAGER_IP"
fi

# ============================================
# 1. Install Docker (OCI image extraction)
# ============================================
echo ""
echo "=== Installing Docker ==="
if ! command -v docker &> /dev/null; then
    sudo apt-get update -qq
    sudo apt-get install -y -qq docker.io > /dev/null 2>&1
    sudo systemctl start docker
    sudo systemctl enable docker
fi
echo "Docker: $(sudo docker --version)"

# ============================================
# 2. Install Firecracker
# ============================================
echo ""
echo "=== Installing Firecracker ==="
FC_VERSION="v1.15.1"

if ! command -v firecracker &> /dev/null; then
    sudo mkdir -p /usr/local/bin
    
    # Download Firecracker release
    curl -sL -o /tmp/firecracker.tgz \
        "https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-x86_64.tgz"
    
    cd /tmp
    sudo tar -xzf firecracker.tgz
    
    sudo cp "/tmp/release-${FC_VERSION}-x86_64/firecracker-${FC_VERSION}-x86_64" /usr/local/bin/firecracker
    sudo cp "/tmp/release-${FC_VERSION}-x86_64/jailer-${FC_VERSION}-x86_64" /usr/local/bin/jailer
    sudo chmod +x /usr/local/bin/firecracker /usr/local/bin/jailer
    
    rm -rf /tmp/firecracker.tgz "/tmp/release-${FC_VERSION}-x86_64"
fi

echo "Firecracker: $(/usr/local/bin/firecracker --version 2>&1 | head -1)"

# ============================================
# 3. Setup Kernel
# ============================================
echo ""
echo "=== Setting up Kernel ==="
sudo mkdir -p /usr/share/firecracker

KERNEL_PATH="/usr/share/firecracker/vmlinux"

# Check if kernel already exists and is valid ELF
if [ -f "$KERNEL_PATH" ] && file "$KERNEL_PATH" | grep -q "ELF"; then
    echo "Kernel already installed: $(file $KERNEL_PATH)"
else
    # Try bundled kernel first
    BUNDLED_KERNEL="$(dirname "$0")/../resources/vmlinux"
    
    if [ -f "$BUNDLED_KERNEL" ] && file "$BUNDLED_KERNEL" | grep -q "ELF"; then
        echo "Using bundled kernel"
        sudo cp "$BUNDLED_KERNEL" "$KERNEL_PATH"
    else
        # Extract from host kernel
        echo "Extracting kernel from host vmlinuz..."
        
        VMLINUZ=$(ls /boot/vmlinuz-* 2>/dev/null | head -1)
        if [ -z "$VMLINUZ" ]; then
            echo "ERROR: No vmlinuz found in /boot"
            echo "Please provide a kernel at $BUNDLED_KERNEL or install linux-image package"
            exit 1
        fi
        
        # Use Python to extract (handles XZ, gzip, bzip2)
        python3 -c "
import sys, lzma, gzip, bz2

with open('$VMLINUZ', 'rb') as f:
    data = f.read()

# Try compression formats at their offsets
formats = [
    ('xz', b'\xfd7zXZ\x00', lzma.decompress),
    ('gzip', b'\x1f\x8b\x08', gzip.decompress),
    ('bzip2', b'BZh', bz2.decompress),
]

for name, magic, decompress in formats:
    offset = data.find(magic)
    if offset >= 0:
        print(f'Found {name} at offset {offset}')
        result = decompress(data[offset:])
        with open('/tmp/vmlinux', 'wb') as f:
            f.write(result)
        print(f'Success: {len(result)} bytes')
        sys.exit(0)

print('ERROR: No supported compression found')
sys.exit(1)
"
        
        if [ -f "/tmp/vmlinux" ] && file "/tmp/vmlinux" | grep -q "ELF"; then
            sudo cp /tmp/vmlinux "$KERNEL_PATH"
            rm /tmp/vmlinux
        else
            echo "ERROR: Failed to extract ELF kernel"
            exit 1
        fi
    fi
    
    echo "Kernel installed: $(file $KERNEL_PATH)"
fi

# ============================================
# 4. Create directories
# ============================================
echo ""
echo "=== Creating directories ==="
sudo mkdir -p /var/lib/firecracker/rootfs
sudo mkdir -p /var/lib/swarmkit/manager /var/lib/swarmkit/worker
sudo mkdir -p /var/run/swarmkit

# ============================================
# 5. Setup network bridge
# ============================================
echo ""
echo "=== Setting up network bridge ==="

# Create bridge if not exists
if ! ip link show swarm-br0 &> /dev/null; then
    sudo ip link add swarm-br0 type bridge
fi

# Add IP if not already configured
if ! ip addr show swarm-br0 | grep -q "192.168.127.1"; then
    sudo ip addr add 192.168.127.1/24 dev swarm-br0 2>/dev/null || true
fi

# Bring up the bridge
sudo ip link set swarm-br0 up 2>/dev/null || true

echo "Bridge: $(ip link show swarm-br0 | head -2)"

# ============================================
# 6. Install SwarmCracker binaries
# ============================================
echo ""
echo "=== Installing SwarmCracker binaries ==="

BIN_DIR="${SWARMCRACKER_BIN_DIR:-$(dirname "$0")/../bin}"

if [ -d "$BIN_DIR" ]; then
    for binary in swarmd-firecracker swarmctl; do
        if [ -f "$BIN_DIR/$binary" ]; then
            sudo cp "$BIN_DIR/$binary" /usr/local/bin/
            sudo chmod +x /usr/local/bin/$binary
            echo "Installed: $binary"
        fi
    done
else
    echo "WARNING: Binary directory not found: $BIN_DIR"
    echo "Please copy swarmd-firecracker and swarmctl to /usr/local/bin manually"
fi

# ============================================
# 7. Generate startup command
# ============================================
echo ""
echo "=== Startup Command ==="

if [ "$ROLE" == "manager" ]; then
    # Detect manager IP if not provided
    if [ -z "$MANAGER_IP" ]; then
        # Try eth1 (Vagrant private network) first, then eth0
        MANAGER_IP=$(ip addr show eth1 2>/dev/null | grep 'inet ' | awk '{print $2}' | cut -d/ -f1)
        if [ -z "$MANAGER_IP" ]; then
            MANAGER_IP=$(ip addr show eth0 2>/dev/null | grep 'inet ' | awk '{print $2}' | cut -d/ -f1)
        fi
    fi
    
    if [ -z "$MANAGER_IP" ]; then
        echo "ERROR: Could not detect manager IP. Specify with --manager-ip"
        exit 1
    fi
    
    echo "To start the manager, run:"
    echo ""
    echo "sudo /usr/local/bin/swarmd-firecracker --manager \\"
    echo "  -d /var/lib/swarmkit/manager \\"
    echo "  --listen-control-api /var/run/swarmkit/swarm.sock \\"
    echo "  --hostname swarm-manager \\"
    echo "  --listen-remote-api 0.0.0.0:4242 \\"
    echo "  --advertise-remote-api ${MANAGER_IP}:4242 \\"
    echo "  --kernel-path /usr/share/firecracker/vmlinux \\"
    echo "  --rootfs-dir /var/lib/firecracker/rootfs \\"
    echo "  --bridge-name swarm-br0"
    echo ""
    echo "Join tokens will be saved to: /var/lib/swarmkit/manager/join-tokens.txt"
else
    echo "To start a worker, first get the join token from the manager:"
    echo "  sudo cat /var/lib/swarmkit/manager/join-tokens.txt"
    echo ""
    echo "Then run on this worker node:"
    echo ""
    echo "sudo /usr/local/bin/swarmd-firecracker \\"
    echo "  -d /var/lib/swarmkit/worker \\"
    echo "  --listen-control-api /var/run/swarmkit/swarm.sock \\"
    echo "  --hostname swarm-worker-1 \\"
    echo "  --join-addr ${MANAGER_IP:-MANAGER_IP}:4242 \\"
    echo "  --join-token WORKER_TOKEN \\"
    echo "  --kernel-path /usr/share/firecracker/vmlinux \\"
    echo "  --rootfs-dir /var/lib/firecracker/rootfs \\"
    echo "  --bridge-name swarm-br0"
fi

echo ""
echo "=== Setup Complete ==="