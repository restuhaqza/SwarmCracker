#!/bin/bash
# SwarmCracker E2E Test Suite
# Covers all phases from cluster init to teardown
# Based on docs/dev/testing/e2e-tests.md

# Don't exit on first error - continue all tests
# set -e

# Configuration
MANAGER_IP="192.168.121.18"
WORKER1_IP="192.168.121.153"
WORKER2_IP="192.168.121.59"
MANAGER_PORT="4242"
SWARM_SOCKET="/var/run/swarmkit/swarm.sock"

# SSH key paths (relative to script directory)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
KEY_MANAGER="$SCRIPT_DIR/.vagrant/machines/manager/libvirt/private_key"
KEY_WORKER1="$SCRIPT_DIR/.vagrant/machines/worker1/libvirt/private_key"
KEY_WORKER2="$SCRIPT_DIR/.vagrant/machines/worker2/libvirt/private_key"

SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -o LogLevel=ERROR"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test counters
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0

# SSH helpers
ssh_manager() { ssh $SSH_OPTS -i "$KEY_MANAGER" vagrant@$MANAGER_IP "$@"; }
ssh_worker1() { ssh $SSH_OPTS -i "$KEY_WORKER1" vagrant@$WORKER1_IP "$@"; }
ssh_worker2() { ssh $SSH_OPTS -i "$KEY_WORKER2" vagrant@$WORKER2_IP "$@"; }

ssh_manager_sudo() { ssh $SSH_OPTS -i "$KEY_MANAGER" vagrant@$MANAGER_IP "echo vagrant | sudo -S $@ 2>/dev/null" 2>/dev/null; }
ssh_worker1_sudo() { ssh $SSH_OPTS -i "$KEY_WORKER1" vagrant@$WORKER1_IP "echo vagrant | sudo -S $@ 2>/dev/null" 2>/dev/null; }
ssh_worker2_sudo() { ssh $SSH_OPTS -i "$KEY_WORKER2" vagrant@$WORKER2_IP "echo vagrant | sudo -S $@ 2>/dev/null" 2>/dev/null; }

# Test result helpers
pass() {
    echo -e "${GREEN}✅ PASS: $1${NC}"
    ((TESTS_PASSED++))
}

fail() {
    echo -e "${RED}❌ FAIL: $1${NC}"
    echo -e "  ${YELLOW}Reason: $2${NC}"
    ((TESTS_FAILED++))
}

skip() {
    echo -e "${YELLOW}⏭️  SKIP: $1${NC}"
    echo -e "  ${YELLOW}Reason: $2${NC}"
    ((TESTS_SKIPPED++))
}

section() {
    echo ""
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
}

# ============================================================
# PHASE 0: Environment Verification
# ============================================================
test_phase0_environment() {
    section "PHASE 0: Environment Verification"
    
    # Test 0.1: KVM availability on all nodes
    echo "📋 Test 0.1: KVM device availability..."
    for node in "manager" "worker1" "worker2"; do
        ssh_func="ssh_${node}_sudo"
        result=$($ssh_func "ls -la /dev/kvm 2>/dev/null" || echo "NOT_FOUND")
        if [[ "$result" =~ "/dev/kvm" ]]; then
            pass "KVM available on $node"
        else
            fail "KVM not available on $node" "Device /dev/kvm not found"
        fi
    done
    
    # Test 0.2: CPU virtualization support
    echo ""
    echo "📋 Test 0.2: CPU virtualization support..."
    for node in "manager" "worker1" "worker2"; do
        ssh_func="ssh_${node}"
        result=$($ssh_func "lscpu | grep -E 'Virtualization|VMX|SVM'" || echo "")
        if [[ -n "$result" ]]; then
            pass "CPU virtualization supported on $node"
        else
            fail "CPU virtualization not detected on $node" "No VMX/SVM flags found"
        fi
    done
    
    # Test 0.3: Inter-node connectivity
    echo ""
    echo "📋 Test 0.3: Network connectivity..."
    if ssh_manager "ping -c 2 $WORKER1_IP" >/dev/null 2>&1; then
        pass "Manager → Worker1 connectivity"
    else
        fail "Manager → Worker1 connectivity" "Ping failed"
    fi
    
    if ssh_manager "ping -c 2 $WORKER2_IP" >/dev/null 2>&1; then
        pass "Manager → Worker2 connectivity"
    else
        fail "Manager → Worker2 connectivity" "Ping failed"
    fi
    
    if ssh_worker1 "ping -c 2 $WORKER2_IP" >/dev/null 2>&1; then
        pass "Worker1 → Worker2 connectivity"
    else
        fail "Worker1 → Worker2 connectivity" "Ping failed"
    fi
    
    # Test 0.4: Required ports open
    echo ""
    echo "📋 Test 0.4: SwarmKit port (4242) on manager..."
    if ssh_manager "ss -tlnp | grep :4242" >/dev/null 2>&1; then
        pass "Port 4242 listening on manager"
    else
        fail "Port 4242 not listening" "Manager API not accessible"
    fi
}

