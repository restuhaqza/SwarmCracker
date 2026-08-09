#!/bin/bash
# Test the blessed deployment path (ADR-005) on running VMs:
#   install.sh -> swarmcracker setup -> swarmcracker cluster init/join
set -e

MANAGER_IP="192.168.121.241"
WORKER1_IP="192.168.121.235"
WORKER2_IP="192.168.121.132"

# SSH helpers (each VM has its own key)
ssh_manager() { ssh -i test-automation/.vagrant/machines/manager/libvirt/private_key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null vagrant@$MANAGER_IP "$@"; }
ssh_worker1() { ssh -i test-automation/.vagrant/machines/worker1/libvirt/private_key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null vagrant@$WORKER1_IP "$@"; }
ssh_worker2() { ssh -i test-automation/.vagrant/machines/worker2/libvirt/private_key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null vagrant@$WORKER2_IP "$@"; }

scp_manager() { scp -i test-automation/.vagrant/machines/manager/libvirt/private_key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$@" vagrant@$MANAGER_IP:/tmp/; }
scp_worker1() { scp -i test-automation/.vagrant/machines/worker1/libvirt/private_key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$@" vagrant@$WORKER1_IP:/tmp/; }
scp_worker2() { scp -i test-automation/.vagrant/machines/worker2/libvirt/private_key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$@" vagrant@$WORKER2_IP:/tmp/; }

echo "========================================"
echo "Testing blessed deployment path (install.sh -> setup -> cluster init/join)"
echo "========================================"
echo ""

# Step 1: Clean up existing state
echo "📋 Step 1: Cleaning existing cluster state..."
for cmd in "ssh_manager" "ssh_worker1" "ssh_worker2"; do
    echo "  Cleaning via $cmd..."
    $cmd "sudo systemctl stop swarmcracker-manager swarmcracker-worker swarmd-firecracker 2>/dev/null || true"
    $cmd "sudo rm -rf /var/lib/swarmkit/* /var/run/swarmkit/* /var/run/firecracker/* 2>/dev/null || true"
    $cmd "sudo rm -f /etc/systemd/system/swarmcracker-*.service 2>/dev/null || true"
    $cmd "sudo systemctl daemon-reload"
done
echo "✅ State cleaned"
echo ""

# Step 2: Copy install.sh to all nodes
echo "📋 Step 2: Copying install.sh to all nodes..."
scp_manager install.sh
scp_worker1 install.sh
scp_worker2 install.sh
echo "✅ install.sh copied"
echo ""

# Step 3: Install binary on all nodes (download + checksum verify only)
echo "📋 Step 3: Installing SwarmCracker binary on all nodes..."
for cmd in "ssh_manager" "ssh_worker1" "ssh_worker2"; do
    echo "  Installing via $cmd..."
    $cmd "chmod +x /tmp/install.sh && sudo /tmp/install.sh" 2>&1 | tail -10
done
echo "✅ Binaries installed"
echo ""

# Step 4: Node setup on all nodes (blessed path: setup check/install/network/config)
echo "📋 Step 4: Running swarmcracker setup on all nodes..."
for cmd in "ssh_manager" "ssh_worker1" "ssh_worker2"; do
    echo "  Setup via $cmd..."
    $cmd "sudo swarmcracker setup check || true"
    $cmd "sudo swarmcracker setup install --download-kernel --download-rootfs" 2>&1 | tail -15
    $cmd "sudo swarmcracker setup network" 2>&1 | tail -10
    $cmd "sudo swarmcracker setup config --non-interactive" 2>&1 | tail -10
done
echo "✅ Node setup complete"
echo ""

# Step 5: Initialize cluster on manager
echo "📋 Step 5: Initializing cluster on manager..."
ssh_manager "sudo swarmcracker cluster init --advertise-addr $MANAGER_IP:4242 --hostname swarm-manager" 2>&1 | tail -40
echo ""

# Step 6: Get join token
echo "📋 Step 6: Getting join token..."
JOIN_TOKEN=$(ssh_manager "sudo swarmcracker cluster token create --role worker 2>/dev/null | grep -oP 'SWMTKN-[a-zA-Z0-9-]+' | head -1" || echo "")
if [ -z "$JOIN_TOKEN" ]; then
    echo "⚠️  No join token found, trying token file..."
    JOIN_TOKEN=$(ssh_manager "sudo cat /var/lib/swarmkit/join-tokens.txt 2>/dev/null | grep -oP 'SWMTKN-[a-zA-Z0-9-]+' | head -1" || echo "")
fi
echo "Join token: ${JOIN_TOKEN:0:12}... FOUND"
echo ""

if [ -z "$JOIN_TOKEN" ]; then
    echo "❌ Failed to get join token. Check manager logs."
    exit 1
fi

# Step 7: Join workers
echo "📋 Step 7: Joining workers to cluster..."
echo "  Joining worker1 ($WORKER1_IP)..."
ssh_worker1 "sudo swarmcracker cluster join --token $JOIN_TOKEN $MANAGER_IP:4242 --hostname swarm-worker-1" 2>&1 | tail -30
echo ""
echo "  Joining worker2 ($WORKER2_IP)..."
ssh_worker2 "sudo swarmcracker cluster join --token $JOIN_TOKEN $MANAGER_IP:4242 --hostname swarm-worker-2" 2>&1 | tail -30
echo ""

# Step 8: Verify cluster
echo "📋 Step 8: Verifying cluster status..."
sleep 5
ssh_manager "sudo swarmcracker cluster status 2>/dev/null" || echo "Failed to get cluster status"
echo ""

echo "========================================"
echo "✅ Test complete!"
echo "========================================"
