# Init/Deinit Improvement Plan

**Created:** 2026-04-11
**Status:** In Progress
**Priority:** High

## Overview

Implement missing cluster lifecycle commands and improve existing init/join robustness.

---

## Phase 1: New Commands (Critical)

### 1.1 `swarmcracker leave` - Worker graceful leave

**Purpose:** Remove worker node from cluster gracefully

**Implementation:**
- Create `cmd/swarmcracker/cmd_leave.go`
- Stop running VMs on this node
- Notify manager via SwarmKit API (if available) or just stop service
- Stop systemd service (`swarmcracker-worker`)
- Disable and remove service file
- Clear state directory (optional `--purge` flag)
- Remove from cluster membership

**Flags:**
- `--purge` - Remove all state/config
- `--force` - Force leave even if manager unreachable
- `--keep-network` - Don't remove bridge/TAP devices

---

### 1.2 `swarmcracker deinit` - Manager teardown

**Purpose:** Remove manager node and optionally destroy cluster

**Implementation:**
- Create `cmd/swarmcracker/cmd_deinit.go`
- Check if other managers exist (warn if last manager)
- Drain/demote workers if multi-manager
- Stop all running VMs
- Stop systemd service (`swarmcracker-manager`)
- Disable and remove service file
- Clear state directory (optional `--purge` flag)
- Remove bridge network (optional `--cleanup-network` flag)

**Flags:**
- `--purge` - Remove all state/config/rootfs
- `--force` - Force deinit without draining
- `--cleanup-network` - Remove bridge and TAP devices
- `--keep-tokens` - Preserve join tokens file

---

### 1.3 `swarmcracker reset` - Full cleanup

**Purpose:** Complete reset for reinstall (nuclear option)

**Implementation:**
- Create `cmd/swarmcracker/cmd_reset.go`
- Kill all Firecracker processes
- Remove all TAP devices
- Remove bridge network
- Stop both manager and worker services
- Remove all systemd service files
- Clear all state directories
- Remove config files
- `--hard` also removes rootfs images and binaries

**Flags:**
- `--hard` - Remove everything including binaries
- `--keep-config` - Preserve /etc/swarmcracker configs
- `--keep-rootfs` - Preserve rootfs images

---

## Phase 2: Init Improvements

### 2.1 Idempotency Check

**Issue:** Re-running init on existing manager creates conflicts

**Fix:**
- Check if `swarmcracker-manager.service` already exists and is active
- Check if `/var/lib/swarmkit` has existing state
- Prompt: "Manager already initialized. Run 'swarmcracker deinit' first or use '--force' to overwrite"
- `--force` flag to skip idempotency check

### 2.2 Rollback on Failure

**Issue:** Partial state left if init fails mid-way

**Fix:**
- Wrap init steps in defer cleanup
- If service start fails, remove service file and configs
- Log cleanup actions for debugging

### 2.3 Service Health Check

**Issue:** Fixed 5-second sleep for "manager ready"

**Fix:**
- Poll `systemctl is-active` with timeout
- Poll SwarmKit socket readiness (`/var/run/swarmkit/swarm.sock`)
- Max timeout configurable via `--ready-timeout` flag

### 2.4 Binary Path Detection

**Issue:** Hardcoded `/usr/local/bin/swarmd-firecracker`

**Fix:**
- Check multiple paths: `/usr/local/bin`, `/usr/bin`, `PATH`
- Use `exec.LookPath("swarmd-firecracker")`
- `--binary-path` flag for custom location

---

## Phase 3: Join Improvements

### 3.1 Token Format Validation

**Issue:** No validation of join token format

**Fix:**
- Validate token starts with `SWMTKN-1-`
- Check token structure: `SWMTKN-1-{role}-{hash}-{secret}`
- Error message: "Invalid token format. Expected: SWMTKN-1-..."

### 3.2 Manager API Validation

**Issue:** Only TCP connectivity checked

**Fix:**
- HTTP/REST API call to manager health endpoint
- Verify manager is SwarmCracker (not generic SwarmKit)
- Retry with backoff for transient failures

### 3.3 Role Token Match

**Issue:** `--manager` flag doesn't verify token is manager token

**Fix:**
- Parse token role component
- If `--manager` flag, verify token contains manager role
- Warn: "Token appears to be worker token but --manager specified"

---

## Implementation Order

1. ✅ `leave` command (most needed)
2. ✅ `deinit` command (manager teardown)
3. ✅ `reset` command (full cleanup)
4. ✅ Init idempotency check
5. ✅ Init rollback on failure
6. ✅ Join token validation
7. ⬜ Service health check (optional)
8. ⬜ Binary path detection (optional)
9. ⬜ Manager API validation (optional)

---

## Files to Create/Modify

| File | Action | Description |
|------|--------|-------------|
| `cmd/swarmcracker/cmd_leave.go` | Create | Leave command implementation |
| `cmd/swarmcracker/cmd_deinit.go` | Create | Deinit command implementation |
| `cmd/swarmcracker/cmd_reset.go` | Create | Reset command implementation |
| `cmd/swarmcracker/cmd_init.go` | Modify | Add idempotency, rollback |
| `cmd/swarmcracker/cmd_join.go` | Modify | Add token validation |
| `cmd/swarmcracker/main.go` | Modify | Register new commands |

---

## Progress Tracking

| Task | Status | Date |
|------|--------|------|
| `leave` command | ✅ Done | 2026-04-11 |
| `deinit` command | ✅ Done | 2026-04-11 |
| `reset` command | ✅ Done | 2026-04-11 |
| Init idempotency | ✅ Done | 2026-04-11 |
| Init rollback | ✅ Done | 2026-04-11 |
| Join token validation | ✅ Done | 2026-04-11 |
| Register commands in main.go | ✅ Done | 2026-04-11 |

---

*Update this file as tasks are completed.*