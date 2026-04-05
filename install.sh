#!/usr/bin/env bash
set -euo pipefail

# ─────────────────────────────────────────────────────────────────────
# SwarmCracker — One-line installer
# Usage: curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash
# ─────────────────────────────────────────────────────────────────────

REPO="restuhaqza/SwarmCracker"
GITHUB="https://github.com"
API="https://api.github.com/repos/${REPO}"

# ─── Colors ──────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

info()    { printf "${GREEN}[INFO]${NC}  %s\n" "$*"; }
warn()    { printf "${YELLOW}[WARN]${NC}  %s\n" "$*"; }
error()   { printf "${RED}[ERROR]${NC} %s\n" "$*" >&2; }
success() { printf "${GREEN}${BOLD}  ✓${NC}  %s\n" "$*"; }
header()  { printf "\n${CYAN}${BOLD}%s${NC}\n" "$*"; }

# ─── Helpers ─────────────────────────────────────────────────────────
detect_arch() {
    local arch
    arch=$(uname -m 2>/dev/null || echo "unknown")
    case "$arch" in
        x86_64|amd64)  echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) error "Unsupported architecture: $arch"; exit 1 ;;
    esac
}

detect_os() {
    local os
    os=$(uname -s 2>/dev/null || echo "unknown")
    case "$os" in
        Linux)   echo "linux" ;;
        Darwin)  echo "darwin" ;;
        *) error "Unsupported OS: $os — SwarmCracker requires Linux (KVM) or macOS (build only)"; exit 1 ;;
    esac
}

need_cmd() {
    if ! command -v "$1" &>/dev/null; then
        error "Required command not found: $1"
        exit 1
    fi
}

# ─── Pre-flight ──────────────────────────────────────────────────────
need_cmd curl
need_cmd tar
need_cmd sha256sum
ARCH=$(detect_arch)
OS=$(detect_os)
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# ─── Fetch latest release ────────────────────────────────────────────
header "🔥 SwarmCracker Installer"

info "Detecting latest release..."
RELEASE_DATA=$(curl -fsSL "${API}/releases/latest" 2>/dev/null)

if [ -z "$RELEASE_DATA" ]; then
    error "Could not fetch release info from GitHub."
    exit 1
fi

VERSION=$(echo "$RELEASE_DATA" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
if [ -z "$VERSION" ]; then
    error "Could not determine latest version."
    exit 1
fi

info "Latest version: ${CYAN}${BOLD}${VERSION}${NC}"
info "OS/Arch: ${OS}/${ARCH}"
info "Install dir: ${INSTALL_DIR}"

TARBALL="swarmcracker-${VERSION}-${OS}-${ARCH}.tar.gz"
DOWNLOAD_URL="${GITHUB}/${REPO}/releases/download/${VERSION}/${TARBALL}"
CHECKSUM_URL="${GITHUB}/${REPO}/releases/download/${VERSION}/checksums.txt"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

# ─── Download & verify ───────────────────────────────────────────────
info "Downloading ${TARBALL}..."
curl -fsSL "$DOWNLOAD_URL" -o "${TMPDIR}/${TARBALL}"

info "Verifying checksum..."
curl -fsSL "$CHECKSUM_URL" -o "${TMPDIR}/checksums.txt"

EXPECTED=$(grep "${TARBALL}" "${TMPDIR}/checksums.txt" | awk '{print $1}')
ACTUAL=$(sha256sum "${TMPDIR}/${TARBALL}" | awk '{print $1}')

if [ "$EXPECTED" != "$ACTUAL" ]; then
    error "Checksum mismatch!"
    error "  Expected: ${EXPECTED}"
    error "  Actual:   ${ACTUAL}"
    exit 1
fi
success "Checksum verified"

# ─── Extract & install ──────────────────────────────────────────────
info "Extracting..."
tar xzf "${TMPDIR}/${TARBALL}" -C "${TMPDIR}"

BINDIR="${TMPDIR}/swarmcracker-${VERSION}-${OS}-${ARCH}"
BINARIES=("swarmcracker" "swarmd-firecracker" "swarmcracker-agent")

for bin in "${BINARIES[@]}"; do
    if [ -f "${BINDIR}/${bin}" ]; then
        cp "${BINDIR}/${bin}" "${INSTALL_DIR}/${bin}"
        chmod +x "${INSTALL_DIR}/${bin}"
        success "${bin} → ${INSTALL_DIR}/${bin}"
    fi
done

# Verify
if command -v swarmcracker &>/dev/null; then
    SWARMCRACKER_VERSION=$("${INSTALL_DIR}/swarmcracker" version 2>/dev/null || echo "${VERSION}")
    info "Installed: ${CYAN}swarmcracker ${SWARMCRACKER_VERSION}${NC}"
else
    warn "swarmcracker binary not found in PATH after install"
fi

# ─── Node setup ──────────────────────────────────────────────────────
header "⚙️  Node Setup"

printf "\n${BOLD}Select node type:${NC}\n"
printf "  ${GREEN}1)${NC} Manager (initialize SwarmKit cluster)\n"
printf "  ${GREEN}2)${NC} Worker (join existing cluster)\n"
printf "  ${GREEN}3)${NC} Skip — just install binaries\n\n"

read -rp "  [1/2/3]: " NODE_TYPE

case "$NODE_TYPE" in
    1)
        setup_manager
        ;;
    2)
        setup_worker
        ;;
    3)
        header "✅ Done"
        success "SwarmCracker ${VERSION} installed to ${INSTALL_DIR}"
        info "Run again with --manager or --worker to configure a node"
        exit 0
        ;;
    *)
        error "Invalid choice"
        exit 1
        ;;
