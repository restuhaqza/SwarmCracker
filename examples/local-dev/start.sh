#!/bin/bash
# Local Development SwarmKit Cluster Startup Script
# Usage: ./start.sh [manager|worker|deploy|clean]

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
MANAGER_DIR="/tmp/local-dev/manager"
WORKER_DIR="/tmp/local-dev/worker"
MANAGER_ADDR="127.0.0.1:4242"
WORKER_ADDR="${WORKER_PORT:-127.0.0.1:4243}"
SWARM_SOCKET="$MANAGER_DIR/swarm.sock"
CONFIG_DIR="$(dirname "$0")/config"

# Helper functions
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

check_command() {
    if ! command -v "$1" &> /dev/null; then
        log_error "$1 not found. Please install it first."
        exit 1
    fi
}

# Prerequisites check
check_prereqs() {
    log_info "Checking prerequisites..."
    check_command "swarmd"
    check_command "swarmctl"
    check_command "firecracker"
    check_command "swarmcracker"

    # Check KVM access
    if [ ! -r /dev/kvm ]; then
        log_error "Cannot access /dev/kvm. Add your user to the kvm group:"
        echo "  sudo usermod -aG kvm \$USER"
        echo "Then log out and back in."
        exit 1
    fi

    log_info "All prerequisites met."
}

# Start manager
start_manager() {
    log_info "Starting SwarmKit manager..."

    # Create manager directory
    mkdir -p "$MANAGER_DIR"

    # Check if already running
    if [ -S "$SWARM_SOCKET" ]; then
        log_warn "Manager socket already exists at $SWARM_SOCKET"
        read -p "Stop existing manager and continue? (y/N) " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            pkill -f "swarmd.*$MANAGER_DIR" || true
            rm -f "$SWARM_SOCKET"
            sleep 1
        else
            log_error "Aborted."
            exit 1
        fi
    fi

    # Start manager
    log_info "Manager listening on: $MANAGER_ADDR"
    log_info "Control socket: $SWARM_SOCKET"
    log_info "Press Ctrl+C to stop"

    swarmd \
        -d "$MANAGER_DIR" \
        --listen-control-api "$SWARM_SOCKET" \
        --hostname manager-local \
        --listen-remote-api "$MANAGER_ADDR" \
        --debug
}

# Start worker
start_worker() {
    log_info "Starting SwarmKit worker with SwarmCracker..."

    # Create worker directory
    mkdir -p "$WORKER_DIR"

    # Get join token from manager
    if [ ! -S "$SWARM_SOCKET" ]; then
        log_error "Manager socket not found. Start manager first:"
        echo "  $0 manager"
        exit 1
    fi

    # Get worker token
    log_info "Getting join token from manager..."
    WORKER_TOKEN=$(export SWARM_SOCKET="$SWARM_SOCKET"; swarmctl cluster inspect default 2>/dev/null | grep -A 1 "Worker:" | tail -1 | xargs || echo "")

    if [ -z "$WORKER_TOKEN" ]; then
        log_error "Failed to get worker token. Is manager running?"
        exit 1
    fi

    log_info "Worker token obtained: ${WORKER_TOKEN:0:20}..."

    # Check SwarmCracker config
    if [ ! -f "$CONFIG_DIR/worker.yaml" ]; then
        log_error "SwarmCracker config not found: $CONFIG_DIR/worker.yaml"
        exit 1
    fi

    # Validate config
    if ! swarmcracker validate --config "$CONFIG_DIR/worker.yaml" 2>/dev/null; then
        log_warn "SwarmCracker config validation failed, but continuing..."
    fi

    # Check if worker already running
    if pgrep -f "swarmd.*$WORKER_DIR" > /dev/null; then
        log_warn "Worker already running"
        exit 0
    fi

    # Start worker
    log_info "Worker listening on: $WORKER_ADDR"
    log_info "Joining manager at: $MANAGER_ADDR"
    log_info "Press Ctrl+C to stop"

    swarmd \
        -d "$WORKER_DIR" \
        --hostname worker-local \
        --join-addr "$MANAGER_ADDR" \
        --join-token "$WORKER_TOKEN" \
        --listen-remote-api "$WORKER_ADDR" \
        --executor firecracker \
        --executor-config "$CONFIG_DIR/worker.yaml" \
        --debug
}

