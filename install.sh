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

# ─── Defaults (can be overridden via CLI flags or env) ───────────────
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
STATE_DIR="${STATE_DIR:-}"
API_ADDR="${API_ADDR:-}"
HOSTNAME_OVERRIDE="${HOSTNAME_OVERRIDE:-}"
MANAGER_ADDR="${MANAGER_ADDR:-}"
JOIN_TOKEN="${JOIN_TOKEN:-}"
BRIDGE_NAME="${BRIDGE_NAME:-swarm-br0}"
SUBNET="${SUBNET:-192.168.127.0/24}"
BRIDGE_IP="${BRIDGE_IP:-192.168.127.1/24}"
KERNEL_PATH="${KERNEL_PATH:-/usr/share/firecracker/vmlinux}"
ROOTFS_DIR="${ROOTFS_DIR:-/var/lib/firecracker/rootfs}"
ADVERTISE_ADDR="${ADVERTISE_ADDR:-}"
VXLAN_ENABLED="${VXLAN_ENABLED:-false}"
VXLAN_PEERS="${VXLAN_PEERS:-}"
VCPUS="${VCPUS:-}"
MEMORY="${MEMORY:-}"
DEBUG_MODE="${DEBUG_MODE:-false}"

# ─── Helper functions ────────────────────────────────────────────────
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

    # Try direct download (latest release)
    local fc_arch
    fc_arch=$(detect_arch)
    # Firecracker uses x86_64/aarch64, not amd64/arm64
    [ "$fc_arch" = "amd64" ] && fc_arch="x86_64"
    [ "$fc_arch" = "arm64" ] && fc_arch="aarch64"
    local fc_ver="v1.15.0"
    info "Downloading Firecracker ${fc_ver}..."
    local fc_tmp
    fc_tmp=$(mktemp -d)
    local fc_url="https://github.com/firecracker-microvm/firecracker/releases/download/${fc_ver}/firecracker-${fc_ver}-${fc_arch}.tgz"
    if curl -fsSL "$fc_url" -o "${fc_tmp}/firecracker.tgz" 2>/dev/null; then
        tar xzf "${fc_tmp}/firecracker.tgz" -C "${fc_tmp}"
        # Binary is named firecracker-<ver>-<arch> inside release-<ver>-<arch>/
        local fc_bin
        fc_bin=$(find "${fc_tmp}" -name 'firecracker-*' -type f ! -name '*.debug' ! -name '*.json' ! -name '*.yaml' ! -name 'SHA256SUMS' | head -1)
        if [ -n "$fc_bin" ]; then
            cp "$fc_bin" "${INSTALL_DIR}/firecracker"
            chmod +x "${INSTALL_DIR}/firecracker"
            success "Firecracker installed to ${INSTALL_DIR}/firecracker"
        else
            warn "Could not find firecracker binary in archive"
        fi
        rm -rf "${fc_tmp}"
    else
        warn "Failed to download Firecracker ${fc_ver}"
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
    # Look in common paths even if not in PATH
    local go_bin=""
    if command -v go &>/dev/null; then
        go_bin="go"
    elif [ -x /usr/local/go/bin/go ]; then
        go_bin="/usr/local/go/bin/go"
    elif [ -x /usr/lib/go/bin/go ]; then
        go_bin="/usr/lib/go/bin/go"
    fi
    export PATH="/usr/local/go/bin:/usr/lib/go/bin:${PATH}"
    if [ -n "$go_bin" ]; then
        info "Building swarmd/swarmctl from source..."
        local sk_tmp
        sk_tmp=$(mktemp -d)
        git clone --depth 1 https://github.com/moby/swarmkit.git "$sk_tmp" 2>/dev/null
        cd "$sk_tmp/swarmd"
        "$go_bin" build -o "${INSTALL_DIR}/swarmd" ./cmd/swarmd
        "$go_bin" build -o "${INSTALL_DIR}/swarmctl" ./cmd/swarmctl
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

    export PATH="/usr/local/go/bin:/usr/lib/go/bin:${PATH}"
    local go_bin=""
    if command -v go &>/dev/null; then
        go_bin="go"
    elif [ -x /usr/local/go/bin/go ]; then
        go_bin="/usr/local/go/bin/go"
    fi
    if [ -n "$go_bin" ]; then
        local sk_tmp
        sk_tmp=$(mktemp -d)
        git clone --depth 1 https://github.com/moby/swarmkit.git "$sk_tmp" 2>/dev/null
        cd "$sk_tmp/swarmd"
        "$go_bin" build -o "${INSTALL_DIR}/swarmctl" ./cmd/swarmctl
        cd - >/dev/null
        rm -rf "$sk_tmp"
        success "swarmctl installed"
    fi
}

