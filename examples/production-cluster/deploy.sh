#!/bin/bash
# Production Cluster Deployment Script
# Deploys SwarmKit managers and workers with SwarmCracker
#
# Usage: ./deploy.sh [num_managers] [num_workers]
# Example: ./deploy.sh 3 5  (deploys 3 managers, 5 workers)

set -e

# Configuration
MANAGER_COUNT="${1:-3}"
WORKER_COUNT="${2:-2}"
MANAGER_NETWORK="192.168.1.0/24"
MANAGER_START_IP=10
WORKER_START_IP=20

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Logging
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_step() {
    echo -e "${BLUE}==>${NC} $1"
}

# Check if running as root
check_root() {
    if [ "$EUID" -ne 0 ]; then
        log_error "This script must be run as root (use sudo)"
        exit 1
    fi
}

# Detect system configuration
detect_system() {
    log_step "Detecting system configuration..."

    # OS detection
    if [ -f /etc/os-release ]; then
        . /etc/os-release
        OS=$ID
        OS_VERSION=$VERSION_ID
        log_info "Detected OS: $OS $OS_VERSION"
    else
        log_error "Cannot detect OS. /etc/os-release not found."
        exit 1
    fi

    # CPU detection
    CPU_CORES=$(nproc)
    log_info "CPU cores: $CPU_CORES"

    # Memory detection
    MEMORY_MB=$(free -m | awk '/Mem:/ {print $2}')
    log_info "Memory: ${MEMORY_MB}MB"

    # Disk detection
    DISK_GB=$(df -BG / | awk 'NR==2 {print $2}' | sed 's/G//')
    log_info "Disk: ${DISK_GB}GB"

    # KVM detection
    if [ -r /dev/kvm ]; then
        log_info "KVM: Available"
    else
        log_warn "KVM: Not available. MicroVMs may not work."
    fi

    # Validate system requirements
    if [ "$CPU_CORES" -lt 2 ]; then
        log_error "Insufficient CPU cores (minimum: 2)"
        exit 1
    fi

    if [ "$MEMORY_MB" -lt 4096 ]; then
        log_warn "Low memory (recommended: 8GB+)"
    fi
}

# Install dependencies
install_dependencies() {
    log_step "Installing dependencies..."

    case $OS in
        ubuntu|debian)
            apt-get update -qq
            apt-get install -y \
                build-essential \
                git \
                wget \
                curl \
                bridge-utils \
                iproute2 \
                iptables \
                qemu-kvm \
                libvirt-daemon-system \
                libvirt-clients \
                tini \
                python3 \
                python3-pip
            ;;
        centos|rhel|fedora)
            yum install -y \
                git \
                wget \
                curl \
                bridge-utils \
                iproute \
                iptables \
                qemu-kvm \
                libvirt \
                tini \
                python3 \
                python3-pip
            ;;
        *)
            log_error "Unsupported OS: $OS"
            exit 1
            ;;
    esac

    log_info "Dependencies installed successfully"
}

# Install Firecracker
install_firecracker() {
    log_step "Installing Firecracker..."

    if command -v firecracker &> /dev/null; then
        log_info "Firecracker already installed ($(firecracker --version))"
        return
    fi

    FC_VERSION="v1.10.0"
    wget -q https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-x86_64.tgz
    tar -xzf firecracker-${FC_VERSION}-x86_64.tgz

    mv release-${FC_VERSION}-x86_64/firecracker-${FC_VERSION}-x86_64 /usr/bin/firecracker
    mv release-${FC_VERSION}-x86_64/jailer-${FC_VERSION}-x86_64 /usr/bin/jailer

    chmod +x /usr/bin/firecracker /usr/bin/jailer

    rm -rf firecracker-${FC_VERSION}-x86_64.tgz release-${FC_VERSION}-x86_64

    log_info "Firecracker ${FC_VERSION} installed"
}

