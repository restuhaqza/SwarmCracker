#!/bin/bash
# Cluster Health Verification Script
# Checks the health of SwarmKit cluster and SwarmCracker workers

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Configuration
SWARM_SOCKET="${SWARM_SOCKET:-/var/run/swarmkit/swarm.sock}"

# Counters
PASS=0
FAIL=0
WARN=0

# Logging
log_pass() {
    echo -e "${GREEN}[✓]${NC} $1"
    ((PASS++))
}

log_fail() {
    echo -e "${RED}[✗]${NC} $1"
    ((FAIL++))
}

log_warn() {
    echo -e "${YELLOW}[!]${NC} $1"
    ((WARN++))
}

log_info() {
    echo -e "${BLUE}[i]${NC} $1"
}

# Check if command exists
check_command() {
    if command -v "$1" &> /dev/null; then
        log_pass "$1 is installed"
        return 0
    else
        log_fail "$1 is not installed"
        return 1
    fi
}

# Check if service is running
check_service() {
    if systemctl is-active --quiet "$1"; then
        log_pass "$1 is running"
        return 0
    else
        log_fail "$1 is not running"
        return 1
    fi
}

# Check if file exists
check_file() {
    if [ -f "$1" ]; then
        log_pass "$1 exists"
        return 0
    else
        log_fail "$1 does not exist"
        return 1
    fi
}

# Check if port is listening
check_port() {
    if lsof -i ":$1" &> /dev/null || ss -tlnp | grep -q ":$1"; then
        log_pass "Port $1 is listening"
        return 0
    else
        log_fail "Port $1 is not listening"
        return 1
    fi
}

