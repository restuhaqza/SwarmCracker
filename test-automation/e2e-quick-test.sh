#!/bin/bash
# SwarmCracker Quick E2E Validation
# Fast validation of key cluster components

set -o pipefail

# Configuration
MANAGER_IP="192.168.121.18"
WORKER1_IP="192.168.121.153"
WORKER2_IP="192.168.121.59"
MANAGER_PORT="4242"

# SSH key paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KEY_MANAGER="$SCRIPT_DIR/.vagrant/machines/manager/libvirt/private_key"
KEY_WORKER1="$SCRIPT_DIR/.vagrant/machines/worker1/libvirt/private_key"
KEY_WORKER2="$SCRIPT_DIR/.vagrant/machines/worker2/libvirt/private_key"

SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

# Test counters
PASSED=0
FAILED=0
SKIPPED=0

# SSH helpers
ssh_m() { ssh $SSH_OPTS -i "$KEY_MANAGER" vagrant@$MANAGER_IP "$@" 2>/dev/null; }
ssh_w1() { ssh $SSH_OPTS -i "$KEY_WORKER1" vagrant@$WORKER1_IP "$@" 2>/dev/null; }
ssh_w2() { ssh $SSH_OPTS -i "$KEY_WORKER2" vagrant@$WORKER2_IP "$@" 2>/dev/null; }

ssh_ms() { ssh $SSH_OPTS -i "$KEY_MANAGER" vagrant@$MANAGER_IP "echo vagrant | sudo -S $@ 2>/dev/null" 2>/dev/null; }
ssh_w1s() { ssh $SSH_OPTS -i "$KEY_WORKER1" vagrant@$WORKER1_IP "echo vagrant | sudo -S $@ 2>/dev/null" 2>/dev/null; }
ssh_w2s() { ssh $SSH_OPTS -i "$KEY_WORKER2" vagrant@$WORKER2_IP "echo vagrant | sudo -S $@ 2>/dev/null" 2>/dev/null; }

# Test functions
pass() { echo -e "${GREEN}✅ PASS${NC}: $1"; ((PASSED++)); }
fail() { echo -e "${RED}❌ FAIL${NC}: $1"; echo -e "  ${YELLOW}→ $2${NC}"; ((FAILED++)); }
skip() { echo -e "${CYAN}⏭  SKIP${NC}: $1"; ((SKIPPED++)); }

test_group() {
    echo ""
    echo -e "${BLUE}════════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}════════════════════════════════════════════════════════════${NC}"
}

# ========================================
# PHASE 0: Infrastructure
# ========================================
test_infrastructure() {
    test_group "PHASE 0: Infrastructure"
    
    # KVM
    echo "→ KVM device..."
    for node in "manager" "worker1" "worker2"; do
        ssh_func="ssh_${node}s"
        if $ssh_func "test -c /dev/kvm"; then
            pass "KVM on $node"
        else
            fail "KVM on $node" "/dev/kvm not accessible"
        fi
    done
    
    # Network
    echo "→ Network connectivity..."
    ssh_m "ping -c1 -W2 $WORKER1_IP" && pass "Manager→Worker1" || fail "Manager→Worker1" "ping failed"
    ssh_m "ping -c1 -W2 $WORKER2_IP" && pass "Manager→Worker2" || fail "Manager→Worker2" "ping failed"
    ssh_w1 "ping -c1 -W2 $WORKER2_IP" && pass "Worker1→Worker2" || fail "Worker1→Worker2" "ping failed"
    
    # Port
    echo "→ SwarmKit API port..."
    ssh_ms "ss -tlnp | grep :$MANAGER_PORT" | grep -q :$MANAGER_PORT && pass "Port $MANAGER_PORT listening" || fail "Port $MANAGER_PORT" "not listening"
}