# ─── Manager setup ───────────────────────────────────────────────────
do_setup_manager() {
    header "🏛️  Manager Setup"

    # Check KVM
    if [ ! -e /dev/kvm ]; then
        warn "/dev/kvm not found — Firecracker VMs will not work on this host"
    fi

    # Defaults
    local state_dir="${STATE_DIR:-/var/lib/swarmcracker/manager}"
    local api_addr="${API_ADDR:-0.0.0.0:4242}"
    local hostname="${HOSTNAME_OVERRIDE:-$(hostname 2>/dev/null || echo 'manager')}"

    printf "\n${BOLD}Manager configuration${NC} (press Enter for defaults)\n"
    read -rp "  Hostname [${hostname}]: " INPUT
    hostname="${INPUT:-$hostname}"

    read -rp "  Listen address [${api_addr}]: " INPUT
    api_addr="${INPUT:-$api_addr}"

    read -rp "  State directory [${state_dir}]: " INPUT
    state_dir="${INPUT:-$state_dir}"

    # Install swarmd (SwarmKit)
    install_swarmd

    mkdir -p "$state_dir"

    info "Initializing SwarmKit manager..."
    swarmd \
        -d "$state_dir" \
        --hostname "$hostname" \
        --listen-control-api "${state_dir}/swarm.sock" \
        --listen-remote-api "$api_addr" \
        --join-addr "" \
        &>/tmp/swarmcracker-manager.log &

    local swarmpid=$!
    sleep 3

    if ! kill -0 "$swarmpid" 2>/dev/null; then
        error "SwarmKit manager failed to start. Check /tmp/swarmcracker-manager.log"
        cat /tmp/swarmcracker-manager.log 2>/dev/null
        exit 1
    fi

    success "Manager running (PID ${swarmpid})"

    # Wait for socket
    local retries=10
    while [ ! -S "${state_dir}/swarm.sock" ] && [ $retries -gt 0 ]; do
        sleep 1
        retries=$((retries - 1))
    done

    if [ ! -S "${state_dir}/swarm.sock" ]; then
        warn "Manager socket not ready. You can check status later with:"
        warn "  swarmctl --socket ${state_dir}/swarm.sock node ls"
    fi

    # Install swarmctl for convenience
    install_swarmctl

    # Get join tokens
    local worker_token=""
    if command -v swarmctl &>/dev/null && [ -S "${state_dir}/swarm.sock" ]; then
        local cluster_json
        cluster_json=$(swarmctl --socket "${state_dir}/swarm.sock" cluster inspect default 2>/dev/null || true)
        worker_token=$(echo "$cluster_json" 2>/dev/null | grep -oP 'SWMTKN-[0-9a-zA-Z-]+' | head -1 || echo "")
    fi

    # Detect manager IP (prefer non-10.0.2.x / non-NAT interfaces)
    local local_ip=""
    for iface in $(ls /sys/class/net/ 2>/dev/null | grep -v lo); do
        local candidate
        candidate=$(ip -4 addr show dev "$iface" 2>/dev/null | grep -oP 'inet \K[0-9.]+')
        if [ -n "$candidate" ] && [[ "$candidate" != 10.0.2.* ]]; then
            local_ip="$candidate"
            break
        fi
    done
    if [ -z "$local_ip" ]; then
        local_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
    fi

    # Print summary
    header "✅ Manager Ready"

    printf "\n"
    printf "${BOLD}  Manager Info${NC}\n"
    printf "  ─────────────────────────────────────\n"
    printf "  Hostname:    ${CYAN}${hostname}${NC}\n"
    printf "  API:         ${CYAN}%s${NC}\n" "$api_addr"
    printf "  State dir:   ${CYAN}${state_dir}${NC}\n"
    printf "  Socket:      ${CYAN}${state_dir}/swarm.sock${NC}\n"

    if [ -n "$local_ip" ]; then
        printf "  IP:          ${CYAN}${local_ip}${NC}\n"
    fi

    printf "\n${BOLD}  To add workers, run this on each worker node:${NC}\n"
    printf "${YELLOW}  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash -s -- --worker \\\n"
    printf "    --manager ${local_ip:-<MANAGER_IP>}:4242 \\\n"
    if [ -n "$worker_token" ]; then
        printf "    --token ${worker_token}${NC}\n"
    else
        printf "    --token <WORKER_TOKEN>${NC}\n"
    fi

    printf "\n${BOLD}  Useful commands:${NC}\n"
    printf "  swarmctl --socket ${state_dir}/swarm.sock node ls\n"
    printf "  swarmctl --socket ${state_dir}/swarm.sock service create --name nginx --image nginx:alpine\n"
    printf "\n"
}