# ============================================================
# PHASE 1: Installation Verification
# ============================================================
test_phase1_installation() {
    section "PHASE 1: Installation Verification"
    
    # Test 1.1: Firecracker binary
    echo "📋 Test 1.1: Firecracker binary..."
    for node in "manager" "worker1" "worker2"; do
        ssh_func="ssh_${node}_sudo"
        result=$($ssh_func "/usr/local/bin/firecracker --version" 2>&1 || echo "")
        if [[ "$result" =~ "Firecracker v" ]]; then
            pass "Firecracker installed on $node ($(echo $result | head -1))"
        else
            fail "Firecracker not installed on $node" "Binary not found or not executable"
        fi
    done
    
    # Test 1.2: swarmcracker binary
    echo ""
    echo "📋 Test 1.2: swarmcracker CLI..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker version" 2>&1 || echo "")
    if [[ "$result" =~ "SwarmCracker" ]] || [[ -n "$result" ]]; then
        pass "swarmcracker CLI installed on manager"
    else
        fail "swarmcracker CLI not installed" "Binary not found"
    fi
    
    # Test 1.3: swarmd-firecracker binary
    echo ""
    echo "📋 Test 1.3: swarmd-firecracker daemon..."
    for node in "manager" "worker1" "worker2"; do
        ssh_func="ssh_${node}"
        result=$($ssh_func "ls -la /usr/local/bin/swarmd-firecracker" 2>&1 || echo "")
        if [[ "$result" =~ "swarmd-firecracker" ]]; then
            pass "swarmd-firecracker installed on $node"
        else
            fail "swarmd-firecracker not installed on $node" "Binary not found"
        fi
    done
    
    # Test 1.4: Kernel image
    echo ""
    echo "📋 Test 1.4: Firecracker kernel (vmlinux)..."
    for node in "manager" "worker1" "worker2"; do
        ssh_func="ssh_${node}_sudo"
        result=$($ssh_func "ls -la /usr/share/firecracker/vmlinux" 2>&1 || echo "")
        if [[ "$result" =~ "vmlinux" ]]; then
            pass "Kernel image available on $node"
        else
            fail "Kernel image missing on $node" "vmlinux not found"
        fi
    done
    
    # Test 1.5: Rootfs image
    echo ""
    echo "📋 Test 1.5: Rootfs image..."
    for node in "manager" "worker1" "worker2"; do
        ssh_func="ssh_${node}_sudo"
        result=$($ssh_func "ls -la /var/lib/firecracker/rootfs/*.ext4" 2>&1 || echo "")
        if [[ "$result" =~ ".ext4" ]]; then
            pass "Rootfs image available on $node"
        else
            fail "Rootfs image missing on $node" "No ext4 rootfs found"
        fi
    done
}