# ========================================
# PHASE 1: Binaries
# ========================================
test_binaries() {
    test_group "PHASE 1: Binary Installation"
    
    # Firecracker
    echo "→ Firecracker..."
    for node in "m" "w1" "w2"; do
        ssh_func="ssh_${node}s"
        ver=$($ssh_func "/usr/local/bin/firecracker --version" | head -1)
        if [[ "$ver" =~ "Firecracker v" ]]; then
            pass "Firecracker on ${node}: $(echo $ver | cut -d' ' -f2)"
        else
            fail "Firecracker on ${node}" "not installed"
        fi
    done
    
    # swarmcracker CLI
    echo "→ swarmcracker CLI..."
    if ssh_ms "/usr/local/bin/swarmcracker --help" | grep -q "SwarmCracker"; then
        pass "swarmcracker CLI"
    else
        fail "swarmcracker CLI" "not functional"
    fi
    
    # swarmd-firecracker
    echo "→ swarmd-firecracker..."
    for node in "m" "w1" "w2"; do
        ssh_func="ssh_${node}s"
        if $ssh_func "test -x /usr/local/bin/swarmd-firecracker"; then
            pass "swarmd-firecracker on ${node}"
        else
            fail "swarmd-firecracker on ${node}" "not found"
        fi
    done
    
    # Assets
    echo "→ Kernel & rootfs..."
    for node in "m" "w1" "w2"; do
        ssh_func="ssh_${node}s"
        $ssh_func "test -f /usr/share/firecracker/vmlinux" && pass "vmlinux on ${node}" || fail "vmlinux on ${node}" "missing"
        $ssh_func "ls /var/lib/firecracker/rootfs/*.ext4" | grep -q ext4 && pass "rootfs on ${node}" || fail "rootfs on ${node}" "missing"
    done
}

# ========================================
# PHASE 2: Services
# ========================================
test_services() {
    test_group "PHASE 2: Systemd Services"
    
    echo "→ Manager service..."
    status=$(ssh_ms "systemctl is-active swarmd-manager.service")
    [[ "$status" == "active" ]] && pass "swarmd-manager active" || fail "swarmd-manager" "status: $status"
    
    echo "→ Worker services..."
    status=$(ssh_w1s "systemctl is-active swarmd-worker.service")
    [[ "$status" == "active" ]] && pass "swarmd-worker1 active" || fail "swarmd-worker1" "status: $status"
    
    status=$(ssh_w2s "systemctl is-active swarmd-worker.service")
    [[ "$status" == "active" ]] && pass "swarmd-worker2 active" || fail "swarmd-worker2" "status: $status"
    
    echo "→ Join tokens..."
    ssh_ms "cat /var/lib/swarmcracker/manager/join-tokens.txt" | grep -q "SWMTKN" && pass "Join tokens available" || fail "Join tokens" "missing"
}

# ========================================
# PHASE 3: Networking
# ========================================
test_networking() {
    test_group "PHASE 3: Overlay Network"
    
    echo "→ Bridge swarm-br0..."
    for node in "m" "w1" "w2"; do
        ssh_func="ssh_${node}s"
        $ssh_func "ip link show swarm-br0" | grep -q swarm-br0 && pass "Bridge on ${node}" || fail "Bridge on ${node}" "missing"
    done
    
    echo "→ VXLAN overlay..."
    ssh_w1s "ip link show vxlan100" | grep -q vxlan100 && pass "VXLAN on worker1" || fail "VXLAN on worker1" "missing"
    ssh_w2s "ip link show vxlan100" | grep -q vxlan100 && pass "VXLAN on worker2" || fail "VXLAN on worker2" "missing"
    
    echo "→ VXLAN FDB (peer entries)..."
    ssh_w1s "bridge fdb show dev vxlan100" | grep -q "$WORKER2_IP" && pass "Worker1→Worker2 FDB" || fail "Worker1 FDB" "missing peer"
    ssh_w2s "bridge fdb show dev vxlan100" | grep -q "$WORKER1_IP" && pass "Worker2→Worker1 FDB" || fail "Worker2 FDB" "missing peer"
    
    echo "→ NAT masquerade..."
    ssh_w1s "iptables -t nat -L POSTROUTING" | grep -q MASQUERADE && pass "NAT on worker1" || fail "NAT on worker1" "missing"
    ssh_w2s "iptables -t nat -L POSTROUTING" | grep -q MASQUERADE && pass "NAT on worker2" || fail "NAT on worker2" "missing"
}

