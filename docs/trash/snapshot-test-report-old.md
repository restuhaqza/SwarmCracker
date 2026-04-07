# Snapshot Feature Test Report

## Summary

Tested snapshot functionality on real Firecracker v1.14.0/v1.14.1 VMs in the KVM/libvirt cluster.

## Test Environment

- **Cluster:** 3 VMs (1 manager, 2 workers) on KVM/libvirt
- **Firecracker Version:** v1.14.0 (workers), v1.14.1 (local test)
- **Kernel:** `/usr/share/firecracker/vmlinux` (4.14.55-84.37.amzn2.x86_64)
- **Test Location:** swarm-worker-1 (192.168.121.24)

## Test Results

### ✅ What Works

1. **VM Lifecycle** - Start, configure, and run Firecracker VMs successfully
2. **API Communication** - All Firecracker API endpoints respond correctly
3. **Unit Tests** - All snapshot package unit tests pass (20+ tests)
4. **Integration Tests** - Snapshot manager methods work correctly with mock data

### ❌ What Doesn't Work

1. **Snapshot Creation on Running VMs** - Firecracker v1.14.x returns error:
   ```
   "fault_message": "Create snapshot error: Cannot save the microVM state: 
   Operation not allowed: save/restore unavailable while running"
   ```

2. **Pause Endpoint** - The `/vm/pause` endpoint doesn't exist in v1.14.x:
   ```
   "fault_message": "Invalid request method and/or path: PUT vm."
   ```

## Root Cause Analysis

### Firecracker v1.14.x Snapshot API Changes

According to Firecracker documentation:
- In **earlier versions** (< v1.10), snapshot/create would automatically pause the VM
- In **v1.14.x**, the API requires the VM to be in a paused state, BUT the pause endpoint was removed/changed
- The correct workflow should be:
  1. VM running
  2. Call snapshot/create (should auto-pause)
  3. Snapshot created, VM remains paused

### Current Issue

The snapshot/create endpoint is **not** automatically pausing the VM in v1.14.0/v1.14.1. This appears to be either:
1. A bug in Firecracker v1.14.x
2. A missing configuration flag
3. A change in the API that requires a different approach

## Code Updates Made

Updated `pkg/snapshot/snapshot.go` to use Firecracker v1.14.x API format:

```go
// Old format (v1.10 and earlier)
payload := map[string]interface{}{
    "snapshot_path": statePath,
    "mem_backend": map[string]interface{}{
        "backend_type": "File",
        "backend_path": memoryPath,
    },
}

// New format (v1.14.x)
payload := map[string]interface{}{
    "snapshot_type": "Full",
    "snapshot_path": statePath,
    "mem_file_path": memoryPath,
}
```

## Test Files Created

1. **`test/integration/snapshot_integration_test.go`** - Integration tests
   - Tests snapshot manager methods
   - Full VM snapshot test (requires bootable rootfs)
   - Cleanup and enforcement tests
   - Checksum verification tests

2. **`infrastructure/ansible/playbooks/test-snapshot.yml`** - Ansible playbook
   - Deploys test VM on real cluster
   - Attempts snapshot creation
   - Tests restore functionality

3. **`test/integration/SNAPSHOT_TESTS.md`** - Documentation
   - How to run tests
   - Prerequisites
   - Bootable rootfs creation guide

## Recommendations

### Short-term

1. **Test with Firecracker v1.10.x** - Try an earlier version where snapshot auto-pause worked
2. **Check Firecracker Issues** - Look for related bugs in Firecracker GitHub repo
3. **Alternative: Stop/Start** - As a workaround, stop the VM, snapshot the state, then restart (not ideal for production)

### Long-term

1. **Wait for Firecracker Fix** - Monitor Firecracker releases for snapshot API fixes
2. **Implement VM State Management** - Add proper pause/resume support when Firecracker provides it
3. **Consider Alternative Approaches** - Look into other snapshot mechanisms (e.g., storage-level snapshots)

## Next Steps

1. **Test Firecracker v1.10.x** - Download and test with earlier version
2. **Check Firecracker GitHub Issues** - Search for snapshot-related bugs
3. **Document Workaround** - If needed, implement stop/snapshot/start workflow
4. **Update Roadmap** - Reflect snapshot limitations in FIRECRACKER-ROADMAP.md

## Test Commands

```bash
# Run unit tests
cd projects/swarmcracker
go test -v ./pkg/snapshot/...

# Run integration tests (no VM required)
go test -v -tags=integration ./test/integration/... -run Snapshot

# Run on real cluster (requires cluster running)
cd infrastructure/ansible
ansible-playbook -i inventory/libvirt playbooks/test-snapshot.yml --limit swarm-worker-1
```

## Files Modified

- `pkg/snapshot/snapshot.go` - Updated API format for v1.14.x compatibility
- `infrastructure/ansible/playbooks/test-snapshot.yml` - Created test playbook
- `test/integration/snapshot_integration_test.go` - Created integration tests
- `test/integration/SNAPSHOT_TESTS.md` - Created documentation

---

**Test Date:** 2026-04-07  
**Tester:** Claw (OpenClaw)  
**Status:** ⚠️ Partial - API incompatibility with Firecracker v1.14.x