# Deploy test services
deploy_services() {
    log_info "Deploying test services..."

    # Check manager is running
    if [ ! -S "$SWARM_SOCKET" ]; then
        log_error "Manager socket not found. Start manager first:"
        echo "  $0 manager"
        exit 1
    fi

    export SWARM_SOCKET="$SWARM_SOCKET"

    # Check if worker is ready
    log_info "Waiting for worker to be ready..."
    for i in {1..30}; do
        NODE_COUNT=$(swarmctl node ls 2>/dev/null | grep -c worker || echo "0")
        if [ "$NODE_COUNT" -gt 0 ]; then
            log_info "Worker is ready!"
            break
        fi
        if [ $i -eq 30 ]; then
            log_error "Worker not ready after 30 seconds"
            exit 1
        fi
        sleep 1
    done

    # Deploy nginx
    log_info "Deploying nginx service (2 replicas)..."
    if swarmctl service ls | grep -q "nginx"; then
        log_warn "nginx service already exists, removing..."
        swarmctl service remove nginx
        sleep 2
    fi

    swarmctl service create \
        --name nginx \
        --image nginx:alpine \
        --replicas 2

    log_info "Waiting for tasks to start..."
    sleep 5

    # Show status
    log_info "Service status:"
    swarmctl service ls

    echo ""
    log_info "Task status:"
    swarmctl service ps nginx

    echo ""
    log_info "Deployment complete!"
    log_info "Check task progress with:"
    echo "  export SWARM_SOCKET=$SWARM_SOCKET"
    echo "  swarmctl service ps nginx"
}

# Show cluster status
show_status() {
    log_info "Cluster status:"
    echo ""

    if [ -S "$SWARM_SOCKET" ]; then
        export SWARM_SOCKET="$SWARM_SOCKET"

        log_info "Nodes:"
        swarmctl node ls || log_warn "Failed to list nodes"
        echo ""

        log_info "Services:"
        swarmctl service ls || log_warn "Failed to list services"
        echo ""

        log_info "Firecracker processes:"
        ps aux | grep firecracker | grep -v grep || log_warn "No Firecracker processes running"
        echo ""

        log_info "Network bridge:"
        ip addr show swarm-br0 2>/dev/null || log_warn "Bridge swarm-br0 not found"
        echo ""

        log_info "TAP devices:"
        ip link show | grep tap || log_warn "No TAP devices found"
    else
        log_warn "Manager not running (socket not found)"
    fi
}

# Clean up
clean_all() {
    log_warn "Cleaning up local-dev cluster..."

    # Remove all services
    if [ -S "$SWARM_SOCKET" ]; then
        export SWARM_SOCKET="$SWARM_SOCKET"
        log_info "Removing services..."
        swarmctl service ls -q 2>/dev/null | xargs -I {} swarmctl service remove {} 2>/dev/null || true
    fi

    # Kill processes
    log_info "Stopping swarmd processes..."
    pkill -f "swarmd.*$MANAGER_DIR" || true
    pkill -f "swarmd.*$WORKER_DIR" || true

    # Wait for processes to stop
    sleep 2

    # Remove directories
    log_info "Removing state directories..."
    rm -rf "$MANAGER_DIR"
    rm -rf "$WORKER_DIR"

    # Remove network bridge
    log_info "Removing network bridge..."
    sudo ip link delete swarm-br0 2>/dev/null || true

    log_info "Cleanup complete!"
}

# Main
main() {
    case "${1:-help}" in
        manager)
            check_prereqs
            start_manager
            ;;
        worker)
            check_prereqs
            start_worker
            ;;
        deploy)
            deploy_services
            ;;
        status)
            show_status
            ;;
        clean)
            clean_all
            ;;
        help|--help|-h)
            cat <<EOF
Local Development SwarmKit Cluster

Usage: $0 [command]

Commands:
  manager    Start manager daemon
  worker     Start worker daemon with SwarmCracker
  deploy     Deploy test services (nginx)
  status     Show cluster status
  clean      Clean up all state and processes
  help       Show this help message

Examples:
  # Terminal 1: Start manager
  $0 manager

  # Terminal 2: Start worker
  $0 worker

  # Terminal 3: Deploy services
  $0 deploy

  # Check status
  $0 status

  # Clean up
  $0 clean

EOF
            ;;
        *)
            log_error "Unknown command: $1"
            echo "Run '$0 help' for usage information."
            exit 1
            ;;
    esac
}

main "$@"