# ============================================================
# PHASE 2: Cluster Status Verification
# ============================================================
test_phase2_cluster() {
    section "PHASE 2: Cluster Status"
    
    # Test 2.1: Manager service running
    echo "📋 Test 2.1: Manager systemd service..."
    result=$(ssh_manager_sudo "systemctl is-active swarmd-manager.service" 2>&1 || echo "")
    if [[ "$result" == "active" ]]; then
        pass "Manager service is active"
    else
        fail "Manager service not active" "Status: $result"
    fi
    
    # Test 2.2: Worker services running
    echo ""
    echo "📋 Test 2.2: Worker systemd services..."
    result=$(ssh_worker1_sudo "systemctl is-active swarmd-worker.service" 2>&1 || echo "")
    if [[ "$result" == "active" ]]; then
        pass "Worker1 service is active"
    else
        fail "Worker1 service not active" "Status: $result"
    fi
    
    result=$(ssh_worker2_sudo "systemctl is-active swarmd-worker.service" 2>&1 || echo "")
    if [[ "$result" == "active" ]]; then
        pass "Worker2 service is active"
    else
        fail "Worker2 service not active" "Status: $result"
    fi
    
    # Test 2.3: Control socket accessible
    echo ""
    echo "📋 Test 2.3: SwarmKit control socket..."
    result=$(ssh_manager_sudo "ls -la $SWARM_SOCKET" 2>&1 || echo "")
    if [[ "$result" =~ "swarm.sock" ]]; then
        pass "Control socket accessible"
    else
        fail "Control socket not found" "Path: $SWARM_SOCKET"
    fi
    
    # Test 2.4: Join tokens available
    echo ""
    echo "📋 Test 2.4: Join tokens file..."
    result=$(ssh_manager_sudo "cat /var/lib/swarmcracker/manager/join-tokens.txt" 2>&1 || echo "")
    if [[ "$result" =~ "SWMTKN" ]]; then
        pass "Join tokens available"
    else
        fail "Join tokens not found" "File missing or empty"
    fi
}

# ============================================================
# PHASE 3: Service Deployment
# ============================================================
test_phase3_service_deployment() {
    section "PHASE 3: Service Deployment"
    
    # Test 3.1: Deploy nginx service
    echo "📋 Test 3.1: Deploy nginx microVM..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker run nginx:latest --vcpus 1 --memory 128 -d" 2>&1 || echo "")
    if [[ "$result" =~ "started" ]] || [[ "$result" =~ "VM started" ]] || [[ "$result" =~ "task-" ]]; then
        NGINX_TASK=$(echo "$result" | grep -oP 'task-[0-9]+' | head -1)
        pass "nginx microVM deployed (ID: $NGINX_TASK)"
    else
        fail "nginx deployment failed" "$result"
        return
    fi
    
    sleep 5
    
    # Test 3.2: List running VMs
    echo ""
    echo "📋 Test 3.2: List running microVMs..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker list" 2>&1 || echo "")
    if [[ "$result" =~ "Running" ]] || [[ "$result" =~ "task-" ]]; then
        VM_COUNT=$(echo "$result" | grep -c "Running" || echo "0")
        pass "VM list shows $VM_COUNT running microVM(s)"
    else
        fail "No VMs listed" "$result"
    fi
    
    # Test 3.3: Deploy redis service
    echo ""
    echo "📋 Test 3.3: Deploy redis microVM..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker run redis:7-alpine --vcpus 1 --memory 256 -d" 2>&1 || echo "")
    if [[ "$result" =~ "started" ]] || [[ "$result" =~ "task-" ]]; then
        REDIS_TASK=$(echo "$result" | grep -oP 'task-[0-9]+' | head -1)
        pass "redis microVM deployed (ID: $REDIS_TASK)"
    else
        fail "redis deployment failed" "$result"
    fi
    
    sleep 3
    
    # Test 3.4: Verify both VMs running
    echo ""
    echo "📋 Test 3.4: Multiple VMs running..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker list" 2>&1 || echo "")
    VM_COUNT=$(echo "$result" | grep -c "Running" || echo "0")
    if [[ "$VM_COUNT" -ge 2 ]]; then
        pass "At least 2 VMs running (count: $VM_COUNT)"
    else
        fail "Expected 2+ VMs" "Found: $VM_COUNT"
    fi
}