# ─── Worker setup ────────────────────────────────────────────────────
do_setup_worker() {
    header "⛏️  Worker Setup"

    if [ ! -e /dev/kvm ]; then
        error "/dev/kvm not found — Firecracker VMs require KVM"
        exit 1
    fi

    # Defaults
    local manager_addr="${MANAGER_ADDR:-}"
    local join_token="${JOIN_TOKEN:-}"
    local hostname="${HOSTNAME_OVERRIDE:-$(hostname 2>/dev/null || echo "worker-$(hostname -s 2>/dev/null || echo $$)")}"
    local bridge_name="${BRIDGE_NAME:-swarm-br0}"
    local subnet="${SUBNET:-192.168.127.0/24}"
    local bridge_ip="${BRIDGE_IP:-192.168.127.1/24}"
    local state_dir="${STATE_DIR:-/var/lib/swarmcracker/worker}"
    local kernel_path="${KERNEL_PATH:-/usr/share/firecracker/vmlinux}"
    local rootfs_dir="${ROOTFS_DIR:-/var/lib/firecracker/rootfs}"

    # If --worker was passed with --manager and --token, skip interactive prompts
    if $SKIP_MENU && [ -n "$manager_addr" ] && [ -n "$join_token" ]; then
        info "Non-interactive worker setup (all params from flags)"
    else
        printf "\n${BOLD}Worker configuration${NC} (press Enter for defaults)\n"
        read -rp "  Hostname [${hostname}]: " INPUT
        hostname="${INPUT:-$hostname}"

        read -rp "  Manager address [${manager_addr}]: " INPUT
        manager_addr="${INPUT:-$manager_addr}"

        if [ -z "$manager_addr" ]; then
            error "Manager address is required (e.g., 192.168.1.10:4242)"
            exit 1
        fi

        read -rp "  Join token [${join_token:-<from manager>}]: " INPUT
        join_token="${INPUT:-$join_token}"

        if [ -z "$join_token" ]; then
            error "Join token is required. Get it from the manager node."
            exit 1
        fi

        read -rp "  Bridge name [${bridge_name}]: " INPUT
        bridge_name="${INPUT:-$bridge_name}"

        read -rp "  Subnet [${subnet}]: " INPUT
        subnet="${INPUT:-$subnet}"

        read -rp "  Bridge IP [${bridge_ip}]: " INPUT
        bridge_ip="${INPUT:-$bridge_ip}"

        read -rp "  State directory [${state_dir}]: " INPUT
        state_dir="${INPUT:-$state_dir}"

        read -rp "  Kernel path [${kernel_path}]: " INPUT
        kernel_path="${INPUT:-$kernel_path}"

        read -rp "  Rootfs directory [${rootfs_dir}]: " INPUT
        rootfs_dir="${INPUT:-$rootfs_dir}"
    fi

    # Install swarmd (SwarmKit)
    install_swarmd

    # Setup networking
    info "Setting up network bridge..."
    setup_bridge "$bridge_name" "$bridge_ip" "$subnet"

    # Setup Firecracker
    setup_firecracker "$kernel_path" "$rootfs_dir"

    mkdir -p "$state_dir"

    info "Starting swarmd-firecracker worker..."
    swarmd-firecracker \
        --state-dir "$state_dir" \
        --hostname "$hostname" \
        --join-addr "$manager_addr" \
        --join-token "$join_token" \
        --listen-remote-api 0.0.0.0:4243 \
        --kernel-path "$kernel_path" \
        --rootfs-dir "$rootfs_dir" \
        --bridge-name "$bridge_name" \
        --subnet "$subnet" \
        --bridge-ip "$bridge_ip" \
        --nat-enabled \
        &>/tmp/swarmcracker-worker.log &

    local workerpid=$!
    sleep 3

    if ! kill -0 "$workerpid" 2>/dev/null; then
        error "Worker failed to start. Check /tmp/swarmcracker-worker.log"
        cat /tmp/swarmcracker-worker.log 2>/dev/null
        exit 1
    fi

    success "Worker running (PID ${workerpid})"

    header "✅ Worker Ready"

    printf "\n"
    printf "${BOLD}  Worker Info${NC}\n"
    printf "  ─────────────────────────────────────\n"
    printf "  Hostname:    ${CYAN}${hostname}${NC}\n"
    printf "  Manager:     ${CYAN}${manager_addr}${NC}\n"
    printf "  Bridge:      ${CYAN}${bridge_name} (${bridge_ip})${NC}\n"
    printf "  State dir:   ${CYAN}${state_dir}${NC}\n"
    printf "  Remote API:  ${CYAN}0.0.0.0:4243${NC}\n"
    printf "\n"
}

