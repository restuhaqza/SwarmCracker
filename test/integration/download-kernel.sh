#!/bin/bash
# Download Firecracker kernel script
set -e

echo "Downloading Firecracker-compatible kernel..."

# Check if we're running as root or can use sudo
SUDO=""
if [ "$EUID" -ne 0 ]; then
    SUDO="sudo"
fi

# Create target directory
echo "Creating kernel directory..."
$SUDO mkdir -p /usr/share/firecracker

# Try multiple sources for the kernel
KERNEL_SOURCES=(
    "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/vmlinux-5.10"
    "https://s3.amazonaws.com/dev.kernel.org/linux/kernel/v5.x/linux-5.10.tar.xz"
    "https://cdn.kernel.org/pub/linux/kernel/v5.x/linux-5.10.201.tar.xz"
)

DOWNLOADED=false
for source in "${KERNEL_SOURCES[@]}"; do
    echo "Trying to download from: $source"
    if wget -O /tmp/vmlinux "$source" 2>/dev/null || curl -L -o /tmp/vmlinux "$source" 2>/dev/null; then
        DOWNLOADED=true
        break
    fi
done

if [ "$DOWNLOADED" = true ]; then
    echo "✓ Kernel downloaded successfully"
    $SUDO mv /tmp/vmlinux /usr/share/firecracker/vmlinux
    $SUDO chmod +x /usr/share/firecracker/vmlinux
    echo "✓ Kernel installed to /usr/share/firecracker/vmlinux"

    # Check file size
    SIZE=$(stat -f%z /usr/share/firecracker/vmlinux 2>/dev/null || stat -c%s /usr/share/firecracker/vmlinux)
    if [ "$SIZE" -lt 1000000 ]; then
        echo "⚠ Warning: Kernel seems too small ($SIZE bytes)"
    fi

    exit 0
else
    echo "✗ Failed to download kernel from all sources"
    echo ""
    echo "Alternative options:"
    echo ""
    echo "1. Build from source:"
    echo "   git clone https://github.com/firecracker-microvm/firecracker.git"
    echo "   cd firecracker"
    echo "   ./tools/devtool build_kernel"
    echo "   cp build/kernel_args /usr/share/firecracker/vmlinux"
    echo ""
    echo "2. Use pre-built kernel from packages:"
    echo "   sudo apt install linux-image-cloud-amd64"
    echo "   # Then extract vmlinux from the deb package"
    echo ""
    echo "3. Download from community mirrors:"
    echo "   Check: https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md"
    echo ""
    exit 1
fi