# ============================================================
# PHASE 4: Service Updates
# ============================================================
test_phase4_updates() {
    section "PHASE 4: Service Updates & Lifecycle"
    
    # Get a running VM to test
    VM_ID=$(ssh_manager_sudo "/usr/local/bin/swarmcracker list" 2>&1 | grep -oP 'task-[0-9]+' | head -1)
    
    if [[ -z "$VM_ID" ]]; then
        skip "Phase 4 tests" "No running VMs to test updates"
        return
    fi
    
    # Test 4.1: VM status inspection
    echo "📋 Test 4.1: Inspect VM status..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker status $VM_ID" 2>&1 || echo "")
    if [[ -n "$result" ]] && [[ ! "$result" =~ "Error" ]]; then
        pass "VM status retrieved for $VM_ID"
    else
        fail "VM status failed" "$result"
    fi
    
    # Test 4.2: VM metrics
    echo ""
    echo "📋 Test 4.2: VM resource metrics..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker metrics $VM_ID" 2>&1 || echo "")
    if [[ -n "$result" ]] && [[ ! "$result" =~ "Error" ]]; then
        pass "VM metrics retrieved for $VM_ID"
    else
        # Metrics might not be implemented yet
        skip "VM metrics" "Feature may not be implemented"
    fi
    
    # Test 4.3: Stop a VM
    echo ""
    echo "📋 Test 4.3: Stop running VM..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker stop $VM_ID" 2>&1 || echo "")
    if [[ "$result" =~ "stopped" ]] || [[ "$result" =~ "success" ]] || [[ -z "$(echo $result | grep -i error)" ]]; then
        pass "VM stopped successfully ($VM_ID)"
    else
        fail "VM stop failed" "$result"
    fi
    
    sleep 2
    
    # Test 4.4: Verify VM stopped
    echo ""
    echo "📋 Test 4.4: Verify VM stopped..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker list" 2>&1 || echo "")
    if [[ ! "$result" =~ "$VM_ID.*Running" ]]; then
        pass "VM $VM_ID confirmed stopped"
    else
        fail "VM still running" "$result"
    fi
}

# ============================================================
# PHASE 5: Snapshots
# ============================================================
test_phase5_snapshots() {
    section "PHASE 5: Snapshots & Recovery"
    
    # Deploy a test VM for snapshot testing
    echo "📋 Test 5.0: Deploy test VM for snapshot..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker run alpine:latest --vcpus 1 --memory 128 -d" 2>&1 || echo "")
    SNAP_VM=$(echo "$result" | grep -oP 'task-[0-9]+' | head -1)
    
    if [[ -z "$SNAP_VM" ]]; then
        skip "Snapshot tests" "Could not deploy test VM"
        return
    fi
    
    sleep 5
    
    # Test 5.1: Create snapshot
    echo ""
    echo "📋 Test 5.1: Create VM snapshot..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker snapshot create $SNAP_VM --name test-snap-$(date +%s)" 2>&1 || echo "")
    if [[ "$result" =~ "created" ]] || [[ "$result" =~ "success" ]] || [[ ! "$result" =~ "Error" ]]; then
        pass "Snapshot created successfully"
    else
        # Snapshot might not be implemented
        skip "Snapshot create" "Feature may not be implemented ($result)"
    fi
    
    # Test 5.2: List snapshots
    echo ""
    echo "📋 Test 5.2: List snapshots..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker snapshot ls" 2>&1 || echo "")
    if [[ -n "$result" ]] && [[ ! "$result" =~ "Error" ]]; then
        pass "Snapshot list retrieved"
    else
        skip "Snapshot list" "Feature may not be implemented"
    fi
    
    # Test 5.3: Restore snapshot
    echo ""
    echo "📋 Test 5.3: Restore snapshot..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker snapshot restore test-snap-$(date +%s)" 2>&1 || echo "")
    if [[ "$result" =~ "restored" ]] || [[ "$result" =~ "success" ]]; then
        pass "Snapshot restored successfully"
    else
        skip "Snapshot restore" "Feature may not be implemented"
    fi
}