esac

# ─── Manager setup ───────────────────────────────────────────────────
setup_manager() {
    header "🏛️  Manager Setup"

    # Check KVM
    if [ ! -e /dev/kvm ]; then
        warn "/dev/kvm not found — Firecracker VMs will not work on this host"
    fi

    # Defaults
    STATE_DIR="${STATE_DIR:-/var/lib/swarmcracker/manager}"
    API_ADDR="${API_ADDR:-0.0.0.0:4242}"
    HOSTNAME="${HOSTNAME:-$(hostname 2>/dev/null || echo 'manager')}"

    printf "\n${BOLD}Manager configuration${NC} (press Enter for defaults)\n"
    read -rp "  Hostname [${HOSTNAME}]: " INPUT
    HOSTNAME="${INPUT:-$HOSTNAME}"

    read -rp "  Listen address [${API_ADDR}]: " INPUT
    API_ADDR="${INPUT:-$API_ADDR}"

    read -rp "  State directory [${STATE_DIR}]: " INPUT
    STATE_DIR="${INPUT:-$STATE_DIR}"

    # Install swarmd (SwarmKit)
    install_swarmd

    mkdir -p "$STATE_DIR"

    info "Initializing SwarmKit manager..."
    swarmd \
        -d "$STATE_DIR" \
        --hostname "$HOSTNAME" \
        --listen-control-api "${STATE_DIR}/swarm.sock" \
        --listen-remote-api "$API_ADDR" \
        --join-addr "" \
        &>/tmp/swarmcracker-manager.log &

    SWARMD_PID=$!
    sleep 3

    if ! kill -0 "$SWARMD_PID" 2>/dev/null; then
        error "SwarmKit manager failed to start. Check /tmp/swarmcracker-manager.log"
        cat /tmp/swarmcracker-manager.log 2>/dev/null
        exit 1
    fi

    success "Manager running (PID ${SWARMD_PID})"

    # Wait for socket
    local retries=10
    while [ ! -S "${STATE_DIR}/swarm.sock" ] && [ $retries -gt 0 ]; do
        sleep 1
        retries=$((retries - 1))
    done

    if [ ! -S "${STATE_DIR}/swarm.sock" ]; then
        warn "Manager socket not ready. You can check status later with:"
        warn "  swarmctl --socket ${STATE_DIR}/swarm.sock node ls"
    fi

    # Install swarmctl for convenience
    install_swarmctl

    # Get join tokens
    WORKER_TOKEN=""
    MANAGER_TOKEN=""
    if command -v swarmctl &>/dev/null && [ -S "${STATE_DIR}/swarm.sock" ]; then
        CLUSTER_JSON=$(swarmctl --socket "${STATE_DIR}/swarm.sock" cluster inspect default 2>/dev/null || true)
        WORKER_TOKEN=$(echo "$CLUSTER_JSON" 2>/dev/null | grep -oP 'SWMTKN-[0-9a-zA-Z-]+' | head -1 || echo "")
    fi

    # Detect manager IP
    LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
    if [ -z "$LOCAL_IP" ]; then
        LOCAL_IP=$(ip route get 1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
    fi

    # Print summary
    header "✅ Manager Ready"

    printf "\n"
    printf "${BOLD}  Manager Info${NC}\n"
    printf "  ─────────────────────────────────────\n"
    printf "  Hostname:    ${CYAN}${HOSTNAME}${NC}\n"
    printf "  API:         ${CYAN}%s${NC}\n" "$API_ADDR"
    printf "  State dir:   ${CYAN}${STATE_DIR}${NC}\n"
    printf "  Socket:      ${CYAN}${STATE_DIR}/swarm.sock${NC}\n"

    if [ -n "$LOCAL_IP" ]; then
        printf "  IP:          ${CYAN}${LOCAL_IP}${NC}\n"
    fi

    printf "\n${BOLD}  To add workers, run this on each worker node:${NC}\n"
    printf "${YELLOW}  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash -s -- --worker \\\n"
    printf "    --manager ${LOCAL_IP:-<MANAGER_IP>}:4242 \\\n"
    if [ -n "$WORKER_TOKEN" ]; then
        printf "    --token ${WORKER_TOKEN}${NC}\n"
    else
        printf "    --token <WORKER_TOKEN>${NC}\n"
    fi

    printf "\n${BOLD}  Useful commands:${NC}\n"
    printf "  swarmctl --socket ${STATE_DIR}/swarm.sock node ls\n"
    printf "  swarmctl --socket ${STATE_DIR}/swarm.sock service create --name nginx --image nginx:alpine\n"
    printf "\n"
}

# ─── Worker setup ────────────────────────────────────────────────────
setup_worker() {
    header "⛏️  Worker Setup"

    if [ ! -e /dev/kvm ]; then
        error "/dev/kvm not found — Firecracker VMs require KVM"
        exit 1
    fi

    # Defaults
    MANAGER_ADDR="${MANAGER_ADDR:-}"
    JOIN_TOKEN="${JOIN_TOKEN:-}"
    HOSTNAME="${HOSTNAME:-$(hostname 2>/dev/null || echo "worker-$(hostname -s 2>/dev/null || echo $$)"}"
    BRIDGE_NAME="${BRIDGE_NAME:-swarm-br0}"
    SUBNET="${SUBNET:-192.168.127.0/24}"
    BRIDGE_IP="${BRIDGE_IP:-192.168.127.1/24}"
    STATE_DIR="${STATE_DIR:-/var/lib/swarmcracker/worker}"
    KERNEL_PATH="${KERNEL_PATH:-/usr/share/firecracker/vmlinux}"
    ROOTFS_DIR="${ROOTFS_DIR:-/var/lib/firecracker/rootfs}"

    printf "\n${BOLD}Worker configuration${NC} (press Enter for defaults)\n"
    read -rp "  Hostname [${HOSTNAME}]: " INPUT
    HOSTNAME="${INPUT:-$HOSTNAME}"

    read -rp "  Manager address [${MANAGER_ADDR}]: " INPUT
    MANAGER_ADDR="${INPUT:-$MANAGER_ADDR}"

    if [ -z "$MANAGER_ADDR" ]; then
        error "Manager address is required (e.g., 192.168.1.10:4242)"
        exit 1
    fi

    read -rp "  Join token [${JOIN_TOKEN:-<from manager>}]: " INPUT
    JOIN_TOKEN="${INPUT:-$JOIN_TOKEN}"

    if [ -z "$JOIN_TOKEN" ]; then
        error "Join token is required. Get it from the manager node."
        exit 1
    fi

    read -rp "  Bridge name [${BRIDGE_NAME}]: " INPUT
    BRIDGE_NAME="${INPUT:-$BRIDGE_NAME}"

    read -rp "  Subnet [${SUBNET}]: " INPUT
    SUBNET="${INPUT:-$SUBNET}"

    read -rp "  Bridge IP [${BRIDGE_IP}]: " INPUT
    BRIDGE_IP="${INPUT:-$BRIDGE_IP}"

    read -rp "  State directory [${STATE_DIR}]: " INPUT
    STATE_DIR="${INPUT:-$STATE_DIR}"

    read -rp "  Kernel path [${KERNEL_PATH}]: " INPUT
    KERNEL_PATH="${INPUT:-$KERNEL_PATH}"

    read -rp "  Rootfs directory [${ROOTFS_DIR}]: " INPUT
    ROOTFS_DIR="${INPUT:-$ROOTFS_DIR}"

    # Install swarmd (SwarmKit)
    install_swarmd

    # Setup networking
    info "Setting up network bridge..."
    setup_bridge "$BRIDGE_NAME" "$BRIDGE_IP" "$SUBNET"

    # Setup Firecracker
    setup_firecracker "$KERNEL_PATH" "$ROOTFS_DIR"

    mkdir -p "$STATE_DIR"

    info "Starting swarmd-firecracker worker..."
    swarmd-firecracker \
        --state-dir "$STATE_DIR" \
        --hostname "$HOSTNAME" \
        --join-addr "$MANAGER_ADDR" \
        --join-token "$JOIN_TOKEN" \
        --listen-remote-api 0.0.0.0:4243 \
        --kernel-path "$KERNEL_PATH" \
        --rootfs-dir "$ROOTFS_DIR" \
        --bridge-name "$BRIDGE_NAME" \
        --subnet "$SUBNET" \
        --bridge-ip "$BRIDGE_IP" \
        --nat-enabled \
        &>/tmp/swarmcracker-worker.log &

    WORKER_PID=$!
    sleep 3

    if ! kill -0 "$WORKER_PID" 2>/dev/null; then
        error "Worker failed to start. Check /tmp/swarmcracker-worker.log"
        cat /tmp/swarmcracker-worker.log 2>/dev/null
        exit 1
    fi

    success "Worker running (PID ${WORKER_PID})"

    header "✅ Worker Ready"

    printf "\n"
    printf "${BOLD}  Worker Info${NC}\n"
    printf "  ─────────────────────────────────────\n"
    printf "  Hostname:    ${CYAN}${HOSTNAME}${NC}\n"
    printf "  Manager:     ${CYAN}${MANAGER_ADDR}${NC}\n"
    printf "  Bridge:      ${CYAN}${BRIDGE_NAME} (${BRIDGE_IP})${NC}\n"
    printf "  State dir:   ${CYAN}${STATE_DIR}${NC}\n"
    printf "  Remote API:  ${CYAN}0.0.0.0:4243${NC}\n"
    printf "\n"
}

# ─── Network bridge setup ───────────────────────────────────────────
setup_bridge() {
    local bridge="$1"
    local ip="$2"

    # Load required kernel modules
    modprobe br_netfilter 2>/dev/null || true
    modprobe vxlan 2>/dev/null || true

    # Enable IP forwarding
    sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1
    sysctl -w net.bridge.bridge-nf-call-iptables=0 >/dev/null 2>&1

    if ip link show "$bridge" &>/dev/null; then
        info "Bridge ${bridge} already exists"
    else
        ip link add name "$bridge" type bridge
        ip addr add "$ip" dev "$bridge"
        ip link set "$bridge" up
        sysctl -w "net.ipv4.conf.${bridge}.forwarding=1" >/dev/null 2>&1
        success "Bridge ${bridge} created (${ip})"
    fi
}

# ─── Firecracker setup ───────────────────────────────────────────────
setup_firecracker() {
    local kernel_path="$1"
    local rootfs_dir="$2"

    # Check if firecracker is installed
    if command -v firecracker &>/dev/null; then
        success "Firecracker already installed"
        return
    fi

    warn "Firecracker not found. Attempting to install..."

    # Try snap (Ubuntu/Debian)
    if command -v snap &>/dev/null; then
        info "Installing Firecracker via snap..."
        snap install firecracker --classic 2>/dev/null && {
            success "Firecracker installed via snap"
            return
        }
    fi

    # Try direct download
    local fc_arch
    fc_arch=$(detect_arch)
    if [ "$fc_arch" = "amd64" ]; then
        info "Downloading Firecracker v1.9.0..."
        local fc_tmp
        fc_tmp=$(mktemp -d)
        curl -fsSL "https://github.com/firecracker-microvm/firecracker/releases/download/v1.9.0/firecracker-v1.9.0-${fc_arch}.tgz" \
            -o "${fc_tmp}/firecracker.tgz"
        tar xzf "${fc_tmp}/firecracker.tgz" -C "${fc_tmp}"
        find "${fc_tmp}" -name 'firecracker' -type f -exec cp {} /usr/local/bin/ \;
        chmod +x /usr/local/bin/firecracker
        rm -rf "${fc_tmp}"
        success "Firecracker installed to /usr/local/bin/firecracker"
    else
        warn "Could not auto-install Firecracker for ${fc_arch}"
        warn "Install manually: https://github.com/firecracker-microvm/firecracker/releases"
    fi

    # Download kernel
    if [ ! -f "$kernel_path" ]; then
        warn "Kernel not found at ${kernel_path}"
        mkdir -p "$(dirname "$kernel_path")"
        info "Downloading Firecracker kernel..."
        curl -fsSL "https://s3.amazonaws.com/spec.ccfc.min/ci-artifacts/kernels/x86_64/vmlinux-5.10.217" \
            -o "$kernel_path" 2>/dev/null && {
            success "Kernel installed to ${kernel_path}"
        } || {
            warn "Kernel download failed — set --kernel-path manually"
        }
    else
        info "Kernel found at ${kernel_path}"
    fi

    # Create rootfs dir
    mkdir -p "$rootfs_dir"
}

# ─── SwarmKit (swarmd/swarmctl) setup ────────────────────────────────
install_swarmd() {
    if command -v swarmd &>/dev/null; then
        info "swarmd already installed"
        return
    fi

    info "Installing SwarmKit tools..."

    # Check if Go is available to build from source
    if command -v go &>/dev/null; then
        info "Building swarmd/swarmctl from source..."
        local sk_tmp
        sk_tmp=$(mktemp -d)
        git clone --depth 1 https://github.com/moby/swarmkit.git "$sk_tmp" 2>/dev/null
        cd "$sk_tmp"
        go build -o "${INSTALL_DIR}/swarmd" ./cmd/swarmd
        go build -o "${INSTALL_DIR}/swarmctl" ./cmd/swarmctl
        cd - >/dev/null
        rm -rf "$sk_tmp"
        success "swarmd + swarmctl built and installed"
    else
        warn "Go not found — swarmd/swarmctl not installed"
        warn "Install Go or provide swarmd/swarmctl binaries manually"
    fi
}

install_swarmctl() {
    if command -v swarmctl &>/dev/null; then
        return
    fi

    if command -v go &>/dev/null; then
        local sk_tmp
        sk_tmp=$(mktemp -d)
        git clone --depth 1 https://github.com/moby/swarmkit.git "$sk_tmp" 2>/dev/null
        cd "$sk_tmp"
        go build -o "${INSTALL_DIR}/swarmctl" ./cmd/swarmctl
        cd - >/dev/null
        rm -rf "$sk_tmp"
        success "swarmctl installed"
    fi
}

# ─── CLI flag parsing (for non-interactive use) ──────────────────────
if [ $# -gt 0 ]; then
    case "${1}" in
        --worker)
            while [ $# -gt 0 ]; do
                case "${1}" in
                    --manager) shift; MANAGER_ADDR="${1:-}" ;;
                    --token)   shift; JOIN_TOKEN="${1:-}" ;;
                    --hostname) shift; HOSTNAME="${1:-}" ;;
                    --bridge)  shift; BRIDGE_NAME="${1:-}" ;;
                    --subnet)  shift; SUBNET="${1:-}" ;;
                    --bridge-ip) shift; BRIDGE_IP="${1:-}" ;;
                    --state-dir) shift; STATE_DIR="${1:-}" ;;
                    --kernel-path) shift; KERNEL_PATH="${1:-}" ;;
                    --rootfs-dir) shift; ROOTFS_DIR="${1:-}" ;;
                    --install-dir) shift; INSTALL_DIR="${1:-}" ;;
                    --version)
                        echo "${VERSION}"
                        exit 0
                        ;;
                    -h|--help)
                        echo "Usage: install.sh [OPTIONS]"
                        echo ""
                        echo "  --worker         Set up as worker node"
                        echo "  --manager ADDR   Manager address (required for worker)"
                        echo "  --token TOKEN    Join token (required for worker)"
                        echo "  --hostname NAME  Node hostname"
                        echo "  --bridge NAME    Bridge name (default: swarm-br0)"
                        echo "  --subnet CIDR    Subnet (default: 192.168.127.0/24)"
                        echo "  --bridge-ip IP   Bridge IP (default: 192.168.127.1/24)"
                        echo "  --state-dir DIR  State directory"
                        echo "  --kernel-path    Kernel path"
                        echo "  --rootfs-dir DIR Rootfs directory"
                        echo "  --install-dir    Binary install dir (default: /usr/local/bin)"
                        echo "  --version        Print version and exit"
                        echo ""
                        echo "Interactive (no flags): choose manager/worker in terminal"
                        exit 0
                        ;;
                esac
                shift
            done
            setup_worker
            exit 0
            ;;
        --version)
            echo "${VERSION}"
            exit 0
            ;;
        -h|--help)
            echo "Usage: curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash"
            echo "       install.sh --worker --manager <IP>:4242 --token <TOKEN>"
            echo ""
            echo "Options:"
            echo "  --worker          Set up as worker (non-interactive)"
            echo "  --manager ADDR    Manager address for worker"
            echo "  --token TOKEN     Join token for worker"
            echo "  --version         Print latest version"
            echo "  --help            Show this help"
            exit 0
            ;;
        *)
            error "Unknown option: ${1}"
            echo "Run with --help for usage"
            exit 1
            ;;
    esac
fi
