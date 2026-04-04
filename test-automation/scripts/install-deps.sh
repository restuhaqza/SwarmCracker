#!/bin/bash
# install-deps.sh - Install all SwarmCracker dependencies
# Run on all VMs (manager + workers)

set -e

echo "🔧 Installing SwarmCracker dependencies..."

# Update package list
export DEBIAN_FRONTEND=noninteractive
apt-get update

# Install essential packages (NOT including golang from apt - we'll install from binary)
apt-get install -y \
  qemu-kvm \
  libvirt-daemon-system \
  libvirt-clients \
  bridge-utils \
  git \
  build-essential \
  curl \
  wget \
  jq \
  iputils-ping \
  net-tools \
  tcpdump \
  podman \
  tini


# Install Go from official binary (reliable method)
echo "📦 Installing Go from official binary..."
GO_VERSION="1.24.0"
if [ ! -f "/usr/local/go/bin/go" ]; then
    wget -q https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf go${GO_VERSION}.linux-amd64.tar.gz
    rm go${GO_VERSION}.linux-amd64.tar.gz
    
    # Set Go as default using update-alternatives
    update-alternatives --install /usr/bin/go go /usr/local/go/bin/go 1
    update-alternatives --set go /usr/local/go/bin/go
fi

# Add Go to PATH for current session
export PATH=$PATH:/usr/local/go/bin
echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile.d/go-path.sh
chmod +x /etc/profile.d/go-path.sh

# Verify Go installation
if ! command -v go &> /dev/null; then
    echo "❌ Go installation failed!"
    exit 1
fi

# Verify KVM availability
if [ ! -e /dev/kvm ]; then
  echo "❌ ERROR: /dev/kvm not found!"
  echo "KVM virtualization is required for Firecracker"
  exit 1
fi

# Add vagrant user to kvm group
usermod -aG kvm vagrant
echo "✅ Added vagrant user to kvm group"

# Install Firecracker v1.14.1 (latest stable)
echo "📥 Installing Firecracker v1.14.1..."
cd /tmp
wget --progress=bar:force:noscroll https://github.com/firecracker-microvm/firecracker/releases/download/v1.14.1/firecracker-v1.14.1-x86_64.tgz
if [ ! -f "firecracker-v1.14.1-x86_64.tgz" ]; then
    echo "❌ Failed to download Firecracker"
    exit 1
fi
tar -xzf firecracker-v1.14.1-x86_64.tgz
# Firecracker v1.14.1 structure: release-v1.14.1-x86_64/firecracker-v1.14.1-x86_64
FC_DIR=$(find . -maxdepth 1 -type d -name "release-*" | head -1)
if [ -z "$FC_DIR" ]; then
    echo "❌ Firecracker release directory not found"
    echo "Archive contents:"
    tar -tzf firecracker-v1.14.1-x86_64.tgz | head -20
    ls -lah
    exit 1
fi
FC_BINARY="$FC_DIR/firecracker-v1.14.1-x86_64"
if [ ! -f "$FC_BINARY" ]; then
    echo "❌ Firecracker binary not found at $FC_BINARY"
    echo "Directory contents:"
    ls -lah "$FC_DIR"
    exit 1
fi
mv "$FC_BINARY" /usr/local/bin/firecracker
chmod +x /usr/local/bin/firecracker
rm -rf firecracker-v1.14.1-x86_64.tgz "$FC_DIR"

# Download Firecracker kernel from official S3 bucket
echo "📥 Downloading Firecracker kernel..."
# Use latest Firecracker v1.15 kernel (Linux 5.10.245)
KERNEL_URL="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-5.10.245"
curl -sL "$KERNEL_URL" -o kernel-vmlinux-x86_64.bin || {
    echo "⚠️  Failed to download kernel from S3, trying fallback..."
    # Fallback to v1.10 kernel
    curl -sL "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.10/x86_64/vmlinux-5.10" -o kernel-vmlinux-x86_64.bin || {
        echo "⚠️  Warning: Could not download Firecracker kernel"
        echo "   Kernel will need to be installed manually for Firecracker to work"
        echo "   Create a placeholder for now"
        mkdir -p /usr/share/firecracker
        touch /usr/share/firecracker/vmlinux
    }
}

if [ -f "kernel-vmlinux-x86_64.bin" ] && [ -s "kernel-vmlinux-x86_64.bin" ]; then
    mkdir -p /usr/share/firecracker
    mv kernel-vmlinux-x86_64.bin /usr/share/firecracker/vmlinux
    chmod +r /usr/share/firecracker/vmlinux
fi

# Verify installations
echo ""
echo "🔍 Verifying installations..."
echo "Go version: $(go version)"
echo "Firecracker version: $(firecracker --version)"
echo "KVM: $(ls -l /dev/kvm)"

# Create required directories
mkdir -p /etc/swarmcracker
mkdir -p /var/lib/swarmkit
mkdir -p /var/lib/firecracker/rootfs
mkdir -p /var/lib/firecracker/logs
mkdir -p /var/run/swarmkit

echo ""
echo "✅ Dependencies installed successfully!"