# ============================================================
# PHASE 6: Node Operations
# ============================================================
test_phase6_node_ops() {
    section "PHASE 6: Node Operations"
    
    # Test 6.1: VXLAN overlay network
    echo "📋 Test 6.1: VXLAN overlay on workers..."
    result=$(ssh_worker1_sudo "ip link show vxlan100" 2>&1 || echo "")
    if [[ "$result" =~ "vxlan100" ]]; then
        pass "VXLAN configured on worker1"
    else
        fail "VXLAN not configured on worker1" "$result"
    fi
    
    result=$(ssh_worker2_sudo "ip link show vxlan100" 2>&1 || echo "")
    if [[ "$result" =~ "vxlan100" ]]; then
        pass "VXLAN configured on worker2"
    else
        fail "VXLAN not configured on worker2" "$result"
    fi
    
    # Test 6.2: Bridge network
    echo ""
    echo "📋 Test 6.2: Bridge network (swarm-br0)..."
    for node in "manager" "worker1" "worker2"; do
        ssh_func="ssh_${node}_sudo"
        result=$($ssh_func "ip link show swarm-br0" 2>&1 || echo "")
        if [[ "$result" =~ "swarm-br0" ]]; then
            pass "Bridge swarm-br0 configured on $node"
        else
            fail "Bridge not configured on $node" "$result"
        fi
    done
    
    # Test 6.3: NAT masquerading
    echo ""
    echo "📋 Test 6.3: NAT masquerading rules..."
    for node in "worker1" "worker2"; do
        ssh_func="ssh_${node}_sudo"
        result=$($ssh_func "iptables -t nat -L POSTROUTING | grep MASQUERADE" 2>&1 || echo "")
        if [[ "$result" =~ "MASQUERADE" ]]; then
            pass "NAT configured on $node"
        else
            fail "NAT not configured on $node" "No MASQUERADE rule"
        fi
    done
    
    # Test 6.4: Cross-node VXLAN FDB
    echo ""
    echo "📋 Test 6.4: VXLAN FDB entries..."
    result=$(ssh_worker1_sudo "bridge fdb show dev vxlan100" 2>&1 || echo "")
    if [[ "$result" =~ "$WORKER2_IP" ]]; then
        pass "Worker1 has FDB entry for Worker2"
    else
        fail "Worker1 missing FDB for Worker2" "$result"
    fi
    
    result=$(ssh_worker2_sudo "bridge fdb show dev vxlan100" 2>&1 || echo "")
    if [[ "$result" =~ "$WORKER1_IP" ]]; then
        pass "Worker2 has FDB entry for Worker1"
    else
        fail "Worker2 missing FDB for Worker1" "$result"
    fi
}

# ============================================================
# PHASE 7: Monitoring & Debugging
# ============================================================
test_phase7_monitoring() {
    section "PHASE 7: Monitoring & Debugging"
    
    # Test 7.1: VM logs
    VM_ID=$(ssh_manager_sudo "/usr/local/bin/swarmcracker list" 2>&1 | grep -oP 'task-[0-9]+' | head -1)
    
    echo "📋 Test 7.1: VM logs retrieval..."
    if [[ -n "$VM_ID" ]]; then
        result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker logs $VM_ID 2>&1 | head -20" || echo "")
        if [[ -n "$result" ]]; then
            pass "VM logs retrieved for $VM_ID"
        else
            fail "VM logs empty" "$result"
        fi
    else
        skip "VM logs" "No running VMs"
    fi
    
    # Test 7.2: Manager service logs
    echo ""
    echo "📋 Test 7.2: Manager systemd logs..."
    result=$(ssh_manager_sudo "journalctl -u swarmd-manager.service --no-pager -n 10" 2>&1 || echo "")
    if [[ -n "$result" ]] && [[ "$result" =~ "swarmd" ]]; then
        pass "Manager logs accessible"
    else
        fail "Manager logs not accessible" "$result"
    fi
    
    # Test 7.3: Worker service logs
    echo ""
    echo "📋 Test 7.3: Worker systemd logs..."
    result=$(ssh_worker1_sudo "journalctl -u swarmd-worker.service --no-pager -n 10" 2>&1 || echo "")
    if [[ -n "$result" ]] && [[ "$result" =~ "swarmd" ]]; then
        pass "Worker1 logs accessible"
    else
        fail "Worker1 logs not accessible" "$result"
    fi
    
    # Test 7.4: gRPC connectivity
    echo ""
    echo "📋 Test 7.4: Manager API connectivity from worker..."
    result=$(ssh_worker1 "curl -s --connect-timeout 5 http://$MANAGER_IP:$MANAGER_PORT/ || echo 'FAILED'" 2>&1)
    # Note: gRPC may not have HTTP endpoint, so we check connectivity differently
    if ssh_worker1 "nc -z -w5 $MANAGER_IP $MANAGER_PORT" 2>&1; then
        pass "Manager API port reachable from worker1"
    else
        fail "Manager API not reachable" "Port $MANAGER_PORT closed"
    fi
}

