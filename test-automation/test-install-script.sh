#!/bin/bash
# Test install.sh on running VMs
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
echo "Testing install.sh on SwarmCracker cluster"
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

# Step 3: Run init on manager
echo "📋 Step 3: Initializing cluster on manager..."
ssh_manager "chmod +x /tmp/install.sh && sudo /tmp/install.sh init --hostname swarm-manager" 2>&1 | tail -40
echo ""

# Step 4: Get join token
echo "📋 Step 4: Getting join token..."
JOIN_TOKEN=$(ssh_manager "sudo cat /var/lib/swarmkit/join-tokens.txt 2>/dev/null | grep -oP 'SWMTKN-[a-zA-Z0-9-]+' | head -1" || echo "")
if [ -z "$JOIN_TOKEN" ]; then
    echo "⚠️  No join token found, trying alternative method..."
    JOIN_TOKEN=$(ssh_manager "sudo /usr/local/bin/swarmctl --socket /var/lib/swarmkit/swarm.sock cluster inspect default 2>/dev/null | grep -oP 'SWMTKN-[a-zA-Z0-9-]+' | head -1" || echo "")
fi
echo "Join token: ${JOIN_TOKEN:-NOT FOUND}"
echo ""

if [ -z "$JOIN_TOKEN" ]; then
    echo "❌ Failed to get join token. Check manager logs."
    exit 1
fi

# Step 5: Join workers
echo "📋 Step 5: Joining workers to cluster..."
echo "  Joining worker1 ($WORKER1_IP)..."
ssh_worker1 "chmod +x /tmp/install.sh && sudo /tmp/install.sh join $MANAGER_IP:4242 --token $JOIN_TOKEN --hostname swarm-worker-1" 2>&1 | tail -30
echo ""
echo "  Joining worker2 ($WORKER2_IP)..."
ssh_worker2 "chmod +x /tmp/install.sh && sudo /tmp/install.sh join $MANAGER_IP:4242 --token $JOIN_TOKEN --hostname swarm-worker-2" 2>&1 | tail -30
echo ""

# Step 6: Verify cluster
echo "📋 Step 6: Verifying cluster status..."
sleep 5
ssh_manager "sudo /usr/local/bin/swarmctl --socket /var/lib/swarmkit/swarm.sock node ls 2>/dev/null" || echo "Failed to list nodes"
echo ""

echo "========================================"
echo "✅ Test complete!"
echo "========================================"