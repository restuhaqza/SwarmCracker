# Snapshot Feature - Test Results & Resolution

## ✅ ROOT CAUSE IDENTIFIED AND FIXED

**Problem:** Wrong HTTP method and endpoint for VM pause operation

**Old (WRONG):**
```bash
curl -X PUT http://localhost/vm/pause -d '{}'
# Error: "Invalid request method and/or path: PUT vm."
```

**New (CORRECT):**
```bash
curl -X PATCH http://localhost/vm -d '{"state": "Paused"}'
# Success: 204 No Content
```

## ✅ TEST RESULTS ON REAL CLUSTER

### Snapshot Creation - WORKING ✅

```yaml
TASK [Pause VM before snapshot (PATCH /vm)]
changed: [swarm-worker-1]

TASK [Create snapshot via Firecracker API (v1.14.0 format)]
changed: [swarm-worker-1]

TASK [Print snapshot result]
msg: "Snapshot result: 
Snapshot created successfully"

TASK [Check snapshot files exist]
vm.state: 15794 bytes      ✅
vm.mem: 268435456 bytes    ✅ (256 MB as configured)
```

### What Works Now

1. ✅ VM starts successfully
2. ✅ VM pauses via `PATCH /vm` with `{"state": "Paused"}`
3. ✅ Snapshot creates successfully
4. ✅ State file created (15 KB)
5. ✅ Memory file created (256 MB)
6. ✅ All integration tests pass

### Restore Issue (Under Investigation) ⚠️

The restore step is failing with exit code 7. This needs investigation:
- Firecracker starts with `--snapshot` flag
- Memory load may be failing
- Need to check restore logs

## 📝 Files Modified

### 1. pkg/snapshot/snapshot.go
Added VM pause/resume support:

```go
// pauseVM pauses the VM via PATCH /vm endpoint (Firecracker v1.14.0+)
func pauseVM(ctx context.Context, socketPath string) error {
    payload := map[string]interface{}{
        "state": "Paused",
    }
    return patchFirecrackerAPI(ctx, socketPath, "/vm", payload)
}

// resumeVM resumes the VM via PATCH /vm endpoint (Firecracker v1.14.0+)
func resumeVM(ctx context.Context, socketPath string) error {
    payload := map[string]interface{}{
        "state": "Resumed",
    }
    return patchFirecrackerAPI(ctx, socketPath, "/vm", payload)
}

// callSnapshotCreate now pauses VM first
func callSnapshotCreate(...) {
    // Pause VM first (required in v1.14.0+)
    if err := pauseVM(ctx, socketPath); err != nil {
        return fmt.Errorf("failed to pause VM: %w", err)
    }
    // ... create snapshot
}
```

### 2. infrastructure/ansible/playbooks/test-snapshot.yml
Updated pause command:

```yaml
- name: Pause VM before snapshot (PATCH /vm)
  shell: |
    curl -s -X PATCH --unix-socket {{ test_socket }} http://localhost/vm \
      -H 'Content-Type: application/json' \
      -d '{"state": "Paused"}'
```

### 3. Documentation Created
- `research/firecracker/SNAPSHOT-API-ANALYSIS.md` - Full API analysis
- `docs/snapshot-test-report.md` - Test report
- `test/integration/SNAPSHOT_TESTS.md` - Test documentation

## 🔍 Firecracker v1.14.x API Changes

| Operation | Endpoint | Method | Body |
|-----------|----------|--------|------|
| Pause VM | `/vm` | PATCH | `{"state": "Paused"}` |
| Resume VM | `/vm` | PATCH | `{"state": "Resumed"}` |
| Create Snapshot | `/snapshot/create` | PUT | `{"snapshot_type": "Full", ...}` |
| Load Snapshot | `/snapshot/load` | PUT | `{"snapshot_path": "...", ...}` |

## 📊 Code Coverage

### Unit Tests
```bash
go test ./pkg/snapshot/...
# PASS - 20+ tests
```

### Integration Tests
```bash
go test -tags=integration ./test/integration/... -run Snapshot
# PASS - All tests
```

### Real Cluster Tests
```bash
ansible-playbook -i inventory/libvirt playbooks/test-snapshot.yml
# PASS - Snapshot creation ✅
# FAIL - Restore (exit code 7, investigating)
```

## 🎯 Next Steps

### Immediate
1. ✅ Snapshot creation - DONE
2. ⏳ Investigate restore failure
3. ⏳ Test full workflow (pause → snapshot → restore → resume)

### Short-term
1. Add restore/resume to SwarmCracker CLI
2. Add snapshot status reporting
3. Test with bootable rootfs (Alpine/Ubuntu)

### Long-term
1. Implement incremental/diff snapshots
2. Add snapshot scheduling
3. Live migration support
4. Web dashboard integration

## 📚 References

- Firecracker Source: https://github.com/firecracker-microvm/firecracker
- Snapshot API: `src/firecracker/src/api_server/request/snapshot.rs`
- Documentation: `docs/snapshotting/snapshot-support.md`
- CHANGELOG: v1.14.0 release notes

---

**Test Date:** 2026-04-07  
**Firecracker Version:** v1.14.0  
**Status:** ✅ Snapshot Creation WORKING, Restore Under Investigation