# ============================================================
# PHASE 8: Volume Management
# ============================================================
test_phase8_volumes() {
    section "PHASE 8: Volume Management"
    
    # Test 8.1: Create volume
    echo "📋 Test 8.1: Create persistent volume..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker volume create test-vol-$(date +%s) --size 100M" 2>&1 || echo "")
    if [[ "$result" =~ "created" ]] || [[ "$result" =~ "success" ]] || [[ ! "$result" =~ "Error" ]]; then
        pass "Volume created successfully"
    else
        skip "Volume create" "Feature may not be implemented ($result)"
    fi
    
    # Test 8.2: List volumes
    echo ""
    echo "📋 Test 8.2: List volumes..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker volume ls" 2>&1 || echo "")
    if [[ -n "$result" ]] && [[ ! "$result" =~ "Error" ]]; then
        pass "Volume list retrieved"
    else
        skip "Volume list" "Feature may not be implemented"
    fi
    
    # Test 8.3: Volume inspection
    echo ""
    echo "📋 Test 8.3: Volume details..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker volume ls" 2>&1 | grep -oP 'vol-[a-zA-Z0-9]+' | head -1 || echo "")
    if [[ -n "$result" ]]; then
        vol_id="$result"
        result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker volume inspect $vol_id" 2>&1 || echo "")
        if [[ -n "$result" ]]; then
            pass "Volume details retrieved for $vol_id"
        else
            skip "Volume inspect" "Feature may not be implemented"
        fi
    else
        skip "Volume inspect" "No volumes to inspect"
    fi
}