# ─── Show help ───────────────────────────────────────────────────────
show_help() {
    cat <<EOF
SwarmCracker Installer

Usage:
  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash
  install.sh [COMMAND] [OPTIONS]

Commands:
  init               Initialize a new SwarmCracker cluster (manager node)
  join               Join an existing cluster (worker node)

Options:
  --worker           Set up as worker node (legacy mode)
  --manager ADDR     Manager address for worker (required with --worker or join)
  --token TOKEN      Join token for worker (required with --worker or join)
  --hostname NAME    Node hostname
  --bridge NAME      Bridge name (default: swarm-br0)
  --subnet CIDR      Subnet (default: 192.168.127.0/24)
  --bridge-ip IP     Bridge IP (default: 192.168.127.1/24)
  --state-dir DIR    State directory
  --kernel-path      Kernel path
  --rootfs-dir DIR   Rootfs directory
  --install-dir      Binary install dir (default: /usr/local/bin)
  --advertise-addr   Address to advertise (auto-detected if not set)
  --vxlan-enabled    Enable VXLAN overlay networking
  --vxlan-peers      Comma-separated VXLAN peer IPs
  --vcpus            Default vCPUs per microVM (default: 1)
  --memory           Default memory MB per microVM (default: 512)
  --debug            Enable debug logging
  --version          Print latest version and exit
  -h, --help         Show this help

Examples:
  # Interactive install
  curl -fsSL ... | bash

  # Initialize manager (new way)
  curl -fsSL ... | bash -s -- init

  # Initialize with VXLAN
  curl -fsSL ... | bash -s -- init --vxlan-enabled --vxlan-peers 192.168.1.11,192.168.1.12

  # Join worker (new way)
  curl -fsSL ... | bash -s -- join --manager 192.168.1.10:4242 --token SWMTKN-1-...

  # Legacy worker setup
  curl -fsSL ... | bash -s -- --worker --manager 192.168.1.10:4242 --token SWMTKN-1-...
EOF
}

# ─── CLI flag parsing ────────────────────────────────────────────────
SKIP_MENU=false
INIT_MODE=false
JOIN_MODE=false

