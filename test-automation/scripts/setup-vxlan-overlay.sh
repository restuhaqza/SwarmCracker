#!/bin/bash
# setup-vxlan-overlay.sh - Automated VXLAN overlay setup for SwarmCracker
# Usage: ./setup-vxlan-overlay.sh <bridge-name> <overlay-ip> <vxlan-id> <phys-interface> <local-ip> <peer-ips...>

set -e

BRIDGE_NAME="${1:-swarm-br0}"
OVERLAY_IP="${2:-10.30.0.1/24}"
VXLAN_ID="${3:-100}"
PHYS_INTERFACE="${4:-enp0s8}"
LOCAL_IP="${5}"
shift 5
PEER_IPS=("$@")

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    log_error "This script must be run as root"
    exit 1
fi

# Validate parameters
if [ -z "$LOCAL_IP" ]; then
    log_error "Local IP is required"
    echo "Usage: $0 <bridge-name> <overlay-ip> <vxlan-id> <phys-interface> <local-ip> <peer-ips...>"
    exit 1
fi

if [ ${#PEER_IPS[@]} -eq 0 ]; then
    log_warn "No peer IPs provided - VXLAN will be configured but may not work until peers are added"
fi

log_info "Setting up VXLAN overlay for $BRIDGE_NAME"
log_info "  Overlay IP: $OVERLAY_IP"
log_info "  VXLAN ID: $VXLAN_ID"
log_info "  Physical interface: $PHYS_INTERFACE ($LOCAL_IP)"
log_info "  Peers: ${PEER_IPS[*]:-none}"

# Load VXLAN kernel module
log_info "Loading VXLAN kernel module..."
modprobe vxlan || log_warn "Failed to load vxlan module (may already be loaded)"

# Check if bridge exists
if ! ip link show "$BRIDGE_NAME" &>/dev/null; then
    log_error "Bridge $BRIDGE_NAME does not exist"
    exit 1
fi

# VXLAN interface name
VXLAN_NAME="${BRIDGE_NAME}-vxlan"

# Clean up existing VXLAN interface if it exists
if ip link show "$VXLAN_NAME" &>/dev/null; then
    log_warn "Removing existing VXLAN interface $VXLAN_NAME"
    ip link delete "$VXLAN_NAME"
fi

# Create VXLAN interface
log_info "Creating VXLAN interface $VXLAN_NAME (VNI $VXLAN_ID)..."
ip link add "$VXLAN_NAME" type vxlan \
    id "$VXLAN_ID" \
    dstport 4789 \
    dev "$PHYS_INTERFACE" \
    local "$LOCAL_IP"

if [ $? -ne 0 ]; then
    log_error "Failed to create VXLAN interface"
    exit 1
fi

# Attach VXLAN to bridge
log_info "Attaching VXLAN to bridge $BRIDGE_NAME..."
ip link set "$VXLAN_NAME" master "$BRIDGE_NAME"

# Add overlay IP to bridge
log_info "Adding overlay IP $OVERLAY_IP to bridge..."
ip addr add "$OVERLAY_IP" dev "$BRIDGE_NAME" 2>/dev/null || log_warn "Overlay IP may already exist"

# Bring up interfaces
log_info "Bringing up interfaces..."
ip link set "$VXLAN_NAME" up

# Add peer forwarding entries
for peer_ip in "${PEER_IPS[@]}"; do
    log_info "Adding peer forwarding entry for $peer_ip..."
    bridge fdb append to 00:00:00:00:00:00 dst "$peer_ip" dev "$VXLAN_NAME" 2>/dev/null || true
done

# Enable proxy ARP and IP forwarding
log_info "Enabling proxy ARP and IP forwarding..."
sysctl -w "net.ipv4.conf.${BRIDGE_NAME}.proxy_arp=1" >/dev/null
sysctl -w "net.ipv4.conf.${BRIDGE_NAME}.forwarding=1" >/dev/null
sysctl -w "net.ipv4.ip_forward=1" >/dev/null

# Make sysctl settings persistent
cat > /etc/sysctl.d/99-swarmcracker-vxlan.conf <<EOF
net.ipv4.conf.${BRIDGE_NAME}.proxy_arp=1
net.ipv4.conf.${BRIDGE_NAME}.forwarding=1
net.ipv4.ip_forward=1
EOF

log_info "Sysctl settings persisted to /etc/sysctl.d/99-swarmcracker-vxlan.conf"

# Add routes to remote worker VM subnets
# This is optional and can be done separately
log_info "To add routes to remote VM subnets, use:"
for peer_ip in "${PEER_IPS[@]}"; do
    # Extract subnet from overlay IP (replace last octet with 0)
    BASE_IP=$(echo "$OVERLAY_IP" | cut -d'.' -f1-3)
    echo "  ip route add 192.168.1X.0/24 via ${BASE_IP}.2 dev $BRIDGE_NAME  # Replace X with worker ID"
done

# Show status
echo ""
log_info "VXLAN overlay setup complete!"
echo ""
echo "=== VXLAN Interface ==="
ip addr show "$VXLAN_NAME" | grep -E "^[0-9]+:|inet "

echo ""
echo "=== Bridge Status ==="
ip addr show "$BRIDGE_NAME" | grep -E "^[0-9]+:|inet "

echo ""
echo "=== Forwarding Database ==="
bridge fdb show dev "$VXLAN_NAME" | grep -E "00:00:00:00:00:00|dst"

echo ""
log_info "VXLAN overlay is ready!"
echo ""
echo "To test connectivity:"
for peer_ip in "${PEER_IPS[@]}"; do
    PEER_OVERLAY=$(echo "$OVERLAY_IP" | sed 's/\.1$/.2/')
    echo "  ping -c 3 $PEER_OVERLAY"
done