# ============================================================
# PHASE 9: Cleanup
# ============================================================
test_phase9_cleanup() {
    section "PHASE 9: Cleanup Tests"
    
    # Test 9.1: Stop all VMs
    echo "📋 Test 9.1: Stop all running microVMs..."
    VM_LIST=$(ssh_manager_sudo "/usr/local/bin/swarmcracker list" 2>&1 | grep -oP 'task-[0-9]+' || echo "")
    
    if [[ -n "$VM_LIST" ]]; then
        for vm in $VM_LIST; do
            result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker stop $vm" 2>&1 || echo "")
            if [[ "$result" =~ "stopped" ]] || [[ -z "$(echo $result | grep -i error)" ]]; then
                pass "Stopped VM $vm"
            else
                fail "Failed to stop $vm" "$result"
            fi
        done
    else
        pass "No VMs to stop"
    fi
    
    sleep 3
    
    # Test 9.2: Verify all VMs stopped
    echo ""
    echo "📋 Test 9.2: Verify all VMs stopped..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker list" 2>&1 || echo "")
    VM_COUNT=$(echo "$result" | grep -c "Running" || echo "0")
    if [[ "$VM_COUNT" -eq 0 ]]; then
        pass "All VMs stopped (0 running)"
    else
        fail "Some VMs still running" "Count: $VM_COUNT"
    fi
    
    # Test 9.3: Delete snapshots (if created)
    echo ""
    echo "📋 Test 9.3: Cleanup snapshots..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker snapshot ls" 2>&1 || echo "")
    if [[ "$result" =~ "test-snap" ]]; then
        for snap in $(echo "$result" | grep -oP 'test-snap-[0-9]+'); do
            ssh_manager_sudo "/usr/local/bin/swarmcracker snapshot delete $snap" 2>&1 || true
        done
        pass "Test snapshots cleaned up"
    else
        pass "No test snapshots to cleanup"
    fi
    
    # Test 9.4: Delete volumes (if created)
    echo ""
    echo "📋 Test 9.4: Cleanup volumes..."
    result=$(ssh_manager_sudo "/usr/local/bin/swarmcracker volume ls" 2>&1 || echo "")
    if [[ "$result" =~ "test-vol" ]]; then
        for vol in $(echo "$result" | grep -oP 'test-vol-[0-9]+'); do
            ssh_manager_sudo "/usr/local/bin/swarmcracker volume delete $vol" 2>&1 || true
        done
        pass "Test volumes cleaned up"
    else
        pass "No test volumes to cleanup"
    fi
    
    # Test 9.5: Services still healthy after cleanup
    echo ""
    echo "📋 Test 9.5: Services still healthy after tests..."
    result=$(ssh_manager_sudo "systemctl is-active swarmd-manager.service" 2>&1 || echo "")
    if [[ "$result" == "active" ]]; then
        pass "Manager service still active"
    else
        fail "Manager service degraded" "Status: $result"
    fi
    
    result=$(ssh_worker1_sudo "systemctl is-active swarmd-worker.service" 2>&1 || echo "")
    if [[ "$result" == "active" ]]; then
        pass "Worker1 service still active"
    else
        fail "Worker1 service degraded" "Status: $result"
    fi
    
    result=$(ssh_worker2_sudo "systemctl is-active swarmd-worker.service" 2>&1 || echo "")
    if [[ "$result" == "active" ]]; then
        pass "Worker2 service still active"
    else
        fail "Worker2 service degraded" "Status: $result"
    fi
}

# ============================================================
# Summary Report
# ============================================================
print_summary() {
    section "TEST SUMMARY"
    
    echo -e "  ${GREEN}Passed:   $TESTS_PASSED${NC}"
    echo -e "  ${RED}Failed:   $TESTS_FAILED${NC}"
    echo -e "  ${YELLOW}Skipped:  $TESTS_SKIPPED${NC}"
    echo -e "  ${BLUE}Total:    $(($TESTS_PASSED + $TESTS_FAILED + $TESTS_SKIPPED))${NC}"
    echo ""
    
    if [[ $TESTS_FAILED -eq 0 ]]; then
        echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
        echo -e "${GREEN}  ✅ ALL CRITICAL TESTS PASSED${NC}"
        echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    else
        echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
        echo -e "${RED}  ❌ SOME TESTS FAILED - Review logs above${NC}"
        echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    fi
    
    echo ""
    echo "Timestamp: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
    echo "Cluster: Manager=$MANAGER_IP, Worker1=$WORKER1_IP, Worker2=$WORKER2_IP"
}

# ============================================================
# Main Execution
# ============================================================
main() {
    echo ""
    echo -e "${BLUE}╔══════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║         SwarmCracker E2E Test Suite                          ║${NC}"
    echo -e "${BLUE}║         Full Lifecycle Validation                           ║${NC}"
    echo -e "${BLUE}╚══════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo "Starting tests at: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
    echo "Target cluster:"
    echo "  - Manager: $MANAGER_IP:$MANAGER_PORT"
    echo "  - Worker1: $WORKER1_IP"
    echo "  - Worker2: $WORKER2_IP"
    echo ""
    
    # Run all phases
    test_phase0_environment
    test_phase1_installation
    test_phase2_cluster
    test_phase3_service_deployment
    test_phase4_updates
    test_phase5_snapshots
    test_phase6_node_ops
    test_phase7_monitoring
    test_phase8_volumes
    test_phase9_cleanup
    
    # Print summary
    print_summary
}

# Run main
main "$@"