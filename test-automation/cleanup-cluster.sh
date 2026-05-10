#!/bin/bash
# cleanup-cluster.sh - Deep cleanup of all cluster state

set -e

MANAGER_IP="192.168.121.155"
WORKER1_IP="192.168.121.129"
WORKER2_IP="192.168.121.43"

KEY_MANAGER=".vagrant/machines/manager/libvirt/private_key"
KEY_WORKER1=".vagrant/machines/worker1/libvirt/private_key"
KEY_WORKER2=".vagrant/machines/worker2/libvirt/private_key"

SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -o LogLevel=ERROR"

ssh_manager_sudo() { ssh $SSH_OPTS -i "$KEY_MANAGER" vagrant@$MANAGER_IP "sudo $@" 2>/dev/null; }
ssh_worker1_sudo() { ssh $SSH_OPTS -i "$KEY_WORKER1" vagrant@$WORKER1_IP "sudo $@" 2>/dev/null; }
ssh_worker2_sudo() { ssh $SSH_OPTS -i "$KEY_WORKER2" vagrant@$WORKER2_IP "sudo $@" 2>/dev/null; }

echo "🧹 Deep Cleanup of SwarmCracker Cluster"
echo "========================================="
echo ""

for node in "manager" "worker1" "worker2"; do
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "Cleaning $node..."
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    
    ssh_func="ssh_${node}_sudo"
    
    # 1. Kill all dnsmasq processes
    echo "  [1/6] Killing dnsmasq..."
    $ssh_func "pkill -9 dnsmasq" || true
    
    # 2. Kill all firecracker processes
    echo "  [2/6] Killing firecracker..."
    $ssh_func "pkill -9 firecracker" || true
    $ssh_func "pkill -9 jailer" || true
    
    # 3. Stop swarmd services
    echo "  [3/6] Stopping swarmd services..."
    $ssh_func "systemctl stop swarmd-manager" 2>/dev/null || true
    $ssh_func "systemctl stop swarmd-worker" 2>/dev/null || true
    $ssh_func "systemctl stop swarmcracker-agent" 2>/dev/null || true
    
    # 4. Clean swarmkit state
    echo "  [4/6] Cleaning swarmkit state..."
    $ssh_func "rm -rf /var/lib/swarmkit/*" || true
    
    # 5. Clean firecracker state
    echo "  [5/6] Cleaning firecracker state..."
    $ssh_func "rm -rf /var/lib/firecracker/*" || true
    $ssh_func "rm -rf /var/run/firecracker/*" || true
    $ssh_func "rm -rf /var/lib/swarmcracker/*" || true
    
    # 6. Clean network interfaces (stale VXLAN)
    echo "  [6/6] Cleaning network interfaces..."
    $ssh_func "ip link delete vxlan100" 2>/dev/null || true
    $ssh_func "ip link delete swarm-br0-vxlan" 2>/dev/null || true
    $ssh_func "ip link delete swarm-br0" 2>/dev/null || true
    $ssh_func "rm -rf /tmp/dnsmasq.pid /tmp/dnsmasq.log" || true
    
    echo "  ✅ $node cleaned"
    echo ""
done

echo "========================================="
echo "✅ All nodes cleaned!"
echo ""
echo "Next steps:"
echo "  1. Re-run Ansible: ansible-playbook playbooks/setup-cluster.yml"
echo "  2. Run E2E tests: ./e2e-test-suite.sh"
echo ""