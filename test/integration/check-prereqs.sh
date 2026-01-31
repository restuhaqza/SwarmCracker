#!/bin/bash
# Integration test setup helper for SwarmCracker

set -e

echo "🔥 SwarmCracker Integration Test Setup"
echo "======================================"
echo ""

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

check_pass() {
    echo -e "${GREEN}✓${NC} $1"
}

check_fail() {
    echo -e "${RED}✗${NC} $1"
}

check_warn() {
    echo -e "${YELLOW}⚠${NC} $1"
}

info() {
    echo "ℹ️  $1"
}

# Check Firecracker
echo "Checking Firecracker..."
if command -v firecracker &> /dev/null; then
    VERSION=$(firecracker --version 2>&1 | head -1)
    check_pass "Firecracker found: $VERSION"
    FIRECRACKER_INSTALLED=true
else
    check_fail "Firecracker not found"
    info "Install from: https://github.com/firecracker-microvm/firecracker/releases"
    info "  wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/firecracker-v1.8.0-x86_64"
    info "  sudo mv firecracker-v1.8.0-x86_64 /usr/local/bin/firecracker"
    info "  sudo chmod +x /usr/local/bin/firecracker"
    FIRECRACKER_INSTALLED=false
fi
echo ""

# Check Firecracker kernel
echo "Checking Firecracker kernel..."
KERNEL_PATHS=(
    "/usr/share/firecracker/vmlinux"
    "/boot/vmlinux"
    "/var/lib/firecracker/vmlinux"
)
KERNEL_FOUND=false
for path in "${KERNEL_PATHS[@]}"; do
    if [ -f "$path" ]; then
        check_pass "Kernel found: $path"
        KERNEL_FOUND=true
        break
    fi
done
if [ "$KERNEL_FOUND" = false ]; then
    check_fail "Firecracker kernel not found"
    info "Download from: https://github.com/firecracker-microvm/firecracker/releases"
    info "  sudo mkdir -p /usr/share/firecracker"
    info "  cd /usr/share/firecracker"
    info "  sudo wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/vmlinux-v1.8.0"
    info "  sudo mv vmlinux-v1.8.0 vmlinux"
fi
echo ""

# Check KVM
echo "Checking KVM..."
if [ -e /dev/kvm ]; then
    if groups | grep -q kvm; then
        check_pass "KVM device available and accessible"
        KVM_OK=true
    else
        check_warn "KVM device exists but user not in kvm group"
        info "Add user to kvm group: sudo usermod -aG kvm \$USER"
        info "Then log out and back in"
        KVM_OK=false
    fi
else
    check_fail "KVM device not available (/dev/kvm)"
    info "KVM requires hardware virtualization support"
    KVM_OK=false
fi
echo ""

# Check container runtimes
echo "Checking container runtimes..."
CONTAINER_RUNTIME=""
if command -v docker &> /dev/null; then
    DOCKER_VERSION=$(docker --version 2>&1)
    check_pass "$DOCKER_VERSION"
    CONTAINER_RUNTIME="docker"
elif command -v podman &> /dev/null; then
    PODMAN_VERSION=$(podman --version 2>&1)
    check_pass "$PODMAN_VERSION"
    CONTAINER_RUNTIME="podman"
else
    check_fail "No container runtime found (docker or podman required)"
    info "Install Docker:"
    info "  curl -fsSL https://get.docker.com | sudo sh"
    info "  sudo usermod -aG docker \$USER"
    info "Or install Podman:"
    info "  sudo apt install podman"
fi
echo ""

# Check network bridge (optional)
echo "Checking network configuration..."
if ip link show swarm-br0 &> /dev/null; then
    check_pass "Bridge swarm-br0 exists"
elif ip link show docker0 &> /dev/null; then
    check_warn "Docker bridge docker0 exists (can be used)"
    info "Create SwarmCracker bridge:"
    info "  sudo ip link add swarm-br0 type bridge"
    info "  sudo ip addr add 172.17.0.1/16 dev swarm-br0"
    info "  sudo ip link set swarm-br0 up"
else
    info "No bridge found (optional for tests)"
    info "Create with:"
    info "  sudo ip link add swarm-br0 type bridge"
    info "  sudo ip addr add 172.17.0.1/16 dev swarm-br0"
    info "  sudo ip link set swarm-br0 up"
fi
echo ""

# Summary
echo "======================================"
echo "Summary"
echo "======================================"

if [ "$FIRECRACKER_INSTALLED" = true ] && [ "$KERNEL_FOUND" = true ] && [ "$KVM_OK" = true ] && [ -n "$CONTAINER_RUNTIME" ]; then
    check_pass "All prerequisites installed!"
    echo ""
    echo "You can now run integration tests:"
    echo "  go test ./test/integration/... -v"
    echo ""
    echo "Or run prerequisites check:"
    echo "  go test ./test/integration/... -v -run TestIntegration_PrerequisitesOnly"
    exit 0
else
    check_fail "Some prerequisites are missing"
    echo ""
    echo "Please install missing components listed above"
    echo ""
    echo "Quick install (Ubuntu/Debian):"
    echo "  # Firecracker"
    echo "  wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/firecracker-v1.8.0-x86_64"
    echo "  sudo mv firecracker-v1.8.0-x86_64 /usr/local/bin/firecracker"
    echo "  sudo chmod +x /usr/local/bin/firecracker"
    echo ""
    echo "  # Kernel"
    echo "  sudo mkdir -p /usr/share/firecracker"
    echo "  cd /usr/share/firecracker"
    echo "  sudo wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/vmlinux-v1.8.0"
    echo "  sudo mv vmlinux-v1.8.0 vmlinux"
    echo ""
    echo "  # KVM access"
    echo "  sudo usermod -aG kvm \$USER"
    echo ""
    echo "  # Container runtime"
    echo "  curl -fsSL https://get.docker.com | sudo sh"
    echo "  sudo usermod -aG docker \$USER"
    echo ""
    echo "Then log out and back in for group changes to take effect"
    exit 1
fi