while [ $# -gt 0 ]; do
    case "${1}" in
        init)
            INIT_MODE=true
            SKIP_MENU=true
            ;;
        join)
            JOIN_MODE=true
            SKIP_MENU=true
            ;;
        --worker)
            SKIP_MENU=true
            ;;
        --manager)
            shift; MANAGER_ADDR="${1:-}"
            ;;
        --token)
            shift; JOIN_TOKEN="${1:-}"
            ;;
        --hostname)
            shift; HOSTNAME_OVERRIDE="${1:-}"
            ;;
        --bridge)
            shift; BRIDGE_NAME="${1:-}"
            ;;
        --subnet)
            shift; SUBNET="${1:-}"
            ;;
        --bridge-ip)
            shift; BRIDGE_IP="${1:-}"
            ;;
        --state-dir)
            shift; STATE_DIR="${1:-}"
            ;;
        --kernel-path)
            shift; KERNEL_PATH="${1:-}"
            ;;
        --rootfs-dir)
            shift; ROOTFS_DIR="${1:-}"
            ;;
        --install-dir)
            shift; INSTALL_DIR="${1:-}"
            ;;
        --advertise-addr)
            shift; ADVERTISE_ADDR="${1:-}"
            ;;
        --vxlan-enabled)
            VXLAN_ENABLED=true
            ;;
        --vxlan-peers)
            shift; VXLAN_PEERS="${1:-}"
            ;;
        --vcpus)
            shift; VCPUS="${1:-}"
            ;;
        --memory)
            shift; MEMORY="${1:-}"
            ;;
        --debug)
            DEBUG_MODE=true
            ;;
        --version)
            # We need to fetch version, so run minimal version of that
            need_cmd curl
            local_ver=$(curl -fsSL "${API}/releases/latest" 2>/dev/null | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
            echo "${local_ver:-unknown}"
            exit 0
            ;;
        -h|--help)
            show_help
            exit 0
            ;;
        *)
            error "Unknown option: ${1}"
            show_help
            exit 1
            ;;
    esac
    shift
done

# ─── Pre-flight ──────────────────────────────────────────────────────
need_cmd curl
need_cmd tar
need_cmd sha256sum

ARCH=$(detect_arch)
OS=$(detect_os)

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

# INIT MODE: Initialize new cluster
if $INIT_MODE; then
    header "🚀 Initializing SwarmCracker Cluster"
    
    # Build init command
    INIT_CMD="${INSTALL_DIR}/swarmcracker init"
    
    [ -n "$ADVERTISE_ADDR" ] && INIT_CMD="$INIT_CMD --advertise-addr $ADVERTISE_ADDR"
    [ -n "$HOSTNAME_OVERRIDE" ] && INIT_CMD="$INIT_CMD --hostname $HOSTNAME_OVERRIDE"
    [ -n "$STATE_DIR" ] && INIT_CMD="$INIT_CMD --state-dir $STATE_DIR"
    [ -n "$BRIDGE_NAME" ] && INIT_CMD="$INIT_CMD --bridge-name $BRIDGE_NAME"
    [ -n "$SUBNET" ] && INIT_CMD="$INIT_CMD --subnet $SUBNET"
    [ -n "$BRIDGE_IP" ] && INIT_CMD="$INIT_CMD --bridge-ip $BRIDGE_IP"
    [ -n "$VCPUS" ] && INIT_CMD="$INIT_CMD --vcpus $VCPUS"
    [ -n "$MEMORY" ] && INIT_CMD="$INIT_CMD --memory $MEMORY"
    [ -n "$KERNEL_PATH" ] && INIT_CMD="$INIT_CMD --kernel $KERNEL_PATH"
    [ -n "$ROOTFS_DIR" ] && INIT_CMD="$INIT_CMD --rootfs-dir $ROOTFS_DIR"
    [ "$VXLAN_ENABLED" = "true" ] && INIT_CMD="$INIT_CMD --vxlan-enabled"
    [ -n "$VXLAN_PEERS" ] && INIT_CMD="$INIT_CMD --vxlan-peers $VXLAN_PEERS"
    [ "$DEBUG_MODE" = "true" ] && INIT_CMD="$INIT_CMD --debug"
    
    info "Running: $INIT_CMD"
    if eval "$INIT_CMD"; then
        header "✅ Installation & Initialization Complete"
        printf "\n${GREEN}SwarmCracker cluster is ready!${NC}\n\n"
        printf "Next steps:\n"
        printf "  1. Get join token: ${CYAN}sudo cat /var/lib/swarmkit/join-tokens.txt${NC}\n"
        printf "  2. On workers, run:\n"
        printf "     ${YELLOW}curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | sudo bash -s -- join \\${NC}\n"
        printf "       ${YELLOW}--manager <MANAGER_IP>:4242 --token <TOKEN>${NC}\n"
        printf "\n"
        exit 0
    else
        error "Cluster initialization failed"
        exit 1
    fi