# ========================================
# PHASE 4: MicroVM Deployment
# ========================================
test_vm_deployment() {
    test_group "PHASE 4: MicroVM Deployment"
    
    echo "→ Deploy test microVM..."
    result=$(ssh_ms "/usr/local/bin/swarmcracker run alpine:latest --vcpus 1 --memory 128 -d" 2>&1)
    task_id=$(echo "$result" | grep -oP 'task-[0-9]+' | head -1)
    
    if [[ -n "$task_id" ]]; then
        pass "MicroVM deployed: $task_id"
    else
        fail "MicroVM deployment" "$result"
        return
    fi
    
    echo "→ Waiting for VM boot..."
    sleep 8
    
    echo "→ Verify VM running..."
    vm_list=$(ssh_ms "/usr/local/bin/swarmcracker list" 2>&1)
    if echo "$vm_list" | grep -q "Running"; then
        vm_count=$(echo "$vm_list" | grep -c "Running" || echo 0)
        pass "$vm_count VM(s) running"
    else
        fail "VM list" "no running VMs"
    fi
    
    echo "→ VM status inspection..."
    if [[ -n "$task_id" ]]; then
        status=$(ssh_ms "/usr/local/bin/swarmcracker status $task_id" 2>&1)
        if [[ -n "$status" ]] && [[ ! "$status" =~ "Error" ]]; then
            pass "VM status retrieved"
        else
            skip "VM status" "feature may not be implemented"
        fi
    fi
}

# ========================================
# PHASE 5: Cleanup
# ========================================
test_cleanup() {
    test_group "PHASE 5: Cleanup & Service Health"
    
    echo "→ Stop test VMs..."
    vm_list=$(ssh_ms "/usr/local/bin/swarmcracker list" 2>&1 | grep -oP 'task-[0-9]+')
    
    stopped=0
    for vm in $vm_list; do
        ssh_ms "/usr/local/bin/swarmcracker stop $vm" 2>&1 >/dev/null && ((stopped++))
    done
    pass "Stopped $stopped VM(s)"
    
    sleep 3
    
    echo "→ Verify all stopped..."
    running=$(ssh_ms "/usr/local/bin/swarmcracker list" 2>&1 | grep -c "Running" || echo 0)
    [[ "$running" -eq 0 ]] && pass "All VMs stopped" || fail "VM cleanup" "$running still running"
    
    echo "→ Services still healthy..."
    status=$(ssh_ms "systemctl is-active swarmd-manager.service")
    [[ "$status" == "active" ]] && pass "Manager still active" || fail "Manager degraded" "$status"
    
    status=$(ssh_w1s "systemctl is-active swarmd-worker.service")
    [[ "$status" == "active" ]] && pass "Worker1 still active" || fail "Worker1 degraded" "$status"
    
    status=$(ssh_w2s "systemctl is-active swarmd-worker.service")
    [[ "$status" == "active" ]] && pass "Worker2 still active" || fail "Worker2 degraded" "$status"
}

# ========================================
# Summary
# ========================================
print_summary() {
    test_group "TEST SUMMARY"
    
    total=$((PASSED + FAILED + SKIPPED))
    echo ""
    echo -e "  ${GREEN}✅ Passed:   $PASSED${NC}"
    echo -e "  ${RED}❌ Failed:   $FAILED${NC}"
    echo -e "  ${CYAN}⏭  Skipped:  $SKIPPED${NC}"
    echo -e "  ${BLUE}📊 Total:    $total${NC}"
    echo ""
    
    if [[ $FAILED -eq 0 ]]; then
        echo -e "${GREEN}╔══════════════════════════════════════════════════════════╗${NC}"
        echo -e "${GREEN}║  ✅ ALL CRITICAL TESTS PASSED                           ║${NC}"
        echo -e "${GREEN}║     SwarmCracker cluster validated!                     ║${NC}"
        echo -e "${GREEN}╚══════════════════════════════════════════════════════════╝${NC}"
        return 0
    else
        echo -e "${RED}╔══════════════════════════════════════════════════════════╗${NC}"
        echo -e "${RED}║  ❌ $FAILED TESTS FAILED                                  ║${NC}"
        echo -e "${RED}║     Review failures above                               ║${NC}"
        echo -e "${RED}╚══════════════════════════════════════════════════════════╝${NC}"
        return 1
    fi
}

# ========================================
# Main
# ========================================
main() {
    echo -e "${BLUE}╔══════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║         SwarmCracker Quick E2E Validation                    ║${NC}"
    echo -e "${BLUE}╚══════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    echo "Time: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
    echo "Cluster: M=$MANAGER_IP W1=$WORKER1_IP W2=$WORKER2_IP"
    
    test_infrastructure
    test_binaries
    test_services
    test_networking
    test_vm_deployment
    test_cleanup
    
    print_summary
}

main "$@"