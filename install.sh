#!/usr/bin/env bash
set -euo pipefail

# ─────────────────────────────────────────────────────────────────────
# SwarmCracker — one-line installer
#
# Downloads the latest release binary and verifies its checksum. That's
# all. Node setup (Firecracker, kernel, rootfs, bridge, config) is
# handled by the `swarmcracker setup` subcommand — see ADR-005
# "blessed deployment path":
#
#   curl -fsSL .../install.sh | sudo bash
#   sudo swarmcracker setup check
#   sudo swarmcracker setup install --download-kernel --download-rootfs
#   sudo swarmcracker setup network
#   sudo swarmcracker setup config --non-interactive
#   sudo swarmcracker cluster init        # manager node
#   sudo swarmcracker cluster join ...    # worker node
# ─────────────────────────────────────────────────────────────────────

REPO="${REPO:-restuhaqza/SwarmCracker}"
GITHUB="${GITHUB:-https://github.com}"
API="${API:-https://api.github.com/repos/${REPO}}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

info()    { printf "\033[0;32m[INFO]\033[0m   %s\n" "$*"; }
error()   { printf "\033[0;31m[ERROR]\033[0m  %s\n" "$*" >&2; }
success() { printf "\033[0;32m\033[1m  ✓\033[0m  %s\n" "$*"; }

detect_arch() {
    case "$(uname -m 2>/dev/null || echo unknown)" in
        x86_64|amd64)  echo amd64 ;;
        aarch64|arm64) echo arm64 ;;
        *) error "Unsupported architecture: $(uname -m)"; exit 1 ;;
    esac
}

detect_os() {
    case "$(uname -s 2>/dev/null || echo unknown)" in
        Linux)  echo linux ;;
        *) error "SwarmCracker requires Linux (KVM)"; exit 1 ;;
    esac
}

need_cmd() {
    command -v "$1" >/dev/null 2>&1 || { error "Required command not found: $1"; exit 1; }
}

show_help() {
    cat <<EOF
SwarmCracker installer — downloads the latest release binary + checksum verify.

Usage:
  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | sudo bash

Options:
  --install-dir DIR   Install binaries to DIR (default: ${INSTALL_DIR})
  --version           Print the latest release version and exit
  -h, --help          Show this help

After install (ADR-005 blessed path):
  sudo swarmcracker setup check
  sudo swarmcracker setup install --download-kernel --download-rootfs
  sudo swarmcracker setup network
  sudo swarmcracker setup config --non-interactive
  sudo swarmcracker cluster init        # manager node
  sudo swarmcracker cluster join ...    # worker node
EOF
}

# ─── CLI flags ───────────────────────────────────────────────────────
VERSION_ONLY=false
while [ $# -gt 0 ]; do
    case "$1" in
        --install-dir)
            shift
            [ $# -gt 0 ] || { error "--install-dir requires a value"; exit 1; }
            INSTALL_DIR="$1"
            ;;
        --version)
            VERSION_ONLY=true
            ;;
        -h|--help)
            show_help
            exit 0
            ;;
        *)
            error "Unknown option: $1"
            show_help
            exit 1
            ;;
    esac
    shift
done

need_cmd curl
need_cmd tar
need_cmd sha256sum

ARCH=$(detect_arch)
OS=$(detect_os)

# ─── Fetch latest release ────────────────────────────────────────────
info "SwarmCracker installer — latest release (${OS}/${ARCH})"

VERSION=$(curl -fsSL "${API}/releases/latest" 2>/dev/null \
    | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/' | head -1)

if [ -z "$VERSION" ]; then
    error "Could not fetch the latest release from GitHub."
    exit 1
fi

if $VERSION_ONLY; then
    echo "$VERSION"
    exit 0
fi

info "Latest version: ${VERSION}"

# ─── Download & verify ───────────────────────────────────────────────
TARBALL="swarmcracker-${VERSION}-${OS}-${ARCH}.tar.gz"
DOWNLOAD_URL="${GITHUB}/${REPO}/releases/download/${VERSION}/${TARBALL}"
CHECKSUM_URL="${GITHUB}/${REPO}/releases/download/${VERSION}/checksums.txt"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

info "Downloading ${TARBALL}..."
curl -fsSL "$DOWNLOAD_URL" -o "${TMPDIR}/${TARBALL}"

info "Verifying checksum..."
curl -fsSL "$CHECKSUM_URL" -o "${TMPDIR}/checksums.txt"

EXPECTED=$(grep "${TARBALL}" "${TMPDIR}/checksums.txt" | awk '{print $1}')
ACTUAL=$(sha256sum "${TMPDIR}/${TARBALL}" | awk '{print $1}')

if [ -z "$EXPECTED" ] || [ "$EXPECTED" != "$ACTUAL" ]; then
    error "Checksum mismatch for ${TARBALL}"
    error "  Expected: ${EXPECTED:-<not found in checksums.txt>}"
    error "  Actual:   ${ACTUAL}"
    exit 1
fi
success "Checksum verified (${ACTUAL:0:12}...)"

# ─── Extract & install ───────────────────────────────────────────────
info "Installing to ${INSTALL_DIR}..."
mkdir -p "$INSTALL_DIR"
tar xzf "${TMPDIR}/${TARBALL}" -C "$TMPDIR"

BINDIR="${TMPDIR}/swarmcracker-${VERSION}-${OS}-${ARCH}"
INSTALLED=0
for bin in swarmcracker swarmd-firecracker swarmcracker-agent; do
    if [ -f "${BINDIR}/${bin}" ]; then
        cp "${BINDIR}/${bin}" "${INSTALL_DIR}/${bin}"
        chmod +x "${INSTALL_DIR}/${bin}"
        success "${bin} → ${INSTALL_DIR}/${bin}"
        INSTALLED=$((INSTALLED + 1))
    fi
done

if [ "$INSTALLED" -eq 0 ]; then
    error "No binaries found in the release tarball — installation failed."
    exit 1
fi

if command -v "${INSTALL_DIR}/swarmcracker" >/dev/null 2>&1; then
    SWARMCRACKER_VERSION=$("${INSTALL_DIR}/swarmcracker" version 2>/dev/null | head -1)
    info "Installed: ${SWARMCRACKER_VERSION:-${VERSION}}"
fi

# ─── Next steps ──────────────────────────────────────────────────────
printf "\n\033[1mNext steps (ADR-005 blessed path):\033[0m\n"
printf "  sudo swarmcracker setup check\n"
printf "  sudo swarmcracker setup install --download-kernel --download-rootfs\n"
printf "  sudo swarmcracker setup network\n"
printf "  sudo swarmcracker setup config --non-interactive\n"
printf "  sudo swarmcracker cluster init        # manager node\n"
printf "  sudo swarmcracker cluster join ...    # worker node\n"