fi

# JOIN MODE: Join existing cluster
if $JOIN_MODE; then
    header "🔗 Joining SwarmCracker Cluster"
    
    if [ -z "$MANAGER_ADDR" ]; then
        error "--manager <ADDR> is required for join"
        exit 1
    fi
    
    if [ -z "$JOIN_TOKEN" ]; then
        error "--token <TOKEN> is required for join"
        exit 1
    fi
    
    # Build join command
    JOIN_CMD="${INSTALL_DIR}/swarmcracker join $MANAGER_ADDR --token $JOIN_TOKEN"
    
    [ -n "$HOSTNAME_OVERRIDE" ] && JOIN_CMD="$JOIN_CMD --hostname $HOSTNAME_OVERRIDE"
    [ -n "$STATE_DIR" ] && JOIN_CMD="$JOIN_CMD --state-dir $STATE_DIR"
    [ -n "$BRIDGE_NAME" ] && JOIN_CMD="$JOIN_CMD --bridge-name $BRIDGE_NAME"
    [ -n "$SUBNET" ] && JOIN_CMD="$JOIN_CMD --subnet $SUBNET"
    [ -n "$BRIDGE_IP" ] && JOIN_CMD="$JOIN_CMD --bridge-ip $BRIDGE_IP"
    [ -n "$VCPUS" ] && JOIN_CMD="$JOIN_CMD --vcpus $VCPUS"
    [ -n "$MEMORY" ] && JOIN_CMD="$JOIN_CMD --memory $MEMORY"
    [ -n "$KERNEL_PATH" ] && JOIN_CMD="$JOIN_CMD --kernel $KERNEL_PATH"
    [ -n "$ROOTFS_DIR" ] && JOIN_CMD="$JOIN_CMD --rootfs-dir $ROOTFS_DIR"
    [ "$VXLAN_ENABLED" = "true" ] && JOIN_CMD="$JOIN_CMD --vxlan-enabled"
    [ -n "$VXLAN_PEERS" ] && JOIN_CMD="$JOIN_CMD --vxlan-peers $VXLAN_PEERS"
    [ "$DEBUG_MODE" = "true" ] && JOIN_CMD="$JOIN_CMD --debug"
    
    info "Running: $JOIN_CMD"
    if eval "$JOIN_CMD"; then
        header "✅ Installation & Join Complete"
        printf "\n${GREEN}Node joined the cluster successfully!${NC}\n\n"
        printf "Check status: ${CYAN}swarmcracker status${NC}\n"
        printf "View logs: ${CYAN}sudo journalctl -u swarmcracker-worker -f${NC}\n\n"
        exit 0
    else
        error "Failed to join cluster"
        exit 1
    fi
fi

# LEGACY WORKER MODE (for backward compatibility)
if $SKIP_MENU; then
    if [ -z "$MANAGER_ADDR" ] || [ -z "$JOIN_TOKEN" ]; then
        error "--worker requires --manager <ADDR> and --token <TOKEN>"
        exit 1
    fi
    do_setup_worker
    exit 0
fi

# Interactive menu
header "⚙️  Node Setup"

printf "\n${BOLD}Select node type:${NC}\n"
printf "  ${GREEN}1)${NC} Manager (initialize SwarmKit cluster)\n"
printf "  ${GREEN}2)${NC} Worker (join existing cluster)\n"
printf "  ${GREEN}3)${NC} Skip — just install binaries\n\n"

read -rp "  [1/2/3]: " NODE_TYPE

case "$NODE_TYPE" in
    1)
        do_setup_manager
        ;;
    2)
        do_setup_worker
        ;;
    3)
        header "✅ Done"
        success "SwarmCracker ${VERSION} installed to ${INSTALL_DIR}"
        info "Run with --worker --manager <IP>:4242 --token <TOKEN> to configure a node"
        ;;
    *)
        error "Invalid choice"
        exit 1
        ;;
esac