# Install SwarmKit
install_swarmkit() {
    log_step "Installing SwarmKit..."

    if command -v swarmd &> /dev/null; then
        log_info "SwarmKit already installed ($(swarmd --version))"
        return
    fi

    git clone https://github.com/moby/swarmkit.git /tmp/swarmkit
    cd /tmp/swarmkit
    make binaries > /dev/null 2>&1

    cp ./bin/swarmd /usr/local/bin/
    cp ./bin/swarmctl /usr/local/bin/

    chmod +x /usr/local/bin/swarmd /usr/local/bin/swarmctl

    log_info "SwarmKit installed (swarmd $(swarmd --version))"
}

# Install SwarmCracker (workers only)
install_swarmcracker() {
    if [ "$1" = "manager" ]; then
        return
    fi

    log_step "Installing SwarmCracker..."

    if command -v swarmcracker &> /dev/null; then
        log_info "SwarmCracker already installed"
        return
    fi

    git clone https://github.com/restuhaqza/swarmcracker.git /opt/swarmcracker
    cd /opt/swarmcracker
    make build > /dev/null 2>&1

    cp ./bin/swarmcracker /usr/local/bin/
    chmod +x /usr/local/bin/swarmcracker

    log_info "SwarmCracker installed"
}

# Configure networking
configure_networking() {
    log_step "Configuring networking..."

    # Enable IP forwarding
    sysctl -w net.ipv4.ip_forward=1 > /dev/null
    echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf

    log_info "IP forwarding enabled"
}

# Configure firewall
configure_firewall() {
    log_step "Configuring firewall..."

    if command -v ufw &> /dev/null; then
        if [ "$1" = "manager" ]; then
            ufw allow 22/tcp > /dev/null
            ufw allow from $MANAGER_NETWORK to any port 2377 proto tcp > /dev/null
            ufw allow from $MANAGER_NETWORK to any port 7946 proto tcp > /dev/null
            ufw allow from $MANAGER_NETWORK to any port 7946 proto udp > /dev/null
            ufw allow from $MANAGER_NETWORK to any port 4242 proto tcp > /dev/null
        else
            ufw allow 22/tcp > /dev/null
            for i in $(seq 1 $MANAGER_COUNT); do
                ufw allow from 192.168.1.$((MANAGER_START_IP + i - 1)) to any port 7946 proto tcp > /dev/null
                ufw allow from 192.168.1.$((MANAGER_START_IP + i - 1)) to any port 7946 proto udp > /dev/null
            done
        fi

        ufw --force enable > /dev/null
        log_info "Firewall configured (ufw)"
    elif command -v firewall-cmd &> /dev/null; then
        # firewalld support
        if [ "$1" = "manager" ]; then
            firewall-cmd --permanent --add-port=2377/tcp > /dev/null
            firewall-cmd --permanent --add-port=7946/tcp > /dev/null
            firewall-cmd --permanent --add-port=7946/udp > /dev/null
            firewall-cmd --permanent --add-port=4242/tcp > /dev/null
        else
            for i in $(seq 1 $MANAGER_COUNT); do
                firewall-cmd --permanent --add-rich-rule="rule family='ipv4' source address='192.168.1.$((MANAGER_START_IP + i - 1))' port protocol='tcp' port='7946' accept" > /dev/null
            done
        fi

        firewall-cmd --reload > /dev/null
        log_info "Firewall configured (firewalld)"
    else
        log_warn "No firewall detected. Skipping firewall configuration."
    fi
}

# Deploy manager
deploy_manager() {
    local manager_num=$1
    local manager_ip=$2

    log_step "Deploying Manager $manager_num ($manager_ip)..."

    mkdir -p /var/lib/swarmkit/manager

    if [ $manager_num -eq 1 ]; then
        # First manager initializes cluster
        cat > /etc/systemd/system/swarmd-manager.service <<EOF
[Unit]
Description=SwarmKit Manager
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmd \\
  -d /var/lib/swarmkit/manager \\
  --listen-control-api /var/run/swarmkit/swarm.sock \\
  --hostname manager-${manager_num} \\
  --listen-remote-api 0.0.0.0:4242 \\
  --debug
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    else
        # Additional managers join cluster
        cat > /etc/systemd/system/swarmd-manager.service <<EOF
[Unit]
Description=SwarmKit Manager
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmd \\
  -d /var/lib/swarmkit/manager \\
  --listen-control-api /var/run/swarmkit/swarm.sock \\
  --hostname manager-${manager_num} \\
  --listen-remote-api ${manager_ip}:4242 \\
  --join-addr 192.168.1.${MANAGER_START_IP}:4242 \\
  --join-token \${MANAGER_TOKEN} \\
  --debug
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    fi

    systemctl daemon-reload
    systemctl enable swarmd-manager
    systemctl start swarmd-manager

    log_info "Manager $manager_num deployed"
}