# Main verification
main() {
    cat << "EOF"
╔═══════════════════════════════════════════════════════════════╗
║         SwarmKit Cluster Health Verification                  ║
╚═══════════════════════════════════════════════════════════════╝
EOF

    # Check prerequisites
    log_info "Checking prerequisites..."
    echo ""

    check_command "swarmd"
    check_command "swarmctl"
    check_command "firecracker"
    check_command "swarmcracker"
    echo ""

    # Check KVM access
    log_info "Checking KVM access..."
    if [ -r /dev/kvm ]; then
        log_pass "KVM device is accessible"
    else
        log_fail "KVM device is not accessible"
    fi
    echo ""

    # Check services
    log_info "Checking services..."
    if systemctl list-units --type=service | grep -q "swarmd-manager"; then
        check_service "swarmd-manager"
    fi

    if systemctl list-units --type=service | grep -q "swarmd-worker"; then
        check_service "swarmd-worker"
    fi
    echo ""

    # Check ports
    log_info "Checking network ports..."
    if systemctl list-units --type=service | grep -q "swarmd-manager"; then
        check_port 2377
        check_port 4242
    fi
    check_port 7946
    echo ""

    # Check SwarmKit socket
    log_info "Checking SwarmKit control socket..."
    if [ -S "$SWARM_SOCKET" ]; then
        log_pass "SwarmKit socket exists at $SWARM_SOCKET"

        # Get cluster info
        if command -v swarmctl &> /dev/null; then
            echo ""
            log_info "Cluster information:"
            echo ""

            # List nodes
            log_info "Nodes:"
            if swarmctl node ls &> /dev/null; then
                swarmctl node ls
                echo ""

                # Count nodes by status
                READY_COUNT=$(swarmctl node ls --format '{{ .Status }}' | grep -c READY || echo "0")
                log_pass "Ready nodes: $READY_COUNT"
            else
                log_fail "Cannot list nodes"
            fi
            echo ""

            # List services
            log_info "Services:"
            if swarmctl service ls &> /dev/null; then
                swarmctl service ls
                echo ""

                SERVICE_COUNT=$(swarmctl service ls --format '{{ .Name }}' | wc -l)
                log_pass "Running services: $SERVICE_COUNT"
            else
                log_fail "Cannot list services"
            fi
            echo ""

            # Check manager status
            log_info "Manager status:"
            MANAGER_STATUS=$(swarmctl node ls --format '{{ .ManagerStatus }}' | grep -v "none" || echo "")
            if [ -n "$MANAGER_STATUS" ]; then
                LEADER_COUNT=$(echo "$MANAGER_STATUS" | grep -c LEADER || echo "0")
                REACHABLE_COUNT=$(echo "$MANAGER_STATUS" | grep -c REACHABLE || echo "0")

                if [ "$LEADER_COUNT" -eq 1 ]; then
                    log_pass "Cluster has 1 LEADER"
                else
                    log_fail "Expected 1 LEADER, found $LEADER_COUNT"
                fi

                log_pass "Reachable managers: $REACHABLE_COUNT"
            else
                log_warn "No managers found"
            fi
        fi
    else
        log_fail "SwarmKit socket not found at $SWARM_SOCKET"
        log_info "This node might be a worker-only node"
    fi
    echo ""

    # Check SwarmCracker configuration
    log_info "Checking SwarmCracker configuration..."
    if [ -f /etc/swarmcracker/worker.yaml ]; then
        log_pass "SwarmCracker config exists"

        # Validate config
        if command -v swarmcracker &> /dev/null; then
            if swarmcracker validate --config /etc/swarmcracker/worker.yaml &> /dev/null; then
                log_pass "SwarmCracker config is valid"
            else
                log_fail "SwarmCracker config validation failed"
            fi
        fi
    else
        log_warn "SwarmCracker config not found (manager-only node?)"
    fi
    echo ""

    # Check Firecracker processes
    log_info "Checking Firecracker microVMs..."
    FC_COUNT=$(pgrep -c firecracker || echo "0")
    if [ "$FC_COUNT" -gt 0 ]; then
        log_pass "Running microVMs: $FC_COUNT"
    else
        log_warn "No Firecracker processes running"
    fi
    echo ""

    # Check network bridge
    log_info "Checking network bridge..."
    if ip addr show swarm-br0 &> /dev/null; then
        log_pass "Bridge swarm-br0 exists"

        BRIDGE_IP=$(ip addr show swarm-br0 | grep 'inet ' | awk '{print $2}')
        if [ -n "$BRIDGE_IP" ]; then
            log_pass "Bridge IP: $BRIDGE_IP"
        fi

        # Count TAP devices
        TAP_COUNT=$(ip link show | grep -c "tapeth" || echo "0")
        if [ "$TAP_COUNT" -gt 0 ]; then
            log_pass "TAP devices: $TAP_COUNT"
        else
            log_warn "No TAP devices found"
        fi
    else
        log_warn "Bridge swarm-br0 not found"
    fi
    echo ""

    # Check IP forwarding
    log_info "Checking IP forwarding..."
    IP_FORWARD=$(sysctl -n net.ipv4.ip_forward)
    if [ "$IP_FORWARD" = "1" ]; then
        log_pass "IP forwarding is enabled"
    else
        log_fail "IP forwarding is disabled"
    fi
    echo ""

    # Check disk space
    log_info "Checking disk space..."
    DISK_USAGE=$(df /var/lib/firecracker | awk 'NR==2 {print $5}' | sed 's/%//')
    if [ "$DISK_USAGE" -lt 80 ]; then
        log_pass "Disk usage: ${DISK_USAGE}%"
    elif [ "$DISK_USAGE" -lt 90 ]; then
        log_warn "Disk usage: ${DISK_USAGE}% (getting full)"
    else
        log_fail "Disk usage: ${DISK_USAGE}% (nearly full)"
    fi
    echo ""

    # Summary
    cat << "EOF"
╔═══════════════════════════════════════════════════════════════╗
║                    Verification Summary                       ║
╚═══════════════════════════════════════════════════════════════╝
EOF

    echo -e "${GREEN}Passed:${NC}   $PASS"
    echo -e "${YELLOW}Warnings:${NC} $WARN"
    echo -e "${RED}Failed:${NC}   $FAIL"
    echo ""

    if [ $FAIL -eq 0 ]; then
        log_pass "Cluster health check passed!"
        exit 0
    else
        log_fail "Cluster health check failed. Please review the issues above."
        exit 1
    fi
}

# Display usage
usage() {
    cat << EOF
Usage: $0 [options]

Verifies the health of SwarmKit cluster and SwarmCracker workers.

Options:
  -h, --help     Show this help message
  -s, --socket   Path to SwarmKit socket (default: /var/run/swarmkit/swarm.sock)

Environment Variables:
  SWARM_SOCKET   Path to SwarmKit socket (default: /var/run/swarmkit/swarm.sock)

Examples:
  $0                    Run verification with defaults
  SWARM_SOCKET=/tmp/sock $0    Use custom socket path

EOF
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            usage
            exit 0
            ;;
        -s|--socket)
            SWARM_SOCKET="$2"
            shift 2
            ;;
        *)
            echo "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

# Run main
main