# Deploy worker
deploy_worker() {
    local worker_num=$1
    local manager_ip=$2

    log_step "Deploying Worker $worker_num..."

    # Configure SwarmCracker
    mkdir -p /etc/swarmcracker
    mkdir -p /var/lib/firecracker/rootfs
    mkdir -p /var/lib/firecracker/logs

    # Copy worker config
    if [ -f "$(dirname "$0")/config/worker.yaml" ]; then
        cp "$(dirname "$0")/config/worker.yaml" /etc/swarmcracker/
    else
        log_warn "Worker config not found. Using defaults."
    fi

    # Create worker service
    cat > /etc/systemd/system/swarmd-worker.service <<EOF
[Unit]
Description=SwarmKit Worker with SwarmCracker
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmd \\
  -d /var/lib/swarmkit/worker \\
  --hostname worker-${worker_num} \\
  --join-addr ${manager_ip}:4242 \\
  --join-token \${WORKER_TOKEN} \\
  --listen-remote-api 0.0.0.0:4242 \\
  --executor firecracker \\
  --executor-config /etc/swarmcracker/worker.yaml \\
  --debug
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable swarmd-worker
    systemctl start swarmd-worker

    log_info "Worker $worker_num deployed"
}

# Main deployment flow
main() {
    cat << "EOF"
╔═══════════════════════════════════════════════════════════════╗
║     SwarmKit Production Cluster Deployment Script            ║
║     Managers: $1, Workers: $2                        ║
╚═══════════════════════════════════════════════════════════════╝
EOF

    check_root
    detect_system
    install_dependencies
    install_firecracker
    install_swarmkit

    # Prompt for deployment type
    echo ""
    echo "Select deployment type:"
    echo "  1) Manager node"
    echo "  2) Worker node"
    read -p "Choice [1/2]: " node_type

    if [ "$node_type" = "1" ]; then
        install_swarmcracker "manager"
        configure_networking
        configure_firewall "manager"

        echo ""
        read -p "Manager number (1-${MANAGER_COUNT}): " manager_num
        deploy_manager $manager_num "192.168.1.$((MANAGER_START_IP + manager_num - 1))"

        if [ $manager_num -eq 1 ]; then
            echo ""
            log_info "Manager 1 deployed. Get join tokens with:"
            echo "  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock"
            echo "  swarmctl cluster inspect default"
        fi
    elif [ "$node_type" = "2" ]; then
        install_swarmcracker "worker"
        configure_networking
        configure_firewall "worker"

        echo ""
        read -p "Worker number: " worker_num
        read -p "Manager IP to join: " manager_ip

        deploy_worker $worker_num $manager_ip
    else
        log_error "Invalid choice"
        exit 1
    fi

    echo ""
    log_info "Deployment complete!"
    log_info "Check status with: ./verify-cluster.sh"
}

# Display usage
usage() {
    cat << EOF
Usage: $0 [num_managers] [num_workers]

Deploys SwarmKit production cluster with managers and workers.

Arguments:
  num_managers    Number of manager nodes (default: 3)
  num_workers     Number of worker nodes (default: 2)

Examples:
  $0 3 5    Deploy 3 managers and 5 workers
  $0        Deploy with defaults (3 managers, 2 workers)

Note: This script must be run on each node individually.
      Run as root (use sudo).

EOF
}

# Parse arguments
if [ "$1" = "-h" ] || [ "$1" = "--help" ]; then
    usage
    exit 0
fi

# Run main
main
